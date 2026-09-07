package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SympoziumPolicySpec defines the desired state of SympoziumPolicy.
// Policies enforce governance over agent behaviour, sandbox isolation,
// resource limits, and tool access.
type SympoziumPolicySpec struct {
	// SandboxPolicy defines sandbox requirements.
	// +optional
	SandboxPolicy *SandboxPolicySpec `json:"sandboxPolicy,omitempty"`

	// SubagentPolicy defines sub-agent depth and concurrency limits.
	// +optional
	SubagentPolicy *SubagentPolicySpec `json:"subagentPolicy,omitempty"`

	// ToolGating defines tool access rules.
	// +optional
	ToolGating *ToolGatingSpec `json:"toolGating,omitempty"`

	// FeatureGates controls which features are enabled/disabled.
	// +optional
	FeatureGates map[string]bool `json:"featureGates,omitempty"`

	// NetworkPolicy defines network isolation settings.
	// +optional
	NetworkPolicy *NetworkPolicySpec `json:"networkPolicy,omitempty"`

	// ModelPolicy defines model access restrictions for bound instances.
	// +optional
	ModelPolicy *ModelPolicySpec `json:"modelPolicy,omitempty"`

	// ImagePolicy defines allowed container image registries for user-specified
	// images in lifecycle hooks, sandbox overrides, and skill sidecars.
	// +optional
	ImagePolicy *ImagePolicySpec `json:"imagePolicy,omitempty"`

	// HarnessPolicy controls whether an Agent bound to this policy may replace
	// Sympozium's trusted agent-runner with an external harness adapter image.
	// External harnesses are disabled unless this field is present and enabled.
	// +optional
	HarnessPolicy *HarnessPolicySpec `json:"harnessPolicy,omitempty"`

	// LifecyclePolicy defines bounds on lifecycle hook RBAC requests.
	// +optional
	LifecyclePolicy *LifecyclePolicySpec `json:"lifecyclePolicy,omitempty"`
}

// HarnessPolicySpec controls access to the experimental external harness
// runtime. Image restrictions are configured separately through ImagePolicy.
type HarnessPolicySpec struct {
	// Enabled explicitly opts Agents bound to this policy into external harness
	// execution. It defaults to false because the harness becomes the pod's
	// primary process and receives the run's model and MCP credentials.
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// AllowUnmetered permits a harness adapter that does not report token usage.
	// External adapters own their model loop, so Sympozium cannot infer usage;
	// without this opt-in, an ensemble token budget could appear enforced while
	// silently receiving no accounting data. Defaults to false.
	// +optional
	AllowUnmetered bool `json:"allowUnmetered,omitempty"`
}

// ModelPolicySpec defines model access restrictions.
type ModelPolicySpec struct {
	// AllowedNamespaces restricts which namespaces' models can be referenced.
	// Empty means all namespaces are allowed (default permissive).
	// +optional
	AllowedNamespaces []string `json:"allowedNamespaces,omitempty"`

	// AllowedModels restricts which model names can be used.
	// Empty means all models are allowed.
	// +optional
	AllowedModels []string `json:"allowedModels,omitempty"`
}

// SandboxPolicySpec defines sandbox enforcement.
type SandboxPolicySpec struct {
	// Required makes sandbox mandatory.
	Required bool `json:"required"`

	// DefaultImage is the fallback sandbox image.
	// +optional
	DefaultImage string `json:"defaultImage,omitempty"`

	// MaxCPU is the maximum CPU allowed for sandbox containers.
	// +optional
	MaxCPU string `json:"maxCPU,omitempty"`

	// MaxMemory is the maximum memory allowed for sandbox containers.
	// +optional
	MaxMemory string `json:"maxMemory,omitempty"`

	// AllowHostMounts controls whether host path mounts are allowed.
	// +kubebuilder:default=false
	AllowHostMounts bool `json:"allowHostMounts,omitempty"`

	// SeccompProfile defines the default seccomp profile type for agent pods.
	// Valid values: "RuntimeDefault", "Unconfined", "Localhost".
	// Defaults to "RuntimeDefault" when not set.
	// +optional
	SeccompProfile *SeccompProfileSpec `json:"seccompProfile,omitempty"`

	// AgentSandboxPolicy configures enforcement for the Kubernetes Agent Sandbox
	// (CRD) execution backend. When set, controls whether runs must use the
	// agent-sandbox CRD and which runtime classes are permitted.
	// +optional
	AgentSandboxPolicy *AgentSandboxPolicySpec `json:"agentSandboxPolicy,omitempty"`
}

// AgentSandboxPolicySpec defines policy for the Agent Sandbox CRD execution backend.
type AgentSandboxPolicySpec struct {
	// Required makes Agent Sandbox mode mandatory (all runs must use Sandbox CRD).
	Required bool `json:"required"`

	// DefaultRuntimeClass is the fallback runtimeClassName when not specified on the run.
	// +optional
	DefaultRuntimeClass string `json:"defaultRuntimeClass,omitempty"`

	// AllowedRuntimeClasses restricts which runtime classes can be used.
	// Empty means all are allowed.
	// +optional
	AllowedRuntimeClasses []string `json:"allowedRuntimeClasses,omitempty"`
}

// SubagentPolicySpec defines sub-agent limits.
type SubagentPolicySpec struct {
	// MaxDepth is the maximum nesting depth for sub-agents.
	// +kubebuilder:default=3
	MaxDepth int `json:"maxDepth,omitempty"`

	// MaxConcurrent is the maximum concurrent sub-agent runs.
	// +kubebuilder:default=5
	MaxConcurrent int `json:"maxConcurrent,omitempty"`
}

// ToolGatingSpec defines tool access rules.
type ToolGatingSpec struct {
	// DefaultAction is the default action for unmatched tools (allow, deny, ask).
	// +kubebuilder:default="allow"
	DefaultAction string `json:"defaultAction,omitempty"`

	// Rules is the list of tool-specific rules.
	Rules []ToolGatingRule `json:"rules,omitempty"`
}

// ToolGatingRule defines a rule for a specific tool.
type ToolGatingRule struct {
	// Tool is the tool name this rule applies to.
	Tool string `json:"tool"`

	// Action is the action to take (allow, deny, ask).
	Action string `json:"action"`
}

// NetworkPolicySpec defines network isolation settings.
type NetworkPolicySpec struct {
	// DenyAll denies all network access from agent pods.
	// +kubebuilder:default=true
	DenyAll bool `json:"denyAll,omitempty"`

	// AllowDNS allows DNS resolution.
	// +kubebuilder:default=true
	AllowDNS bool `json:"allowDNS,omitempty"`

	// AllowEventBus allows communication with the event bus.
	// +kubebuilder:default=true
	AllowEventBus bool `json:"allowEventBus,omitempty"`

	// AllowedEgress defines allowed egress endpoints.
	// +optional
	AllowedEgress []EgressRule `json:"allowedEgress,omitempty"`
}

// EgressRule defines an allowed egress endpoint.
type EgressRule struct {
	// Host is the allowed destination host or CIDR.
	Host string `json:"host"`

	// Port is the allowed destination port.
	// +optional
	Port int `json:"port,omitempty"`
}

// ImagePolicySpec defines allowed container image registries for user-specified
// images (lifecycle hooks, sandbox overrides, skill sidecars).
type ImagePolicySpec struct {
	// AllowedRegistries is a list of registry prefixes that images must match.
	// Example: ["ghcr.io/sympozium-ai/", "docker.io/library/"]
	// When empty, all registries are allowed (no restriction).
	// +optional
	AllowedRegistries []string `json:"allowedRegistries,omitempty"`
}

// LifecyclePolicySpec defines bounds on lifecycle hook RBAC requests.
type LifecyclePolicySpec struct {
	// DeniedResources is a list of Kubernetes resource types that lifecycle
	// hooks may not request RBAC access to (e.g. "secrets", "clusterroles").
	// +optional
	DeniedResources []string `json:"deniedResources,omitempty"`

	// MaxRetryAttempts caps lifecycle.retry.maxAttempts for any AgentRun bound
	// to this policy. A gate that keeps asking for another attempt burns tokens
	// silently, so this is the operator's ceiling on what a run may request.
	// 0 means no ceiling.
	// +optional
	MaxRetryAttempts int `json:"maxRetryAttempts,omitempty"`
}

// SympoziumPolicyStatus defines the observed state of SympoziumPolicy.
type SympoziumPolicyStatus struct {
	// BoundInstances is the number of Agents bound to this policy.
	// +optional
	BoundInstances int `json:"boundInstances,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Bound",type="integer",JSONPath=".status.boundInstances"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// SympoziumPolicy is the Schema for the sympoziumpolicies API.
// It enforces governance, sandbox requirements, network isolation,
// and tool access for bound Agents.
type SympoziumPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SympoziumPolicySpec   `json:"spec,omitempty"`
	Status SympoziumPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SympoziumPolicyList contains a list of SympoziumPolicy.
type SympoziumPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SympoziumPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SympoziumPolicy{}, &SympoziumPolicyList{})
}
