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
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.agentRef is immutable"
	AgentRef string `json:"agentRef"`

	// RuntimeRef is an administrator-approved AgentRuntime implementing the
	// v1alpha2 persistent session contract.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec.runtimeRef is immutable"
	RuntimeRef string `json:"runtimeRef"`

	// DesiredState starts or stops the session. Stopping removes the Deployment
	// and Service while preserving this CR's audit status.
	// +kubebuilder:default=running
	// +kubebuilder:validation:Enum=running;stopped
	// +optional
	DesiredState string `json:"desiredState,omitempty"`

	// IdleTimeout stops the session workload after this period without an active
	// authenticated chat request. The PVC and HarnessSession are preserved.
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

	// LastActivityTime is updated when an authenticated chat starts or completes.
	// +optional
	LastActivityTime *metav1.Time `json:"lastActivityTime,omitempty"`

	// RequestCount is the number of chat requests accepted by the API proxy.
	// +optional
	RequestCount int64 `json:"requestCount,omitempty"`

	// ActiveRequests prevents idle timeout from stopping in-flight model work.
	// +optional
	ActiveRequests int32 `json:"activeRequests,omitempty"`

	// ErrorCount is the number of requests that failed or were cancelled.
	// +optional
	ErrorCount int64 `json:"errorCount,omitempty"`

	// LastRequestID is returned to the caller in X-Sympozium-Request-ID.
	// +optional
	LastRequestID string `json:"lastRequestID,omitempty"`

	// LastRequestState is started, succeeded, failed, or cancelled.
	// +optional
	LastRequestState string `json:"lastRequestState,omitempty"`

	// LastRequestStartedAt and LastRequestCompletedAt bound the latest request.
	// +optional
	LastRequestStartedAt *metav1.Time `json:"lastRequestStartedAt,omitempty"`
	// +optional
	LastRequestCompletedAt *metav1.Time `json:"lastRequestCompletedAt,omitempty"`

	// UsageAccounting is unavailable until adapters report trustworthy usage.
	// +optional
	UsageAccounting string `json:"usageAccounting,omitempty"`

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
