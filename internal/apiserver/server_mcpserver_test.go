package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestListMCPServersEmpty(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcpservers?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var items []sympoziumv1alpha1.MCPServer
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected 0 items, got %d", len(items))
	}
}

func TestCreateAndGetMCPServer(t *testing.T) {
	srv, cl := newTestServer(t)

	// Create
	payload := CreateMCPServerRequest{
		Name:          "dynatrace",
		TransportType: "http",
		ToolsPrefix:   "dt",
		URL:           "http://dynatrace-mcp:8080",
		Timeout:       60,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcpservers?namespace=default", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Verify in cluster
	var got sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "dynatrace", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get mcpserver: %v", err)
	}
	if got.Spec.TransportType != "http" {
		t.Fatalf("expected transport http, got %s", got.Spec.TransportType)
	}
	if got.Spec.ToolsPrefix != "dt" {
		t.Fatalf("expected prefix dt, got %s", got.Spec.ToolsPrefix)
	}
	if got.Spec.URL != "http://dynatrace-mcp:8080" {
		t.Fatalf("expected URL http://dynatrace-mcp:8080, got %s", got.Spec.URL)
	}
	if got.Spec.Timeout != 60 {
		t.Fatalf("expected timeout 60, got %d", got.Spec.Timeout)
	}

	// Get via API
	req = httptest.NewRequest(http.MethodGet, "/api/v1/mcpservers/dynatrace?namespace=default", nil)
	rec = httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on get, got %d body=%s", rec.Code, rec.Body.String())
	}

	var fetched sympoziumv1alpha1.MCPServer
	if err := json.Unmarshal(rec.Body.Bytes(), &fetched); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if fetched.Spec.ToolsPrefix != "dt" {
		t.Fatalf("get response prefix mismatch: %s", fetched.Spec.ToolsPrefix)
	}
}

func TestCreateMCPServerWithDeployment(t *testing.T) {
	srv, cl := newTestServer(t)

	payload := CreateMCPServerRequest{
		Name:          "k8s-net",
		TransportType: "stdio",
		ToolsPrefix:   "k8snet",
		Image:         "ghcr.io/org/k8s-net-mcp:v1",
	}
	raw, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcpservers?namespace=default", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "k8s-net", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get mcpserver: %v", err)
	}
	if got.Spec.Deployment == nil {
		t.Fatal("expected deployment to be set")
	}
	if got.Spec.Deployment.Image != "ghcr.io/org/k8s-net-mcp:v1" {
		t.Fatalf("expected image ghcr.io/org/k8s-net-mcp:v1, got %s", got.Spec.Deployment.Image)
	}
}

func TestCreateMCPServerValidation(t *testing.T) {
	srv, _ := newTestServer(t)

	// Missing required fields
	payload := `{"name":"bad"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcpservers?namespace=default", bytes.NewBufferString(payload))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestCreateMCPServerDuplicate(t *testing.T) {
	existing := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "existing", Namespace: "default"},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: "http",
			ToolsPrefix:   "ex",
		},
	}
	srv, _ := newTestServer(t, existing)

	payload := CreateMCPServerRequest{
		Name:          "existing",
		TransportType: "http",
		ToolsPrefix:   "ex",
	}
	raw, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcpservers?namespace=default", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDeleteMCPServer(t *testing.T) {
	existing := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "to-delete", Namespace: "default"},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: "http",
			ToolsPrefix:   "del",
		},
	}
	srv, cl := newTestServer(t, existing)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/mcpservers/to-delete?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d body=%s", rec.Code, rec.Body.String())
	}

	// Verify deleted
	var got sympoziumv1alpha1.MCPServer
	err := cl.Get(context.Background(), client.ObjectKey{Name: "to-delete", Namespace: "default"}, &got)
	if err == nil {
		t.Fatal("expected mcpserver to be deleted")
	}
}

func TestDeleteMCPServerNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/mcpservers/nonexistent?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPatchMCPServer(t *testing.T) {
	existing := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "to-patch", Namespace: "default"},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: "http",
			ToolsPrefix:   "old",
			Timeout:       30,
		},
	}
	srv, cl := newTestServer(t, existing)

	newPrefix := "new"
	newTimeout := 90
	payload := PatchMCPServerRequest{
		ToolsPrefix: &newPrefix,
		Timeout:     &newTimeout,
		ToolsAllow:  []string{"read_logs", "get_metrics"},
	}
	raw, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/mcpservers/to-patch?namespace=default", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "to-patch", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get mcpserver: %v", err)
	}
	if got.Spec.ToolsPrefix != "new" {
		t.Fatalf("expected prefix new, got %s", got.Spec.ToolsPrefix)
	}
	if got.Spec.Timeout != 90 {
		t.Fatalf("expected timeout 90, got %d", got.Spec.Timeout)
	}
	if len(got.Spec.ToolsAllow) != 2 {
		t.Fatalf("expected 2 toolsAllow, got %d", len(got.Spec.ToolsAllow))
	}
	// Transport should remain unchanged
	if got.Spec.TransportType != "http" {
		t.Fatalf("expected transport unchanged (http), got %s", got.Spec.TransportType)
	}
}

func TestPatchMCPServerNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	payload := `{"timeout":60}`
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/mcpservers/ghost?namespace=default", bytes.NewBufferString(payload))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestListMCPServersReturnsAll(t *testing.T) {
	s1 := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "server-a", Namespace: "default"},
		Spec:       sympoziumv1alpha1.MCPServerSpec{TransportType: "http", ToolsPrefix: "a"},
	}
	s2 := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "server-b", Namespace: "other"},
		Spec:       sympoziumv1alpha1.MCPServerSpec{TransportType: "stdio", ToolsPrefix: "b"},
	}
	srv, _ := newTestServer(t, s1, s2)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcpservers", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var items []sympoziumv1alpha1.MCPServer
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items across namespaces, got %d", len(items))
	}
}

func TestGetMCPServerNotFound(t *testing.T) {
	srv, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/mcpservers/nope?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestInstallDefaultMCPServers(t *testing.T) {
	// Seed a source MCPServer in sympozium-system (the "defaults" namespace).
	src := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "github",
			Namespace: "sympozium-system",
			Labels:    map[string]string{"sympozium.ai/catalog": "true"},
		},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: "stdio",
			ToolsPrefix:   "github",
		},
	}
	srv, cl := newTestServer(t, src)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcpservers/install-defaults?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp InstallDefaultMCPServersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Copied) != 1 || resp.Copied[0] != "github" {
		t.Fatalf("expected [github] copied, got %v", resp.Copied)
	}

	// Verify the MCPServer was created in the target namespace.
	var got sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "github", Namespace: "default"}, &got); err != nil {
		t.Fatalf("expected github MCPServer in default ns: %v", err)
	}
	if got.Spec.ToolsPrefix != "github" {
		t.Fatalf("expected prefix github, got %s", got.Spec.ToolsPrefix)
	}
	if !got.Spec.Suspended {
		t.Fatal("expected installed default MCPServer to be suspended")
	}

	// Second call should report already present.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/mcpservers/install-defaults?namespace=default", nil)
	rec2 := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec2, req2)

	var resp2 InstallDefaultMCPServersResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if len(resp2.AlreadyPresent) != 1 {
		t.Fatalf("expected 1 already present, got %v", resp2.AlreadyPresent)
	}
}

func TestMCPServerAuthToken(t *testing.T) {
	mcp := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "github", Namespace: "default"},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: "stdio",
			ToolsPrefix:   "github",
			Deployment:    &sympoziumv1alpha1.MCPServerDeployment{Image: "mcp/github"},
		},
	}
	srv, cl := newTestServer(t, mcp)

	// POST token
	body := `{"token":"ghp_test123"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcpservers/github/auth/token?namespace=default", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var resp mcpServerAuthStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status != "complete" {
		t.Fatalf("expected status complete, got %s", resp.Status)
	}
	if resp.SecretName != "mcp-github-token" {
		t.Fatalf("expected secret mcp-github-token, got %s", resp.SecretName)
	}

	// Verify the secret was created.
	secret := &corev1.Secret{}
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "mcp-github-token", Namespace: "default"}, secret); err != nil {
		t.Fatalf("expected secret: %v", err)
	}
	if string(secret.Data["GITHUB_PERSONAL_ACCESS_TOKEN"]) != "ghp_test123" {
		t.Fatalf("token mismatch: %s", string(secret.Data["GITHUB_PERSONAL_ACCESS_TOKEN"]))
	}

	// Verify the MCPServer was patched with the secret ref.
	var updated sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "github", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get updated mcp: %v", err)
	}
	found := false
	for _, ref := range updated.Spec.Deployment.SecretRefs {
		if ref.Name == "mcp-github-token" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected secretRef mcp-github-token in deployment, got %v", updated.Spec.Deployment.SecretRefs)
	}

	// GET auth status should return complete.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/mcpservers/github/auth/status?namespace=default", nil)
	rec2 := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec2, req2)

	var status mcpServerAuthStatusResponse
	if err := json.Unmarshal(rec2.Body.Bytes(), &status); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if status.Status != "complete" {
		t.Fatalf("expected complete, got %s", status.Status)
	}
}

func TestPatchMCPServerSuspended(t *testing.T) {
	existing := &sympoziumv1alpha1.MCPServer{
		ObjectMeta: metav1.ObjectMeta{Name: "suspendable", Namespace: "default"},
		Spec: sympoziumv1alpha1.MCPServerSpec{
			TransportType: "http",
			ToolsPrefix:   "sus",
			Timeout:       30,
		},
	}
	srv, cl := newTestServer(t, existing)

	// Suspend
	trueVal := true
	payload := PatchMCPServerRequest{Suspended: &trueVal}
	raw, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/mcpservers/suspendable?namespace=default", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "suspendable", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get mcpserver: %v", err)
	}
	if !got.Spec.Suspended {
		t.Fatal("expected Suspended to be true after patch")
	}

	// Unsuspend
	falseVal := false
	payload2 := PatchMCPServerRequest{Suspended: &falseVal}
	raw2, _ := json.Marshal(payload2)

	req2 := httptest.NewRequest(http.MethodPatch, "/api/v1/mcpservers/suspendable?namespace=default", bytes.NewReader(raw2))
	rec2 := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rec2.Code, rec2.Body.String())
	}

	if err := cl.Get(context.Background(), client.ObjectKey{Name: "suspendable", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get mcpserver: %v", err)
	}
	if got.Spec.Suspended {
		t.Fatal("expected Suspended to be false after unsuspend patch")
	}
}

func TestCreateMCPServerWithSecretRefs(t *testing.T) {
	srv, cl := newTestServer(t)

	payload := CreateMCPServerRequest{
		Name:          "github",
		TransportType: "stdio",
		ToolsPrefix:   "github",
		Image:         "mcp/github",
		SecretRefs:    []string{"mcp-github-token"},
		Env:           map[string]string{"LOG_LEVEL": "debug"},
	}
	raw, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/mcpservers?namespace=default", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	var got sympoziumv1alpha1.MCPServer
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "github", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Spec.Deployment == nil {
		t.Fatal("expected deployment")
	}
	if len(got.Spec.Deployment.SecretRefs) != 1 || got.Spec.Deployment.SecretRefs[0].Name != "mcp-github-token" {
		t.Fatalf("expected secretRef mcp-github-token, got %v", got.Spec.Deployment.SecretRefs)
	}
	if got.Spec.Deployment.Env["LOG_LEVEL"] != "debug" {
		t.Fatalf("expected env LOG_LEVEL=debug, got %v", got.Spec.Deployment.Env)
	}
}
