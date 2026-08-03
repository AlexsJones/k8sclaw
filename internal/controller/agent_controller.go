// Package controller contains the reconciliation logic for Sympozium CRDs.
package controller

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

const sympoziumInstanceFinalizer = "sympozium.ai/finalizer"

// channelServiceAccountName is the ServiceAccount used by all channel
// Deployments. Shared across namespaces and intentionally unowned — it
// outlives any single Agent so that concurrent reconciles don't fight
// over ownership.
const channelServiceAccountName = "sympozium-channel"

// AgentReconciler reconciles a Agent object.
type AgentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	ImageTag string // release tag for Sympozium images
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=agents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=agents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=agents/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets;configmaps;services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles Agent reconciliation.
func (r *AgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("sympoziuminstance", req.NamespacedName)

	var instance sympoziumv1alpha1.Agent
	if err := r.Get(ctx, req.NamespacedName, &instance); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !instance.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&instance, sympoziumInstanceFinalizer) {
			log.Info("Cleaning up instance resources")
			if err := r.cleanupChannelDeployments(ctx, &instance); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.cleanupMemoryConfigMap(ctx, &instance); err != nil {
				log.Error(err, "failed to cleanup memory ConfigMap")
			}
			if err := r.cleanupMemoryDeployment(ctx, &instance); err != nil {
				log.Error(err, "failed to cleanup memory deployment")
			}
			patch := client.MergeFrom(instance.DeepCopy())
			controllerutil.RemoveFinalizer(&instance, sympoziumInstanceFinalizer)
			if err := r.Patch(ctx, &instance, patch); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if missing
	if !controllerutil.ContainsFinalizer(&instance, sympoziumInstanceFinalizer) {
		patch := client.MergeFrom(instance.DeepCopy())
		controllerutil.AddFinalizer(&instance, sympoziumInstanceFinalizer)
		if err := r.Patch(ctx, &instance, patch); err != nil {
			return ctrl.Result{}, err
		}
		// Re-fetch so subsequent operations use the latest resourceVersion.
		if err := r.Get(ctx, req.NamespacedName, &instance); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Capture status baseline before any status mutations so the merge patch
	// includes every field we touch (channels, phase, active-agent count).
	statusBase := instance.DeepCopy()

	// Reconcile channel deployments
	if err := r.reconcileChannels(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile channels")
		instance.Status.Phase = "Error"
		_ = r.Status().Patch(ctx, &instance, client.MergeFrom(statusBase))
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Reconcile memory ConfigMap (legacy) and memory PVC (SkillPack-based).
	if err := r.reconcileMemoryConfigMap(ctx, log, &instance); err != nil {
		log.Error(err, "failed to reconcile memory ConfigMap")
	}
	if err := r.reconcileMemoryPVC(ctx, log, &instance); err != nil {
		log.Error(err, "failed to reconcile memory PVC")
	}
	if err := r.reconcileMemoryDeployment(ctx, log, &instance); err != nil {
		log.Error(err, "failed to reconcile memory deployment")
	}

	// Reconcile web endpoint
	if err := r.reconcileWebEndpoint(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile web endpoint")
	}

	// Count active agent pods
	activeCount, hasServing, err := r.countActiveAgentPods(ctx, &instance)
	if err != nil {
		log.Error(err, "failed to count agent pods")
	}

	// Update status
	if hasServing {
		instance.Status.Phase = "Serving"
	} else {
		instance.Status.Phase = "Running"
	}
	instance.Status.ActiveAgentPods = activeCount
	if err := r.Status().Patch(ctx, &instance, client.MergeFrom(statusBase)); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// reconcileChannels ensures a Deployment exists for each configured channel.
func (r *AgentReconciler) reconcileChannels(ctx context.Context, instance *sympoziumv1alpha1.Agent) error {
	channelStatuses := make([]sympoziumv1alpha1.ChannelStatus, 0, len(instance.Spec.Channels))

	if len(instance.Spec.Channels) > 0 {
		if err := r.ensureChannelServiceAccount(ctx, instance.Namespace); err != nil {
			return err
		}
	}

	for _, ch := range instance.Spec.Channels {
		deployName := fmt.Sprintf("%s-channel-%s", instance.Name, ch.Type)

		// Require the referenced Secret up-front, unless a CSI volume will
		// materialize it on first mount (e.g. Vault Secrets Store CSI).
		if ch.ConfigRef.Secret != "" && !channelMountsCSI(ch) {
			var secret corev1.Secret
			if err := r.Get(ctx, types.NamespacedName{
				Name:      ch.ConfigRef.Secret,
				Namespace: instance.Namespace,
			}, &secret); err != nil {
				if errors.IsNotFound(err) {
					channelStatuses = append(channelStatuses, sympoziumv1alpha1.ChannelStatus{
						Type:    ch.Type,
						Status:  "Error",
						Message: fmt.Sprintf("secret %q not found", ch.ConfigRef.Secret),
					})
					continue
				}
				return err
			}
		}

		// WhatsApp channels need a PVC for credential persistence (QR link survives restarts)
		if ch.Type == "whatsapp" {
			if err := r.ensureWhatsAppPVC(ctx, instance, deployName); err != nil {
				return err
			}
		}

		var deploy appsv1.Deployment
		err := r.Get(ctx, types.NamespacedName{
			Name:      deployName,
			Namespace: instance.Namespace,
		}, &deploy)

		if errors.IsNotFound(err) {
			// Create channel deployment
			deploy := r.buildChannelDeployment(instance, ch, deployName)
			if err := controllerutil.SetControllerReference(instance, deploy, r.Scheme); err != nil {
				return err
			}
			if err := r.Create(ctx, deploy); err != nil {
				return err
			}
			channelStatuses = append(channelStatuses, sympoziumv1alpha1.ChannelStatus{
				Type:   ch.Type,
				Status: "Pending",
			})
		} else if err != nil {
			return err
		} else {
			status := "Connected"
			if deploy.Status.ReadyReplicas == 0 {
				status = "Disconnected"
			}
			channelStatuses = append(channelStatuses, sympoziumv1alpha1.ChannelStatus{
				Type:   ch.Type,
				Status: status,
			})
		}
	}

	instance.Status.Channels = channelStatuses
	return nil
}

// buildChannelDeployment creates a Deployment spec for a channel pod.
func (r *AgentReconciler) buildChannelDeployment(
	instance *sympoziumv1alpha1.Agent,
	ch sympoziumv1alpha1.ChannelSpec,
	name string,
) *appsv1.Deployment {
	replicas := int32(1)
	tag := r.ImageTag
	if tag == "" {
		tag = "latest"
	}
	registry := os.Getenv("SYMPOZIUM_IMAGE_REGISTRY")
	if registry == "" {
		registry = "ghcr.io/sympozium-ai/sympozium"
	}
	image := fmt.Sprintf("%s/channel-%s:%s", registry, ch.Type, tag)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"sympozium.ai/component": "channel",
				"sympozium.ai/channel":   ch.Type,
				"sympozium.ai/instance":  instance.Name,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"sympozium.ai/component": "channel",
					"sympozium.ai/channel":   ch.Type,
					"sympozium.ai/instance":  instance.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"sympozium.ai/component": "channel",
						"sympozium.ai/channel":   ch.Type,
						"sympozium.ai/instance":  instance.Name,
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: channelServiceAccountName,
					Containers: []corev1.Container{
						{
							Name:            "channel",
							Image:           image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Env: []corev1.EnvVar{
								{Name: "INSTANCE_NAME", Value: instance.Name},
								{Name: "EVENT_BUS_URL", Value: "nats://nats.sympozium-system.svc:4222"},
								{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: resolveOTelEndpoint(instance)},
								{Name: "OTEL_EXPORTER_OTLP_PROTOCOL", Value: "grpc"},
								{Name: "OTEL_SERVICE_NAME", Value: fmt.Sprintf("sympozium-channel-%s", ch.Type)},
							},
						},
					},
				},
			},
		},
	}

	// Inject channel credentials from secret (if referenced)
	if ch.ConfigRef.Secret != "" {
		deploy.Spec.Template.Spec.Containers[0].EnvFrom = []corev1.EnvFromSource{
			{
				SecretRef: &corev1.SecretEnvSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ch.ConfigRef.Secret,
					},
				},
			},
		}
	}

	// WhatsApp channels need a persistent volume for credential storage
	if ch.Type == "whatsapp" {
		pvcName := fmt.Sprintf("%s-data", name)
		deploy.Spec.Strategy = appsv1.DeploymentStrategy{
			Type: appsv1.RecreateDeploymentStrategyType, // prevent two pods mounting the same PVC
		}
		deploy.Spec.Template.Spec.Volumes = []corev1.Volume{
			{
				Name: "whatsapp-data",
				VolumeSource: corev1.VolumeSource{
					PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
						ClaimName: pvcName,
					},
				},
			},
		}
		deploy.Spec.Template.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
			{
				Name:      "whatsapp-data",
				MountPath: "/data",
			},
		}
	}

	// Slack-specific configuration (threading, allowed-triggers,
	// thread stickiness, access control). Slack pod gates inbound
	// messages itself before publishing to NATS; the controller's
	// channel router still applies AccessControl as defence in depth.
	if ch.Type == "slack" {
		c := &deploy.Spec.Template.Spec.Containers[0]
		if ch.Slack != nil {
			if ch.Slack.Threading {
				c.Env = append(c.Env, corev1.EnvVar{Name: "SLACK_THREADING", Value: "true"})
			}
			if ch.Slack.ThreadStickiness {
				c.Env = append(c.Env, corev1.EnvVar{Name: "SLACK_THREAD_STICKINESS", Value: "true"})
			}
			if len(ch.Slack.AllowedTriggers) > 0 {
				c.Env = append(c.Env, corev1.EnvVar{
					Name:  "SLACK_ALLOWED_TRIGGERS",
					Value: strings.Join(ch.Slack.AllowedTriggers, ","),
				})
			}
		}
		if ch.AccessControl != nil {
			ac := ch.AccessControl
			if len(ac.AllowedSenders) > 0 {
				c.Env = append(c.Env, corev1.EnvVar{Name: "SLACK_ALLOWED_SENDERS", Value: strings.Join(ac.AllowedSenders, ",")})
			}
			if len(ac.DeniedSenders) > 0 {
				c.Env = append(c.Env, corev1.EnvVar{Name: "SLACK_DENIED_SENDERS", Value: strings.Join(ac.DeniedSenders, ",")})
			}
			if len(ac.AllowedChats) > 0 {
				c.Env = append(c.Env, corev1.EnvVar{Name: "SLACK_ALLOWED_CHATS", Value: strings.Join(ac.AllowedChats, ",")})
			}
		}
	}

	// Per-channel volumes (e.g. CSI SecretProviderClass priming the
	// configRef Secret). Applied last so per-channel volumes are appended
	// alongside any built-in volumes (e.g. WhatsApp's credential PVC).
	if len(ch.Volumes) > 0 {
		deploy.Spec.Template.Spec.Volumes = append(deploy.Spec.Template.Spec.Volumes, ch.Volumes...)
	}
	if len(ch.VolumeMounts) > 0 {
		c := &deploy.Spec.Template.Spec.Containers[0]
		c.VolumeMounts = append(c.VolumeMounts, ch.VolumeMounts...)
	}

	return deploy
}

// ensureWhatsAppPVC creates a PVC for the WhatsApp credential store if it doesn't exist.
func (r *AgentReconciler) ensureWhatsAppPVC(ctx context.Context, instance *sympoziumv1alpha1.Agent, deployName string) error {
	pvcName := fmt.Sprintf("%s-data", deployName)
	var pvc corev1.PersistentVolumeClaim
	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: instance.Namespace}, &pvc)
	if err == nil {
		return nil // already exists
	}
	if !errors.IsNotFound(err) {
		return err
	}

	pvc = corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"sympozium.ai/component": "channel",
				"sympozium.ai/channel":   "whatsapp",
				"sympozium.ai/instance":  instance.Name,
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse("256Mi"),
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(instance, &pvc, r.Scheme); err != nil {
		return err
	}

	r.Log.Info("Creating WhatsApp credential PVC", "name", pvcName)
	return r.Create(ctx, &pvc)
}

// channelMountsCSI reports whether the channel pod mounts any CSI volume.
// When true the controller skips the configRef Secret existence check because
// the Secret is expected to be materialized by the CSI driver (e.g. Vault
// Secrets Store CSI) on first pod mount. This is intentionally broad: any CSI
// volume triggers the bypass, since channel pods with CSI are overwhelmingly
// used for secret injection.
func channelMountsCSI(ch sympoziumv1alpha1.ChannelSpec) bool {
	for _, v := range ch.Volumes {
		if v.CSI != nil {
			return true
		}
	}
	return false
}

// ensureChannelServiceAccount creates the sympozium-channel ServiceAccount in
// the given namespace if it does not already exist. Channel Deployments
// reference this SA so workloads like Vault Secrets Store CSI can authenticate
// via the pod's SA token.
func (r *AgentReconciler) ensureChannelServiceAccount(ctx context.Context, namespace string) error {
	sa := &corev1.ServiceAccount{}
	err := r.Get(ctx, client.ObjectKey{Name: channelServiceAccountName, Namespace: namespace}, sa)
	if err == nil {
		return nil // already exists
	}
	if !errors.IsNotFound(err) {
		return fmt.Errorf("checking for channel service account: %w", err)
	}
	sa = &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      channelServiceAccountName,
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
		return fmt.Errorf("creating channel service account: %w", err)
	}
	return nil
}

// cleanupChannelDeployments removes channel deployments owned by the instance.
func (r *AgentReconciler) cleanupChannelDeployments(ctx context.Context, instance *sympoziumv1alpha1.Agent) error {
	var deploys appsv1.DeploymentList
	if err := r.List(ctx, &deploys,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{"sympozium.ai/instance": instance.Name, "sympozium.ai/component": "channel"},
	); err != nil {
		return err
	}

	for i := range deploys.Items {
		if err := r.Delete(ctx, &deploys.Items[i]); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}

	return nil
}

// countActiveAgentPods counts running agent pods for this instance.
func (r *AgentReconciler) countActiveAgentPods(ctx context.Context, instance *sympoziumv1alpha1.Agent) (int, bool, error) {
	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{"sympozium.ai/instance": instance.Name},
	); err != nil {
		return 0, false, err
	}

	count := 0
	hasServing := false
	for _, run := range runs.Items {
		if run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseRunning ||
			run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseServing {
			count++
		}
		if run.Status.Phase == sympoziumv1alpha1.AgentRunPhaseServing {
			hasServing = true
		}
	}
	return count, hasServing, nil
}

// reconcileMemoryConfigMap ensures the memory ConfigMap exists when memory is
// enabled for the instance. The ConfigMap is named "<instance>-memory" and
// contains a single key "MEMORY.md".
func (r *AgentReconciler) reconcileMemoryConfigMap(ctx context.Context, log logr.Logger, instance *sympoziumv1alpha1.Agent) error {
	if instance.Spec.Memory == nil || !instance.Spec.Memory.Enabled {
		return nil
	}

	cmName := fmt.Sprintf("%s-memory", instance.Name)
	var cm corev1.ConfigMap
	err := r.Get(ctx, types.NamespacedName{Name: cmName, Namespace: instance.Namespace}, &cm)
	if err == nil {
		return nil // Already exists.
	}
	if !errors.IsNotFound(err) {
		return err
	}

	// Create the memory ConfigMap with initial content.
	initialContent := "# Agent Memory\n\nNo memories recorded yet.\n"
	cm = corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance":  instance.Name,
				"sympozium.ai/component": "memory",
			},
		},
		Data: map[string]string{
			"MEMORY.md": initialContent,
		},
	}

	if err := controllerutil.SetControllerReference(instance, &cm, r.Scheme); err != nil {
		return err
	}

	log.Info("Creating memory ConfigMap", "name", cmName)
	return r.Create(ctx, &cm)
}

// cleanupMemoryConfigMap deletes the memory ConfigMap for an instance.
func (r *AgentReconciler) cleanupMemoryConfigMap(ctx context.Context, instance *sympoziumv1alpha1.Agent) error {
	cmName := fmt.Sprintf("%s-memory", instance.Name)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: instance.Namespace,
		},
	}
	if err := r.Delete(ctx, cm); err != nil && !errors.IsNotFound(err) {
		return err
	}
	return nil
}

// reconcileMemoryPVC ensures a PersistentVolumeClaim exists for instances that
// use the "memory" SkillPack. The PVC persists the SQLite database across
// ephemeral agent pod runs.
func (r *AgentReconciler) reconcileMemoryPVC(ctx context.Context, log logr.Logger, instance *sympoziumv1alpha1.Agent) error {
	if !instanceHasMemorySkill(instance) {
		return nil
	}

	pvcName := fmt.Sprintf("%s-memory-db", instance.Name)
	var pvc corev1.PersistentVolumeClaim
	err := r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: instance.Namespace}, &pvc)
	if err == nil {
		return nil // Already exists.
	}
	if !errors.IsNotFound(err) {
		return err
	}

	storageSize := resource.MustParse("1Gi")
	pvc = corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance":  instance.Name,
				"sympozium.ai/component": "memory-db",
			},
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: storageSize,
				},
			},
		},
	}

	if err := controllerutil.SetControllerReference(instance, &pvc, r.Scheme); err != nil {
		return err
	}

	log.Info("Creating memory PVC", "name", pvcName)
	return r.Create(ctx, &pvc)
}

// instanceHasMemorySkill returns true if the instance references the "memory" SkillPack.
func instanceHasMemorySkill(instance *sympoziumv1alpha1.Agent) bool {
	for _, skill := range instance.Spec.Skills {
		if skill.SkillPackRef == "memory" {
			return true
		}
	}
	return false
}

// memoryAdminTokenEnv returns the env var that gates the memory-server's
// admin-only DELETE /delete endpoint on a bearer token sourced from a Secret.
// The Secret name is supplied by Helm via MEMORY_ADMIN_TOKEN_SECRET and is empty
// when the feature is disabled (returns nil — endpoint stays disabled). The
// SecretKeyRef is optional, so a missing Secret leaves MEMORY_ADMIN_TOKEN unset
// (disabling /delete) instead of blocking pod startup. The token is injected
// only into the memory-server pod, never into agent pods.
func memoryAdminTokenEnv() []corev1.EnvVar {
	secretName := os.Getenv("MEMORY_ADMIN_TOKEN_SECRET")
	if secretName == "" {
		return nil
	}
	optional := true
	return []corev1.EnvVar{{
		Name: "MEMORY_ADMIN_TOKEN",
		ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: secretName},
				Key:                  "token",
				Optional:             &optional,
			},
		},
	}}
}

// memoryServerContainer returns a pointer to the memory-server container in a
// pod spec, or nil when it is absent. Matched by name rather than index so an
// injected sidecar cannot shift the target.
func memoryServerContainer(spec *corev1.PodSpec) *corev1.Container {
	for i := range spec.Containers {
		if spec.Containers[i].Name == "memory-server" {
			return &spec.Containers[i]
		}
	}
	return nil
}

// syncMemoryAdminTokenEnv reconciles just the MEMORY_ADMIN_TOKEN env var on an
// existing memory Deployment: added when adminDelete is switched on, repointed
// when the Secret name changes, removed when the feature is switched off. It is
// a no-op when the env already matches, so steady-state reconciles issue no
// writes and the pod is not restarted.
//
// Shared by the per-agent memory Deployment (AgentReconciler) and the shared
// workflow memory Deployment (EnsembleReconciler), both of which are otherwise
// create-only and would never pick the token up after the fact.
//
// Note this reacts to the Secret *reference* changing, not to the token value
// inside the Secret. Rotating the value in place leaves the pod holding the old
// token in its env until it restarts.
func syncMemoryAdminTokenEnv(ctx context.Context, c client.Client, log logr.Logger, deploy *appsv1.Deployment) error {
	container := memoryServerContainer(&deploy.Spec.Template.Spec)
	if container == nil {
		return nil
	}

	desired := memoryAdminTokenEnv()
	var want *corev1.EnvVar
	if len(desired) > 0 {
		want = &desired[0]
	}

	idx := -1
	for i := range container.Env {
		if container.Env[i].Name == "MEMORY_ADMIN_TOKEN" {
			idx = i
			break
		}
	}

	switch {
	case want == nil && idx < 0:
		return nil // Disabled and absent — nothing to do.
	case want == nil:
		container.Env = append(container.Env[:idx], container.Env[idx+1:]...)
		log.Info("Removing memory admin token env", "deployment", deploy.Name)
	case idx < 0:
		container.Env = append(container.Env, *want)
		log.Info("Adding memory admin token env", "deployment", deploy.Name)
	case equality.Semantic.DeepEqual(container.Env[idx], *want):
		return nil // Already correct.
	default:
		container.Env[idx] = *want
		log.Info("Updating memory admin token env", "deployment", deploy.Name)
	}

	return c.Update(ctx, deploy)
}

// reconcileMemoryDeployment ensures a Deployment + Service exist for the memory
// server when the "memory" SkillPack is attached. The Deployment mounts the
// memory PVC and exposes an HTTP API that agent pods call.
func (r *AgentReconciler) reconcileMemoryDeployment(ctx context.Context, log logr.Logger, instance *sympoziumv1alpha1.Agent) error {
	if !instanceHasMemorySkill(instance) {
		return nil
	}

	deployName := fmt.Sprintf("%s-memory", instance.Name)
	pvcName := fmt.Sprintf("%s-memory-db", instance.Name)

	tag := r.ImageTag
	if tag == "" {
		tag = "latest"
	}
	registry := os.Getenv("SYMPOZIUM_IMAGE_REGISTRY")
	if registry == "" {
		registry = "ghcr.io/sympozium-ai/sympozium"
	}
	image := fmt.Sprintf("%s/skill-memory:%s", registry, tag)

	// --- Deployment ---
	var existingDeploy appsv1.Deployment
	err := r.Get(ctx, types.NamespacedName{Name: deployName, Namespace: instance.Namespace}, &existingDeploy)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}
	if err == nil {
		// Already exists. The rest of the spec is deliberately left alone, but the
		// admin-token env is reconciled so enabling adminDelete (or pointing it at a
		// different Secret) takes effect without deleting the Deployment.
		return syncMemoryAdminTokenEnv(ctx, r.Client, log, &existingDeploy)
	}

	replicas := int32(1)
	fsGroup := int64(1000)
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"sympozium.ai/component": "memory",
				"sympozium.ai/instance":  instance.Name,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType, // RWO PVC — only one pod at a time
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"sympozium.ai/component": "memory",
					"sympozium.ai/instance":  instance.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"sympozium.ai/component": "memory",
						"sympozium.ai/instance":  instance.Name,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "memory-server",
							Image:           image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Ports: []corev1.ContainerPort{
								{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
							},
							Env: []corev1.EnvVar{
								{Name: "MEMORY_DB_PATH", Value: "/data/memory.db"},
								{Name: "MEMORY_PORT", Value: "8080"},
								// OTel wiring so the memory sidecar emits
								// sympozium.memory.read / .write counters
								// (ISI-1406 gap 6). Keyed on endpoint presence.
								{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: resolveOTelEndpoint(instance)},
								{Name: "OTEL_EXPORTER_OTLP_PROTOCOL", Value: "grpc"},
								{Name: "OTEL_SERVICE_NAME", Value: fmt.Sprintf("sympozium-memory-%s", instance.Name)},
								{Name: "MEMORY_AGENT", Value: instance.Name},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "memory-db", MountPath: "/data"},
							},
							StartupProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health",
										Port: intstr.FromInt32(8080),
									},
								},
								PeriodSeconds:    2,
								FailureThreshold: 30, // up to 60s for slow PVC mounts
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health",
										Port: intstr.FromInt32(8080),
									},
								},
								PeriodSeconds: 10,
							},
							LivenessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/health",
										Port: intstr.FromInt32(8080),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       30,
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("64Mi"),
								},
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("200m"),
									corev1.ResourceMemory: resource.MustParse("128Mi"),
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "memory-db",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: pvcName,
								},
							},
						},
					},
					SecurityContext: &corev1.PodSecurityContext{
						FSGroup: &fsGroup,
					},
				},
			},
		},
	}

	// Gate the admin-only DELETE /delete endpoint on a Secret-backed bearer
	// token, injected into the memory-server pod only (never into agents).
	deploy.Spec.Template.Spec.Containers[0].Env = append(deploy.Spec.Template.Spec.Containers[0].Env, memoryAdminTokenEnv()...)

	if err := controllerutil.SetControllerReference(instance, deploy, r.Scheme); err != nil {
		return err
	}

	log.Info("Creating memory Deployment", "name", deployName)
	if err := r.Create(ctx, deploy); err != nil {
		return err
	}

	// --- Service ---
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deployName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"sympozium.ai/component": "memory",
				"sympozium.ai/instance":  instance.Name,
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"sympozium.ai/component": "memory",
				"sympozium.ai/instance":  instance.Name,
			},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080), Protocol: corev1.ProtocolTCP},
			},
		},
	}

	if err := controllerutil.SetControllerReference(instance, svc, r.Scheme); err != nil {
		return err
	}

	log.Info("Creating memory Service", "name", deployName)
	return r.Create(ctx, svc)
}

// cleanupMemoryDeployment deletes the memory Deployment and Service for an instance.
func (r *AgentReconciler) cleanupMemoryDeployment(ctx context.Context, instance *sympoziumv1alpha1.Agent) error {
	name := fmt.Sprintf("%s-memory", instance.Name)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: instance.Namespace},
	}
	if err := r.Delete(ctx, deploy); err != nil && !errors.IsNotFound(err) {
		return err
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: instance.Namespace},
	}
	if err := r.Delete(ctx, svc); err != nil && !errors.IsNotFound(err) {
		return err
	}

	return nil
}

// reconcileWebEndpoint ensures a server-mode AgentRun exists when the
// "web-endpoint" skill is present. The AgentRun controller handles creating
// the actual Deployment + Service.
func (r *AgentReconciler) reconcileWebEndpoint(ctx context.Context, instance *sympoziumv1alpha1.Agent) error {
	for _, skill := range instance.Spec.Skills {
		if skill.SkillPackRef == "web-endpoint" || skill.SkillPackRef == "skillpack-web-endpoint" {
			return r.ensureWebEndpointAgentRun(ctx, instance, skill)
		}
	}
	return nil
}

// ensureWebEndpointAgentRun checks for an existing server-mode AgentRun for
// this instance and creates one if none exists. The AgentRun controller will
// detect the RequiresServer sidecar and create a Deployment + Service.
func (r *AgentReconciler) ensureWebEndpointAgentRun(ctx context.Context, instance *sympoziumv1alpha1.Agent, webSkill sympoziumv1alpha1.SkillRef) error {
	// Check if a serving AgentRun already exists for this instance.
	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{
			"sympozium.ai/instance":  instance.Name,
			"sympozium.ai/component": "web-endpoint",
		},
	); err != nil {
		return fmt.Errorf("list web-endpoint agent runs: %w", err)
	}

	for _, run := range runs.Items {
		if run.DeletionTimestamp.IsZero() {
			return nil
		}
	}

	// No existing run — create one.
	runName := fmt.Sprintf("%s-web-endpoint", instance.Name)

	agentRun := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"sympozium.ai/instance":  instance.Name,
				"sympozium.ai/component": "web-endpoint",
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef:   instance.Name,
			AgentID:    "web-endpoint",
			SessionKey: "web-endpoint",
			Task:       sympoziumv1alpha1.NewStringTask("Serve HTTP requests for this instance"),
			Mode:       "server",
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:                 resolveProvider(instance),
				Model:                    instance.Spec.Agents.Default.Model,
				AuthSecretRef:            resolveAuthSecret(instance),
				ProviderHeaders:          instance.Spec.Agents.Default.ProviderHeaders,
				ProviderHeadersSecretRef: instance.Spec.Agents.Default.ProviderHeadersSecretRef,
			},
			Skills:           []sympoziumv1alpha1.SkillRef{webSkill},
			ImagePullSecrets: instance.Spec.ImagePullSecrets,
			Volumes:          instance.Spec.Volumes,
			VolumeMounts:     instance.Spec.VolumeMounts,
			Env:              instance.Spec.Agents.Default.Env,
			Timeout:          instance.Spec.Agents.Default.ParseRunTimeout(),
		},
	}

	if instance.Spec.Agents.Default.BaseURL != "" {
		agentRun.Spec.Model.BaseURL = instance.Spec.Agents.Default.BaseURL
	}
	if len(instance.Spec.Agents.Default.NodeSelector) > 0 {
		agentRun.Spec.Model.NodeSelector = instance.Spec.Agents.Default.NodeSelector
	}

	if err := controllerutil.SetControllerReference(instance, agentRun, r.Scheme); err != nil {
		return fmt.Errorf("set owner reference: %w", err)
	}

	if err := r.Create(ctx, agentRun); err != nil {
		if errors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("create web-endpoint AgentRun: %w", err)
	}

	r.Log.Info("created web-endpoint AgentRun", "instance", instance.Name, "run", runName)
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.Agent{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}
