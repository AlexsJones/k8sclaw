package v1alpha1

import (
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EnsembleSpec defines a bundle of pre-configured agent personas.
// Installing an Ensemble stamps out Agents, SympoziumSchedules,
// and optionally seeds memory for each persona.
type EnsembleSpec struct {
	// Enabled controls whether the controller stamps out resources for this ensemble.
	// Ensembles default to disabled (catalog-only) and must be explicitly
	// activated — for example via the TUI or kubectl patch.
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Description is a human-readable summary of this ensemble.
	// +optional
	Description string `json:"description,omitempty"`

	// Category classifies this ensemble (e.g. "platform", "security", "devops").
	// +optional
	Category string `json:"category,omitempty"`

	// Version is the ensemble version.
	// +optional
	Version string `json:"version,omitempty"`

	// Personas is the list of agent personas in this ensemble.
	AgentConfigs []AgentConfigSpec `json:"agentConfigs"`

	// AuthRefs references secrets containing AI provider credentials.
	// Applied to all generated Agents. Set during install.
	// +optional
	AuthRefs []SecretRef `json:"authRefs,omitempty"`

	// ExcludeAgentConfigs lists agent config names to skip during reconciliation.
	// Personas listed here will not have their Instance/Schedule created,
	// and existing resources for them will be deleted. Set by the TUI when
	// a user disables an individual persona.
	// +optional
	ExcludeAgentConfigs []string `json:"excludePersonas,omitempty"`

	// ChannelConfigs maps channel types to their credential secret names.
	// Populated during agent config onboarding when users provide channel tokens.
	// The controller uses this to set ConfigRef on generated instances.
	// +optional
	ChannelConfigs map[string]string `json:"channelConfigs,omitempty"`

	// PolicyRef references the SympoziumPolicy to apply to all generated instances.
	// +optional
	PolicyRef string `json:"policyRef,omitempty"`

	// BaseURL overrides the provider's default API endpoint for all generated instances.
	// Set during onboarding for local providers (e.g. Ollama, LM Studio) that do not
	// require authentication.
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// ProviderHeaders are additional HTTP headers sent with every LLM provider request.
	// Useful for OpenAI-compatible gateways (e.g. Portkey) that use headers for routing
	// or configuration. Per-agent-config ProviderHeaders override these on key collision.
	// +optional
	ProviderHeaders map[string]string `json:"providerHeaders,omitempty"`

	// ProviderHeadersSecretRef references a Kubernetes Secret whose data keys are
	// injected as provider request headers. Values from the secret override inline
	// ProviderHeaders on key collision. Use this for sensitive header values.
	// +optional
	ProviderHeadersSecretRef string `json:"providerHeadersSecretRef,omitempty"`

	// ModelRef references a Model CR for cluster-local inference.
	// When set, the controller resolves the Model's endpoint and configures all
	// generated instances to use it (provider=openai, baseURL=endpoint, no auth).
	// Takes precedence over AuthRefs and BaseURL.
	// +optional
	ModelRef string `json:"modelRef,omitempty"`

	// SkillParams provides per-skill parameters applied to all generated instances.
	// The outer key is the SkillPack name (e.g. "github-gitops"), and the inner map
	// holds key/value pairs injected as SKILL_<KEY> environment variables.
	// Set during onboarding when the user configures skill-specific settings.
	// +optional
	SkillParams map[string]map[string]string `json:"skillParams,omitempty"`

	// TaskOverride replaces each persona's default schedule task with a
	// team-level objective. Set during onboarding when the user provides
	// instructions for the team. Each persona's schedule task is prepended
	// with this directive so every agent works toward the same goal.
	// +optional
	TaskOverride string `json:"taskOverride,omitempty"`

	// ChannelAccessControl maps channel types to their access control rules.
	// Propagated to generated instances during reconciliation.
	// +optional
	ChannelAccessControl map[string]*ChannelAccessControl `json:"channelAccessControl,omitempty"`

	// ChannelTriggers maps channel types to their trigger rules
	// (start/stop keywords). Propagated as ChannelSpec.Triggers on every
	// generated Agent. Per-agent-config overrides in
	// AgentConfigSpec.ChannelTriggers take precedence.
	// +optional
	ChannelTriggers map[string]*ChannelTriggerSpec `json:"channelTriggers,omitempty"`

	// SlackOptions configures Slack-specific channel settings
	// (threading, allowed-triggers, sticky threads). Propagated as
	// ChannelSpec.Slack on every generated Agent for the slack channel.
	// Per-agent-config overrides in AgentConfigSpec.SlackOptions take
	// precedence.
	// +optional
	SlackOptions *SlackChannelOptions `json:"slackOptions,omitempty"`

	// ChannelVolumes maps channel type to extra pod volumes injected into
	// the generated channel deployment (e.g. Vault CSI SecretProviderClass
	// volume). Mirrors ChannelConfigs/ChannelAccessControl indexing.
	// +optional
	ChannelVolumes map[string][]corev1.Volume `json:"channelVolumes,omitempty"`

	// ChannelVolumeMounts maps channel type to extra container mounts on
	// the generated channel deployment.
	// +optional
	ChannelVolumeMounts map[string][]corev1.VolumeMount `json:"channelVolumeMounts,omitempty"`

	// AgentSandbox configures the Kubernetes Agent Sandbox (CRD) execution backend
	// for all instances generated by this ensemble. When enabled, agent runs use Sandbox
	// CRs with gVisor/Kata kernel-level isolation.
	// +optional
	AgentSandbox *AgentSandboxDefaults `json:"agentSandbox,omitempty"`

	// SharedMemory configures a shared memory pool accessible to all agent configurations
	// in this ensemble. Each agent config retains its private memory; the shared pool
	// provides cross-agent config knowledge sharing within the workflow.
	// +optional
	SharedMemory *SharedMemorySpec `json:"sharedMemory,omitempty"`

	// AutoStoreMemory sets the default for whether agents generated by this
	// ensemble automatically write a summary of each successful run to their
	// private memory server. Nil defaults to true (existing behaviour). Set to
	// false to disable automatic memory writes across the whole ensemble while
	// keeping the memory skill's /store endpoint for curated memory.
	// Per-agent-config overrides via AgentConfigMemory.AutoStore take precedence.
	// +optional
	AutoStoreMemory *bool `json:"autoStoreMemory,omitempty"`

	// Stimulus configures an auto-injected prompt that fires when all agent
	// pods in the ensemble reach Serving phase. The stimulus is delivered to
	// the target node specified via a "stimulus" relationship edge.
	// +optional
	Stimulus *StimulusSpec `json:"stimulus,omitempty"`

	// Relationships defines directed edges between personas in the ensemble,
	// enabling coordination patterns like delegation, sequential pipelines,
	// and supervision.
	// +optional
	Relationships []AgentConfigRelationship `json:"relationships,omitempty"`

	// WorkflowType describes the overall orchestration pattern for this ensemble.
	// "autonomous" (default): personas run independently on their own schedules.
	// "pipeline": personas execute in sequence defined by sequential edges.
	// "delegation": personas can actively delegate to each other at runtime.
	// +kubebuilder:validation:Enum=autonomous;pipeline;delegation
	// +kubebuilder:default="autonomous"
	// +optional
	WorkflowType string `json:"workflowType,omitempty"`

	// Volumes are additional pod volumes propagated to every Agent stamped
	// out by this ensemble. Useful for declaring a single Vault CSI
	// SecretProviderClass volume that all team members share. Names must not
	// collide with Sympozium-reserved volume names
	// (workspace, ipc, skills, tmp, memory, mcp-config).
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// VolumeMounts are additional volume mounts propagated to every Agent
	// stamped out by this ensemble and applied to the agent container.
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
}

// AgentConfigSpec defines a single agent configuration within an Ensemble.
type AgentConfigSpec struct {
	// Name is the agent configuration identifier (used as suffix in generated instance names).
	Name string `json:"name"`

	// DisplayName is the human-readable name shown in the TUI.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// SystemPrompt is the system prompt that defines the agent's role and behaviour.
	SystemPrompt string `json:"systemPrompt"`

	// Model overrides the default model for this agent configuration.
	// If empty, the ensemble-level or onboarding-time model is used.
	// +optional
	Model string `json:"model,omitempty"`

	// Provider overrides the ensemble-level provider for this agent configuration.
	// When set, the controller uses this persona's own provider
	// instead of the ensemble-level AuthRefs/BaseURL.
	// +optional
	Provider string `json:"provider,omitempty"`

	// BaseURL overrides the ensemble-level base URL for this agent configuration.
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// ProviderHeaders overrides ensemble-level provider headers for this agent configuration.
	// Keys here take precedence over ensemble-level ProviderHeaders.
	// +optional
	ProviderHeaders map[string]string `json:"providerHeaders,omitempty"`

	// ProviderHeadersSecretRef overrides the ensemble-level provider headers secret
	// for this agent configuration. When set, replaces (not merges with) the
	// ensemble-level ProviderHeadersSecretRef.
	// +optional
	ProviderHeadersSecretRef string `json:"providerHeadersSecretRef,omitempty"`

	// Skills lists SkillPack references to mount into the agent pod.
	// +optional
	Skills []string `json:"skills,omitempty"`

	// ToolPolicy defines which tools this agent config is allowed to use.
	// +optional
	ToolPolicy *AgentConfigToolPolicy `json:"toolPolicy,omitempty"`

	// Schedule defines a recurring task for this agent configuration.
	// +optional
	Schedule *AgentConfigSchedule `json:"schedule,omitempty"`

	// Memory defines initial memory seeds for this agent configuration.
	// +optional
	Memory *AgentConfigMemory `json:"memory,omitempty"`

	// Channels lists channel types this agent config should be bound to after install.
	// Users can modify channel bindings later via the TUI edit modal.
	// +optional
	Channels []string `json:"channels,omitempty"`

	// WebEndpoint configures the web endpoint for this agent configuration.
	// +optional
	WebEndpoint *AgentConfigWebEndpoint `json:"webEndpoint,omitempty"`

	// Lifecycle defines pre and post run hooks for this agent configuration.
	// Propagated to the generated Agent's AgentConfig.
	// +optional
	Lifecycle *LifecycleHooks `json:"lifecycle,omitempty"`

	// ChannelAccessControl maps channel types to per-agent-configuration access control
	// overrides. When set, these take priority over ensemble-level
	// ChannelAccessControl for this agent configuration. Use AllowedChats with Discord
	// channel IDs to route specific Discord channels to this persona.
	// +optional
	ChannelAccessControl map[string]*ChannelAccessControl `json:"channelAccessControl,omitempty"`

	// ChannelTriggers maps channel types to per-agent-configuration trigger
	// overrides (start/stop keywords). When set, these take priority over
	// ensemble-level ChannelTriggers for this agent configuration.
	// +optional
	ChannelTriggers map[string]*ChannelTriggerSpec `json:"channelTriggers,omitempty"`

	// SlackOptions overrides ensemble-level Slack-specific channel
	// settings (threading, allowed-triggers, sticky threads) for this
	// agent configuration. When non-nil, replaces the ensemble-level
	// SlackOptions entirely (no field-level merge).
	// +optional
	SlackOptions *SlackChannelOptions `json:"slackOptions,omitempty"`

	// MCPServers configures remote MCP (Model Context Protocol) servers
	// for this agent configuration. Each entry references an MCPServer CR
	// by Name (or supplies an inline URL) and is mounted into the generated
	// Agent's spec.mcpServers list, which the agent-runner consumes via the
	// mcp-bridge sidecar.
	// +optional
	MCPServers []MCPServerRef `json:"mcpServers,omitempty"`

	// Subagents configures ad-hoc sub-agent spawning limits for this persona.
	// When set, the generated Agent's SubagentsSpec is populated and the
	// spawn_subagents tool becomes available (requires the "subagents" SkillPack).
	// +optional
	Subagents *SubagentsSpec `json:"subagents,omitempty"`

	// Env defines additional environment variables injected into the
	// agent-runner container of every AgentRun created for this agent
	// configuration. Propagated to the generated Agent's
	// AgentConfig.Env, which the controllers then copy onto each
	// AgentRunSpec.Env.
	// +optional
	Env map[string]string `json:"env,omitempty"`

	// RunTimeout is the maximum duration for each agent run created for this
	// agent configuration (e.g. "30m", "1h"). Propagated to the generated
	// Agent's AgentConfig.RunTimeout, which the controllers then persist onto
	// each AgentRunSpec.Timeout. A persisted timeout drives all controller-side
	// gates consistently (watchdog, Job activeDeadlineSeconds, and the
	// RUN_TIMEOUT env), so scheduled/sweep runs can exceed the 10m default.
	// When empty, the provider-appropriate default applies.
	// +optional
	RunTimeout string `json:"runTimeout,omitempty"`
}

// AgentConfigWebEndpoint configures the web endpoint for an agent configuration.
type AgentConfigWebEndpoint struct {
	// Enabled indicates whether the web endpoint is active.
	Enabled bool `json:"enabled"`

	// Hostname overrides the auto-derived hostname.
	// +optional
	Hostname string `json:"hostname,omitempty"`
}

// AgentConfigToolPolicy defines tool access for an agent configuration.
type AgentConfigToolPolicy struct {
	// Allow lists explicitly allowed tools.
	// +optional
	Allow []string `json:"allow,omitempty"`

	// Deny lists explicitly denied tools.
	// +optional
	Deny []string `json:"deny,omitempty"`
}

// AgentConfigSchedule defines a recurring task configuration for an agent configuration.
type AgentConfigSchedule struct {
	// Type categorises the schedule: heartbeat, scheduled, or sweep.
	// +kubebuilder:validation:Enum=heartbeat;scheduled;sweep
	// +kubebuilder:default="heartbeat"
	Type string `json:"type"`

	// Interval is a human-readable interval (e.g. "30m", "1h", "6h").
	// Converted to a cron expression by the controller.
	// +optional
	Interval string `json:"interval,omitempty"`

	// Cron is a raw cron expression. Takes precedence over Interval.
	// +optional
	Cron string `json:"cron,omitempty"`

	// Task is the task description sent to the agent on each trigger.
	Task string `json:"task"`

	// FirstTick controls whether a newly created schedule runs straight away.
	// "immediate" (default): the first tick is treated as already due, so the
	// agent runs as soon as the ensemble is enabled.
	// "afterInterval": wait a full interval before the first run. Use this when
	// the agent has nothing to do until something else has happened — a
	// heartbeat that fires at t=0 wakes up to an empty inbox, and when a
	// stimulus targets the same agent the two collide and do the work twice.
	// +kubebuilder:validation:Enum=immediate;afterInterval
	// +kubebuilder:default="immediate"
	// +optional
	FirstTick string `json:"firstTick,omitempty"`
}

// AgentConfigMemory defines initial memory configuration for an agent configuration.
type AgentConfigMemory struct {
	// Enabled indicates whether persistent memory is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// AutoStore overrides the ensemble-level AutoStoreMemory setting for this
	// agent configuration. When set, it controls whether the generated agent
	// automatically writes a summary of each successful run to its memory
	// server. Nil inherits the ensemble-level default (which itself defaults to
	// true). See MemorySpec.AutoStore.
	// +optional
	AutoStore *bool `json:"autoStore,omitempty"`

	// Seeds is a list of initial memory entries injected into MEMORY.md.
	// +optional
	Seeds []string `json:"seeds,omitempty"`
}

// SharedMemorySpec configures a shared memory pool for all agent configurations in an ensemble.
type SharedMemorySpec struct {
	// Enabled activates the shared memory server for this ensemble.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// StorageSize is the PVC storage request for the shared memory database.
	// Defaults to "1Gi".
	// +kubebuilder:default="1Gi"
	// +optional
	StorageSize string `json:"storageSize,omitempty"`

	// AccessRules defines per-agent-configuration access controls for the shared memory.
	// If empty, all agent configurations get read-write access.
	// +optional
	AccessRules []SharedMemoryAccessRule `json:"accessRules,omitempty"`

	// Membrane configures the synthetic membrane layer: selective permeability,
	// provenance tracking, token budgets, and circuit breakers.
	// +optional
	Membrane *MembraneSpec `json:"membrane,omitempty"`
}

// SharedMemoryAccessRule defines access control for a specific agent configuration.
type SharedMemoryAccessRule struct {
	// AgentConfig is the agent config name this rule applies to.
	AgentConfig string `json:"agentConfig"`

	// Access is the access level: "read-write" or "read-only".
	// +kubebuilder:validation:Enum="read-write";"read-only"
	// +kubebuilder:default="read-write"
	Access string `json:"access"`
}

// StimulusSpec defines a prompt that is automatically injected into the target
// agent when all ensemble pods reach Serving phase.
type StimulusSpec struct {
	// Name identifies this stimulus node in relationships.
	Name string `json:"name"`

	// Prompt is the task text injected into the target agent's AgentRun.
	Prompt string `json:"prompt"`

	// Trigger controls when the stimulus fires.
	// "onReady" (default): fire automatically as soon as every agent in the
	// ensemble reports ready. Enabling the ensemble is then the only consent
	// step — the first run starts on its own.
	// "manual": never fire automatically. The ensemble reaches Ready and waits;
	// the run is created only by POST /api/v1/ensembles/{name}/stimulus/trigger.
	// Use this when a cycle costs real money or reaches the network, and a human
	// should choose the moment it starts.
	// +kubebuilder:validation:Enum=onReady;manual
	// +kubebuilder:default="onReady"
	// +optional
	Trigger string `json:"trigger,omitempty"`
}

// FiresOnReady reports whether the stimulus should be delivered automatically
// once the ensemble's agents are ready. An empty Trigger means "onReady" so
// ensembles written before the field existed keep their behaviour.
func (s *StimulusSpec) FiresOnReady() bool {
	return s.Trigger == "" || s.Trigger == StimulusTriggerOnReady
}

const (
	// StimulusTriggerOnReady fires the stimulus on the readiness edge.
	StimulusTriggerOnReady = "onReady"
	// StimulusTriggerManual fires the stimulus only via the trigger API.
	StimulusTriggerManual = "manual"
)

// AgentConfigRelationship defines a directed edge between two agent configurations in an ensemble.
type AgentConfigRelationship struct {
	// Source is the agent config name that initiates the interaction.
	Source string `json:"source"`

	// Target is the agent config name that receives the interaction.
	Target string `json:"target"`

	// Type categorises the relationship.
	// "delegation": source requests target and awaits the result.
	// "sequential": source must complete before target starts.
	// "supervision": source monitors target (observability only, no runtime effect).
	// "stimulus": source is a StimulusSpec node that injects a prompt into target on readiness.
	// +kubebuilder:validation:Enum=delegation;sequential;supervision;stimulus
	Type string `json:"type"`

	// Condition is an optional description of when this edge activates
	// (e.g. "when source run succeeds", "on explicit request").
	// +optional
	Condition string `json:"condition,omitempty"`

	// Timeout is the maximum duration to wait for the target to complete.
	// Applies to delegation and sequential types. Format: "5m", "1h".
	// +optional
	Timeout string `json:"timeout,omitempty"`

	// ResultFormat constrains the expected output (e.g. "json", "markdown").
	// +optional
	ResultFormat string `json:"resultFormat,omitempty"`
}

// ParseTimeout returns the edge's Timeout as a duration, or nil when it is
// unset, malformed, or non-positive. Callers fall back to their own default
// rather than treating a bad value as zero, which would expire the edge
// immediately. On a delegation edge the SpawnRouter enforces this as the
// deadline for the child run; on a sequential edge it is persisted onto the
// successor's AgentRunSpec.Timeout.
func (r AgentConfigRelationship) ParseTimeout() *metav1.Duration {
	if r.Timeout == "" {
		return nil
	}
	d, err := time.ParseDuration(r.Timeout)
	if err != nil || d <= 0 {
		return nil
	}
	return &metav1.Duration{Duration: d}
}

// InstalledAgentConfig tracks the resources created for one persona.
type InstalledAgentConfig struct {
	// Name is the agent configuration identifier.
	Name string `json:"name"`

	// InstanceName is the name of the generated Agent.
	InstanceName string `json:"instanceName"`

	// ScheduleName is the name of the generated SympoziumSchedule (if any).
	// +optional
	ScheduleName string `json:"scheduleName,omitempty"`
}

// EnsembleStatus defines the observed state of Ensemble.
type EnsembleStatus struct {
	// Phase is the current phase (Pending, Ready, Error).
	// +optional
	Phase string `json:"phase,omitempty"`

	// AgentConfigCount is the number of personas defined in this ensemble.
	// +optional
	AgentConfigCount int `json:"personaCount,omitempty"`

	// InstalledCount is the number of personas successfully installed.
	// +optional
	InstalledCount int `json:"installedCount,omitempty"`

	// InstalledAgentConfigs lists the resources created for each persona.
	// +optional
	InstalledAgentConfigs []InstalledAgentConfig `json:"installedPersonas,omitempty"`

	// SharedMemoryReady indicates the shared memory infrastructure is provisioned.
	// +optional
	SharedMemoryReady bool `json:"sharedMemoryReady,omitempty"`

	// TokenBudgetUsed tracks aggregate token consumption across all runs
	// in the current execution wave. Only populated when Membrane.TokenBudget is configured.
	// +optional
	TokenBudgetUsed int64 `json:"tokenBudgetUsed,omitempty"`

	// CircuitBreakerOpen indicates the delegation circuit breaker has tripped
	// due to consecutive delegate failures exceeding the configured threshold.
	// +optional
	CircuitBreakerOpen bool `json:"circuitBreakerOpen,omitempty"`

	// ConsecutiveDelegateFailures tracks the current streak of consecutive
	// delegate failures for circuit breaker evaluation.
	// +optional
	ConsecutiveDelegateFailures int `json:"consecutiveDelegateFailures,omitempty"`

	// AllAgentsServing indicates all agents have reached Running or Serving
	// phase (infrastructure ready). Used for stimulus edge detection.
	// +optional
	AllAgentsServing bool `json:"allAgentsServing,omitempty"`

	// StimulusDelivered indicates the stimulus prompt has been injected in
	// the current readiness cycle. Reset when the ensemble is disabled.
	// +optional
	StimulusDelivered bool `json:"stimulusDelivered,omitempty"`

	// StimulusGeneration increments each time a stimulus is delivered
	// (automatic or manual).
	// +optional
	StimulusGeneration int64 `json:"stimulusGeneration,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Enabled",type="boolean",JSONPath=".spec.enabled"
// +kubebuilder:printcolumn:name="Personas",type="integer",JSONPath=".status.personaCount"
// +kubebuilder:printcolumn:name="Installed",type="integer",JSONPath=".status.installedCount"
// +kubebuilder:printcolumn:name="Workflow",type="string",JSONPath=".spec.workflowType"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// Ensemble is the Schema for the ensembles API.
// It bundles pre-configured agent personas that can be installed to stamp out
// Agents, Schedules, and memory seeds.
type Ensemble struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EnsembleSpec   `json:"spec,omitempty"`
	Status EnsembleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EnsembleList contains a list of Ensemble.
type EnsembleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Ensemble `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Ensemble{}, &EnsembleList{})
}
