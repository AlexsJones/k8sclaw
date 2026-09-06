package v1alpha1

// CellnExecutionSpec is the immutable-source portion of celln.dev/v1alpha1.
// Invocation aliases are lookups into Tools, never host filesystem paths.
type CellnExecutionSpec struct {
	Mote CellnImmutableRef `json:"mote"`
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	Tools []CellnToolRef `json:"tools"`
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Inputs       []CellnInput      `json:"inputs,omitempty"`
	Invocation   CellnInvocation   `json:"invocation"`
	Capabilities CellnCapabilities `json:"capabilities"`
	// Lane never promotes an agent-authored artifact to tool authority.
	// +kubebuilder:validation:Enum=agent;tool
	Lane string `json:"lane"`
}

type CellnImmutableRef struct {
	// +kubebuilder:validation:Pattern="^blake3:[0-9a-f]{64}$"
	Hash string `json:"hash"`
}

type CellnToolRef struct {
	// +kubebuilder:validation:Pattern="^/"
	Alias string `json:"alias"`
	// +kubebuilder:validation:Pattern="^blake3:[0-9a-f]{64}$"
	Hash string `json:"hash"`
	// +optional
	Closure *CellnImmutableRef `json:"closure,omitempty"`
}

type CellnInput struct {
	// +kubebuilder:validation:Pattern="^[a-z0-9._-]{1,64}$"
	Name string `json:"name"`
	// +kubebuilder:validation:Pattern="^blake3:[0-9a-f]{64}$"
	Hash string `json:"hash"`
	// +kubebuilder:validation:MinLength=1
	MediaType string `json:"mediaType"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	Bytes int64 `json:"bytes"`
}

type CellnInvocation struct {
	Alias string `json:"alias"`
	// +kubebuilder:validation:MaxItems=128
	// +optional
	Args []string `json:"args,omitempty"`
}

type CellnCapabilities struct {
	// +kubebuilder:validation:Enum=none;read-only;read-write
	Workspace string `json:"workspace"`
	// +kubebuilder:validation:MaxItems=32
	// +optional
	Egress []string `json:"egress,omitempty"`
	// Timeout is supplied by AgentRun.spec.timeout, not this object.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=268435456
	MemoryBytes int64 `json:"memoryBytes"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	OutputBytes int64 `json:"outputBytes"`
}
