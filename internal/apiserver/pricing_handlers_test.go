package apiserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func newPricingTestServer(t *testing.T, objs ...client.Object) *Server {
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

func simulatedPricesBody(t *testing.T) *bytes.Reader {
	t.Helper()
	body, _ := json.Marshal(SimulatedPricesRequest{
		SimulatedEnabled: true,
		SimulatedPrices: []sympoziumv1alpha1.SimulatedPrice{
			{Provider: "llama-server", Match: "Qwen", InputPerMTokMicro: 200_000, OutputPerMTokMicro: 800_000},
		},
	})
	return bytes.NewReader(body)
}

func TestPutSimulatedPrices_ForbiddenWhenAuthDisabled(t *testing.T) {
	srv := newPricingTestServer(t)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/pricing/simulated", simulatedPricesBody(t))
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}

func TestPutSimulatedPrices_PersistsAndGetReturns(t *testing.T) {
	srv := newPricingTestServer(t)
	mux := srv.buildMux(nil, newTestTokenReader("secret"))

	req := httptest.NewRequest(http.MethodPut, "/api/v1/pricing/simulated", simulatedPricesBody(t))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	// Persisted into the SympoziumConfig singleton (auto-created).
	var config sympoziumv1alpha1.SympoziumConfig
	if err := srv.client.Get(req.Context(), types.NamespacedName{Name: "default", Namespace: "sympozium-system"}, &config); err != nil {
		t.Fatalf("get config: %v", err)
	}
	if config.Spec.Pricing == nil || !config.Spec.Pricing.SimulatedEnabled || len(config.Spec.Pricing.SimulatedPrices) != 1 {
		t.Fatalf("pricing spec = %+v", config.Spec.Pricing)
	}
	if config.Spec.Pricing.UpdatedAt == nil {
		t.Fatal("expected updatedAt stamp")
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/v1/pricing", nil)
	getReq.Header.Set("Authorization", "Bearer secret")
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", getRec.Code)
	}
	var resp PricingResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Simulated == nil || len(resp.Simulated.SimulatedPrices) != 1 {
		t.Fatalf("simulated = %+v", resp.Simulated)
	}
	if !resp.Writable {
		t.Fatal("expected writable=true with auth enabled")
	}
	if len(resp.LocalProviders) == 0 {
		t.Fatal("expected localProviders")
	}
}

func TestPutSimulatedPrices_Validation(t *testing.T) {
	srv := newPricingTestServer(t)
	mux := srv.buildMux(nil, newTestTokenReader("secret"))

	for name, prices := range map[string][]sympoziumv1alpha1.SimulatedPrice{
		"zero rate":     {{Provider: "openai", Match: "gpt-4o", InputPerMTokMicro: 0, OutputPerMTokMicro: 1}},
		"oversize rate": {{Provider: "openai", Match: "gpt-4o", InputPerMTokMicro: 1, OutputPerMTokMicro: 2_000_000_000}},
		"bad provider":  {{Provider: "bad\nprovider", Match: "gpt-4o", InputPerMTokMicro: 1, OutputPerMTokMicro: 1}},
	} {
		body, _ := json.Marshal(SimulatedPricesRequest{SimulatedEnabled: true, SimulatedPrices: prices})
		req := httptest.NewRequest(http.MethodPut, "/api/v1/pricing/simulated", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
}

func TestGetRun_SimulatedOverlayForLocalProvider(t *testing.T) {
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			Model: sympoziumv1alpha1.ModelSpec{Provider: "llama-server", Model: "Qwen3.6-35B-A3B"},
		},
		Status: sympoziumv1alpha1.AgentRunStatus{
			TokenUsage: &sympoziumv1alpha1.TokenUsage{InputTokens: 1_000_000, OutputTokens: 500_000, TotalTokens: 1_500_000},
		},
	}
	config := &sympoziumv1alpha1.SympoziumConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "sympozium-system"},
		Spec: sympoziumv1alpha1.SympoziumConfigSpec{
			Pricing: &sympoziumv1alpha1.PricingSpec{
				SimulatedEnabled: true,
				SimulatedPrices: []sympoziumv1alpha1.SimulatedPrice{
					{Provider: "llama-server", Match: "Qwen", InputPerMTokMicro: 200_000, OutputPerMTokMicro: 800_000},
				},
			},
		},
	}
	srv := newPricingTestServer(t, run, config)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var resp struct {
		Status struct {
			CostEstimate *sympoziumv1alpha1.CostEstimate `json:"costEstimate"`
		} `json:"status"`
		SimulatedCostEstimate *sympoziumv1alpha1.CostEstimate `json:"simulatedCostEstimate"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Status.CostEstimate != nil {
		t.Fatalf("local provider must have no persisted estimate, got %+v", resp.Status.CostEstimate)
	}
	sim := resp.SimulatedCostEstimate
	if sim == nil {
		t.Fatal("expected simulatedCostEstimate")
	}
	// 1M input @ 200000 + 0.5M output @ 800000 = 200000 + 400000 micro-USD.
	if sim.AmountMicro != 600_000 || sim.Source != "simulated" {
		t.Fatalf("simulated = %+v, want 600000 micro / source simulated", sim)
	}
}

func TestGetRun_NoOverlayWhenDisabled(t *testing.T) {
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-1", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			Model: sympoziumv1alpha1.ModelSpec{Provider: "llama-server", Model: "Qwen3.6-35B-A3B"},
		},
		Status: sympoziumv1alpha1.AgentRunStatus{
			TokenUsage: &sympoziumv1alpha1.TokenUsage{InputTokens: 1000, OutputTokens: 1000, TotalTokens: 2000},
		},
	}
	srv := newPricingTestServer(t, run)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-1?namespace=default", nil)
	rec := httptest.NewRecorder()
	srv.buildMux(nil, nil).ServeHTTP(rec, req)

	if bytes.Contains(rec.Body.Bytes(), []byte("simulatedCostEstimate")) {
		t.Fatal("expected no simulatedCostEstimate when overlay disabled")
	}
}

func TestDeleteSimulatedPrices(t *testing.T) {
	config := &sympoziumv1alpha1.SympoziumConfig{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: "sympozium-system"},
		Spec: sympoziumv1alpha1.SympoziumConfigSpec{
			Pricing: &sympoziumv1alpha1.PricingSpec{SimulatedEnabled: true},
		},
	}
	srv := newPricingTestServer(t, config)
	mux := srv.buildMux(nil, newTestTokenReader("secret"))

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/pricing/simulated", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}

	var got sympoziumv1alpha1.SympoziumConfig
	if err := srv.client.Get(req.Context(), types.NamespacedName{Name: "default", Namespace: "sympozium-system"}, &got); err != nil {
		t.Fatalf("get config: %v", err)
	}
	if got.Spec.Pricing != nil {
		t.Fatalf("expected pricing cleared, got %+v", got.Spec.Pricing)
	}
}
