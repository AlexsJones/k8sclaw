package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// HarnessSessionReconciler turns an explicitly session-capable AgentRuntime
// into a private, Agent-owned Deployment and Service. It deliberately does
// not reuse AgentRun's Job lifecycle: an AgentRun remains an immutable,
// terminal execution record while this resource owns interactive state.
type HarnessSessionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

// +kubebuilder:rbac:groups=sympozium.ai,resources=harnesssessions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=harnesssessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=agents;agentruntimes,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete

func (r *HarnessSessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("harnesssession", req.NamespacedName)
	var session sympoziumv1alpha1.HarnessSession
	if err := r.Get(ctx, req.NamespacedName, &session); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if session.Spec.DesiredState == "stopped" {
		if err := r.deleteWorkload(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		return r.setStatus(ctx, &session, "Draining", "Stopped", "session is stopped", "", "", "")
	}

	agent, runtime, reason := r.resolveInputs(ctx, &session)
	if reason != "" {
		log.Info("HarnessSession cannot start", "reason", reason)
		return r.setStatus(ctx, &session, "Failed", "Invalid", reason, "", "", "")
	}

	if err := r.reconcileDeployment(ctx, &session, agent, runtime); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileService(ctx, &session, runtime.Spec.Session.Port); err != nil {
		return ctrl.Result{}, err
	}

	var deployment appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: sessionWorkloadName(&session), Namespace: session.Namespace}, &deployment); err != nil {
		return ctrl.Result{}, err
	}
	phase, conditionStatus, conditionReason, message := "Pending", metav1.ConditionFalse, "WaitingForDeployment", "waiting for session deployment to become ready"
	if deployment.Status.ReadyReplicas > 0 {
		phase, conditionStatus, conditionReason, message = "Ready", metav1.ConditionTrue, "DeploymentReady", "session endpoint is ready for proxied requests"
	}
	endpoint := fmt.Sprintf("http://%s.%s.svc:%d", sessionWorkloadName(&session), session.Namespace, runtime.Spec.Session.Port)
	result, err := r.setStatusWithCondition(ctx, &session, phase, conditionStatus, conditionReason, message, runtime.Status.ResolvedImageDigest, sessionWorkloadName(&session), endpoint)
	if err != nil || phase == "Ready" {
		return result, err
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *HarnessSessionReconciler) resolveInputs(ctx context.Context, session *sympoziumv1alpha1.HarnessSession) (*sympoziumv1alpha1.Agent, *sympoziumv1alpha1.AgentRuntime, string) {
	if strings.TrimSpace(session.Spec.AgentRef) == "" || strings.TrimSpace(session.Spec.RuntimeRef) == "" {
		return nil, nil, "spec.agentRef and spec.runtimeRef are required"
	}
	agent := &sympoziumv1alpha1.Agent{}
	if err := r.Get(ctx, types.NamespacedName{Name: session.Spec.AgentRef, Namespace: session.Namespace}, agent); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil, fmt.Sprintf("Agent %q was not found", session.Spec.AgentRef)
		}
		return nil, nil, fmt.Sprintf("could not read Agent %q", session.Spec.AgentRef)
	}
	runtime := &sympoziumv1alpha1.AgentRuntime{}
	if err := r.Get(ctx, types.NamespacedName{Name: session.Spec.RuntimeRef, Namespace: session.Namespace}, runtime); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil, fmt.Sprintf("AgentRuntime %q was not found", session.Spec.RuntimeRef)
		}
		return nil, nil, fmt.Sprintf("could not read AgentRuntime %q", session.Spec.RuntimeRef)
	}
	if !meta.IsStatusConditionTrue(runtime.Status.Conditions, sympoziumv1alpha1.AgentRuntimeReadyCondition) {
		return nil, nil, fmt.Sprintf("AgentRuntime %q is not Ready", runtime.Name)
	}
	if runtime.Spec.ContractVersion != "v1alpha2" || runtime.Spec.Session == nil || runtime.Spec.Session.Protocol != "openai-chat" || runtime.Spec.Session.Port == 0 {
		return nil, nil, fmt.Sprintf("AgentRuntime %q does not declare the v1alpha2 openai-chat session contract", runtime.Name)
	}
	if runtime.Spec.Model == nil || strings.TrimSpace(runtime.Spec.Model.Provider) == "" || strings.TrimSpace(runtime.Spec.Model.Model) == "" {
		return nil, nil, fmt.Sprintf("AgentRuntime %q must declare spec.model.provider and spec.model.model for sessions", runtime.Name)
	}
	if !agentAllowsModelCredential(agent, runtime.Spec.Model.Provider, runtime.Spec.Model.AuthSecretRef) {
		return nil, nil, fmt.Sprintf("Agent %q does not allow runtime model credential %q for provider %q", agent.Name, runtime.Spec.Model.AuthSecretRef, runtime.Spec.Model.Provider)
	}
	return agent, runtime, ""
}

func sessionWorkloadName(session *sympoziumv1alpha1.HarnessSession) string {
	return session.Name
}

func (r *HarnessSessionReconciler) reconcileDeployment(ctx context.Context, session *sympoziumv1alpha1.HarnessSession, agent *sympoziumv1alpha1.Agent, runtime *sympoziumv1alpha1.AgentRuntime) error {
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: sessionWorkloadName(session), Namespace: session.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, deployment, func() error {
		if err := controllerutil.SetControllerReference(session, deployment, r.Scheme); err != nil {
			return err
		}
		labels := map[string]string{
			"app.kubernetes.io/name": "harness-session", "app.kubernetes.io/instance": session.Name,
			"app.kubernetes.io/managed-by": "sympozium", "sympozium.ai/agent": agent.Name,
		}
		replicas := int32(1)
		readOnly, noPrivEsc := true, false
		deployment.Spec.Replicas = &replicas
		deployment.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
		deployment.Spec.Template.ObjectMeta = metav1.ObjectMeta{Labels: labels}
		container := corev1.Container{
			Name: "harness", Image: runtime.Spec.Image, ImagePullPolicy: corev1.PullIfNotPresent,
			Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: runtime.Spec.Session.Port, Protocol: corev1.ProtocolTCP}},
			SecurityContext: &corev1.SecurityContext{RunAsNonRoot: boolPtr(true), ReadOnlyRootFilesystem: &readOnly, AllowPrivilegeEscalation: &noPrivEsc, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			Resources:       corev1.ResourceRequirements{},
			Env: []corev1.EnvVar{
				{Name: "SYMPOZIUM_HARNESS_CONTRACT_VERSION", Value: "v1alpha2"},
				{Name: "MODEL_PROVIDER", Value: runtime.Spec.Model.Provider}, {Name: "MODEL_NAME", Value: runtime.Spec.Model.Model}, {Name: "MODEL_BASE_URL", Value: runtime.Spec.Model.BaseURL},
				{Name: "HOME", Value: "/tmp/home"}, {Name: "XDG_CONFIG_HOME", Value: "/tmp/config"}, {Name: "XDG_CACHE_HOME", Value: "/tmp/cache"},
			},
			VolumeMounts: []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
		}
		if runtime.Spec.Resources != nil {
			container.Resources = *runtime.Spec.Resources
		}
		if runtime.Spec.Model.AuthSecretRef != "" {
			for _, key := range allowedAuthSecretKeys {
				optional := true
				container.Env = append(container.Env, corev1.EnvVar{Name: key, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: runtime.Spec.Model.AuthSecretRef}, Key: key, Optional: &optional}}})
			}
		}
		deployment.Spec.Template.Spec = corev1.PodSpec{AutomountServiceAccountToken: boolPtr(false), EnableServiceLinks: boolPtr(false), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: boolPtr(true), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{container}, Volumes: []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}, ImagePullSecrets: agent.Spec.ImagePullSecrets}
		return nil
	})
	return err
}

func (r *HarnessSessionReconciler) reconcileService(ctx context.Context, session *sympoziumv1alpha1.HarnessSession, port int32) error {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: sessionWorkloadName(session), Namespace: session.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, svc, func() error {
		if err := controllerutil.SetControllerReference(session, svc, r.Scheme); err != nil {
			return err
		}
		svc.Spec.Selector = map[string]string{"app.kubernetes.io/name": "harness-session", "app.kubernetes.io/instance": session.Name}
		svc.Spec.Ports = []corev1.ServicePort{{Name: "http", Port: port, TargetPort: intstr.FromInt32(port), Protocol: corev1.ProtocolTCP}}
		return nil
	})
	return err
}

func (r *HarnessSessionReconciler) deleteWorkload(ctx context.Context, session *sympoziumv1alpha1.HarnessSession) error {
	for _, obj := range []client.Object{&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: sessionWorkloadName(session), Namespace: session.Namespace}}, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: sessionWorkloadName(session), Namespace: session.Namespace}}} {
		if err := r.Delete(ctx, obj); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *HarnessSessionReconciler) setStatus(ctx context.Context, session *sympoziumv1alpha1.HarnessSession, phase, reason, message, digest, service, endpoint string) (ctrl.Result, error) {
	return r.setStatusWithCondition(ctx, session, phase, metav1.ConditionFalse, reason, message, digest, service, endpoint)
}

func (r *HarnessSessionReconciler) setStatusWithCondition(ctx context.Context, session *sympoziumv1alpha1.HarnessSession, phase string, conditionStatus metav1.ConditionStatus, reason, message, digest, service, endpoint string) (ctrl.Result, error) {
	session.Status.Phase, session.Status.ResolvedImageDigest, session.Status.ServiceName, session.Status.Endpoint = phase, digest, service, endpoint
	meta.SetStatusCondition(&session.Status.Conditions, metav1.Condition{Type: sympoziumv1alpha1.HarnessSessionReadyCondition, Status: conditionStatus, Reason: reason, Message: message, ObservedGeneration: session.Generation})
	if err := r.Status().Update(ctx, session); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *HarnessSessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&sympoziumv1alpha1.HarnessSession{}).Owns(&appsv1.Deployment{}).Owns(&corev1.Service{}).Complete(r)
}
