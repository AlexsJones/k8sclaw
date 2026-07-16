package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/sessionkey"
)

// Defaults for WorkspaceSession provisioning. These apply when the parent
// Agent's WorkspaceSpec leaves the corresponding field empty.
const (
	defaultWorkspaceSize    = "1Gi"
	defaultWorkspaceIdleTTL = 30 * 24 * time.Hour // 30 days
	workspaceSweepInterval  = 1 * time.Hour
)

// Annotation and label keys exchanged between the AgentRun controller and
// the pod-build path so the latter knows when to swap the /workspace
// emptyDir for a session-scoped PVC and emit the recreation marker.
const (
	// WorkspacePVCAnnotation, when set on an AgentRun, names a PVC that
	// the pod builder mounts at /workspace in place of the emptyDir.
	WorkspacePVCAnnotation = "sympozium.ai/workspace-pvc"

	// WorkspaceSessionAnnotation, when set on an AgentRun, names the
	// WorkspaceSession resource that owns the PVC above. The pod
	// builder uses it to seed /workspace/.sympozium/state.json so
	// agent-runner and harness wrappers can detect a recreation.
	WorkspaceSessionAnnotation = "sympozium.ai/workspace-session"

	// SessionKeyHashLabel exposes a deterministic 16-char hex hash of
	// the AgentRun's SessionKey, so session-scoped lookups (sibling
	// locks, idle sweeps) can use cheap label selectors instead of
	// scanning every run in the namespace.
	SessionKeyHashLabel = "sympozium.ai/session-key-hash"
)

// WorkspaceSessionReconciler ensures the PVC backing each WorkspaceSession
// exists and reclaims idle sessions.
type WorkspaceSessionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=workspacesessions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=workspacesessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=workspacesessions/finalizers,verbs=update

// Reconcile ensures the PVC for a WorkspaceSession exists, owns it for
// cascade deletion, and applies the idle-TTL sweep policy.
func (r *WorkspaceSessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("workspacesession", req.NamespacedName)

	ws := &sympoziumv1alpha1.WorkspaceSession{}
	if err := r.Get(ctx, req.NamespacedName, ws); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if !ws.DeletionTimestamp.IsZero() {
		// Owned PVC will be cleaned up by Kubernetes garbage collection
		// via the owner reference; nothing extra to do here.
		return ctrl.Result{}, nil
	}

	// Validate spec — Size and SessionKey are both required by CRD
	// validation, but defend defensively in case of older CRs.
	if ws.Spec.AgentRef == "" || ws.Spec.SessionKey == "" || ws.Spec.Size == "" {
		return ctrl.Result{}, r.setFailed(ctx, ws, "invalid spec: agentRef, sessionKey, and size are required")
	}

	// Parse size — fail-fast on malformed quantities.
	sizeQty, err := resource.ParseQuantity(ws.Spec.Size)
	if err != nil {
		return ctrl.Result{}, r.setFailed(ctx, ws, fmt.Sprintf("invalid size %q: %v", ws.Spec.Size, err))
	}

	// Ensure the parent Agent exists; set an owner reference so
	// deleting the Agent cascades to its workspaces. We tolerate a
	// missing Agent (e.g. AgentRun created the session ahead of the
	// reconciler) by requeueing.
	agent := &sympoziumv1alpha1.Agent{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: ws.Namespace, Name: ws.Spec.AgentRef}, agent); err != nil {
		if errors.IsNotFound(err) {
			log.Info("Parent Agent not yet visible; requeueing", "agent", ws.Spec.AgentRef)
			return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
		}
		return ctrl.Result{}, err
	}
	if !hasOwner(ws.OwnerReferences, agent.UID) {
		if err := controllerutil.SetControllerReference(agent, ws, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting owner reference on workspacesession: %w", err)
		}
		if err := r.Update(ctx, ws); err != nil {
			return ctrl.Result{}, fmt.Errorf("updating workspacesession owner ref: %w", err)
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Decide the desired PVC name from (agent, sessionKey-hash, generation).
	generation := ws.Status.PVCGeneration
	if generation == 0 {
		generation = 1
	}
	pvcName := workspacePVCName(ws.Spec.AgentRef, ws.Spec.SessionKey, generation)

	// Ensure the PVC exists.
	pvc := &corev1.PersistentVolumeClaim{}
	getErr := r.Get(ctx, types.NamespacedName{Namespace: ws.Namespace, Name: pvcName}, pvc)
	switch {
	case errors.IsNotFound(getErr):
		pvc = r.buildPVC(ws, pvcName, sizeQty)
		if err := controllerutil.SetControllerReference(ws, pvc, r.Scheme); err != nil {
			return ctrl.Result{}, fmt.Errorf("setting owner reference on workspace PVC: %w", err)
		}
		if err := r.Create(ctx, pvc); err != nil && !errors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("creating workspace PVC: %w", err)
		}
		// If this is a regeneration (generation > 1), record why.
		if ws.Status.PVCName != "" && ws.Status.PVCName != pvcName {
			ws.Status.RecreatedInfo = &sympoziumv1alpha1.WorkspaceRecreatedInfo{
				Reason:  "PVCRecreated",
				At:      metav1.Now(),
				Message: fmt.Sprintf("PVC %q replaced by %q", ws.Status.PVCName, pvcName),
			}
		}
	case getErr != nil:
		return ctrl.Result{}, fmt.Errorf("getting workspace PVC: %w", getErr)
	default:
		// Reconcile online PVC expansion. CSI drivers with
		// allowVolumeExpansion=true (EBS gp3, GCE PD, Azure Disk, most
		// modern CSI) honour an increase to spec.resources.requests.storage
		// without unmounting. Shrinks and StorageClass changes cannot
		// be applied in place; we surface them as a Condition so the
		// operator can act (e.g. by recreating the WorkspaceSession).
		if cond, err := r.reconcilePVCResize(ctx, log, ws, pvc, sizeQty); err != nil {
			return ctrl.Result{}, err
		} else if cond != nil {
			ws.Status.Conditions = setCondition(ws.Status.Conditions, *cond)
		}
	}

	// Update status to Bound. LastTouchedAt is owned by the AgentRun
	// controller (it stamps it on every claim); we never advance it from
	// here so the sweeper observes accurate idleness.
	patch := client.MergeFrom(ws.DeepCopy())
	ws.Status.Phase = sympoziumv1alpha1.WorkspaceSessionPhaseBound
	ws.Status.PVCName = pvcName
	ws.Status.PVCGeneration = generation
	if err := r.Status().Patch(ctx, ws, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("updating workspacesession status: %w", err)
	}

	// Apply idle-TTL sweep.
	ttl := defaultWorkspaceIdleTTL
	if ws.Spec.IdleTTL != nil {
		ttl = ws.Spec.IdleTTL.Duration
	}
	if ttl > 0 && ws.Status.LastTouchedAt != nil {
		idle := time.Since(ws.Status.LastTouchedAt.Time)
		if idle >= ttl {
			// Only sweep if no live AgentRun references this session.
			busy, err := r.hasLiveAgentRun(ctx, ws)
			if err != nil {
				log.Error(err, "Failed to check for live AgentRuns; deferring sweep")
				return ctrl.Result{RequeueAfter: workspaceSweepInterval}, nil
			}
			if !busy {
				log.Info("Reclaiming idle WorkspaceSession",
					"idle", idle.String(), "ttl", ttl.String())
				if err := r.Delete(ctx, ws); err != nil && !errors.IsNotFound(err) {
					return ctrl.Result{}, fmt.Errorf("deleting idle workspacesession: %w", err)
				}
				return ctrl.Result{}, nil
			}
		}
	}

	// Periodic requeue for the sweeper.
	return ctrl.Result{RequeueAfter: workspaceSweepInterval}, nil
}

// setFailed marks the WorkspaceSession as Failed with a diagnostic message
// and returns the original error so the caller can short-circuit.
func (r *WorkspaceSessionReconciler) setFailed(ctx context.Context, ws *sympoziumv1alpha1.WorkspaceSession, msg string) error {
	patch := client.MergeFrom(ws.DeepCopy())
	ws.Status.Phase = sympoziumv1alpha1.WorkspaceSessionPhaseFailed
	ws.Status.Conditions = setCondition(ws.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             metav1.ConditionFalse,
		Reason:             "InvalidSpec",
		Message:            msg,
		LastTransitionTime: metav1.Now(),
	})
	if err := r.Status().Patch(ctx, ws, patch); err != nil {
		return fmt.Errorf("%s: %w", msg, err)
	}
	return fmt.Errorf("%s", msg)
}

// reconcilePVCResize attempts to bring the existing PVC's storage
// request in line with the WorkspaceSession's spec.Size.
//
//   - Grow: if the desired size exceeds the current request AND the
//     StorageClass advertises allowVolumeExpansion=true, patch the PVC.
//     The CSI driver performs the resize online; no pod restart needed.
//   - Shrink: K8s does not support shrinking PVCs in place. The
//     request is surfaced as a Condition (ResizeBlocked / ShrinkUnsupported)
//     and the existing PVC is left intact.
//   - Class not expandable: surfaced as a Condition
//     (ResizeBlocked / ExpansionNotAllowed); operator must switch to an
//     expandable StorageClass or recreate the WorkspaceSession.
//
// Returns a non-nil Condition when the caller should update Status.Conditions.
func (r *WorkspaceSessionReconciler) reconcilePVCResize(
	ctx context.Context,
	log logr.Logger,
	ws *sympoziumv1alpha1.WorkspaceSession,
	pvc *corev1.PersistentVolumeClaim,
	desired resource.Quantity,
) (*metav1.Condition, error) {
	current := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
	cmp := desired.Cmp(current)
	if cmp == 0 {
		// In sync — clear any stale ResizeBlocked condition.
		if meta := findCondition(ws.Status.Conditions, "ResizeBlocked"); meta != nil && meta.Status == metav1.ConditionTrue {
			return &metav1.Condition{
				Type:               "ResizeBlocked",
				Status:             metav1.ConditionFalse,
				Reason:             "InSync",
				Message:            fmt.Sprintf("PVC %q matches desired size %s", pvc.Name, desired.String()),
				LastTransitionTime: metav1.Now(),
			}, nil
		}
		return nil, nil
	}
	if cmp < 0 {
		// Shrink request — never honoured.
		log.Info("Workspace shrink requested but not supported; leaving PVC intact",
			"pvc", pvc.Name, "current", current.String(), "desired", desired.String())
		return &metav1.Condition{
			Type:               "ResizeBlocked",
			Status:             metav1.ConditionTrue,
			Reason:             "ShrinkUnsupported",
			Message:            fmt.Sprintf("cannot shrink PVC %q from %s to %s; Kubernetes does not support PVC shrink", pvc.Name, current.String(), desired.String()),
			LastTransitionTime: metav1.Now(),
		}, nil
	}

	// Grow request — only proceed if the StorageClass allows it.
	scName := storageClassName(pvc)
	expandable, scErr := r.storageClassAllowsExpansion(ctx, scName)
	if scErr != nil {
		// Treat unknown as not-expandable but log; do not error out the
		// reconcile loop since this is reported via Condition.
		log.Info("Could not resolve StorageClass for expansion check; assuming not expandable",
			"storageClass", scName, "err", scErr)
		return &metav1.Condition{
			Type:               "ResizeBlocked",
			Status:             metav1.ConditionTrue,
			Reason:             "StorageClassUnknown",
			Message:            fmt.Sprintf("could not resolve StorageClass %q to check allowVolumeExpansion: %v", scName, scErr),
			LastTransitionTime: metav1.Now(),
		}, nil
	}
	if !expandable {
		return &metav1.Condition{
			Type:               "ResizeBlocked",
			Status:             metav1.ConditionTrue,
			Reason:             "ExpansionNotAllowed",
			Message:            fmt.Sprintf("StorageClass %q does not have allowVolumeExpansion=true; cannot grow PVC %q from %s to %s", scName, pvc.Name, current.String(), desired.String()),
			LastTransitionTime: metav1.Now(),
		}, nil
	}

	patch := client.MergeFrom(pvc.DeepCopy())
	if pvc.Spec.Resources.Requests == nil {
		pvc.Spec.Resources.Requests = corev1.ResourceList{}
	}
	pvc.Spec.Resources.Requests[corev1.ResourceStorage] = desired
	if err := r.Patch(ctx, pvc, patch); err != nil {
		return nil, fmt.Errorf("patching PVC %q storage request: %w", pvc.Name, err)
	}
	log.Info("Expanded workspace PVC", "pvc", pvc.Name, "from", current.String(), "to", desired.String())
	return &metav1.Condition{
		Type:               "ResizeBlocked",
		Status:             metav1.ConditionFalse,
		Reason:             "Expanded",
		Message:            fmt.Sprintf("PVC %q expanded from %s to %s (CSI resize in progress)", pvc.Name, current.String(), desired.String()),
		LastTransitionTime: metav1.Now(),
	}, nil
}

// storageClassName returns the effective StorageClass name for the PVC,
// or "" when the cluster's default StorageClass would be used.
func storageClassName(pvc *corev1.PersistentVolumeClaim) string {
	if pvc.Spec.StorageClassName != nil {
		return *pvc.Spec.StorageClassName
	}
	return ""
}

// storageClassAllowsExpansion returns true when the named StorageClass
// has AllowVolumeExpansion set to true. An empty name means the cluster
// default — we resolve it by listing StorageClasses and finding the one
// marked as default. When no StorageClass can be resolved (RBAC, cluster
// default unset, etc.) the caller treats it as not-expandable.
func (r *WorkspaceSessionReconciler) storageClassAllowsExpansion(ctx context.Context, name string) (bool, error) {
	if name != "" {
		sc := &storagev1.StorageClass{}
		if err := r.Get(ctx, types.NamespacedName{Name: name}, sc); err != nil {
			return false, err
		}
		return sc.AllowVolumeExpansion != nil && *sc.AllowVolumeExpansion, nil
	}
	scs := &storagev1.StorageClassList{}
	if err := r.List(ctx, scs); err != nil {
		return false, err
	}
	for i := range scs.Items {
		sc := &scs.Items[i]
		if sc.Annotations["storageclass.kubernetes.io/is-default-class"] == "true" ||
			sc.Annotations["storageclass.beta.kubernetes.io/is-default-class"] == "true" {
			return sc.AllowVolumeExpansion != nil && *sc.AllowVolumeExpansion, nil
		}
	}
	return false, fmt.Errorf("no default StorageClass found")
}

// findCondition returns a pointer to the named Condition in the slice,
// or nil if not present.
func findCondition(conds []metav1.Condition, condType string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == condType {
			return &conds[i]
		}
	}
	return nil
}

// buildPVC constructs the desired PVC for this WorkspaceSession.
func (r *WorkspaceSessionReconciler) buildPVC(ws *sympoziumv1alpha1.WorkspaceSession, name string, size resource.Quantity) *corev1.PersistentVolumeClaim {
	labels := map[string]string{
		"sympozium.ai/component":        "workspace",
		"sympozium.ai/agent":            ws.Spec.AgentRef,
		"sympozium.ai/session-key-hash": sessionkey.Hash(ws.Spec.SessionKey),
		"sympozium.ai/workspacesession": ws.Name,
	}
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ws.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: size,
				},
			},
		},
	}
	if ws.Spec.StorageClassName != "" {
		sc := ws.Spec.StorageClassName
		pvc.Spec.StorageClassName = &sc
	}
	return pvc
}

// hasLiveAgentRun returns true if any non-terminal AgentRun in the same
// namespace currently claims this (agent, sessionKey) pair.
func (r *WorkspaceSessionReconciler) hasLiveAgentRun(ctx context.Context, ws *sympoziumv1alpha1.WorkspaceSession) (bool, error) {
	runs := &sympoziumv1alpha1.AgentRunList{}
	if err := r.List(ctx, runs,
		client.InNamespace(ws.Namespace),
		client.MatchingLabels{
			"sympozium.ai/instance":         ws.Spec.AgentRef,
			"sympozium.ai/session-key-hash": sessionkey.Hash(ws.Spec.SessionKey),
		},
	); err != nil {
		return false, err
	}
	for _, run := range runs.Items {
		if !isTerminalPhase(run.Status.Phase) {
			return true, nil
		}
	}
	return false, nil
}

// isTerminalPhase reports whether an AgentRun phase is final and frees
// the workspace lock.
func isTerminalPhase(p sympoziumv1alpha1.AgentRunPhase) bool {
	switch p {
	case sympoziumv1alpha1.AgentRunPhaseSucceeded, sympoziumv1alpha1.AgentRunPhaseFailed:
		return true
	}
	return false
}

// hasOwner reports whether OwnerReferences already contain an entry with
// the given UID.
func hasOwner(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, ref := range refs {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

// setCondition upserts a condition by type, preserving LastTransitionTime
// when status does not change.
func setCondition(conds []metav1.Condition, c metav1.Condition) []metav1.Condition {
	for i, existing := range conds {
		if existing.Type == c.Type {
			if existing.Status == c.Status {
				c.LastTransitionTime = existing.LastTransitionTime
			}
			conds[i] = c
			return conds
		}
	}
	return append(conds, c)
}

// workspacePVCName returns the canonical PVC name for a WorkspaceSession.
// Format: ws-<agent>-<hash>-g<generation>. The hash provides a stable
// identifier without exposing the free-form session key in object names.
func workspacePVCName(agent, sessionKey string, generation int32) string {
	return fmt.Sprintf("ws-%s-%s-g%d", agent, sessionkey.Hash(sessionKey), generation)
}

// workspaceSessionName returns the canonical WorkspaceSession resource
// name for a given (agent, sessionKey) pair. Shared by the AgentRun
// controller so it can look up or create the session deterministically.
func workspaceSessionName(agent, sessionKey string) string {
	return fmt.Sprintf("ws-%s-%s", agent, sessionkey.Hash(sessionKey))
}

// SetupWithManager wires the reconciler into the controller manager.
func (r *WorkspaceSessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.WorkspaceSession{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Complete(r)
}

// agentWantsPerSessionPVC reports whether the parent Agent has opted into
// per-session workspace PVCs.
func agentWantsPerSessionPVC(agent *sympoziumv1alpha1.Agent) bool {
	return agent != nil && agent.Spec.Workspace != nil && agent.Spec.Workspace.PerSessionPVC
}

// agentRunQualifiesForSessionPVC reports whether this AgentRun should
// participate in the per-session PVC / session-lock machinery: it must
// carry a non-empty SessionKey, must not be a sub-agent (sub-agents stay
// ephemeral by design), and its parent Agent must opt in.
func agentRunQualifiesForSessionPVC(agentRun *sympoziumv1alpha1.AgentRun, agent *sympoziumv1alpha1.Agent) bool {
	if agentRun == nil || agentRun.Spec.SessionKey == "" {
		return false
	}
	if agentRun.Spec.Parent != nil {
		return false
	}
	return agentWantsPerSessionPVC(agent)
}

// ensureWorkspaceSession idempotently creates the WorkspaceSession that
// owns the per-session PVC for (agent, sessionKey). Returns the
// canonical PVC name and the resource name of the WorkspaceSession.
// The caller is expected to stamp these onto the AgentRun's annotations
// so the pod builder can mount the PVC.
//
// When the WorkspaceSession already exists, mutable fields (Size,
// IdleTTL) are re-synced from the parent Agent's WorkspaceSpec so edits
// to the Agent (or to the Ensemble that stamps the Agent) propagate on
// the next AgentRun. Whether a Size change can actually be applied to
// the underlying PVC is decided by the WorkspaceSession reconciler:
// grow-only when the StorageClass allows online expansion; otherwise
// the request is surfaced as a Condition and the existing PVC is left
// intact. StorageClassName is captured at creation time and is NOT
// re-synced — switching storage classes requires a new PVC (PR4b).
func ensureWorkspaceSession(
	ctx context.Context,
	c client.Client,
	scheme *runtime.Scheme,
	agent *sympoziumv1alpha1.Agent,
	sessionKey string,
) (pvcName, wsName string, err error) {
	wsName = workspaceSessionName(agent.Name, sessionKey)
	ws := &sympoziumv1alpha1.WorkspaceSession{}
	getErr := c.Get(ctx, types.NamespacedName{Namespace: agent.Namespace, Name: wsName}, ws)
	switch {
	case errors.IsNotFound(getErr):
		ws = buildWorkspaceSessionFromAgent(agent, wsName, sessionKey)
		if err := controllerutil.SetControllerReference(agent, ws, scheme); err != nil {
			return "", "", fmt.Errorf("setting owner ref on workspacesession: %w", err)
		}
		if err := c.Create(ctx, ws); err != nil && !errors.IsAlreadyExists(err) {
			return "", "", fmt.Errorf("creating workspacesession: %w", err)
		}
		// Re-Get after create so we have a populated status (PVC name
		// will be filled in by the reconciler's first pass, but we can
		// derive it deterministically here).
	case getErr != nil:
		return "", "", fmt.Errorf("getting workspacesession: %w", getErr)
	default:
		// Re-sync mutable fields from the parent Agent.
		desiredSize := desiredSize(agent)
		desiredTTL := desiredIdleTTL(agent)
		needsPatch := false
		patch := client.MergeFrom(ws.DeepCopy())
		if ws.Spec.Size != desiredSize {
			ws.Spec.Size = desiredSize
			needsPatch = true
		}
		if !idleTTLEqual(ws.Spec.IdleTTL, desiredTTL) {
			ws.Spec.IdleTTL = desiredTTL
			needsPatch = true
		}
		if needsPatch {
			if err := c.Patch(ctx, ws, patch); err != nil {
				return "", "", fmt.Errorf("patching workspacesession spec: %w", err)
			}
		}
	}

	generation := ws.Status.PVCGeneration
	if generation == 0 {
		generation = 1
	}
	pvcName = workspacePVCName(agent.Name, sessionKey, generation)
	return pvcName, wsName, nil
}

// desiredSize returns the workspace size the parent Agent currently asks
// for, falling back to the controller default when unspecified.
func desiredSize(agent *sympoziumv1alpha1.Agent) string {
	if agent.Spec.Workspace == nil || agent.Spec.Workspace.Size == "" {
		return defaultWorkspaceSize
	}
	return agent.Spec.Workspace.Size
}

// desiredIdleTTL returns the IdleTTL the parent Agent currently asks
// for, or nil to fall back to the controller default.
func desiredIdleTTL(agent *sympoziumv1alpha1.Agent) *metav1.Duration {
	if agent.Spec.Workspace == nil {
		return nil
	}
	return agent.Spec.Workspace.IdleTTL
}

// idleTTLEqual compares two *metav1.Duration values structurally.
func idleTTLEqual(a, b *metav1.Duration) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return a.Duration == b.Duration
	}
}

// buildWorkspaceSessionFromAgent constructs a new WorkspaceSession CR
// from the parent Agent's WorkspaceSpec, applying defaults for any
// unspecified fields.
func buildWorkspaceSessionFromAgent(
	agent *sympoziumv1alpha1.Agent,
	wsName, sessionKey string,
) *sympoziumv1alpha1.WorkspaceSession {
	spec := agent.Spec.Workspace
	size := defaultWorkspaceSize
	storageClass := ""
	var ttl *metav1.Duration
	if spec != nil {
		if spec.Size != "" {
			size = spec.Size
		}
		storageClass = spec.StorageClassName
		ttl = spec.IdleTTL
	}
	return &sympoziumv1alpha1.WorkspaceSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:      wsName,
			Namespace: agent.Namespace,
			Labels: map[string]string{
				"sympozium.ai/agent":     agent.Name,
				SessionKeyHashLabel:      sessionkey.Hash(sessionKey),
				"sympozium.ai/component": "workspace",
			},
		},
		Spec: sympoziumv1alpha1.WorkspaceSessionSpec{
			AgentRef:         agent.Name,
			SessionKey:       sessionKey,
			Size:             size,
			StorageClassName: storageClass,
			IdleTTL:          ttl,
		},
	}
}

// touchWorkspaceSession bumps LastTouchedAt + LastRunName on the named
// WorkspaceSession so the sweeper observes recent activity. Best-effort:
// transient errors are logged but do not block the AgentRun reconcile,
// since stale touch info only affects idle reclamation timing.
func touchWorkspaceSession(
	ctx context.Context,
	c client.Client,
	namespace, wsName string,
	agentRun *sympoziumv1alpha1.AgentRun,
) error {
	ws := &sympoziumv1alpha1.WorkspaceSession{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: wsName}, ws); err != nil {
		return err
	}
	patch := client.MergeFrom(ws.DeepCopy())
	now := metav1.Now()
	ws.Status.LastTouchedAt = &now
	ws.Status.LastRunName = agentRun.Name
	return c.Status().Patch(ctx, ws, patch)
}

// peerBlocksAdmission reports whether peer should prevent self from
// acquiring the session lock. A peer blocks when it already holds the
// lock (Running/Serving, or it has a Job / start time recorded), or when
// it is a fellow waiter that is older than self — creationTimestamp
// first, name as a deterministic tie-break. This gives waiters a strict
// FIFO order, so two Pending runs can never block each other forever.
func peerBlocksAdmission(self, peer *sympoziumv1alpha1.AgentRun) bool {
	if isTerminalPhase(peer.Status.Phase) {
		return false
	}
	switch peer.Status.Phase {
	case sympoziumv1alpha1.AgentRunPhaseRunning, sympoziumv1alpha1.AgentRunPhaseServing:
		return true
	}
	if peer.Status.JobName != "" || peer.Status.StartedAt != nil {
		return true
	}
	// Both are waiters: only an older peer blocks (FIFO admission).
	if !peer.CreationTimestamp.Time.Equal(self.CreationTimestamp.Time) {
		return peer.CreationTimestamp.Time.Before(self.CreationTimestamp.Time)
	}
	return peer.Name < self.Name
}

// listBlockingSessionPeers returns AgentRuns in the namespace that share
// self's (agent, sessionKeyHash) and block self from acquiring the
// session lock: peers that already hold the lock, plus older waiters.
// See peerBlocksAdmission for the exact rules.
func listBlockingSessionPeers(
	ctx context.Context,
	c client.Client,
	self *sympoziumv1alpha1.AgentRun,
	agentRef, sessionKeyHash string,
) ([]sympoziumv1alpha1.AgentRun, error) {
	runs := &sympoziumv1alpha1.AgentRunList{}
	if err := c.List(ctx, runs,
		client.InNamespace(self.Namespace),
		client.MatchingLabels{
			"sympozium.ai/instance": agentRef,
			SessionKeyHashLabel:     sessionKeyHash,
		},
	); err != nil {
		return nil, err
	}
	blocking := make([]sympoziumv1alpha1.AgentRun, 0, len(runs.Items))
	for i := range runs.Items {
		run := &runs.Items[i]
		if run.Name == self.Name {
			continue
		}
		if peerBlocksAdmission(self, run) {
			blocking = append(blocking, *run)
		}
	}
	return blocking, nil
}
