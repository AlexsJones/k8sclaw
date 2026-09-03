package controller

import (
	"context"
	"fmt"
	"sort"
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
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

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

const (
	harnessSessionSystemNamespace = "sympozium-system"

	// harnessSessionFailedRequeue bounds how long a Failed session waits before
	// it re-checks its Agent and runtime when no watch event arrives. Auto-start
	// creates the session right after its Agent, so a cache that has not yet
	// observed the Agent must not leave the session Failed for good.
	harnessSessionFailedRequeue = 30 * time.Second

	// harnessSessionStaleRequestAfter is longer than the API proxy timeout. An
	// ActiveRequests count whose latest start is older than this cannot describe
	// real in-flight work: the API server that recorded the start did not live
	// to record the completion. Without this bound one crash would disable idle
	// shutdown forever.
	harnessSessionStaleRequestAfter = 5 * time.Minute

	harnessSessionStateClaimSize = "1Gi"
)

// +kubebuilder:rbac:groups=sympozium.ai,resources=harnesssessions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=sympozium.ai,resources=harnesssessions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=sympozium.ai,resources=agents;agentruntimes,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
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
	clearStaleActiveRequests(&session, log)
	if idleTimeoutElapsed(&session) {
		session.Spec.DesiredState = "stopped"
		if err := r.Update(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		// Update refreshes the object from the server, which reinstates the stale
		// counter the status write below must clear.
		clearStaleActiveRequests(&session, log)
		if err := r.deleteWorkload(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		return r.setStatus(ctx, &session, "Draining", "IdleTimeout", "session stopped after its configured idle timeout", session.Status.ResolvedImageDigest, "", "")
	}

	if session.Spec.DesiredState == "stopped" {
		if err := r.deleteWorkload(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		reason, message := "Stopped", "session is stopped"
		if condition := meta.FindStatusCondition(session.Status.Conditions, sympoziumv1alpha1.HarnessSessionReadyCondition); condition != nil && condition.Reason == "IdleTimeout" {
			reason, message = "IdleTimeout", "session stopped after its configured idle timeout"
		}
		// The digest that last ran is audit information and survives a stop.
		return r.setStatus(ctx, &session, "Draining", reason, message, session.Status.ResolvedImageDigest, "", "")
	}

	agent, runtime, reason, err := r.resolveInputs(ctx, &session)
	if err != nil {
		return ctrl.Result{}, err
	}
	if reason != "" {
		log.Info("HarnessSession cannot run", "reason", reason)
		// Fail closed. A session whose Agent, runtime, or credential binding is
		// no longer valid must not keep serving with the previous configuration.
		// Durable state is kept so a corrected binding resumes the conversation.
		if err := r.deleteWorkload(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		if _, err := r.setStatus(ctx, &session, "Failed", "Invalid", reason, session.Status.ResolvedImageDigest, "", ""); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: harnessSessionFailedRequeue}, nil
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
		if session.Status.Phase != "Ready" || session.Status.LastActivityTime == nil {
			// Becoming Ready starts the idle clock. Without this a session resumed
			// after an idle stop would carry its stale pre-stop activity time and
			// be stopped again on the next reconcile.
			now := metav1.Now()
			session.Status.LastActivityTime = &now
			session.Status.UsageAccounting = "unavailable"
		}
	} else if diagnosedReason, diagnosedMessage := r.diagnoseStartup(ctx, &session, &deployment); diagnosedReason != "" {
		conditionReason, message = diagnosedReason, diagnosedMessage
	}
	endpoint := fmt.Sprintf("http://%s.%s.svc:%d", sessionWorkloadName(&session), session.Namespace, runtime.Spec.Session.Port)
	result, err := r.setStatusWithCondition(ctx, &session, phase, conditionStatus, conditionReason, message, runtime.Status.ResolvedImageDigest, sessionWorkloadName(&session), endpoint)
	if err != nil {
		return result, err
	}
	if phase == "Ready" {
		if session.Spec.IdleTimeout != nil && session.Spec.IdleTimeout.Duration > 0 && session.Status.LastActivityTime != nil {
			if activeRequestsBlockIdle(&session) {
				// Completion of the request patches status and triggers a reconcile;
				// this requeue only covers the crash case bounded above.
				return ctrl.Result{RequeueAfter: harnessSessionStaleRequestAfter}, nil
			}
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

// clearStaleActiveRequests drops an in-flight count that no live request can
// explain, so the next status write stops reporting phantom work.
func clearStaleActiveRequests(session *sympoziumv1alpha1.HarnessSession, log logr.Logger) {
	if session.Status.ActiveRequests > 0 && !activeRequestsBlockIdle(session) {
		log.Info("clearing stale in-flight request count", "activeRequests", session.Status.ActiveRequests)
		session.Status.ActiveRequests = 0
	}
}

// idleTimeoutElapsed reports whether a Ready session has been without proxied
// activity for at least its configured idle timeout.
func idleTimeoutElapsed(session *sympoziumv1alpha1.HarnessSession) bool {
	if session.Spec.IdleTimeout == nil || session.Spec.IdleTimeout.Duration <= 0 || session.Status.Phase != "Ready" || session.Status.LastActivityTime == nil {
		return false
	}
	if activeRequestsBlockIdle(session) {
		return false
	}
	return time.Since(session.Status.LastActivityTime.Time) >= session.Spec.IdleTimeout.Duration
}

// activeRequestsBlockIdle reports whether recorded in-flight requests are
// recent enough to be real. The API server records a start before proxying and
// a completion afterwards; a request cannot outlive the proxy timeout.
func activeRequestsBlockIdle(session *sympoziumv1alpha1.HarnessSession) bool {
	if session.Status.ActiveRequests <= 0 {
		return false
	}
	if session.Status.LastRequestStartedAt != nil && time.Since(session.Status.LastRequestStartedAt.Time) >= harnessSessionStaleRequestAfter {
		return false
	}
	return true
}

// resolveInputs validates the Agent/runtime binding. A non-empty reason is a
// definitive, user-actionable rejection; a non-nil error is transient and is
// retried with backoff instead of being reported as Failed.
func (r *HarnessSessionReconciler) resolveInputs(ctx context.Context, session *sympoziumv1alpha1.HarnessSession) (*sympoziumv1alpha1.Agent, *sympoziumv1alpha1.AgentRuntime, string, error) {
	if strings.TrimSpace(session.Spec.AgentRef) == "" || strings.TrimSpace(session.Spec.RuntimeRef) == "" {
		return nil, nil, "spec.agentRef and spec.runtimeRef are required", nil
	}
	agent := &sympoziumv1alpha1.Agent{}
	if err := r.Get(ctx, types.NamespacedName{Name: session.Spec.AgentRef, Namespace: session.Namespace}, agent); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil, fmt.Sprintf("Agent %q was not found", session.Spec.AgentRef), nil
		}
		return nil, nil, "", fmt.Errorf("read Agent %q: %w", session.Spec.AgentRef, err)
	}
	runtime := &sympoziumv1alpha1.AgentRuntime{}
	if err := r.Get(ctx, types.NamespacedName{Name: session.Spec.RuntimeRef, Namespace: session.Namespace}, runtime); err != nil {
		if errors.IsNotFound(err) {
			return nil, nil, fmt.Sprintf("AgentRuntime %q was not found", session.Spec.RuntimeRef), nil
		}
		return nil, nil, "", fmt.Errorf("read AgentRuntime %q: %w", session.Spec.RuntimeRef, err)
	}
	if !meta.IsStatusConditionTrue(runtime.Status.Conditions, sympoziumv1alpha1.AgentRuntimeReadyCondition) {
		return nil, nil, fmt.Sprintf("AgentRuntime %q is not Ready", runtime.Name), nil
	}
	if runtime.Spec.ContractVersion != "v1alpha2" || runtime.Spec.Session == nil || runtime.Spec.Session.Protocol != "openai-chat" || runtime.Spec.Session.Port == 0 {
		return nil, nil, fmt.Sprintf("AgentRuntime %q does not declare the v1alpha2 openai-chat session contract", runtime.Name), nil
	}
	model, modelReason := resolveSessionModel(agent, runtime)
	if modelReason != "" {
		return nil, nil, modelReason, nil
	}
	// This is an in-memory resolved copy, not a mutation of the admin-owned
	// AgentRuntime. The Agent remains the owner of the default model route and
	// credential allowlist when the runtime deliberately leaves model blank.
	runtime.Spec.Model = model
	if !agentAllowsModelCredential(agent, model.Provider, model.AuthSecretRef) {
		return nil, nil, fmt.Sprintf("Agent %q does not allow runtime model credential %q for provider %q", agent.Name, model.AuthSecretRef, model.Provider), nil
	}
	return agent, runtime, "", nil
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

func sessionWorkloadLabels(session *sympoziumv1alpha1.HarnessSession) map[string]string {
	return map[string]string{"app.kubernetes.io/name": "harness-session", "app.kubernetes.io/instance": session.Name}
}

// diagnoseStartup explains why a running session has no ready replica, so the
// Agent Chat view can show an actionable reason instead of an indefinite
// "Starting". It inspects only controller-owned objects. The phase stays
// Pending because Kubernetes keeps retrying image pulls, scheduling, and
// restarts; the operator decides whether to stop or fix the input.
func (r *HarnessSessionReconciler) diagnoseStartup(ctx context.Context, session *sympoziumv1alpha1.HarnessSession, deployment *appsv1.Deployment) (string, string) {
	for _, condition := range deployment.Status.Conditions {
		if condition.Type == appsv1.DeploymentReplicaFailure && condition.Status == corev1.ConditionTrue {
			return "ReplicaFailure", "session pod cannot be created: " + condition.Message
		}
	}
	var pods corev1.PodList
	if err := r.List(ctx, &pods, client.InNamespace(session.Namespace), client.MatchingLabels(sessionWorkloadLabels(session))); err != nil {
		return "", ""
	}
	var candidates []corev1.Pod
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp == nil {
			candidates = append(candidates, pod)
		}
	}
	if len(candidates) == 0 {
		var claim corev1.PersistentVolumeClaim
		if err := r.Get(ctx, types.NamespacedName{Name: sessionWorkloadName(session), Namespace: session.Namespace}, &claim); err == nil && claim.Status.Phase == corev1.ClaimPending {
			return "StateClaimPending", "durable state claim is Pending; check that a StorageClass can provision it"
		}
		return "", ""
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].CreationTimestamp.After(candidates[j].CreationTimestamp.Time)
	})
	pod := candidates[0]
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse && condition.Reason == corev1.PodReasonUnschedulable {
			return "Unschedulable", "session pod cannot be scheduled: " + condition.Message
		}
	}
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil {
			switch status.State.Waiting.Reason {
			case "ImagePullBackOff", "ErrImagePull", "InvalidImageName":
				return "ImagePullFailed", "adapter image cannot be pulled: " + firstNonEmpty(status.State.Waiting.Message, status.State.Waiting.Reason)
			case "CrashLoopBackOff":
				return "AdapterCrashLoop", fmt.Sprintf("adapter container keeps exiting (%d restarts); inspect the pod logs for the adapter's startup diagnostics", status.RestartCount)
			case "CreateContainerConfigError", "CreateContainerError":
				return "ContainerConfigError", "adapter container cannot start: " + firstNonEmpty(status.State.Waiting.Message, status.State.Waiting.Reason)
			}
		}
		if status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 {
			return "AdapterExited", fmt.Sprintf("adapter container exited with code %d; inspect the pod logs for the adapter's startup diagnostics", status.State.Terminated.ExitCode)
		}
		if status.State.Running != nil && !status.Ready {
			return "AdapterNotReady", "adapter is running but its /healthz readiness probe has not passed yet"
		}
	}
	return "", ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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
		// A claim's access mode is immutable and its size may only grow. Set the
		// spec at creation only, so an operator who expanded the volume is not
		// fought by a reconcile that tries to shrink it back.
		if claim.CreationTimestamp.IsZero() {
			claim.Spec.AccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}
			claim.Spec.Resources.Requests = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(harnessSessionStateClaimSize)}
		}
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
		labels := sessionWorkloadLabels(session)
		labels["app.kubernetes.io/managed-by"] = "sympozium"
		labels["sympozium.ai/agent"] = agent.Name
		replicas := int32(1)
		readOnly, noPrivEsc := true, false
		deployment.Spec.Replicas = &replicas
		// The state claim is ReadWriteOnce, so a rolling update would leave the
		// replacement pod waiting on a volume the old pod still holds.
		deployment.Spec.Strategy = appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
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
		svc.Spec.Selector = sessionWorkloadLabels(session)
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
			PodSelector: metav1.LabelSelector{MatchLabels: sessionWorkloadLabels(session)},
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
	return ctrl.NewControllerManagedBy(mgr).
		For(&sympoziumv1alpha1.HarnessSession{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.NetworkPolicy{}).
		// A session's validity depends on objects it does not own. Re-evaluate
		// when the Agent's spec or the runtime (including its Ready status)
		// changes or disappears, so a Failed session recovers without a manual
		// retry and a revoked binding stops promptly.
		Watches(&sympoziumv1alpha1.Agent{}, handler.EnqueueRequestsFromMapFunc(r.sessionsReferencing(func(session *sympoziumv1alpha1.HarnessSession) string { return session.Spec.AgentRef })), builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&sympoziumv1alpha1.AgentRuntime{}, handler.EnqueueRequestsFromMapFunc(r.sessionsReferencing(func(session *sympoziumv1alpha1.HarnessSession) string { return session.Spec.RuntimeRef }))).
		Complete(r)
}

// sessionsReferencing maps a changed Agent or AgentRuntime to the sessions in
// its namespace that bind to it.
func (r *HarnessSessionReconciler) sessionsReferencing(ref func(*sympoziumv1alpha1.HarnessSession) string) handler.MapFunc {
	return func(ctx context.Context, obj client.Object) []reconcile.Request {
		var sessions sympoziumv1alpha1.HarnessSessionList
		if err := r.List(ctx, &sessions, client.InNamespace(obj.GetNamespace())); err != nil {
			r.Log.Error(err, "could not list HarnessSessions for dependency change", "object", client.ObjectKeyFromObject(obj))
			return nil
		}
		var requests []reconcile.Request
		for i := range sessions.Items {
			if ref(&sessions.Items[i]) == obj.GetName() {
				requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&sessions.Items[i])})
			}
		}
		return requests
	}
}

func protocolPtr(protocol corev1.Protocol) *corev1.Protocol { return &protocol }

func intstrPtr(port int32) *intstr.IntOrString {
	value := intstr.FromInt32(port)
	return &value
}
