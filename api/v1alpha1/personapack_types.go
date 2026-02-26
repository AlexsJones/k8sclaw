package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PersonaPackSpec defines a bundle of pre-configured agent personas.
// Installing a PersonaPack stamps out SympoziumInstances, SympoziumSchedules,
// and optionally seeds memory for each persona.
type PersonaPackSpec struct {
	// Description is a human-readable summary of this persona pack.
	// +optional
	Description string `json:"description,omitempty"`

	// Category classifies this persona pack (e.g. "platform", "security", "devops").
	// +optional
	Category string `json:"category,omitempty"`

	// Version is the persona pack version.
	// +optional
	Version string `json:"version,omitempty"`

	// Personas is the list of agent personas in this pack.
	Personas []PersonaSpec `json:"personas"`

	// AuthRefs references secrets containing AI provider credentials.
	// Applied to all generated SympoziumInstances. Set during install.
	// +optional
	AuthRefs []SecretRef `json:"authRefs,omitempty"`

	// PolicyRef references the SympoziumPolicy to apply to all generated instances.
	// +optional
	PolicyRef string `json:"policyRef,omitempty"`
}

// PersonaSpec defines a single agent persona within a PersonaPack.
type PersonaSpec struct {
	// Name is the persona identifier (used as suffix in generated instance names).
	Name string `json:"name"`

	// DisplayName is the human-readable name shown in the TUI.
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// SystemPrompt is the system prompt that defines the agent's role and behaviour.
	SystemPrompt string `json:"systemPrompt"`

	// Model overrides the default model for this persona.
	// If empty, the pack-level or onboarding-time model is used.
	// +optional
	Model string `json:"model,omitempty"`

	// Skills lists SkillPack references to mount into the agent pod.
	// +optional
	Skills []string `json:"skills,omitempty"`

	// ToolPolicy defines which tools this persona is allowed to use.
	// +optional
	ToolPolicy *PersonaToolPolicy `json:"toolPolicy,omitempty"`

	// Schedule defines a recurring task for this persona.
	// +optional
	Schedule *PersonaSchedule `json:"schedule,omitempty"`

	// Memory defines initial memory seeds for this persona.
	// +optional
	Memory *PersonaMemory `json:"memory,omitempty"`

	// Channels lists channel types this persona should be bound to after install.
	// Users can modify channel bindings later via the TUI edit modal.
	// +optional
	Channels []string `json:"channels,omitempty"`
}

// PersonaToolPolicy defines tool access for a persona.
type PersonaToolPolicy struct {
	// Allow lists explicitly allowed tools.
	// +optional
	Allow []string `json:"allow,omitempty"`

	// Deny lists explicitly denied tools.
	// +optional
	Deny []string `json:"deny,omitempty"`
}

// PersonaSchedule defines a recurring task configuration for a persona.
type PersonaSchedule struct {
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
}

// PersonaMemory defines initial memory configuration for a persona.
type PersonaMemory struct {
	// Enabled indicates whether persistent memory is active.
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// Seeds is a list of initial memory entries injected into MEMORY.md.
	// +optional
	Seeds []string `json:"seeds,omitempty"`
}

// InstalledPersona tracks the resources created for one persona.
type InstalledPersona struct {
	// Name is the persona identifier.
	Name string `json:"name"`

	// InstanceName is the name of the generated SympoziumInstance.
	InstanceName string `json:"instanceName"`

	// ScheduleName is the name of the generated SympoziumSchedule (if any).
	// +optional
	ScheduleName string `json:"scheduleName,omitempty"`
}

// PersonaPackStatus defines the observed state of PersonaPack.
type PersonaPackStatus struct {
	// Phase is the current phase (Pending, Ready, Error).
	// +optional
	Phase string `json:"phase,omitempty"`

	// PersonaCount is the number of personas defined in this pack.
	// +optional
	PersonaCount int `json:"personaCount,omitempty"`

	// InstalledCount is the number of personas successfully installed.
	// +optional
	InstalledCount int `json:"installedCount,omitempty"`

	// InstalledPersonas lists the resources created for each persona.
	// +optional
	InstalledPersonas []InstalledPersona `json:"installedPersonas,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Personas",type="integer",JSONPath=".status.personaCount"
// +kubebuilder:printcolumn:name="Installed",type="integer",JSONPath=".status.installedCount"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// PersonaPack is the Schema for the personapacks API.
// It bundles pre-configured agent personas that can be installed to stamp out
// SympoziumInstances, Schedules, and memory seeds.
type PersonaPack struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   PersonaPackSpec   `json:"spec,omitempty"`
	Status PersonaPackStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// PersonaPackList contains a list of PersonaPack.
type PersonaPackList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PersonaPack `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PersonaPack{}, &PersonaPackList{})
}
