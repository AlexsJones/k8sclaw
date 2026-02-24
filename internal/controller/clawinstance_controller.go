// Package controller contains the reconciliation logic for KubeClaw CRDs.
package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kubeclawv1alpha1 "github.com/kubeclaw/kubeclaw/api/v1alpha1"
)

const clawInstanceFinalizer = "kubeclaw.io/finalizer"

// ClawInstanceReconciler reconciles a ClawInstance object.
type ClawInstanceReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Log      logr.Logger
	ImageTag string // release tag for KubeClaw images
}

// +kubebuilder:rbac:groups=kubeclaw.io,resources=clawinstances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=kubeclaw.io,resources=clawinstances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=kubeclaw.io,resources=clawinstances/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets;configmaps;services,verbs=get;list;watch;create;update;patch;delete

// Reconcile handles ClawInstance reconciliation.
func (r *ClawInstanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("clawinstance", req.NamespacedName)

	var instance kubeclawv1alpha1.ClawInstance
	if err := r.Get(ctx, req.NamespacedName, &instance); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !instance.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&instance, clawInstanceFinalizer) {
			log.Info("Cleaning up instance resources")
			if err := r.cleanupChannelDeployments(ctx, &instance); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.cleanupMemoryConfigMap(ctx, &instance); err != nil {
				log.Error(err, "failed to cleanup memory ConfigMap")
			}
			controllerutil.RemoveFinalizer(&instance, clawInstanceFinalizer)
			if err := r.Update(ctx, &instance); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if missing
	if !controllerutil.ContainsFinalizer(&instance, clawInstanceFinalizer) {
		controllerutil.AddFinalizer(&instance, clawInstanceFinalizer)
		if err := r.Update(ctx, &instance); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Reconcile channel deployments
	if err := r.reconcileChannels(ctx, &instance); err != nil {
		log.Error(err, "failed to reconcile channels")
		instance.Status.Phase = "Error"
		_ = r.Status().Update(ctx, &instance)
		return ctrl.Result{RequeueAfter: 30 * time.Second}, err
	}

	// Reconcile memory ConfigMap
	if err := r.reconcileMemoryConfigMap(ctx, log, &instance); err != nil {
		log.Error(err, "failed to reconcile memory ConfigMap")
	}

	// Count active agent pods
	activeCount, err := r.countActiveAgentPods(ctx, &instance)
	if err != nil {
		log.Error(err, "failed to count agent pods")
	}

	// Update status
	instance.Status.Phase = "Running"
	instance.Status.ActiveAgentPods = activeCount
	if err := r.Status().Update(ctx, &instance); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// reconcileChannels ensures a Deployment exists for each configured channel.
func (r *ClawInstanceReconciler) reconcileChannels(ctx context.Context, instance *kubeclawv1alpha1.ClawInstance) error {
	channelStatuses := make([]kubeclawv1alpha1.ChannelStatus, 0, len(instance.Spec.Channels))

	for _, ch := range instance.Spec.Channels {
		deployName := fmt.Sprintf("%s-channel-%s", instance.Name, ch.Type)

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
			channelStatuses = append(channelStatuses, kubeclawv1alpha1.ChannelStatus{
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
			channelStatuses = append(channelStatuses, kubeclawv1alpha1.ChannelStatus{
				Type:   ch.Type,
				Status: status,
			})
		}
	}

	instance.Status.Channels = channelStatuses
	return nil
}

// buildChannelDeployment creates a Deployment spec for a channel pod.
func (r *ClawInstanceReconciler) buildChannelDeployment(
	instance *kubeclawv1alpha1.ClawInstance,
	ch kubeclawv1alpha1.ChannelSpec,
	name string,
) *appsv1.Deployment {
	replicas := int32(1)
	tag := r.ImageTag
	if tag == "" {
		tag = "latest"
	}
	image := fmt.Sprintf("ghcr.io/alexsjones/kubeclaw/channel-%s:%s", ch.Type, tag)

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: instance.Namespace,
			Labels: map[string]string{
				"kubeclaw.io/component": "channel",
				"kubeclaw.io/channel":   ch.Type,
				"kubeclaw.io/instance":  instance.Name,
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"kubeclaw.io/component": "channel",
					"kubeclaw.io/channel":   ch.Type,
					"kubeclaw.io/instance":  instance.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"kubeclaw.io/component": "channel",
						"kubeclaw.io/channel":   ch.Type,
						"kubeclaw.io/instance":  instance.Name,
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            "channel",
							Image:           image,
							ImagePullPolicy: corev1.PullIfNotPresent,
							Env: []corev1.EnvVar{
								{Name: "INSTANCE_NAME", Value: instance.Name},
								{Name: "EVENT_BUS_URL", Value: "nats://nats.kubeclaw-system.svc:4222"},
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

	return deploy
}

// ensureWhatsAppPVC creates a PVC for the WhatsApp credential store if it doesn't exist.
func (r *ClawInstanceReconciler) ensureWhatsAppPVC(ctx context.Context, instance *kubeclawv1alpha1.ClawInstance, deployName string) error {
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
				"kubeclaw.io/component": "channel",
				"kubeclaw.io/channel":   "whatsapp",
				"kubeclaw.io/instance":  instance.Name,
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

// cleanupChannelDeployments removes channel deployments owned by the instance.
func (r *ClawInstanceReconciler) cleanupChannelDeployments(ctx context.Context, instance *kubeclawv1alpha1.ClawInstance) error {
	var deploys appsv1.DeploymentList
	if err := r.List(ctx, &deploys,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{"kubeclaw.io/instance": instance.Name, "kubeclaw.io/component": "channel"},
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
func (r *ClawInstanceReconciler) countActiveAgentPods(ctx context.Context, instance *kubeclawv1alpha1.ClawInstance) (int, error) {
	var runs kubeclawv1alpha1.AgentRunList
	if err := r.List(ctx, &runs,
		client.InNamespace(instance.Namespace),
		client.MatchingLabels{"kubeclaw.io/instance": instance.Name},
	); err != nil {
		return 0, err
	}

	count := 0
	for _, run := range runs.Items {
		if run.Status.Phase == kubeclawv1alpha1.AgentRunPhaseRunning {
			count++
		}
	}
	return count, nil
}

// reconcileMemoryConfigMap ensures the memory ConfigMap exists when memory is
// enabled for the instance. The ConfigMap is named "<instance>-memory" and
// contains a single key "MEMORY.md".
func (r *ClawInstanceReconciler) reconcileMemoryConfigMap(ctx context.Context, log logr.Logger, instance *kubeclawv1alpha1.ClawInstance) error {
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
				"kubeclaw.io/instance":  instance.Name,
				"kubeclaw.io/component": "memory",
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
func (r *ClawInstanceReconciler) cleanupMemoryConfigMap(ctx context.Context, instance *kubeclawv1alpha1.ClawInstance) error {
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

// SetupWithManager sets up the controller with the Manager.
func (r *ClawInstanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&kubeclawv1alpha1.ClawInstance{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}
