package controller

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestCellnCancellationWaitsForTeardown(t *testing.T) {
	run := newTestCellnRun(t, "cancel", "uid")
	run.Status.CellnActionID = cellnActionID(run)
	run.Finalizers = []string{agentRunFinalizer}
	status := http.StatusAccepted
	phase := "Cancelling"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != "POST" || req.URL.Path != "/v1/executions/"+cellnActionID(run)+"/cancel" || req.Header.Get("Authorization") != "Bearer "+testCellnToken {
			t.Error("incorrect cancellation request")
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(executionRecord{RequestID: cellnActionID(run), Phase: phase})
	}))
	defer srv.Close()
	t.Setenv("CELLN_ROUTER_URL", srv.URL)
	r := newAgentRunTestReconciler(t, run)
	for _, code := range []int{http.StatusAccepted, http.StatusUnauthorized, http.StatusServiceUnavailable} {
		status = code
		result, err := r.reconcileDelete(context.Background(), logr.Discard(), run)
		if err != nil || result.RequeueAfter == 0 {
			t.Fatalf("cleanup did not retry HTTP %d: %+v %v", code, result, err)
		}
		var stored sympoziumv1alpha1.AgentRun
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
			t.Fatal(err)
		}
		if !controllerutil.ContainsFinalizer(&stored, agentRunFinalizer) {
			t.Fatal("removed finalizer before remote teardown")
		}
	}
	status = http.StatusOK
	if done, err := r.cancelCelln(context.Background(), run); done || err == nil {
		t.Fatal("accepted nonterminal 200")
	}
	phase = "Cancelled"
	if done, err := r.cancelCelln(context.Background(), run); !done || err != nil {
		t.Fatalf("terminal cancellation: %v %v", done, err)
	}
	phase = "Succeeded"
	if done, err := r.cancelCelln(context.Background(), run); done || err == nil {
		t.Fatal("accepted success without receipt")
	}
}

func TestCellnDeadlineDoesNotFinishWhileCleanupPending(t *testing.T) {
	run := newTestCellnRun(t, "deadline-pending", "uid")
	run.Status.CellnActionID = cellnActionID(run)
	run.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
	started := metav1.NewTime(time.Now().Add(-time.Hour))
	run.Status.StartedAt = &started
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { w.WriteHeader(http.StatusAccepted) }))
	defer srv.Close()
	t.Setenv("CELLN_ROUTER_URL", srv.URL)
	r := newAgentRunTestReconciler(t, run)
	result, err := r.reconcileRunningCelln(context.Background(), logr.Discard(), run)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("did not wait: %+v %v", result, err)
	}
	var stored sympoziumv1alpha1.AgentRun
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Status.Phase != sympoziumv1alpha1.AgentRunPhaseRunning {
		t.Fatal("reported completion before teardown")
	}
}
