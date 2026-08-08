package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// newTestCellnRun builds a minimal AgentRun suitable for driving
// reconcilePendingCelln / reconcileRunningCelln directly.
func newTestCellnRun(name string, uid types.UID) *sympoziumv1alpha1.AgentRun {
	return &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			UID:       uid,
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "my-instance",
			Backend:  "celln",
			Task:     sympoziumv1alpha1.NewStringTask("do stuff"),
		},
	}
}

// ── Fix 1: action IDs must be unique per object identity, not just per name ──

func TestReconcilePendingCelln_ActionIDUniquePerUID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	t.Setenv("CELLN_ROUTER_URL", srv.URL)

	runA := newTestCellnRun("dup-name", types.UID("uid-aaaa"))
	rA := newAgentRunTestReconciler(t, runA)
	if _, err := rA.reconcilePendingCelln(context.Background(), logr.Discard(), runA); err != nil {
		t.Fatalf("reconcilePendingCelln (run A): %v", err)
	}
	var storedA sympoziumv1alpha1.AgentRun
	if err := rA.Client.Get(context.Background(), client.ObjectKeyFromObject(runA), &storedA); err != nil {
		t.Fatalf("get stored run A: %v", err)
	}

	runB := newTestCellnRun("dup-name", types.UID("uid-bbbb"))
	rB := newAgentRunTestReconciler(t, runB)
	if _, err := rB.reconcilePendingCelln(context.Background(), logr.Discard(), runB); err != nil {
		t.Fatalf("reconcilePendingCelln (run B): %v", err)
	}
	var storedB sympoziumv1alpha1.AgentRun
	if err := rB.Client.Get(context.Background(), client.ObjectKeyFromObject(runB), &storedB); err != nil {
		t.Fatalf("get stored run B: %v", err)
	}

	if storedA.Status.CellnActionID == "" || storedB.Status.CellnActionID == "" {
		t.Fatalf("expected both runs to have a CellnActionID set, got %q and %q",
			storedA.Status.CellnActionID, storedB.Status.CellnActionID)
	}
	if storedA.Status.CellnActionID == storedB.Status.CellnActionID {
		t.Fatalf("expected distinct CellnActionID for same-name AgentRuns with different UIDs, both got %q",
			storedA.Status.CellnActionID)
	}
	// Sanity: same name, so the collision would only be caught by including UID.
	if !strings.HasPrefix(storedA.Status.CellnActionID, "dup-name-") || !strings.HasPrefix(storedB.Status.CellnActionID, "dup-name-") {
		t.Fatalf("expected both action IDs to retain the AgentRun name as a prefix, got %q and %q",
			storedA.Status.CellnActionID, storedB.Status.CellnActionID)
	}
}

// ── Fix 2: controller-side backstop deadline ─────────────────────────────────

func TestReconcileRunningCelln_DeadlineExceeded_FailsRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(executionRecord{RequestID: "whatever", Phase: "Running"})
	}))
	defer srv.Close()
	t.Setenv("CELLN_ROUTER_URL", srv.URL)

	run := newTestCellnRun("wedged-run", types.UID("uid-cccc"))
	run.Spec.Timeout = &metav1.Duration{Duration: 10 * time.Second}
	run.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
	run.Status.CellnActionID = "wedged-run-uid-cccc"
	// Effective timeout is 10s + 30s slack = 40s. Start it well beyond that.
	started := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	run.Status.StartedAt = &started

	r := newAgentRunTestReconciler(t, run)
	result, err := r.reconcileRunningCelln(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Fatalf("reconcileRunningCelln returned error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue on deadline-exceeded failure, got RequeueAfter=%v", result.RequeueAfter)
	}
	var stored sympoziumv1alpha1.AgentRun
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatalf("get stored run: %v", err)
	}
	if stored.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("expected phase Failed, got %q", stored.Status.Phase)
	}
	if !strings.Contains(stored.Status.Error, "deadline") {
		t.Errorf("expected status.error to mention the deadline, got %q", stored.Status.Error)
	}
}

// ── Fix 3: RequeueAfter with a nil error, not a non-nil error ───────────────

func TestReconcilePendingCelln_RouterUnreachable_RequeuesWithoutError(t *testing.T) {
	// Port 1 is reserved/unassigned: connections to it are refused immediately
	// without any real network I/O, so this fails fast and deterministically.
	t.Setenv("CELLN_ROUTER_URL", "http://127.0.0.1:1")

	run := newTestCellnRun("unreachable-run", types.UID("uid-dddd"))
	r := newAgentRunTestReconciler(t, run)

	result, err := r.reconcilePendingCelln(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Fatalf("expected nil error so controller-runtime honors RequeueAfter, got: %v", err)
	}
	if result.RequeueAfter != 10*time.Second {
		t.Errorf("RequeueAfter = %v, want 10s", result.RequeueAfter)
	}
}

func TestReconcileRunningCelln_RouterUnreachable_RequeuesWithoutError(t *testing.T) {
	t.Setenv("CELLN_ROUTER_URL", "http://127.0.0.1:1")

	run := newTestCellnRun("unreachable-poll-run", types.UID("uid-eeee"))
	run.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
	run.Status.CellnActionID = "unreachable-poll-run-uid-eeee"
	started := metav1.NewTime(time.Now())
	run.Status.StartedAt = &started

	r := newAgentRunTestReconciler(t, run)
	result, err := r.reconcileRunningCelln(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Fatalf("expected nil error so controller-runtime honors RequeueAfter, got: %v", err)
	}
	if result.RequeueAfter != 10*time.Second {
		t.Errorf("RequeueAfter = %v, want 10s", result.RequeueAfter)
	}
}

// ── Migration: /v1/executions, forge-from-task, not /v1/actions ────────────

func TestReconcilePendingCelln_PostsAWellFormedForgeExecutionRequest(t *testing.T) {
	var gotMethod, gotPath string
	var got executionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decoding request body: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	t.Setenv("CELLN_ROUTER_URL", srv.URL)

	run := newTestCellnRun("well-formed", types.UID("uid-ffff"))
	run.Spec.Task = sympoziumv1alpha1.NewStringTask("write a haiku generator")
	run.Spec.Timeout = &metav1.Duration{Duration: 45 * time.Second}

	r := newAgentRunTestReconciler(t, run)
	if _, err := r.reconcilePendingCelln(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("reconcilePendingCelln: %v", err)
	}

	if gotMethod != http.MethodPost || gotPath != "/v1/executions" {
		t.Fatalf("expected POST /v1/executions, got %s %s", gotMethod, gotPath)
	}
	if got.APIVersion != "celln.dev/v1alpha1" {
		t.Errorf("apiVersion = %q, want celln.dev/v1alpha1", got.APIVersion)
	}
	if got.ID != cellnActionID(run) {
		t.Errorf("id = %q, want %q", got.ID, cellnActionID(run))
	}
	if got.Forge == nil || got.Forge.Task != "write a haiku generator" {
		t.Fatalf("expected forge.task to carry the AgentRun task, got %+v", got.Forge)
	}
	if got.Execution.Lane != "agent" {
		t.Errorf("execution.lane = %q, want \"agent\" — forged code is never tool-lane authority", got.Execution.Lane)
	}
	if !got.Execution.RequireHardwareIsolation {
		t.Error("expected requireHardwareIsolation: true")
	}
	if got.Capabilities.TimeoutMs != 45000 {
		t.Errorf("capabilities.timeoutMs = %d, want 45000 (spec.timeout converted to ms)", got.Capabilities.TimeoutMs)
	}
	// This is the whole point of the migration: no pre-declared mote/tools/
	// invocation should ever be sent for an AgentRun task — there isn't one.
	body, _ := json.Marshal(got)
	if strings.Contains(string(body), `"mote"`) || strings.Contains(string(body), `"invocation"`) {
		t.Errorf("forge request must not carry mote/invocation fields, got %s", body)
	}
}

func TestReconcileRunningCelln_SucceededSetsResultFromExecutionRecordOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(executionRecord{
			RequestID: "succeeded-run-uid-9999",
			Phase:     "Succeeded",
			Output:    "42",
			Receipt: &executionReceipt{
				CellID:   "cell-abc123",
				Resolved: executionResolved{Tools: []string{"blake3:deadbeef"}},
			},
		})
	}))
	defer srv.Close()
	t.Setenv("CELLN_ROUTER_URL", srv.URL)

	run := newTestCellnRun("succeeded-run", types.UID("uid-9999"))
	run.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
	run.Status.CellnActionID = "succeeded-run-uid-9999"
	started := metav1.NewTime(time.Now())
	run.Status.StartedAt = &started

	r := newAgentRunTestReconciler(t, run)
	if _, err := r.reconcileRunningCelln(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("reconcileRunningCelln: %v", err)
	}

	var stored sympoziumv1alpha1.AgentRun
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatalf("get stored run: %v", err)
	}
	if stored.Status.Phase != sympoziumv1alpha1.AgentRunPhaseSucceeded {
		t.Fatalf("expected phase Succeeded, got %q", stored.Status.Phase)
	}
	if stored.Status.Result != "42" {
		t.Errorf("expected status.result to come from the executionRecord's own Output field, got %q", stored.Status.Result)
	}
}

// ── Fix 4: backend: celln + agentSandbox.enabled are mutually exclusive ─────

func TestReconcilePending_CellnAndAgentSandboxBothEnabled_Rejected(t *testing.T) {
	// Deliberately do NOT start an httptest server and point CELLN_ROUTER_URL
	// at a closed port: if the mutual-exclusivity check were bypassed and the
	// celln dispatch path were reached, this would fail fast (not hang), but
	// the assertions below confirm the celln path is never reached at all.
	t.Setenv("CELLN_ROUTER_URL", "http://127.0.0.1:1")

	run := newTestRun()
	run.Spec.Backend = "celln"
	run.Spec.AgentSandbox = &sympoziumv1alpha1.AgentSandboxSpec{Enabled: true}

	r := newAgentRunTestReconciler(t, run, parityAgent())

	result, err := r.reconcilePending(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Fatalf("reconcilePending returned error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("expected no requeue on rejection, got RequeueAfter=%v", result.RequeueAfter)
	}
	var stored sympoziumv1alpha1.AgentRun
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatalf("get stored run: %v", err)
	}
	if stored.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("expected phase Failed, got %q (error=%q)", stored.Status.Phase, stored.Status.Error)
	}
	if !strings.Contains(stored.Status.Error, "celln") || !strings.Contains(stored.Status.Error, "agentSandbox") {
		t.Errorf("expected status.error to mention both celln and agentSandbox, got %q", stored.Status.Error)
	}
	// Neither backend should have run: no Job, no CellnActionID, no SandboxName.
	if stored.Status.CellnActionID != "" {
		t.Errorf("expected no Celln dispatch to have occurred, got CellnActionID=%q", stored.Status.CellnActionID)
	}
	if stored.Status.SandboxName != "" || stored.Status.SandboxClaimName != "" {
		t.Errorf("expected no Sandbox CR to have been created, got SandboxName=%q SandboxClaimName=%q",
			stored.Status.SandboxName, stored.Status.SandboxClaimName)
	}
}
