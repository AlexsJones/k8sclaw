// Package skilltools serves a run's SkillPack native tools to an external agent
// harness as an MCP server on the pod's loopback interface.
//
// Why it exists. When agent-runner is the agent process, spec.toolPolicy is
// enforced by agent-runner itself: it filters the tool list before the model
// sees it, and it is the only writer of /ipc/tools/exec-request-*.json. Policy
// and writer are the same trusted process, so the skill sidecars can execute
// whatever arrives without checking authority.
//
// Harness mode separates them. The agent process is someone else's binary, so
// its /ipc mount is narrowed to input and output (see taskmodes/harness.go) and
// it cannot write exec requests at all. This server is how the tools come back:
// Sympozium-owned code that holds the policy, answers tools/list with only the
// permitted tools, rejects tools/call for anything else, and is the only thing
// in the pod that turns a harness request into an exec request.
//
// The harness reaches it through the MCP server registry it already reads, so
// an adapter needs no code for this — one more entry in a list it already
// translates.
package skilltools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/sympozium-ai/sympozium/internal/mcpbridge"
	"github.com/sympozium-ai/sympozium/pkg/sidecartools"
)

// protocolVersion is the MCP revision this server implements. tools/list and
// tools/call are stable across recent revisions; a client asking for another
// version still gets this one, which is what the spec prescribes.
const protocolVersion = "2024-11-05"

// serverName is the MCP server identity, and the name the controller uses for
// the registry entry. Harnesses namespace tools per server, so it also decides
// how the tools appear to the model (dsh, for instance, renders
// mcp__sympozium-skills__<tool>).
const ServerName = "sympozium-skills"

// resultPollInterval is how often a pending exec result is checked for.
const resultPollInterval = 100 * time.Millisecond

const (
	defaultMaxRequestBytes = 1 << 20
	defaultMaxOutputBytes  = 1 << 20
	defaultMaxConcurrent   = 4
)

// Config is everything the server needs, all of it controller-supplied. None of
// it comes from the agent, which is the point: the policy this server enforces
// cannot be edited by the process it is enforcing against.
type Config struct {
	// ManifestPath is the read-only sidecar-tools ConfigMap mount.
	ManifestPath string
	// ToolPolicyAllow / ToolPolicyDeny are the AgentRun's spec.toolPolicy,
	// comma-separated, exactly as agent-runner receives them.
	ToolPolicyAllow string
	ToolPolicyDeny  string
	// IPCToolsDir is where exec requests are written. This process has it;
	// the harness container does not.
	IPCToolsDir string
	// Addr is the listen address. Loopback only — see NewServer.
	Addr string
	// ExecTimeout bounds a single tool call.
	ExecTimeout time.Duration
	// LoadTimeout is how long to wait for the manifest mount. Zero means
	// sidecartools.DefaultLoadTimeout.
	LoadTimeout time.Duration
	// MaxRequestBytes, MaxOutputBytes, and MaxConcurrent bound untrusted MCP
	// clients. Zero selects the conservative defaults above.
	MaxRequestBytes int64
	MaxOutputBytes  int64
	MaxConcurrent   int
}

// Server serves the permitted SkillPack tools over MCP.
type Server struct {
	cfg     Config
	tools   map[string]sidecartools.Tool
	order   []string
	schemas map[string]*jsonschema.Schema
	sem     chan struct{}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  any             `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string                  `json:"jsonrpc"`
	ID      json.RawMessage         `json:"id"`
	Result  json.RawMessage         `json:"result,omitempty"`
	Error   *mcpbridge.JSONRPCError `json:"error,omitempty"`
}

// NewServer loads the manifest, applies the tool policy once at startup, and
// returns a server that will serve exactly the surviving tools.
//
// Filtering at startup rather than per request is deliberate: the manifest and
// the policy are both immutable for the life of the run, and a single decision
// point means tools/list and tools/call cannot disagree about what is allowed.
func NewServer(cfg Config) (*Server, error) {
	if cfg.IPCToolsDir == "" {
		cfg.IPCToolsDir = "/ipc/tools"
	}
	if cfg.ExecTimeout <= 0 {
		cfg.ExecTimeout = sidecartools.DefaultExecTimeout * time.Second
	}
	if cfg.MaxRequestBytes <= 0 {
		cfg.MaxRequestBytes = defaultMaxRequestBytes
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = defaultMaxOutputBytes
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = defaultMaxConcurrent
	}

	manifest, err := sidecartools.LoadManifest(cfg.ManifestPath, cfg.LoadTimeout)
	if err != nil {
		return nil, err
	}

	permitted := sidecartools.FilterByPolicy(manifest.Tools, cfg.ToolPolicyAllow, cfg.ToolPolicyDeny)
	s := &Server{
		cfg:     cfg,
		tools:   make(map[string]sidecartools.Tool, len(permitted)),
		schemas: make(map[string]*jsonschema.Schema, len(permitted)),
		sem:     make(chan struct{}, cfg.MaxConcurrent),
	}
	for _, t := range permitted {
		s.tools[t.Name] = t
		s.order = append(s.order, t.Name)
		if t.Parameters != nil {
			compiler := jsonschema.NewCompiler()
			url := "urn:sympozium:skill-tool:" + t.Name
			if err := compiler.AddResource(url, t.Parameters); err != nil {
				return nil, fmt.Errorf("tool %s input schema: %w", t.Name, err)
			}
			sch, err := compiler.Compile(url)
			if err != nil {
				return nil, fmt.Errorf("tool %s input schema: %w", t.Name, err)
			}
			s.schemas[t.Name] = sch
		}
	}

	log.Printf("skill-tools: serving %d of %d tool(s) after spec.toolPolicy (allow=%q deny=%q)",
		len(permitted), len(manifest.Tools), cfg.ToolPolicyAllow, cfg.ToolPolicyDeny)
	return s, nil
}

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /", s.handleJSONRPC)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	srv := &http.Server{
		Addr:              s.cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       10 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("skill-tools: MCP server listening on %s", s.cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		http.Error(w, "Origin is not allowed on the loopback MCP endpoint", http.StatusForbidden)
		return
	}
	if contentType := r.Header.Get("Content-Type"); contentType != "" && !strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxRequestBytes)
	var req rpcRequest
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(&req); err != nil {
		writeError(w, nil, -32700, "parse error")
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeError(w, req.ID, -32600, "invalid request")
		return
	}
	if !validRPCID(req.ID) {
		writeError(w, nil, -32600, "id must be a string, number, or null")
		return
	}

	switch req.Method {
	case "initialize":
		caps := json.RawMessage(`{"tools":{}}`)
		writeResult(w, req.ID, mcpbridge.MCPInitializeResult{
			ProtocolVersion: protocolVersion,
			Capabilities:    caps,
			ServerInfo: mcpbridge.MCPImplementation{
				Name:    ServerName,
				Version: "1",
			},
		})

	case "notifications/initialized":
		// A notification carries no id and takes no response.
		w.WriteHeader(http.StatusAccepted)

	case "tools/list":
		writeResult(w, req.ID, mcpbridge.MCPToolsListResult{Tools: s.listTools()})

	case "tools/call":
		s.handleToolCall(w, r.Context(), &req)

	case "ping":
		writeResult(w, req.ID, struct{}{})

	default:
		writeError(w, req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

func validRPCID(id json.RawMessage) bool {
	if len(id) == 0 || string(id) == "null" {
		return true
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(string(id)))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return false
	}
	switch value.(type) {
	case string, json.Number:
		return true
	default:
		return false
	}
}

// listTools renders the permitted tools in manifest order.
func (s *Server) listTools() []mcpbridge.MCPTool {
	out := make([]mcpbridge.MCPTool, 0, len(s.order))
	for _, name := range s.order {
		t := s.tools[name]
		schema := t.Parameters
		if schema == nil {
			// MCP requires an inputSchema. A tool with no parameters is an
			// object with none, not a missing field.
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, mcpbridge.MCPTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}
	return out
}

func (s *Server) handleToolCall(w http.ResponseWriter, ctx context.Context, req *rpcRequest) {
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		writeError(w, req.ID, -32000, "too many concurrent tool calls")
		return
	}
	raw, err := json.Marshal(req.Params)
	if err != nil {
		writeError(w, req.ID, -32602, "invalid params")
		return
	}
	var params mcpbridge.MCPToolCallParams
	if err := json.Unmarshal(raw, &params); err != nil {
		writeError(w, req.ID, -32602, "invalid params")
		return
	}

	// The authority check. tools/list already omitted anything the policy
	// denies, but a client is free to call a name it was never offered, so the
	// same decision is applied again here. This is the line that makes
	// spec.toolPolicy enforced rather than advertised.
	tool, ok := s.tools[params.Name]
	if !ok {
		log.Printf("skill-tools: refused %q — not permitted by spec.toolPolicy for this run", params.Name)
		writeResult(w, req.ID, mcpbridge.MCPToolCallResult{
			IsError: true,
			Content: []mcpbridge.MCPContent{{
				Type: "text",
				Text: fmt.Sprintf("tool %q is not available to this run", params.Name),
			}},
		})
		return
	}

	args := string(params.Arguments)
	if args == "" || args == "null" {
		args = "{}"
	}
	if schema := s.schemas[params.Name]; schema != nil {
		var value any
		dec := json.NewDecoder(strings.NewReader(args))
		dec.UseNumber()
		if err := dec.Decode(&value); err != nil || schema.Validate(value) != nil {
			writeError(w, req.ID, -32602, "tool arguments do not match inputSchema")
			return
		}
	}

	out, err := s.dispatch(ctx, tool, args)
	if err != nil {
		writeResult(w, req.ID, mcpbridge.MCPToolCallResult{
			IsError: true,
			Content: []mcpbridge.MCPContent{{Type: "text", Text: err.Error()}},
		})
		return
	}
	writeResult(w, req.ID, mcpbridge.MCPToolCallResult{
		Content: []mcpbridge.MCPContent{{Type: "text", Text: out}},
	})
}

// dispatch writes the exec request and waits for the sidecar's result.
//
// The request is built by pkg/sidecartools, the same code agent-runner uses, so
// a harness-driven call and an agent-runner-driven call of the same tool
// produce the same argv — including the "--" end-of-options marker that keeps a
// model-supplied value starting with "-" from being read as a flag.
func (s *Server) dispatch(ctx context.Context, tool sidecartools.Tool, argsJSON string) (string, error) {
	req, err := sidecartools.BuildExecRequest(tool, argsJSON, nil)
	if err != nil {
		return "", err
	}

	data, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshalling exec request: %w", err)
	}

	reqPath := filepath.Join(s.cfg.IPCToolsDir, fmt.Sprintf("exec-request-%s.json", req.ID))
	resPath := filepath.Join(s.cfg.IPCToolsDir, fmt.Sprintf("exec-result-%s.json", req.ID))

	if err := os.MkdirAll(s.cfg.IPCToolsDir, 0o755); err != nil {
		return "", fmt.Errorf("creating %s: %w", s.cfg.IPCToolsDir, err)
	}
	if err := os.WriteFile(reqPath, data, 0o644); err != nil {
		return "", fmt.Errorf("writing exec request: %w", err)
	}
	log.Printf("skill-tools: dispatched %s (%s) target=%s", tool.Name, req.ID, req.Target)

	deadline := time.Now().Add(s.cfg.ExecTimeout)
	for {
		if info, err := os.Stat(resPath); err == nil && info.Size() > s.cfg.MaxOutputBytes+(64<<10) {
			return "", fmt.Errorf("tool %s result file exceeded the %d-byte output limit", tool.Name, s.cfg.MaxOutputBytes)
		}
		if res, ok := readExecResult(resPath); ok {
			if int64(len(res.Stdout)+len(res.Stderr)) > s.cfg.MaxOutputBytes {
				return "", fmt.Errorf("tool %s output exceeded %d bytes", tool.Name, s.cfg.MaxOutputBytes)
			}
			if res.TimedOut {
				return "", fmt.Errorf("tool %s timed out", tool.Name)
			}
			if res.ExitCode != 0 {
				return "", fmt.Errorf("tool %s exited %d: %s", tool.Name, res.ExitCode, res.Stderr)
			}
			return res.Stdout, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("tool %s produced no result within %s", tool.Name, s.cfg.ExecTimeout)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(resultPollInterval):
		}
	}
}

// execResult mirrors what tool-executor.sh writes.
type execResult struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timedOut,omitempty"`
}

// readExecResult returns the result once it is both present and complete. An
// empty or unparseable file is treated as not-yet-written, because the reader
// can observe a partial write.
func readExecResult(path string) (execResult, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return execResult{}, false
	}
	var res execResult
	if err := json.Unmarshal(data, &res); err != nil {
		return execResult{}, false
	}
	return res, true
}

func writeResult(w http.ResponseWriter, id json.RawMessage, result any) {
	payload, err := json.Marshal(result)
	if err != nil {
		writeError(w, id, -32603, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  payload,
	})
}

func writeError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &mcpbridge.JSONRPCError{Code: code, Message: msg},
	})
}
