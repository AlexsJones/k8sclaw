package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentRunSpec defines the desired state of an AgentRun.
// Each agent invocation (including sub-agents) produces an AgentRun CR.
type AgentRunSpec struct {
	// AgentRef is the name of the Agent this run belongs to.
	AgentRef string `json:"agentRef"`

	// AgentID identifies the agent configuration to use.
	AgentID string `json:"agentId"`

	// SessionKey is the unique session identifier for this run.
	SessionKey string `json:"sessionKey"`

	// Env defines custom environment variables to pass to the agent container.
	// +optional
	Env map[string]string `json:"env,omitempty"`

	// Parent contains parent run information for sub-agents.
	// +optional
	Parent *ParentRunRef `json:"parent,omitempty"`

	// Task is the polymorphic task description for the agent. It accepts
	// either a string (legacy Path A: the prompt passed to the LLM via
	// the TASK env var) or an object describing an orchestration mode
	// (e.g. {mode: "sidecar-driven", tool: "...", parameters: {...}}).
	// The controller dispatches by the mode field for object form.
	//
	// String form preserves backward compatibility with every
	// pre-existing AgentRun. Object form lets external contributors add
	// new orchestration modes (sidecar-driven today, others tomorrow)
	// by registering a TaskModeHandler — no changes to the central
	// controller logic required.
	//
	// The CRD schema is intentionally typeless (no `type:` is emitted)
	// to accept either a string (legacy Path A) or an object describing
	// an orchestration mode (Path B). The combination of
	// `+kubebuilder:validation:Schemaless` (drops the auto-generated
	// `type: object` plus the unused `properties` block) and
	// `+kubebuilder:validation:XPreserveUnknownFields` (preserves unknown
	// fields on the apiserver side) reproduces the polymorphic shape
	// the field actually has — verified via `--dry-run=server` against
	// a real apiserver that string-form tasks were previously rejected
	// by the schema with "spec.task: must be of type object".
	//
	// See: PR #302 review (issuecomment 5033007953).
	// +kubebuilder:validation:Schemaless
	// +kubebuilder:validation:XPreserveUnknownFields
	// +kubebuilder:printerColumns:name="Task",type="string",JSONPath=".spec.task"
	Task *TaskSpec `json:"task,omitempty"`

	// SystemPrompt is the system prompt for the agent.
	// +optional
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// Model specifies the LLM configuration for this run.
	Model ModelSpec `json:"model"`

	// Sandbox defines sandbox configuration for this run.
	// +optional
	Sandbox *AgentRunSandboxSpec `json:"sandbox,omitempty"`

	// AgentSandbox configures the Kubernetes Agent Sandbox (CRD) execution backend.
	// When enabled, the controller creates a Sandbox CR (kubernetes-sigs/agent-sandbox)
	// instead of a Job, providing gVisor/Kata kernel-level isolation and warm pool support.
	// Mutually exclusive with sandbox.enabled and mode=server.
	// +optional
	AgentSandbox *AgentSandboxSpec `json:"agentSandbox,omitempty"`

	// Skills to mount into the agent pod.
	// +optional
	Skills []SkillRef `json:"skills,omitempty"`

	// ToolPolicy defines which tools this agent is allowed to use.
	// +optional
	ToolPolicy *ToolPolicySpec `json:"toolPolicy,omitempty"`

	// Timeout is the maximum duration for this agent run.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Cleanup policy: "delete" to remove pod after completion, "keep" for debugging.
	// +kubebuilder:default="delete"
	// +kubebuilder:validation:Enum=delete;keep
	Cleanup string `json:"cleanup,omitempty"`

	// Mode: "task" (default, Job) or "server" (Deployment, long-running).
	// Auto-set to "server" when any skill has RequiresServer=true.
	// +kubebuilder:default="task"
	// +kubebuilder:validation:Enum=task;server
	// +optional
	Mode string `json:"mode,omitempty"`

	// DryRun skips the LLM call and produces a synthetic result, allowing
	// pipeline execution paths to be traced without burning tokens.
	// The flag is automatically propagated to sequential successors.
	// +optional
	DryRun bool `json:"dryRun,omitempty"`

	// CanaryMode runs built-in health checks instead of the LLM conversation
	// loop. The agent executes deterministic platform checks (API server,
	// cluster info, k8s resources) and one minimal LLM call to verify
	// provider connectivity, then outputs a structured JSON result.
	// +optional
	CanaryMode bool `json:"canaryMode,omitempty"`

	// ImagePullSecrets are secrets to use when pulling container images.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Lifecycle defines pre and post run hooks for this agent run.
	// PreRun hooks execute as init containers before the agent starts.
	// PostRun hooks execute in a follow-up Job after the agent completes.
	// +optional
	Lifecycle *LifecycleHooks `json:"lifecycle,omitempty"`

	// UseContext controls whether the agent-runner preserves conversation
	// history between LLM calls. Set to false to force every prompt — including
	// those issued via the sidecar-initiated prompt channel — to be answered
	// in isolation. Default (nil) is true; matches the historical behaviour for
	// runs that pre-date this field.
	// Settable only on this CR; the sidecar cannot override it.
	// +optional
	// +kubebuilder:default=true
	UseContext *bool `json:"useContext,omitempty"`

	// Volumes are additional pod volumes to attach to the agent pod.
	// Typically populated from Agent.Spec.Volumes by the controller,
	// but may also be set directly on an AgentRun. Useful for mounting
	// secrets via CSI drivers (Vault CSI, Secrets Store CSI), PVCs,
	// or arbitrary projected volumes. Names must not collide with
	// reserved volumes: workspace, ipc, skills, tmp, memory, mcp-config.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// VolumeMounts are additional volume mounts applied to the main
	// agent container. Names must reference entries in Volumes (or any
	// other volume present on the pod).
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
}

// ParentRunRef links a sub-agent to its parent.
type ParentRunRef struct {
	// RunName is the name of the parent AgentRun.
	RunName string `json:"runName"`

	// SessionKey is the session key of the parent.
	SessionKey string `json:"sessionKey"`

	// SpawnDepth is how many levels deep this sub-agent is.
	SpawnDepth int `json:"spawnDepth"`
}

// ModelSpec defines which LLM to use.
type ModelSpec struct {
	// Provider is the AI provider (openai, anthropic, azure-openai, github-copilot, ollama, etc.).
	Provider string `json:"provider"`

	// Model is the model identifier.
	Model string `json:"model"`

	// BaseURL overrides the provider's default API endpoint.
	// Use this for OpenAI-compatible providers (GitHub Copilot, Azure OpenAI,
	// Ollama, vLLM, LMStudio, etc.).
	// Examples:
	//   GitHub Copilot: https://api.githubcopilot.com
	//   Azure OpenAI:   https://<resource>.openai.azure.com/openai/deployments/<deployment>
	//   Ollama:         http://ollama.default.svc:11434/v1
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// Thinking mode (off, low, medium, high).
	// +optional
	Thinking string `json:"thinking,omitempty"`

	// AuthSecretRef references the secret containing the API key.
	AuthSecretRef string `json:"authSecretRef"`

	// ProviderHeaders are additional HTTP headers sent with every LLM provider request.
	// +optional
	ProviderHeaders map[string]string `json:"providerHeaders,omitempty"`

	// ProviderHeadersSecretRef references a Kubernetes Secret whose data keys are
	// injected as provider request headers. Values from the secret override inline
	// ProviderHeaders on key collision.
	// +optional
	ProviderHeadersSecretRef string `json:"providerHeadersSecretRef,omitempty"`

	// ModelRef references a Model CR by name for cluster-local inference.
	// When set, provider, baseURL, and authSecretRef are auto-resolved from the Model's status.
	// +optional
	ModelRef string `json:"modelRef,omitempty"`

	// NodeSelector constrains agent pods to nodes with matching labels.
	// Inherited from AgentConfig at AgentRun creation time.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
}

// AgentRunSandboxSpec defines sandbox settings for an individual agent run.
type AgentRunSandboxSpec struct {
	// Enabled indicates whether sandboxing is enabled.
	Enabled bool `json:"enabled"`

	// Image is the sandbox container image.
	// +optional
	Image string `json:"image,omitempty"`

	// ImagePullPolicy overrides the sandbox container image pull policy.
	// Valid values are "Always", "IfNotPresent", or "Never". Defaults to
	// "IfNotPresent" when unset.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// SecurityContext for the sandbox container.
	// +optional
	SecurityContext *SandboxSecurityContext `json:"securityContext,omitempty"`

	// Resources for the sandbox container.
	// +optional
	Resources *ResourceSpec `json:"resources,omitempty"`
}

// AgentSandboxSpec configures the Kubernetes Agent Sandbox (CRD) execution backend.
// When enabled, the controller creates a Sandbox CR (from kubernetes-sigs/agent-sandbox)
// instead of a batchv1.Job. This provides gVisor/Kata kernel-level isolation, warm pools
// for cold-start elimination, and suspend/resume lifecycle management.
type AgentSandboxSpec struct {
	// Enabled activates Agent Sandbox mode for this run.
	Enabled bool `json:"enabled"`

	// RuntimeClass selects the sandbox runtime (e.g., "gvisor", "kata").
	// Maps to the Sandbox CR's runtimeClassName field.
	// +optional
	RuntimeClass string `json:"runtimeClass,omitempty"`

	// WarmPoolRef references a SandboxWarmPool to claim a pre-warmed sandbox from.
	// When set, a SandboxClaim is created instead of a bare Sandbox CR.
	// +optional
	WarmPoolRef string `json:"warmPoolRef,omitempty"`

	// Resources for the sandbox container.
	// +optional
	Resources *ResourceSpec `json:"resources,omitempty"`
}

// SandboxSecurityContext defines security settings for the sandbox.
type SandboxSecurityContext struct {
	// ReadOnlyRootFilesystem makes the root filesystem read-only.
	ReadOnlyRootFilesystem bool `json:"readOnlyRootFilesystem,omitempty"`

	// RunAsNonRoot ensures the container runs as a non-root user.
	RunAsNonRoot bool `json:"runAsNonRoot,omitempty"`

	// Capabilities to add or drop.
	// +optional
	Capabilities *CapabilitiesSpec `json:"capabilities,omitempty"`

	// SeccompProfile defines the seccomp profile.
	// +optional
	SeccompProfile *SeccompProfileSpec `json:"seccompProfile,omitempty"`
}

// CapabilitiesSpec defines Linux capabilities.
type CapabilitiesSpec struct {
	// Drop is a list of capabilities to drop.
	Drop []string `json:"drop,omitempty"`
}

// SeccompProfileSpec defines seccomp settings.
type SeccompProfileSpec struct {
	// Type is the seccomp profile type.
	Type string `json:"type"`
}

// ResourceSpec defines resource requests and limits.
type ResourceSpec struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

// ToolPolicySpec defines which tools an agent may use.
type ToolPolicySpec struct {
	// Allow lists explicitly allowed tools.
	Allow []string `json:"allow,omitempty"`

	// Deny lists explicitly denied tools.
	Deny []string `json:"deny,omitempty"`
}

// AgentRunPhase represents the lifecycle phase of an AgentRun.
type AgentRunPhase string

const (
	AgentRunPhasePending          AgentRunPhase = "Pending"
	AgentRunPhaseRunning          AgentRunPhase = "Running"
	AgentRunPhaseServing          AgentRunPhase = "Serving"
	AgentRunPhasePostRunning      AgentRunPhase = "PostRunning"
	AgentRunPhaseAwaitingDelegate AgentRunPhase = "AwaitingDelegate"
	AgentRunPhaseSucceeded        AgentRunPhase = "Succeeded"
	AgentRunPhaseFailed           AgentRunPhase = "Failed"
	// AgentRunPhaseSkipped is a terminal phase for runs a preRun lifecycle
	// hook skipped (no work to do). The agent never made an LLM call, so the
	// run consumed no tokens; it is distinct from Succeeded and Failed.
	AgentRunPhaseSkipped AgentRunPhase = "Skipped"
)

// AgentRunStatus defines the observed state of AgentRun.
type AgentRunStatus struct {
	// Phase is the current phase (Pending, Running, Succeeded, Failed, Skipped).
	// +optional
	Phase AgentRunPhase `json:"phase,omitempty"`

	// PodName is the name of the pod running this agent.
	// +optional
	PodName string `json:"podName,omitempty"`

	// JobName is the name of the Job created for this run.
	// +optional
	JobName string `json:"jobName,omitempty"`

	// SandboxName is the name of the Sandbox CR created for this run (agent-sandbox mode).
	// +optional
	SandboxName string `json:"sandboxName,omitempty"`

	// SandboxClaimName is the name of the SandboxClaim when using warm pools.
	// +optional
	SandboxClaimName string `json:"sandboxClaimName,omitempty"`

	// StartedAt is when the agent run started.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// CompletedAt is when the agent run completed.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// Result is the agent's final reply (populated on success).
	// +optional
	Result string `json:"result,omitempty"`

	// Error is the error message (populated on failure).
	// +optional
	Error string `json:"error,omitempty"`

	// ExitCode of the agent container.
	// +optional
	ExitCode *int32 `json:"exitCode,omitempty"`

	// TokenUsage contains LLM token counts and timing for this run.
	// +optional
	TokenUsage *TokenUsage `json:"tokenUsage,omitempty"`

	// CostEstimate is the estimated dollar cost of this run, derived from
	// tokenUsage and the cluster price table at completion time. It is an
	// ESTIMATE, not billing data: retried runs count only the final attempt
	// and failed runs report no usage. Absent (never zero) when the provider
	// is local/self-hosted, the run uses modelRef, or no price-table entry
	// matches.
	// +optional
	CostEstimate *CostEstimate `json:"costEstimate,omitempty"`

	// DeploymentName is the name of the Deployment created for server-mode runs.
	// +optional
	DeploymentName string `json:"deploymentName,omitempty"`

	// ServiceName is the name of the Service created for server-mode runs.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// TraceID is the OTel trace ID for this agent run, if instrumentation is enabled.
	// Enables operators to look up the full distributed trace in their backend.
	// Set by the controller when creating the Job.
	// +optional
	TraceID string `json:"traceID,omitempty"`

	// PostRunJobName is the name of the Job created for postRun lifecycle hooks.
	// +optional
	PostRunJobName string `json:"postRunJobName,omitempty"`

	// GateVerdict records the outcome of the response gate hook, if configured.
	// One of: approved, rejected, rewritten, timeout, error, allowed-by-default.
	// Empty when no gate hook is configured.
	// +optional
	GateVerdict string `json:"gateVerdict,omitempty"`

	// Delegates tracks in-flight persona delegations for this run.
	// Populated when the run enters AwaitingDelegate phase.
	// +optional
	Delegates []DelegateStatus `json:"delegates,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// DelegateStatus tracks an in-flight delegation to another persona or ad-hoc sub-agent.
type DelegateStatus struct {
	// ChildRunName is the name of the spawned AgentRun.
	ChildRunName string `json:"childRunName"`

	// TargetPersona is the persona name that was delegated to.
	// Empty for ad-hoc sub-agent spawns (spawn_subagents tool).
	// +optional
	TargetPersona string `json:"targetPersona,omitempty"`

	// BatchID correlates children that belong to the same spawn_subagents batch.
	// Empty for persona-based delegations.
	// +optional
	BatchID string `json:"batchId,omitempty"`

	// TaskID is the caller-assigned identifier for this child within a batch.
	// Empty for persona-based delegations.
	// +optional
	TaskID string `json:"taskId,omitempty"`

	// Phase is the child run's current phase.
	// +optional
	Phase AgentRunPhase `json:"phase,omitempty"`

	// Result is populated when the child run completes successfully.
	// +optional
	Result string `json:"result,omitempty"`

	// Error is populated when the child run fails.
	// +optional
	Error string `json:"error,omitempty"`
}

// TokenUsage tracks LLM token consumption and timing for an AgentRun.
type TokenUsage struct {
	// InputTokens is the total number of prompt/input tokens sent to the LLM.
	InputTokens int `json:"inputTokens"`

	// OutputTokens is the total number of completion/output tokens received.
	OutputTokens int `json:"outputTokens"`

	// TotalTokens is InputTokens + OutputTokens.
	TotalTokens int `json:"totalTokens"`

	// ToolCalls is the number of tool invocations during this run.
	ToolCalls int `json:"toolCalls"`

	// DurationMs is the wall-clock time of the LLM interaction in milliseconds.
	DurationMs int64 `json:"durationMs"`
}

// CostEstimate is an estimated dollar cost in integer micro-USD (1e-6 USD).
// Floats are forbidden in CRD types by Kubernetes API conventions.
type CostEstimate struct {
	// AmountMicro is the total estimated cost in micro-USD.
	AmountMicro int64 `json:"amountMicro"`

	// InputAmountMicro is the input-token share of the total.
	// +optional
	InputAmountMicro int64 `json:"inputAmountMicro,omitempty"`

	// OutputAmountMicro is the output-token share of the total.
	// +optional
	OutputAmountMicro int64 `json:"outputAmountMicro,omitempty"`

	// Currency is always "USD" in v1.
	Currency string `json:"currency"`

	// Source identifies the price table used. Persisted estimates are always
	// "defaultTable"; "simulated" appears only in apiserver responses and is
	// never written to status.
	// +kubebuilder:validation:Enum=defaultTable;simulated
	Source string `json:"source"`

	// PriceKey is the matched table entry ("provider/matchPrefix") for audit.
	// +optional
	PriceKey string `json:"priceKey,omitempty"`

	// EstimatedAt records when the estimate was frozen. Estimates are never
	// recomputed when the price table changes.
	// +optional
	EstimatedAt *metav1.Time `json:"estimatedAt,omitempty"`
}

// LifecycleHookContainer defines a container to run as a lifecycle hook.
type LifecycleHookContainer struct {
	// Name is the container name.
	Name string `json:"name"`

	// Image is the container image.
	Image string `json:"image"`

	// ImagePullPolicy overrides the container image pull policy. Valid values are
	// "Always", "IfNotPresent", or "Never". Defaults to "IfNotPresent" when unset.
	// +kubebuilder:validation:Enum=Always;IfNotPresent;Never
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// Command overrides the container entrypoint.
	// +optional
	Command []string `json:"command,omitempty"`

	// Args are arguments to the entrypoint.
	// +optional
	Args []string `json:"args,omitempty"`

	// Env is a list of environment variables for this hook container.
	// Each entry may carry a literal value or source it from a Secret key via
	// valueFrom.secretKeyRef, allowing hooks to authenticate to private
	// sources (e.g. a GitHub PAT) without embedding plaintext credentials.
	// +optional
	Env []EnvVar `json:"env,omitempty"`

	// Timeout is the maximum duration for this hook container.
	// Defaults to 5 minutes.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// Gate makes this hook a response gate. When true, agent output is held
	// until this hook approves, rejects, or rewrites it by patching the
	// annotation sympozium.ai/gate-verdict on the AgentRun CR.
	// Only valid on PostRun hooks. At most one PostRun hook may set gate: true.
	// +optional
	Gate bool `json:"gate,omitempty"`
}

// LifecycleHooks defines pre and post run hooks for an agent.
type LifecycleHooks struct {
	// PreRun containers execute as init containers before the agent starts.
	// They have access to /workspace, /ipc, /tmp and standard env vars.
	// +optional
	PreRun []LifecycleHookContainer `json:"preRun,omitempty"`

	// PostRun containers execute in a follow-up Job after the agent completes.
	// They have access to /workspace (with agent output) and receive
	// AGENT_EXIT_CODE and AGENT_RESULT as additional env vars.
	// PostRun failures are recorded as Conditions but do not change the
	// agent's final phase (best-effort semantics).
	// +optional
	PostRun []LifecycleHookContainer `json:"postRun,omitempty"`

	// RBAC defines namespace-scoped Kubernetes RBAC rules to create for
	// lifecycle hook containers. A Role and RoleBinding are provisioned
	// in the agent namespace, bound to the "sympozium-agent" ServiceAccount.
	// This allows hooks to interact with Kubernetes resources (e.g., create
	// or delete ConfigMaps, read Secrets).
	// +optional
	RBAC []RBACRule `json:"rbac,omitempty"`

	// GateDefault controls what happens when a gate hook fails or times out.
	// "allow" passes the original result through unchanged; "block" replaces
	// it with a policy error message. Defaults to "block".
	// +kubebuilder:validation:Enum=allow;block
	// +kubebuilder:default="block"
	// +optional
	GateDefault string `json:"gateDefault,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Agent",type="string",JSONPath=".spec.agentRef"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Pod",type="string",JSONPath=".status.podName"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// AgentRun is the Schema for the agentruns API.
// Each agent invocation produces an AgentRun CR that the orchestrator
// reconciles into a Kubernetes Job.
type AgentRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentRunSpec   `json:"spec,omitempty"`
	Status AgentRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentRunList contains a list of AgentRun.
type AgentRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRun{}, &AgentRunList{})
}
