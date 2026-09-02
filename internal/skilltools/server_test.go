package skilltools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/mcpbridge"
)

// writeManifest lays down a sidecar-tools manifest like the controller's.
func writeManifest(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "sidecar-tools.json")
	body := `{"tools":[
      {"name":"kubectl_get","description":"read resources","target":"k8s-ops","exec":["kubectl","get"],
       "positionalArgs":["resource"],
       "parameters":{"type":"object","properties":{"resource":{"type":"string"}}}},
      {"name":"kubectl_delete","description":"delete resources","target":"k8s-ops","exec":["kubectl","delete"],
       "positionalArgs":["resource"]}
    ]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return path
}

func newTestServer(t *testing.T, allow, deny string) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s, err := NewServer(Config{
		ManifestPath:    writeManifest(t, dir),
		ToolPolicyAllow: allow,
		ToolPolicyDeny:  deny,
		IPCToolsDir:     toolsDir,
		ExecTimeout:     500 * time.Millisecond,
		LoadTimeout:     2 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return s, toolsDir
}

// call issues one JSON-RPC request and returns the decoded response.
func call(t *testing.T, s *Server, method string, params any) mcpbridge.JSONRPCResponse {
	t.Helper()
	body, err := json.Marshal(mcpbridge.JSONRPCRequest{
		JSONRPC: "2.0", ID: 1, Method: method, Params: params,
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	rec := httptest.NewRecorder()
	s.handleJSONRPC(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body))))

	var resp mcpbridge.JSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response (%s): %v", rec.Body.String(), err)
	}
	return resp
}

func TestServer_Initialize(t *testing.T) {
	s, _ := newTestServer(t, "", "")
	resp := call(t, s, "initialize", mcpbridge.MCPInitializeParams{ProtocolVersion: "2024-11-05"})
	if resp.Error != nil {
		t.Fatalf("initialize returned error: %+v", resp.Error)
	}
	var res mcpbridge.MCPInitializeResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if res.ServerInfo.Name != ServerName {
		t.Errorf("serverInfo.name = %q, want %q", res.ServerInfo.Name, ServerName)
	}
	// A harness client namespaces tools per server; capabilities must declare
	// tools or a strict client will never call tools/list.
	if !strings.Contains(string(res.Capabilities), "tools") {
		t.Errorf("capabilities = %s, want a tools capability", res.Capabilities)
	}
}

// The list a harness sees is already filtered. This is the difference between
// spec.toolPolicy shaping a prompt and spec.toolPolicy being enforced.
func TestServer_ToolsList_AppliesToolPolicy(t *testing.T) {
	s, _ := newTestServer(t, "", "kubectl_delete")
	resp := call(t, s, "tools/list", nil)
	if resp.Error != nil {
		t.Fatalf("tools/list returned error: %+v", resp.Error)
	}
	var res mcpbridge.MCPToolsListResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "kubectl_get" {
		t.Fatalf("tools = %+v, want only kubectl_get", res.Tools)
	}
	// MCP requires an inputSchema even when a tool takes no parameters.
	if res.Tools[0].InputSchema == nil {
		t.Error("inputSchema is nil; a strict MCP client rejects the tool")
	}
}

func TestServer_ToolsList_AllowListIsExclusive(t *testing.T) {
	s, _ := newTestServer(t, "kubectl_get", "")
	var res mcpbridge.MCPToolsListResult
	if err := json.Unmarshal(call(t, s, "tools/list", nil).Result, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "kubectl_get" {
		t.Errorf("tools = %+v, want only the allow-listed kubectl_get", res.Tools)
	}
}

// The load-bearing test. A client may call a name it was never offered, so the
// policy is applied again at call time. If this regresses, spec.toolPolicy is
// advertised rather than enforced and nothing else notices.
func TestServer_ToolsCall_RefusesDeniedToolAndWritesNoExecRequest(t *testing.T) {
	s, toolsDir := newTestServer(t, "", "kubectl_delete")

	resp := call(t, s, "tools/call", mcpbridge.MCPToolCallParams{
		Name:      "kubectl_delete",
		Arguments: json.RawMessage(`{"resource":"pod//critical"}`),
	})
	if resp.Error != nil {
		t.Fatalf("unexpected transport error: %+v", resp.Error)
	}
	var res mcpbridge.MCPToolCallResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.IsError {
		t.Error("calling a denied tool succeeded; the policy is not enforced at call time")
	}

	entries, err := os.ReadDir(toolsDir)
	if err != nil {
		t.Fatalf("read tools dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "exec-request-") {
			t.Fatalf("a denied call still wrote %s; the sidecar would have executed it", e.Name())
		}
	}
}

// A permitted call reaches the sidecar as an argv-mode exec request built by
// pkg/sidecartools — same construction agent-runner uses, including the "--"
// end-of-options marker that stops a value starting with "-" being read as a flag.
func TestServer_ToolsCall_DispatchesExecRequest(t *testing.T) {
	s, toolsDir := newTestServer(t, "", "")

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Stand in for tool-executor.sh.
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			entries, _ := os.ReadDir(toolsDir)
			for _, e := range entries {
				if !strings.HasPrefix(e.Name(), "exec-request-") {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(toolsDir, e.Name()))
				if err != nil {
					continue
				}
				var req struct {
					ID     string   `json:"id"`
					Argv   []string `json:"argv"`
					Target string   `json:"target"`
				}
				if err := json.Unmarshal(raw, &req); err != nil {
					continue
				}
				if req.Target != "k8s-ops" {
					t.Errorf("target = %q, want the tool's owning sidecar", req.Target)
				}
				var sawMarker bool
				for _, a := range req.Argv {
					if a == "--" {
						sawMarker = true
					}
				}
				if !sawMarker {
					t.Errorf("argv = %v, missing the \"--\" end-of-options marker", req.Argv)
				}
				if len(req.Argv) == 0 || req.Argv[len(req.Argv)-1] != "-n" {
					t.Errorf("argv = %v, want the positional value last and unmodified", req.Argv)
				}
				out, _ := json.Marshal(map[string]any{"id": req.ID, "exitCode": 0, "stdout": "no resources found"})
				_ = os.WriteFile(filepath.Join(toolsDir, "exec-result-"+req.ID+".json"), out, 0o644)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	// A value starting with "-" is exactly what the marker protects.
	resp := call(t, s, "tools/call", mcpbridge.MCPToolCallParams{
		Name:      "kubectl_get",
		Arguments: json.RawMessage(`{"resource":"-n"}`),
	})
	<-done

	var res mcpbridge.MCPToolCallResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.IsError {
		t.Fatalf("permitted call failed: %+v", res.Content)
	}
	if len(res.Content) != 1 || res.Content[0].Text != "no resources found" {
		t.Errorf("content = %+v, want the sidecar's stdout", res.Content)
	}
}

func TestServer_ToolsCall_TimesOutWithoutAResult(t *testing.T) {
	s, _ := newTestServer(t, "", "")
	resp := call(t, s, "tools/call", mcpbridge.MCPToolCallParams{
		Name:      "kubectl_get",
		Arguments: json.RawMessage(`{"resource":"pods"}`),
	})
	var res mcpbridge.MCPToolCallResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !res.IsError {
		t.Error("a call with no responding sidecar should fail, not hang or succeed")
	}
}

func TestServer_UnknownMethod(t *testing.T) {
	s, _ := newTestServer(t, "", "")
	resp := call(t, s, "resources/list", nil)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Errorf("error = %+v, want JSON-RPC method-not-found", resp.Error)
	}
}

func TestServer_PreservesStringRequestID(t *testing.T) {
	s, _ := newTestServer(t, "", "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0","id":"client-7","method":"tools/list"}`))
	req.Header.Set("Content-Type", "application/json")
	s.handleJSONRPC(rec, req)
	var response struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if response.ID != "client-7" {
		t.Fatalf("id = %q, want client-7", response.ID)
	}
}

func TestServer_RejectsCompositeRequestID(t *testing.T) {
	s, _ := newTestServer(t, "", "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0","id":{"nested":true},"method":"tools/list"}`))
	s.handleJSONRPC(rec, req)
	if !strings.Contains(rec.Body.String(), "id must be a string, number, or null") {
		t.Fatalf("response = %s", rec.Body.String())
	}
}

func TestServer_RejectsBrowserOrigin(t *testing.T) {
	s, _ := newTestServer(t, "", "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Origin", "https://attacker.example")
	s.handleJSONRPC(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestServer_BoundsRequestBody(t *testing.T) {
	s, _ := newTestServer(t, "", "")
	s.cfg.MaxRequestBytes = 32
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","padding":"xxxxxxxxxxxxxxxxxxxxxxxx"}`))
	s.handleJSONRPC(rec, req)
	if !strings.Contains(rec.Body.String(), "parse error") {
		t.Fatalf("response = %s, want bounded parse error", rec.Body.String())
	}
}

func TestServer_ValidatesToolArgumentsAgainstSchema(t *testing.T) {
	s, _ := newTestServer(t, "", "")
	resp := call(t, s, "tools/call", mcpbridge.MCPToolCallParams{Name: "kubectl_get", Arguments: json.RawMessage(`{"resource":12}`)})
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("error = %+v, want invalid params", resp.Error)
	}
}

func TestServer_BoundsConcurrentToolCalls(t *testing.T) {
	s, _ := newTestServer(t, "", "")
	for i := 0; i < cap(s.sem); i++ {
		s.sem <- struct{}{}
	}
	defer func() {
		for len(s.sem) > 0 {
			<-s.sem
		}
	}()
	resp := call(t, s, "tools/call", mcpbridge.MCPToolCallParams{Name: "kubectl_get", Arguments: json.RawMessage(`{"resource":"pods"}`)})
	if resp.Error == nil || resp.Error.Code != -32000 {
		t.Fatalf("error = %+v, want concurrency limit", resp.Error)
	}
}

func TestServer_BoundsToolOutput(t *testing.T) {
	s, toolsDir := newTestServer(t, "", "")
	s.cfg.MaxOutputBytes = 8
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			entries, _ := os.ReadDir(toolsDir)
			for _, entry := range entries {
				if !strings.HasPrefix(entry.Name(), "exec-request-") {
					continue
				}
				raw, _ := os.ReadFile(filepath.Join(toolsDir, entry.Name()))
				var request struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(raw, &request) != nil || request.ID == "" {
					continue
				}
				result, _ := json.Marshal(map[string]any{"id": request.ID, "exitCode": 0, "stdout": "this output is too large"})
				_ = os.WriteFile(filepath.Join(toolsDir, "exec-result-"+request.ID+".json"), result, 0o644)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	resp := call(t, s, "tools/call", mcpbridge.MCPToolCallParams{Name: "kubectl_get", Arguments: json.RawMessage(`{"resource":"pods"}`)})
	<-done
	var result mcpbridge.MCPToolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "output exceeded") {
		t.Fatalf("result = %+v, want bounded output error", result)
	}
}

func TestNewServer_MissingManifestFails(t *testing.T) {
	_, err := NewServer(Config{
		ManifestPath: filepath.Join(t.TempDir(), "absent.json"),
		IPCToolsDir:  t.TempDir(),
		LoadTimeout:  200 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected an error: an absent manifest means the ConfigMap did not mount")
	}
}

// The internal server's own name must sit inside the namespace Sympozium
// reserves, or the reservation protects nothing.
func TestServerNameIsReserved(t *testing.T) {
	if !sympoziumv1alpha1.IsReservedName(ServerName) {
		t.Errorf("ServerName %q is outside the reserved %q prefix; an operator could claim it",
			ServerName, sympoziumv1alpha1.ReservedNamePrefix)
	}
}
