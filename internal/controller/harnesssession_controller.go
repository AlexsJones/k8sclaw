package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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

// HarnessSessionReconciler turns an explicitly session-capable AgentRuntime
// into a private, Agent-owned Deployment and Service. It deliberately does
// not reuse AgentRun's Job lifecycle: an AgentRun remains an immutable,
// terminal execution record while this resource owns interactive state.
type HarnessSessionReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Log    logr.Logger
}

const harnessSessionSystemNamespace = "sympozium-system"

// +kubebuilder:rbac:groups=sympozium.ai,resources=harnesssessions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=harnesssessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=agents;agentruntimes,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete

func (r *HarnessSessionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("harnesssession", req.NamespacedName)
	var session sympoziumv1alpha1.HarnessSession
	if err := r.Get(ctx, req.NamespacedName, &session); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if session.Spec.IdleTimeout != nil && session.Status.Phase == "Ready" && session.Status.ActiveRequests == 0 && session.Status.LastActivityTime != nil && time.Since(session.Status.LastActivityTime.Time) >= session.Spec.IdleTimeout.Duration {
		session.Spec.DesiredState = "stopped"
		if err := r.Update(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.deleteWorkload(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		return r.setStatus(ctx, &session, "Draining", "IdleTimeout", "session stopped after its configured idle timeout", "", "", "")
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

	if err := r.reconcileStateClaim(ctx, &session, agent); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileDeployment(ctx, &session, agent, runtime); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileService(ctx, &session, runtime.Spec.Session.Port); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.reconcileNetworkPolicy(ctx, &session, runtime.Spec.Session.Port); err != nil {
		return ctrl.Result{}, err
	}

	var deployment appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: sessionWorkloadName(&session), Namespace: session.Namespace}, &deployment); err != nil {
		return ctrl.Result{}, err
	}
	phase, conditionStatus, conditionReason, message := "Pending", metav1.ConditionFalse, "WaitingForDeployment", "waiting for session deployment to become ready"
	if deployment.Status.ReadyReplicas > 0 {
		phase, conditionStatus, conditionReason, message = "Ready", metav1.ConditionTrue, "DeploymentReady", "session endpoint is ready for proxied requests"
		if session.Status.LastActivityTime == nil {
			now := metav1.Now()
			session.Status.LastActivityTime = &now
			session.Status.UsageAccounting = "unavailable"
		}
	}
	endpoint := fmt.Sprintf("http://%s.%s.svc:%d", sessionWorkloadName(&session), session.Namespace, runtime.Spec.Session.Port)
	result, err := r.setStatusWithCondition(ctx, &session, phase, conditionStatus, conditionReason, message, runtime.Status.ResolvedImageDigest, sessionWorkloadName(&session), endpoint)
	if err != nil {
		return result, err
	}
	if phase == "Ready" {
		if session.Spec.IdleTimeout != nil && session.Status.LastActivityTime != nil {
			remaining := time.Until(session.Status.LastActivityTime.Add(session.Spec.IdleTimeout.Duration))
			if remaining < time.Second {
				remaining = time.Second
			}
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
		return result, nil
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
	model, modelReason := resolveSessionModel(agent, runtime)
	if modelReason != "" {
		return nil, nil, modelReason
	}
	// This is an in-memory resolved copy, not a mutation of the admin-owned
	// AgentRuntime. The Agent remains the owner of the default model route and
	// credential allowlist when the runtime deliberately leaves model blank.
	runtime.Spec.Model = model
	if !agentAllowsModelCredential(agent, model.Provider, model.AuthSecretRef) {
		return nil, nil, fmt.Sprintf("Agent %q does not allow runtime model credential %q for provider %q", agent.Name, model.AuthSecretRef, model.Provider)
	}
	return agent, runtime, ""
}

func resolveSessionModel(agent *sympoziumv1alpha1.Agent, runtime *sympoziumv1alpha1.AgentRuntime) (*sympoziumv1alpha1.AgentRuntimeModel, string) {
	model := &sympoziumv1alpha1.AgentRuntimeModel{}
	if runtime.Spec.Model != nil {
		*model = *runtime.Spec.Model
	}
	if model.Model == "" {
		model.Model = agent.Spec.Agents.Default.Model
	}
	if model.BaseURL == "" {
		model.BaseURL = agent.Spec.Agents.Default.BaseURL
	}
	if model.Provider == "" && len(agent.Spec.AuthRefs) == 1 {
		model.Provider = agent.Spec.AuthRefs[0].Provider
	}
	if model.AuthSecretRef == "" {
		for _, ref := range agent.Spec.AuthRefs {
			if ref.Provider == "" || strings.EqualFold(ref.Provider, model.Provider) {
				model.AuthSecretRef = ref.Secret
				break
			}
		}
	}
	if strings.TrimSpace(model.Provider) == "" || strings.TrimSpace(model.Model) == "" {
		return nil, fmt.Sprintf("AgentRuntime %q needs spec.model.provider/model, or an Agent with exactly one provider credential and a default model", runtime.Name)
	}
	return model, ""
}

func sessionWorkloadName(session *sympoziumv1alpha1.HarnessSession) string {
	return session.Name
}

// reconcileStateClaim gives a HarnessSession durable adapter state. The Pi
// v1alpha2 adapter stores its named sessions under /tmp/pi-sessions and its
// generated provider config beneath HOME, so the claim is mounted over /tmp.
// Stopping a session deliberately removes only compute/network resources; the
// claim remains owned by the HarnessSession and is deleted only with the CR.
func (r *HarnessSessionReconciler) reconcileStateClaim(ctx context.Context, session *sympoziumv1alpha1.HarnessSession, agent *sympoziumv1alpha1.Agent) error {
	claim := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: sessionWorkloadName(session), Namespace: session.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, claim, func() error {
		if err := controllerutil.SetControllerReference(session, claim, r.Scheme); err != nil {
			return err
		}
		claim.Labels = map[string]string{
			"app.kubernetes.io/name":       "harness-session",
			"app.kubernetes.io/instance":   session.Name,
			"app.kubernetes.io/managed-by": "sympozium",
			"sympozium.ai/agent":           agent.Name,
		}
		claim.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
		claim.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")}
		return nil
	})
	return err
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
				{Name: "SYMPOZIUM_SESSION_PORT", Value: fmt.Sprintf("%d", runtime.Spec.Session.Port)},
				{Name: "MODEL_PROVIDER", Value: runtime.Spec.Model.Provider}, {Name: "MODEL_NAME", Value: runtime.Spec.Model.Model}, {Name: "MODEL_BASE_URL", Value: runtime.Spec.Model.BaseURL},
				{Name: "HOME", Value: "/tmp/home"}, {Name: "XDG_CONFIG_HOME", Value: "/tmp/config"}, {Name: "XDG_CACHE_HOME", Value: "/tmp/cache"},
			},
			VolumeMounts:   []corev1.VolumeMount{{Name: "tmp", MountPath: "/tmp"}},
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(runtime.Spec.Session.Port)}}, InitialDelaySeconds: 1, PeriodSeconds: 2, FailureThreshold: 15},
			LivenessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/healthz", Port: intstr.FromInt32(runtime.Spec.Session.Port)}}, InitialDelaySeconds: 10, PeriodSeconds: 10, FailureThreshold: 3},
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
		fsGroup := int64(1000)
		fsGroupPolicy := corev1.FSGroupChangeOnRootMismatch
		deployment.Spec.Template.Spec = corev1.PodSpec{AutomountServiceAccountToken: boolPtr(false), EnableServiceLinks: boolPtr(false), SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: boolPtr(true), FSGroup: &fsGroup, FSGroupChangePolicy: &fsGroupPolicy, SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{container}, Volumes: []corev1.Volume{{Name: "tmp", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: sessionWorkloadName(session)}}}}, ImagePullSecrets: agent.Spec.ImagePullSecrets}
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

// reconcileNetworkPolicy gives a session the minimum useful model egress while
// deliberately omitting NATS. A harness session has no IPC bridge and must not
// bypass the API/controller boundary by publishing directly.
func (r *HarnessSessionReconciler) reconcileNetworkPolicy(ctx context.Context, session *sympoziumv1alpha1.HarnessSession, port int32) error {
	policy := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: sessionWorkloadName(session), Namespace: session.Namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, policy, func() error {
		if err := controllerutil.SetControllerReference(session, policy, r.Scheme); err != nil {
			return err
		}
		apiNamespace := metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": harnessSessionSystemNamespace}}
		apiPod := metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/component": "apiserver"}}
		policy.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": "harness-session", "app.kubernetes.io/instance": session.Name}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &apiNamespace, PodSelector: &apiPod}}, Ports: []networkingv1.NetworkPolicyPort{{Protocol: protocolPtr(corev1.ProtocolTCP), Port: intstrPtr(port)}}}},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{Ports: []networkingv1.NetworkPolicyPort{{Protocol: protocolPtr(corev1.ProtocolUDP), Port: intstrPtr(53)}, {Protocol: protocolPtr(corev1.ProtocolTCP), Port: intstrPtr(53)}}},
				// HTTPS covers public providers; 8080 and 9473 preserve standard
				// cluster-local/node-proxy model routes. NATS (4222) is absent.
				{Ports: []networkingv1.NetworkPolicyPort{{Protocol: protocolPtr(corev1.ProtocolTCP), Port: intstrPtr(443)}, {Protocol: protocolPtr(corev1.ProtocolTCP), Port: intstrPtr(8080)}, {Protocol: protocolPtr(corev1.ProtocolTCP), Port: intstrPtr(9473)}}},
			},
		}
		return nil
	})
	return err
}

func (r *HarnessSessionReconciler) deleteWorkload(ctx context.Context, session *sympoziumv1alpha1.HarnessSession) error {
	for _, obj := range []client.Object{&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: sessionWorkloadName(session), Namespace: session.Namespace}}, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: sessionWorkloadName(session), Namespace: session.Namespace}}, &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: sessionWorkloadName(session), Namespace: session.Namespace}}} {
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
	return ctrl.NewControllerManagedBy(mgr).For(&sympoziumv1alpha1.HarnessSession{}).Owns(&appsv1.Deployment{}).Owns(&corev1.PersistentVolumeClaim{}).Owns(&corev1.Service{}).Owns(&networkingv1.NetworkPolicy{}).Complete(r)
}

func protocolPtr(protocol corev1.Protocol) *corev1.Protocol { return &protocol }

func intstrPtr(port int32) *intstr.IntOrString {
	value := intstr.FromInt32(port)
	return &value
}
