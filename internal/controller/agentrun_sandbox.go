// Package controller provides Agent Sandbox (kubernetes-sigs/agent-sandbox) CRD
// support for AgentRun reconciliation. When agent-sandbox mode is enabled, the
// controller creates Sandbox CRs instead of batchv1.Jobs.
package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"

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

	// Setup shared with the Job backend — see prepareRunPrerequisites. Server mode
	// is not reachable here: reconcilePending forks to this backend before its
	// spec.mode check, so an agentSandbox run is always task-mode.
	prereqs, err := r.prepareRunPrerequisites(ctx, log, span, agentRun)
	if err != nil {
		return ctrl.Result{}, err
	}

	taskSidecars, requeue, err := r.prepareTaskPrerequisites(ctx, log, agentRun, prereqs.sidecars)
	if requeue != nil {
		return *requeue, err
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	// Render the pod via the same template the Job backend uses, so containers,
	// volumes, pod security, and the registered pod mutators apply to both.
	template, err := r.buildAgentPodTemplate(ctx, agentRun, prereqs.inputs.memoryEnabled,
		prereqs.inputs.observability, taskSidecars, prereqs.mcpServers, prereqs.inputs.allowedOutboundChannels)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Build the Sandbox CR or SandboxClaim.
	var sandboxObj *unstructured.Unstructured
	warmPoolRef := agentRun.Spec.AgentSandbox.WarmPoolRef
	if warmPoolRef != "" {
		sandboxObj = r.buildSandboxClaimCR(agentRun, warmPoolRef)
	} else {
		sandboxObj = r.buildSandboxCR(agentRun, template)
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

// sandboxCRLabels renders the agent pod labels for an unstructured Sandbox CR or
// SandboxClaim, plus the marker distinguishing sandbox-backed runs.
//
// Shares agentPodLabels with the Job backend so the label set has one definition.
func sandboxCRLabels(agentRun *sympoziumv1alpha1.AgentRun) map[string]interface{} {
	labels := map[string]interface{}{
		"sympozium.ai/agent-sandbox": "true",
	}
	for k, v := range agentPodLabels(agentRun) {
		labels[k] = v
	}
	return labels
}

// buildSandboxCR constructs an unstructured Sandbox CR from the shared agent pod
// template.
//
// The template is the same one buildJob wraps in a batchv1.Job, so both backends
// ship the same pod. Pod shaping belongs in buildAgentPodTemplate; fields set here
// apply to sandbox-backed runs only. TestBackendParity covers this.
func (r *AgentRunReconciler) buildSandboxCR(
	agentRun *sympoziumv1alpha1.AgentRun,
	template corev1.PodTemplateSpec,
) *unstructured.Unstructured {
	labels := sandboxCRLabels(agentRun)

	podSpec := podSpecToMap(template.Spec)

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
	labels := sandboxCRLabels(agentRun)

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

// ── typed → unstructured conversion ──────────────────────────────────────────
//
// The Sandbox CR carries its pod template as unstructured data, so the
// corev1.PodTemplateSpec from buildAgentPodTemplate is converted on the way out.
// A field these converters omit produces no output key and no error.
//
// podSpecToMap and containerToMap are explicit allowlists over structs Sympozium
// authors, so the field set is bounded. TestPodSpecToMap_CoversEveryField and
// TestContainerToMap_CoversEveryField fail on any field neither converted here nor
// listed in the omit tables in agentrun_sandbox_convert_test.go.
//
// volumeToMap is lossless instead — see the note on that function.

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
		m["command"] = stringsToUnstructured(c.Command)
	}

	// Lifecycle hook containers set Args (see buildContainers).
	if len(c.Args) > 0 {
		m["args"] = stringsToUnstructured(c.Args)
	}

	if c.WorkingDir != "" {
		m["workingDir"] = c.WorkingDir
	}

	if len(c.Env) > 0 {
		envList := make([]interface{}, 0, len(c.Env))
		for _, e := range c.Env {
			envList = append(envList, envVarToMap(e))
		}
		m["env"] = envList
	}

	if len(c.EnvFrom) > 0 {
		envFromList := make([]interface{}, 0, len(c.EnvFrom))
		for _, ef := range c.EnvFrom {
			efMap := map[string]interface{}{}
			if ef.Prefix != "" {
				efMap["prefix"] = ef.Prefix
			}
			if ef.SecretRef != nil {
				ref := map[string]interface{}{"name": ef.SecretRef.Name}
				if ef.SecretRef.Optional != nil {
					ref["optional"] = *ef.SecretRef.Optional
				}
				efMap["secretRef"] = ref
			}
			if ef.ConfigMapRef != nil {
				ref := map[string]interface{}{"name": ef.ConfigMapRef.Name}
				if ef.ConfigMapRef.Optional != nil {
					ref["optional"] = *ef.ConfigMapRef.Optional
				}
				efMap["configMapRef"] = ref
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
			if vm.SubPath != "" {
				vmMap["subPath"] = vm.SubPath
			}
			if vm.MountPropagation != nil {
				vmMap["mountPropagation"] = string(*vm.MountPropagation)
			}
			vmList = append(vmList, vmMap)
		}
		m["volumeMounts"] = vmList
	}

	if c.SecurityContext != nil {
		m["securityContext"] = containerSecurityContextToMap(c.SecurityContext)
	}

	if res := resourceRequirementsToMap(c.Resources); len(res) > 0 {
		m["resources"] = res
	}

	// Native Kubernetes sidecars are init containers with restartPolicy: Always.
	if c.RestartPolicy != nil {
		m["restartPolicy"] = string(*c.RestartPolicy)
	}

	return m
}

// envVarToMap converts a corev1.EnvVar, including valueFrom.
//
// valueFrom carries the provider API key (SecretKeyRef over
// allowedAuthSecretKeys) and the canary HOST_IP (FieldRef).
func envVarToMap(e corev1.EnvVar) map[string]interface{} {
	m := map[string]interface{}{"name": e.Name}

	if e.ValueFrom == nil {
		// Only emit value for literal env vars — the two are mutually exclusive
		// and an empty "value" alongside valueFrom is rejected by the apiserver.
		m["value"] = e.Value
		return m
	}

	vf := map[string]interface{}{}
	if ref := e.ValueFrom.SecretKeyRef; ref != nil {
		sk := map[string]interface{}{"name": ref.Name, "key": ref.Key}
		if ref.Optional != nil {
			sk["optional"] = *ref.Optional
		}
		vf["secretKeyRef"] = sk
	}
	if ref := e.ValueFrom.ConfigMapKeyRef; ref != nil {
		ck := map[string]interface{}{"name": ref.Name, "key": ref.Key}
		if ref.Optional != nil {
			ck["optional"] = *ref.Optional
		}
		vf["configMapKeyRef"] = ck
	}
	if ref := e.ValueFrom.FieldRef; ref != nil {
		fr := map[string]interface{}{"fieldPath": ref.FieldPath}
		if ref.APIVersion != "" {
			fr["apiVersion"] = ref.APIVersion
		}
		vf["fieldRef"] = fr
	}
	if ref := e.ValueFrom.ResourceFieldRef; ref != nil {
		rf := map[string]interface{}{"resource": ref.Resource}
		if ref.ContainerName != "" {
			rf["containerName"] = ref.ContainerName
		}
		if !ref.Divisor.IsZero() {
			rf["divisor"] = ref.Divisor.String()
		}
		vf["resourceFieldRef"] = rf
	}
	// A non-nil ValueFrom whose members are all nil would serialise as an empty
	// valueFrom{}, which the apiserver rejects. Unreachable from buildContainers
	// today; fall back to the literal form rather than emitting an invalid pod.
	if len(vf) == 0 {
		m["value"] = e.Value
		return m
	}

	m["valueFrom"] = vf
	return m
}

// containerSecurityContextToMap converts a container SecurityContext.
//
// Host-access skill sidecars set privileged, seccompProfile, and runAsUser here
// (see buildContainers).
func containerSecurityContextToMap(sc *corev1.SecurityContext) map[string]interface{} {
	m := map[string]interface{}{}
	if sc.ReadOnlyRootFilesystem != nil {
		m["readOnlyRootFilesystem"] = *sc.ReadOnlyRootFilesystem
	}
	if sc.AllowPrivilegeEscalation != nil {
		m["allowPrivilegeEscalation"] = *sc.AllowPrivilegeEscalation
	}
	if sc.Privileged != nil {
		m["privileged"] = *sc.Privileged
	}
	if sc.RunAsUser != nil {
		m["runAsUser"] = *sc.RunAsUser
	}
	if sc.RunAsGroup != nil {
		m["runAsGroup"] = *sc.RunAsGroup
	}
	if sc.RunAsNonRoot != nil {
		m["runAsNonRoot"] = *sc.RunAsNonRoot
	}
	if sc.ProcMount != nil {
		m["procMount"] = string(*sc.ProcMount)
	}
	if sc.Capabilities != nil {
		caps := map[string]interface{}{}
		if len(sc.Capabilities.Drop) > 0 {
			caps["drop"] = capabilitiesToUnstructured(sc.Capabilities.Drop)
		}
		if len(sc.Capabilities.Add) > 0 {
			caps["add"] = capabilitiesToUnstructured(sc.Capabilities.Add)
		}
		if len(caps) > 0 {
			m["capabilities"] = caps
		}
	}
	if sc.SeccompProfile != nil {
		p := map[string]interface{}{"type": string(sc.SeccompProfile.Type)}
		if sc.SeccompProfile.LocalhostProfile != nil {
			p["localhostProfile"] = *sc.SeccompProfile.LocalhostProfile
		}
		m["seccompProfile"] = p
	}
	if sc.SELinuxOptions != nil {
		m["seLinuxOptions"] = seLinuxOptionsToMap(sc.SELinuxOptions)
	}
	if sc.WindowsOptions != nil {
		m["windowsOptions"] = windowsOptionsToMap(sc.WindowsOptions)
	}
	if sc.AppArmorProfile != nil {
		p := map[string]interface{}{"type": string(sc.AppArmorProfile.Type)}
		if sc.AppArmorProfile.LocalhostProfile != nil {
			p["localhostProfile"] = *sc.AppArmorProfile.LocalhostProfile
		}
		m["appArmorProfile"] = p
	}
	return m
}

// podSpecToMap converts the pod-level fields of the shared pod template.
//
// buildAgentPodTemplate is the only producer of this PodSpec, so the field set is
// bounded by what it sets. The omit table and TestPodSpecToMap_CoversEveryField
// make each exclusion explicit.
func podSpecToMap(spec corev1.PodSpec) map[string]interface{} {
	m := map[string]interface{}{}

	if spec.ServiceAccountName != "" {
		m["serviceAccountName"] = spec.ServiceAccountName
	}
	if spec.RestartPolicy != "" {
		m["restartPolicy"] = string(spec.RestartPolicy)
	}
	if spec.HostNetwork {
		m["hostNetwork"] = true
	}
	if spec.HostPID {
		m["hostPID"] = true
	}
	if spec.HostIPC {
		m["hostIPC"] = true
	}
	if spec.DNSPolicy != "" {
		m["dnsPolicy"] = string(spec.DNSPolicy)
	}
	if spec.RuntimeClassName != nil {
		m["runtimeClassName"] = *spec.RuntimeClassName
	}
	if spec.PriorityClassName != "" {
		m["priorityClassName"] = spec.PriorityClassName
	}
	if spec.NodeName != "" {
		m["nodeName"] = spec.NodeName
	}
	if spec.ActiveDeadlineSeconds != nil {
		m["activeDeadlineSeconds"] = *spec.ActiveDeadlineSeconds
	}
	if spec.TerminationGracePeriodSeconds != nil {
		m["terminationGracePeriodSeconds"] = *spec.TerminationGracePeriodSeconds
	}
	if spec.AutomountServiceAccountToken != nil {
		m["automountServiceAccountToken"] = *spec.AutomountServiceAccountToken
	}

	if len(spec.NodeSelector) > 0 {
		sel := make(map[string]interface{}, len(spec.NodeSelector))
		for k, v := range spec.NodeSelector {
			sel[k] = v
		}
		m["nodeSelector"] = sel
	}

	if len(spec.ImagePullSecrets) > 0 {
		refs := make([]interface{}, 0, len(spec.ImagePullSecrets))
		for _, ref := range spec.ImagePullSecrets {
			refs = append(refs, map[string]interface{}{"name": ref.Name})
		}
		m["imagePullSecrets"] = refs
	}

	if len(spec.Tolerations) > 0 {
		tols := make([]interface{}, 0, len(spec.Tolerations))
		for _, t := range spec.Tolerations {
			tol := map[string]interface{}{}
			if t.Key != "" {
				tol["key"] = t.Key
			}
			if t.Operator != "" {
				tol["operator"] = string(t.Operator)
			}
			if t.Value != "" {
				tol["value"] = t.Value
			}
			if t.Effect != "" {
				tol["effect"] = string(t.Effect)
			}
			if t.TolerationSeconds != nil {
				tol["tolerationSeconds"] = *t.TolerationSeconds
			}
			tols = append(tols, tol)
		}
		m["tolerations"] = tols
	}

	if spec.SecurityContext != nil {
		m["securityContext"] = podSecurityContextToMap(spec.SecurityContext)
	}

	if len(spec.Containers) > 0 {
		list := make([]interface{}, 0, len(spec.Containers))
		for _, c := range spec.Containers {
			list = append(list, containerToMap(c))
		}
		m["containers"] = list
	}

	if len(spec.InitContainers) > 0 {
		list := make([]interface{}, 0, len(spec.InitContainers))
		for _, c := range spec.InitContainers {
			list = append(list, containerToMap(c))
		}
		m["initContainers"] = list
	}

	if len(spec.Volumes) > 0 {
		list := make([]interface{}, 0, len(spec.Volumes))
		for _, v := range spec.Volumes {
			list = append(list, volumeToMap(v))
		}
		m["volumes"] = list
	}

	return m
}

// podSecurityContextToMap converts a pod-level SecurityContext.
func podSecurityContextToMap(sc *corev1.PodSecurityContext) map[string]interface{} {
	m := map[string]interface{}{}
	if sc.RunAsNonRoot != nil {
		m["runAsNonRoot"] = *sc.RunAsNonRoot
	}
	if sc.RunAsUser != nil {
		m["runAsUser"] = *sc.RunAsUser
	}
	if sc.RunAsGroup != nil {
		m["runAsGroup"] = *sc.RunAsGroup
	}
	if sc.FSGroup != nil {
		m["fsGroup"] = *sc.FSGroup
	}
	if sc.FSGroupChangePolicy != nil {
		m["fsGroupChangePolicy"] = string(*sc.FSGroupChangePolicy)
	}
	if len(sc.SupplementalGroups) > 0 {
		groups := make([]interface{}, len(sc.SupplementalGroups))
		for i, g := range sc.SupplementalGroups {
			groups[i] = g
		}
		m["supplementalGroups"] = groups
	}
	if sc.SeccompProfile != nil {
		p := map[string]interface{}{"type": string(sc.SeccompProfile.Type)}
		if sc.SeccompProfile.LocalhostProfile != nil {
			p["localhostProfile"] = *sc.SeccompProfile.LocalhostProfile
		}
		m["seccompProfile"] = p
	}
	if sc.SELinuxOptions != nil {
		m["seLinuxOptions"] = seLinuxOptionsToMap(sc.SELinuxOptions)
	}
	if sc.WindowsOptions != nil {
		m["windowsOptions"] = windowsOptionsToMap(sc.WindowsOptions)
	}
	if sc.AppArmorProfile != nil {
		p := map[string]interface{}{"type": string(sc.AppArmorProfile.Type)}
		if sc.AppArmorProfile.LocalhostProfile != nil {
			p["localhostProfile"] = *sc.AppArmorProfile.LocalhostProfile
		}
		m["appArmorProfile"] = p
	}
	if len(sc.Sysctls) > 0 {
		list := make([]interface{}, 0, len(sc.Sysctls))
		for _, s := range sc.Sysctls {
			list = append(list, map[string]interface{}{"name": s.Name, "value": s.Value})
		}
		m["sysctls"] = list
	}
	return m
}

func seLinuxOptionsToMap(o *corev1.SELinuxOptions) map[string]interface{} {
	m := map[string]interface{}{}
	if o.User != "" {
		m["user"] = o.User
	}
	if o.Role != "" {
		m["role"] = o.Role
	}
	if o.Type != "" {
		m["type"] = o.Type
	}
	if o.Level != "" {
		m["level"] = o.Level
	}
	return m
}

func windowsOptionsToMap(o *corev1.WindowsSecurityContextOptions) map[string]interface{} {
	m := map[string]interface{}{}
	if o.GMSACredentialSpecName != nil {
		m["gmsaCredentialSpecName"] = *o.GMSACredentialSpecName
	}
	if o.GMSACredentialSpec != nil {
		m["gmsaCredentialSpec"] = *o.GMSACredentialSpec
	}
	if o.RunAsUserName != nil {
		m["runAsUserName"] = *o.RunAsUserName
	}
	if o.HostProcess != nil {
		m["hostProcess"] = *o.HostProcess
	}
	return m
}

// resourceRequirementsToMap converts requests/limits, rendering quantities in
// their canonical string form ("100m", "64Mi").
func resourceRequirementsToMap(r corev1.ResourceRequirements) map[string]interface{} {
	res := map[string]interface{}{}
	if r.Requests != nil {
		reqs := map[string]interface{}{}
		for k, v := range r.Requests {
			reqs[string(k)] = v.String()
		}
		res["requests"] = reqs
	}
	if r.Limits != nil {
		lims := map[string]interface{}{}
		for k, v := range r.Limits {
			lims[string(k)] = v.String()
		}
		res["limits"] = lims
	}
	return res
}

func stringsToUnstructured(in []string) []interface{} {
	out := make([]interface{}, len(in))
	for i, s := range in {
		out[i] = s
	}
	return out
}

func capabilitiesToUnstructured(in []corev1.Capability) []interface{} {
	out := make([]interface{}, len(in))
	for i, c := range in {
		out[i] = string(c)
	}
	return out
}

// volumeToMap converts a corev1.Volume to an unstructured map losslessly.
//
// This is not an allowlist: buildVolumes passes agentRun.Spec.Volumes through
// untouched, so the source may be any member of the VolumeSource union (CSI,
// downwardAPI, nfs, …), including types added by later Kubernetes releases. An
// unrecognised source would produce a volume with no source, which the apiserver
// rejects.
func volumeToMap(v corev1.Volume) map[string]interface{} {
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&v)
	if err != nil {
		// Unreachable in practice: corev1.Volume is plain JSON-tagged data.
		// Returning the name alone surfaces as a rejected pod spec rather than a
		// dropped volume.
		return map[string]interface{}{"name": v.Name}
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
