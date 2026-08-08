// Package controller provides Celln dispatcher support for AgentRun
// reconciliation. When spec.backend == "celln", the controller dispatches the
// task to the Celln router (an HTTP service in celln-system) instead of
// creating a Kubernetes Job. The router distributes work across per-node
// Celln dispatchers, which spawn real KVM-isolated cells.
//
// Celln is for one-shot hermetic computations. It does not support ensembles,
// delegation, shared memory, IPC, NATS, streaming, or sub-agent spawns.
// Tasks that need those capabilities must use the Job backend.
//
// This speaks the celln.dev/v1alpha1 ExecutionRequest/ExecutionReceipt
// contract over POST/GET /v1/executions — not Celln's older, free-form
// /v1/actions. Every AgentRun task is a forge-from-task request: Sympozium
// has no pre-declared, hash-pinned program to name, so it asks the
// dispatcher to have a model write one. The task string itself is never
// treated as executable authority on the Celln side — only the hash Celln
// computes after actually compiling it is. See the "forge" field on
// ExecutionRequest in the Celln repo for the full contract.
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

// defaultCellnTimeout is the Celln agent's own internal timeout when
// spec.timeout is unset.
const defaultCellnTimeout = 90 * time.Second

// cellnDeadlineSlack is added on top of the effective Celln timeout before the
// controller gives up waiting on the router. The Celln-side timeout should
// fire first under normal operation; this is a backstop against a wedged
// Celln backend (crashed dispatcher, network partition, etc.) that never
// writes a terminal phase.
const cellnDeadlineSlack = 30 * time.Second

// Capability bounds for a Celln-dispatched AgentRun. Not yet exposed as
// AgentRun spec fields — fixed, conservative defaults matching Celln's own
// examples until there's a real need to make them configurable per run.
const (
	cellnMemoryBytes = 268435456 // 256MiB
	cellnOutputBytes = 65536
)

func cellnRouterURL() string {
	if u := os.Getenv("CELLN_ROUTER_URL"); u != "" {
		return u
	}
	return defaultCellnRouterURL
}

// cellnEffectiveTimeout returns the timeout used both as the forged
// program's own run deadline (sent to the dispatcher at dispatch) and as
// the basis for the controller-side backstop deadline in
// reconcileRunningCelln. Kept as a single helper so the default can't drift
// between the two call sites.
func cellnEffectiveTimeout(agentRun *sympoziumv1alpha1.AgentRun) time.Duration {
	if agentRun.Spec.Timeout != nil {
		return agentRun.Spec.Timeout.Duration
	}
	return defaultCellnTimeout
}

// cellnActionID builds a Celln execution id that is unique per Kubernetes
// object identity (not just per name). Kubernetes object names are
// reusable — delete an AgentRun and recreate one with the same name and it
// gets a fresh UID, but the Celln dispatcher's execution registry (keyed by
// this id, idempotent-by-design, with a TTL on stale entries) would
// otherwise return the previous run's stale status instead of dispatching a
// new one.
func cellnActionID(agentRun *sympoziumv1alpha1.AgentRun) string {
	return agentRun.Name + "-" + string(agentRun.UID)
}

var cellnHTTPClient = &http.Client{Timeout: 30 * time.Second}

// executionRequest mirrors celln.dev/v1alpha1's ExecutionRequest, forge
// variant only. Field names/JSON tags must match crates/celln-spec exactly.
type executionRequest struct {
	APIVersion   string              `json:"apiVersion"`
	ID           string              `json:"id"`
	Workload     executionWorkload   `json:"workload"`
	Forge        *executionForge     `json:"forge,omitempty"`
	Capabilities executionCapability `json:"capabilities"`
	Execution    executionPolicy     `json:"execution"`
}

type executionWorkload struct {
	ID     string `json:"id"`
	Caller string `json:"caller"`
}

type executionForge struct {
	Task string `json:"task"`
}

type executionCapability struct {
	Workspace   string `json:"workspace"`
	TimeoutMs   uint64 `json:"timeoutMs"`
	MemoryBytes uint64 `json:"memoryBytes"`
	OutputBytes uint64 `json:"outputBytes"`
}

type executionPolicy struct {
	Lane                     string `json:"lane"`
	RequireHardwareIsolation bool   `json:"requireHardwareIsolation"`
}

// executionRecord is the Celln dispatcher's own polling wrapper around a
// terminal ExecutionReceipt — not the receipt itself, which is an immutable
// wire contract with no room for a human-readable reason or bounded text
// output. This is where those live instead.
type executionRecord struct {
	RequestID string            `json:"requestId"`
	Phase     string            `json:"phase"`
	Reason    string            `json:"reason,omitempty"`
	Output    string            `json:"output,omitempty"`
	Receipt   *executionReceipt `json:"receipt,omitempty"`
}

// executionReceipt mirrors celln.dev/v1alpha1's ExecutionReceipt. Only
// decoded for the fields worth logging — Sympozium doesn't persist
// provenance on AgentRun.status beyond what already exists (Result).
type executionReceipt struct {
	CellID   string            `json:"cellId"`
	Resolved executionResolved `json:"resolved"`
	Output   *executionOutput  `json:"output,omitempty"`
}

type executionResolved struct {
	Tools []string `json:"tools,omitempty"`
}

type executionOutput struct {
	Hash  string `json:"hash"`
	Bytes uint64 `json:"bytes"`
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

	id := cellnActionID(agentRun)
	request := executionRequest{
		APIVersion: "celln.dev/v1alpha1",
		ID:         id,
		Workload: executionWorkload{
			ID:     agentRun.Spec.AgentRef,
			Caller: fmt.Sprintf("sympozium:%s/%s", agentRun.Namespace, agentRun.Name),
		},
		Forge: &executionForge{Task: task},
		Capabilities: executionCapability{
			Workspace:   "none",
			TimeoutMs:   uint64(cellnEffectiveTimeout(agentRun).Milliseconds()),
			MemoryBytes: cellnMemoryBytes,
			OutputBytes: cellnOutputBytes,
		},
		Execution: executionPolicy{
			Lane:                     "agent",
			RequireHardwareIsolation: true,
		},
	}

	body, err := json.Marshal(request)
	if err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("Celln: marshal execution request: %v", err))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		routerURL+"/v1/executions", bytes.NewReader(body))
	if err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("Celln: build request: %v", err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := cellnHTTPClient.Do(req)
	if err != nil {
		log.Error(err, "Celln router unreachable", "url", routerURL)
		// Return a nil error so controller-runtime honors RequeueAfter as a
		// fixed 10s retry cadence, rather than routing through its own
		// rate-limited workqueue backoff (which ignores Result when err != nil
		// and does not retry at a reliable fixed interval).
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ctrl.Result{}, r.failRun(ctx, agentRun,
			fmt.Sprintf("Celln router refused dispatch (HTTP %d): %s", resp.StatusCode, string(detail)))
	}

	// Transition to Running. The Celln execution id is derived from the
	// AgentRun's name and UID (see cellnActionID) and persisted to status so
	// reconcileRunningCelln polls using the exact id we dispatched with.
	now := metav1.Time{Time: time.Now()}
	if err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
		ar.Status.CellnActionID = id
		ar.Status.StartedAt = &now
		ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
	}); err != nil {
		log.Error(err, "Failed to update AgentRun status after Celln dispatch")
		return ctrl.Result{RequeueAfter: 2 * time.Second}, err
	}

	log.Info("Celln dispatch accepted", "executionId", id)
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileRunningCelln polls the Celln router for a running execution's
// status and maps the result back to the AgentRun.
func (r *AgentRunReconciler) reconcileRunningCelln(
	ctx context.Context,
	log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun,
) (ctrl.Result, error) {
	routerURL := cellnRouterURL()
	actionID := agentRun.Status.CellnActionID
	if actionID == "" {
		return ctrl.Result{}, r.failRun(ctx, agentRun, "Celln execution ID missing from status")
	}

	// Controller-side backstop deadline. The Celln agent's own timeout
	// (sent at dispatch, see cellnEffectiveTimeout) should fire first under
	// normal operation and produce a terminal "Failed" phase from the
	// router. This guards against a wedged Celln backend — dispatcher
	// crashed mid-request, network partition, etc. — that never writes a
	// terminal phase, which would otherwise leave the run in Running
	// forever since the phases below requeue indefinitely.
	if agentRun.Status.StartedAt != nil {
		deadline := cellnEffectiveTimeout(agentRun) + cellnDeadlineSlack
		if elapsed := time.Since(agentRun.Status.StartedAt.Time); elapsed > deadline {
			log.Info("Celln backend deadline exceeded", "elapsed", elapsed, "deadline", deadline)
			return ctrl.Result{}, r.failRun(ctx, agentRun,
				fmt.Sprintf("Celln backend deadline exceeded: no terminal status after %s (deadline %s)", elapsed.Round(time.Second), deadline))
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		routerURL+"/v1/executions/"+actionID, nil)
	if err != nil {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	resp, err := cellnHTTPClient.Do(req)
	if err != nil {
		log.Error(err, "Celln router unreachable during poll", "url", routerURL)
		// nil error so controller-runtime honors RequeueAfter as a fixed 10s
		// retry cadence instead of its own workqueue backoff (see the
		// matching comment in reconcilePendingCelln).
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return ctrl.Result{}, r.failRun(ctx, agentRun,
			fmt.Sprintf("Celln execution %q not found on router", actionID))
	}

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		log.Info("Celln router returned non-OK", "status", resp.StatusCode, "body", string(detail))
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	var record executionRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		log.Error(err, "Failed to decode Celln execution record")
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	log.Info("Celln execution status", "phase", record.Phase)

	switch record.Phase {
	case "Admitting", "Forging", "Resolving":
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil

	case "Running":
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil

	case "Succeeded":
		now := metav1.Time{Time: time.Now()}
		if err := r.updateStatusWithRetry(ctx, agentRun, func(ar *sympoziumv1alpha1.AgentRun) {
			ar.Status.Phase = sympoziumv1alpha1.AgentRunPhaseSucceeded
			ar.Status.CompletedAt = &now
			ar.Status.Result = record.Output
		}); err != nil {
			log.Error(err, "Failed to persist Celln Succeeded status")
			return ctrl.Result{RequeueAfter: 5 * time.Second}, err
		}

		if record.Receipt != nil {
			log.Info("Celln execution succeeded",
				"cellId", record.Receipt.CellID,
				"programHash", firstOrEmpty(record.Receipt.Resolved.Tools))
		}
		return ctrl.Result{}, nil

	case "Failed", "Refused", "Cancelled":
		return ctrl.Result{}, r.failRun(ctx, agentRun,
			fmt.Sprintf("Celln execution %s: %s", record.Phase, record.Reason))

	default:
		log.Info("Celln execution in unknown phase", "phase", record.Phase)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
}

func firstOrEmpty(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
