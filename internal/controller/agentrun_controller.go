package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
	"github.com/alexsjones/sympozium/internal/orchestrator"
)

const agentRunFinalizer = "sympozium.ai/agentrun-finalizer"
const systemNamespace = "sympozium-system"

// DefaultRunHistoryLimit is how many completed AgentRuns to keep per instance
// before the oldest are pruned.
const DefaultRunHistoryLimit = 50

// AgentRunReconciler reconciles AgentRun objects.
// It watches AgentRun CRDs and reconciles them into Kubernetes Jobs/Pods.
type AgentRunReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	Log             logr.Logger
	PodBuilder      *orchestrator.PodBuilder
	Clientset       kubernetes.Interface
	ImageTag        string // release tag for Sympozium images (e.g. "v0.0.25")
	RunHistoryLimit int    // max completed runs to keep per instance (0 = use default)
}

const imageRegistry = "ghcr.io/alexsjones/sympozium"

// imageRef returns a fully qualified image reference using the reconciler's tag.
func (r *AgentRunReconciler) imageRef(name string) string {
	tag := r.ImageTag
	if tag == "" {
		tag = "latest"
	}
	return fmt.Sprintf("%s/%s:%s", imageRegistry, name, tag)
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=agentruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=agentruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=agentruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/log,verbs=get
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings;clusterroles;clusterrolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles AgentRun create/update/delete events.
func (r *AgentRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("agentrun", req.NamespacedName)

	// Fetch the AgentRun
	agentRun := &sympoziumv1alpha1.AgentRun{}
	if err := r.Get(ctx, req.NamespacedName, agentRun); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !agentRun.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, log, agentRun)
	}

	// Add finalizer only for non-terminal runs. Completed/failed runs have
	// their finalizer removed in reconcileCompleted; we must not re-add it or
	// we create an infinite remove→add→remove loop.
	isTerminal := agentRun.Status.Phase == sympoziumv1alpha1.AgentRunPhaseSucceeded ||
		agentRun.Status.Phase == sympoziumv1alpha1.AgentRunPhaseFailed
	if !isTerminal && !controllerutil.ContainsFinalizer(agentRun, agentRunFinalizer) {
		controllerutil.AddFinalizer(agentRun, agentRunFinalizer)
		if err := r.Update(ctx, agentRun); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}

	// Reconcile based on current phase
	switch agentRun.Status.Phase {
	case "", sympoziumv1alpha1.AgentRunPhasePending:
		return r.reconcilePending(ctx, log, agentRun)
	case sympoziumv1alpha1.AgentRunPhaseRunning:
		return r.reconcileRunning(ctx, log, agentRun)
	case sympoziumv1alpha1.AgentRunPhaseSucceeded, sympoziumv1alpha1.AgentRunPhaseFailed:
		return r.reconcileCompleted(ctx, log, agentRun)
	default:
		log.Info("Unknown phase", "phase", agentRun.Status.Phase)
		return ctrl.Result{}, nil
	}
}

// reconcilePending handles an AgentRun that needs a Job created.
func (r *AgentRunReconciler) reconcilePending(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	log.Info("Reconciling pending AgentRun")

	// Validate against policy
	if err := r.validatePolicy(ctx, agentRun); err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("policy validation failed: %v", err))
	}

	// Ensure the sympozium-agent ServiceAccount exists in the target namespace.
	if err := r.ensureAgentServiceAccount(ctx, agentRun.Namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring agent service account: %w", err)
	}

	// Create the input ConfigMap with the task
	if err := r.createInputConfigMap(ctx, agentRun); err != nil {
		return ctrl.Result{}, fmt.Errorf("creating input ConfigMap: %w", err)
	}

	// Look up the SympoziumInstance to check for memory configuration.
	instance := &sympoziumv1alpha1.SympoziumInstance{}
	memoryEnabled := false
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Spec.InstanceRef,
	}, instance); err == nil {
		if instance.Spec.Memory != nil && instance.Spec.Memory.Enabled {
			memoryEnabled = true
		}
		// If the AgentRun has no skills, inherit from the SympoziumInstance.
		// This is a safety net — tuiCreateRun and the schedule controller
		// should already copy skills, but older runs or manual CRs may not.
		if len(agentRun.Spec.Skills) == 0 && len(instance.Spec.Skills) > 0 {
			agentRun.Spec.Skills = instance.Spec.Skills
		}
	}

	// Resolve skill sidecars from SkillPack CRDs.
	sidecars := r.resolveSkillSidecars(ctx, log, agentRun)

	// Mirror skill ConfigMaps from sympozium-system into the agent namespace
	// so projected volumes can reference them (ConfigMaps are namespace-local).
	if err := r.mirrorSkillConfigMaps(ctx, log, agentRun); err != nil {
		log.Error(err, "Failed to mirror skill ConfigMaps, skills may be missing")
	}

	// Create RBAC resources for skill sidecars that need them.
	if err := r.ensureSkillRBAC(ctx, log, agentRun, sidecars); err != nil {
		log.Error(err, "Failed to create skill RBAC, continuing without")
	}

	// Build and create the Job
	job := r.buildJob(agentRun, memoryEnabled, sidecars)
	if err := controllerutil.SetControllerReference(agentRun, job, r.Scheme); err != nil {
		return ctrl.Result{}, fmt.Errorf("setting owner reference: %w", err)
	}

	if err := r.Create(ctx, job); err != nil {
		if errors.IsAlreadyExists(err) {
			log.Info("Job already exists")
		} else {
			return ctrl.Result{}, fmt.Errorf("creating Job: %w", err)
		}
	}

	// Update status to Running
	now := metav1.Now()
	agentRun.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
	agentRun.Status.JobName = job.Name
	agentRun.Status.StartedAt = &now
	if err := r.Status().Update(ctx, agentRun); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileRunning checks on a running Job and updates status.
func (r *AgentRunReconciler) reconcileRunning(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	log.Info("Checking running AgentRun")

	// Find the Job
	job := &batchv1.Job{}
	jobName := client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Status.JobName,
	}
	if err := r.Get(ctx, jobName, job); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, r.failRun(ctx, agentRun, "Job not found")
		}
		return ctrl.Result{}, err
	}

	// Update pod name from Job
	if agentRun.Status.PodName == "" {
		podList := &corev1.PodList{}
		if err := r.List(ctx, podList,
			client.InNamespace(agentRun.Namespace),
			client.MatchingLabels{"sympozium.ai/agent-run": agentRun.Name},
		); err == nil && len(podList.Items) > 0 {
			agentRun.Status.PodName = podList.Items[0].Name
			_ = r.Status().Update(ctx, agentRun)
		}
	}

	// Check Job completion
	if job.Status.Succeeded > 0 {
		// Extract the LLM response from pod logs before the pod is gone.
		result, usage := r.extractResultFromPod(ctx, log, agentRun)
		// Extract and persist memory updates if applicable.
		r.extractAndPersistMemory(ctx, log, agentRun)
		return r.succeedRun(ctx, agentRun, result, usage)
	}
	if job.Status.Failed > 0 {
		return ctrl.Result{}, r.failRun(ctx, agentRun, "Job failed")
	}

	// When the pod has skill sidecar containers (3+ containers), those
	// sidecars may keep the pod alive long after the agent has finished,
	// preventing the Job from reporting success. Detect agent completion
	// at the container level and clean up proactively.
	// For simple 2-container pods (agent + ipc-bridge), skip this check —
	// the ipc-bridge exits shortly after the agent and the Job completes
	// naturally.
	if agentRun.Status.PodName != "" {
		if done, exitCode, reason, hasSidecars := r.checkAgentContainer(ctx, log, agentRun); done && hasSidecars {
			if exitCode == 0 {
				log.Info("Agent container terminated successfully; cleaning up lingering sidecars")
				result, usage := r.extractResultFromPod(ctx, log, agentRun)
				r.extractAndPersistMemory(ctx, log, agentRun)
				// Delete the Job so Kubernetes kills remaining sidecar containers.
				_ = r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
				return r.succeedRun(ctx, agentRun, result, usage)
			}
			errMsg := fmt.Sprintf("agent container exited with code %d", exitCode)
			if reason != "" {
				errMsg = fmt.Sprintf("%s (%s)", errMsg, reason)
			}
			log.Info("Agent container terminated with error; cleaning up", "exitCode", exitCode, "reason", reason)
			// Try to extract the error from pod logs before cleaning up.
			if logErr, _ := r.extractResultFromPod(ctx, log, agentRun); logErr != "" {
				errMsg = logErr
			}
			_ = r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationBackground))
			return ctrl.Result{}, r.failRun(ctx, agentRun, errMsg)
		}
	}

	// Check timeout (explicit spec timeout or hard default for scheduled runs).
	if agentRun.Status.StartedAt != nil {
		elapsed := time.Since(agentRun.Status.StartedAt.Time)
		timeout := 10 * time.Minute // default hard timeout
		if agentRun.Spec.Timeout != nil {
			timeout = agentRun.Spec.Timeout.Duration
		}
		if elapsed > timeout {
			log.Info("AgentRun timed out", "elapsed", elapsed, "timeout", timeout)
			// Delete the Job to kill the pod
			_ = r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground))
			return ctrl.Result{}, r.failRun(ctx, agentRun, "timeout")
		}
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// checkAgentContainer inspects the pod's container statuses and returns:
//   - done: whether the "agent" container has terminated
//   - exitCode: the container exit code (only meaningful when done=true)
//   - reason: the termination reason string (e.g. "OOMKilled", "Error")
//   - hasSidecars: whether the pod has more than 2 containers (agent + ipc-bridge),
//     indicating skill sidecars that could keep the pod alive after the agent exits
func (r *AgentRunReconciler) checkAgentContainer(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (done bool, exitCode int32, reason string, hasSidecars bool) {
	pod := &corev1.Pod{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Status.PodName,
	}, pod); err != nil {
		return false, 0, "", false
	}

	hasSidecars = len(pod.Spec.Containers) > 2

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != "agent" {
			continue
		}
		if cs.State.Terminated != nil {
			return true, cs.State.Terminated.ExitCode, cs.State.Terminated.Reason, hasSidecars
		}
		return false, 0, "", hasSidecars
	}
	return false, 0, "", hasSidecars
}

// reconcileCompleted handles cleanup of completed AgentRuns.
// Instead of deleting immediately, it keeps up to RunHistoryLimit completed
// runs per instance and prunes only the oldest ones beyond that threshold.
func (r *AgentRunReconciler) reconcileCompleted(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	// Clean up cluster-scoped RBAC created for skill sidecars.
	r.cleanupSkillRBAC(ctx, log, agentRun)

	// Remove the finalizer so the CR can be deleted later if needed.
	// Use a Patch (not Update) to avoid overwriting status fields (like
	// tokenUsage) that were set by the status subresource update in succeedRun.
	if controllerutil.ContainsFinalizer(agentRun, agentRunFinalizer) {
		log.Info("Removing finalizer from completed AgentRun")
		patch := client.MergeFrom(agentRun.DeepCopy())
		controllerutil.RemoveFinalizer(agentRun, agentRunFinalizer)
		if err := r.Patch(ctx, agentRun, patch); err != nil {
			if errors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}

	// Prune old runs beyond the history limit for this instance.
	if err := r.pruneOldRuns(ctx, log, agentRun); err != nil {
		log.Error(err, "Failed to prune old AgentRuns")
		// Non-fatal: don't block reconciliation.
	}

	return ctrl.Result{}, nil
}

// runHistoryLimit returns the effective run history limit.
func (r *AgentRunReconciler) runHistoryLimit() int {
	if r.RunHistoryLimit > 0 {
		return r.RunHistoryLimit
	}
	return DefaultRunHistoryLimit
}

// pruneOldRuns lists all completed runs for the same instance and deletes the
// oldest ones when the total exceeds the configured RunHistoryLimit.
func (r *AgentRunReconciler) pruneOldRuns(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) error {
	instanceRef := agentRun.Spec.InstanceRef
	if instanceRef == "" {
		return nil
	}

	var allRuns sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &allRuns,
		client.InNamespace(agentRun.Namespace),
		client.MatchingLabels{"sympozium.ai/instance": instanceRef},
	); err != nil {
		return fmt.Errorf("listing runs for instance %s: %w", instanceRef, err)
	}

	// Collect only completed (Succeeded/Failed) runs.
	var completed []sympoziumv1alpha1.AgentRun
	for _, run := range allRuns.Items {
		if run.Status.Phase == "Succeeded" || run.Status.Phase == "Failed" {
			completed = append(completed, run)
		}
	}

	limit := r.runHistoryLimit()
	if len(completed) <= limit {
		return nil
	}

	// Sort oldest first by creation timestamp.
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].CreationTimestamp.Before(&completed[j].CreationTimestamp)
	})

	pruneCount := len(completed) - limit
	log.Info("Pruning old AgentRuns", "instance", instanceRef, "total", len(completed), "limit", limit, "pruning", pruneCount)

	for i := 0; i < pruneCount; i++ {
		run := &completed[i]
		log.Info("Deleting old AgentRun", "name", run.Name, "created", run.CreationTimestamp.Time)
		if err := r.Delete(ctx, run); err != nil {
			if !errors.IsNotFound(err) {
				return fmt.Errorf("deleting run %s: %w", run.Name, err)
			}
		}
	}

	return nil
}

// reconcileDelete handles AgentRun deletion.
func (r *AgentRunReconciler) reconcileDelete(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (ctrl.Result, error) {
	log.Info("Reconciling AgentRun deletion")

	// Clean up cluster-scoped RBAC resources created for skill sidecars.
	r.cleanupSkillRBAC(ctx, log, agentRun)

	// Delete the Job if it exists
	if agentRun.Status.JobName != "" {
		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      agentRun.Status.JobName,
				Namespace: agentRun.Namespace,
			},
		}
		if err := r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground)); err != nil && !errors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}

	patch := client.MergeFrom(agentRun.DeepCopy())
	controllerutil.RemoveFinalizer(agentRun, agentRunFinalizer)
	return ctrl.Result{}, r.Patch(ctx, agentRun, patch)
}

// validatePolicy checks the AgentRun against the applicable SympoziumPolicy.
func (r *AgentRunReconciler) validatePolicy(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) error {
	// Look up the SympoziumInstance to find the policy
	instance := &sympoziumv1alpha1.SympoziumInstance{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Spec.InstanceRef,
	}, instance); err != nil {
		return fmt.Errorf("instance %q not found: %w", agentRun.Spec.InstanceRef, err)
	}

	if instance.Spec.PolicyRef == "" {
		return nil // No policy, allow
	}

	policy := &sympoziumv1alpha1.SympoziumPolicy{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      instance.Spec.PolicyRef,
	}, policy); err != nil {
		return fmt.Errorf("policy %q not found: %w", instance.Spec.PolicyRef, err)
	}

	// Validate sub-agent depth
	if agentRun.Spec.Parent != nil && policy.Spec.SubagentPolicy != nil {
		if agentRun.Spec.Parent.SpawnDepth > policy.Spec.SubagentPolicy.MaxDepth {
			return fmt.Errorf("sub-agent depth %d exceeds max %d",
				agentRun.Spec.Parent.SpawnDepth, policy.Spec.SubagentPolicy.MaxDepth)
		}
	}

	// Validate concurrency
	if policy.Spec.SubagentPolicy != nil {
		activeRuns := &sympoziumv1alpha1.AgentRunList{}
		if err := r.List(ctx, activeRuns,
			client.InNamespace(agentRun.Namespace),
			client.MatchingLabels{"sympozium.ai/instance": agentRun.Spec.InstanceRef},
		); err == nil {
			running := 0
			for _, run := range activeRuns.Items {
				if run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseRunning {
					running++
				}
			}
			if running >= policy.Spec.SubagentPolicy.MaxConcurrent {
				return fmt.Errorf("concurrency limit reached: %d/%d", running, policy.Spec.SubagentPolicy.MaxConcurrent)
			}
		}
	}

	return nil
}

// ensureAgentServiceAccount creates the sympozium-agent ServiceAccount in the
// given namespace if it does not already exist. This is needed because agent
// Jobs reference this SA and run in the user's namespace, not sympozium-system.
func (r *AgentRunReconciler) ensureAgentServiceAccount(ctx context.Context, namespace string) error {
	sa := &corev1.ServiceAccount{}
	err := r.Get(ctx, client.ObjectKey{Name: "sympozium-agent", Namespace: namespace}, sa)
	if err == nil {
		return nil // already exists
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("checking for agent service account: %w", err)
	}
	sa = &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sympozium-agent",
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "sympozium",
			},
		},
	}
	if err := r.Create(ctx, sa); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("creating agent service account: %w", err)
	}
	return nil
}

// buildJob constructs the Kubernetes Job for an AgentRun.
func (r *AgentRunReconciler) buildJob(agentRun *sympoziumv1alpha1.AgentRun, memoryEnabled bool, sidecars []resolvedSidecar) *batchv1.Job {
	labels := map[string]string{
		"sympozium.ai/agent-run": agentRun.Name,
		"sympozium.ai/instance":  agentRun.Spec.InstanceRef,
		"sympozium.ai/component": "agent-run",
	}

	ttl := int32(300)
	deadline := int64(600)
	if agentRun.Spec.Timeout != nil {
		deadline = int64(agentRun.Spec.Timeout.Duration.Seconds()) + 60
	}
	backoffLimit := int32(0)

	// Build containers
	containers := r.buildContainers(agentRun, memoryEnabled, sidecars)
	volumes := r.buildVolumes(agentRun, memoryEnabled)

	runAsNonRoot := true
	runAsUser := int64(1000)
	fsGroup := int64(1000)

	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentRun.Name,
			Namespace: agentRun.Namespace,
			Labels:    labels,
		},
		Spec: batchv1.JobSpec{
			TTLSecondsAfterFinished: &ttl,
			ActiveDeadlineSeconds:   &deadline,
			BackoffLimit:            &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: "sympozium-agent",
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: &runAsNonRoot,
						RunAsUser:    &runAsUser,
						FSGroup:      &fsGroup,
					},
					Containers: containers,
					Volumes:    volumes,
				},
			},
		},
	}
}

// buildContainers constructs the container list for an agent pod.
func (r *AgentRunReconciler) buildContainers(agentRun *sympoziumv1alpha1.AgentRun, memoryEnabled bool, sidecars []resolvedSidecar) []corev1.Container {
	readOnly := true
	noPrivEsc := false

	containers := []corev1.Container{
		// Main agent container
		{
			Name:            "agent",
			Image:           r.imageRef("agent-runner"),
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				ReadOnlyRootFilesystem:   &readOnly,
				AllowPrivilegeEscalation: &noPrivEsc,
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			},
			Env: []corev1.EnvVar{
				{Name: "AGENT_RUN_ID", Value: agentRun.Name},
				{Name: "AGENT_ID", Value: agentRun.Spec.AgentID},
				{Name: "SESSION_KEY", Value: agentRun.Spec.SessionKey},
				{Name: "TASK", Value: agentRun.Spec.Task},
				{Name: "SYSTEM_PROMPT", Value: agentRun.Spec.SystemPrompt},
				{Name: "MODEL_PROVIDER", Value: agentRun.Spec.Model.Provider},
				{Name: "MODEL_NAME", Value: agentRun.Spec.Model.Model},
				{Name: "MODEL_BASE_URL", Value: agentRun.Spec.Model.BaseURL},
				{Name: "THINKING_MODE", Value: agentRun.Spec.Model.Thinking},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "workspace", MountPath: "/workspace"},
				{Name: "skills", MountPath: "/skills", ReadOnly: true},
				{Name: "ipc", MountPath: "/ipc"},
				{Name: "tmp", MountPath: "/tmp"},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("250m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1"),
					corev1.ResourceMemory: resource.MustParse("1Gi"),
				},
			},
		},
		// IPC bridge sidecar
		{
			Name:            "ipc-bridge",
			Image:           r.imageRef("ipc-bridge"),
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env: []corev1.EnvVar{
				{Name: "AGENT_RUN_ID", Value: agentRun.Name},
				{Name: "INSTANCE_NAME", Value: agentRun.Spec.InstanceRef},
				{Name: "EVENT_BUS_URL", Value: "nats://nats.sympozium-system.svc:4222"},
			},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "ipc", MountPath: "/ipc"},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("50m"),
					corev1.ResourceMemory: resource.MustParse("64Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("128Mi"),
				},
			},
		},
	}

	// Inject auth secret if provided.
	if agentRun.Spec.Model.AuthSecretRef != "" {
		containers[0].EnvFrom = []corev1.EnvFromSource{
			{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: agentRun.Spec.Model.AuthSecretRef,
					},
				},
			},
		}
	}

	// Add memory volume mount if memory is enabled.
	if memoryEnabled {
		containers[0].VolumeMounts = append(containers[0].VolumeMounts,
			corev1.VolumeMount{Name: "memory", MountPath: "/memory", ReadOnly: true},
		)
		containers[0].Env = append(containers[0].Env,
			corev1.EnvVar{Name: "MEMORY_ENABLED", Value: "true"},
		)
	}

	// Add sandbox sidecar if enabled
	if agentRun.Spec.Sandbox != nil && agentRun.Spec.Sandbox.Enabled {
		sandboxImage := r.imageRef("sandbox")
		if agentRun.Spec.Sandbox.Image != "" {
			sandboxImage = agentRun.Spec.Sandbox.Image
		}

		containers = append(containers, corev1.Container{
			Name:            "sandbox",
			Image:           sandboxImage,
			ImagePullPolicy: corev1.PullIfNotPresent,
			SecurityContext: &corev1.SecurityContext{
				ReadOnlyRootFilesystem: &readOnly,
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"},
				},
			},
			Command: []string{"sleep", "infinity"},
			VolumeMounts: []corev1.VolumeMount{
				{Name: "workspace", MountPath: "/workspace"},
				{Name: "tmp", MountPath: "/tmp"},
			},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("256Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("500m"),
					corev1.ResourceMemory: resource.MustParse("512Mi"),
				},
			},
		})
	}

	// Always enable tools — the IPC bridge is always present so
	// send_channel_message, read_file, and list_directory work without
	// sidecars.  execute_command gracefully times out if no skill sidecar
	// is running to handle exec requests.
	containers[0].Env = append(containers[0].Env,
		corev1.EnvVar{Name: "TOOLS_ENABLED", Value: "true"},
	)

	// Pass channel context so the agent knows how to reply when the run
	// was triggered by a channel message (WhatsApp, Telegram, etc.).
	if ch := agentRun.Annotations["sympozium.ai/reply-channel"]; ch != "" {
		containers[0].Env = append(containers[0].Env,
			corev1.EnvVar{Name: "SOURCE_CHANNEL", Value: ch},
		)
	}
	if cid := agentRun.Annotations["sympozium.ai/reply-chat-id"]; cid != "" {
		containers[0].Env = append(containers[0].Env,
			corev1.EnvVar{Name: "SOURCE_CHAT_ID", Value: cid},
		)
	}

	// Inject skill sidecar containers.
	for _, sc := range sidecars {
		cmd := sc.sidecar.Command

		var envVars []corev1.EnvVar
		for _, e := range sc.sidecar.Env {
			envVars = append(envVars, corev1.EnvVar{Name: e.Name, Value: e.Value})
		}

		mounts := []corev1.VolumeMount{
			{Name: "ipc", MountPath: "/ipc"},
			{Name: "tmp", MountPath: "/tmp"},
		}
		if sc.sidecar.MountWorkspace {
			mounts = append(mounts, corev1.VolumeMount{Name: "workspace", MountPath: "/workspace"})
		}

		cpuReq := "100m"
		memReq := "128Mi"
		if sc.sidecar.Resources != nil {
			if sc.sidecar.Resources.CPU != "" {
				cpuReq = sc.sidecar.Resources.CPU
			}
			if sc.sidecar.Resources.Memory != "" {
				memReq = sc.sidecar.Resources.Memory
			}
		}

		container := corev1.Container{
			Name:            fmt.Sprintf("skill-%s", sc.skillPackName),
			Image:           sc.sidecar.Image,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Env:             envVars,
			VolumeMounts:    mounts,
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(cpuReq),
					corev1.ResourceMemory: resource.MustParse(memReq),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(cpuReq),
					corev1.ResourceMemory: resource.MustParse(memReq),
				},
			},
		}
		// Only set Command if the SkillPack specifies one; otherwise
		// let the container image's default CMD (tool-executor) run.
		if len(cmd) > 0 {
			container.Command = cmd
		}
		containers = append(containers, container)
	}

	return containers
}

// buildVolumes constructs the volume list for an agent pod.
func (r *AgentRunReconciler) buildVolumes(agentRun *sympoziumv1alpha1.AgentRun, memoryEnabled bool) []corev1.Volume {
	workspaceSizeLimit := resource.MustParse("1Gi")
	ipcSizeLimit := resource.MustParse("64Mi")
	tmpSizeLimit := resource.MustParse("256Mi")
	memoryMedium := corev1.StorageMediumMemory

	volumes := []corev1.Volume{
		{
			Name: "workspace",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					SizeLimit: &workspaceSizeLimit,
				},
			},
		},
		{
			Name: "ipc",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    memoryMedium,
					SizeLimit: &ipcSizeLimit,
				},
			},
		},
		{
			Name: "tmp",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					SizeLimit: &tmpSizeLimit,
				},
			},
		},
	}

	// Build skills projected volume from skill references
	var sources []corev1.VolumeProjection
	for _, skill := range agentRun.Spec.Skills {
		if skill.SkillPackRef != "" {
			sources = append(sources, corev1.VolumeProjection{
				ConfigMap: &corev1.ConfigMapProjection{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: skill.SkillPackRef,
					},
				},
			})
		}
		if skill.ConfigMapRef != "" {
			sources = append(sources, corev1.VolumeProjection{
				ConfigMap: &corev1.ConfigMapProjection{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: skill.ConfigMapRef,
					},
				},
			})
		}
	}

	if len(sources) > 0 {
		volumes = append(volumes, corev1.Volume{
			Name: "skills",
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: sources,
				},
			},
		})
	} else {
		// Empty skills volume
		volumes = append(volumes, corev1.Volume{
			Name: "skills",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}

	// Add memory ConfigMap volume if memory is enabled.
	if memoryEnabled {
		cmName := fmt.Sprintf("%s-memory", agentRun.Spec.InstanceRef)
		volumes = append(volumes, corev1.Volume{
			Name: "memory",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: cmName,
					},
					Optional: boolPtr(true),
				},
			},
		})
	}

	return volumes
}

// boolPtr returns a pointer to a bool.
func boolPtr(b bool) *bool { return &b }

// createInputConfigMap creates a ConfigMap with the agent's task input.
func (r *AgentRunReconciler) createInputConfigMap(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-input", agentRun.Name),
			Namespace: agentRun.Namespace,
			Labels: map[string]string{
				"sympozium.ai/agent-run": agentRun.Name,
			},
		},
		Data: map[string]string{
			"task":          agentRun.Spec.Task,
			"system-prompt": agentRun.Spec.SystemPrompt,
			"agent-id":      agentRun.Spec.AgentID,
			"session-key":   agentRun.Spec.SessionKey,
		},
	}

	if err := controllerutil.SetControllerReference(agentRun, cm, r.Scheme); err != nil {
		return err
	}

	if err := r.Create(ctx, cm); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return err
	}
	return nil
}

// succeedRun marks an AgentRun as succeeded and stores the result.
func (r *AgentRunReconciler) succeedRun(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, result string, usage *sympoziumv1alpha1.TokenUsage) (ctrl.Result, error) {
	now := metav1.Now()
	agentRun.Status.Phase = sympoziumv1alpha1.AgentRunPhaseSucceeded
	agentRun.Status.CompletedAt = &now
	agentRun.Status.Result = result
	agentRun.Status.TokenUsage = usage
	return ctrl.Result{}, r.Status().Update(ctx, agentRun)
}

const (
	resultMarkerStart = "__SYMPOZIUM_RESULT__"
	resultMarkerEnd   = "__SYMPOZIUM_END__"
)

// extractResultFromPod reads the agent container logs and looks for the
// structured result marker written by agent-runner.
func (r *AgentRunReconciler) extractResultFromPod(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) (string, *sympoziumv1alpha1.TokenUsage) {
	if r.Clientset == nil || agentRun.Status.PodName == "" {
		return "", nil
	}

	tailLines := int64(20)
	opts := &corev1.PodLogOptions{
		Container: "agent",
		TailLines: &tailLines,
	}
	req := r.Clientset.CoreV1().Pods(agentRun.Namespace).GetLogs(agentRun.Status.PodName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		log.V(1).Info("could not read pod logs for result", "err", err)
		return "", nil
	}
	defer stream.Close()

	raw, err := io.ReadAll(stream)
	if err != nil {
		log.V(1).Info("error reading pod logs", "err", err)
		return "", nil
	}

	logs := string(raw)
	startIdx := strings.LastIndex(logs, resultMarkerStart)
	if startIdx < 0 {
		return "", nil
	}
	payload := logs[startIdx+len(resultMarkerStart):]
	endIdx := strings.Index(payload, resultMarkerEnd)
	if endIdx < 0 {
		return "", nil
	}
	jsonStr := strings.TrimSpace(payload[:endIdx])

	// Parse the full agent result including metrics.
	var parsed struct {
		Status   string `json:"status"`
		Response string `json:"response"`
		Metrics  struct {
			DurationMs   int64 `json:"durationMs"`
			InputTokens  int   `json:"inputTokens"`
			OutputTokens int   `json:"outputTokens"`
			ToolCalls    int   `json:"toolCalls"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		log.V(1).Info("could not parse result JSON", "err", err)
		return jsonStr, nil // Return raw JSON as fallback.
	}
	if parsed.Status == "error" {
		return "", nil
	}

	var usage *sympoziumv1alpha1.TokenUsage
	if parsed.Metrics.InputTokens > 0 || parsed.Metrics.OutputTokens > 0 {
		usage = &sympoziumv1alpha1.TokenUsage{
			InputTokens:  parsed.Metrics.InputTokens,
			OutputTokens: parsed.Metrics.OutputTokens,
			TotalTokens:  parsed.Metrics.InputTokens + parsed.Metrics.OutputTokens,
			ToolCalls:    parsed.Metrics.ToolCalls,
			DurationMs:   parsed.Metrics.DurationMs,
		}
		log.Info("extracted token usage",
			"inputTokens", usage.InputTokens,
			"outputTokens", usage.OutputTokens,
			"totalTokens", usage.TotalTokens,
			"toolCalls", usage.ToolCalls,
			"durationMs", usage.DurationMs)
	}

	return parsed.Response, usage
}

const (
	memoryMarkerStart = "__SYMPOZIUM_MEMORY__"
	memoryMarkerEnd   = "__SYMPOZIUM_MEMORY_END__"
)

// extractAndPersistMemory reads the agent container logs for a memory update
// marker and patches the instance's memory ConfigMap with the new content.
func (r *AgentRunReconciler) extractAndPersistMemory(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) {
	if r.Clientset == nil || agentRun.Status.PodName == "" {
		return
	}

	tailLines := int64(100)
	opts := &corev1.PodLogOptions{
		Container: "agent",
		TailLines: &tailLines,
	}
	req := r.Clientset.CoreV1().Pods(agentRun.Namespace).GetLogs(agentRun.Status.PodName, opts)
	stream, err := req.Stream(ctx)
	if err != nil {
		return
	}
	defer stream.Close()

	raw, err := io.ReadAll(stream)
	if err != nil {
		return
	}

	logs := string(raw)
	startIdx := strings.LastIndex(logs, memoryMarkerStart)
	if startIdx < 0 {
		return
	}
	payload := logs[startIdx+len(memoryMarkerStart):]
	endIdx := strings.Index(payload, memoryMarkerEnd)
	if endIdx < 0 {
		return
	}
	memoryContent := strings.TrimSpace(payload[:endIdx])
	if memoryContent == "" {
		return
	}

	// Patch the memory ConfigMap.
	cmName := fmt.Sprintf("%s-memory", agentRun.Spec.InstanceRef)
	var cm corev1.ConfigMap
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      cmName,
	}, &cm); err != nil {
		log.V(1).Info("memory ConfigMap not found, skipping memory update", "err", err)
		return
	}

	cm.Data["MEMORY.md"] = memoryContent
	if err := r.Update(ctx, &cm); err != nil {
		log.V(1).Info("failed to update memory ConfigMap", "err", err)
		return
	}
	log.Info("Updated memory ConfigMap", "configmap", cmName, "bytes", len(memoryContent))
}

// failRun marks an AgentRun as failed.
func (r *AgentRunReconciler) failRun(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, reason string) error {
	now := metav1.Now()
	agentRun.Status.Phase = sympoziumv1alpha1.AgentRunPhaseFailed
	agentRun.Status.CompletedAt = &now
	agentRun.Status.Error = reason
	return r.Status().Update(ctx, agentRun)
}

// --- Skill sidecar resolution and RBAC ---

// resolvedSidecar pairs a SkillPack name with its sidecar spec.
type resolvedSidecar struct {
	skillPackName string
	sidecar       sympoziumv1alpha1.SkillSidecar
}

// resolveSkillSidecars looks up SkillPack CRDs for the AgentRun's active
// skills and returns any that have a sidecar defined.
func (r *AgentRunReconciler) resolveSkillSidecars(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) []resolvedSidecar {
	var sidecars []resolvedSidecar
	for _, ref := range agentRun.Spec.Skills {
		if ref.SkillPackRef == "" {
			continue
		}
		// The SkillPackRef on the AgentRun may be the ConfigMap name produced by
		// the SkillPack controller (e.g. "skillpack-k8s-ops"). Try to resolve
		// the SkillPack CRD by stripping the "skillpack-" prefix first.
		spName := ref.SkillPackRef
		if strings.HasPrefix(spName, "skillpack-") {
			spName = strings.TrimPrefix(spName, "skillpack-")
		}

		sp := &sympoziumv1alpha1.SkillPack{}
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: agentRun.Namespace,
			Name:      spName,
		}, sp); err != nil {
			// SkillPack not in agent namespace — try sympozium-system (default
			// location for built-in skills installed by `sympozium install`).
			if err2 := r.Get(ctx, client.ObjectKey{
				Namespace: systemNamespace,
				Name:      spName,
			}, sp); err2 != nil {
				log.V(1).Info("SkillPack not found, skipping sidecar", "name", spName)
				continue
			}
		}

		if sp.Spec.Sidecar != nil && sp.Spec.Sidecar.Image != "" {
			sidecars = append(sidecars, resolvedSidecar{
				skillPackName: spName,
				sidecar:       *sp.Spec.Sidecar,
			})
		}
	}
	return sidecars
}

// mirrorSkillConfigMaps copies skill ConfigMaps from sympozium-system into the
// AgentRun's namespace so that projected volumes can reference them.
// ConfigMap volume projections are namespace-local in Kubernetes, so when
// SkillPacks live in sympozium-system their ConfigMaps must be mirrored.
func (r *AgentRunReconciler) mirrorSkillConfigMaps(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) error {
	if agentRun.Namespace == systemNamespace {
		return nil // no mirroring needed
	}
	for _, ref := range agentRun.Spec.Skills {
		cmName := ref.SkillPackRef
		if cmName == "" {
			cmName = ref.ConfigMapRef
		}
		if cmName == "" {
			continue
		}

		// Look for the ConfigMap in sympozium-system.
		source := &corev1.ConfigMap{}
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: systemNamespace,
			Name:      cmName,
		}, source); err != nil {
			log.V(1).Info("Skill ConfigMap not found in sympozium-system, skipping mirror", "configmap", cmName)
			continue
		}

		// Check if ConfigMap already exists in the agent namespace.
		existing := &corev1.ConfigMap{}
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: agentRun.Namespace,
			Name:      cmName,
		}, existing); err == nil {
			// Already present — update data to ensure we have the latest skills.
			existing.Data = source.Data
			if err := r.Update(ctx, existing); err != nil {
				log.Error(err, "Failed to update mirrored skill ConfigMap", "configmap", cmName)
			} else {
				log.V(1).Info("Updated mirrored skill ConfigMap with latest data", "configmap", cmName)
			}
			continue
		}

		// Create a mirror copy in the agent namespace, owned by the AgentRun
		// so it is garbage-collected when the run is deleted.
		mirror := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      cmName,
				Namespace: agentRun.Namespace,
				Labels: map[string]string{
					"sympozium.ai/component":  "skillpack-mirror",
					"sympozium.ai/agent-run":  agentRun.Name,
					"sympozium.ai/managed-by": "sympozium",
				},
			},
			Data: source.Data,
		}
		if err := controllerutil.SetControllerReference(agentRun, mirror, r.Scheme); err != nil {
			log.Error(err, "Failed to set owner reference on skill ConfigMap mirror", "configmap", cmName)
			continue
		}
		if err := r.Create(ctx, mirror); err != nil {
			if !errors.IsAlreadyExists(err) {
				log.Error(err, "Failed to mirror skill ConfigMap", "configmap", cmName)
			}
		} else {
			log.Info("Mirrored skill ConfigMap into agent namespace", "configmap", cmName, "from", systemNamespace)
		}
	}
	return nil
}

// ensureSkillRBAC creates Role/ClusterRole and bindings for skill sidecars.
// Resources are labelled with the AgentRun name for cleanup.
func (r *AgentRunReconciler) ensureSkillRBAC(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun, sidecars []resolvedSidecar) error {
	for _, sc := range sidecars {
		// Namespace-scoped Role + RoleBinding
		if len(sc.sidecar.RBAC) > 0 {
			roleName := fmt.Sprintf("sympozium-skill-%s-%s", sc.skillPackName, agentRun.Name)
			var rules []rbacv1.PolicyRule
			for _, rule := range sc.sidecar.RBAC {
				rules = append(rules, rbacv1.PolicyRule{
					APIGroups: rule.APIGroups,
					Resources: rule.Resources,
					Verbs:     rule.Verbs,
				})
			}

			role := &rbacv1.Role{
				ObjectMeta: metav1.ObjectMeta{
					Name:      roleName,
					Namespace: agentRun.Namespace,
					Labels: map[string]string{
						"sympozium.ai/agent-run":  agentRun.Name,
						"sympozium.ai/skill":      sc.skillPackName,
						"sympozium.ai/managed-by": "sympozium",
					},
				},
				Rules: rules,
			}
			if err := controllerutil.SetControllerReference(agentRun, role, r.Scheme); err != nil {
				log.Error(err, "Failed to set owner on Role")
			}
			if err := r.Create(ctx, role); err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("creating skill Role %s: %w", roleName, err)
			}

			rb := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      roleName,
					Namespace: agentRun.Namespace,
					Labels: map[string]string{
						"sympozium.ai/agent-run":  agentRun.Name,
						"sympozium.ai/skill":      sc.skillPackName,
						"sympozium.ai/managed-by": "sympozium",
					},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "Role",
					Name:     roleName,
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      "sympozium-agent",
						Namespace: agentRun.Namespace,
					},
				},
			}
			if err := controllerutil.SetControllerReference(agentRun, rb, r.Scheme); err != nil {
				log.Error(err, "Failed to set owner on RoleBinding")
			}
			if err := r.Create(ctx, rb); err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("creating skill RoleBinding %s: %w", roleName, err)
			}
			log.Info("Created skill RBAC (namespaced)", "role", roleName, "skill", sc.skillPackName)
		}

		// Cluster-scoped ClusterRole + ClusterRoleBinding
		if len(sc.sidecar.ClusterRBAC) > 0 {
			crName := fmt.Sprintf("sympozium-skill-%s-%s", sc.skillPackName, agentRun.Name)
			var rules []rbacv1.PolicyRule
			for _, rule := range sc.sidecar.ClusterRBAC {
				rules = append(rules, rbacv1.PolicyRule{
					APIGroups: rule.APIGroups,
					Resources: rule.Resources,
					Verbs:     rule.Verbs,
				})
			}

			cr := &rbacv1.ClusterRole{
				ObjectMeta: metav1.ObjectMeta{
					Name: crName,
					Labels: map[string]string{
						"sympozium.ai/agent-run":  agentRun.Name,
						"sympozium.ai/skill":      sc.skillPackName,
						"sympozium.ai/managed-by": "sympozium",
					},
				},
				Rules: rules,
			}
			if err := r.Create(ctx, cr); err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("creating skill ClusterRole %s: %w", crName, err)
			}

			crb := &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: crName,
					Labels: map[string]string{
						"sympozium.ai/agent-run":  agentRun.Name,
						"sympozium.ai/skill":      sc.skillPackName,
						"sympozium.ai/managed-by": "sympozium",
					},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     crName,
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      "sympozium-agent",
						Namespace: agentRun.Namespace,
					},
				},
			}
			if err := r.Create(ctx, crb); err != nil && !errors.IsAlreadyExists(err) {
				return fmt.Errorf("creating skill ClusterRoleBinding %s: %w", crName, err)
			}
			log.Info("Created skill RBAC (cluster)", "clusterRole", crName, "skill", sc.skillPackName)
		}
	}
	return nil
}

// cleanupSkillRBAC removes cluster-scoped RBAC resources created for an AgentRun.
// Namespace-scoped resources (Role, RoleBinding) are cleaned up automatically
// via owner references and garbage collection.
func (r *AgentRunReconciler) cleanupSkillRBAC(ctx context.Context, log logr.Logger, agentRun *sympoziumv1alpha1.AgentRun) {
	// List ClusterRoles owned by this run
	crList := &rbacv1.ClusterRoleList{}
	if err := r.List(ctx, crList, client.MatchingLabels{
		"sympozium.ai/agent-run":  agentRun.Name,
		"sympozium.ai/managed-by": "sympozium",
	}); err == nil {
		for i := range crList.Items {
			if err := r.Delete(ctx, &crList.Items[i]); err != nil && !errors.IsNotFound(err) {
				log.V(1).Info("Failed to delete ClusterRole", "name", crList.Items[i].Name, "err", err)
			}
		}
	}

	// List ClusterRoleBindings owned by this run
	crbList := &rbacv1.ClusterRoleBindingList{}
	if err := r.List(ctx, crbList, client.MatchingLabels{
		"sympozium.ai/agent-run":  agentRun.Name,
		"sympozium.ai/managed-by": "sympozium",
	}); err == nil {
		for i := range crbList.Items {
			if err := r.Delete(ctx, &crbList.Items[i]); err != nil && !errors.IsNotFound(err) {
				log.V(1).Info("Failed to delete ClusterRoleBinding", "name", crbList.Items[i].Name, "err", err)
			}
		}
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.AgentRun{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
