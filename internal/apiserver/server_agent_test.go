package apiserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func newInstanceTestServer(t *testing.T) (*Server, *runtime.Scheme) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sympoziumv1alpha1.Agent{}).
		Build()

	return NewServer(cl, nil, nil, logr.Discard()), scheme
}

func TestCreateInstance_NoHardcodedOTLPEndpoint(t *testing.T) {
	srv, _ := newInstanceTestServer(t)

	body, _ := json.Marshal(CreateInstanceRequest{
		Name:     "test-adhoc",
		Provider: "lm-studio",
		Model:    "qwen/qwen3.5-35b-a3b",
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents?namespace=default", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201, body = %s", rec.Code, rec.Body.String())
	}

	// Retrieve the created instance from the fake client and verify.
	var inst sympoziumv1alpha1.Agent
	if err := srv.client.Get(req.Context(), types.NamespacedName{Name: "test-adhoc", Namespace: "default"}, &inst); err != nil {
		t.Fatalf("failed to get created instance: %v", err)
	}

	if inst.Spec.Observability == nil {
		t.Fatal("expected Observability spec to be set")
	}
	if !inst.Spec.Observability.Enabled {
		t.Error("expected Observability.Enabled = true")
	}
	if inst.Spec.Observability.OTLPEndpoint != "" {
		t.Errorf("expected empty OTLPEndpoint (should not be hardcoded), got %q", inst.Spec.Observability.OTLPEndpoint)
	}
	if inst.Spec.Observability.OTLPProtocol != "" {
		t.Errorf("expected empty OTLPProtocol (should not be hardcoded), got %q", inst.Spec.Observability.OTLPProtocol)
	}
}
