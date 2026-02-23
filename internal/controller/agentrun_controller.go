package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	k8sclawv1alpha1 "github.com/k8sclaw/k8sclaw/api/v1alpha1"
	"github.com/k8sclaw/k8sclaw/internal/orchestrator"
)

const agentRunFinalizer = "k8sclaw.io/agentrun-finalizer"

// AgentRunReconciler reconciles AgentRun objects.
// It watches AgentRun CRDs and reconciles them into Kubernetes Jobs/Pods.
type AgentRunReconciler struct {
	client.Client
	Scheme     *runtime.Scheme
	Log        logr.Logger
	PodBuilder *orchestrator.PodBuilder
}

// +kubebuilder:rbac:groups=k8sclaw.io,resources=agentruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=k8sclaw.io,resources=agentruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=k8sclaw.io,resources=agentruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create

// Reconcile handles AgentRun create/update/delete events.
func (r *AgentRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("agentrun", req.NamespacedName)

	// Fetch the AgentRun
	agentRun := &k8sclawv1alpha1.AgentRun{}
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

	// Add finalizer
	if !controllerutil.ContainsFinalizer(agentRun, agentRunFinalizer) {
		controllerutil.AddFinalizer(agentRun, agentRunFinalizer)
		if err := r.Update(ctx, agentRun); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Reconcile based on current phase
	switch agentRun.Status.Phase {
	case "", k8sclawv1alpha1.AgentRunPhasePending:
		return r.reconcilePending(ctx, log, agentRun)
	case k8sclawv1alpha1.AgentRunPhaseRunning:
		return r.reconcileRunning(ctx, log, agentRun)
	case k8sclawv1alpha1.AgentRunPhaseSucceeded, k8sclawv1alpha1.AgentRunPhaseFailed:
		return r.reconcileCompleted(ctx, log, agentRun)
	default:
		log.Info("Unknown phase", "phase", agentRun.Status.Phase)
		return ctrl.Result{}, nil
	}
}

// reconcilePending handles an AgentRun that needs a Job created.
func (r *AgentRunReconciler) reconcilePending(ctx context.Context, log logr.Logger, agentRun *k8sclawv1alpha1.AgentRun) (ctrl.Result, error) {
	log.Info("Reconciling pending AgentRun")

	// Validate against policy
	if err := r.validatePolicy(ctx, agentRun); err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("policy validation failed: %v", err))
	}

	// Ensure the k8sclaw-agent ServiceAccount exists in the target namespace.
	if err := r.ensureAgentServiceAccount(ctx, agentRun.Namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring agent service account: %w", err)
	}

	// Create the input ConfigMap with the task
	if err := r.createInputConfigMap(ctx, agentRun); err != nil {
		return ctrl.Result{}, fmt.Errorf("creating input ConfigMap: %w", err)
	}

	// Build and create the Job
	job := r.buildJob(agentRun)
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
	agentRun.Status.Phase = k8sclawv1alpha1.AgentRunPhaseRunning
	agentRun.Status.JobName = job.Name
	agentRun.Status.StartedAt = &now
	if err := r.Status().Update(ctx, agentRun); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileRunning checks on a running Job and updates status.
func (r *AgentRunReconciler) reconcileRunning(ctx context.Context, log logr.Logger, agentRun *k8sclawv1alpha1.AgentRun) (ctrl.Result, error) {
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
			client.MatchingLabels{"k8sclaw.io/agent-run": agentRun.Name},
		); err == nil && len(podList.Items) > 0 {
			agentRun.Status.PodName = podList.Items[0].Name
			_ = r.Status().Update(ctx, agentRun)
		}
	}

	// Check Job completion
	if job.Status.Succeeded > 0 {
		return r.succeedRun(ctx, agentRun)
	}
	if job.Status.Failed > 0 {
		return ctrl.Result{}, r.failRun(ctx, agentRun, "Job failed")
	}

	// Check timeout
	if agentRun.Spec.Timeout != nil && agentRun.Status.StartedAt != nil {
		elapsed := time.Since(agentRun.Status.StartedAt.Time)
		if elapsed > agentRun.Spec.Timeout.Duration {
			log.Info("AgentRun timed out", "elapsed", elapsed)
			// Delete the Job to kill the pod
			_ = r.Delete(ctx, job, client.PropagationPolicy(metav1.DeletePropagationForeground))
			return ctrl.Result{}, r.failRun(ctx, agentRun, "timeout")
		}
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileCompleted handles cleanup of completed AgentRuns.
func (r *AgentRunReconciler) reconcileCompleted(ctx context.Context, log logr.Logger, agentRun *k8sclawv1alpha1.AgentRun) (ctrl.Result, error) {
	if agentRun.Spec.Cleanup == "delete" {
		log.Info("Cleaning up completed AgentRun")
		controllerutil.RemoveFinalizer(agentRun, agentRunFinalizer)
		if err := r.Update(ctx, agentRun); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// reconcileDelete handles AgentRun deletion.
func (r *AgentRunReconciler) reconcileDelete(ctx context.Context, log logr.Logger, agentRun *k8sclawv1alpha1.AgentRun) (ctrl.Result, error) {
	log.Info("Reconciling AgentRun deletion")

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

	controllerutil.RemoveFinalizer(agentRun, agentRunFinalizer)
	return ctrl.Result{}, r.Update(ctx, agentRun)
}

// validatePolicy checks the AgentRun against the applicable ClawPolicy.
func (r *AgentRunReconciler) validatePolicy(ctx context.Context, agentRun *k8sclawv1alpha1.AgentRun) error {
	// Look up the ClawInstance to find the policy
	instance := &k8sclawv1alpha1.ClawInstance{}
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Spec.InstanceRef,
	}, instance); err != nil {
		return fmt.Errorf("instance %q not found: %w", agentRun.Spec.InstanceRef, err)
	}

	if instance.Spec.PolicyRef == "" {
		return nil // No policy, allow
	}

	policy := &k8sclawv1alpha1.ClawPolicy{}
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
		activeRuns := &k8sclawv1alpha1.AgentRunList{}
		if err := r.List(ctx, activeRuns,
			client.InNamespace(agentRun.Namespace),
			client.MatchingLabels{"k8sclaw.io/instance": agentRun.Spec.InstanceRef},
		); err == nil {
			running := 0
			for _, run := range activeRuns.Items {
				if run.Status.Phase == k8sclawv1alpha1.AgentRunPhaseRunning {
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

// ensureAgentServiceAccount creates the k8sclaw-agent ServiceAccount in the
// given namespace if it does not already exist. This is needed because agent
// Jobs reference this SA and run in the user's namespace, not k8sclaw-system.
func (r *AgentRunReconciler) ensureAgentServiceAccount(ctx context.Context, namespace string) error {
	sa := &corev1.ServiceAccount{}
	err := r.Get(ctx, client.ObjectKey{Name: "k8sclaw-agent", Namespace: namespace}, sa)
	if err == nil {
		return nil // already exists
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("checking for agent service account: %w", err)
	}
	sa = &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "k8sclaw-agent",
			Namespace: namespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "k8sclaw",
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
func (r *AgentRunReconciler) buildJob(agentRun *k8sclawv1alpha1.AgentRun) *batchv1.Job {
	labels := map[string]string{
		"k8sclaw.io/agent-run": agentRun.Name,
		"k8sclaw.io/instance":  agentRun.Spec.InstanceRef,
		"k8sclaw.io/component": "agent-run",
	}

	ttl := int32(300)
	deadline := int64(600)
	if agentRun.Spec.Timeout != nil {
		deadline = int64(agentRun.Spec.Timeout.Duration.Seconds()) + 60
	}
	backoffLimit := int32(0)

	// Build containers
	containers := r.buildContainers(agentRun)
	volumes := r.buildVolumes(agentRun)

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
					ServiceAccountName: "k8sclaw-agent",
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
func (r *AgentRunReconciler) buildContainers(agentRun *k8sclawv1alpha1.AgentRun) []corev1.Container {
	readOnly := true
	noPrivEsc := false

	containers := []corev1.Container{
		// Main agent container
		{
			Name:            "agent",
			Image:           "ghcr.io/alexsjones/k8sclaw/agent-runner:latest",
			ImagePullPolicy: corev1.PullAlways,
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
				{Name: "MODEL_PROVIDER", Value: agentRun.Spec.Model.Provider},
				{Name: "MODEL_NAME", Value: agentRun.Spec.Model.Model},
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
			Image:           "ghcr.io/alexsjones/k8sclaw/ipc-bridge:latest",
			ImagePullPolicy: corev1.PullAlways,
			Env: []corev1.EnvVar{
				{Name: "AGENT_RUN_ID", Value: agentRun.Name},
				{Name: "INSTANCE_NAME", Value: agentRun.Spec.InstanceRef},
				{Name: "EVENT_BUS_URL", Value: "nats://nats.k8sclaw-system.svc:4222"},
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

	// Add sandbox sidecar if enabled
	if agentRun.Spec.Sandbox != nil && agentRun.Spec.Sandbox.Enabled {
		sandboxImage := "ghcr.io/alexsjones/k8sclaw/sandbox:latest"
		if agentRun.Spec.Sandbox.Image != "" {
			sandboxImage = agentRun.Spec.Sandbox.Image
		}

		containers = append(containers, corev1.Container{
			Name:            "sandbox",
			Image:           sandboxImage,
			ImagePullPolicy: corev1.PullAlways,
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

	return containers
}

// buildVolumes constructs the volume list for an agent pod.
func (r *AgentRunReconciler) buildVolumes(agentRun *k8sclawv1alpha1.AgentRun) []corev1.Volume {
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

	return volumes
}

// createInputConfigMap creates a ConfigMap with the agent's task input.
func (r *AgentRunReconciler) createInputConfigMap(ctx context.Context, agentRun *k8sclawv1alpha1.AgentRun) error {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-input", agentRun.Name),
			Namespace: agentRun.Namespace,
			Labels: map[string]string{
				"k8sclaw.io/agent-run": agentRun.Name,
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

// succeedRun marks an AgentRun as succeeded.
func (r *AgentRunReconciler) succeedRun(ctx context.Context, agentRun *k8sclawv1alpha1.AgentRun) (ctrl.Result, error) {
	now := metav1.Now()
	agentRun.Status.Phase = k8sclawv1alpha1.AgentRunPhaseSucceeded
	agentRun.Status.CompletedAt = &now
	return ctrl.Result{}, r.Status().Update(ctx, agentRun)
}

// failRun marks an AgentRun as failed.
func (r *AgentRunReconciler) failRun(ctx context.Context, agentRun *k8sclawv1alpha1.AgentRun, reason string) error {
	now := metav1.Now()
	agentRun.Status.Phase = k8sclawv1alpha1.AgentRunPhaseFailed
	agentRun.Status.CompletedAt = &now
	agentRun.Status.Error = reason
	return r.Status().Update(ctx, agentRun)
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&k8sclawv1alpha1.AgentRun{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
