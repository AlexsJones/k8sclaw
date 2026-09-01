package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// HarnessSessionReadyCondition reports whether the session endpoint can
	// accept proxied requests.
	HarnessSessionReadyCondition = "Ready"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Agent",type=string,JSONPath=`.spec.agentRef`
// +kubebuilder:printcolumn:name="Runtime",type=string,JSONPath=`.spec.runtimeRef`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// HarnessSession is a persistent, Agent-owned adapter process. It is separate
// from AgentRun on purpose: completed one-shot runs remain Jobs while this CR
// owns the Deployment and private Service required for interactive use.
type HarnessSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   HarnessSessionSpec   `json:"spec,omitempty"`
	Status HarnessSessionStatus `json:"status,omitempty"`
}

// HarnessSessionSpec is the desired persistent session state.
type HarnessSessionSpec struct {
	// AgentRef is the Agent whose credential allowlist and model routing bound
	// this session. The controller rejects a missing Agent.
	AgentRef string `json:"agentRef"`

	// RuntimeRef is an administrator-approved AgentRuntime implementing the
	// v1alpha2 persistent session contract.
	RuntimeRef string `json:"runtimeRef"`

	// DesiredState starts or stops the session. Stopping removes the Deployment
	// and Service while preserving this CR's audit status.
	// +kubebuilder:default=running
	// +kubebuilder:validation:Enum=running;stopped
	// +optional
	DesiredState string `json:"desiredState,omitempty"`

	// IdleTimeout is reserved for the API proxy to record activity and for the
	// controller to stop idle sessions. It is not activated until the proxy
	// activity endpoint lands; this avoids silently timing out sessions before
	// there is a trustworthy activity signal.
	// +optional
	IdleTimeout *metav1.Duration `json:"idleTimeout,omitempty"`
}

// HarnessSessionStatus is the observed lifecycle and immutable provenance.
type HarnessSessionStatus struct {
	// Phase is Pending, Ready, Draining, or Failed.
	// +optional
	Phase string `json:"phase,omitempty"`

	// ResolvedImageDigest records the exact adapter image selected at creation.
	// +optional
	ResolvedImageDigest string `json:"resolvedImageDigest,omitempty"`

	// ServiceName is the private in-namespace Service owned by this session.
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// Endpoint is an internal-only URL for the Sympozium API server. It is
	// status/audit information, not a browser-facing endpoint.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Conditions represent the latest observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true

// HarnessSessionList contains HarnessSession objects.
type HarnessSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HarnessSession `json:"items"`
}

func init() {
	SchemeBuilder.Register(&HarnessSession{}, &HarnessSessionList{})
}
