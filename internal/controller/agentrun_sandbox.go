// Package controller provides Agent Sandbox (kubernetes-sigs/agent-sandbox) CRD
// support for AgentRun reconciliation. When agent-sandbox mode is enabled, the
// controller creates Sandbox CRs instead of batchv1.Jobs.
package controller

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// Agent Sandbox CRD GVRs (kubernetes-sigs/agent-sandbox).
//
// WARNING: These use the v1alpha1 API group "agents.x-k8s.io". As the upstream
// project graduates (v1alpha1 → v1beta1 → v1), the API group, version, and CR
// schema will change. When updating, also update:
//   - internal/apiserver/server.go (capability detection group/version string)
//   - charts/sympozium/templates/rbac.yaml (RBAC apiGroups)
//   - hack/agent-sandbox-crds.yaml (bundled test CRDs)
//   - test/integration/test-agent-sandbox.sh, test-sandbox-lmstudio-*.sh
var (
	sandboxGVR = schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxes",
	}
	sandboxClaimGVR = schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxclaims",
	}
	warmPoolGVR = schema.GroupVersionResource{
		Group:    "agents.x-k8s.io",
		Version:  "v1alpha1",
		Resource: "sandboxwarmpools",
	}
)

// reconcilePendingAgentSandbox handles pending AgentRuns that use the Agent Sandbox
// CRD execution backend. It creates a Sandbox CR (or SandboxClaim if a warm pool
// is referenced) instead of a batchv1.Job.
func (r *AgentRunReconciler) reconcilePendingAgentSandbox(
	ctx context.Context,
	log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun,
) (ctrl.Result, error) {
	ctx, span := controllerTracer.Start(ctx, "agentrun.create_sandbox",
		trace.WithAttributes(
			attribute.String("agentrun.name", agentRun.Name),
			attribute.String("instance.name", agentRun.Spec.AgentRef),
			attribute.String("runtime.class", agentRun.Spec.AgentSandbox.RuntimeClass),
		),
	)
	defer span.End()

	if r.DynamicClient == nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, "agent-sandbox mode requires dynamic client (agent-sandbox CRDs not available)")
	}

	log.Info("Creating Agent Sandbox CR for AgentRun")

	// Look up the Agent for memory/observability config.
	instance := &sympoziumv1alpha1.Agent{}
	memoryEnabled := false
	var observability *sympoziumv1alpha1.ObservabilitySpec
	var mcpServers []sympoziumv1alpha1.MCPServerRef
	var allowedOutboundChannels []string
	if err := r.Get(ctx, client.ObjectKey{
		Namespace: agentRun.Namespace,
		Name:      agentRun.Spec.AgentRef,
	}, instance); err == nil {
		if instance.Spec.Memory != nil && instance.Spec.Memory.Enabled {
			memoryEnabled = true
		}
		for _, ch := range instance.Spec.Channels {
			if t := strings.TrimSpace(ch.Type); t != "" {
				allowedOutboundChannels = append(allowedOutboundChannels, t)
			}
		}
		if instance.Spec.Observability != nil && instance.Spec.Observability.Enabled {
			obsCopy := *instance.Spec.Observability
			observability = &obsCopy
		}
		if len(agentRun.Spec.Skills) == 0 && len(instance.Spec.Skills) > 0 {
			if agentRun.Labels["sympozium.ai/source"] != "web-proxy" {
				agentRun.Spec.Skills = instance.Spec.Skills
			}
		}
		mcpServers = instance.Spec.MCPServers
	}

	// Resolve MCP servers.
	if len(mcpServers) > 0 {
		mcpServers = r.resolveMCPServerURLs(ctx, agentRun.Namespace, mcpServers)
	}

	// Create input ConfigMap and MCP ConfigMap.
	if err := r.createInputConfigMap(ctx, agentRun); err != nil {
		return ctrl.Result{}, fmt.Errorf("creating input ConfigMap: %w", err)
	}
	if len(mcpServers) > 0 {
		if err := r.ensureMCPConfigMap(ctx, agentRun, mcpServers); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating MCP ConfigMap: %w", err)
		}
	}

	// Ensure ServiceAccount.
	if err := r.ensureAgentServiceAccount(ctx, agentRun.Namespace); err != nil {
		return ctrl.Result{}, fmt.Errorf("ensuring agent service account: %w", err)
	}

	// Write traceparent.
	traceparent := formatTraceparent(span.SpanContext())
	if traceparent != "" {
		if agentRun.Annotations == nil {
			agentRun.Annotations = map[string]string{}
		}
		agentRun.Annotations["otel.dev/traceparent"] = traceparent
	}

	// Resolve sidecars (filter server-only).
	sidecars := r.resolveSkillSidecars(ctx, log, agentRun)
	taskSidecars := make([]resolvedSidecar, 0, len(sidecars))
	for _, sc := range sidecars {
		if sc.sidecar.RequiresServer {
			continue
		}
		taskSidecars = append(taskSidecars, sc)
	}

	// Wait for memory server if memory skill is attached.
	if agentRunHasMemorySkill(agentRun) {
		memoryDeployName := fmt.Sprintf("%s-memory", agentRun.Spec.AgentRef)
		var memoryDeploy appsv1.Deployment
		if err := r.Get(ctx, client.ObjectKey{
			Namespace: agentRun.Namespace,
			Name:      memoryDeployName,
		}, &memoryDeploy); err != nil {
			age := time.Since(agentRun.CreationTimestamp.Time)
			if age > 120*time.Second {
				return ctrl.Result{}, r.failRun(ctx, agentRun,
					fmt.Sprintf("memory server deployment %q not found after %s", memoryDeployName, age.Truncate(time.Second)))
			}
			log.Info("Memory server deployment not found, requeueing", "deployment", memoryDeployName, "age", age.Truncate(time.Second))
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
		if memoryDeploy.Status.ReadyReplicas < 1 {
			age := time.Since(agentRun.CreationTimestamp.Time)
			if age > 120*time.Second {
				return ctrl.Result{}, r.failRun(ctx, agentRun,
					fmt.Sprintf("memory server deployment %q has no ready replicas after %s", memoryDeployName, age.Truncate(time.Second)))
			}
			log.Info("Memory server not ready, requeueing", "deployment", memoryDeployName, "readyReplicas", memoryDeploy.Status.ReadyReplicas, "age", age.Truncate(time.Second))
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	// Mirror skills and create RBAC.
	if err := r.mirrorSkillConfigMaps(ctx, log, agentRun); err != nil {
		log.Error(err, "Failed to mirror skill ConfigMaps")
	}
	// RBAC creation is fatal: without it the agent sandbox will run but every
	// kubectl/API call inside skill sidecars will fail with "forbidden".
	// See reconcilePending in agentrun_controller.go for common causes.
	if err := r.ensureSkillRBAC(ctx, log, agentRun, taskSidecars); err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun,
			fmt.Sprintf("failed to create skill RBAC — the agent sandbox would run without Kubernetes permissions. "+
				"Check controller logs and kube-apiserver for authentication errors. "+
				"Common causes: expired ServiceAccount tokens, clock skew between nodes, "+
				"or missing RBAC permissions on the controller ClusterRole (re-run helm upgrade). "+
				"Underlying error: %v", err))
	}

	// Create RBAC for lifecycle hook containers if needed.
	if err := r.ensureLifecycleRBAC(ctx, log, agentRun); err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun,
			fmt.Sprintf("failed to create lifecycle RBAC — hook containers would lack Kubernetes permissions. "+
				"Check controller logs and kube-apiserver for authentication errors. "+
				"Underlying error: %v", err))
	}

	// Create workspace PVC when postRun lifecycle hooks are defined.
	if agentRun.Spec.Lifecycle != nil && len(agentRun.Spec.Lifecycle.PostRun) > 0 {
		if err := r.ensureWorkspacePVC(ctx, agentRun); err != nil {
			return ctrl.Result{}, fmt.Errorf("creating workspace PVC: %w", err)
		}
	}

	// Build containers/volumes using the existing shared logic. The sandbox
	// path used to bubble the dispatch error up to the reconciler, which made
	// an unknown task.mode (or any handler validation failure) requeue
	// forever — same loop the Job path used to hit before #302. Mirror the
	// Job path: surface the failure on AgentRun.status and return cleanly so
	// the run reaches a terminal Failed state instead of spinning.
	// PR #302 review (issuecomment 5033007953) — first smaller ask.
	containers, initContainers, err := r.buildContainers(agentRun, memoryEnabled, observability, taskSidecars, mcpServers, allowedOutboundChannels)
	if err != nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun,
			fmt.Sprintf("task-mode dispatch rejected the AgentRun (sandbox path): %v", err))
	}
	volumes := r.buildVolumes(agentRun, memoryEnabled, taskSidecars, mcpServers)

	// Build the Sandbox CR or SandboxClaim.
	var sandboxObj *unstructured.Unstructured
	warmPoolRef := agentRun.Spec.AgentSandbox.WarmPoolRef
	if warmPoolRef != "" {
		sandboxObj = r.buildSandboxClaimCR(agentRun, warmPoolRef)
	} else {
		sandboxObj = r.buildSandboxCR(agentRun, containers, initContainers, volumes)
	}

	// Create the CR via dynamic client.
	gvr := sandboxGVR
	if warmPoolRef != "" {
		gvr = sandboxClaimGVR
	}
	created, err := r.DynamicClient.Resource(gvr).Namespace(agentRun.Namespace).Create(
		ctx, sandboxObj, metav1.CreateOptions{},
	)
	if err != nil {
		if errors.IsAlreadyExists(err) {
			log.Info("Agent Sandbox CR already exists", "name", sandboxObj.GetName())
		} else {
			return ctrl.Result{}, fmt.Errorf("creating Agent Sandbox CR: %w", err)
		}
	}

	// Update status.
	now := metav1.Now()
	agentRun.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
	agentRun.Status.StartedAt = &now
	if warmPoolRef != "" {
		agentRun.Status.SandboxClaimName = sandboxObj.GetName()
	} else {
		agentRun.Status.SandboxName = sandboxObj.GetName()
	}
	if created != nil {
		agentRun.Status.SandboxName = created.GetName()
	}
	if sc := span.SpanContext(); sc.HasTraceID() {
		agentRun.Status.TraceID = sc.TraceID().String()
	}
	if err := r.Status().Update(ctx, agentRun); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileRunningAgentSandbox checks the status of a Sandbox CR and maps it
// back to the AgentRun lifecycle.
func (r *AgentRunReconciler) reconcileRunningAgentSandbox(
	ctx context.Context,
	log logr.Logger,
	agentRun *sympoziumv1alpha1.AgentRun,
) (ctrl.Result, error) {
	ctx, span := controllerTracer.Start(ctx, "agentrun.check_sandbox",
		trace.WithAttributes(
			attribute.String("agentrun.name", agentRun.Name),
			attribute.String("sandbox.name", agentRun.Status.SandboxName),
		),
	)
	defer span.End()

	if r.DynamicClient == nil {
		return ctrl.Result{}, r.failRun(ctx, agentRun, "dynamic client unavailable")
	}

	sandboxName := agentRun.Status.SandboxName
	if sandboxName == "" {
		sandboxName = agentRun.Status.SandboxClaimName
	}

	log.Info("Checking Agent Sandbox CR status", "sandbox", sandboxName)

	// Fetch the Sandbox CR.
	sandbox, err := r.DynamicClient.Resource(sandboxGVR).Namespace(agentRun.Namespace).Get(
		ctx, sandboxName, metav1.GetOptions{},
	)
	if err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, r.failRun(ctx, agentRun, fmt.Sprintf("sandbox CR %q not found", sandboxName))
		}
		return ctrl.Result{}, fmt.Errorf("getting sandbox CR: %w", err)
	}

	// The upstream agent-sandbox controller creates a pod with the same name
	// as the Sandbox CR. Update podName on the AgentRun status so that
	// extractResultFromPod can read logs from it.
	if agentRun.Status.PodName != sandboxName {
		agentRun.Status.PodName = sandboxName
		if err := r.Status().Update(ctx, agentRun); err != nil {
			return ctrl.Result{}, err
		}
	}

	// The upstream Sandbox CRD uses status.conditions (not status.phase).
	// Derive a phase from the Ready condition and the pod's actual state.
	phase := sandboxPhaseFromConditions(sandbox.Object)
	log.V(1).Info("Sandbox phase derived from conditions", "phase", phase)

	// If the conditions don't give us a terminal state, check the pod
	// directly — the agent-runner is a run-to-completion workload.
	if phase == "Running" || phase == "" {
		phase = r.refineSandboxPhaseFromPod(ctx, agentRun.Namespace, sandboxName, phase)
	}

	switch phase {
	case "Running", "Ready", "":
		// Still running — check for timeout.
		if agentRun.Spec.Timeout != nil && agentRun.Status.StartedAt != nil {
			elapsed := time.Since(agentRun.Status.StartedAt.Time)
			if elapsed > agentRun.Spec.Timeout.Duration {
				log.Info("Agent Sandbox run timed out", "elapsed", elapsed)
				_ = r.DynamicClient.Resource(sandboxGVR).Namespace(agentRun.Namespace).Delete(
					ctx, sandboxName, metav1.DeleteOptions{},
				)
				return ctrl.Result{}, r.failRun(ctx, agentRun, "agent sandbox timed out")
			}
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil

	case "Completed", "Succeeded":
		log.Info("Agent Sandbox completed successfully")
		result, resultErr, usage, skipped := r.extractResultFromPod(ctx, log, agentRun)
		if skipped {
			return r.skipRun(ctx, agentRun, result)
		}
		if resultErr != "" {
			hasPostRunHooks := agentRun.Spec.Lifecycle != nil && len(agentRun.Spec.Lifecycle.PostRun) > 0
			if hasPostRunHooks {
				return r.startPostRun(ctx, log, agentRun, 1, resultErr, nil)
			}
			return ctrl.Result{}, r.failRun(ctx, agentRun, resultErr)
		}
		r.extractAndPersistMemory(ctx, log, agentRun)
		hasPostRunHooks := agentRun.Spec.Lifecycle != nil && len(agentRun.Spec.Lifecycle.PostRun) > 0
		if hasPostRunHooks {
			return r.startPostRun(ctx, log, agentRun, 0, result, usage)
		}
		return r.succeedRun(ctx, agentRun, result, usage)

	case "Failed", "Error":
		// Try to extract the structured result from pod logs first — the
		// agent-runner writes a detailed error there. Fall back to the
		// Sandbox condition message if pod logs aren't available.
		_, resultErr, _, _ := r.extractResultFromPod(ctx, log, agentRun)
		if resultErr == "" {
			resultErr = sandboxConditionMessage(sandbox.Object)
		}
		if resultErr == "" {
			resultErr = fmt.Sprintf("sandbox CR entered phase %q", phase)
		}
		hasPostRunHooks := agentRun.Spec.Lifecycle != nil && len(agentRun.Spec.Lifecycle.PostRun) > 0
		if hasPostRunHooks {
			return r.startPostRun(ctx, log, agentRun, 1, resultErr, nil)
		}
		return ctrl.Result{}, r.failRun(ctx, agentRun, resultErr)

	case "Suspended":
		log.Info("Agent Sandbox is suspended")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil

	default:
		log.V(1).Info("Unknown sandbox phase, requeueing", "phase", phase)
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
}

// buildSandboxCR constructs an unstructured Sandbox CR from the AgentRun spec.
func (r *AgentRunReconciler) buildSandboxCR(
	agentRun *sympoziumv1alpha1.AgentRun,
	containers []corev1.Container,
	initContainers []corev1.Container,
	volumes []corev1.Volume,
) *unstructured.Unstructured {
	labels := map[string]interface{}{
		"sympozium.ai/agent-run":       agentRun.Name,
		"sympozium.ai/instance":        agentRun.Spec.AgentRef,
		"sympozium.ai/component":       "agent-run",
		"sympozium.ai/role":            "agent",
		"sympozium.ai/agent-sandbox":   "true",
		"app.kubernetes.io/part-of":    "sympozium",
		"app.kubernetes.io/managed-by": "sympozium-controller",
	}

	runAsNonRoot := true
	runAsUser := int64(1000)
	fsGroup := int64(1000)

	// Build the pod template spec that goes inside the Sandbox CR.
	podSpec := map[string]interface{}{
		"serviceAccountName": "sympozium-agent",
		"restartPolicy":      "Never",
		"securityContext": map[string]interface{}{
			"runAsNonRoot": runAsNonRoot,
			"runAsUser":    runAsUser,
			"fsGroup":      fsGroup,
		},
	}

	// Convert containers to unstructured.
	containerList := make([]interface{}, 0, len(containers))
	for _, c := range containers {
		containerList = append(containerList, containerToMap(c))
	}
	podSpec["containers"] = containerList

	if len(initContainers) > 0 {
		initList := make([]interface{}, 0, len(initContainers))
		for _, c := range initContainers {
			initList = append(initList, containerToMap(c))
		}
		podSpec["initContainers"] = initList
	}

	// Convert volumes.
	volumeList := make([]interface{}, 0, len(volumes))
	for _, v := range volumes {
		volumeList = append(volumeList, volumeToMap(v))
	}
	if len(volumeList) > 0 {
		podSpec["volumes"] = volumeList
	}

	// Set runtimeClassName if specified (lives inside podTemplate.spec for upstream CRD).
	if rc := agentRun.Spec.AgentSandbox.RuntimeClass; rc != "" {
		podSpec["runtimeClassName"] = rc
	}

	spec := map[string]interface{}{
		"podTemplate": map[string]interface{}{
			"metadata": map[string]interface{}{
				"labels": labels,
			},
			"spec": podSpec,
		},
	}

	// Set owner reference for GC.
	ownerRefs := []interface{}{
		map[string]interface{}{
			"apiVersion":         "sympozium.ai/v1alpha1",
			"kind":               "AgentRun",
			"name":               agentRun.Name,
			"uid":                string(agentRun.UID),
			"controller":         true,
			"blockOwnerDeletion": true,
		},
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "Sandbox",
			"metadata": map[string]interface{}{
				"name":            fmt.Sprintf("sb-%s", agentRun.Name),
				"namespace":       agentRun.Namespace,
				"labels":          labels,
				"ownerReferences": ownerRefs,
			},
			"spec": spec,
		},
	}

	return obj
}

// buildSandboxClaimCR constructs an unstructured SandboxClaim CR that claims
// a pre-warmed sandbox from a SandboxWarmPool.
func (r *AgentRunReconciler) buildSandboxClaimCR(
	agentRun *sympoziumv1alpha1.AgentRun,
	warmPoolRef string,
) *unstructured.Unstructured {
	labels := map[string]interface{}{
		"sympozium.ai/agent-run":       agentRun.Name,
		"sympozium.ai/instance":        agentRun.Spec.AgentRef,
		"sympozium.ai/component":       "agent-run",
		"sympozium.ai/role":            "agent",
		"sympozium.ai/agent-sandbox":   "true",
		"app.kubernetes.io/part-of":    "sympozium",
		"app.kubernetes.io/managed-by": "sympozium-controller",
	}

	ownerRefs := []interface{}{
		map[string]interface{}{
			"apiVersion":         "sympozium.ai/v1alpha1",
			"kind":               "AgentRun",
			"name":               agentRun.Name,
			"uid":                string(agentRun.UID),
			"controller":         true,
			"blockOwnerDeletion": true,
		},
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "SandboxClaim",
			"metadata": map[string]interface{}{
				"name":            fmt.Sprintf("sbc-%s", agentRun.Name),
				"namespace":       agentRun.Namespace,
				"labels":          labels,
				"ownerReferences": ownerRefs,
			},
			"spec": map[string]interface{}{
				"warmPoolRef": map[string]interface{}{
					"name": warmPoolRef,
				},
			},
		},
	}

	return obj
}

// EnsureWarmPool creates or updates a SandboxWarmPool CR for a Agent
// that has agent-sandbox warm pool configuration.
func (r *AgentRunReconciler) EnsureWarmPool(
	ctx context.Context,
	log logr.Logger,
	instance *sympoziumv1alpha1.Agent,
	dynamicClient dynamic.Interface,
) error {
	agentSandbox := instance.Spec.Agents.Default.AgentSandbox
	if agentSandbox == nil || !agentSandbox.Enabled || agentSandbox.WarmPool == nil {
		return nil
	}

	wp := agentSandbox.WarmPool
	poolName := fmt.Sprintf("wp-%s", instance.Name)

	runtimeClass := wp.RuntimeClass
	if runtimeClass == "" {
		runtimeClass = agentSandbox.RuntimeClass
	}

	spec := map[string]interface{}{
		"size": int64(wp.Size),
	}
	if runtimeClass != "" {
		spec["runtimeClassName"] = runtimeClass
	}

	// Build a basic pod template for warm pool sandboxes.
	spec["podTemplate"] = map[string]interface{}{
		"spec": map[string]interface{}{
			"serviceAccountName": "sympozium-agent",
			"containers": []interface{}{
				map[string]interface{}{
					"name":    "agent",
					"image":   r.imageRef("agent-runner"),
					"command": []string{"sleep", "infinity"},
					"resources": map[string]interface{}{
						"requests": map[string]interface{}{
							"cpu":    "250m",
							"memory": "512Mi",
						},
						"limits": map[string]interface{}{
							"cpu":    "1",
							"memory": "1Gi",
						},
					},
				},
			},
		},
	}

	labels := map[string]interface{}{
		"sympozium.ai/instance":        instance.Name,
		"sympozium.ai/component":       "warm-pool",
		"app.kubernetes.io/part-of":    "sympozium",
		"app.kubernetes.io/managed-by": "sympozium-controller",
	}

	ownerRefs := []interface{}{
		map[string]interface{}{
			"apiVersion":         "sympozium.ai/v1alpha1",
			"kind":               "Agent",
			"name":               instance.Name,
			"uid":                string(instance.UID),
			"controller":         true,
			"blockOwnerDeletion": true,
		},
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "agents.x-k8s.io/v1alpha1",
			"kind":       "SandboxWarmPool",
			"metadata": map[string]interface{}{
				"name":            poolName,
				"namespace":       instance.Namespace,
				"labels":          labels,
				"ownerReferences": ownerRefs,
			},
			"spec": spec,
		},
	}

	_, err := dynamicClient.Resource(warmPoolGVR).Namespace(instance.Namespace).Get(
		ctx, poolName, metav1.GetOptions{},
	)
	if errors.IsNotFound(err) {
		_, err = dynamicClient.Resource(warmPoolGVR).Namespace(instance.Namespace).Create(
			ctx, obj, metav1.CreateOptions{},
		)
		if err != nil {
			return fmt.Errorf("creating SandboxWarmPool: %w", err)
		}
		log.Info("Created SandboxWarmPool", "name", poolName, "size", wp.Size)
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking SandboxWarmPool: %w", err)
	}

	// Update existing warm pool.
	_, err = dynamicClient.Resource(warmPoolGVR).Namespace(instance.Namespace).Update(
		ctx, obj, metav1.UpdateOptions{},
	)
	if err != nil {
		return fmt.Errorf("updating SandboxWarmPool: %w", err)
	}
	log.Info("Updated SandboxWarmPool", "name", poolName, "size", wp.Size)
	return nil
}

// sandboxPhaseFromConditions derives a phase string from the upstream Sandbox
// CR's status.conditions. The upstream CRD uses a "Ready" condition instead of
// a top-level phase field.
func sandboxPhaseFromConditions(obj map[string]interface{}) string {
	conditions, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		condType, _, _ := unstructured.NestedString(cond, "type")
		if condType != "Ready" {
			continue
		}
		status, _, _ := unstructured.NestedString(cond, "status")
		if status == "True" {
			return "Running"
		}
		// Ready=False is transient during pod startup (ContainerCreating,
		// Pending). We don't treat it as a failure here — let the caller
		// check the pod directly for a terminal phase.
	}
	return ""
}

// sandboxConditionMessage extracts the message from the Ready condition.
func sandboxConditionMessage(obj map[string]interface{}) string {
	conditions, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	for _, c := range conditions {
		cond, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		condType, _, _ := unstructured.NestedString(cond, "type")
		if condType == "Ready" {
			msg, _, _ := unstructured.NestedString(cond, "message")
			return msg
		}
	}
	return ""
}

// refineSandboxPhaseFromPod checks the actual pod state to determine if the
// agent-runner has completed. The upstream sandbox controller keeps the Sandbox
// CR "Ready" even after the pod finishes, so we inspect the pod directly.
func (r *AgentRunReconciler) refineSandboxPhaseFromPod(ctx context.Context, namespace, podName, fallback string) string {
	if r.Clientset == nil {
		return fallback
	}
	pod, err := r.Clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fallback
	}
	switch pod.Status.Phase {
	case corev1.PodSucceeded:
		return "Completed"
	case corev1.PodFailed:
		return "Failed"
	default:
		return fallback
	}
}

// containerToMap converts a corev1.Container to an unstructured map.
func containerToMap(c corev1.Container) map[string]interface{} {
	m := map[string]interface{}{
		"name":  c.Name,
		"image": c.Image,
	}

	if c.ImagePullPolicy != "" {
		m["imagePullPolicy"] = string(c.ImagePullPolicy)
	}

	if len(c.Command) > 0 {
		cmds := make([]interface{}, len(c.Command))
		for i, cmd := range c.Command {
			cmds[i] = cmd
		}
		m["command"] = cmds
	}

	if len(c.Env) > 0 {
		envList := make([]interface{}, 0, len(c.Env))
		for _, e := range c.Env {
			envMap := map[string]interface{}{
				"name":  e.Name,
				"value": e.Value,
			}
			envList = append(envList, envMap)
		}
		m["env"] = envList
	}

	if len(c.EnvFrom) > 0 {
		envFromList := make([]interface{}, 0, len(c.EnvFrom))
		for _, ef := range c.EnvFrom {
			efMap := map[string]interface{}{}
			if ef.SecretRef != nil {
				efMap["secretRef"] = map[string]interface{}{
					"name": ef.SecretRef.Name,
				}
			}
			envFromList = append(envFromList, efMap)
		}
		m["envFrom"] = envFromList
	}

	if len(c.VolumeMounts) > 0 {
		vmList := make([]interface{}, 0, len(c.VolumeMounts))
		for _, vm := range c.VolumeMounts {
			vmMap := map[string]interface{}{
				"name":      vm.Name,
				"mountPath": vm.MountPath,
			}
			if vm.ReadOnly {
				vmMap["readOnly"] = true
			}
			vmList = append(vmList, vmMap)
		}
		m["volumeMounts"] = vmList
	}

	if c.SecurityContext != nil {
		sc := map[string]interface{}{}
		if c.SecurityContext.ReadOnlyRootFilesystem != nil {
			sc["readOnlyRootFilesystem"] = *c.SecurityContext.ReadOnlyRootFilesystem
		}
		if c.SecurityContext.AllowPrivilegeEscalation != nil {
			sc["allowPrivilegeEscalation"] = *c.SecurityContext.AllowPrivilegeEscalation
		}
		if c.SecurityContext.Capabilities != nil && len(c.SecurityContext.Capabilities.Drop) > 0 {
			drop := make([]interface{}, len(c.SecurityContext.Capabilities.Drop))
			for i, cap := range c.SecurityContext.Capabilities.Drop {
				drop[i] = string(cap)
			}
			sc["capabilities"] = map[string]interface{}{
				"drop": drop,
			}
		}
		m["securityContext"] = sc
	}

	res := map[string]interface{}{}
	if c.Resources.Requests != nil {
		reqs := map[string]interface{}{}
		for k, v := range c.Resources.Requests {
			reqs[string(k)] = v.String()
		}
		res["requests"] = reqs
	}
	if c.Resources.Limits != nil {
		lims := map[string]interface{}{}
		for k, v := range c.Resources.Limits {
			lims[string(k)] = v.String()
		}
		res["limits"] = lims
	}
	if len(res) > 0 {
		m["resources"] = res
	}

	return m
}

// volumeToMap converts a corev1.Volume to an unstructured map.
func volumeToMap(v corev1.Volume) map[string]interface{} {
	m := map[string]interface{}{
		"name": v.Name,
	}

	if v.EmptyDir != nil {
		ed := map[string]interface{}{}
		if v.EmptyDir.Medium != "" {
			ed["medium"] = string(v.EmptyDir.Medium)
		}
		if v.EmptyDir.SizeLimit != nil {
			ed["sizeLimit"] = v.EmptyDir.SizeLimit.String()
		}
		m["emptyDir"] = ed
	}

	if v.ConfigMap != nil {
		m["configMap"] = map[string]interface{}{
			"name": v.ConfigMap.Name,
		}
	}

	if v.Projected != nil {
		sources := make([]interface{}, 0, len(v.Projected.Sources))
		for _, src := range v.Projected.Sources {
			srcMap := map[string]interface{}{}
			if src.ConfigMap != nil {
				srcMap["configMap"] = map[string]interface{}{
					"name": src.ConfigMap.Name,
				}
			}
			if src.Secret != nil {
				srcMap["secret"] = map[string]interface{}{
					"name": src.Secret.Name,
				}
			}
			sources = append(sources, srcMap)
		}
		m["projected"] = map[string]interface{}{
			"sources": sources,
		}
	}

	if v.PersistentVolumeClaim != nil {
		m["persistentVolumeClaim"] = map[string]interface{}{
			"claimName": v.PersistentVolumeClaim.ClaimName,
			"readOnly":  v.PersistentVolumeClaim.ReadOnly,
		}
	}

	return m
}

// CheckAgentSandboxCRDs checks if the Agent Sandbox CRDs are installed in the cluster.
// Returns true if available, false otherwise.
func CheckAgentSandboxCRDs(dynamicClient dynamic.Interface) bool {
	if dynamicClient == nil {
		return false
	}
	_, err := dynamicClient.Resource(sandboxGVR).List(
		context.Background(), metav1.ListOptions{Limit: 1},
	)
	return err == nil
}
