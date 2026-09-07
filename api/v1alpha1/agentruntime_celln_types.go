package v1alpha1

// AgentRuntimeCellnProfile describes immutable artifacts for explicitly
// versioned native adapters. These declarations do not attest to signatures, hardware,
// conformance or node readiness. They are not consumed by dispatch yet.
// +kubebuilder:validation:XValidation:rule="self.contractVersion == 'celln.json-tools/v1' ? has(self.json) : !has(self.json)",message="JSON loop ceilings must match the selected adapter contract"
type AgentRuntimeCellnProfile struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Revision string `json:"revision"`
	// +kubebuilder:validation:Enum=celln.reference-functions/v1;celln.json-tools/v1
	ContractVersion string            `json:"contractVersion"`
	Executable      CellnImmutableRef `json:"executable"`
	Closure         CellnImmutableRef `json:"closure"`
	Mote            CellnImmutableRef `json:"mote"`
	// +kubebuilder:validation:Pattern="^[0-9a-f]{64}$"
	// +kubebuilder:validation:MaxLength=64
	PublisherKey string `json:"publisherKey"`
	// +kubebuilder:validation:Pattern="^/([A-Za-z0-9_-][A-Za-z0-9_.+-]*/)*[A-Za-z0-9_-][A-Za-z0-9_.+-]*$"
	// +kubebuilder:validation:MaxLength=256
	EntryPoint string `json:"entryPoint"`
	// +kubebuilder:validation:Enum=linux/amd64
	Platform string `json:"platform"`
	// +kubebuilder:validation:Enum=agent
	Lane string `json:"lane"`
	// +kubebuilder:validation:Enum=disposable-one-shot
	Lifecycle string `json:"lifecycle"`
	// Immutable data delivery has no supported contract in this profile.
	// +optional
	// +kubebuilder:validation:MaxItems=0
	RuntimeData []CellnImmutableRef     `json:"runtimeData,omitempty"`
	Limits      AgentRuntimeCellnLimits `json:"limits"`
	// +optional
	JSON *CellnHarnessJSONLimits `json:"json,omitempty"`
}

// AgentRuntimeCellnLimits are ceilings, never Agent grants or provider policy.
// Model mediation and tools retain their selected adapter's dispatch contract;
// this profile cannot add protocols, tools or destinations.
type AgentRuntimeCellnLimits struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=300000
	TimeoutMillis int64 `json:"timeoutMillis"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=268435456
	MemoryBytes int64 `json:"memoryBytes"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2048
	TaskBytes int64 `json:"taskBytes"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	OutputBytes int64 `json:"outputBytes"`
	// +kubebuilder:validation:Enum=none
	Workspace string `json:"workspace"`
}
