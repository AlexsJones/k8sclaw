package v1alpha1

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentSpec defines the desired state of a Agent.
// Each user or tenant gets a Agent that declares their desired channels,
// agents, and policy bindings.
type AgentSpec struct {
	// DisplayName is the human-readable name for this agent. When set, it is
	// used for per-message sender attribution in channels (e.g. the Slack
	// username a shared bot posts under) so a multi-agent Ensemble shows which
	// agent is replying. Falls back to the Agent's metadata.name when empty.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// Channels this instance connects to.
	// +optional
	Channels []ChannelSpec `json:"channels,omitempty"`

	// Agent configuration.
	Agents AgentsSpec `json:"agents"`

	// Skills to mount (from SkillPack CRDs or ConfigMaps).
	// +optional
	Skills []SkillRef `json:"skills,omitempty"`

	// PolicyRef references the SympoziumPolicy that applies to this instance.
	// +optional
	PolicyRef string `json:"policyRef,omitempty"`

	// AuthRefs references secrets containing AI provider credentials.
	// +optional
	AuthRefs []SecretRef `json:"authRefs,omitempty"`

	// Memory configures persistent memory for this instance.
	// When enabled, a MEMORY.md ConfigMap is managed and mounted into agent pods.
	// +optional
	Memory *MemorySpec `json:"memory,omitempty"`

	// Observability configures OpenTelemetry for agent pods spawned by this instance.
	// When nil, inherits from Helm chart global values.
	// +optional
	Observability *ObservabilitySpec `json:"observability,omitempty"`

	// MCPServers configures remote MCP (Model Context Protocol) servers
	// that agents in this instance can access via the mcp-bridge sidecar.
	// +optional
	MCPServers []MCPServerRef `json:"mcpServers,omitempty"`

	// Deprecated: Use the "web-endpoint" SkillPack in Skills instead.
	// WebEndpoint exposes this agent as an HTTP API (OpenAI-compatible + MCP).
	// When nil or Enabled is false, no web-proxy infrastructure is deployed.
	// +optional
	WebEndpoint *WebEndpointSpec `json:"webEndpoint,omitempty"`

	// ImagePullSecrets are secrets to use when pulling container images.
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// Volumes are additional pod volumes to attach to agent pods spawned
	// by this Agent. Common use cases include CSI driver volumes
	// (e.g. Vault Secrets Store CSI / secrets-store.csi.k8s.io),
	// PersistentVolumeClaims, and projected Secret/ConfigMap volumes.
	// These are appended to the volumes Sympozium manages internally
	// (workspace, ipc, skills, tmp, memory, etc.). Names must not
	// collide with reserved volumes: workspace, ipc, skills, tmp,
	// memory, mcp-config.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// VolumeMounts are additional volume mounts applied to the main
	// agent container. Names must reference entries in Volumes or any
	// other volume defined on the pod. Use Volumes + VolumeMounts
	// together to surface secrets from CSI drivers (e.g. Vault CSI)
	// inside the agent's filesystem.
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
}

// MCPServerRef references a remote MCP server for tool integration.
type MCPServerRef struct {
	// Name is a unique identifier for this MCP server connection.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// URL is the MCP server's Streamable HTTP endpoint. Optional if an MCPServer CR with this name exists.
	// +optional
	URL string `json:"url,omitempty"`

	// ToolsPrefix is prepended to tool names from this server to avoid collisions.
	// Must be unique across all configured servers.
	// +kubebuilder:validation:MinLength=1
	ToolsPrefix string `json:"toolsPrefix"`

	// Timeout is the per-request timeout in seconds for this server.
	// +kubebuilder:default=30
	// +optional
	Timeout int `json:"timeout,omitempty"`

	// AuthSecret references a Kubernetes Secret containing auth credentials.
	// The key specified by AuthKey (default "token") is used as a Bearer token.
	// +optional
	AuthSecret string `json:"authSecret,omitempty"`

	// AuthKey is the key within AuthSecret to use. Defaults to "token".
	// +optional
	AuthKey string `json:"authKey,omitempty"`

	// Headers are additional HTTP headers to send with every request.
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// ToolsAllow lists tool names (without prefix) to expose. If set, only these tools are registered.
	// +optional
	ToolsAllow []string `json:"toolsAllow,omitempty"`

	// ToolsDeny lists tool names (without prefix) to hide. Applied after toolsAllow.
	// +optional
	ToolsDeny []string `json:"toolsDeny,omitempty"`
}

// MemorySpec configures persistent memory for a Agent.
type MemorySpec struct {
	// Enabled indicates whether persistent memory is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// MaxSizeKB caps the memory ConfigMap size in kilobytes.
	// +kubebuilder:default=256
	// +optional
	MaxSizeKB int `json:"maxSizeKB,omitempty"`

	// AutoStore controls whether each successful agent run automatically writes
	// a truncated task/response summary to the memory server (tagged "auto",
	// "agent-run"). Defaults to true. Set to false to disable automatic memory
	// writes while keeping the memory skill and its /store endpoint available
	// for deliberate, curated memory management. Nil is treated as true so
	// existing agents keep their current behaviour.
	// +optional
	AutoStore *bool `json:"autoStore,omitempty"`

	// SystemPrompt is injected into every agent run for this instance
	// to instruct the agent on how to use memory.
	// +optional
	SystemPrompt string `json:"systemPrompt,omitempty"`
}

// ObservabilitySpec configures OpenTelemetry for agent runs.
type ObservabilitySpec struct {
	// Enabled turns OpenTelemetry tracing/metrics on for this instance.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// OTLPEndpoint is the collector endpoint (for example:
	// "otel-collector.observability.svc:4317" for gRPC or
	// "http://otel-collector.observability.svc:4318" for HTTP/protobuf).
	// +optional
	OTLPEndpoint string `json:"otlpEndpoint,omitempty"`

	// OTLPProtocol is "grpc" or "http/protobuf".
	// +kubebuilder:validation:Enum=grpc;http/protobuf
	// +optional
	OTLPProtocol string `json:"otlpProtocol,omitempty"`

	// ServiceName overrides the OTel service name (default: "sympozium-agent-runner").
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// Headers are additional OTLP export headers (e.g., auth tokens).
	// +optional
	Headers map[string]string `json:"headers,omitempty"`

	// HeadersSecretRef references a Secret containing OTLP export headers.
	// +optional
	HeadersSecretRef string `json:"headersSecretRef,omitempty"`

	// SamplingRatio is the trace sampling probability as a string ("0.0" to "1.0").
	// Parsed to float64 at runtime. String type avoids controller-gen float issues.
	// +optional
	SamplingRatio string `json:"samplingRatio,omitempty"`

	// ResourceAttributes are additional OTel resource attributes (key/value).
	// +optional
	ResourceAttributes map[string]string `json:"resourceAttributes,omitempty"`
}

// WebEndpointSpec configures the web-proxy that exposes an agent as an HTTP API.
// When the field is absent or Enabled is false, the controller deploys nothing.
// Infrastructure is only created when Enabled is explicitly set to true.
type WebEndpointSpec struct {
	// Enabled is the master switch. When false (or when WebEndpoint is nil),
	// no web-proxy Deployment, Service, HTTPRoute, or Secret is created.
	// When toggled from true→false, the controller tears down all resources.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// Hostname for this instance's HTTPRoute (e.g. "alice.sympozium.example.com").
	// If empty, defaults to "<instance-name>.<gateway.baseDomain>" from Helm values.
	// +optional
	Hostname string `json:"hostname,omitempty"`

	// AuthSecretRef references a K8s Secret containing the API key.
	// The Secret must have a key named "api-key".
	// If empty, one is auto-generated with a random sk-<hex> key.
	// +optional
	AuthSecretRef string `json:"authSecretRef,omitempty"`

	// RateLimit defines request rate limiting.
	// +optional
	RateLimit *RateLimitSpec `json:"rateLimit,omitempty"`
}

// RateLimitSpec defines rate limiting for the web endpoint.
type RateLimitSpec struct {
	// RequestsPerMinute is the maximum requests per minute per API key.
	// +kubebuilder:default=60
	RequestsPerMinute int `json:"requestsPerMinute,omitempty"`

	// BurstSize allows short bursts above the rate limit.
	// +kubebuilder:default=10
	BurstSize int `json:"burstSize,omitempty"`
}

// ChannelSpec defines a channel connection.
type ChannelSpec struct {
	// Type is the channel type (telegram, whatsapp, discord, slack).
	Type string `json:"type"`

	// ConfigRef references the secret containing channel credentials.
	// Optional for channels that use alternative authentication (e.g. WhatsApp QR pairing).
	ConfigRef SecretRef `json:"configRef,omitempty"`

	// AccessControl restricts which users and chats can interact via this channel.
	// When nil, all users and chats are allowed.
	// +optional
	AccessControl *ChannelAccessControl `json:"accessControl,omitempty"`

	// Triggers controls when an inbound message becomes an AgentRun.
	// When nil, every accepted message triggers the agent.
	// +optional
	Triggers *ChannelTriggerSpec `json:"triggers,omitempty"`

	// Slack holds Slack-specific options. Ignored for other channel types.
	// +optional
	Slack *SlackChannelOptions `json:"slack,omitempty"`

	// Volumes are extra pod volumes for the channel pod (e.g. CSI
	// SecretProviderClass priming the configRef Secret). Channel pods are
	// independent of agent pods, so these are managed separately from
	// AgentSpec.Volumes.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// VolumeMounts are extra mounts for the channel container.
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
}

// ChannelAccessControl restricts which users and chats can interact via a channel.
type ChannelAccessControl struct {
	// AllowedSenders restricts interaction to these sender IDs.
	// When non-empty, only listed senders can trigger agent runs.
	// +optional
	AllowedSenders []string `json:"allowedSenders,omitempty"`

	// DeniedSenders blocks these sender IDs from interacting.
	// Overrides AllowedSenders when a sender appears in both lists.
	// +optional
	DeniedSenders []string `json:"deniedSenders,omitempty"`

	// AllowedChats restricts interaction to these chat/group IDs.
	// When non-empty, only messages from listed chats are accepted.
	// +optional
	AllowedChats []string `json:"allowedChats,omitempty"`

	// DenyMessage is the text sent back to rejected senders.
	// When empty, rejected messages are silently dropped.
	// +optional
	DenyMessage string `json:"denyMessage,omitempty"`
}

// ChannelTriggerSpec controls when an inbound channel message becomes
// an AgentRun. All fields are optional; when omitted the agent triggers
// on every accepted message.
type ChannelTriggerSpec struct {
	// StopKeywords mute the agent in a chat when matched on an inbound
	// message text (case-insensitive substring). Once muted, subsequent
	// messages in that chat are dropped until a StartKeywords match
	// resumes the agent. Has no effect on chats that are already muted.
	// +optional
	StopKeywords []string `json:"stopKeywords,omitempty"`

	// StartKeywords resume a previously-muted agent in a chat (case-
	// insensitive substring). Only evaluated while the chat is muted;
	// otherwise ignored. The triggering message itself is consumed by
	// the resume action and does not produce an AgentRun.
	// +optional
	StartKeywords []string `json:"startKeywords,omitempty"`
}

// SlackReactionDisabled is the literal value used in any
// SlackChannelOptions emoji field to suppress that reaction. Empty
// values fall back to per-slot defaults; use this constant to opt out
// entirely.
const SlackReactionDisabled = "none"

// SlackChannelOptions holds Slack-specific channel configuration. These
// fields have no effect on other channel types.
//
// Reaction emojis use Slack emoji names without surrounding colons
// (e.g. "robot_face"). Each emoji slot has a sensible default; leave a
// field empty to use the default, or set it to SlackReactionDisabled
// ("none") to disable that reaction.
type SlackChannelOptions struct {
	// EmojiOnTrigger is the reaction added to inbound messages that
	// successfully start an AgentRun. Default: "eyes".
	// +optional
	EmojiOnTrigger string `json:"emojiOnTrigger,omitempty"`

	// EmojiOnStop is the reaction added when a stop keyword mutes
	// the chat. Default: "mute".
	// +optional
	EmojiOnStop string `json:"emojiOnStop,omitempty"`

	// EmojiOnStart is the reaction added when a start keyword
	// resumes the chat. Default: "loud_sound".
	// +optional
	EmojiOnStart string `json:"emojiOnStart,omitempty"`

	// Threading controls reply placement. When true, replies are sent
	// in a thread: messages already inside a thread reply in that
	// thread, and top-level messages get a new thread anchored to the
	// inbound message. When false (default) replies are posted at the
	// top level of the channel.
	// +optional
	Threading bool `json:"threading,omitempty"`

	// AllowedTriggers gates which inbound message kinds may start an
	// AgentRun. Values: "mention" (the bot is @-mentioned),
	// "dm" (direct message to the bot), "channel" (any other channel
	// or group message). When empty, all kinds trigger the agent.
	// Composes (AND) with ChannelAccessControl.
	// +optional
	AllowedTriggers []string `json:"allowedTriggers,omitempty"`

	// ThreadStickiness, when true together with Threading, lets the
	// user who originally opened a thread keep talking to the bot
	// without re-mentioning. The first sender to address the bot in
	// a thread becomes the thread's "owner". Any message from anyone
	// other than the owner — even an @-mention from a denied user —
	// permanently marks the thread "interrupted". Once interrupted,
	// every subsequent message (including from the owner) must
	// satisfy AllowedTriggers (e.g. @-mention) to be processed; the
	// lax free-flow mode never resumes for that thread. Has no
	// effect when Threading is false.
	// +optional
	ThreadStickiness bool `json:"threadStickiness,omitempty"`
}

// AgentsSpec defines agent configuration.
type AgentsSpec struct {
	// Default is the default agent configuration.
	Default AgentConfig `json:"default"`
}

// AgentConfig defines configuration for an agent.
type AgentConfig struct {
	// Model is the LLM model to use.
	Model string `json:"model"`

	// BaseURL overrides the provider's default API endpoint.
	// Use for OpenAI-compatible providers (GitHub Copilot, Azure OpenAI, Ollama, etc.).
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// ProviderHeaders are additional HTTP headers sent with every LLM provider request.
	// Useful for OpenAI-compatible gateways (e.g. Portkey) that use headers for routing.
	// +optional
	ProviderHeaders map[string]string `json:"providerHeaders,omitempty"`

	// ProviderHeadersSecretRef references a Kubernetes Secret whose data keys are
	// injected as provider request headers. Values from the secret override inline
	// ProviderHeaders on key collision.
	// +optional
	ProviderHeadersSecretRef string `json:"providerHeadersSecretRef,omitempty"`

	// Thinking is the thinking mode (off, low, medium, high).
	// +optional
	Thinking string `json:"thinking,omitempty"`

	// Sandbox configuration.
	// +optional
	Sandbox *SandboxSpec `json:"sandbox,omitempty"`

	// AgentSandbox configures the Kubernetes Agent Sandbox (CRD) execution backend defaults.
	// When enabled, agent runs use Sandbox CRs with gVisor/Kata kernel isolation.
	// +optional
	AgentSandbox *AgentSandboxDefaults `json:"agentSandbox,omitempty"`

	// Subagents configuration.
	// +optional
	Subagents *SubagentsSpec `json:"subagents,omitempty"`

	// RunTimeout is the maximum duration for each agent run (e.g. "30m", "1h").
	// When empty, the controller watchdog enforces a flat 10-minute cap
	// regardless of provider, so this must be set explicitly to allow longer
	// runs. Local models (ollama, lm-studio, vllm) are significantly slower per
	// request, so a longer value (e.g. "30m") is recommended for them.
	// +optional
	RunTimeout string `json:"runTimeout,omitempty"`

	// NodeSelector constrains agent pods to nodes with matching labels.
	// Used for node-pinned inference (e.g., Ollama installed on specific GPU nodes).
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// Lifecycle defines pre and post run hooks for agent runs.
	// Propagated to AgentRunSpec.Lifecycle when runs are created.
	// +optional
	Lifecycle *LifecycleHooks `json:"lifecycle,omitempty"`

	// Env defines additional environment variables injected into the
	// agent-runner container of every AgentRun created for this agent
	// (channel-driven, scheduled, stimulus, web-endpoint, sequential
	// handoff). Useful for tuning runtime knobs such as
	// MAX_TOOL_ITERATIONS without forking the agent-runner image.
	// The controller copies these values onto AgentRunSpec.Env when it
	// creates a run on behalf of the agent; manually-applied AgentRun CRs
	// are not modified, so any AgentRunSpec.Env set there is preserved
	// as-is.
	// +optional
	Env map[string]string `json:"env,omitempty"`
}

// ParseRunTimeout parses the configured RunTimeout string into a
// *metav1.Duration suitable for AgentRunSpec.Timeout. It returns nil when
// RunTimeout is empty or fails to parse, in which case the controller watchdog
// applies its flat 10-minute default. When non-nil, a single persisted
// AgentRunSpec.Timeout drives all controller-side gates consistently (watchdog,
// Job activeDeadlineSeconds, and the RUN_TIMEOUT env injection).
func (c AgentConfig) ParseRunTimeout() *metav1.Duration {
	if c.RunTimeout == "" {
		return nil
	}
	d, err := time.ParseDuration(c.RunTimeout)
	if err != nil || d <= 0 {
		return nil
	}
	return &metav1.Duration{Duration: d}
}

// SandboxSpec defines sandbox configuration.
type SandboxSpec struct {
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

	// Resources for the sandbox container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

// AgentSandboxDefaults defines instance-level defaults for the Kubernetes
// Agent Sandbox (CRD) execution backend.
type AgentSandboxDefaults struct {
	// Enabled activates Agent Sandbox mode for all runs in this instance.
	Enabled bool `json:"enabled"`

	// RuntimeClass is the default runtimeClassName (e.g., "gvisor", "kata").
	// +optional
	RuntimeClass string `json:"runtimeClass,omitempty"`

	// WarmPool configures a controller-managed SandboxWarmPool for this instance.
	// +optional
	WarmPool *WarmPoolSpec `json:"warmPool,omitempty"`
}

// WarmPoolSpec configures a managed SandboxWarmPool for pre-provisioned sandboxes.
type WarmPoolSpec struct {
	// Size is the number of pre-warmed sandboxes to maintain.
	// +kubebuilder:default=2
	Size int `json:"size,omitempty"`

	// RuntimeClass for warm pool sandboxes.
	// +optional
	RuntimeClass string `json:"runtimeClass,omitempty"`

	// Resources for each warm pool sandbox.
	// +optional
	Resources *ResourceSpec `json:"resources,omitempty"`
}

// SubagentsSpec defines sub-agent spawning configuration.
type SubagentsSpec struct {
	// MaxDepth is the maximum nesting depth for sub-agents.
	// +kubebuilder:default=2
	MaxDepth int `json:"maxDepth,omitempty"`

	// MaxConcurrent is the maximum number of concurrent agent runs.
	// +kubebuilder:default=5
	MaxConcurrent int `json:"maxConcurrent,omitempty"`

	// MaxChildrenPerAgent is the maximum number of children per agent.
	// +kubebuilder:default=3
	MaxChildrenPerAgent int `json:"maxChildrenPerAgent,omitempty"`
}

// SkillRef references a SkillPack or ConfigMap containing skills.
type SkillRef struct {
	// SkillPackRef references a SkillPack CRD by name.
	// +optional
	SkillPackRef string `json:"skillPackRef,omitempty"`

	// ConfigMapRef references a ConfigMap by name.
	// +optional
	ConfigMapRef string `json:"configMapRef,omitempty"`

	// Params are per-instance key/value pairs injected as SKILL_<KEY> environment
	// variables into the skill sidecar container. This allows the same SkillPack to
	// be configured differently per Agent (e.g. different GitHub repos).
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// SecretRef references a Kubernetes Secret.
type SecretRef struct {
	// Provider is the AI provider name (e.g. "openai", "anthropic", "azure-openai", "ollama").
	// +optional
	Provider string `json:"provider,omitempty"`
	// Secret is the name of the Secret.
	Secret string `json:"secret"`
}

// AgentStatus defines the observed state of Agent.
type AgentStatus struct {
	// Phase is the current phase (Pending, Running, Serving, Error).
	// +optional
	Phase string `json:"phase,omitempty"`

	// Channels reports the status of each connected channel.
	// +optional
	Channels []ChannelStatus `json:"channels,omitempty"`

	// ActiveAgentPods is the number of currently running agent pods.
	// +optional
	ActiveAgentPods int `json:"activeAgentPods,omitempty"`

	// TotalAgentRuns is the total number of agent runs for this instance.
	// +optional
	TotalAgentRuns int64 `json:"totalAgentRuns,omitempty"`

	// Conditions represent the latest available observations of an object's state.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// WebEndpoint reports the status of the web endpoint.
	// +optional
	WebEndpoint *WebEndpointStatus `json:"webEndpoint,omitempty"`
}

// WebEndpointStatus reports the observed state of a web endpoint.
type WebEndpointStatus struct {
	// Status is the current status (Pending, Ready, Error).
	Status string `json:"status"`

	// URL is the external URL for the web endpoint.
	// +optional
	URL string `json:"url,omitempty"`

	// AuthSecretName is the name of the Secret containing the API key.
	// +optional
	AuthSecretName string `json:"authSecretName,omitempty"`
}

// ChannelStatus reports the status of a channel.
type ChannelStatus struct {
	// Type is the channel type.
	Type string `json:"type"`

	// Status is the connection status (Connected, Disconnected, Error).
	Status string `json:"status"`

	// LastHealthCheck is the timestamp of the last health check.
	// +optional
	LastHealthCheck *metav1.Time `json:"lastHealthCheck,omitempty"`

	// Message provides additional details about the channel status.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Active Agents",type="integer",JSONPath=".status.activeAgentPods"
// +kubebuilder:printcolumn:name="Total Runs",type="integer",JSONPath=".status.totalAgentRuns"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Agent is the Schema for the agents API.
// It represents a per-user/per-tenant gateway configuration.
type Agent struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentSpec   `json:"spec,omitempty"`
	Status AgentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AgentList contains a list of Agent.
type AgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Agent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Agent{}, &AgentList{})
}
