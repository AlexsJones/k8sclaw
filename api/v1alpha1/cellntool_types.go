package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// CellnToolSpec describes one immutable executable-tool revision. Metadata is
// not proof of admission, distribution, prewarming, or authority to execute.
// Schema references name immutable JSON-schema artifacts, not executable code.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="tool revisions are immutable; publish a new revision"
// +kubebuilder:validation:XValidation:rule="self.invocationABI != 'celln.json-stdio/v1' || (size(self.description) <= 512 && self.limits.timeoutMillis <= 30000)",message="JSON adapter descriptions and tool deadlines must fit its bounded contract"
type CellnToolSpec struct {
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Revision string `json:"revision"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1024
	Description string `json:"description"`
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	SupportOwner string `json:"supportOwner"`
	// +kubebuilder:validation:Pattern="^[0-9a-f]{64}$"
	// +kubebuilder:validation:MaxLength=64
	PublisherKey string            `json:"publisherKey"`
	Executable   CellnImmutableRef `json:"executable"`
	Closure      CellnImmutableRef `json:"closure"`
	// +kubebuilder:validation:Pattern="^/([A-Za-z0-9_-][A-Za-z0-9_.+-]*/)*[A-Za-z0-9_-][A-Za-z0-9_.+-]*$"
	// +kubebuilder:validation:MaxLength=256
	EntryPoint string `json:"entryPoint"`
	// +kubebuilder:validation:Enum=celln.argv/v1;celln.json-stdio/v1
	InvocationABI   string            `json:"invocationABI"`
	ArgumentsSchema CellnImmutableRef `json:"argumentsSchema"`
	ResultSchema    CellnImmutableRef `json:"resultSchema"`
	// +kubebuilder:validation:Enum=linux/amd64
	Platform string `json:"platform"`
	// +kubebuilder:validation:Enum=tool;agent
	Lane string `json:"lane"`
	// A source image is provenance only. Dispatch never pulls this reference.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern="^[A-Za-z0-9][A-Za-z0-9._:/-]*@sha256:[0-9a-f]{64}$"
	SourceImage string          `json:"sourceImage,omitempty"`
	Limits      CellnToolLimits `json:"limits"`
}

// CellnToolLimits are maximum requested/admitted ceilings, not grants.
type CellnToolLimits struct {
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=300000
	TimeoutMillis int64 `json:"timeoutMillis"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=268435456
	MemoryBytes int64 `json:"memoryBytes"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	ArgumentBytes int64 `json:"argumentBytes"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	OutputBytes int64 `json:"outputBytes"`
	// Workspaces/immutable inputs and remote operations require a separately
	// implemented tool-delivery contract; this schema cannot silently grant them.
	// +kubebuilder:validation:Enum=none
	Workspace string `json:"workspace"`
	// +optional
	// +kubebuilder:validation:MaxItems=0
	Egress []string `json:"egress,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=0
	Inputs []CellnImmutableRef `json:"inputs,omitempty"`
	// +kubebuilder:validation:Enum=none;external-side-effects
	Effects string `json:"effects"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Revision",type=string,JSONPath=`.spec.revision`
// +kubebuilder:printcolumn:name="Lane",type=string,JSONPath=`.spec.lane`
// CellnTool is operator-managed catalogue metadata. There is intentionally no
// default approval/Ready condition and no execution integration in this slice.
type CellnTool struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CellnToolSpec `json:"spec"`
	// +optional
	Status CellnToolStatus `json:"status,omitempty"`
}

type CellnToolStatus struct {
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
type CellnToolList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CellnTool `json:"items"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// CellnToolSubmission is untrusted user intent. Creating it neither approves a
// publisher nor publishes an executable catalogue revision.
type CellnToolSubmission struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              CellnToolSpec `json:"spec"`
	// +optional
	Status CellnToolStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
type CellnToolSubmissionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CellnToolSubmission `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CellnTool{}, &CellnToolList{}, &CellnToolSubmission{}, &CellnToolSubmissionList{})
}
