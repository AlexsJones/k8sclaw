package controller

import (
	"context"

	"github.com/go-logr/logr"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
)

// SympoziumPolicyReconciler reconciles a SympoziumPolicy object.
type SympoziumPolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=sympoziumpolicies/finalizers,verbs=update

// Reconcile handles SympoziumPolicy reconciliation.
func (r *SympoziumPolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("sympoziumpolicy", req.NamespacedName)

	var policy sympoziumv1alpha1.SympoziumPolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Count SympoziumInstances that reference this policy
	var instances sympoziumv1alpha1.SympoziumInstanceList
	if err := r.List(ctx, &instances, client.InNamespace(req.Namespace)); err != nil {
		return ctrl.Result{}, err
	}

	bound := 0
	for _, inst := range instances.Items {
		if inst.Spec.PolicyRef == policy.Name {
			bound++
		}
	}

	// Update status
	policy.Status.BoundInstances = bound
	if err := r.Status().Update(ctx, &policy); err != nil {
		log.Error(err, "failed to update policy status")
		return ctrl.Result{}, err
	}

	log.Info("Reconciled SympoziumPolicy", "boundInstances", bound)
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SympoziumPolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.SympoziumPolicy{}).
		Complete(r)
}
