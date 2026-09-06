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

const testCellnHash = "blake3:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestCellnReceiptPreservesNullAndEmptyFields(t *testing.T) {
	wire := `{"apiVersion":"celln.dev/v1alpha1","requestId":"id","phase":"succeeded","node":"node","cellId":"cell","resolved":{"mote":null,"tools":[],"inputs":[]},"output":null,"startedAt":"2026-09-06T00:00:00Z","completedAt":"2026-09-06T00:00:01Z"}`
	var receipt executionReceipt
	if err := json.Unmarshal([]byte(wire), &receipt); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var before, after any
	if err := json.Unmarshal([]byte(wire), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("receipt contract changed during persistence: %s", encoded)
	}
}

func testFrozenCellnRequest(run *sympoziumv1alpha1.AgentRun) executionRequest {
	return executionRequest{APIVersion: "celln.dev/v1alpha1", ID: cellnActionID(run), Workload: executionWorkload{ID: run.Spec.AgentRef, Caller: "test"}, Forge: &executionForge{Task: "original"}, Capabilities: executionCapability{Workspace: "none", TimeoutMs: 90000, MemoryBytes: cellnMemoryBytes, OutputBytes: cellnOutputBytes}, Execution: executionPolicy{Lane: "agent", RequireHardwareIsolation: true}}
}

func TestCellnPinnedRequestAndFrozenRetry(t *testing.T) {
	run := newTestCellnRun(t, "pinned", "uid")
	run.Spec.Celln = &sympoziumv1alpha1.CellnExecutionSpec{
		Mote:         sympoziumv1alpha1.CellnImmutableRef{Hash: testCellnHash},
		Tools:        []sympoziumv1alpha1.CellnToolRef{{Alias: "/tool", Hash: testCellnHash}},
		Inputs:       []sympoziumv1alpha1.CellnInput{{Name: "input", Hash: testCellnHash, MediaType: "text/plain", Bytes: 4}},
		Invocation:   sympoziumv1alpha1.CellnInvocation{Alias: "/tool", Args: []string{"original"}},
		Capabilities: sympoziumv1alpha1.CellnCapabilities{Workspace: "read-only", MemoryBytes: cellnMemoryBytes, OutputBytes: cellnOutputBytes}, Lane: "tool",
	}
	run.Spec.Task = nil
	var requests []executionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testCellnToken {
			t.Error("unauthenticated dispatch")
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
	agent := parityAgent()
	agent.Name = run.Spec.AgentRef
	agent.Namespace = run.Namespace
	r := newAgentRunTestReconciler(t, run, agent)
	if _, err := r.reconcilePending(context.Background(), logr.Discard(), run); err != nil {
		t.Fatal(err)
	}
	var stored sympoziumv1alpha1.AgentRun
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Celln.Invocation.Args = []string{"mutated"}
	stored.Spec.Task = sympoziumv1alpha1.NewStringTask("different task")
	if _, err := r.reconcilePendingCelln(context.Background(), logr.Discard(), &stored); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("dispatch count %d", len(requests))
	}
	for _, req := range requests {
		if req.Forge != nil || req.Mote == nil || req.Mote.Hash != testCellnHash || req.Invocation.Args[0] != "original" || req.Execution.Lane != "tool" || len(req.Inputs) != 1 {
			t.Fatalf("lost pinned authority: %+v", req)
		}
	}
}

func TestCellnReceiptValidationRejectsFalseSuccess(t *testing.T) {
	run := newTestCellnRun(t, "receipt", "uid")
	run.Status.CellnActionID = cellnActionID(run)
	encoded, _ := json.Marshal(testFrozenCellnRequest(run))
	run.Status.CellnRequest = string(encoded)
	for _, tc := range []struct {
		name   string
		mutate func(*executionRecord)
	}{
		{"missing", func(r *executionRecord) { r.Receipt = nil }},
		{"identity", func(r *executionRecord) { r.Receipt.RequestID = "other" }},
		{"phase", func(r *executionRecord) { r.Receipt.Phase = "failed" }},
		{"version", func(r *executionRecord) { r.Receipt.APIVersion = "other" }},
		{"hash", func(r *executionRecord) { r.Receipt.Resolved.Tools[0] = "latest" }},
		{"timestamps", func(r *executionRecord) { r.Receipt.CompletedAt = "2020-01-01T00:00:00Z" }},
		{"undeclared-input", func(r *executionRecord) { r.Receipt.Resolved.Inputs = []string{testCellnHash} }},
		{"unreceipted-output", func(r *executionRecord) { r.Output = "forged" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := executionRecord{RequestID: run.Status.CellnActionID, Phase: "Succeeded", Receipt: &executionReceipt{APIVersion: "celln.dev/v1alpha1", RequestID: run.Status.CellnActionID, Phase: "succeeded", Node: "node", CellID: "cell", StartedAt: "2026-09-06T00:00:00Z", CompletedAt: "2026-09-06T00:00:01Z", Resolved: executionResolved{Tools: []string{testCellnHash}}}}
			if err := validateCellnReceipt(run, record); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&record)
			if validateCellnReceipt(run, record) == nil {
				t.Fatal("accepted false success")
			}
		})
	}
}
