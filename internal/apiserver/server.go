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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
	"github.com/alexsjones/sympozium/internal/eventbus"
)

// Server is the Sympozium API server.
type Server struct {
	client   client.Client
	eventBus eventbus.EventBus
	log      logr.Logger
	upgrader websocket.Upgrader
}

// NewServer creates a new API server.
func NewServer(c client.Client, bus eventbus.EventBus, log logr.Logger) *Server {
	return &Server{
		client:   c,
		eventBus: bus,
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
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	BaseURL    string `json:"baseURL,omitempty"`
	SecretName string `json:"secretName,omitempty"`
	PolicyRef  string `json:"policyRef,omitempty"`
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

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: req.InstanceRef + "-",
			Namespace:    ns,
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			InstanceRef: req.InstanceRef,
			AgentID:     req.AgentID,
			SessionKey:  req.SessionKey,
			Task:        req.Task,
		},
	}

	if req.Model != "" {
		run.Spec.Model = sympoziumv1alpha1.ModelSpec{
			Model: req.Model,
		}
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
	Model          string            `json:"model,omitempty"`
	BaseURL        string            `json:"baseURL,omitempty"`
	ChannelConfigs map[string]string `json:"channelConfigs,omitempty"`
	PolicyRef      string            `json:"policyRef,omitempty"`
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

	if req.ChannelConfigs != nil {
		pp.Spec.ChannelConfigs = req.ChannelConfigs
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

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
