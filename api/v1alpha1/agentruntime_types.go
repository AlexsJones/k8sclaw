package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentRuntimeReadyCondition is the condition type the AgentRuntime controller
// sets when the spec validates (image digest-pinned, capabilities supported).
// Admission and the AgentRun reconciler check it before binding a runtime to a
// run, so a runtime referenced by name fails closed until it is Ready.
const AgentRuntimeReadyCondition = "Ready"

// AgentRuntimeCellnReadyCondition is independent of OCI Ready. Only verified
// artifact admission, conformance and distribution may eventually satisfy it.
const AgentRuntimeCellnReadyCondition = "CellnReady"

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Digest",type=string,JSONPath=`.status.resolvedImageDigest`
// +kubebuilder:printcolumn:name="Contract",type=string,JSONPath=`.spec.contractVersion`
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.spec.supportOwner`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AgentRuntime describes an administrator-approved runtime that replaces
// Sympozium's built-in `agent-runner` as the pod's primary process.
//
// It is the object-form, admin-owned counterpart to the inline `task.mode:
// harness` parameters: instead of a run author naming an image and a set of
// capability claims per run, they reference a runtime the platform team has
// vetted. The resource pins the digest, declares the adapter contract and
// capabilities, and records a support owner so a run can show provenance.
type AgentRuntime struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentRuntimeSpec   `json:"spec,omitempty"`
	Status AgentRuntimeStatus `json:"status,omitempty"`
}

// AgentRuntimeSpec is the desired state of an AgentRuntime.
type AgentRuntimeSpec struct {
	// Celln declares an additional placement profile, not executable authority.
	// Image remains required: Celln-only runtimes and catalogue-backed dispatch
	// are not supported yet. OCI Ready never implies CellnReady.
	// +optional
	Celln *AgentRuntimeCellnProfile `json:"celln,omitempty"`

	// Image is the digest-pinned OCI reference that becomes the pod's primary
	// process. A mutable tag is rejected: the digest is the trust anchor, and
	// it is recorded on status.resolvedImageDigest for audit.
	Image string `json:"image"`

	// ContractVersion is the adapter contract version this image implements
	// (e.g. "v1alpha1"). Adapters must fail closed on versions they do not
	// understand.
	// +optional
	ContractVersion string `json:"contractVersion,omitempty"`

	// Capabilities declares what this runtime honours, from the same set the
	// harness task mode accepts (persona, toolFilter). Empty means it claims
	// nothing.
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`

	// Model describes how this runtime routes to a model by default. Agents
	// that bind this runtime may still override it through their own config.
	// +optional
	Model *AgentRuntimeModel `json:"model,omitempty"`

	// Resources are the primary container's requests/limits. When nil, the
	// platform defaults apply.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`

	// SupportOwner identifies the team or person responsible for this runtime,
	// surfaced on run detail so operators know who to page.
	// +optional
	SupportOwner string `json:"supportOwner,omitempty"`

	// Conformance records this runtime's conformance status against the
	// versioned adapter contract.
	// +optional
	Conformance *AgentRuntimeConformance `json:"conformance,omitempty"`

	// Session describes the network contract exposed by a persistent-session
	// capable adapter. It is deliberately absent from v1alpha1 one-shot
	// runtimes: a runtime must opt in explicitly before Sympozium will keep it
	// running behind a Service.
	// +optional
	Session *AgentRuntimeSession `json:"session,omitempty"`
}

// AgentRuntimeSession describes the narrow HTTP interface an adapter exposes
// for a HarnessSession. The API server is the only supported client of this
// endpoint; it is never exposed directly to browsers or tenants.
type AgentRuntimeSession struct {
	// Protocol identifies the session wire protocol. The initial supported
	// value is "openai-chat".
	// +kubebuilder:validation:Enum=openai-chat
	Protocol string `json:"protocol"`

	// Port is the container and Service port on which the adapter listens.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
}

// AgentRuntimeModel describes a runtime's default model routing.
type AgentRuntimeModel struct {
	// Provider is the model provider (e.g. "openai-compatible", "anthropic",
	// "deepseek").
	// +optional
	Provider string `json:"provider,omitempty"`

	// Model is the model name the runtime calls.
	// +optional
	Model string `json:"model,omitempty"`

	// BaseURL overrides the provider's default endpoint.
	// +optional
	BaseURL string `json:"baseURL,omitempty"`

	// AuthSecretRef references a Secret key holding the provider credential.
	// Sympozium validates this reference against the backing Agent's allowlist
	// at run time; it does not trust it as written here.
	// +optional
	AuthSecretRef string `json:"authSecretRef,omitempty"`
}

// AgentRuntimeConformance records how this runtime stands against the adapter
// contract. It is informational: the controller does not block a runtime on a
// non-conformant marker, but it surfaces it.
type AgentRuntimeConformance struct {
	// Status is one of: pending, conformant, non-conformant.
	// +kubebuilder:validation:Enum=pending;conformant;non-conformant
	// +optional
	Status string `json:"status,omitempty"`

	// Owner identifies who performed the conformance assessment.
	// +optional
	Owner string `json:"owner,omitempty"`

	// Reference is a URL to the conformance report or run.
	// +optional
	Reference string `json:"reference,omitempty"`
}

// AgentRuntimeStatus is the observed state of an AgentRuntime.
type AgentRuntimeStatus struct {
	// ResolvedImageDigest is the digest parsed from spec.image. It is what a
	// run audit should record, independent of the exact reference syntax the
	// operator wrote.
	// +optional
	ResolvedImageDigest string `json:"resolvedImageDigest,omitempty"`

	// Conditions represent the latest observations (Ready, and a reason when
	// the spec fails validation).
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// AgentRuntimeList contains a list of AgentRuntime.
type AgentRuntimeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentRuntime `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AgentRuntime{}, &AgentRuntimeList{})
}
