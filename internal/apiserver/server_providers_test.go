package apiserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
