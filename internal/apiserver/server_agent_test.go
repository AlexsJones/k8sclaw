package apiserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestInstallDefaultRuntimes(t *testing.T) {
	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "harness-examples", Namespace: "sympozium-system", Labels: map[string]string{"sympozium.ai/harness-example": "true"}},
		Spec:       sympoziumv1alpha1.SympoziumPolicySpec{HarnessPolicy: &sympoziumv1alpha1.HarnessPolicySpec{Enabled: true}},
	}
	runtime := &sympoziumv1alpha1.AgentRuntime{
		ObjectMeta: metav1.ObjectMeta{Name: "pi-v0-84-4", Namespace: "sympozium-system", Labels: map[string]string{"sympozium.ai/harness-example": "true"}},
		Spec:       sympoziumv1alpha1.AgentRuntimeSpec{Image: "ghcr.io/sympozium-ai/harness-adapters/pi@sha256:1234"},
	}
	srv, cl := newTestServer(t, policy, runtime)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtimes/install-defaults?namespace=team-a", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response InstallDefaultRuntimesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Copied) != 2 {
		t.Fatalf("copied = %v, want policy and runtime", response.Copied)
	}
	var installed sympoziumv1alpha1.AgentRuntime
	if err := cl.Get(context.Background(), types.NamespacedName{Name: runtime.Name, Namespace: "team-a"}, &installed); err != nil {
		t.Fatalf("get installed runtime: %v", err)
	}
	if installed.Spec.Image != runtime.Spec.Image {
		t.Fatalf("image = %q, want %q", installed.Spec.Image, runtime.Spec.Image)
	}

	// Installation is idempotent and must not replace namespaced objects.
	rec = httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.AlreadyPresent) != 2 {
		t.Fatalf("alreadyPresent = %v, want policy and runtime", response.AlreadyPresent)
	}
}

func TestCreateInstance_NoHardcodedOTLPEndpoint(t *testing.T) {
	srv, _ := newInstanceTestServer(t)

	body, _ := json.Marshal(CreateInstanceRequest{
		Name:       "test-adhoc",
		Provider:   "lm-studio",
		Model:      "qwen/qwen3.5-35b-a3b",
		RuntimeRef: "pi-v0-84-4",
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
	if inst.Spec.RuntimeRef != "pi-v0-84-4" {
		t.Errorf("RuntimeRef = %q, want Agent-level harness selection preserved", inst.Spec.RuntimeRef)
	}
}

func TestPatchAgent_RuntimeRef(t *testing.T) {
	srv, _ := newInstanceTestServer(t)
	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "runtime-agent", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentSpec{Agents: sympoziumv1alpha1.AgentsSpec{
			Default: sympoziumv1alpha1.AgentConfig{Model: "local", BaseURL: "http://llm.local/v1"},
		}},
	}
	if err := srv.client.Create(t.Context(), agent); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/agents/runtime-agent", bytes.NewBufferString(`{"runtimeRef":"reference-v1"}`))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got sympoziumv1alpha1.Agent
	if err := srv.client.Get(t.Context(), types.NamespacedName{Name: agent.Name, Namespace: agent.Namespace}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Spec.RuntimeRef != "reference-v1" {
		t.Errorf("runtimeRef = %q, want reference-v1", got.Spec.RuntimeRef)
	}
}
