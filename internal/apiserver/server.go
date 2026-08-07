// Package apiserver provides the HTTP + WebSocket API server for Sympozium.
package apiserver

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrock"
	"github.com/go-logr/logr"
	"github.com/gorilla/websocket"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/agentedit"
	"github.com/sympozium-ai/sympozium/internal/controller"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
)

const systemNamespace = "sympozium-system"

// memoryProxyClient is used when the apiserver proxies UI/API requests to a
// per-Ensemble shared memory server (/list, /provenance). The otelhttp
// transport injects the W3C traceparent from the inbound request context (the
// apiserver mux is wrapped with otelhttp), so these proxied reads nest under
// the originating trace instead of opening a new root span on the memory
// server (ISI-1406: board observed orphaned single-span /list traces). When
// OTel is disabled the global propagator is a no-op, so no header is added.
var memoryProxyClient = &http.Client{
	Timeout:   5 * time.Second,
	Transport: otelhttp.NewTransport(http.DefaultTransport),
}

// Server is the Sympozium API server.
type Server struct {
	client       client.Client
	eventBus     eventbus.EventBus
	kube         kubernetes.Interface
	log          logr.Logger
	upgrader     websocket.Upgrader
	densityCache *controller.DensityCache // optional: set when llmfit DaemonSet is enabled
	authEnabled  bool                     // set by buildMux; gates pricing writes
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

// SetDensityCache sets the fitness cache for fitness API endpoints.
func (s *Server) SetDensityCache(cache *controller.DensityCache) {
	s.densityCache = cache
}

// Start starts the HTTP server (headless, no embedded UI).
// When expected holds a non-empty token, the auth middleware is applied.
func (s *Server) Start(addr string, expected *tokenReader) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           s.buildMux(nil, expected),
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.log.Info("Starting API server", "addr", addr, "auth", expected != nil && expected.Current() != "")
	return server.ListenAndServe()
}

// StartWithUI starts the HTTP server with an embedded frontend SPA
// and optional bearer-token authentication.
func (s *Server) StartWithUI(addr string, expected *tokenReader, frontendFS fs.FS) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           s.buildMux(frontendFS, expected),
		ReadHeaderTimeout: 10 * time.Second,
	}

	s.log.Info("Starting API server with UI", "addr", addr)
	return server.ListenAndServe()
}

// Handler returns the HTTP handler for testing. Pass nil or a reader whose
// current() returns "" to skip auth.
func (s *Server) Handler(expected *tokenReader) http.Handler { return s.buildMux(nil, expected) }

// buildMux creates the HTTP mux with all API routes.
// When frontendFS is non-nil, it serves the SPA for non-API paths.
// When expected holds a non-empty token, API routes require Bearer
// authentication backed by the reader so the token can be rotated without
// a pod restart.
func (s *Server) buildMux(frontendFS fs.FS, expected *tokenReader) http.Handler {
	// Some cluster-wide mutating routes (pricing) refuse writes outright when
	// the server runs unauthenticated, instead of inheriting the open-API mode.
	s.authEnabled = expected != nil && expected.Current() != ""

	mux := http.NewServeMux()

	// Instance endpoints
	mux.HandleFunc("GET /api/v1/agents", s.listAgents)
	mux.HandleFunc("GET /api/v1/agents/{name}", s.getAgent)
	mux.HandleFunc("POST /api/v1/agents", s.createAgent)
	mux.HandleFunc("DELETE /api/v1/agents/{name}", s.deleteAgent)
	mux.HandleFunc("PATCH /api/v1/agents/{name}", s.patchAgent)
	mux.HandleFunc("GET /api/v1/agents/{name}/web-endpoint", s.getWebEndpointStatus)

	// Run endpoints
	mux.HandleFunc("GET /api/v1/runs", s.listRuns)
	mux.HandleFunc("GET /api/v1/runs/{name}", s.getRun)
	mux.HandleFunc("GET /api/v1/runs/{name}/telemetry", s.getRunTelemetry)
	mux.HandleFunc("POST /api/v1/runs", s.createRun)
	mux.HandleFunc("DELETE /api/v1/runs/{name}", s.deleteRun)
	mux.HandleFunc("POST /api/v1/runs/{name}/gate-verdict", s.patchRunGateVerdict)

	// Observability endpoints
	mux.HandleFunc("GET /api/v1/observability/metrics", s.getObservabilityMetrics)

	// Policy endpoints
	mux.HandleFunc("GET /api/v1/policies", s.listPolicies)
	mux.HandleFunc("GET /api/v1/policies/{name}", s.getPolicy)

	// Skill endpoints
	mux.HandleFunc("GET /api/v1/skills", s.listSkills)
	mux.HandleFunc("GET /api/v1/skills/{name}", s.getSkill)

	// GitHub GitOps skill auth endpoints (PAT token)
	mux.HandleFunc("POST /api/v1/skills/github-gitops/auth/token", s.handleGithubAuthToken)
	mux.HandleFunc("GET /api/v1/skills/github-gitops/auth/status", s.handleGithubAuthStatus)

	// Schedule endpoints
	mux.HandleFunc("GET /api/v1/schedules", s.listSchedules)
	mux.HandleFunc("GET /api/v1/schedules/{name}", s.getSchedule)
	mux.HandleFunc("POST /api/v1/schedules", s.createSchedule)
	mux.HandleFunc("PATCH /api/v1/schedules/{name}", s.patchSchedule)
	mux.HandleFunc("DELETE /api/v1/schedules/{name}", s.deleteSchedule)

	// Ensemble endpoints
	mux.HandleFunc("GET /api/v1/ensembles", s.listEnsembles)
	mux.HandleFunc("POST /api/v1/ensembles", s.createEnsemble)
	mux.HandleFunc("POST /api/v1/ensembles/install-defaults", s.installDefaultEnsembles)
	mux.HandleFunc("GET /api/v1/ensembles/{name}", s.getEnsemble)
	mux.HandleFunc("PATCH /api/v1/ensembles/{name}", s.patchEnsemble)
	mux.HandleFunc("DELETE /api/v1/ensembles/{name}", s.deleteEnsemble)
	mux.HandleFunc("POST /api/v1/ensembles/{name}/clone", s.cloneEnsemble)
	mux.HandleFunc("GET /api/v1/ensembles/{name}/shared-memory", s.listEnsembleSharedMemory)
	mux.HandleFunc("GET /api/v1/ensembles/{name}/shared-memory/{id}/provenance", s.getEnsembleSharedMemoryProvenance)
	mux.HandleFunc("POST /api/v1/ensembles/{name}/stimulus/trigger", s.triggerStimulus)

	// MCP Server endpoints
	mux.HandleFunc("GET /api/v1/mcpservers", s.listMCPServers)
	mux.HandleFunc("POST /api/v1/mcpservers/install-defaults", s.installDefaultMCPServers)
	mux.HandleFunc("GET /api/v1/mcpservers/{name}", s.getMCPServer)
	mux.HandleFunc("POST /api/v1/mcpservers", s.createMCPServer)
	mux.HandleFunc("DELETE /api/v1/mcpservers/{name}", s.deleteMCPServer)
	mux.HandleFunc("PATCH /api/v1/mcpservers/{name}", s.patchMCPServer)
	mux.HandleFunc("POST /api/v1/mcpservers/{name}/auth/token", s.handleMCPServerAuthToken)
	mux.HandleFunc("GET /api/v1/mcpservers/{name}/auth/status", s.handleMCPServerAuthStatus)

	// Node endpoints
	mux.HandleFunc("GET /api/v1/nodes", s.listClusterNodes)

	// DRA inventory (llmfit-dra ResourceSlices — accelerators + fabric NICs)
	mux.HandleFunc("GET /api/v1/dra/nodes", s.listDRANodes)

	// Fitness endpoints (llmfit DaemonSet telemetry)
	mux.HandleFunc("GET /api/v1/density/nodes", s.listDensityNodes)
	mux.HandleFunc("GET /api/v1/density/nodes/{name}", s.getDensityNode)
	mux.HandleFunc("GET /api/v1/density/runtimes", s.listDensityRuntimes)
	mux.HandleFunc("GET /api/v1/density/installed-models", s.listDensityInstalledModels)
	mux.HandleFunc("GET /api/v1/density/query", s.queryDensity)
	mux.HandleFunc("GET /api/v1/catalog", s.getCatalog)
	mux.HandleFunc("POST /api/v1/density/simulate", s.handleSimulate)
	mux.HandleFunc("GET /api/v1/density/cost", s.handleCost)

	// Model endpoints (cluster-local inference)
	mux.HandleFunc("GET /api/v1/models", s.listModels)
	mux.HandleFunc("GET /api/v1/models/{name}", s.getModel)
	mux.HandleFunc("POST /api/v1/models", s.createModel)
	mux.HandleFunc("DELETE /api/v1/models/{name}", s.deleteModel)

	// Gateway config endpoints (singleton SympoziumConfig)
	mux.HandleFunc("GET /api/v1/gateway", s.getGatewayConfig)
	mux.HandleFunc("POST /api/v1/gateway", s.createGatewayConfig)
	mux.HandleFunc("PATCH /api/v1/gateway", s.patchGatewayConfig)
	mux.HandleFunc("DELETE /api/v1/gateway", s.deleteGatewayConfig)
	mux.HandleFunc("GET /api/v1/gateway/metrics", s.getGatewayMetrics)

	// System canary endpoints
	mux.HandleFunc("GET /api/v1/canary", s.getCanaryConfig)
	mux.HandleFunc("PATCH /api/v1/canary", s.patchCanaryConfig)

	// Model pricing endpoints (cost estimation; distinct from /density/cost,
	// which is GPU placement cost)
	mux.HandleFunc("GET /api/v1/pricing", s.getPricing)
	mux.HandleFunc("PUT /api/v1/pricing/simulated", s.putSimulatedPrices)
	mux.HandleFunc("DELETE /api/v1/pricing/simulated", s.deleteSimulatedPrices)

	// Provider discovery endpoints (model listing, node discovery)
	mux.HandleFunc("GET /api/v1/providers/nodes", s.listProviderNodes)
	mux.HandleFunc("GET /api/v1/providers/models", s.proxyProviderModels)
	mux.HandleFunc("POST /api/v1/providers/bedrock/models", s.listBedrockModels)

	// Cluster info & capabilities
	mux.HandleFunc("GET /api/v1/cluster", s.getClusterInfo)
	mux.HandleFunc("GET /api/v1/capabilities", s.getCapabilities)

	// Agent Sandbox CRD management
	mux.HandleFunc("POST /api/v1/agent-sandbox/install", s.installAgentSandboxCRDs)
	mux.HandleFunc("DELETE /api/v1/agent-sandbox/install", s.uninstallAgentSandboxCRDs)

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

	// Wrap the mux with otelhttp for automatic HTTP span instrumentation.
	handler := otelhttp.NewHandler(mux, "sympozium-apiserver",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return "sympozium.api." + r.Method
		}),
	)

	// Wrap with auth middleware if a token is configured.
	if expected != nil && expected.Current() != "" {
		return authMiddleware(expected, handler)
	}

	s.log.Info("WARNING: API server auth token is empty — /api and /ws endpoints are unauthenticated; any caller can create runs in any namespace")
	return handler
}

// authMiddleware returns an http.Handler that checks for a valid Bearer token.
// The ?token= query parameter is accepted only for WebSocket (/ws/) upgrades.
// Health and metrics endpoints are exempted.
//
// The expected token is read through a tokenReader so that a Secret rotation
// takes effect without a pod restart. Each request calls expected.Current(),
// which re-reads the mounted file (one stat + read syscall pair, a few µs).
// If the expected token is empty at request time, the middleware fails closed
// (401) — running open is a startup decision only.
func authMiddleware(expected *tokenReader, next http.Handler) http.Handler {
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

		// Prefer the Authorization header. Fall back to the ?token= query
		// parameter only for WebSocket upgrades (/ws/), where browsers cannot
		// set custom headers — REST callers must use the header so tokens do
		// not leak into access logs, proxies, or browser history.
		token := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
		if token == "" && strings.HasPrefix(path, "/ws/") {
			token = r.URL.Query().Get("token")
		}
		got := []byte(token)
		expectedBytes := []byte(expected.Current())
		// Fail closed: if the expected token is empty (file rotated to
		// empty, or read failure with no cached value), reject unconditionally.
		// Running open is a startup decision, not something a runtime
		// rotation should be able to flip.
		if len(expectedBytes) == 0 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		// Length-mismatch short-circuit: subtle.ConstantTimeCompare returns
		// 0 on different-length inputs but the timing leak on length is
		// visible. Branches on length are not a leak we care about (token
		// length is fixed for any given deployment), so reject early.
		if len(got) != len(expectedBytes) {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if subtle.ConstantTimeCompare(got, expectedBytes) != 1 {
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

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list sympoziumv1alpha1.AgentList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Name < list.Items[j].Name
	})
	writeJSON(w, list.Items)
}

func (s *Server) getAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var inst sympoziumv1alpha1.Agent
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &inst); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, inst)
}

func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	inst := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := s.client.Delete(r.Context(), inst); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// PatchInstanceRequest is the request body for partially updating a Agent.
type PatchInstanceRequest struct {
	WebEndpoint     *PatchWebEndpoint                 `json:"webEndpoint,omitempty"`
	Lifecycle       *sympoziumv1alpha1.LifecycleHooks `json:"lifecycle,omitempty"`
	RequireApproval *bool                             `json:"requireApproval,omitempty"`
}

// PatchWebEndpoint is the web endpoint patch payload.
type PatchWebEndpoint struct {
	Enabled   *bool   `json:"enabled,omitempty"`
	Hostname  *string `json:"hostname,omitempty"`
	RateLimit *struct {
		RequestsPerMinute *int `json:"requestsPerMinute,omitempty"`
	} `json:"rateLimit,omitempty"`
}

func (s *Server) patchAgent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req PatchInstanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var inst sympoziumv1alpha1.Agent
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &inst); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Build the edit rather than mutating the Agent directly: agentedit routes it
	// to the owning Ensemble's agent config when the Agent is ensemble-managed, so
	// the change is not reverted by the next reconcile.
	edit := agentedit.Edit{}

	if req.WebEndpoint != nil {
		we := &agentedit.WebEndpointEdit{
			Enabled: req.WebEndpoint.Enabled == nil || *req.WebEndpoint.Enabled,
		}
		if we.Enabled {
			if req.WebEndpoint.Hostname != nil {
				we.Hostname = *req.WebEndpoint.Hostname
			}
			if req.WebEndpoint.RateLimit != nil && req.WebEndpoint.RateLimit.RequestsPerMinute != nil {
				we.RequestsPerMinute = *req.WebEndpoint.RateLimit.RequestsPerMinute
			}
		}
		edit.WebEndpoint = we
	}

	// Apply lifecycle hooks patch.
	if req.Lifecycle != nil {
		hooks := req.Lifecycle
		if len(hooks.PreRun) == 0 && len(hooks.PostRun) == 0 && len(hooks.RBAC) == 0 {
			hooks = nil
		}
		edit.Lifecycle = &hooks
	}

	// Apply requireApproval toggle. This adds or removes a built-in manual
	// gate hook that sleeps until an operator approves via the UI or API.
	// It composes with the lifecycle patch above, so it is applied to a copy of
	// the Agent and the resulting hooks are handed to agentedit.
	if req.RequireApproval != nil {
		staged := inst.DeepCopy()
		if edit.Lifecycle != nil {
			staged.Spec.Agents.Default.Lifecycle = *edit.Lifecycle
		}
		applyRequireApproval(staged, *req.RequireApproval)
		hooks := staged.Spec.Agents.Default.Lifecycle
		edit.Lifecycle = &hooks
	}

	target, err := agentedit.Apply(r.Context(), s.client, &inst, edit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// The body stays the Agent, as clients expect; the header says where the edit
	// landed and whether it has taken effect. agentedit waits for an
	// ensemble-routed edit to reach the Agent before returning, so a client
	// re-reading on success normally sees the new values — except when the header
	// reports "still updating", in which case its next poll will pick them up.
	//
	// inst is re-read so the body reflects the reconciled state rather than what
	// was fetched at the top of the handler.
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &inst); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("X-Sympozium-Applied-To", target.String())
	writeJSON(w, inst)
}

// WebEndpointStatusResponse is the response for the web-endpoint status endpoint.
type WebEndpointStatusResponse struct {
	Enabled        bool   `json:"enabled"`
	DeploymentName string `json:"deploymentName,omitempty"`
	ServiceName    string `json:"serviceName,omitempty"`
	GatewayReady   bool   `json:"gatewayReady"`
	RouteURL       string `json:"routeURL,omitempty"`
	AuthSecretName string `json:"authSecretName,omitempty"`
}

func (s *Server) getWebEndpointStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var inst sympoziumv1alpha1.Agent
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &inst); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := WebEndpointStatusResponse{}

	// Check for web-endpoint skill.
	for _, skill := range inst.Spec.Skills {
		if skill.SkillPackRef == "web-endpoint" || skill.SkillPackRef == "skillpack-web-endpoint" {
			resp.Enabled = true
			break
		}
	}

	if !resp.Enabled {
		writeJSON(w, resp)
		return
	}

	// Find server-mode AgentRun for this instance.
	var runs sympoziumv1alpha1.AgentRunList
	if err := s.client.List(r.Context(), &runs,
		client.InNamespace(ns),
		client.MatchingLabels{"sympozium.ai/instance": name},
	); err == nil {
		for _, run := range runs.Items {
			if run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseServing {
				resp.DeploymentName = run.Status.DeploymentName
				resp.ServiceName = run.Status.ServiceName
				break
			}
		}
	}

	// Check gateway readiness.
	var config sympoziumv1alpha1.SympoziumConfig
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: "default", Namespace: "sympozium-system"}, &config); err == nil {
		if config.Status.Gateway != nil && config.Status.Gateway.Ready {
			resp.GatewayReady = true
		}
	}

	writeJSON(w, resp)
}

// CreateInstanceRequest is the request body for creating a new Agent.
type CreateInstanceRequest struct {
	Name               string                                  `json:"name"`
	Provider           string                                  `json:"provider"`
	Model              string                                  `json:"model"`
	BaseURL            string                                  `json:"baseURL,omitempty"`
	SecretName         string                                  `json:"secretName,omitempty"`
	APIKey             string                                  `json:"apiKey,omitempty"`
	AWSRegion          string                                  `json:"awsRegion,omitempty"`
	AWSAccessKeyID     string                                  `json:"awsAccessKeyId,omitempty"`
	AWSSecretAccessKey string                                  `json:"awsSecretAccessKey,omitempty"`
	AWSSessionToken    string                                  `json:"awsSessionToken,omitempty"`
	PolicyRef          string                                  `json:"policyRef,omitempty"`
	Skills             []sympoziumv1alpha1.SkillRef            `json:"skills,omitempty"`
	Channels           []sympoziumv1alpha1.ChannelSpec         `json:"channels,omitempty"`
	HeartbeatInterval  string                                  `json:"heartbeatInterval,omitempty"`
	NodeSelector       map[string]string                       `json:"nodeSelector,omitempty"`
	AgentSandbox       *sympoziumv1alpha1.AgentSandboxDefaults `json:"agentSandbox,omitempty"`
	RunTimeout         string                                  `json:"runTimeout,omitempty"`
	RequireApproval    bool                                    `json:"requireApproval,omitempty"`
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
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

	inst := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: req.Model,
				},
			},
			Memory: &sympoziumv1alpha1.MemorySpec{
				Enabled:   true,
				MaxSizeKB: 256,
			},
			Observability: &sympoziumv1alpha1.ObservabilitySpec{
				Enabled:     true,
				ServiceName: "sympozium",
			},
		},
	}

	if req.BaseURL != "" {
		inst.Spec.Agents.Default.BaseURL = req.BaseURL
	} else {
		// Apply default baseURL for keyless local providers (fixes #39).
		inst.Spec.Agents.Default.BaseURL = defaultProviderBaseURL(req.Provider)
	}
	if len(req.NodeSelector) > 0 {
		inst.Spec.Agents.Default.NodeSelector = req.NodeSelector
	}
	if req.AgentSandbox != nil {
		inst.Spec.Agents.Default.AgentSandbox = req.AgentSandbox
	}
	if req.RunTimeout != "" {
		inst.Spec.Agents.Default.RunTimeout = req.RunTimeout
	}
	if req.RequireApproval {
		applyRequireApproval(inst, true)
	}

	// Bedrock: create a multi-key secret with AWS credentials.
	if req.Provider == "bedrock" && req.AWSRegion != "" && req.SecretName == "" {
		req.SecretName = defaultProviderSecretName(req.Name, req.Provider)
		secretData := map[string]string{"AWS_REGION": req.AWSRegion}
		if req.AWSAccessKeyID != "" {
			secretData["AWS_ACCESS_KEY_ID"] = req.AWSAccessKeyID
			secretData["AWS_SECRET_ACCESS_KEY"] = req.AWSSecretAccessKey
			if req.AWSSessionToken != "" {
				secretData["AWS_SESSION_TOKEN"] = req.AWSSessionToken
			}
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.SecretName,
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "sympozium",
					"sympozium.ai/instance":        req.Name,
				},
			},
			StringData: secretData,
		}
		if err := createOrUpdateSecret(r.Context(), s.client, secret); err != nil {
			http.Error(w, "failed to create credentials secret: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	secretAutoCreated := false
	if req.Provider != "" && req.APIKey != "" && req.SecretName == "" {
		secretAutoCreated = true
		req.SecretName = defaultProviderSecretName(req.Name, req.Provider)
		envKey := providerEnvKey(req.Provider)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.SecretName,
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "sympozium",
					"sympozium.ai/instance":        req.Name,
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

	if req.Provider != "" && req.SecretName != "" && !secretAutoCreated {
		existing := &corev1.Secret{}
		if err := s.client.Get(r.Context(), types.NamespacedName{Name: req.SecretName, Namespace: ns}, existing); err != nil {
			if k8serrors.IsNotFound(err) {
				http.Error(w, fmt.Sprintf("secret %q not found in namespace %q", req.SecretName, ns), http.StatusBadRequest)
				return
			}
			http.Error(w, "failed to get secret: "+err.Error(), http.StatusInternalServerError)
			return
		}
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
		inst.Spec.Skills = req.Skills
	}

	if len(req.Channels) > 0 {
		inst.Spec.Channels = req.Channels
	}

	if err := s.client.Create(r.Context(), inst); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Auto-create a heartbeat schedule when an interval is provided.
	if req.HeartbeatInterval != "" {
		cron := intervalToCronExpr(req.HeartbeatInterval)
		sched := &sympoziumv1alpha1.SympoziumSchedule{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.Name + "-heartbeat",
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "sympozium",
					"sympozium.ai/instance":        req.Name,
				},
			},
			Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
				AgentRef:          req.Name,
				Schedule:          cron,
				Task:              "heartbeat",
				Type:              "heartbeat",
				ConcurrencyPolicy: "Forbid",
				IncludeMemory:     true,
			},
		}
		if err := s.client.Create(r.Context(), sched); err != nil && !k8serrors.IsAlreadyExists(err) {
			// Log but don't fail the instance creation.
			s.log.Error(err, "failed to create heartbeat schedule", "instance", req.Name)
		}
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, inst)
}

// intervalToCronExpr converts a human-readable interval (e.g. "1h", "30m") to a cron expression.
func intervalToCronExpr(interval string) string {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "1m", "1min":
		return "* * * * *"
	case "5m", "5min":
		return "*/5 * * * *"
	case "10m", "10min":
		return "*/10 * * * *"
	case "15m":
		return "*/15 * * * *"
	case "30m":
		return "*/30 * * * *"
	case "1h":
		return "0 * * * *"
	case "2h":
		return "0 */2 * * *"
	case "6h":
		return "0 */6 * * *"
	case "12h":
		return "0 */12 * * *"
	case "24h", "1d":
		return "0 0 * * *"
	default:
		if strings.Contains(interval, " ") {
			return interval
		}
		return "0 * * * *"
	}
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

	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Name < list.Items[j].Name
	})

	simTable := s.simulatedTable(r.Context())
	out := make([]runWithCostOverlay, len(list.Items))
	for i := range list.Items {
		out[i] = runWithCostOverlay{
			AgentRun:              list.Items[i],
			SimulatedCostEstimate: overlaySimulatedCost(simTable, &list.Items[i]),
		}
	}
	writeJSON(w, out)
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

	writeJSON(w, runWithCostOverlay{
		AgentRun:              run,
		SimulatedCostEstimate: overlaySimulatedCost(s.simulatedTable(r.Context()), &run),
	})
}

// CreateRunRequest is the request body for creating a new AgentRun.
type CreateRunRequest struct {
	AgentRef   string `json:"agentRef"`
	Task       string `json:"task"`
	AgentID    string `json:"agentId,omitempty"`
	SessionKey string `json:"sessionKey,omitempty"`
	Model      string `json:"model,omitempty"`
	Timeout    string `json:"timeout,omitempty"`
	Backend    string `json:"backend,omitempty"`
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

	if req.AgentRef == "" || req.Task == "" {
		http.Error(w, "agentRef and task are required", http.StatusBadRequest)
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

	// Look up the Agent to inherit auth, model, and skills.
	var inst sympoziumv1alpha1.Agent
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: req.AgentRef, Namespace: ns}, &inst); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, fmt.Sprintf("instance %q not found in namespace %q", req.AgentRef, ns), http.StatusNotFound)
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

	// Infer provider from baseURL for keyless local providers (e.g., Ollama via node-probe).
	if len(inst.Spec.AuthRefs) == 0 && inst.Spec.Agents.Default.BaseURL != "" {
		if strings.Contains(inst.Spec.Agents.Default.BaseURL, "ollama") || strings.Contains(inst.Spec.Agents.Default.BaseURL, ":11434") {
			provider = "ollama"
		} else if strings.Contains(inst.Spec.Agents.Default.BaseURL, "lm-studio") || strings.Contains(inst.Spec.Agents.Default.BaseURL, ":1234") {
			provider = "lm-studio"
		} else if strings.Contains(inst.Spec.Agents.Default.BaseURL, "llama-server") {
			provider = "llama-server"
		} else {
			provider = "custom"
		}
	}

	// Cloud providers require an API key; local providers with a baseURL do not.
	if authSecret == "" && inst.Spec.Agents.Default.BaseURL == "" {
		http.Error(w, fmt.Sprintf("instance %q has no API key configured (authRefs is empty)", req.AgentRef), http.StatusBadRequest)
		return
	}

	// Use request-supplied model or fall back to the instance default.
	model := req.Model
	if model == "" {
		model = inst.Spec.Agents.Default.Model
	}

	// Use request-supplied timeout or fall back to the instance default.
	timeout := inst.Spec.Agents.Default.ParseRunTimeout()
	if req.Timeout != "" {
		if d, err := time.ParseDuration(req.Timeout); err == nil {
			timeout = &metav1.Duration{Duration: d}
		}
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: req.AgentRef + "-",
			Namespace:    ns,
			Labels: map[string]string{
				"sympozium.ai/instance": req.AgentRef,
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef:   req.AgentRef,
			AgentID:    req.AgentID,
			SessionKey: req.SessionKey,
			Task:       sympoziumv1alpha1.NewStringTask(req.Task),
			Backend:    req.Backend,
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:                 provider,
				Model:                    model,
				BaseURL:                  inst.Spec.Agents.Default.BaseURL,
				AuthSecretRef:            authSecret,
				ProviderHeaders:          inst.Spec.Agents.Default.ProviderHeaders,
				ProviderHeadersSecretRef: inst.Spec.Agents.Default.ProviderHeadersSecretRef,
				NodeSelector:             inst.Spec.Agents.Default.NodeSelector,
			},
			Skills:           inst.Spec.Skills,
			ImagePullSecrets: inst.Spec.ImagePullSecrets,
			Lifecycle:        inst.Spec.Agents.Default.Lifecycle,
			Env:              inst.Spec.Agents.Default.Env,
			Timeout:          timeout,
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

const manualGateHookName = "manual-approval-gate"

// applyRequireApproval adds or removes a built-in manual approval gate hook
// on the instance's lifecycle. When enabled, all runs from this instance will
// pause in PostRunning until an operator approves via the UI or API.
func applyRequireApproval(inst *sympoziumv1alpha1.Agent, enable bool) {
	lc := inst.Spec.Agents.Default.Lifecycle

	if enable {
		// Ensure lifecycle struct exists.
		if lc == nil {
			lc = &sympoziumv1alpha1.LifecycleHooks{}
			inst.Spec.Agents.Default.Lifecycle = lc
		}

		// Check if already present.
		for _, h := range lc.PostRun {
			if h.Name == manualGateHookName {
				return // already configured
			}
		}

		lc.GateDefault = "block"
		lc.PostRun = append(lc.PostRun, sympoziumv1alpha1.LifecycleHookContainer{
			Name:    manualGateHookName,
			Image:   "busybox:1.36",
			Gate:    true,
			Command: []string{"sh", "-c", "echo 'Waiting for manual approval...'; sleep 86400"},
		})

		// Ensure RBAC for patching agentruns (needed for API/UI approval).
		hasAgentRunRBAC := false
		for _, r := range lc.RBAC {
			for _, res := range r.Resources {
				if res == "agentruns" {
					hasAgentRunRBAC = true
					break
				}
			}
		}
		if !hasAgentRunRBAC {
			lc.RBAC = append(lc.RBAC, sympoziumv1alpha1.RBACRule{
				APIGroups: []string{"sympozium.ai"},
				Resources: []string{"agentruns"},
				Verbs:     []string{"get", "patch"},
			})
		}
	} else {
		// Remove the manual gate hook.
		if lc == nil {
			return
		}
		var filtered []sympoziumv1alpha1.LifecycleHookContainer
		for _, h := range lc.PostRun {
			if h.Name != manualGateHookName {
				filtered = append(filtered, h)
			}
		}
		lc.PostRun = filtered
		if lc.GateDefault == "block" && !hasAnyGateHook(lc) {
			lc.GateDefault = ""
		}
		// Clean up lifecycle if empty.
		if len(lc.PreRun) == 0 && len(lc.PostRun) == 0 && len(lc.RBAC) == 0 {
			inst.Spec.Agents.Default.Lifecycle = nil
		}
	}
}

func hasAnyGateHook(lc *sympoziumv1alpha1.LifecycleHooks) bool {
	for _, h := range lc.PostRun {
		if h.Gate {
			return true
		}
	}
	return false
}

// GateVerdictRequest is the request body for approving or rejecting a gated AgentRun.
type GateVerdictRequest struct {
	Action   string `json:"action"`             // approve, reject, rewrite
	Response string `json:"response,omitempty"` // replacement text for reject/rewrite
	Reason   string `json:"reason,omitempty"`   // audit trail
}

func (s *Server) patchRunGateVerdict(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req GateVerdictRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Action != "approve" && req.Action != "reject" && req.Action != "rewrite" {
		http.Error(w, `action must be "approve", "reject", or "rewrite"`, http.StatusBadRequest)
		return
	}
	if (req.Action == "reject" || req.Action == "rewrite") && req.Response == "" {
		http.Error(w, "response is required for reject/rewrite actions", http.StatusBadRequest)
		return
	}

	var run sympoziumv1alpha1.AgentRun
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &run); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "agent run not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if run.Status.Phase != sympoziumv1alpha1.AgentRunPhasePostRunning {
		http.Error(w, fmt.Sprintf("run is in phase %q, gate verdict can only be set during PostRunning", run.Status.Phase), http.StatusConflict)
		return
	}

	if run.Annotations == nil {
		run.Annotations = make(map[string]string)
	}
	verdictJSON, _ := json.Marshal(req)
	run.Annotations["sympozium.ai/gate-verdict"] = string(verdictJSON)

	if err := s.client.Update(r.Context(), &run); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, run)
}

// --- Policy handlers ---

func (s *Server) listPolicies(w http.ResponseWriter, r *http.Request) {
	// Policies are platform-wide shared resources — list across all namespaces.
	var list sympoziumv1alpha1.SympoziumPolicyList
	if err := s.client.List(r.Context(), &list); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Name < list.Items[j].Name
	})
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

	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Name < list.Items[j].Name
	})
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

// --- MCP Server handlers ---

func (s *Server) listMCPServers(w http.ResponseWriter, r *http.Request) {
	// MCPServers are platform-wide — list across all namespaces.
	var list sympoziumv1alpha1.MCPServerList
	if err := s.client.List(r.Context(), &list); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Name < list.Items[j].Name
	})
	writeJSON(w, list.Items)
}

func (s *Server) getMCPServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var mcp sympoziumv1alpha1.MCPServer
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &mcp); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "mcpserver not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, mcp)
}

// CreateMCPServerRequest is the request body for creating an MCPServer.
type CreateMCPServerRequest struct {
	Name          string            `json:"name"`
	TransportType string            `json:"transportType"`
	ToolsPrefix   string            `json:"toolsPrefix"`
	URL           string            `json:"url,omitempty"`
	Image         string            `json:"image,omitempty"`
	Timeout       int               `json:"timeout,omitempty"`
	ToolsAllow    []string          `json:"toolsAllow,omitempty"`
	ToolsDeny     []string          `json:"toolsDeny,omitempty"`
	Env           map[string]string `json:"env,omitempty"`
	SecretRefs    []string          `json:"secretRefs,omitempty"`
	Args          []string          `json:"args,omitempty"`
}

func (s *Server) createMCPServer(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req CreateMCPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.TransportType == "" || req.ToolsPrefix == "" {
		http.Error(w, "name, transportType, and toolsPrefix are required", http.StatusBadRequest)
		return
	}

	mcp := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: req.TransportType,
			ToolsPrefix:   req.ToolsPrefix,
			URL:           req.URL,
			ToolsAllow:    req.ToolsAllow,
			ToolsDeny:     req.ToolsDeny,
		},
	}

	if req.Timeout > 0 {
		mcp.Spec.Timeout = req.Timeout
	}

	if req.Image != "" {
		dep := &sympoziumv1alpha1.MCPServerDeployment{
			Image: req.Image,
			Args:  req.Args,
			Env:   req.Env,
		}
		for _, ref := range req.SecretRefs {
			dep.SecretRefs = append(dep.SecretRefs, sympoziumv1alpha1.MCPSecretRef{Name: ref})
		}
		mcp.Spec.Deployment = dep
	}

	if err := s.client.Create(r.Context(), mcp); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			http.Error(w, "mcpserver already exists", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, mcp)
}

func (s *Server) deleteMCPServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	mcp := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := s.client.Delete(r.Context(), mcp); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "mcpserver not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// PatchMCPServerRequest is the request body for partially updating an MCPServer.
type PatchMCPServerRequest struct {
	TransportType *string  `json:"transportType,omitempty"`
	URL           *string  `json:"url,omitempty"`
	ToolsPrefix   *string  `json:"toolsPrefix,omitempty"`
	Timeout       *int     `json:"timeout,omitempty"`
	ToolsAllow    []string `json:"toolsAllow,omitempty"`
	ToolsDeny     []string `json:"toolsDeny,omitempty"`
	Suspended     *bool    `json:"suspended,omitempty"`
}

func (s *Server) patchMCPServer(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req PatchMCPServerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var mcp sympoziumv1alpha1.MCPServer
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &mcp); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "mcpserver not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if req.TransportType != nil {
		mcp.Spec.TransportType = *req.TransportType
	}
	if req.URL != nil {
		mcp.Spec.URL = *req.URL
	}
	if req.ToolsPrefix != nil {
		mcp.Spec.ToolsPrefix = *req.ToolsPrefix
	}
	if req.Timeout != nil {
		mcp.Spec.Timeout = *req.Timeout
	}
	if req.ToolsAllow != nil {
		mcp.Spec.ToolsAllow = req.ToolsAllow
	}
	if req.ToolsDeny != nil {
		mcp.Spec.ToolsDeny = req.ToolsDeny
	}
	if req.Suspended != nil {
		mcp.Spec.Suspended = *req.Suspended
	}

	if err := s.client.Update(r.Context(), &mcp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, mcp)
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

	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Name < list.Items[j].Name
	})
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
	// InstanceRef is the name of the Agent this schedule belongs to.
	AgentRef string `json:"agentRef"`
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

	if req.AgentRef == "" || req.Schedule == "" || req.Task == "" {
		http.Error(w, "agentRef, schedule, and task are required", http.StatusBadRequest)
		return
	}

	sched := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			AgentRef: req.AgentRef,
			Schedule: req.Schedule,
			Task:     req.Task,
			Suspend:  req.Suspend,
		},
	}

	if req.Name != "" {
		sched.ObjectMeta.Name = req.Name
	} else {
		sched.ObjectMeta.GenerateName = req.AgentRef + "-schedule-"
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

// PatchScheduleRequest is the request body for updating an existing SympoziumSchedule.
type PatchScheduleRequest struct {
	Schedule          *string `json:"schedule,omitempty"`
	Task              *string `json:"task,omitempty"`
	Type              *string `json:"type,omitempty"`
	Suspend           *bool   `json:"suspend,omitempty"`
	ConcurrencyPolicy *string `json:"concurrencyPolicy,omitempty"`
}

func (s *Server) patchSchedule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req PatchScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var sched sympoziumv1alpha1.SympoziumSchedule
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &sched); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if req.Schedule != nil {
		sched.Spec.Schedule = *req.Schedule
	}
	if req.Task != nil {
		sched.Spec.Task = *req.Task
	}
	if req.Type != nil {
		sched.Spec.Type = *req.Type
	}
	if req.Suspend != nil {
		sched.Spec.Suspend = *req.Suspend
	}
	if req.ConcurrencyPolicy != nil {
		sched.Spec.ConcurrencyPolicy = *req.ConcurrencyPolicy
	}

	if err := s.client.Update(r.Context(), &sched); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, sched)
}

// --- Ensemble handlers ---

func (s *Server) listEnsembles(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var list sympoziumv1alpha1.EnsembleList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Filter out system canary — it has its own dedicated UI section.
	filtered := make([]sympoziumv1alpha1.Ensemble, 0, len(list.Items))
	for _, e := range list.Items {
		if e.Labels["sympozium.ai/canary"] == "true" {
			continue
		}
		filtered = append(filtered, e)
	}

	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Name < filtered[j].Name
	})
	writeJSON(w, filtered)
}

func (s *Server) getEnsemble(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var pp sympoziumv1alpha1.Ensemble
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &pp); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	writeJSON(w, pp)
}

// PatchEnsembleRequest represents a partial update to a Ensemble.
type PatchEnsembleRequest struct {
	Enabled              *bool                                              `json:"enabled,omitempty"`
	Provider             string                                             `json:"provider,omitempty"`
	SecretName           string                                             `json:"secretName,omitempty"`
	APIKey               string                                             `json:"apiKey,omitempty"`
	AWSRegion            string                                             `json:"awsRegion,omitempty"`
	AWSAccessKeyID       string                                             `json:"awsAccessKeyId,omitempty"`
	AWSSecretAccessKey   string                                             `json:"awsSecretAccessKey,omitempty"`
	AWSSessionToken      string                                             `json:"awsSessionToken,omitempty"`
	Model                string                                             `json:"model,omitempty"`
	BaseURL              string                                             `json:"baseURL,omitempty"`
	Channels             []string                                           `json:"channels,omitempty"`
	ChannelConfigs       map[string]string                                  `json:"channelConfigs,omitempty"`
	PolicyRef            string                                             `json:"policyRef,omitempty"`
	HeartbeatInterval    string                                             `json:"heartbeatInterval,omitempty"`
	SkillParams          map[string]map[string]string                       `json:"skillParams,omitempty"`
	GithubToken          string                                             `json:"githubToken,omitempty"`
	AgentConfigs         []AgentConfigPatchSpec                             `json:"agentConfigs,omitempty"`
	ChannelAccessControl map[string]*sympoziumv1alpha1.ChannelAccessControl `json:"channelAccessControl,omitempty"`
	AgentSandbox         *sympoziumv1alpha1.AgentSandboxDefaults            `json:"agentSandbox,omitempty"`
	Relationships        []sympoziumv1alpha1.AgentConfigRelationship        `json:"relationships,omitempty"`
	WorkflowType         string                                             `json:"workflowType,omitempty"`
	SharedMemory         *sympoziumv1alpha1.SharedMemorySpec                `json:"sharedMemory,omitempty"`
	ModelRef             string                                             `json:"modelRef,omitempty"`
	Stimulus             *sympoziumv1alpha1.StimulusSpec                    `json:"stimulus,omitempty"`
}

// AgentConfigPatchSpec allows partial updates to individual personas by name.
type AgentConfigPatchSpec struct {
	Name         string                           `json:"name"`
	SystemPrompt *string                          `json:"systemPrompt,omitempty"`
	Skills       []string                         `json:"skills,omitempty"`
	Model        *string                          `json:"model,omitempty"`
	Provider     *string                          `json:"provider,omitempty"`
	BaseURL      *string                          `json:"baseURL,omitempty"`
	Subagents    *sympoziumv1alpha1.SubagentsSpec `json:"subagents,omitempty"`
}

func (s *Server) patchEnsemble(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req PatchEnsembleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var pp sympoziumv1alpha1.Ensemble
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &pp); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	if req.Enabled != nil {
		pp.Spec.Enabled = *req.Enabled
	}

	// Bedrock: create a multi-key secret with AWS credentials.
	if req.Provider == "bedrock" && req.AWSRegion != "" && req.SecretName == "" {
		req.SecretName = defaultProviderSecretName(name, req.Provider)
		secretData := map[string]string{"AWS_REGION": req.AWSRegion}
		if req.AWSAccessKeyID != "" {
			secretData["AWS_ACCESS_KEY_ID"] = req.AWSAccessKeyID
			secretData["AWS_SECRET_ACCESS_KEY"] = req.AWSSecretAccessKey
			if req.AWSSessionToken != "" {
				secretData["AWS_SESSION_TOKEN"] = req.AWSSessionToken
			}
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.SecretName,
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "sympozium",
					"sympozium.ai/ensemble":        name,
				},
			},
			StringData: secretData,
		}
		if err := createOrUpdateSecret(r.Context(), s.client, secret); err != nil {
			http.Error(w, "failed to create credentials secret: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Create or update a K8s Secret when the user provides a raw API key.
	if req.Provider != "" && req.APIKey != "" {
		if req.SecretName == "" {
			req.SecretName = defaultProviderSecretName(name, req.Provider)
		}
		envKey := providerEnvKey(req.Provider)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      req.SecretName,
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "sympozium",
					"sympozium.ai/ensemble":        name,
				},
			},
			StringData: map[string]string{envKey: req.APIKey},
		}
		if err := createOrUpdateSecret(r.Context(), s.client, secret); err != nil {
			http.Error(w, "failed to create credentials secret: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if req.Provider != "" && req.SecretName != "" {
		existing := &corev1.Secret{}
		if err := s.client.Get(r.Context(), types.NamespacedName{Name: req.SecretName, Namespace: ns}, existing); err != nil {
			if k8serrors.IsNotFound(err) {
				http.Error(w, fmt.Sprintf("secret %q not found in namespace %q", req.SecretName, ns), http.StatusBadRequest)
				return
			}
			http.Error(w, "failed to get secret: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if req.Provider != "" && req.SecretName != "" {
		pp.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{
			{Provider: req.Provider, Secret: req.SecretName},
		}
	}

	if req.Model != "" {
		for i := range pp.Spec.AgentConfigs {
			pp.Spec.AgentConfigs[i].Model = req.Model
		}
	}

	if req.ModelRef != "" {
		pp.Spec.ModelRef = req.ModelRef
		pp.Spec.BaseURL = "" // controller resolves from Model CR
		pp.Spec.AuthRefs = nil
	} else if req.BaseURL != "" {
		pp.Spec.BaseURL = req.BaseURL
		pp.Spec.ModelRef = ""
	} else if req.Provider != "" {
		// Apply the provider's default baseURL. For cloud providers this
		// is empty, which clears any stale local proxy URL. For local
		// providers this sets the conventional endpoint.
		pp.Spec.BaseURL = defaultProviderBaseURL(req.Provider)
	}

	if len(req.Channels) > 0 {
		for i := range pp.Spec.AgentConfigs {
			pp.Spec.AgentConfigs[i].Channels = req.Channels
		}
	}

	if req.ChannelConfigs != nil {
		pp.Spec.ChannelConfigs = req.ChannelConfigs
	}

	if len(req.ChannelAccessControl) > 0 {
		pp.Spec.ChannelAccessControl = req.ChannelAccessControl
	}

	if req.PolicyRef != "" {
		pp.Spec.PolicyRef = req.PolicyRef
	}

	if req.HeartbeatInterval != "" {
		for i := range pp.Spec.AgentConfigs {
			if pp.Spec.AgentConfigs[i].Schedule != nil {
				pp.Spec.AgentConfigs[i].Schedule.Interval = req.HeartbeatInterval
				pp.Spec.AgentConfigs[i].Schedule.Cron = "" // clear cron so interval takes precedence
			}
		}
	}

	if len(req.SkillParams) > 0 {
		if pp.Spec.SkillParams == nil {
			pp.Spec.SkillParams = make(map[string]map[string]string)
		}
		for skill, params := range req.SkillParams {
			pp.Spec.SkillParams[skill] = params
		}
	}

	// Apply per-persona patches.
	for _, patch := range req.AgentConfigs {
		for i := range pp.Spec.AgentConfigs {
			if pp.Spec.AgentConfigs[i].Name == patch.Name {
				if patch.SystemPrompt != nil {
					pp.Spec.AgentConfigs[i].SystemPrompt = *patch.SystemPrompt
				}
				if patch.Skills != nil {
					pp.Spec.AgentConfigs[i].Skills = patch.Skills
				}
				if patch.Model != nil {
					pp.Spec.AgentConfigs[i].Model = *patch.Model
				}
				if patch.Provider != nil {
					pp.Spec.AgentConfigs[i].Provider = *patch.Provider
				}
				if patch.BaseURL != nil {
					pp.Spec.AgentConfigs[i].BaseURL = *patch.BaseURL
				}
				if patch.Subagents != nil {
					pp.Spec.AgentConfigs[i].Subagents = patch.Subagents
				}
				break
			}
		}
	}

	if req.AgentSandbox != nil {
		pp.Spec.AgentSandbox = req.AgentSandbox
	}

	if req.Relationships != nil {
		pp.Spec.Relationships = req.Relationships
	}

	if req.WorkflowType != "" {
		pp.Spec.WorkflowType = req.WorkflowType
	}

	if req.SharedMemory != nil {
		pp.Spec.SharedMemory = req.SharedMemory
	}

	if req.Stimulus != nil {
		pp.Spec.Stimulus = req.Stimulus
	}

	// Store GitHub token as a cluster secret when provided inline.
	if req.GithubToken != "" {
		if err := s.writeGithubTokenSecret(req.GithubToken); err != nil {
			http.Error(w, "failed to store GitHub token: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	if err := s.client.Update(r.Context(), &pp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, pp)
}

func (s *Server) deleteEnsemble(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	pp := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := s.client.Delete(r.Context(), pp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// CreateEnsembleRequest represents a request to create a new Ensemble from scratch.
type CreateEnsembleRequest struct {
	Name          string                                      `json:"name"`
	Description   string                                      `json:"description,omitempty"`
	Category      string                                      `json:"category,omitempty"`
	WorkflowType  string                                      `json:"workflowType,omitempty"`
	AgentConfigs  []sympoziumv1alpha1.AgentConfigSpec         `json:"agentConfigs"`
	Relationships []sympoziumv1alpha1.AgentConfigRelationship `json:"relationships,omitempty"`
	SharedMemory  *sympoziumv1alpha1.SharedMemorySpec         `json:"sharedMemory,omitempty"`
	ModelRef      string                                      `json:"modelRef,omitempty"`
	Stimulus      *sympoziumv1alpha1.StimulusSpec             `json:"stimulus,omitempty"`
}

func (s *Server) createEnsemble(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req CreateEnsembleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if len(req.AgentConfigs) == 0 {
		http.Error(w, "at least one persona is required", http.StatusBadRequest)
		return
	}

	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Description:   req.Description,
			Category:      req.Category,
			WorkflowType:  req.WorkflowType,
			AgentConfigs:  req.AgentConfigs,
			Relationships: req.Relationships,
			SharedMemory:  req.SharedMemory,
			ModelRef:      req.ModelRef,
			Stimulus:      req.Stimulus,
		},
	}

	if err := s.client.Create(r.Context(), ensemble); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			http.Error(w, "ensemble already exists: "+req.Name, http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, ensemble)
}

// CloneEnsembleRequest represents a request to clone an existing Ensemble.
type CloneEnsembleRequest struct {
	Name string `json:"name"`
}

func (s *Server) cloneEnsemble(w http.ResponseWriter, r *http.Request) {
	sourceName := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req CloneEnsembleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name is required for the cloned ensemble", http.StatusBadRequest)
		return
	}

	// Read the source ensemble.
	source := &sympoziumv1alpha1.Ensemble{}
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: sourceName, Namespace: ns}, source); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "source ensemble not found: "+sourceName, http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Clone: copy spec, strip activation state and auth.
	clone := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Description:   source.Spec.Description + " (cloned from " + sourceName + ")",
			Category:      source.Spec.Category,
			Version:       source.Spec.Version,
			AgentConfigs:  source.Spec.AgentConfigs,
			Relationships: source.Spec.Relationships,
			WorkflowType:  source.Spec.WorkflowType,
			SharedMemory:  source.Spec.SharedMemory,
			PolicyRef:     source.Spec.PolicyRef,
			SkillParams:   source.Spec.SkillParams,
			TaskOverride:  source.Spec.TaskOverride,
			AgentSandbox:  source.Spec.AgentSandbox,
			Stimulus:      source.Spec.Stimulus,
			// Intentionally omitted: Enabled, AuthRefs, ChannelConfigs, BaseURL, ExcludeAgentConfigs
		},
	}

	if err := s.client.Create(r.Context(), clone); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			http.Error(w, "ensemble already exists: "+req.Name, http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, clone)
}

// listEnsembleSharedMemory proxies to the pack's shared memory server to list entries.
func (s *Server) listEnsembleSharedMemory(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	// Verify the pack exists and has shared memory enabled.
	pp := &sympoziumv1alpha1.Ensemble{}
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: name, Namespace: ns}, pp); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "Ensemble not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if pp.Spec.SharedMemory == nil || !pp.Spec.SharedMemory.Enabled {
		http.Error(w, "shared memory not enabled for this pack", http.StatusNotFound)
		return
	}

	// Proxy to the shared memory server's /list endpoint.
	sharedMemoryURL := fmt.Sprintf("http://%s-shared-memory.%s.svc:8080/list", name, ns)

	// Forward query params.
	if tags := r.URL.Query().Get("tags"); tags != "" {
		sharedMemoryURL += "?tags=" + tags
	}
	if limit := r.URL.Query().Get("limit"); limit != "" {
		sep := "?"
		if strings.Contains(sharedMemoryURL, "?") {
			sep = "&"
		}
		sharedMemoryURL += sep + "limit=" + limit
	}
	if minKind := r.URL.Query().Get("min_kind"); minKind != "" {
		sep := "?"
		if strings.Contains(sharedMemoryURL, "?") {
			sep = "&"
		}
		sharedMemoryURL += sep + "min_kind=" + minKind
	}
	if sourceAgent := r.URL.Query().Get("source_agent"); sourceAgent != "" {
		sep := "?"
		if strings.Contains(sharedMemoryURL, "?") {
			sep = "&"
		}
		sharedMemoryURL += sep + "source_agent=" + sourceAgent
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", sharedMemoryURL, nil)
	if err != nil {
		http.Error(w, "failed to create proxy request", http.StatusInternalServerError)
		return
	}

	resp, err := memoryProxyClient.Do(req)
	if err != nil {
		http.Error(w, "shared memory server unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (s *Server) getEnsembleSharedMemoryProvenance(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	pp := &sympoziumv1alpha1.Ensemble{}
	if err := s.client.Get(r.Context(), client.ObjectKey{Name: name, Namespace: ns}, pp); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "Ensemble not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if pp.Spec.SharedMemory == nil || !pp.Spec.SharedMemory.Enabled {
		http.Error(w, "shared memory not enabled for this ensemble", http.StatusNotFound)
		return
	}

	entryID := r.PathValue("id")
	provenanceURL := fmt.Sprintf("http://%s-shared-memory.%s.svc:8080/provenance?id=%s", name, ns, entryID)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", provenanceURL, nil)
	if err != nil {
		http.Error(w, "failed to create proxy request", http.StatusInternalServerError)
		return
	}

	resp, err := memoryProxyClient.Do(req)
	if err != nil {
		http.Error(w, "shared memory server unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (s *Server) triggerStimulus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	// Fetch the ensemble.
	var ensemble sympoziumv1alpha1.Ensemble
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &ensemble); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "ensemble not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Validate stimulus is configured.
	if ensemble.Spec.Stimulus == nil {
		http.Error(w, "no stimulus configured for this ensemble", http.StatusBadRequest)
		return
	}

	// Resolve stimulus target from relationships.
	targetPersona, err := controller.ResolveStimulusTarget(&ensemble)
	if err != nil {
		http.Error(w, "no stimulus relationship found", http.StatusBadRequest)
		return
	}

	targetAgentName := name + "-" + targetPersona

	// Look up the target agent.
	var targetInst sympoziumv1alpha1.Agent
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: targetAgentName, Namespace: ns}, &targetInst); err != nil {
		http.Error(w, fmt.Sprintf("stimulus target agent %q not found", targetAgentName), http.StatusNotFound)
		return
	}

	// Same builder the controller's readiness path uses, so a manual trigger
	// produces an identical run — including the agent config's ToolPolicy.
	agentRun := controller.BuildStimulusRun(
		r.Context(), s.client, &ensemble, &targetInst, targetPersona,
		controller.StimulusTriggerSourceManual, time.Now(),
	)
	runName := agentRun.Name

	if err := s.client.Create(r.Context(), agentRun); err != nil {
		http.Error(w, "failed to create stimulus run: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Update ensemble status. Marking the stimulus delivered matters for a
	// manual trigger fired before the ensemble finished coming up: without it
	// the controller still sees an undelivered stimulus, hits the readiness
	// edge moments later, and fires a second run for work already in flight.
	ensemble.Status.StimulusGeneration++
	ensemble.Status.StimulusDelivered = true
	if err := s.client.Status().Update(r.Context(), &ensemble); err != nil {
		s.log.Error(err, "Failed to update ensemble stimulus generation", "ensemble", name)
	}

	// Publish event.
	if s.eventBus != nil {
		s.eventBus.Publish(r.Context(), eventbus.TopicStimulusDelivered, &eventbus.Event{
			Topic:     eventbus.TopicStimulusDelivered,
			Timestamp: time.Now(),
			Metadata: map[string]string{
				"ensemble":      name,
				"target":        targetPersona,
				"triggerSource": controller.StimulusTriggerSourceManual,
				"runName":       runName,
			},
		})
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]string{
		"runName":  runName,
		"target":   targetPersona,
		"stimulus": ensemble.Spec.Stimulus.Name,
	})
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

// createOrUpdateSecret creates a secret, or updates it if it already exists.
func createOrUpdateSecret(ctx context.Context, c client.Client, secret *corev1.Secret) error {
	createErr := c.Create(ctx, secret)
	if createErr == nil {
		return nil
	}
	if !k8serrors.IsAlreadyExists(createErr) {
		return createErr
	}
	existing := &corev1.Secret{}
	if err := c.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, existing); err != nil {
		return err
	}
	if existing.Data == nil {
		existing.Data = map[string][]byte{}
	}
	for k, v := range secret.StringData {
		existing.Data[k] = []byte(v)
	}
	return c.Update(ctx, existing)
}

func defaultProviderSecretName(resourceName, provider string) string {
	provider = strings.TrimSpace(strings.ToLower(provider))
	if provider == "" {
		return resourceName + "-credentials"
	}
	return fmt.Sprintf("%s-%s-key", resourceName, provider)
}

// defaultProviderBaseURL returns the conventional default base URL for
// keyless local providers (ollama, lm-studio). Returns "" for cloud providers.
func defaultProviderBaseURL(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "ollama":
		return "http://ollama.default.svc:11434/v1"
	case "lm-studio":
		return "http://localhost:1234/v1"
	case "llama-server":
		return "http://localhost:8080/v1"
	default:
		return ""
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

type PodInfo struct {
	Name         string            `json:"name"`
	Namespace    string            `json:"namespace"`
	Phase        string            `json:"phase"`
	NodeName     string            `json:"nodeName,omitempty"`
	PodIP        string            `json:"podIP,omitempty"`
	StartTime    *metav1.Time      `json:"startTime,omitempty"`
	RestartCount int32             `json:"restartCount"`
	AgentRef     string            `json:"agentRef,omitempty"`
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
			AgentRef:     inst,
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

type ObservabilityMetricsResponse struct {
	CollectorReachable bool               `json:"collectorReachable"`
	CollectorError     string             `json:"collectorError,omitempty"`
	CollectedAt        string             `json:"collectedAt"`
	Namespace          string             `json:"namespace"`
	AgentRunsTotal     float64            `json:"agentRunsTotal"`
	InputTokensTotal   float64            `json:"inputTokensTotal"`
	OutputTokensTotal  float64            `json:"outputTokensTotal"`
	ToolInvocations    float64            `json:"toolInvocations"`
	RunStatus          map[string]float64 `json:"runStatus,omitempty"`
	InputByModel       []MetricBreakdown  `json:"inputByModel,omitempty"`
	OutputByModel      []MetricBreakdown  `json:"outputByModel,omitempty"`
	ToolsByName        []MetricBreakdown  `json:"toolsByName,omitempty"`
	RawMetricNames     []string           `json:"rawMetricNames,omitempty"`
}

type MetricBreakdown struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

type promSample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

func (s *Server) getObservabilityMetrics(w http.ResponseWriter, r *http.Request) {
	namespace := r.URL.Query().Get("namespace")
	if namespace == "" {
		namespace = "default"
	}

	resp := ObservabilityMetricsResponse{
		CollectorReachable: false,
		CollectedAt:        time.Now().UTC().Format(time.RFC3339),
		Namespace:          namespace,
		RunStatus:          map[string]float64{},
	}

	// Check collector connectivity.
	raw, err := s.readCollectorMetrics(r.Context())
	if err == nil {
		resp.CollectorReachable = true
	} else {
		resp.CollectorError = err.Error()
	}

	// Always aggregate from AgentRun CRDs — this is the source of truth.
	var runs sympoziumv1alpha1.AgentRunList
	if err := s.client.List(r.Context(), &runs, client.InNamespace(namespace)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	inputByModel := map[string]float64{}
	outputByModel := map[string]float64{}
	toolsByName := map[string]float64{}

	for i := range runs.Items {
		run := runs.Items[i]
		resp.AgentRunsTotal++
		phase := strings.TrimSpace(strings.ToLower(string(run.Status.Phase)))
		if phase == "" {
			phase = "unknown"
		}
		resp.RunStatus[phase]++

		model := strings.TrimSpace(run.Spec.Model.Model)
		if model == "" {
			model = "unknown"
		}
		if run.Status.TokenUsage != nil {
			in := float64(run.Status.TokenUsage.InputTokens)
			out := float64(run.Status.TokenUsage.OutputTokens)
			tools := float64(run.Status.TokenUsage.ToolCalls)
			resp.InputTokensTotal += in
			resp.OutputTokensTotal += out
			resp.ToolInvocations += tools
			inputByModel[model] += in
			outputByModel[model] += out
			if tools > 0 {
				toolsByName["tool_calls"] += tools
			}
		}
	}

	resp.InputByModel = mapToMetricBreakdown(inputByModel)
	resp.OutputByModel = mapToMetricBreakdown(outputByModel)
	resp.ToolsByName = mapToMetricBreakdown(toolsByName)

	// Include raw metric names from the collector if available.
	if resp.CollectorReachable {
		samples := parsePrometheusSamples(raw)
		metricNames := map[string]struct{}{}
		for _, sample := range samples {
			metricNames[sample.Name] = struct{}{}
		}
		names := make([]string, 0, len(metricNames))
		for n := range metricNames {
			names = append(names, n)
		}
		sort.Strings(names)
		resp.RawMetricNames = names
	} else {
		resp.RawMetricNames = []string{"sympozium.agent.runs", "gen_ai.usage.input_tokens", "gen_ai.usage.output_tokens", "sympozium.tool.invocations"}
	}

	writeJSON(w, resp)
}

func mapToMetricBreakdown(m map[string]float64) []MetricBreakdown {
	out := make([]MetricBreakdown, 0, len(m))
	for k, v := range m {
		out = append(out, MetricBreakdown{Label: k, Value: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Value == out[j].Value {
			return out[i].Label < out[j].Label
		}
		return out[i].Value > out[j].Value
	})
	return out
}

func (s *Server) readCollectorMetrics(ctx context.Context) (string, error) {
	if s.kube == nil {
		return "", fmt.Errorf("kubernetes client unavailable")
	}

	collectorURL := os.Getenv("SYMPOZIUM_COLLECTOR_METRICS_URL")
	if collectorURL == "" {
		collectorURL = "http://sympozium-otel-collector.sympozium-system.svc.cluster.local:8889/metrics"
	}

	httpClient := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		collectorURL,
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create collector metrics request: %w", err)
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to query collector metrics: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("collector metrics request failed: HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(res.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read collector metrics body: %w", err)
	}
	return string(raw), nil
}

func parsePrometheusSamples(raw string) []promSample {
	out := []promSample{}
	scanner := bufio.NewScanner(strings.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		name, labels := parsePromMetricSelector(fields[0])
		out = append(out, promSample{Name: name, Labels: labels, Value: value})
	}
	return out
}

func parsePromMetricSelector(selector string) (string, map[string]string) {
	start := strings.Index(selector, "{")
	end := strings.LastIndex(selector, "}")
	if start < 0 || end < 0 || end <= start {
		return selector, map[string]string{}
	}
	name := selector[:start]
	return name, parsePromLabels(selector[start+1 : end])
}

func parsePromLabels(raw string) map[string]string {
	out := map[string]string{}
	for _, part := range splitCommaRespectQuotes(raw) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		k := strings.TrimSpace(kv[0])
		v := strings.TrimSpace(kv[1])
		v = strings.Trim(v, "\"")
		v = strings.ReplaceAll(v, `\"`, `"`)
		if k != "" {
			out[k] = v
		}
	}
	return out
}

func splitCommaRespectQuotes(s string) []string {
	parts := []string{}
	var b strings.Builder
	inQuotes := false
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			b.WriteRune(r)
			escaped = true
		case r == '"':
			b.WriteRune(r)
			inQuotes = !inQuotes
		case r == ',' && !inQuotes:
			part := strings.TrimSpace(b.String())
			if part != "" {
				parts = append(parts, part)
			}
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	last := strings.TrimSpace(b.String())
	if last != "" {
		parts = append(parts, last)
	}
	return parts
}

func (s *Server) getRunTelemetry(w http.ResponseWriter, r *http.Request) {
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

	writeJSON(w, map[string]interface{}{
		"runName":   name,
		"namespace": ns,
		"podName":   run.Status.PodName,
		"phase":     run.Status.Phase,
		"traceIds":  []string{},
		"events":    []interface{}{},
	})
}

type InstallDefaultEnsemblesResponse struct {
	SourceNamespace string   `json:"sourceNamespace"`
	TargetNamespace string   `json:"targetNamespace"`
	Copied          []string `json:"copied"`
	AlreadyPresent  []string `json:"alreadyPresent"`
}

func (s *Server) installDefaultEnsembles(w http.ResponseWriter, r *http.Request) {
	targetNS := r.URL.Query().Get("namespace")
	if targetNS == "" {
		targetNS = "default"
	}
	sourceNS := "sympozium-system"

	var sourceList sympoziumv1alpha1.EnsembleList
	if err := s.client.List(r.Context(), &sourceList, client.InNamespace(sourceNS)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := InstallDefaultEnsemblesResponse{SourceNamespace: sourceNS, TargetNamespace: targetNS, Copied: []string{}, AlreadyPresent: []string{}}
	for _, src := range sourceList.Items {
		var existing sympoziumv1alpha1.Ensemble
		err := s.client.Get(r.Context(), types.NamespacedName{Name: src.Name, Namespace: targetNS}, &existing)
		if err == nil {
			resp.AlreadyPresent = append(resp.AlreadyPresent, src.Name)
			continue
		}
		if !k8serrors.IsNotFound(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		pack := &sympoziumv1alpha1.Ensemble{ObjectMeta: metav1.ObjectMeta{Name: src.Name, Namespace: targetNS, Labels: src.Labels, Annotations: src.Annotations}, Spec: src.Spec}
		pack.Spec.Enabled = false
		if err := s.client.Create(r.Context(), pack); err != nil {
			if k8serrors.IsAlreadyExists(err) {
				resp.AlreadyPresent = append(resp.AlreadyPresent, src.Name)
				continue
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Copied = append(resp.Copied, src.Name)
	}

	writeJSON(w, resp)
}

// ── MCP Server defaults & auth ──────────────────────────────────────────────

type InstallDefaultMCPServersResponse struct {
	SourceNamespace string   `json:"sourceNamespace"`
	TargetNamespace string   `json:"targetNamespace"`
	Copied          []string `json:"copied"`
	AlreadyPresent  []string `json:"alreadyPresent"`
}

func (s *Server) installDefaultMCPServers(w http.ResponseWriter, r *http.Request) {
	targetNS := r.URL.Query().Get("namespace")
	if targetNS == "" {
		targetNS = "default"
	}
	sourceNS := "sympozium-system"

	var sourceList sympoziumv1alpha1.MCPServerList
	if err := s.client.List(r.Context(), &sourceList, client.InNamespace(sourceNS)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := InstallDefaultMCPServersResponse{SourceNamespace: sourceNS, TargetNamespace: targetNS, Copied: []string{}, AlreadyPresent: []string{}}
	for _, src := range sourceList.Items {
		var existing sympoziumv1alpha1.MCPServer
		err := s.client.Get(r.Context(), types.NamespacedName{Name: src.Name, Namespace: targetNS}, &existing)
		if err == nil {
			resp.AlreadyPresent = append(resp.AlreadyPresent, src.Name)
			continue
		}
		if !k8serrors.IsNotFound(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		mcp := &sympoziumv1alpha1.MCPServer{
			ObjectMeta: metav1.ObjectMeta{Name: src.Name, Namespace: targetNS, Labels: src.Labels, Annotations: src.Annotations},
			Spec:       src.Spec,
		}
		mcp.Spec.Suspended = true
		if err := s.client.Create(r.Context(), mcp); err != nil {
			if k8serrors.IsAlreadyExists(err) {
				resp.AlreadyPresent = append(resp.AlreadyPresent, src.Name)
				continue
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp.Copied = append(resp.Copied, src.Name)
	}

	writeJSON(w, resp)
}

// mcpServerAuthTokenRequest is the request body for setting an MCP server's auth token.
type mcpServerAuthTokenRequest struct {
	Token string `json:"token"`
}

type mcpServerAuthStatusResponse struct {
	Status     string `json:"status"` // "idle" or "complete"
	SecretName string `json:"secretName"`
}

// mcpServerSecretName returns the conventional Secret name for an MCP server.
func mcpServerSecretName(mcpName string) string {
	return "mcp-" + mcpName + "-token"
}

func (s *Server) handleMCPServerAuthStatus(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	secretName := mcpServerSecretName(name)
	status := "idle"

	existing := &corev1.Secret{}
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: secretName, Namespace: ns}, existing); err == nil {
		if len(existing.Data) > 0 {
			status = "complete"
		}
	}

	writeJSON(w, mcpServerAuthStatusResponse{Status: status, SecretName: secretName})
}

func (s *Server) handleMCPServerAuthToken(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}

	var req mcpServerAuthTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	token := strings.TrimSpace(req.Token)
	if token == "" {
		http.Error(w, "token is required", http.StatusBadRequest)
		return
	}

	secretName := mcpServerSecretName(name)
	secretKey := types.NamespacedName{Name: secretName, Namespace: ns}

	existing := &corev1.Secret{}
	err := s.client.Get(r.Context(), secretKey, existing)
	if k8serrors.IsNotFound(err) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: ns,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "sympozium",
					"app.kubernetes.io/component":  "mcp-server-secret",
					"sympozium.ai/mcpserver":       name,
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				"GITHUB_PERSONAL_ACCESS_TOKEN": []byte(token),
			},
		}
		if err := s.client.Create(r.Context(), secret); err != nil {
			http.Error(w, fmt.Sprintf("failed to create secret: %v", err), http.StatusInternalServerError)
			return
		}
	} else if err != nil {
		http.Error(w, fmt.Sprintf("failed to get secret: %v", err), http.StatusInternalServerError)
		return
	} else {
		patch := client.MergeFrom(existing.DeepCopy())
		if existing.Data == nil {
			existing.Data = make(map[string][]byte)
		}
		existing.Data["GITHUB_PERSONAL_ACCESS_TOKEN"] = []byte(token)
		if err := s.client.Patch(r.Context(), existing, patch); err != nil {
			http.Error(w, fmt.Sprintf("failed to update secret: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Ensure the MCPServer's deployment references this secret.
	var mcp sympoziumv1alpha1.MCPServer
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &mcp); err == nil {
		if mcp.Spec.Deployment != nil {
			hasRef := false
			for _, ref := range mcp.Spec.Deployment.SecretRefs {
				if ref.Name == secretName {
					hasRef = true
					break
				}
			}
			if !hasRef {
				mcpPatch := client.MergeFrom(mcp.DeepCopy())
				mcp.Spec.Deployment.SecretRefs = append(mcp.Spec.Deployment.SecretRefs, sympoziumv1alpha1.MCPSecretRef{Name: secretName})
				_ = s.client.Patch(r.Context(), &mcp, mcpPatch)
			}
		}
	}

	writeJSON(w, mcpServerAuthStatusResponse{Status: "complete", SecretName: secretName})
}

// GatewayConfigResponse is the response for gateway config endpoints.
type GatewayConfigResponse struct {
	Enabled                  bool   `json:"enabled"`
	GatewayClassName         string `json:"gatewayClassName,omitempty"`
	Name                     string `json:"name,omitempty"`
	BaseDomain               string `json:"baseDomain,omitempty"`
	TLSEnabled               bool   `json:"tlsEnabled"`
	CertManagerClusterIssuer string `json:"certManagerClusterIssuer,omitempty"`
	TLSSecretName            string `json:"tlsSecretName,omitempty"`
	// Status fields
	Phase         string `json:"phase,omitempty"`
	Ready         bool   `json:"ready"`
	Address       string `json:"address,omitempty"`
	ListenerCount int    `json:"listenerCount,omitempty"`
	Message       string `json:"message,omitempty"`
}

// PatchGatewayConfigRequest is the request body for patching gateway config.
type PatchGatewayConfigRequest struct {
	Enabled                  *bool   `json:"enabled,omitempty"`
	GatewayClassName         *string `json:"gatewayClassName,omitempty"`
	Name                     *string `json:"name,omitempty"`
	BaseDomain               *string `json:"baseDomain,omitempty"`
	TLSEnabled               *bool   `json:"tlsEnabled,omitempty"`
	CertManagerClusterIssuer *string `json:"certManagerClusterIssuer,omitempty"`
	TLSSecretName            *string `json:"tlsSecretName,omitempty"`
}

func gatewayConfigResponseFromCR(config *sympoziumv1alpha1.SympoziumConfig) GatewayConfigResponse {
	resp := GatewayConfigResponse{
		Phase: config.Status.Phase,
	}
	if config.Status.Gateway != nil {
		resp.Ready = config.Status.Gateway.Ready
		resp.Address = config.Status.Gateway.Address
		resp.ListenerCount = config.Status.Gateway.ListenerCount
	}
	for _, c := range config.Status.Conditions {
		if c.Type == "Ready" && c.Message != "" {
			resp.Message = c.Message
			break
		}
	}
	if gw := config.Spec.Gateway; gw != nil {
		resp.Enabled = gw.Enabled
		resp.GatewayClassName = gw.GatewayClassName
		resp.Name = gw.Name
		resp.BaseDomain = gw.BaseDomain
		if gw.TLS != nil {
			resp.TLSEnabled = gw.TLS.Enabled
			resp.CertManagerClusterIssuer = gw.TLS.CertManagerClusterIssuer
			resp.TLSSecretName = gw.TLS.SecretName
		}
	}
	return resp
}

// configNamespace returns the namespace for SympoziumConfig operations.
// The config is a platform-wide singleton that lives in sympozium-system.
func configNamespace(r *http.Request) string {
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		return ns
	}
	return "sympozium-system"
}

func (s *Server) getGatewayConfig(w http.ResponseWriter, r *http.Request) {
	ns := configNamespace(r)

	var config sympoziumv1alpha1.SympoziumConfig
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: "default", Namespace: ns}, &config); err != nil {
		if k8serrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			// Return empty/disabled state (handles both missing CR and missing CRD)
			writeJSON(w, GatewayConfigResponse{})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, gatewayConfigResponseFromCR(&config))
}

func (s *Server) createGatewayConfig(w http.ResponseWriter, r *http.Request) {
	ns := configNamespace(r)

	var req PatchGatewayConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	config := sympoziumv1alpha1.SympoziumConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "default",
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.SympoziumConfigSpec{
			Gateway: &sympoziumv1alpha1.GatewaySpec{},
		},
	}
	applyGatewayPatch(config.Spec.Gateway, &req)

	if err := s.client.Create(r.Context(), &config); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			http.Error(w, "gateway config already exists, use PATCH to update", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, gatewayConfigResponseFromCR(&config))
}

func (s *Server) patchGatewayConfig(w http.ResponseWriter, r *http.Request) {
	ns := configNamespace(r)

	var req PatchGatewayConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var config sympoziumv1alpha1.SympoziumConfig
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: "default", Namespace: ns}, &config); err != nil {
		if k8serrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			http.Error(w, "gateway config not found, use POST to create", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if config.Spec.Gateway == nil {
		config.Spec.Gateway = &sympoziumv1alpha1.GatewaySpec{}
	}
	applyGatewayPatch(config.Spec.Gateway, &req)

	if err := s.client.Update(r.Context(), &config); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, gatewayConfigResponseFromCR(&config))
}

func (s *Server) deleteGatewayConfig(w http.ResponseWriter, r *http.Request) {
	ns := configNamespace(r)

	var config sympoziumv1alpha1.SympoziumConfig
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: "default", Namespace: ns}, &config); err != nil {
		if k8serrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			http.Error(w, "gateway config not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.client.Delete(r.Context(), &config); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func applyGatewayPatch(gw *sympoziumv1alpha1.GatewaySpec, req *PatchGatewayConfigRequest) {
	if req.Enabled != nil {
		gw.Enabled = *req.Enabled
	}
	if req.GatewayClassName != nil {
		gw.GatewayClassName = *req.GatewayClassName
	}
	if req.Name != nil {
		gw.Name = *req.Name
	}
	if req.BaseDomain != nil {
		gw.BaseDomain = *req.BaseDomain
	}
	if req.TLSEnabled != nil || req.CertManagerClusterIssuer != nil || req.TLSSecretName != nil {
		if gw.TLS == nil {
			gw.TLS = &sympoziumv1alpha1.GatewayTLSSpec{}
		}
		if req.TLSEnabled != nil {
			gw.TLS.Enabled = *req.TLSEnabled
		}
		if req.CertManagerClusterIssuer != nil {
			gw.TLS.CertManagerClusterIssuer = *req.CertManagerClusterIssuer
		}
		if req.TLSSecretName != nil {
			gw.TLS.SecretName = *req.TLSSecretName
		}
	}
}

// ── System Canary endpoints ─────────────────────────────────────────────────

// CanaryConfigResponse is the response for GET /api/v1/canary.
type CanaryConfigResponse struct {
	Enabled         bool          `json:"enabled"`
	Interval        string        `json:"interval,omitempty"`
	Model           string        `json:"model,omitempty"`
	Provider        string        `json:"provider,omitempty"`
	BaseURL         string        `json:"baseURL,omitempty"`
	AuthSecretRef   string        `json:"authSecretRef,omitempty"`
	EnsembleCreated bool          `json:"ensembleCreated"`
	LastRunPhase    string        `json:"lastRunPhase,omitempty"`
	LastRunTime     string        `json:"lastRunTime,omitempty"`
	HealthStatus    string        `json:"healthStatus,omitempty"`
	LastRunResult   string        `json:"lastRunResult,omitempty"`
	Checks          []CanaryCheck `json:"checks,omitempty"`
}

// CanaryCheck is a single health check result parsed from the canary report.
type CanaryCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // "pass" or "fail"
	Details string `json:"details"`
}

// PatchCanaryConfigRequest is the request body for PATCH /api/v1/canary.
type PatchCanaryConfigRequest struct {
	Enabled       *bool   `json:"enabled,omitempty"`
	Interval      *string `json:"interval,omitempty"`
	Model         *string `json:"model,omitempty"`
	Provider      *string `json:"provider,omitempty"`
	BaseURL       *string `json:"baseURL,omitempty"`
	AuthSecretRef *string `json:"authSecretRef,omitempty"`
}

func (s *Server) getCanaryConfig(w http.ResponseWriter, r *http.Request) {
	ns := configNamespace(r)

	var config sympoziumv1alpha1.SympoziumConfig
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: "default", Namespace: ns}, &config); err != nil {
		if k8serrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			writeJSON(w, CanaryConfigResponse{})
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := CanaryConfigResponse{}
	if c := config.Spec.Canary; c != nil {
		resp.Enabled = c.Enabled
		resp.Interval = c.Interval
		resp.Model = c.Model
		resp.Provider = c.Provider
		resp.BaseURL = c.BaseURL
		resp.AuthSecretRef = c.AuthSecretRef
	}
	if c := config.Status.Canary; c != nil {
		resp.EnsembleCreated = c.EnsembleCreated
		resp.LastRunPhase = c.LastRunPhase
		resp.LastRunTime = c.LastRunTime
		resp.HealthStatus = c.HealthStatus
	}

	// Fetch latest canary run status directly (more current than controller status)
	if resp.Enabled {
		var runs sympoziumv1alpha1.AgentRunList
		if err := s.client.List(r.Context(), &runs,
			client.InNamespace(ns),
			client.MatchingLabels{"sympozium.ai/instance": "system-canary-canary"},
		); err == nil && len(runs.Items) > 0 {
			// Sort to find the latest run
			sort.Slice(runs.Items, func(i, j int) bool {
				return runs.Items[j].CreationTimestamp.Before(&runs.Items[i].CreationTimestamp)
			})
			latest := &runs.Items[0]

			// Override phase from live run data (more current than controller status)
			resp.LastRunPhase = string(latest.Status.Phase)
			if latest.Status.CompletedAt != nil {
				resp.LastRunTime = latest.Status.CompletedAt.Format("2006-01-02T15:04:05Z")
			}

			// Use result if available
			if latest.Status.Result != "" {
				resp.LastRunResult = latest.Status.Result

				// Try structured JSON first (canary mode).
				var structured struct {
					Overall string `json:"overall"`
					Checks  []struct {
						Name    string `json:"name"`
						Status  string `json:"status"`
						Details string `json:"details"`
					} `json:"checks"`
				}
				if json.Unmarshal([]byte(latest.Status.Result), &structured) == nil && structured.Overall != "" {
					resp.HealthStatus = structured.Overall
					for _, c := range structured.Checks {
						resp.Checks = append(resp.Checks, CanaryCheck{
							Name:    c.Name,
							Status:  c.Status,
							Details: c.Details,
						})
					}
				} else {
					// Fallback: legacy markdown parsing.
					upper := strings.ToUpper(latest.Status.Result)
					switch {
					case strings.Contains(upper, "UNHEALTHY"):
						resp.HealthStatus = "unhealthy"
					case strings.Contains(upper, "DEGRADED"):
						resp.HealthStatus = "degraded"
					case strings.Contains(upper, "HEALTHY"):
						resp.HealthStatus = "healthy"
					}
					resp.Checks = parseCanaryChecks(latest.Status.Result)
				}
			} else if latest.Status.Phase == "Failed" {
				resp.HealthStatus = "unhealthy"
			}
		}
	}

	writeJSON(w, resp)
}

// parseCanaryChecks extracts individual check rows from the canary markdown table.
// It looks for rows matching "| <name> | PASS/FAIL | <details> |".
func parseCanaryChecks(result string) []CanaryCheck {
	var checks []CanaryCheck
	for _, line := range strings.Split(result, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "|--") || strings.HasPrefix(line, "| Check") {
			continue
		}
		parts := strings.Split(line, "|")
		// Expect: "", name, status, details, ""
		if len(parts) < 4 {
			continue
		}
		name := strings.TrimSpace(parts[1])
		status := strings.TrimSpace(parts[2])
		details := strings.TrimSpace(parts[3])
		if name == "" || name == "Check" {
			continue
		}
		upper := strings.ToUpper(status)
		if upper != "PASS" && upper != "FAIL" {
			continue
		}
		checks = append(checks, CanaryCheck{
			Name:    name,
			Status:  strings.ToLower(upper),
			Details: details,
		})
	}
	return checks
}

func (s *Server) patchCanaryConfig(w http.ResponseWriter, r *http.Request) {
	ns := configNamespace(r)

	var req PatchCanaryConfigRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	var config sympoziumv1alpha1.SympoziumConfig
	created := false
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: "default", Namespace: ns}, &config); err != nil {
		if !k8serrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Auto-create the SympoziumConfig CR
		config = sympoziumv1alpha1.SympoziumConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "default",
				Namespace: ns,
			},
			Spec: sympoziumv1alpha1.SympoziumConfigSpec{
				Canary: &sympoziumv1alpha1.CanarySpec{},
			},
		}
		created = true
	}

	if config.Spec.Canary == nil {
		config.Spec.Canary = &sympoziumv1alpha1.CanarySpec{}
	}
	if req.Enabled != nil {
		config.Spec.Canary.Enabled = *req.Enabled
	}
	if req.Interval != nil {
		config.Spec.Canary.Interval = *req.Interval
	}
	if req.Model != nil {
		config.Spec.Canary.Model = *req.Model
	}
	if req.Provider != nil {
		config.Spec.Canary.Provider = *req.Provider
	}
	if req.BaseURL != nil {
		config.Spec.Canary.BaseURL = *req.BaseURL
	}
	if req.AuthSecretRef != nil {
		config.Spec.Canary.AuthSecretRef = *req.AuthSecretRef
	}

	if created {
		if err := s.client.Create(r.Context(), &config); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else if err := s.client.Update(r.Context(), &config); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := CanaryConfigResponse{
		Enabled:       config.Spec.Canary.Enabled,
		Interval:      config.Spec.Canary.Interval,
		Model:         config.Spec.Canary.Model,
		Provider:      config.Spec.Canary.Provider,
		BaseURL:       config.Spec.Canary.BaseURL,
		AuthSecretRef: config.Spec.Canary.AuthSecretRef,
	}
	if c := config.Status.Canary; c != nil {
		resp.EnsembleCreated = c.EnsembleCreated
		resp.LastRunPhase = c.LastRunPhase
		resp.LastRunTime = c.LastRunTime
		resp.HealthStatus = c.HealthStatus
	}
	writeJSON(w, resp)
}

// GatewayMetricsResponse is the response for the gateway metrics endpoint.
type GatewayMetricsResponse struct {
	TotalRequests    int             `json:"totalRequests"`
	SuccessCount     int             `json:"successCount"`
	ErrorCount       int             `json:"errorCount"`
	SkippedCount     int             `json:"skippedCount"`
	AvgDurationSec   float64         `json:"avgDurationSec"`
	UptimeSec        int64           `json:"uptimeSec"`
	ServingInstances int             `json:"servingInstances"`
	Buckets          []GatewayBucket `json:"buckets"`
}

// GatewayBucket is a single time bucket in the gateway metrics timeseries.
type GatewayBucket struct {
	Timestamp   int64   `json:"ts"`
	Requests    int     `json:"requests"`
	Errors      int     `json:"errors"`
	AvgDuration float64 `json:"avgDurationSec"`
}

func (s *Server) getGatewayMetrics(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = "default"
	}
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}

	var window time.Duration
	var bucketSize time.Duration
	switch rangeParam {
	case "1h":
		window = 1 * time.Hour
		bucketSize = 5 * time.Minute
	case "7d":
		window = 7 * 24 * time.Hour
		bucketSize = 24 * time.Hour
	default: // "24h"
		window = 24 * time.Hour
		bucketSize = 1 * time.Hour
	}

	now := time.Now().UTC()
	cutoff := now.Add(-window)

	// List web-proxy AgentRuns.
	var runs sympoziumv1alpha1.AgentRunList
	if err := s.client.List(r.Context(), &runs,
		client.InNamespace(ns),
		client.MatchingLabels{"sympozium.ai/source": "web-proxy"},
	); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Initialize buckets.
	numBuckets := int(window / bucketSize)
	buckets := make([]GatewayBucket, numBuckets)
	bucketDurTotal := make([]float64, numBuckets)
	bucketDurCount := make([]int, numBuckets)
	for i := range buckets {
		buckets[i].Timestamp = cutoff.Add(time.Duration(i) * bucketSize).UnixMilli()
	}

	resp := GatewayMetricsResponse{Buckets: buckets}
	var durTotal float64
	var durCount int

	for i := range runs.Items {
		run := &runs.Items[i]
		created := run.CreationTimestamp.Time
		if created.Before(cutoff) {
			continue
		}

		resp.TotalRequests++
		isFailed := run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseFailed
		switch {
		case isFailed:
			resp.ErrorCount++
		case run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseSkipped:
			resp.SkippedCount++
		default:
			resp.SuccessCount++
		}

		var durSec float64
		if run.Status.TokenUsage != nil && run.Status.TokenUsage.DurationMs > 0 {
			durSec = float64(run.Status.TokenUsage.DurationMs) / 1000.0
			durTotal += durSec
			durCount++
		}

		// Place into bucket.
		idx := int(created.Sub(cutoff) / bucketSize)
		if idx >= numBuckets {
			idx = numBuckets - 1
		}
		if idx >= 0 {
			buckets[idx].Requests++
			if isFailed {
				buckets[idx].Errors++
			}
			if durSec > 0 {
				bucketDurTotal[idx] += durSec
				bucketDurCount[idx]++
			}
		}
	}

	// Compute bucket averages.
	for i := range buckets {
		if bucketDurCount[i] > 0 {
			buckets[i].AvgDuration = bucketDurTotal[i] / float64(bucketDurCount[i])
		}
	}

	if durCount > 0 {
		resp.AvgDurationSec = durTotal / float64(durCount)
	}

	// Count serving instances and compute uptime.
	var allRuns sympoziumv1alpha1.AgentRunList
	if err := s.client.List(r.Context(), &allRuns,
		client.InNamespace(ns),
		client.MatchingLabels{"sympozium.ai/source": "web-proxy"},
	); err == nil {
		for i := range allRuns.Items {
			run := &allRuns.Items[i]
			if run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseServing {
				resp.ServingInstances++
				uptime := int64(now.Sub(run.CreationTimestamp.Time).Seconds())
				if uptime > resp.UptimeSec {
					resp.UptimeSec = uptime
				}
			}
		}
	}

	writeJSON(w, resp)
}

// ClusterInfoResponse is the response for GET /api/v1/cluster.
type ClusterInfoResponse struct {
	Nodes      int    `json:"nodes"`
	Namespaces int    `json:"namespaces"`
	Pods       int    `json:"pods"`
	Version    string `json:"version,omitempty"`
}

func (s *Server) getClusterInfo(w http.ResponseWriter, r *http.Request) {
	var resp ClusterInfoResponse

	// Count nodes
	var nodeList corev1.NodeList
	if err := s.client.List(r.Context(), &nodeList); err == nil {
		resp.Nodes = len(nodeList.Items)
	}

	// Count all namespaces
	var nsList corev1.NamespaceList
	if err := s.client.List(r.Context(), &nsList); err == nil {
		resp.Namespaces = len(nsList.Items)
	}

	// Count all pods cluster-wide
	var podList corev1.PodList
	if err := s.client.List(r.Context(), &podList); err == nil {
		resp.Pods = len(podList.Items)
	}

	// Get cluster version
	if s.kube != nil {
		if info, err := s.kube.Discovery().ServerVersion(); err == nil {
			resp.Version = info.GitVersion
		}
	}

	writeJSON(w, resp)
}

// ── Capabilities endpoint ────────────────────────────────────────────────────

// CapabilityStatus describes whether a feature is available in the cluster.
type CapabilityStatus struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// CapabilitiesResponse lists optional features and whether their prerequisites are met.
type CapabilitiesResponse struct {
	AgentSandbox CapabilityStatus `json:"agentSandbox"`
}

func (s *Server) getCapabilities(w http.ResponseWriter, r *http.Request) {
	resp := CapabilitiesResponse{}

	// Check if Agent Sandbox CRDs (agents.x-k8s.io) are installed.
	// We use ServerResourcesForGroupVersion (targeted query) instead of
	// ServerGroupsAndResources (full scan) because the latter suffers from
	// partial discovery failures: when ANY unrelated API group is unhealthy
	// (e.g. metrics-server), it returns a non-nil error with partial results
	// that silently omit the failed groups. This caused false negatives where
	// installed CRDs were reported as missing.
	// WARNING: This uses the v1alpha1 API group. As the upstream project
	// (kubernetes-sigs/agent-sandbox) graduates, update this string AND the
	// GVRs in internal/controller/agentrun_sandbox.go (see the full list there).
	if s.kube != nil {
		resources, err := s.kube.Discovery().ServerResourcesForGroupVersion("agents.x-k8s.io/v1alpha1")
		if err == nil {
			found := false
			for _, r := range resources.APIResources {
				if r.Name == "sandboxes" {
					found = true
					break
				}
			}
			if found {
				resp.AgentSandbox = CapabilityStatus{Available: true}
			} else {
				resp.AgentSandbox = CapabilityStatus{
					Available: false,
					Reason:    "Agent Sandbox CRDs (agents.x-k8s.io) are not installed. Install kubernetes-sigs/agent-sandbox to enable this feature.",
				}
			}
		} else {
			resp.AgentSandbox = CapabilityStatus{
				Available: false,
				Reason:    "Agent Sandbox CRDs (agents.x-k8s.io) are not installed. Install kubernetes-sigs/agent-sandbox to enable this feature.",
			}
		}
	} else {
		resp.AgentSandbox = CapabilityStatus{
			Available: false,
			Reason:    "Kubernetes client not available",
		}
	}

	writeJSON(w, resp)
}

// ── Provider discovery endpoints ─────────────────────────────────────────────

// ProviderNode describes a node with inference providers discovered by the node-probe DaemonSet.
type ProviderNode struct {
	NodeName  string            `json:"nodeName"`
	NodeIP    string            `json:"nodeIP"`
	Providers []NodeProvider    `json:"providers"`
	Labels    map[string]string `json:"labels,omitempty"`
}

// NodeProvider describes a single inference provider found on a node.
type NodeProvider struct {
	Name      string   `json:"name"`
	Port      int      `json:"port"`
	ProxyPort int      `json:"proxyPort,omitempty"`
	Models    []string `json:"models"`
	LastProbe string   `json:"lastProbe,omitempty"`
}

// ProviderModelsResponse is the response from the model proxy endpoint.
type ProviderModelsResponse struct {
	Models []string `json:"models"`
	Source string   `json:"source"`
}

// listProviderNodes returns nodes annotated by the node-probe DaemonSet with inference providers.
func (s *Server) listProviderNodes(w http.ResponseWriter, r *http.Request) {
	var nodeList corev1.NodeList
	if err := s.client.List(r.Context(), &nodeList); err != nil {
		http.Error(w, "failed to list nodes: "+err.Error(), http.StatusInternalServerError)
		return
	}

	providerFilter := r.URL.Query().Get("provider")

	var result []ProviderNode
	for _, node := range nodeList.Items {
		annotations := node.Annotations
		if annotations == nil {
			continue
		}

		// Only include nodes marked healthy by the probe.
		if annotations["sympozium.ai/inference-healthy"] != "true" {
			continue
		}

		lastProbe := annotations["sympozium.ai/inference-last-probe"]

		// Parse inference annotations to find providers.
		var providers []NodeProvider
		for key, val := range annotations {
			if !strings.HasPrefix(key, "sympozium.ai/inference-") {
				continue
			}
			suffix := strings.TrimPrefix(key, "sympozium.ai/inference-")

			// Skip meta annotations, models annotations, and proxy-port.
			if suffix == "healthy" || suffix == "last-probe" || suffix == "proxy-port" || strings.HasPrefix(suffix, "models-") {
				continue
			}

			providerName := suffix
			if providerFilter != "" && providerName != providerFilter {
				continue
			}

			port := 0
			fmt.Sscanf(val, "%d", &port)
			if port == 0 {
				continue
			}

			var models []string
			if modelsStr, ok := annotations["sympozium.ai/inference-models-"+providerName]; ok && modelsStr != "" {
				models = strings.Split(modelsStr, ",")
			}

			proxyPort := 0
			if pp, ok := annotations["sympozium.ai/inference-proxy-port"]; ok {
				fmt.Sscanf(pp, "%d", &proxyPort)
			}

			providers = append(providers, NodeProvider{
				Name:      providerName,
				Port:      port,
				ProxyPort: proxyPort,
				Models:    models,
				LastProbe: lastProbe,
			})
		}

		if len(providers) == 0 {
			continue
		}

		// Find InternalIP.
		nodeIP := ""
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				nodeIP = addr.Address
				break
			}
		}

		result = append(result, ProviderNode{
			NodeName:  node.Name,
			NodeIP:    nodeIP,
			Providers: providers,
			Labels:    node.Labels,
		})
	}

	if result == nil {
		result = []ProviderNode{}
	}
	writeJSON(w, result)
}

// proxyProviderModels proxies a model listing request to an in-cluster or node-based inference provider.
func (s *Server) proxyProviderModels(w http.ResponseWriter, r *http.Request) {
	baseURL := r.URL.Query().Get("baseURL")
	if baseURL == "" {
		http.Error(w, "baseURL query parameter is required", http.StatusBadRequest)
		return
	}

	// SSRF protection: validate the URL.
	parsed, err := url.Parse(baseURL)
	if err != nil {
		http.Error(w, "invalid baseURL", http.StatusBadRequest)
		return
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		http.Error(w, "baseURL must use http or https scheme", http.StatusBadRequest)
		return
	}

	// Resolve hostname and check for disallowed IPs.
	hostname := parsed.Hostname()
	ips, err := net.LookupHost(hostname)
	if err != nil {
		http.Error(w, "cannot resolve baseURL hostname", http.StatusBadRequest)
		return
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		// Block SSRF targets: link-local (cloud metadata), loopback, private,
		// and unspecified ranges — otherwise this handler can be steered at the
		// kube-apiserver or any in-cluster service. This is a best-effort check;
		// a DNS-rebinding name can still change between resolution here and the
		// client's own re-resolution below.
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() {
			http.Error(w, "baseURL resolves to a disallowed address", http.StatusForbidden)
			return
		}
	}

	provider := r.URL.Query().Get("provider")
	apiKey := r.Header.Get("X-Provider-Api-Key")

	// Determine the models endpoint URL.
	modelsURL := ""
	if provider == "ollama" || strings.Contains(baseURL, ":11434") {
		// Ollama uses /api/tags.
		modelsURL = strings.TrimRight(baseURL, "/")
		// If baseURL ends with /v1, strip it for the Ollama native API.
		modelsURL = strings.TrimSuffix(modelsURL, "/v1")
		modelsURL += "/api/tags"
	} else {
		// OpenAI-compatible: /v1/models.
		modelsURL = strings.TrimRight(baseURL, "/")
		if !strings.HasSuffix(modelsURL, "/v1") {
			modelsURL += "/v1"
		}
		modelsURL += "/models"
	}

	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, modelsURL, nil)
	if err != nil {
		http.Error(w, "failed to build request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "failed to reach provider: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		http.Error(w, fmt.Sprintf("provider returned HTTP %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read provider response", http.StatusBadGateway)
		return
	}

	// Parse models from response.
	models := parseProviderModels(body)

	writeJSON(w, ProviderModelsResponse{
		Models: models,
		Source: "live",
	})
}

// listBedrockModels uses AWS credentials to list available Bedrock foundation models.
func (s *Server) listBedrockModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Region          string `json:"region"`
		AccessKeyID     string `json:"accessKeyId"`
		SecretAccessKey string `json:"secretAccessKey"`
		SessionToken    string `json:"sessionToken,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Region == "" {
		req.Region = "us-east-1"
	}
	if req.AccessKeyID == "" || req.SecretAccessKey == "" {
		http.Error(w, "accessKeyId and secretAccessKey are required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(req.Region),
		awsconfig.WithCredentialsProvider(awscredentials.NewStaticCredentialsProvider(
			req.AccessKeyID, req.SecretAccessKey, req.SessionToken,
		)),
	)
	if err != nil {
		http.Error(w, "failed to configure AWS: "+err.Error(), http.StatusInternalServerError)
		return
	}

	bedrockClient := bedrock.NewFromConfig(cfg)
	output, err := bedrockClient.ListFoundationModels(ctx, &bedrock.ListFoundationModelsInput{})
	if err != nil {
		http.Error(w, "failed to list Bedrock models: "+err.Error(), http.StatusBadGateway)
		return
	}

	var models []string
	for _, m := range output.ModelSummaries {
		if m.ModelId == nil {
			continue
		}
		// Only include models that support text output (conversational).
		hasText := false
		for _, mod := range m.OutputModalities {
			if mod == "TEXT" {
				hasText = true
				break
			}
		}
		if !hasText {
			continue
		}
		models = append(models, *m.ModelId)
	}
	sort.Strings(models)

	writeJSON(w, ProviderModelsResponse{
		Models: models,
		Source: "live",
	})
}

// parseProviderModels extracts model names from a JSON response.
// Supports Ollama format and OpenAI-compatible format.
func parseProviderModels(body []byte) []string {
	// Try Ollama format: {"models":[{"name":"llama3:latest"}]}
	var ollamaResp struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &ollamaResp); err == nil && len(ollamaResp.Models) > 0 {
		names := make([]string, 0, len(ollamaResp.Models))
		for _, m := range ollamaResp.Models {
			name := m.Name
			// Strip ":latest" tag for cleaner display.
			if idx := strings.Index(name, ":"); idx > 0 {
				if name[idx+1:] == "latest" {
					name = name[:idx]
				}
			}
			names = append(names, name)
		}
		sort.Strings(names)
		return names
	}

	// Try OpenAI-compatible format: {"data":[{"id":"model-name"}]}
	var openaiResp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &openaiResp); err == nil && len(openaiResp.Data) > 0 {
		names := make([]string, 0, len(openaiResp.Data))
		for _, m := range openaiResp.Data {
			names = append(names, m.ID)
		}
		sort.Strings(names)
		return names
	}

	return []string{}
}

// --- Node handlers ---

type ClusterNode struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Ready  bool              `json:"ready"`
}

func (s *Server) listClusterNodes(w http.ResponseWriter, r *http.Request) {
	var nodeList corev1.NodeList
	if err := s.client.List(r.Context(), &nodeList); err != nil {
		http.Error(w, "failed to list nodes: "+err.Error(), http.StatusInternalServerError)
		return
	}

	var result []ClusterNode
	for _, node := range nodeList.Items {
		ready := false
		for _, cond := range node.Status.Conditions {
			if cond.Type == corev1.NodeReady && cond.Status == corev1.ConditionTrue {
				ready = true
				break
			}
		}
		result = append(result, ClusterNode{
			Name:   node.Name,
			Labels: node.Labels,
			Ready:  ready,
		})
	}

	writeJSON(w, result)
}

// --- Model handlers ---

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	var list sympoziumv1alpha1.ModelList
	var opts []client.ListOption
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}
	if err := s.client.List(r.Context(), &list, opts...); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	sort.Slice(list.Items, func(i, j int) bool {
		return list.Items[i].Name < list.Items[j].Name
	})
	writeJSON(w, list.Items)
}

func (s *Server) getModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = systemNamespace
	}

	var model sympoziumv1alpha1.Model
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &model); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "model not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, model)
}

type createModelRequest struct {
	Name                   string            `json:"name"`
	ServerType             string            `json:"serverType"` // "llama-cpp", "vllm", "tgi", "custom"
	URL                    string            `json:"url"`
	ModelID                string            `json:"modelID"` // HuggingFace model ID (for vllm/tgi)
	Filename               string            `json:"filename"`
	StorageSize            string            `json:"storageSize"`
	StorageClass           string            `json:"storageClass"`
	GPU                    int               `json:"gpu"`
	Memory                 string            `json:"memory"`
	CPU                    string            `json:"cpu"`
	ContextSize            int               `json:"contextSize"`
	Image                  string            `json:"image"`
	Port                   int32             `json:"port"`
	Args                   []string          `json:"args"`
	NodeSelector           map[string]string `json:"nodeSelector"`
	Placement              string            `json:"placement"`              // "auto" or "manual" (default "manual")
	Namespace              string            `json:"namespace"`              // target namespace (default "sympozium-system")
	HuggingFaceTokenSecret string            `json:"huggingFaceTokenSecret"` // Secret name for HF_TOKEN
}

func (s *Server) createModel(w http.ResponseWriter, r *http.Request) {
	var req createModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	// Validate source based on server type.
	serverType := sympoziumv1alpha1.InferenceServerType(req.ServerType)
	switch serverType {
	case sympoziumv1alpha1.InferenceServerVLLM, sympoziumv1alpha1.InferenceServerTGI:
		if req.ModelID == "" {
			http.Error(w, "modelID is required for vllm/tgi server types", http.StatusBadRequest)
			return
		}
	case sympoziumv1alpha1.InferenceServerCustom:
		// Custom: no source validation.
	default:
		// llama-cpp (default): requires URL.
		if req.URL == "" {
			http.Error(w, "url is required for llama-cpp server type", http.StatusBadRequest)
			return
		}
		serverType = sympoziumv1alpha1.InferenceServerLlamaCpp
	}

	// Defaults
	if req.Filename == "" {
		req.Filename = "model.gguf"
	}
	if req.StorageSize == "" {
		req.StorageSize = "10Gi"
	}
	if req.Memory == "" {
		req.Memory = "16Gi"
	}
	if req.CPU == "" {
		req.CPU = "4"
	}

	ns := req.Namespace
	if ns == "" {
		ns = systemNamespace
	}

	model := &sympoziumv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{
			Name:      req.Name,
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.ModelCRDSpec{
			Source: sympoziumv1alpha1.ModelSource{
				URL:      req.URL,
				ModelID:  req.ModelID,
				Filename: req.Filename,
			},
			Storage: sympoziumv1alpha1.ModelStorage{
				Size:         req.StorageSize,
				StorageClass: req.StorageClass,
			},
			Resources: sympoziumv1alpha1.ModelResources{
				GPU:    req.GPU,
				Memory: req.Memory,
				CPU:    req.CPU,
			},
			NodeSelector: req.NodeSelector,
		},
	}

	// Set placement mode.
	if req.Placement == "auto" {
		model.Spec.Placement.Mode = sympoziumv1alpha1.PlacementAuto
		model.Spec.NodeSelector = nil // Auto mode will determine the node.
	}

	model.Spec.Inference.ServerType = serverType
	if req.Image != "" {
		model.Spec.Inference.Image = req.Image
	}
	if req.Port > 0 {
		model.Spec.Inference.Port = req.Port
	}
	if req.ContextSize > 0 {
		model.Spec.Inference.ContextSize = req.ContextSize
	}
	if len(req.Args) > 0 {
		model.Spec.Inference.Args = req.Args
	}
	if req.HuggingFaceTokenSecret != "" {
		model.Spec.Inference.HuggingFaceTokenSecret = req.HuggingFaceTokenSecret
	}

	if err := s.client.Create(r.Context(), model); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			http.Error(w, "model already exists", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	writeJSON(w, model)
}

func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = systemNamespace
	}

	model := &sympoziumv1alpha1.Model{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
	}
	if err := s.client.Delete(r.Context(), model); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "model not found", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
