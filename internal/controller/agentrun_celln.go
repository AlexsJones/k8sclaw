// Package controller provides Celln dispatcher support for AgentRun
// reconciliation. When spec.backend == "celln", the controller dispatches the
// task to the Celln router (an HTTP service in celln-system) instead of
// creating a Kubernetes Job. The router distributes work across per-node
// Celln dispatchers, which spawn real KVM-isolated cells.
//
// Celln is for one-shot hermetic computations. It does not support ensembles,
// delegation, shared memory, IPC, NATS, streaming, or sub-agent spawns.
// Tasks that need those capabilities must use the Job backend.
package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/go-logr/logr"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

const defaultCellnRouterURL = "http://celln-router.celln-system.svc.cluster.local:8787"

func cellnRouterURL() string {
	if u := os.Getenv("CELLN_ROUTER_URL"); u != "" {
		return u
	}
	return defaultCellnRouterURL
}

var cellnHTTPClient = &http.Client{Timeout: 30 * time.Second}

// cellnSubmitAction is the JSON body for POST /v1/actions.
type cellnSubmitAction struct {
	ID      string `json:"id"`
	Task    string `json:"task"`
	Timeout uint64 `json:"timeout"`
}

// cellnActionStatus is the JSON body returned by the Celln router.
type cellnActionStatus struct {
	ID          string `json:"id"`
	Phase       string `json:"phase"`
	Output      string `json:"output,omitempty"`
	OutputHash  string `json:"outputHash,omitempty"`
	OutputBytes uint64 `json:"outputBytes,omitempty"`
	Error       string `json:"error,omitempty"`
}

// reconcilePendingCelln dispatches a pending AgentRun to the Celln router.
// On success the run transitions to Running; on failure it transitions to
// Failed with a diagnostic error.
func (r *AgentRunReconciler) reconcilePendingCelln(
	ctx context.Context,
	log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun,
) (ctrl.Result, error) {
	log.Info("Dispatching AgentRun to Celln backend")

	routerURL := cellnRouterURL()
	task := agentRun.Spec.Task.GetPrompt()
	if task == "" {
		return ctrl.Result{}, r.failRun(ctx, agentRun, "Celln backend requires a string-form task")
	}

	action := cellnSubmitAction{
		ID:   agentRun.Name,
		Task: task,
	}
	if agentRun.Spec.Timeout != nil {
		action.Timeout = uint64(agentRun.Spec.Timeout.Duration.Seconds())
	} else {
		action.Timeout = 90 // Celln agent default
	}

	body, err := json.Marshal(action)
	if err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("Celln: marshal submit action: %v", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		routerURL+"/v1/actions", bytes.NewReader(body))
	if err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("Celln: build request: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := cellnHTTPClient.Do(req)
	if err != nil {
		log.Error(err, "Celln router unreachable", "url", routerURL)
		return ctrl.Result{RequeueAfter: 10 * time.Second},
			fmt.Errorf("celln router unreachable at %s: %w", routerURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ctrl.Result{}, r.failRun(ctx, agentRun,
			fmt.Sprintf("Celln router refused dispatch (HTTP %d): %s", resp.StatusCode, string(detail)))
	}

	// Transition to Running. The Celln action ID is the AgentRun name.
	now := metav1.Time{Time: time.Now()}
	if err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		ar.Status.CellnActionID = action.ID
		ar.Status.StartedAt = &now
		ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
	}); err != nil {
		log.Error(err, "Failed to update AgentRun status after Celln dispatch")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, err
	}

	log.Info("Celln dispatch accepted", "actionId", action.ID)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileRunningCelln polls the Celln router for a running action's status
// and maps the result back to the AgentRun.
func (r *AgentRunReconciler) reconcileRunningCelln(
	ctx context.Context,
	log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun,
) (ctrl.Result, error) {
	routerURL := cellnRouterURL()
	actionID := agentRun.Status.CellnActionID
	if actionID == "" {
		return ctrl.Result{}, r.failRun(ctx, agentRun, "Celln action ID missing from status")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		routerURL+"/v1/actions/"+actionID, nil)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	resp, err := cellnHTTPClient.Do(req)
	if err != nil {
		log.Error(err, "Celln router unreachable during poll", "url", routerURL)
		return ctrl.Result{RequeueAfter: 10 * time.Second},
			fmt.Errorf("celln router unreachable during poll at %s: %w", routerURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ctrl.Result{}, r.failRun(ctx, agentRun,
			fmt.Sprintf("Celln action %q not found on router", actionID))
	}

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		log.Info("Celln router returned non-OK", "status", resp.StatusCode, "body", string(detail))
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	var status cellnActionStatus
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		log.Error(err, "Failed to decode Celln action status")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	log.Info("Celln action status", "phase", status.Phase)

	switch status.Phase {
	case "Pending", "Admitting":
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil

	case "Running":
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil

	case "Succeeded":
		now := metav1.Time{Time: time.Now()}
		if err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
			ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseSucceeded
			ar.Status.CompletedAt = &now
			ar.Status.Result = status.Output
		}); err != nil {
			log.Error(err, "Failed to persist Celln Succeeded status")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}

		log.Info("Celln action succeeded",
			"outputBytes", status.OutputBytes,
			"outputHash", status.OutputHash)
		return ctrl.Result{}, nil

	case "Failed":
		return ctrl.Result{}, r.failRun(ctx, agentRun,
			fmt.Sprintf("Celln action failed: %s", status.Error))

	default:
		log.Info("Celln action in unknown phase", "phase", status.Phase)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
}
