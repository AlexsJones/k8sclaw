package apiserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestCreateRunWithRuntimeRefCarriesHarnessPrompt(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "test-agent", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentSpec{Agents: sympoziumv1alpha1.AgentsSpec{
			Default: sympoziumv1alpha1.AgentConfig{Model: "local", BaseURL: "http://llm.local/v1"},
		}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
	srv := NewServer(cl, nil, nil, logr.Discard())
	body := bytes.NewBufferString(`{"agentRef":"test-agent","task":"prove harness prompt","runtimeRef":"reference-v1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", body)
	resp := httptest.NewRecorder()

	srv.Handler(nil).ServeHTTP(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d: %s", resp.Code, http.StatusCreated, resp.Body.String())
	}
	var created sympoziumv1alpha1.AgentRun
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Spec.Task.Mode != "harness" {
		t.Fatalf("task.mode = %q, want harness", created.Spec.Task.Mode)
	}
	if got := created.Spec.Task.Parameters["runtime"]; got != "reference-v1" {
		t.Errorf("task.parameters.runtime = %q", got)
	}
	if got := created.Spec.Task.Parameters["prompt"]; got != "prove harness prompt" {
		t.Errorf("task.parameters.prompt = %q", got)
	}
}
