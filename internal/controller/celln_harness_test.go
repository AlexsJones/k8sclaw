package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func harnessRun(t *testing.T) *sympoziumv1alpha1.AgentRun {
	run := newTestCellnRun(t, "harness-binding", "uid")
	run.Spec.Model = sympoziumv1alpha1.ModelSpec{Provider: "deepseek", Model: "deepseek-chat"}
	run.Spec.Celln = &sympoziumv1alpha1.CellnExecutionSpec{
		Mote:       sympoziumv1alpha1.CellnImmutableRef{Hash: testCellnHash},
		Tools:      []sympoziumv1alpha1.CellnToolRef{{Alias: "/harness", Hash: testCellnHash, Closure: &sympoziumv1alpha1.CellnImmutableRef{Hash: testCellnHash}}},
		Invocation: sympoziumv1alpha1.CellnInvocation{Alias: "/harness"}, Lane: "agent",
		Capabilities: sympoziumv1alpha1.CellnCapabilities{Workspace: "none", Egress: []string{"https://api.deepseek.com"}, MemoryBytes: cellnMemoryBytes, OutputBytes: cellnOutputBytes},
		Harness: &sympoziumv1alpha1.CellnHarnessSpec{ContractVersion: "celln.reference-functions/v1", ModelGrant: sympoziumv1alpha1.CellnImmutableRef{Hash: testCellnHash}, BorrowedTools: []sympoziumv1alpha1.CellnBorrowedTool{
			{Name: "add", Path: "/add", Hash: testCellnHash, Description: "add two integer strings"},
			{Name: "multiply", Path: "/multiply", Hash: testCellnHash, Description: "multiply two integer strings"},
		}},
	}
	return run
}

func TestCellnHarnessFreezesSelectedModelTaskAndTools(t *testing.T) {
	testCellnHarnessFreezing(t, false)
}

func TestCellnJSONHarnessFreezesPersonaSchemasLimitsAndVersion(t *testing.T) {
	testCellnHarnessFreezing(t, true)
}

func testCellnHarnessFreezing(t *testing.T, jsonAdapter bool) {
	t.Setenv("CELLN_HARNESS_ENABLED", "true")
	run := harnessRun(t)
	version := "celln.dev/v1alpha2"
	if jsonAdapter {
		version = "celln.dev/v1alpha3"
		run.Spec.SystemPrompt = "Approved persona"
		run.Spec.Celln.Harness.ContractVersion = "celln.json-tools/v1"
		run.Spec.Celln.Harness.JSON = &sympoziumv1alpha1.CellnHarnessJSONLimits{MaxTurns: 3, MaxCalls: 2}
		for i := range run.Spec.Celln.Harness.BorrowedTools {
			run.Spec.Celln.Harness.BorrowedTools[i].JSONStdio = &sympoziumv1alpha1.CellnJSONToolIO{ABI: "celln.json-stdio/v1", InputSchema: testCellnHash, OutputSchema: testCellnHash, InputBytes: 1024, OutputBytes: 1024, TimeoutMs: 1000}
		}
	}
	var requests []executionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testCellnToken {
			t.Error("missing auth")
		}
		var req executionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		requests = append(requests, req)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	t.Setenv("CELLN_ROUTER_URL", srv.URL)
	r := newAgentRunTestReconciler(t, run)
	if _, err := r.reconcilePendingCelln(context.Background(), logr.Discard(), run); err != nil {
		t.Fatal(err)
	}
	var stored sympoziumv1alpha1.AgentRun
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Model.Model = "mutated-model"
	stored.Spec.Task = sympoziumv1alpha1.NewStringTask("mutated task")
	stored.Spec.Celln.Harness.BorrowedTools[0].Path = "/mutated"
	stored.Spec.Celln.Harness.ModelGrant.Hash = "mutated"
	if jsonAdapter {
		stored.Spec.SystemPrompt = "mutated persona"
		stored.Spec.Celln.Harness.JSON.MaxCalls = 16
		stored.Spec.Celln.Harness.BorrowedTools[0].JSONStdio.InputSchema = "mutated schema"
	}
	if _, err := r.reconcilePendingCelln(context.Background(), logr.Discard(), &stored); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatalf("frozen identity changed: %+v", requests)
	}
	req := requests[0]
	if req.APIVersion != version || req.Harness == nil || req.Harness.Model != "deepseek-chat" || req.Harness.Task != "do stuff" || req.Harness.BorrowedTools[0].Path != "/add" {
		t.Fatalf("missing binding: %+v", req)
	}
	if jsonAdapter && (req.Harness.JSON.System != "Approved persona" || req.Harness.JSON.MaxCalls != 2 || req.Harness.BorrowedTools[0].JSONStdio.InputSchema != testCellnHash) {
		t.Fatal("JSON persona or schema/limit identities were not frozen")
	}
	for _, mutate := range []func(*executionRequest){
		func(r *executionRequest) { r.APIVersion = "celln.dev/v1alpha1" },
		func(r *executionRequest) { r.Harness = nil },
		func(r *executionRequest) { r.Harness.BorrowedTools[0].Path = "/../bad" },
		func(r *executionRequest) { r.Capabilities.Egress = []string{"https://other.example"} },
	} {
		bytes, _ := json.Marshal(req)
		var bad executionRequest
		_ = json.Unmarshal(bytes, &bad)
		mutate(&bad)
		if validateCellnRequest(bad) == nil {
			t.Fatal("accepted invalid Harness binding")
		}
	}
	receipt := executionRecord{RequestID: stored.Status.CellnActionID, Phase: "Succeeded", Receipt: &executionReceipt{APIVersion: version, RequestID: stored.Status.CellnActionID, Phase: "succeeded", Node: "node", CellID: "cell", StartedAt: "2026-09-07T00:00:00Z", CompletedAt: "2026-09-07T00:00:01Z", Resolved: executionResolved{Mote: &req.Mote.Hash, Tools: []string{testCellnHash}}}}
	if err := validateCellnReceipt(&stored, receipt); err != nil {
		t.Fatal(err)
	}
	receipt.Receipt.APIVersion = "celln.dev/v1alpha1"
	if validateCellnReceipt(&stored, receipt) == nil {
		t.Fatal("accepted downgraded receipt")
	}
	if jsonAdapter {
		receipt.Receipt.APIVersion = "celln.dev/v1alpha2"
		if validateCellnReceipt(&stored, receipt) == nil {
			t.Fatal("accepted reference receipt for JSON execution")
		}
	}
}

func TestCellnHarnessRefusesIgnoredModelAuthorityAndDisabledGate(t *testing.T) {
	for _, mutate := range []func(*sympoziumv1alpha1.ModelSpec){
		func(m *sympoziumv1alpha1.ModelSpec) { m.Provider = "openai" },
		func(m *sympoziumv1alpha1.ModelSpec) { m.AuthSecretRef = "tenant-key" },
		func(m *sympoziumv1alpha1.ModelSpec) { m.BaseURL = "https://other.example" },
		func(m *sympoziumv1alpha1.ModelSpec) { m.Thinking = "high" },
		func(m *sympoziumv1alpha1.ModelSpec) { m.ProviderHeaders = map[string]string{"X-Test": "value"} },
		func(m *sympoziumv1alpha1.ModelSpec) { m.ProviderHeadersSecretRef = "headers" },
		func(m *sympoziumv1alpha1.ModelSpec) { m.ModelRef = "local" },
		func(m *sympoziumv1alpha1.ModelSpec) { m.NodeSelector = map[string]string{"node": "x"} },
	} {
		m := sympoziumv1alpha1.ModelSpec{Provider: "deepseek"}
		mutate(&m)
		if cellnHarnessModelSupported(m) {
			t.Fatal("ignored model authority")
		}
	}
	t.Setenv("CELLN_HARNESS_ENABLED", "")
	run := harnessRun(t)
	r := newAgentRunTestReconciler(t, run)
	if _, err := r.reconcilePendingCelln(context.Background(), logr.Discard(), run); err != nil {
		t.Fatal(err)
	}
	if run.Status.CellnRequest != "" {
		t.Fatal("disabled binding dispatched")
	}
}

func TestCellnReferenceHarnessRefusesIgnoredPersona(t *testing.T) {
	t.Setenv("CELLN_HARNESS_ENABLED", "true")
	run := harnessRun(t)
	run.Spec.SystemPrompt = "This must not be silently discarded"
	r := newAgentRunTestReconciler(t, run)
	if _, err := r.reconcilePendingCelln(context.Background(), logr.Discard(), run); err != nil {
		t.Fatal(err)
	}
	var stored sympoziumv1alpha1.AgentRun
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.CellnRequest != "" || stored.Status.Phase != "Failed" {
		t.Fatal("reference adapter accepted unsupported persona")
	}
}
