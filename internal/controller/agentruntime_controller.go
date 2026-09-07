package controller

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller/taskmodes"
)

// AgentRuntimeReconciler reconciles AgentRuntime objects. It is a validation
// gate, not a deployment controller: the runtime is an external image
// Sympozium does not run, so the reconciler's job is to verify the spec is
// something Sympozium could safely admit, and to record the resolved digest
// and a Ready condition that later slices (Agent binding, run detail) read.
type AgentRuntimeReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=agentruntimes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=agentruntimes/status,verbs=get;update;patch

func (r *AgentRuntimeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("agentruntime", req.NamespacedName)

	var runtime sympoziumv1alpha1.AgentRuntime
	if err := r.Get(ctx, req.NamespacedName, &runtime); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	digest, reason := r.validate(&runtime)
	if reason != "" {
		log.Info("AgentRuntime failed validation", "reason", reason)
		return r.updateStatus(ctx, &runtime, "", reason)
	}

	return r.updateStatus(ctx, &runtime, digest, "")
}

// validate checks the spec is something Sympozium could admit and returns the
// resolved digest on success, or a human-readable reason on failure.
func (r *AgentRuntimeReconciler) validate(runtime *sympoziumv1alpha1.AgentRuntime) (string, string) {
	image := runtime.Spec.Image
	digest, ok := sympoziumv1alpha1.ParseImageDigest(image)
	if !ok {
		return "", fmt.Sprintf("spec.image must be a digest-pinned OCI reference (e.g. \"ghcr.io/acme/harness@sha256:<64-hex>\"); got %q", image)
	}

	for _, capability := range runtime.Spec.Capabilities {
		caps, err := taskmodes.ParseCapabilities([]string{capability})
		if err != nil {
			return "", fmt.Sprintf("unknown capability %q; known: %v", capability, taskmodes.KnownCapabilities())
		}
		// outputSchema, subagents and resume are platform-mediated behaviours
		// that no external runtime can implement safely yet. Rejecting the
		// declaration keeps the descriptor honest.
		if caps.OutputSchema || caps.Subagents || caps.Resume {
			return "", fmt.Sprintf("capability %q is not supported for external runtimes; supported: [persona toolFilter]", capability)
		}
	}

	// v1alpha2 is reserved for persistent HTTP sessions. Keep the descriptor
	// honest at the existing Ready gate so a malformed session runtime cannot
	// look selectable and fail only after a user creates a session.
	if runtime.Spec.ContractVersion == "v1alpha2" {
		if runtime.Spec.Session == nil || runtime.Spec.Session.Protocol != "openai-chat" || runtime.Spec.Session.Port < 1 {
			return "", "v1alpha2 requires spec.session.protocol=openai-chat and a valid spec.session.port"
		}
	}

	return digest, ""
}

func (r *AgentRuntimeReconciler) updateStatus(ctx context.Context, runtime *sympoziumv1alpha1.AgentRuntime, digest, reason string) (ctrl.Result, error) {
	// Metadata and OCI validation cannot certify Celln admission or placement.
	// Overwrite any stale/forged positive condition until the independent
	// verifier, conformance and distribution gates are implemented.
	cellnReason, cellnMessage := "NotConfigured", "no Celln runtime profile configured"
	if runtime.Spec.Celln != nil {
		cellnReason = "VerificationUnavailable"
		cellnMessage = "Celln profile declared; artifact admission, adapter conformance and distribution verification are not implemented"
	}
	meta.SetStatusCondition(&runtime.Status.Conditions, metav1.Condition{
		Type:               sympoziumv1alpha1.AgentRuntimeCellnReadyCondition,
		Status:             metav1.ConditionFalse,
		Reason:             cellnReason,
		Message:            cellnMessage,
		ObservedGeneration: runtime.Generation,
	})

	if digest != "" {
		meta.SetStatusCondition(&runtime.Status.Conditions, metav1.Condition{
			Type:               sympoziumv1alpha1.AgentRuntimeReadyCondition,
			Status:             metav1.ConditionTrue,
			Reason:             "Validated",
			Message:            "spec validated; runtime is ready to bind",
			ObservedGeneration: runtime.Generation,
		})
		runtime.Status.ResolvedImageDigest = digest
	} else {
		meta.SetStatusCondition(&runtime.Status.Conditions, metav1.Condition{
			Type:               sympoziumv1alpha1.AgentRuntimeReadyCondition,
			Status:             metav1.ConditionFalse,
			Reason:             "Invalid",
			Message:            reason,
			ObservedGeneration: runtime.Generation,
		})
		runtime.Status.ResolvedImageDigest = ""
	}

	if err := r.Status().Update(ctx, runtime); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentRuntimeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.AgentRuntime{}).
		Complete(r)
}
