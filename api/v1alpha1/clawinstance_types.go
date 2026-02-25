package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClawInstanceSpec defines the desired state of a ClawInstance.
// Each user or tenant gets a ClawInstance that declares their desired channels,
// agents, and policy bindings.
type ClawInstanceSpec struct {
	// Channels this instance connects to.
	// +optional
	Channels []ChannelSpec `json:"channels,omitempty"`

	// Agent configuration.
	Agents AgentsSpec `json:"agents"`

	// Skills to mount (from SkillPack CRDs or ConfigMaps).
	// +optional
	Skills []SkillRef `json:"skills,omitempty"`

	// PolicyRef references the ClawPolicy that applies to this instance.
	// +optional
	PolicyRef string `json:"policyRef,omitempty"`

	// AuthRefs references secrets containing AI provider credentials.
	// +optional
	AuthRefs []SecretRef `json:"authRefs,omitempty"`

	// Memory configures persistent memory for this instance.
	// When enabled, a MEMORY.md ConfigMap is managed and mounted into agent pods.
	// +optional
	Memory *MemorySpec `json:"memory,omitempty"`
}

// MemorySpec configures persistent memory for a ClawInstance.
type MemorySpec struct {
	// Enabled indicates whether persistent memory is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// MaxSizeKB caps the memory ConfigMap size in kilobytes.
	// +kubebuilder:default=256
	// +optional
	MaxSizeKB int `json:"maxSizeKB,omitempty"`

	// SystemPrompt is injected into every agent run for this instance
	// to instruct the agent on how to use memory.
	// +optional
	SystemPrompt string `json:"systemPrompt,omitempty"`
}

// ChannelSpec defines a channel connection.
type ChannelSpec struct {
	// Type is the channel type (telegram, whatsapp, discord, slack).
	Type string `json:"type"`

	// ConfigRef references the secret containing channel credentials.
	// Optional for channels that use alternative authentication (e.g. WhatsApp QR pairing).
	ConfigRef SecretRef `json:"configRef,omitempty"`
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

	// Thinking is the thinking mode (off, low, medium, high).
	// +optional
	Thinking string `json:"thinking,omitempty"`

	// Sandbox configuration.
	// +optional
	Sandbox *SandboxSpec `json:"sandbox,omitempty"`

	// Subagents configuration.
	// +optional
	Subagents *SubagentsSpec `json:"subagents,omitempty"`
}

// SandboxSpec defines sandbox configuration.
type SandboxSpec struct {
	// Enabled indicates whether sandboxing is enabled.
	Enabled bool `json:"enabled"`

	// Image is the sandbox container image.
	// +optional
	Image string `json:"image,omitempty"`

	// Resources for the sandbox container.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
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
}

// SecretRef references a Kubernetes Secret.
type SecretRef struct {
	// Provider is the AI provider name (e.g. "openai", "anthropic", "azure-openai", "ollama").
	// +optional
	Provider string `json:"provider,omitempty"`
	// Secret is the name of the Secret.
	Secret string `json:"secret"`
}

// ClawInstanceStatus defines the observed state of ClawInstance.
type ClawInstanceStatus struct {
	// Phase is the current phase (Pending, Running, Error).
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

// ClawInstance is the Schema for the clawinstances API.
// It represents a per-user/per-tenant gateway configuration.
type ClawInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClawInstanceSpec   `json:"spec,omitempty"`
	Status ClawInstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClawInstanceList contains a list of ClawInstance.
type ClawInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClawInstance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClawInstance{}, &ClawInstanceList{})
}
