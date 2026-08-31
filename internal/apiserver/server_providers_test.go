package apiserver

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/go-logr/logr"
	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func newProviderTestServer(t *testing.T, objs ...client.Object) *Server {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return NewServer(cl, nil, nil, logr.Discard())
}

func TestListProviderNodes_Empty(t *testing.T) {
	srv := newProviderTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/nodes?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	var nodes []ProviderNode
	if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(nodes))
	}
}

func TestListProviderNodes_WithAnnotations(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node-1",
			Labels: map[string]string{
				"kubernetes.io/hostname": "gpu-node-1",
			},
			Annotations: map[string]string{
				"sympozium.ai/inference-healthy":       "true",
				"sympozium.ai/inference-last-probe":    "2026-03-15T12:00:00Z",
				"sympozium.ai/inference-ollama":        "11434",
				"sympozium.ai/inference-models-ollama": "llama3,mistral",
			},
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.1.5"},
			},
		},
	}

	srv := newProviderTestServer(t, node)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/nodes?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	var nodes []ProviderNode
	if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}

	n := nodes[0]
	if n.NodeName != "gpu-node-1" {
		t.Errorf("nodeName = %q, want gpu-node-1", n.NodeName)
	}
	if n.NodeIP != "10.0.1.5" {
		t.Errorf("nodeIP = %q, want 10.0.1.5", n.NodeIP)
	}
	if len(n.Providers) != 1 {
		t.Fatalf("providers count = %d, want 1", len(n.Providers))
	}

	p := n.Providers[0]
	if p.Name != "ollama" {
		t.Errorf("provider name = %q, want ollama", p.Name)
	}
	if p.Port != 11434 {
		t.Errorf("provider port = %d, want 11434", p.Port)
	}
	if len(p.Models) != 2 {
		t.Fatalf("models count = %d, want 2", len(p.Models))
	}
	if p.Models[0] != "llama3" || p.Models[1] != "mistral" {
		t.Errorf("models = %v, want [llama3 mistral]", p.Models)
	}
}

func TestListProviderNodes_FilterByProvider(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node-1",
			Annotations: map[string]string{
				"sympozium.ai/inference-healthy": "true",
				"sympozium.ai/inference-ollama":  "11434",
				"sympozium.ai/inference-vllm":    "8000",
			},
		},
		Status: corev1.NodeStatus{
			Addresses: []corev1.NodeAddress{
				{Type: corev1.NodeInternalIP, Address: "10.0.1.5"},
			},
		},
	}

	srv := newProviderTestServer(t, node)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/nodes?namespace=default&provider=ollama", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	var nodes []ProviderNode
	json.Unmarshal(rec.Body.Bytes(), &nodes)

	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(nodes))
	}
	if len(nodes[0].Providers) != 1 {
		t.Fatalf("expected 1 provider (ollama only), got %d", len(nodes[0].Providers))
	}
	if nodes[0].Providers[0].Name != "ollama" {
		t.Errorf("provider = %q, want ollama", nodes[0].Providers[0].Name)
	}
}

func TestListProviderNodes_SkipsUnhealthyNodes(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "dead-node",
			Annotations: map[string]string{
				"sympozium.ai/inference-ollama": "11434",
				// no inference-healthy annotation
			},
		},
	}

	srv := newProviderTestServer(t, node)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/nodes?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	var nodes []ProviderNode
	json.Unmarshal(rec.Body.Bytes(), &nodes)

	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes (unhealthy), got %d", len(nodes))
	}
}

func TestProxyProviderModels_MissingBaseURL(t *testing.T) {
	srv := newProviderTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/models?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestProxyProviderModels_SSRFBlocksLinkLocal(t *testing.T) {
	srv := newProviderTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/models?namespace=default&baseURL=http://169.254.169.254/latest/meta-data/", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	// Should be blocked — either 400 (can't resolve) or 403 (link-local blocked)
	if rec.Code != http.StatusBadRequest && rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 400 or 403 for link-local address", rec.Code)
	}
}

func TestProxyProviderModels_InvalidScheme(t *testing.T) {
	srv := newProviderTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/models?namespace=default&baseURL=ftp://example.com", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestProxyProviderModels_SecretBackedDiscoveryRequiresAuth(t *testing.T) {
	srv := newProviderTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/providers/models?provider=databricks&agentRef=example", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestValidateProviderURL(t *testing.T) {
	tests := []struct {
		name     string
		rawURL   string
		provider string
		wantErr  bool
	}{
		{name: "databricks https", rawURL: "https://workspace.cloud.databricks.com/ai-gateway/mlflow/v1", provider: "databricks"},
		{name: "databricks explicit 443", rawURL: "https://workspace.cloud.databricks.com:443/v1", provider: "databricks"},
		{name: "databricks plaintext", rawURL: "http://workspace.cloud.databricks.com/v1", provider: "databricks", wantErr: true},
		{name: "databricks nonstandard port", rawURL: "https://workspace.cloud.databricks.com:8443/v1", provider: "databricks", wantErr: true},
		{name: "databricks wrong host", rawURL: "https://example.com/v1", provider: "databricks", wantErr: true},
		{name: "embedded credentials", rawURL: "https://user:password@example.com/v1", provider: "custom", wantErr: true},
		{name: "fragment", rawURL: "https://example.com/v1#internal", provider: "custom", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := url.Parse(tt.rawURL)
			if err != nil {
				t.Fatalf("parse URL: %v", err)
			}
			err = validateProviderURL(parsed, tt.provider)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateProviderURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestIsDisallowedProviderIP(t *testing.T) {
	tests := []struct {
		ip         string
		disallowed bool
	}{
		{ip: "127.0.0.1", disallowed: true},
		{ip: "169.254.169.254", disallowed: true},
		{ip: "10.0.0.1", disallowed: true},
		{ip: "100.64.0.1", disallowed: true},
		{ip: "198.18.0.1", disallowed: true},
		{ip: "::1", disallowed: true},
		{ip: "fc00::1", disallowed: true},
		{ip: "8.8.8.8", disallowed: false},
		{ip: "2606:4700:4700::1111", disallowed: false},
	}
	for _, tt := range tests {
		if got := isDisallowedProviderIP(net.ParseIP(tt.ip)); got != tt.disallowed {
			t.Errorf("isDisallowedProviderIP(%s) = %v, want %v", tt.ip, got, tt.disallowed)
		}
	}
}

func TestProviderHTTPClientRejectsRedirects(t *testing.T) {
	client := newProviderHTTPClient()
	if err := client.CheckRedirect(&http.Request{}, []*http.Request{{}}); err != http.ErrUseLastResponse {
		t.Errorf("CheckRedirect error = %v, want http.ErrUseLastResponse", err)
	}
}

func TestParseProviderModels_OllamaFormat(t *testing.T) {
	body := `{"models":[{"name":"llama3:latest"},{"name":"codellama:7b"}]}`
	models := parseProviderModels([]byte(body))

	if len(models) != 2 {
		t.Fatalf("count = %d, want 2", len(models))
	}
	// Sorted: codellama:7b, llama3
	if models[0] != "codellama:7b" {
		t.Errorf("models[0] = %q, want codellama:7b", models[0])
	}
	if models[1] != "llama3" {
		t.Errorf("models[1] = %q, want llama3 (stripped :latest)", models[1])
	}
}

func TestParseProviderModels_OpenAIFormat(t *testing.T) {
	body := `{"data":[{"id":"gpt-4o"},{"id":"gpt-3.5-turbo"}]}`
	models := parseProviderModels([]byte(body))

	if len(models) != 2 {
		t.Fatalf("count = %d, want 2", len(models))
	}
}

func TestParseProviderModels_InvalidJSON(t *testing.T) {
	models := parseProviderModels([]byte("not json"))
	if len(models) != 0 {
		t.Errorf("expected empty for invalid JSON, got %v", models)
	}
}

func TestParseDatabricksServingEndpoints(t *testing.T) {
	body := `{
  "endpoints": [
    {"name":"databricks-claude-sonnet-4-6","state":{"ready":"READY"},"config":{"served_entities":[{"entity_name":"system.ai.databricks-claude-sonnet-4-6"}]}},
    {"name":"gpt-4-1","state":{"ready":"READY"},"config":{"served_entities":[{"external_model":{"task":"llm/v1/chat"}}]}},
    {"name":"azure-cohere-embed-v-4-0","state":{"ready":"READY"},"config":{"served_entities":[{"external_model":{"task":"llm/v1/embeddings"}}]}},
    {"name":"starting-model","state":{"ready":"NOT_READY"},"config":{}}
  ]
}`

	models := parseDatabricksServingEndpoints([]byte(body))
	want := []string{"databricks-claude-sonnet-4-6", "gpt-4-1"}
	if len(models) != len(want) {
		t.Fatalf("models = %v, want %v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Errorf("models[%d] = %q, want %q", i, models[i], want[i])
		}
	}
}

func TestDatabricksAuthorization(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		"Authorization": []byte("Bearer db-token"),
		"API_KEY":       []byte("fallback-token"),
	}}
	if got := databricksAuthorization(secret); got != "Bearer db-token" {
		t.Errorf("authorization = %q, want Bearer db-token", got)
	}
}

func TestDatabricksAuthorizationIgnoresUnrelatedKeys(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		"API_KEY":        []byte("generic-token"),
		"OPENAI_API_KEY": []byte("openai-token"),
	}}
	if got := databricksAuthorization(secret); got != "" {
		t.Errorf("authorization = %q, want empty", got)
	}
}

func TestDatabricksAuthorizationRejectsUnsafeValues(t *testing.T) {
	tests := []corev1.Secret{
		{Data: map[string][]byte{"Authorization": []byte("Basic credentials")}},
		{Data: map[string][]byte{"Authorization": []byte("Bearer token\r\nX-Injected: value")}},
		{Data: map[string][]byte{"DATABRICKS_TOKEN": []byte("token\nX-Injected: value")}},
	}
	for i := range tests {
		if got := databricksAuthorization(&tests[i]); got != "" {
			t.Errorf("case %d authorization = %q, want empty", i, got)
		}
	}
}

func TestDatabricksAuthorizationProviderAPIKey(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{
		"PROVIDER_API_KEY": []byte("db-token"),
	}}
	if got := databricksAuthorization(secret); got != "Bearer db-token" {
		t.Errorf("authorization = %q, want Bearer db-token", got)
	}
}

func TestProviderSecretRefMatchesProvider(t *testing.T) {
	agent := &sympoziumv1alpha1.Agent{
		Spec: sympoziumv1alpha1.AgentSpec{
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "openai", Secret: "openai-secret"},
				{Provider: "databricks", Secret: "databricks-secret"},
			},
		},
	}
	if got := providerSecretRef(agent, "databricks"); got != "databricks-secret" {
		t.Errorf("secret ref = %q, want databricks-secret", got)
	}
}
