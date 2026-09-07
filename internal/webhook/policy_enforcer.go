// Package webhook provides validating and mutating admission webhooks for Sympozium.
// These enforce SympoziumPolicy constraints on AgentRun resources.
package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-logr/logr"

	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller/taskmodes"
)

// systemNamespace is the namespace where built-in SkillPacks live by default.
const systemNamespace = "sympozium-system"

// reservedVolumeNames mirrors the controller-side reservedVolumeNames helper.
var reservedVolumeNames = map[string]struct{}{
	"workspace":                {},
	"ipc":                      {},
	"skills":                   {},
	"tmp":                      {},
	"memory":                   {},
	"mcp-config":               {},
	"sidecar-tools":            {},
	"harness-home":             {},
	"sympozium-run-api-access": {},
}

// builtinToolNames are the tool names Sympozium may register for the agent
// before sidecar tools. A native sidecar tool that collides with one of these is
// silently shadowed at runtime (executeToolCall dispatches sidecar tools last),
// so admission rejects the collision. This MUST stay in sync with the agent
// runner's tool registration: the Tool* constants in cmd/agent-runner/tools.go,
// the memory tools in cmd/agent-runner/memory_tools.go (memoryToolNames), and the
// workflow-memory tools (workflowMemoryToolNames). MCP tool names are dynamic
// (discovered at runtime) and cannot be checked here — operators are responsible
// for keeping sidecar tool names distinct from their MCP tool prefixes; the agent
// runner additionally skips a sidecar tool whose name is already registered.
var builtinToolNames = map[string]struct{}{
	// Always-on built-ins (cmd/agent-runner/tools.go).
	"execute_command":      {},
	"read_file":            {},
	"write_file":           {},
	"edit_file":            {},
	"list_directory":       {},
	"send_channel_message": {},
	"fetch_url":            {},
	"schedule_task":        {},
	"delegate_to_persona":  {},
	"spawn_subagents":      {},
	// Memory tools (cmd/agent-runner/memory_tools.go).
	"memory_search":          {},
	"memory_store":           {},
	"memory_list":            {},
	"workflow_memory_search": {},
	"workflow_memory_store":  {},
	"workflow_memory_list":   {},
}

// PolicyEnforcer is a validating webhook that enforces SympoziumPolicy on AgentRuns.
type PolicyEnforcer struct {
	Client  client.Client
	Log     logr.Logger
	Decoder admission.Decoder
}

// Handle validates AgentRun creation/updates against the bound SympoziumPolicy.
func (pe *PolicyEnforcer) Handle(ctx context.Context, req admission.Request) admission.Response {
	run := &sympoziumv1alpha1.AgentRun{}
	if err := pe.Decoder.Decode(req, run); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Skip validation for runs that are being deleted. Otherwise the
	// controller's own finalizer-removal Update gets rejected when the
	// referenced Instance has already been deleted (e.g. Ensemble
	// disable cascade), leaving the AgentRun stuck in a terminating
	// state forever with no way for kubelet GC to finish.
	if !run.DeletionTimestamp.IsZero() {
		return admission.Allowed("run is being deleted; skipping policy validation")
	}

	// Look up the owning Agent
	var instance sympoziumv1alpha1.Agent
	if err := pe.Client.Get(ctx, types.NamespacedName{
		Name:      run.Spec.AgentRef,
		Namespace: run.Namespace,
	}, &instance); err != nil {
		return admission.Errored(http.StatusBadRequest,
			fmt.Errorf("failed to find Agent %s: %w", run.Spec.AgentRef, err))
	}

	// Resolve a harness runtime reference into inline image/capabilities before
	// any other check, so image policy, capability, and digest validation all
	// see the resolved values rather than a runtime name they cannot use. When
	// the task names neither an image nor a runtime, the Agent's runtimeRef is
	// inherited.
	run.Spec.Task = taskmodes.ApplyAgentRuntime(run.Spec.Task, instance.Spec.RuntimeRef)
	normalized, err := taskmodes.NormalizeHarnessTask(run.Namespace, run.Spec.Task, func(ns, name string) (*sympoziumv1alpha1.AgentRuntime, error) {
		var rt sympoziumv1alpha1.AgentRuntime
		if err := pe.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &rt); err != nil {
			return nil, err
		}
		return &rt, nil
	})
	if err != nil {
		return admission.Denied(err.Error())
	}
	run.Spec.Task = normalized

	// A harness replaces trusted agent-runner and receives the selected model
	// credential as environment variables. Do not let a run author turn that
	// into an arbitrary Secret read: the backing Agent is the authority that
	// grants a provider credential to this execution identity.
	if taskmodes.HarnessImage(run.Spec.Task) != "" && !agentAllowsModelCredential(&instance, run.Spec.Model.Provider, run.Spec.Model.AuthSecretRef) {
		return admission.Denied(fmt.Sprintf("task.mode %q may use only model credentials declared in Agent %q spec.authRefs; secret %q is not allowed for provider %q", taskmodes.Harness, instance.Name, run.Spec.Model.AuthSecretRef, run.Spec.Model.Provider))
	}

	// Validate user-supplied volumes (AgentRun + resolved SkillPack sidecars).
	// This catches reserved-name collisions and same-name-different-source
	// collisions before they become silent mismounts at runtime.
	if err := pe.validateVolumes(ctx, run); err != nil {
		return admission.Denied(err.Error())
	}

	// Validate native sidecar tools declared on resolved SkillPacks: unique
	// snake_case names, no collision with built-in tools, and positional args
	// that reference declared parameters. The definitions become a read-only,
	// controller-written manifest the agent cannot forge, so admission is where
	// they are vetted.
	if err := pe.validateSidecarTools(ctx, run); err != nil {
		return admission.Denied(err.Error())
	}

	// Validate the run against its task mode's capability descriptor, so a
	// request the mode cannot honour is rejected here rather than accepted
	// and silently ignored at runtime.
	if err := taskmodes.ValidateTask(run.Spec.Task); err != nil {
		return admission.Denied(err.Error())
	}
	if err := pe.validateTaskModeCapabilities(run); err != nil {
		return admission.Denied(err.Error())
	}
	if err := taskmodes.ValidateRunCompatibility(run); err != nil {
		return admission.Denied(err.Error())
	}
	if err := pe.validateHarnessIsolation(ctx, run); err != nil {
		return admission.Denied(err.Error())
	}

	// Also policy-independent: a task mode that replaces the agent container
	// against an execution backend that never builds one.
	if err := validateTaskModeBackend(run); err != nil {
		return admission.Denied(err.Error())
	}

	// And an MCP server name that intrudes on Sympozium's reserved namespace.
	// The names come from the backing Agent, which is already in hand.
	if err := validateReservedMCPServerNames(instance.Spec.MCPServers); err != nil {
		return admission.Denied(err.Error())
	}
	// External adapters are outside Sympozium's trust boundary. Remote MCP
	// entries can carry Secret-derived credentials and arbitrary headers, so
	// fail closed until a trusted local mediator owns those connections.
	if taskmodes.HarnessImage(run.Spec.Task) != "" && len(instance.Spec.MCPServers) > 0 {
		return admission.Denied("task.mode \"harness\" cannot use Agent.spec.mcpServers: remote MCP credentials are never exposed to external adapters; use mediated SkillPack tools or remove the remote MCP servers")
	}

	// Replacing the trusted runner is a privileged operation. Require an
	// explicit policy binding before the otherwise-permissive early return.
	if instance.Spec.PolicyRef == "" {
		if taskmodes.HarnessImage(run.Spec.Task) != "" {
			return admission.Denied("task.mode \"harness\" requires an Agent with a SympoziumPolicy whose spec.harnessPolicy.enabled is true")
		}
		return admission.Allowed("no policy bound")
	}

	// Look up the SympoziumPolicy
	var policy sympoziumv1alpha1.SympoziumPolicy
	if err := pe.Client.Get(ctx, types.NamespacedName{
		Name:      instance.Spec.PolicyRef,
		Namespace: run.Namespace,
	}, &policy); err != nil {
		return admission.Errored(http.StatusInternalServerError,
			fmt.Errorf("failed to find SympoziumPolicy %s: %w", instance.Spec.PolicyRef, err))
	}

	if taskmodes.HarnessImage(run.Spec.Task) != "" &&
		(policy.Spec.HarnessPolicy == nil || !policy.Spec.HarnessPolicy.Enabled) {
		return admission.Denied("task.mode \"harness\" is disabled by policy; set spec.harnessPolicy.enabled: true to opt in")
	}
	if taskmodes.HarnessImage(run.Spec.Task) != "" && !policy.Spec.HarnessPolicy.AllowUnmetered {
		return admission.Denied("task.mode \"harness\" may not run unmetered under this policy; set spec.harnessPolicy.allowUnmetered: true only for an adapter whose accounting risk you accept")
	}

	// Validate sandbox policy
	if policy.Spec.SandboxPolicy != nil && policy.Spec.SandboxPolicy.Required {
		if run.Spec.Sandbox == nil || !run.Spec.Sandbox.Enabled {
			return admission.Denied("sandbox is required by policy")
		}
	}

	// Validate resource limits
	if err := pe.validateResources(run, &policy); err != nil {
		return admission.Denied(err.Error())
	}

	// Validate sub-agent depth
	if err := pe.validateSubagentDepth(run, &policy); err != nil {
		return admission.Denied(err.Error())
	}

	// Validate tool policy
	if err := pe.validateToolPolicy(run, &policy); err != nil {
		return admission.Denied(err.Error())
	}

	// Validate feature gates
	if err := pe.validateFeatureGates(run, &policy); err != nil {
		return admission.Denied(err.Error())
	}

	// Validate agent-sandbox policy
	if err := pe.validateAgentSandbox(run, &policy); err != nil {
		return admission.Denied(err.Error())
	}

	// Validate env vars do not override sensitive variables
	if err := pe.validateEnvVars(run); err != nil {
		return admission.Denied(err.Error())
	}

	// Validate image sources against policy allowlist
	if err := pe.validateImagePolicy(run, &policy); err != nil {
		return admission.Denied(err.Error())
	}

	// Validate lifecycle RBAC bounds
	if err := pe.validateLifecycleRBAC(run, &policy); err != nil {
		return admission.Denied(err.Error())
	}

	// Validate the gate retry ceiling
	if err := pe.validateRetryPolicy(run, &policy); err != nil {
		return admission.Denied(err.Error())
	}

	return admission.Allowed("policy validated")
}

// agentAllowsModelCredential checks the Agent-owned allowlist for a model
// Secret. An empty reference is valid for cluster-local and unauthenticated
// providers. Empty provider on an AuthRef means the Agent deliberately grants
// that Secret independent of provider naming.
func agentAllowsModelCredential(agent *sympoziumv1alpha1.Agent, provider, secret string) bool {
	if strings.TrimSpace(secret) == "" {
		return true
	}
	for _, ref := range agent.Spec.AuthRefs {
		if ref.Secret != secret {
			continue
		}
		if ref.Provider == "" || strings.EqualFold(ref.Provider, provider) {
			return true
		}
	}
	return false
}

func (pe *PolicyEnforcer) validateResources(run *sympoziumv1alpha1.AgentRun, policy *sympoziumv1alpha1.SympoziumPolicy) error {
	if policy.Spec.SandboxPolicy == nil || run.Spec.Sandbox == nil {
		return nil
	}

	if run.Spec.Sandbox.Resources == nil {
		return nil
	}

	// Compare both requests and limits against the policy caps, so a run cannot
	// request more sandbox CPU/memory than its bound SympoziumPolicy permits.
	check := func(dimension, capStr string, quantities []string) error {
		if capStr == "" {
			return nil
		}
		maxQ, err := resource.ParseQuantity(capStr)
		if err != nil {
			// A malformed policy cap should not silently disable enforcement.
			return fmt.Errorf("policy sandboxPolicy.max%s %q is not a valid quantity: %w", dimension, capStr, err)
		}
		for _, q := range quantities {
			if q == "" {
				continue
			}
			reqQ, err := resource.ParseQuantity(q)
			if err != nil {
				return fmt.Errorf("sandbox %s request %q is not a valid quantity: %w", dimension, q, err)
			}
			if reqQ.Cmp(maxQ) > 0 {
				return fmt.Errorf("sandbox %s %s exceeds policy limit %s", dimension, q, capStr)
			}
		}
		return nil
	}

	res := run.Spec.Sandbox.Resources
	if err := check("CPU", policy.Spec.SandboxPolicy.MaxCPU, []string{res.Requests["cpu"], res.Limits["cpu"]}); err != nil {
		return err
	}
	if err := check("Memory", policy.Spec.SandboxPolicy.MaxMemory, []string{res.Requests["memory"], res.Limits["memory"]}); err != nil {
		return err
	}

	return nil
}

type volumeOrigin struct {
	source string
	volume corev1.Volume
}

// validateVolumes rejects reserved-name collisions and same-name-different-source
// collisions across AgentRun.spec.volumes and resolved SkillPack sidecar volumes.
func (pe *PolicyEnforcer) validateVolumes(ctx context.Context, run *sympoziumv1alpha1.AgentRun) error {
	declarations := make(map[string][]volumeOrigin)

	for _, v := range run.Spec.Volumes {
		if _, reserved := reservedVolumeNames[v.Name]; reserved {
			return fmt.Errorf("AgentRun.spec.volumes[%q]: name is reserved by Sympozium (including workspace, ipc, skills, tmp, memory, mcp-config, and sympozium-run-api-access)", v.Name)
		}
		declarations[v.Name] = append(declarations[v.Name], volumeOrigin{
			source: "AgentRun.spec.volumes",
			volume: v,
		})
	}

	// SkillPack lookup is best-effort: missing SkillPacks are skipped so the
	// controller's lenient resolver remains the source of truth.
	for _, ref := range run.Spec.Skills {
		if ref.SkillPackRef == "" {
			continue
		}
		spName := strings.TrimPrefix(ref.SkillPackRef, "skillpack-")

		sp := &sympoziumv1alpha1.SkillPack{}
		if err := pe.Client.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: spName}, sp); err != nil {
			if err2 := pe.Client.Get(ctx, types.NamespacedName{Namespace: systemNamespace, Name: spName}, sp); err2 != nil {
				continue
			}
		}
		if sp.Spec.Sidecar == nil {
			continue
		}
		for _, v := range sp.Spec.Sidecar.Volumes {
			if _, reserved := reservedVolumeNames[v.Name]; reserved {
				return fmt.Errorf("SkillPack %q sidecar volume %q: name is reserved by Sympozium (including workspace, ipc, skills, tmp, memory, mcp-config, and sympozium-run-api-access)", spName, v.Name)
			}
			declarations[v.Name] = append(declarations[v.Name], volumeOrigin{
				source: fmt.Sprintf("SkillPack/%s.spec.sidecar.volumes", spName),
				volume: v,
			})
		}
	}

	for name, decls := range declarations {
		if len(decls) < 2 {
			continue
		}
		first := decls[0]
		for _, d := range decls[1:] {
			if !apiequality.Semantic.DeepEqual(first.volume.VolumeSource, d.volume.VolumeSource) {
				return fmt.Errorf("volume %q is declared by both %s and %s with different VolumeSource; rename one (e.g. prefix the SkillPack name) so each declaration is unambiguous", name, first.source, d.source)
			}
		}
	}

	return nil
}

var sidecarToolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// validateSidecarTools rejects malformed or conflicting native tool declarations
// across the AgentRun's resolved SkillPack sidecars. Like validateVolumes, the
// SkillPack lookup is best-effort so the controller's lenient resolver stays the
// source of truth for missing packs.
func (pe *PolicyEnforcer) validateSidecarTools(ctx context.Context, run *sympoziumv1alpha1.AgentRun) error {
	seen := make(map[string]string) // tool name -> declaring SkillPack

	for _, ref := range run.Spec.Skills {
		if ref.SkillPackRef == "" {
			continue
		}
		spName := strings.TrimPrefix(ref.SkillPackRef, "skillpack-")

		sp := &sympoziumv1alpha1.SkillPack{}
		if err := pe.Client.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: spName}, sp); err != nil {
			if err2 := pe.Client.Get(ctx, types.NamespacedName{Namespace: systemNamespace, Name: spName}, sp); err2 != nil {
				continue
			}
		}
		if sp.Spec.Sidecar == nil {
			continue
		}

		for _, tool := range sp.Spec.Sidecar.Tools {
			if err := validateSidecarToolStructural(spName, tool); err != nil {
				return err
			}
			// Cross-pack uniqueness is inherently per-run (it depends on which
			// SkillPacks are attached together), so it stays here rather than in
			// the structural validator shared with SkillPack admission.
			if other, dup := seen[tool.Name]; dup {
				return fmt.Errorf("sidecar tool %q is declared by both SkillPack %q and SkillPack %q; tool names must be unique across attached SkillPacks", tool.Name, other, spName)
			}
			seen[tool.Name] = spName
		}
	}

	return nil
}

// validateSidecarToolStructural runs the single-tool checks that do not depend
// on which other SkillPacks are attached. It is shared by the AgentRun policy
// webhook and the SkillPack validating webhook so authors get the same errors at
// SkillPack apply time.
func validateSidecarToolStructural(spName string, tool sympoziumv1alpha1.SidecarTool) error {
	if !sidecarToolNamePattern.MatchString(tool.Name) {
		return fmt.Errorf("SkillPack %q sidecar tool %q: name must be snake_case matching ^[a-z][a-z0-9_]*$", spName, tool.Name)
	}
	if _, isBuiltin := builtinToolNames[tool.Name]; isBuiltin {
		return fmt.Errorf("SkillPack %q sidecar tool %q: name collides with a built-in Sympozium tool", spName, tool.Name)
	}
	if len(tool.Exec) == 0 {
		return fmt.Errorf("SkillPack %q sidecar tool %q: exec must declare at least the executable", spName, tool.Name)
	}

	var props map[string]json.RawMessage
	var required map[string]struct{}
	if tool.Parameters != nil && len(tool.Parameters.Raw) > 0 {
		p, req, err := toolParameterSchema(tool.Parameters.Raw)
		if err != nil {
			return fmt.Errorf("SkillPack %q sidecar tool %q: parameters is not a valid JSON Schema object: %v", spName, tool.Name, err)
		}
		props, required = p, req
	}

	// Positional args must reference a declared, required parameter. Requiring
	// "required" prevents an omitted earlier positional from silently shifting
	// every later positional into the wrong slot.
	if len(tool.PositionalArgs) > 0 {
		if props == nil {
			return fmt.Errorf("SkillPack %q sidecar tool %q: positionalArgs is set but no parameters are declared", spName, tool.Name)
		}
		for _, pa := range tool.PositionalArgs {
			if _, ok := props[pa]; !ok {
				return fmt.Errorf("SkillPack %q sidecar tool %q: positionalArgs references %q which is not a declared parameter", spName, tool.Name, pa)
			}
			if _, ok := required[pa]; !ok {
				return fmt.Errorf("SkillPack %q sidecar tool %q: positional arg %q must be listed in parameters.required (optional positionals would shift later arguments)", spName, tool.Name, pa)
			}
		}
	}

	// In args mode (the default) only positional parameters reach the process;
	// any other declared parameter the model fills would be silently dropped.
	// Reject that ambiguity at author time.
	inputMode := tool.InputMode
	if inputMode == "" {
		inputMode = "args"
	}
	if inputMode == "args" && props != nil {
		positional := make(map[string]struct{}, len(tool.PositionalArgs))
		for _, pa := range tool.PositionalArgs {
			positional[pa] = struct{}{}
		}
		for name := range props {
			if _, ok := positional[name]; !ok {
				return fmt.Errorf("SkillPack %q sidecar tool %q: inputMode=args declares parameter %q that is not in positionalArgs; in args mode non-positional parameters are dropped — add it to positionalArgs or use inputMode=stdin", spName, tool.Name, name)
			}
		}
	}

	return nil
}

// toolParameterSchema extracts the top-level "properties" map and the set of
// "required" property names from a JSON Schema object. Either may be nil/empty
// when absent.
func toolParameterSchema(raw []byte) (map[string]json.RawMessage, map[string]struct{}, error) {
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, nil, err
	}
	required := make(map[string]struct{}, len(schema.Required))
	for _, r := range schema.Required {
		required[r] = struct{}{}
	}
	return schema.Properties, required, nil
}

// validateTaskModeCapabilities rejects an AgentRun whose object-form task
// asks its mode for something the mode does not support — spec.toolPolicy on
// a mode that cannot filter tools, spec.systemPrompt on one that ignores it.
// Without this the run is admitted and the field is quietly dropped, which is
// the failure mode a capability descriptor exists to remove.
//
// Policy-independent, so it runs before the SympoziumPolicy lookup: the
// mismatch is between the run and its mode, not between the run and a policy.
//
// A mode with no registered handler passes. The webhook is a separate binary
// from the controller, so a mode registered only in the controller's main()
// (the documented downstream-registration path) is not visible here; denying
// it would make that mode unschedulable. Unknown modes still fail in the
// controller with the supported-mode list.
func (pe *PolicyEnforcer) validateTaskModeCapabilities(run *sympoziumv1alpha1.AgentRun) error {
	return taskmodes.ValidateCapabilities(run)
}

// validateHarnessIsolation rejects privilege sources that would apply at pod
// scope or can communicate through storage shared with an external runtime.
// The controller repeats this check for clusters that run without admission.
func (pe *PolicyEnforcer) validateHarnessIsolation(ctx context.Context, run *sympoziumv1alpha1.AgentRun) error {
	if taskmodes.HarnessImage(run.Spec.Task) == "" {
		return nil
	}
	if run.Spec.Lifecycle != nil && len(run.Spec.Lifecycle.RBAC) > 0 {
		return fmt.Errorf("task.mode %q cannot be combined with lifecycle RBAC because hooks share run storage with the external runtime", taskmodes.Harness)
	}
	for _, ref := range run.Spec.Skills {
		if ref.SkillPackRef == "" {
			continue
		}
		name := strings.TrimPrefix(ref.SkillPackRef, "skillpack-")
		pack := &sympoziumv1alpha1.SkillPack{}
		if err := pe.Client.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: name}, pack); err != nil {
			if err := pe.Client.Get(ctx, types.NamespacedName{Namespace: systemNamespace, Name: name}, pack); err != nil {
				continue
			}
		}
		if pack.Spec.Sidecar != nil && pack.Spec.Sidecar.HostAccess != nil && pack.Spec.Sidecar.HostAccess.Enabled {
			return fmt.Errorf("task.mode %q cannot be combined with SkillPack %q host access, host networking, host PID, or privileged execution", taskmodes.Harness, name)
		}
	}
	return nil
}

// validateTaskModeBackend rejects a task mode that replaces the agent
// container on an execution backend that never creates one.
//
// backend: celln dispatches the task string to the celln router instead of
// scheduling a pod, so it never reaches buildContainers — a mode: harness run
// would be admitted, dispatched, and the operator's harness image silently
// dropped. The controller repeats this check (the webhook is a separate,
// optional deployment), and it mirrors the existing celln/agentSandbox
// mutual-exclusion guard there.
//
// agentSandbox is deliberately not included: it builds its pod through
// buildAgentPodTemplate, which wraps buildContainers, so task-mode dispatch
// applies normally.
// validateReservedMCPServerNames rejects an Agent-configured MCP server whose
// name intrudes on Sympozium's reserved namespace.
//
// Sympozium injects servers into the registry an agent reads — the skill tool
// server a harness reaches its SkillPack tools through, on loopback. An
// operator server sharing that name would shadow it, sending the agent's tool
// calls somewhere with no policy check in between. Same reasoning as
// reservedVolumeNames and builtinToolNames above: a collision the runtime would
// resolve silently is refused up front.
//
// Checked here against the backing Agent rather than the AgentRun, because that
// is where mcpServers are declared; the controller repeats it on the resolved
// list for clusters running without this webhook.
func validateReservedMCPServerNames(mcpServers []sympoziumv1alpha1.MCPServerRef) error {
	for _, srv := range mcpServers {
		if sympoziumv1alpha1.IsReservedName(srv.Name) {
			return fmt.Errorf(
				"mcpServer %q uses the reserved %q name prefix, which Sympozium uses for the servers it injects itself; rename it",
				srv.Name, sympoziumv1alpha1.ReservedNamePrefix)
		}
	}
	return nil
}

func validateTaskModeBackend(run *sympoziumv1alpha1.AgentRun) error {
	if run == nil || run.Spec.Backend != "celln" {
		return nil
	}
	if !taskmodes.ReplacesAgentContainer(run.Spec.Task) {
		return nil
	}
	return fmt.Errorf("task.mode %q replaces the agent container, which backend: celln never creates; use the default job backend (or agentSandbox) for this mode",
		run.Spec.Task.GetMode())
}

func (pe *PolicyEnforcer) validateSubagentDepth(run *sympoziumv1alpha1.AgentRun, policy *sympoziumv1alpha1.SympoziumPolicy) error {
	if policy.Spec.SubagentPolicy == nil || run.Spec.Parent == nil {
		return nil
	}

	if policy.Spec.SubagentPolicy.MaxDepth > 0 && run.Spec.Parent.SpawnDepth >= policy.Spec.SubagentPolicy.MaxDepth {
		return fmt.Errorf("sub-agent depth %d exceeds maximum %d",
			run.Spec.Parent.SpawnDepth, policy.Spec.SubagentPolicy.MaxDepth)
	}

	return nil
}

func (pe *PolicyEnforcer) validateToolPolicy(run *sympoziumv1alpha1.AgentRun, policy *sympoziumv1alpha1.SympoziumPolicy) error {
	if run.Spec.ToolPolicy == nil || policy.Spec.ToolGating == nil {
		return nil
	}

	// Check that allowed tools in the run spec don't conflict with policy denied tools
	for _, rule := range policy.Spec.ToolGating.Rules {
		if rule.Action == "deny" {
			for _, allowed := range run.Spec.ToolPolicy.Allow {
				if allowed == rule.Tool {
					return fmt.Errorf("tool %q is denied by policy", rule.Tool)
				}
			}
		}
	}

	return nil
}

func (pe *PolicyEnforcer) validateAgentSandbox(run *sympoziumv1alpha1.AgentRun, policy *sympoziumv1alpha1.SympoziumPolicy) error {
	agentSandboxEnabled := run.Spec.AgentSandbox != nil && run.Spec.AgentSandbox.Enabled
	sidecarSandboxEnabled := run.Spec.Sandbox != nil && run.Spec.Sandbox.Enabled

	// Mutual exclusivity: cannot use both sandbox modes.
	if agentSandboxEnabled && sidecarSandboxEnabled {
		return fmt.Errorf("sandbox.enabled and agentSandbox.enabled are mutually exclusive")
	}

	// Agent Sandbox + server mode not yet supported.
	if agentSandboxEnabled && run.Spec.Mode == "server" {
		return fmt.Errorf("agentSandbox is not supported with mode=server")
	}

	// Policy enforcement: agent-sandbox required.
	if policy.Spec.SandboxPolicy != nil &&
		policy.Spec.SandboxPolicy.AgentSandboxPolicy != nil &&
		policy.Spec.SandboxPolicy.AgentSandboxPolicy.Required {
		if !agentSandboxEnabled {
			return fmt.Errorf("agent-sandbox mode is required by policy")
		}
	}

	// Validate runtime class against allowed list.
	if agentSandboxEnabled &&
		policy.Spec.SandboxPolicy != nil &&
		policy.Spec.SandboxPolicy.AgentSandboxPolicy != nil {
		asp := policy.Spec.SandboxPolicy.AgentSandboxPolicy
		if len(asp.AllowedRuntimeClasses) > 0 && run.Spec.AgentSandbox.RuntimeClass != "" {
			allowed := false
			for _, rc := range asp.AllowedRuntimeClasses {
				if rc == run.Spec.AgentSandbox.RuntimeClass {
					allowed = true
					break
				}
			}
			if !allowed {
				return fmt.Errorf("runtime class %q is not allowed by policy (allowed: %v)",
					run.Spec.AgentSandbox.RuntimeClass, asp.AllowedRuntimeClasses)
			}
		}
	}

	return nil
}

func (pe *PolicyEnforcer) validateFeatureGates(run *sympoziumv1alpha1.AgentRun, policy *sympoziumv1alpha1.SympoziumPolicy) error {
	if policy.Spec.FeatureGates == nil {
		return nil
	}

	// Check sandbox feature gate
	if run.Spec.Sandbox != nil && run.Spec.Sandbox.Enabled {
		if enabled, exists := policy.Spec.FeatureGates["code-execution"]; exists && !enabled {
			return fmt.Errorf("feature gate 'code-execution' is disabled by policy")
		}
	}

	// Check sub-agents feature gate
	if run.Spec.Parent != nil {
		if enabled, exists := policy.Spec.FeatureGates["sub-agents"]; exists && !enabled {
			return fmt.Errorf("feature gate 'sub-agents' is disabled by policy")
		}
	}

	return nil
}

// deniedEnvVarKeys lists environment variable names that cannot be set via
// agentRun.spec.env to prevent injection attacks.
var deniedEnvVarKeys = map[string]bool{
	"PATH":            true,
	"LD_PRELOAD":      true,
	"LD_LIBRARY_PATH": true,
	"HOME":            true,
	"SHELL":           true,
	"USER":            true,
	"HOSTNAME":        true,
}

func (pe *PolicyEnforcer) validateEnvVars(run *sympoziumv1alpha1.AgentRun) error {
	for key := range run.Spec.Env {
		if deniedEnvVarKeys[key] {
			return fmt.Errorf("environment variable %q is not allowed: overriding system variables is denied", key)
		}
	}
	if run.Spec.Lifecycle != nil {
		for _, h := range run.Spec.Lifecycle.PreRun {
			if err := validateHookEnv("preRun", h.Name, h.Env); err != nil {
				return err
			}
		}
		for _, h := range run.Spec.Lifecycle.PostRun {
			if err := validateHookEnv("postRun", h.Name, h.Env); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateHookEnv enforces that each lifecycle hook env entry uses either a
// literal value or a secretKeyRef source (never both), and that a secretKeyRef
// names both a Secret and a key.
func validateHookEnv(phase, hook string, env []sympoziumv1alpha1.EnvVar) error {
	for _, e := range env {
		if e.ValueFrom == nil {
			continue
		}
		if e.Value != "" {
			return fmt.Errorf("%s hook %q env %q: value and valueFrom are mutually exclusive", phase, hook, e.Name)
		}
		ref := e.ValueFrom.SecretKeyRef
		if ref == nil {
			return fmt.Errorf("%s hook %q env %q: valueFrom must set secretKeyRef", phase, hook, e.Name)
		}
		if ref.Name == "" || ref.Key == "" {
			return fmt.Errorf("%s hook %q env %q: secretKeyRef requires both name and key", phase, hook, e.Name)
		}
	}
	return nil
}

func (pe *PolicyEnforcer) validateImagePolicy(run *sympoziumv1alpha1.AgentRun, policy *sympoziumv1alpha1.SympoziumPolicy) error {
	if policy.Spec.ImagePolicy == nil || len(policy.Spec.ImagePolicy.AllowedRegistries) == 0 {
		return nil
	}

	var images []string

	// Collect images from lifecycle hooks
	if run.Spec.Lifecycle != nil {
		for _, h := range run.Spec.Lifecycle.PreRun {
			images = append(images, h.Image)
		}
		for _, h := range run.Spec.Lifecycle.PostRun {
			images = append(images, h.Image)
		}
	}

	// Collect sandbox image override
	if run.Spec.Sandbox != nil && run.Spec.Sandbox.Image != "" {
		images = append(images, run.Spec.Sandbox.Image)
	}

	// Collect the harness image for a `mode: harness` task on the custom
	// backend. That image becomes the pod's primary process, so the
	// allowedRegistries list is the control an operator uses to bound which
	// external harnesses may run in the cluster.
	if img := taskmodes.HarnessImage(run.Spec.Task); img != "" {
		images = append(images, img)
	}

	for _, img := range images {
		if !policy.Spec.ImagePolicy.Allows(img) {
			return fmt.Errorf("image %q is not from an allowed registry (allowed: %v)",
				img, policy.Spec.ImagePolicy.AllowedRegistries)
		}
	}
	return nil
}

func (pe *PolicyEnforcer) validateLifecycleRBAC(run *sympoziumv1alpha1.AgentRun, policy *sympoziumv1alpha1.SympoziumPolicy) error {
	if run.Spec.Lifecycle == nil || len(run.Spec.Lifecycle.RBAC) == 0 {
		return nil
	}
	if policy.Spec.LifecyclePolicy == nil || len(policy.Spec.LifecyclePolicy.DeniedResources) == 0 {
		return nil
	}

	denied := make(map[string]bool)
	for _, r := range policy.Spec.LifecyclePolicy.DeniedResources {
		denied[r] = true
	}

	for _, rule := range run.Spec.Lifecycle.RBAC {
		for _, res := range rule.Resources {
			if denied[res] {
				return fmt.Errorf("lifecycle RBAC requests access to denied resource %q", res)
			}
		}
	}
	return nil
}

// validateRetryPolicy bounds how many gate-driven attempts a run may request.
// A gate that keeps returning "retry" burns tokens silently, so the operator's
// ceiling is enforced here rather than trusted to the run's own spec.
func (pe *PolicyEnforcer) validateRetryPolicy(run *sympoziumv1alpha1.AgentRun, policy *sympoziumv1alpha1.SympoziumPolicy) error {
	if run.Spec.Lifecycle == nil || run.Spec.Lifecycle.Retry == nil {
		return nil
	}
	if policy.Spec.LifecyclePolicy == nil || policy.Spec.LifecyclePolicy.MaxRetryAttempts <= 0 {
		return nil
	}

	requested := run.Spec.Lifecycle.Retry.MaxAttempts
	if requested > policy.Spec.LifecyclePolicy.MaxRetryAttempts {
		return fmt.Errorf("lifecycle retry maxAttempts %d exceeds maximum %d",
			requested, policy.Spec.LifecyclePolicy.MaxRetryAttempts)
	}
	return nil
}

// MutatingPolicyEnforcer is a mutating webhook that injects defaults based on SympoziumPolicy.
type MutatingPolicyEnforcer struct {
	Client  client.Client
	Log     logr.Logger
	Decoder admission.Decoder
}

// Handle mutates AgentRun resources to enforce policy defaults.
func (mpe *MutatingPolicyEnforcer) Handle(ctx context.Context, req admission.Request) admission.Response {
	run := &sympoziumv1alpha1.AgentRun{}
	if err := mpe.Decoder.Decode(req, run); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	// Look up the owning Agent
	var instance sympoziumv1alpha1.Agent
	if err := mpe.Client.Get(ctx, types.NamespacedName{
		Name:      run.Spec.AgentRef,
		Namespace: run.Namespace,
	}, &instance); err != nil {
		return admission.Allowed("instance not found, skipping mutation")
	}

	if instance.Spec.PolicyRef == "" {
		return admission.Allowed("no policy")
	}

	var policy sympoziumv1alpha1.SympoziumPolicy
	if err := mpe.Client.Get(ctx, types.NamespacedName{
		Name:      instance.Spec.PolicyRef,
		Namespace: run.Namespace,
	}, &policy); err != nil {
		return admission.Allowed("policy not found, skipping mutation")
	}

	modified := false

	// Inject sandbox defaults
	if policy.Spec.SandboxPolicy != nil && policy.Spec.SandboxPolicy.Required {
		if run.Spec.Sandbox == nil {
			run.Spec.Sandbox = &sympoziumv1alpha1.AgentRunSandboxSpec{
				Enabled: true,
			}
			modified = true
		}
		if policy.Spec.SandboxPolicy.DefaultImage != "" && run.Spec.Sandbox.Image == "" {
			run.Spec.Sandbox.Image = policy.Spec.SandboxPolicy.DefaultImage
			modified = true
		}
	}

	// Inject seccomp profile default from policy
	if policy.Spec.SandboxPolicy != nil && policy.Spec.SandboxPolicy.SeccompProfile != nil {
		if run.Spec.Sandbox == nil {
			run.Spec.Sandbox = &sympoziumv1alpha1.AgentRunSandboxSpec{}
			modified = true
		}
		if run.Spec.Sandbox.SecurityContext == nil {
			run.Spec.Sandbox.SecurityContext = &sympoziumv1alpha1.SandboxSecurityContext{}
			modified = true
		}
		if run.Spec.Sandbox.SecurityContext.SeccompProfile == nil {
			run.Spec.Sandbox.SecurityContext.SeccompProfile = &sympoziumv1alpha1.SeccompProfileSpec{
				Type: policy.Spec.SandboxPolicy.SeccompProfile.Type,
			}
			modified = true
		}
	}

	// Inject tool policy defaults from SympoziumPolicy
	if policy.Spec.ToolGating != nil && run.Spec.ToolPolicy == nil {
		tp := &sympoziumv1alpha1.ToolPolicySpec{}
		for _, rule := range policy.Spec.ToolGating.Rules {
			switch rule.Action {
			case "allow":
				tp.Allow = append(tp.Allow, rule.Tool)
			case "deny":
				tp.Deny = append(tp.Deny, rule.Tool)
			}
		}
		run.Spec.ToolPolicy = tp
		modified = true
	}

	// Inject network isolation labels (used by NetworkPolicy)
	if run.Labels == nil {
		run.Labels = make(map[string]string)
	}
	if _, exists := run.Labels["sympozium.ai/role"]; !exists {
		run.Labels["sympozium.ai/role"] = "agent"
		modified = true
	}
	if run.Spec.Sandbox != nil && run.Spec.Sandbox.Enabled {
		run.Labels["sympozium.ai/sandbox"] = "true"
		modified = true
	}

	// Inject agent-sandbox defaults from policy.
	if policy.Spec.SandboxPolicy != nil && policy.Spec.SandboxPolicy.AgentSandboxPolicy != nil {
		asp := policy.Spec.SandboxPolicy.AgentSandboxPolicy

		// If policy requires agent-sandbox and it's not set, inject it.
		if asp.Required && (run.Spec.AgentSandbox == nil || !run.Spec.AgentSandbox.Enabled) {
			run.Spec.AgentSandbox = &sympoziumv1alpha1.AgentSandboxSpec{
				Enabled: true,
			}
			modified = true
		}

		// Inject default runtime class.
		if run.Spec.AgentSandbox != nil && run.Spec.AgentSandbox.Enabled {
			if asp.DefaultRuntimeClass != "" && run.Spec.AgentSandbox.RuntimeClass == "" {
				run.Spec.AgentSandbox.RuntimeClass = asp.DefaultRuntimeClass
				modified = true
			}
			run.Labels["sympozium.ai/agent-sandbox"] = "true"
			modified = true
		}
	}

	// Disable service account token automount via annotation
	if run.Annotations == nil {
		run.Annotations = make(map[string]string)
	}
	if _, exists := run.Annotations["sympozium.ai/disable-sa-token"]; !exists {
		run.Annotations["sympozium.ai/disable-sa-token"] = "true"
		modified = true
	}

	if !modified {
		return admission.Allowed("no mutations needed")
	}

	// Create the JSON patch
	marshaledRun, err := json.Marshal(run)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}

	return admission.PatchResponseFromRaw(req.Object.Raw, marshaledRun)
}

// BuildAgentPodSecurityContext returns a restricted SecurityContext for agent pods.
func BuildAgentPodSecurityContext() *corev1.SecurityContext {
	falseBool := false
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &falseBool,
		ReadOnlyRootFilesystem:   &falseBool,
		RunAsNonRoot:             boolPtr(true),
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

func boolPtr(b bool) *bool {
	return &b
}

// ModelValidator is a validating webhook for Model CRs.
type ModelValidator struct {
	Log     logr.Logger
	Decoder admission.Decoder
}

// Handle validates Model creation/updates.
func (mv *ModelValidator) Handle(_ context.Context, req admission.Request) admission.Response {
	model := &sympoziumv1alpha1.Model{}
	if err := mv.Decoder.Decode(req, model); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}

	if err := mv.validateModelSource(model); err != nil {
		return admission.Denied(err.Error())
	}

	return admission.Allowed("model validated")
}

func (mv *ModelValidator) validateModelSource(model *sympoziumv1alpha1.Model) error {
	src := model.Spec.Source

	// At least one source must be specified
	if src.URL == "" && src.ModelID == "" {
		return fmt.Errorf("model source must specify either url or modelID")
	}

	// Validate URL scheme for URL-based sources
	if src.URL != "" {
		if !strings.HasPrefix(src.URL, "https://") && !strings.HasPrefix(src.URL, "http://") {
			return fmt.Errorf("model source URL must use http:// or https:// scheme")
		}
	}

	// Validate SHA256 format if provided
	if src.SHA256 != "" {
		if len(src.SHA256) != 64 {
			return fmt.Errorf("sha256 checksum must be exactly 64 hex characters, got %d", len(src.SHA256))
		}
		for _, c := range src.SHA256 {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return fmt.Errorf("sha256 checksum contains invalid character: %c", c)
			}
		}
	}

	return nil
}
