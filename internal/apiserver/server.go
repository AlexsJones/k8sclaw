// Package apiserver provides the HTTP + WebSocket API server for Sympozium.
package apiserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
	"github.com/alexsjones/sympozium/internal/eventbus"
)

// Server is the Sympozium API server.
type Server struct {
	client   client.Client
	eventBus eventbus.EventBus
	kube     kubernetes.Interface
	log      logr.Logger
	upgrader websocket.Upgrader
}

// NewServer creates a new API server.
func NewServer(c client.Client, bus eventbus.EventBus, kube kubernetes.Interface, log logr.Logger) *Server {
	return &Server{
		client:   c,
		eventBus: bus,
		kube:     kube,
		log:      log,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// Start starts the HTTP server (headless, no embedded UI).
// When token is non-empty the auth middleware is applied.
func (s *Server) Start(addr, token string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           s.buildMux(nil, token),
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.log.Info("Starting API server", "addr", addr, "auth", token != "")
	return server.ListenAndServe()
}

// StartWithUI starts the HTTP server with an embedded frontend SPA
// and optional bearer-token authentication.
func (s *Server) StartWithUI(addr, token string, frontendFS fs.FS) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           s.buildMux(frontendFS, token),
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.log.Info("Starting API server with UI", "addr", addr)
	return server.ListenAndServe()
}

// buildMux creates the HTTP mux with all API routes.
// When frontendFS is non-nil, it serves the SPA for non-API paths.
// When token is non-empty, API routes require Bearer authentication.
func (s *Server) buildMux(frontendFS fs.FS, token string) http.Handler {
	mux := http.NewServeMux()

	// Instance endpoints
	mux.HandleFunc("GET /api/v1/instances", s.listInstances)
	mux.HandleFunc("GET /api/v1/instances/{name}", s.getInstance)
	mux.HandleFunc("POST /api/v1/instances", s.createInstance)
	mux.HandleFunc("DELETE /api/v1/instances/{name}", s.deleteInstance)

	// Run endpoints
	mux.HandleFunc("GET /api/v1/runs", s.listRuns)
	mux.HandleFunc("GET /api/v1/runs/{name}", s.getRun)
	mux.HandleFunc("POST /api/v1/runs", s.createRun)
	mux.HandleFunc("DELETE /api/v1/runs/{name}", s.deleteRun)

	// Policy endpoints
	mux.HandleFunc("GET /api/v1/policies", s.listPolicies)
	mux.HandleFunc("GET /api/v1/policies/{name}", s.getPolicy)

	// Skill endpoints
	mux.HandleFunc("GET /api/v1/skills", s.listSkills)
	mux.HandleFunc("GET /api/v1/skills/{name}", s.getSkill)

	// Schedule endpoints
	mux.HandleFunc("GET /api/v1/schedules", s.listSchedules)
	mux.HandleFunc("GET /api/v1/schedules/{name}", s.getSchedule)
	mux.HandleFunc("POST /api/v1/schedules", s.createSchedule)
	mux.HandleFunc("DELETE /api/v1/schedules/{name}", s.deleteSchedule)

	// PersonaPack endpoints
	mux.HandleFunc("GET /api/v1/personapacks", s.listPersonaPacks)
	mux.HandleFunc("GET /api/v1/personapacks/{name}", s.getPersonaPack)
	mux.HandleFunc("PATCH /api/v1/personapacks/{name}", s.patchPersonaPack)
	mux.HandleFunc("DELETE /api/v1/personapacks/{name}", s.deletePersonaPack)

	// Namespace listing
	mux.HandleFunc("GET /api/v1/namespaces", s.listNamespaces)
	mux.HandleFunc("GET /api/v1/pods", s.listPods)
	mux.HandleFunc("GET /api/v1/pods/{name}/logs", s.getPodLogs)

	// WebSocket streaming
	mux.HandleFunc("/ws/stream", s.handleStream)

	// Health & metrics
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})
	mux.Handle("/metrics", promhttp.Handler())

	// If a frontend FS is provided, serve it as an SPA fallback.
	if frontendFS != nil {
		mux.HandleFunc("/", s.spaHandler(frontendFS))
	}

	// Wrap with auth middleware if a token is configured.
	if token != "" {
		return authMiddleware(token, mux)
	}

	return mux
}

// authMiddleware returns an http.Handler that checks for a valid Bearer token
// or ?token= query parameter. Health and metrics endpoints are exempted.
func authMiddleware(expectedToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Skip auth for health, metrics, and static assets.
		if path == "/healthz" || path == "/readyz" || path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		// Skip auth for non-API paths (frontend SPA assets).
		if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/ws/") {
			next.ServeHTTP(w, r)
			return
		}

		// Check Authorization header or query param.
		token := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token != expectedToken {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// spaHandler serves the embedded SPA. Known static files are served directly;
// all other paths return index.html for client-side routing.
func (s *Server) spaHandler(frontendFS fs.FS) http.HandlerFunc {
	fileServer := http.FileServer(http.FS(frontendFS))
	return func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/" {
			path = "index.html"
		} else {
			path = strings.TrimPrefix(path, "/")
		}

		// Try to open the file. If it exists, serve it.
		if f, err := frontendFS.Open(path); err == nil {
			f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// Fallback to index.html for SPA routing.
		r.URL.Path = "/"
		fileServer.ServeHTTP(w, r)
	}
}

// --- Instance handlers ---

func (s *Server) listInstances(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list sympoziumv1alpha1.SympoziumInstanceList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, list.Items)
}

func (s *Server) getInstance(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var inst sympoziumv1alpha1.SympoziumInstance
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &inst); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, inst)
}

func (s *Server) deleteInstance(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	inst := &sympoziumv1alpha1.SympoziumInstance{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := s.client.Delete(r.Context(), inst); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// CreateInstanceRequest is the request body for creating a new SympoziumInstance.
type CreateInstanceRequest struct {
	Name           string            `json:"name"`
	Provider       string            `json:"provider"`
	Model          string            `json:"model"`
	BaseURL        string            `json:"baseURL,omitempty"`
	SecretName     string            `json:"secretName,omitempty"`
	PolicyRef      string            `json:"policyRef,omitempty"`
	Skills         []string          `json:"skills,omitempty"`
	Channels       []string          `json:"channels,omitempty"`
	ChannelConfigs map[string]string `json:"channelConfigs,omitempty"`
}

func (s *Server) createInstance(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req CreateInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Provider == "" || req.Model == "" {
		http.Error(w, "name, provider, and model are required", http.StatusBadRequest)
		return
	}

	inst := &sympoziumv1alpha1.SympoziumInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.SympoziumInstanceSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: req.Model,
				},
			},
		},
	}

	if req.BaseURL != "" {
		inst.Spec.Agents.Default.BaseURL = req.BaseURL
	}

	if req.SecretName != "" {
		inst.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{
			{Provider: req.Provider, Secret: req.SecretName},
		}
	}

	if req.PolicyRef != "" {
		inst.Spec.PolicyRef = req.PolicyRef
	}
	if len(req.Skills) > 0 {
		inst.Spec.Skills = make([]sympoziumv1alpha1.SkillRef, 0, len(req.Skills))
		seen := make(map[string]struct{}, len(req.Skills))
		for _, sk := range req.Skills {
			sk = strings.TrimSpace(sk)
			if sk == "" {
				continue
			}
			if _, ok := seen[sk]; ok {
				continue
			}
			seen[sk] = struct{}{}
			inst.Spec.Skills = append(inst.Spec.Skills, sympoziumv1alpha1.SkillRef{
				SkillPackRef: sk,
			})
		}
	}
	if len(req.Channels) > 0 {
		inst.Spec.Channels = make([]sympoziumv1alpha1.ChannelSpec, 0, len(req.Channels))
		seen := make(map[string]struct{}, len(req.Channels))
		for _, ch := range req.Channels {
			ch = strings.TrimSpace(ch)
			if ch == "" {
				continue
			}
			if _, ok := seen[ch]; ok {
				continue
			}
			seen[ch] = struct{}{}
			cs := sympoziumv1alpha1.ChannelSpec{Type: ch}
			if req.ChannelConfigs != nil {
				if secret := strings.TrimSpace(req.ChannelConfigs[ch]); secret != "" {
					cs.ConfigRef = sympoziumv1alpha1.SecretRef{Secret: secret}
				}
			}
			inst.Spec.Channels = append(inst.Spec.Channels, cs)
		}
	}

	if err := s.client.Create(r.Context(), inst); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, inst)
}

// --- Run handlers ---

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list sympoziumv1alpha1.AgentRunList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, list.Items)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var run sympoziumv1alpha1.AgentRun
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &run); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, run)
}

// CreateRunRequest is the request body for creating a new AgentRun.
type CreateRunRequest struct {
	InstanceRef string `json:"instanceRef"`
	Task        string `json:"task"`
	AgentID     string `json:"agentId,omitempty"`
	SessionKey  string `json:"sessionKey,omitempty"`
	Model       string `json:"model,omitempty"`
	Timeout     string `json:"timeout,omitempty"`
}

func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req CreateRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.InstanceRef == "" || req.Task == "" {
		http.Error(w, "instanceRef and task are required", http.StatusBadRequest)
		return
	}

	if req.AgentID == "" {
		req.AgentID = "primary"
	}
	if req.SessionKey == "" {
		req.SessionKey = fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	if req.Timeout == "" {
		req.Timeout = "5m"
	}

	// Look up the SympoziumInstance to inherit auth, model, and skills.
	var inst sympoziumv1alpha1.SympoziumInstance
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: req.InstanceRef, Namespace: ns}, &inst); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, fmt.Sprintf("instance %q not found in namespace %q", req.InstanceRef, ns), http.StatusNotFound)
		} else {
			http.Error(w, "failed to get instance: "+err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Resolve auth secret and provider from instance — first AuthRef wins.
	authSecret := ""
	provider := "openai"
	if len(inst.Spec.AuthRefs) > 0 {
		authSecret = inst.Spec.AuthRefs[0].Secret
		if inst.Spec.AuthRefs[0].Provider != "" {
			provider = inst.Spec.AuthRefs[0].Provider
		}
	}
	if authSecret == "" {
		http.Error(w, fmt.Sprintf("instance %q has no API key configured (authRefs is empty)", req.InstanceRef), http.StatusBadRequest)
		return
	}

	// Use request-supplied model or fall back to the instance default.
	model := req.Model
	if model == "" {
		model = inst.Spec.Agents.Default.Model
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: req.InstanceRef + "-",
			Namespace:    ns,
			Labels: map[string]string{
				"sympozium.ai/instance": req.InstanceRef,
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			InstanceRef: req.InstanceRef,
			AgentID:     req.AgentID,
			SessionKey:  req.SessionKey,
			Task:        req.Task,
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:      provider,
				Model:         model,
				BaseURL:       inst.Spec.Agents.Default.BaseURL,
				AuthSecretRef: authSecret,
			},
			Skills: inst.Spec.Skills,
		},
	}

	if err := s.client.Create(r.Context(), run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, run)
}

func (s *Server) deleteRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := s.client.Delete(r.Context(), run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- Policy handlers ---

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	// Policies are platform-wide shared resources — list across all namespaces.
	var list sympoziumv1alpha1.SympoziumPolicyList
	if err := s.client.List(r.Context(), &list); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, list.Items)
}

func (s *Server) getPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var pol sympoziumv1alpha1.SympoziumPolicy
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &pol); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, pol)
}

// --- Skill handlers ---

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	// SkillPacks are platform-wide shared resources — list across all namespaces.
	var list sympoziumv1alpha1.SkillPackList
	if err := s.client.List(r.Context(), &list); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, list.Items)
}

func (s *Server) getSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var sk sympoziumv1alpha1.SkillPack
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &sk); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, sk)
}

// --- Schedule handlers ---

func (s *Server) listSchedules(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list sympoziumv1alpha1.SympoziumScheduleList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, list.Items)
}

func (s *Server) getSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var sched sympoziumv1alpha1.SympoziumSchedule
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &sched); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, sched)
}

// CreateScheduleRequest is the request body for creating a new SympoziumSchedule.
type CreateScheduleRequest struct {
	// Name is the schedule resource name. If empty, a name is generated from instanceRef.
	Name string `json:"name,omitempty"`
	// InstanceRef is the name of the SympoziumInstance this schedule belongs to.
	InstanceRef string `json:"instanceRef"`
	// Schedule is a cron expression (e.g. "0 * * * *").
	Schedule string `json:"schedule"`
	// Task is the task description sent to the agent on each trigger.
	Task string `json:"task"`
	// Type categorises the schedule: heartbeat, scheduled, or sweep.
	Type string `json:"type,omitempty"`
	// Suspend pauses scheduling when true.
	Suspend bool `json:"suspend,omitempty"`
	// ConcurrencyPolicy controls behaviour when a trigger fires while the previous run is active.
	ConcurrencyPolicy string `json:"concurrencyPolicy,omitempty"`
}

func (s *Server) createSchedule(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req CreateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.InstanceRef == "" || req.Schedule == "" || req.Task == "" {
		http.Error(w, "instanceRef, schedule, and task are required", http.StatusBadRequest)
		return
	}

	sched := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			InstanceRef: req.InstanceRef,
			Schedule:    req.Schedule,
			Task:        req.Task,
			Suspend:     req.Suspend,
		},
	}

	if req.Name != "" {
		sched.ObjectMeta.Name = req.Name
	} else {
		sched.ObjectMeta.GenerateName = req.InstanceRef + "-schedule-"
	}

	if req.Type != "" {
		sched.Spec.Type = req.Type
	}
	if req.ConcurrencyPolicy != "" {
		sched.Spec.ConcurrencyPolicy = req.ConcurrencyPolicy
	}

	if err := s.client.Create(r.Context(), sched); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, sched)
}

func (s *Server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	sched := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := s.client.Delete(r.Context(), sched); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- PersonaPack handlers ---

func (s *Server) listPersonaPacks(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list sympoziumv1alpha1.PersonaPackList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, list.Items)
}

func (s *Server) getPersonaPack(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var pp sympoziumv1alpha1.PersonaPack
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &pp); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, pp)
}

// PatchPersonaPackRequest represents a partial update to a PersonaPack.
type PatchPersonaPackRequest struct {
	Enabled        *bool             `json:"enabled,omitempty"`
	Provider       string            `json:"provider,omitempty"`
	SecretName     string            `json:"secretName,omitempty"`
	APIKey         string            `json:"apiKey,omitempty"`
	Model          string            `json:"model,omitempty"`
	BaseURL        string            `json:"baseURL,omitempty"`
	Channels       []string          `json:"channels,omitempty"`
	ChannelConfigs map[string]string `json:"channelConfigs,omitempty"`
	PolicyRef      string            `json:"policyRef,omitempty"`
	Skills         []string          `json:"skills,omitempty"`
}

func (s *Server) patchPersonaPack(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req PatchPersonaPackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var pp sympoziumv1alpha1.PersonaPack
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &pp); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if req.Enabled != nil {
		pp.Spec.Enabled = *req.Enabled
	}

	// Auto-create a K8s Secret when the user provides a raw API key.
	if req.Provider != "" && req.APIKey != "" && req.SecretName == "" {
		req.SecretName = name + "-credentials"
		envKey := providerEnvKey(req.Provider)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.SecretName,
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "sympozium",
					"sympozium.ai/persona-pack":    name,
				},
			},
			StringData: map[string]string{envKey: req.APIKey},
		}
		createErr := s.client.Create(r.Context(), secret)
		if createErr != nil && !k8serrors.IsAlreadyExists(createErr) {
			http.Error(w, "failed to create credentials secret: "+createErr.Error(), http.StatusInternalServerError)
			return
		}
		if k8serrors.IsAlreadyExists(createErr) {
			// Update the existing secret with the new key.
			existing := &corev1.Secret{}
			if err := s.client.Get(r.Context(), types.NamespacedName{Name: req.SecretName, Namespace: ns}, existing); err != nil {
				http.Error(w, "failed to get existing secret: "+err.Error(), http.StatusInternalServerError)
				return
			}
			if existing.Data == nil {
				existing.Data = map[string][]byte{}
			}
			existing.Data[envKey] = []byte(req.APIKey)
			if err := s.client.Update(r.Context(), existing); err != nil {
				http.Error(w, "failed to update credentials secret: "+err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	if req.Provider != "" && req.SecretName != "" {
		pp.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{
			{Provider: req.Provider, Secret: req.SecretName},
		}
	}

	if req.Model != "" {
		for i := range pp.Spec.Personas {
			pp.Spec.Personas[i].Model = req.Model
		}
	}
	if req.Skills != nil {
		normalized := make([]string, 0, len(req.Skills))
		seen := make(map[string]struct{}, len(req.Skills))
		for _, sk := range req.Skills {
			sk = strings.TrimSpace(sk)
			if sk == "" {
				continue
			}
			if _, ok := seen[sk]; ok {
				continue
			}
			seen[sk] = struct{}{}
			normalized = append(normalized, sk)
		}
		for i := range pp.Spec.Personas {
			pp.Spec.Personas[i].Skills = append([]string(nil), normalized...)
		}
	}

	if req.ChannelConfigs != nil {
		pp.Spec.ChannelConfigs = req.ChannelConfigs
	}
	if req.Channels != nil {
		normalized := make([]string, 0, len(req.Channels))
		seen := make(map[string]struct{}, len(req.Channels))
		for _, ch := range req.Channels {
			ch = strings.TrimSpace(ch)
			if ch == "" {
				continue
			}
			if _, ok := seen[ch]; ok {
				continue
			}
			seen[ch] = struct{}{}
			normalized = append(normalized, ch)
		}
		for i := range pp.Spec.Personas {
			pp.Spec.Personas[i].Channels = append([]string(nil), normalized...)
		}
	}

	if req.PolicyRef != "" {
		pp.Spec.PolicyRef = req.PolicyRef
	}

	if err := s.client.Update(r.Context(), &pp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, pp)
}

func (s *Server) deletePersonaPack(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	pp := &sympoziumv1alpha1.PersonaPack{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := s.client.Delete(r.Context(), pp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// --- WebSocket streaming ---

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if s.eventBus == nil {
		http.Error(w, "streaming not available (no event bus)", http.StatusServiceUnavailable)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.log.Error(err, "failed to upgrade websocket")
		return
	}
	defer conn.Close()

	// Subscribe to agent events
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	events, err := s.eventBus.Subscribe(ctx, eventbus.TopicAgentStreamChunk)
	if err != nil {
		s.log.Error(err, "failed to subscribe to events")
		return
	}

	// Read loop (handle client messages / keep-alive)
	go func() {
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				cancel()
				return
			}
		}
	}()

	// Ping ticker — keeps the connection alive through proxies/port-forwards.
	pingTicker := time.NewTicker(15 * time.Second)
	defer pingTicker.Stop()

	// Write loop (forward events to client)
	for {
		select {
		case <-ctx.Done():
			return
		case <-pingTicker.C:
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case event, ok := <-events:
			if !ok {
				return
			}
			data, _ := json.Marshal(event)
			if err := conn.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}
}

// providerEnvKey returns the environment variable key for a provider's API key.
func providerEnvKey(provider string) string {
	switch provider {
	case "openai":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "azure-openai":
		return "AZURE_OPENAI_API_KEY"
	default:
		return "PROVIDER_API_KEY"
	}
}

func (s *Server) listNamespaces(w http.ResponseWriter, r *http.Request) {
	var nsList corev1.NamespaceList
	if err := s.client.List(r.Context(), &nsList); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	names := make([]string, 0, len(nsList.Items))
	for _, ns := range nsList.Items {
		names = append(names, ns.Name)
	}
	writeJSON(w, names)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// PodInfo is a flattened pod representation for the web UI.
type PodInfo struct {
	Name         string            `json:"name"`
	Namespace    string            `json:"namespace"`
	Phase        string            `json:"phase"`
	NodeName     string            `json:"nodeName,omitempty"`
	PodIP        string            `json:"podIP,omitempty"`
	StartTime    *metav1.Time      `json:"startTime,omitempty"`
	RestartCount int32             `json:"restartCount"`
	InstanceRef  string            `json:"instanceRef,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
}

func (s *Server) listPods(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list corev1.PodList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	out := make([]PodInfo, 0, len(list.Items))
	for _, p := range list.Items {
		var restarts int32
		for _, cs := range p.Status.ContainerStatuses {
			restarts += cs.RestartCount
		}
		inst := ""
		if p.Labels != nil {
			inst = p.Labels["sympozium.ai/instance"]
		}
		out = append(out, PodInfo{
			Name:         p.Name,
			Namespace:    p.Namespace,
			Phase:        string(p.Status.Phase),
			NodeName:     p.Spec.NodeName,
			PodIP:        p.Status.PodIP,
			StartTime:    p.Status.StartTime,
			RestartCount: restarts,
			InstanceRef:  inst,
			Labels:       p.Labels,
		})
	}

	writeJSON(w, out)
}

func (s *Server) getPodLogs(w http.ResponseWriter, r *http.Request) {
	if s.kube == nil {
		http.Error(w, "pod logs unavailable", http.StatusServiceUnavailable)
		return
	}

	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	tail := int64(200)
	req := s.kube.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{TailLines: &tail})
	raw, err := req.Do(r.Context()).Raw()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"logs": string(raw)})
}
