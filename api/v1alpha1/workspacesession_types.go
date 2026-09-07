package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkspaceSpec configures how the /workspace volume is provisioned for
// agent pods. When PerSessionPVC is true, each unique SessionKey gets a
// dedicated PersistentVolumeClaim (via a WorkspaceSession resource) that
// survives across AgentRuns. When false (the default), /workspace is an
// emptyDir scoped to the lifetime of each pod.
//
// Per-session workspaces are required by harnesses (codex, claude-code,
// etc.) whose session state lives in a directory under $HOME, and are
// useful any time an agent needs continuity across turns of the same
// conversation (e.g. a long-lived Slack thread).
//
// Per-session workspaces mount as ReadWriteOnce; the controller
// serialises AgentRuns that share a (agent, sessionKey) pair to avoid
// multi-attach failures.
type WorkspaceSpec struct {
	// PerSessionPVC enables a dedicated PVC per (agent, sessionKey).
	// When false or nil, /workspace remains an ephemeral emptyDir.
	// +optional
	PerSessionPVC bool `json:"perSessionPVC,omitempty"`

	// Size requests storage capacity for the per-session PVC
	// (e.g. "1Gi"). Defaults to "1Gi" (matching the emptyDir size limit
	// used for ephemeral workspaces).
	// +optional
	// +kubebuilder:validation:Pattern=`^[0-9]+([.][0-9]+)?(([KMGTPE]i?)|[a-zA-Z]*)?$`
	Size string `json:"size,omitempty"`

	// StorageClassName selects the StorageClass for the per-session
	// PVC. When empty, the cluster's default StorageClass is used.
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`

	// IdleTTL is the duration a WorkspaceSession (and its PVC) may sit
	// idle without any AgentRun referencing it before the sweeper
	// reclaims it. Defaults to "720h" (30 days). Set to "0s" to disable
	// idle reclamation entirely.
	// +optional
	IdleTTL *metav1.Duration `json:"idleTTL,omitempty"`
}

// WorkspaceSessionSpec defines the desired state of a WorkspaceSession.
// One WorkspaceSession represents one persistent /workspace for one
// (Agent, sessionKey) pair. It owns the underlying PVC, so deleting the
// WorkspaceSession (or its parent Agent) cascades to the PVC.
type WorkspaceSessionSpec struct {
	// AgentRef is the name of the Agent that owns this workspace.
	// +kubebuilder:validation:MinLength=1
	AgentRef string `json:"agentRef"`

	// SessionKey is the free-form session identifier this workspace
	// belongs to. Derived K8s identifiers (PVC name, labels) use a
	// deterministic hash of this value.
	// +kubebuilder:validation:MinLength=1
	SessionKey string `json:"sessionKey"`

	// Size requests storage capacity for the PVC (e.g. "1Gi").
	// +kubebuilder:validation:Pattern=`^[0-9]+([.][0-9]+)?(([KMGTPE]i?)|[a-zA-Z]*)?$`
	Size string `json:"size"`

	// StorageClassName selects the StorageClass for the PVC. When empty,
	// the cluster default is used.
	// +optional
	StorageClassName string `json:"storageClassName,omitempty"`

	// IdleTTL is the duration this session may sit idle (no live
	// AgentRun referencing it) before the sweeper reclaims it. When
	// nil, the controller's default applies.
	// +optional
	IdleTTL *metav1.Duration `json:"idleTTL,omitempty"`
}

// WorkspaceSessionPhase describes the lifecycle phase of a WorkspaceSession.
// +kubebuilder:validation:Enum=Pending;Bound;Recreating;Releasing;Failed
type WorkspaceSessionPhase string

const (
	// WorkspaceSessionPhasePending indicates the PVC has not yet been
	// created.
	WorkspaceSessionPhasePending WorkspaceSessionPhase = "Pending"
	// WorkspaceSessionPhaseBound indicates the PVC exists and is
	// available for mounting.
	WorkspaceSessionPhaseBound WorkspaceSessionPhase = "Bound"
	// WorkspaceSessionPhaseRecreating indicates the PVC is being torn
	// down and re-created (e.g. after a storage-class change).
	WorkspaceSessionPhaseRecreating WorkspaceSessionPhase = "Recreating"
	// WorkspaceSessionPhaseReleasing indicates the session has been
	// marked for reclamation and is awaiting deletion.
	WorkspaceSessionPhaseReleasing WorkspaceSessionPhase = "Releasing"
	// WorkspaceSessionPhaseFailed indicates an unrecoverable error.
	WorkspaceSessionPhaseFailed WorkspaceSessionPhase = "Failed"
)

// WorkspaceSessionStatus reports the observed state of a WorkspaceSession.
type WorkspaceSessionStatus struct {
	// Phase is the current lifecycle phase.
	// +optional
	Phase WorkspaceSessionPhase `json:"phase,omitempty"`

	// PVCName is the name of the PersistentVolumeClaim backing this
	// workspace. The controller-managed name has the form
	// "ws-<agent>-<hash>-g<generation>".
	// +optional
	PVCName string `json:"pvcName,omitempty"`

	// PVCGeneration is incremented each time the controller recreates
	// the PVC (e.g. after a storage-class change). The current PVC name
	// embeds this value.
	// +optional
	PVCGeneration int32 `json:"pvcGeneration,omitempty"`

	// LastTouchedAt records when an AgentRun last claimed this session.
	// The sweeper compares this against IdleTTL.
	// +optional
	LastTouchedAt *metav1.Time `json:"lastTouchedAt,omitempty"`

	// LastRunName is the name of the most recent AgentRun that
	// referenced this workspace.
	// +optional
	LastRunName string `json:"lastRunName,omitempty"`

	// RecreatedInfo describes the last recreation event, if any.
	// Wrapper images and the agent-runner surface this to the user when
	// a previously-persistent workspace has been blown away (e.g. PVC
	// lost, storage-class migration).
	// +optional
	RecreatedInfo *WorkspaceRecreatedInfo `json:"recreatedInfo,omitempty"`

	// Conditions represent the latest available observations.
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// WorkspaceRecreatedInfo documents why a workspace PVC was destroyed and
// re-created. Surfaces to the agent via /workspace/.sympozium/state.json
// so the harness can warn the user that prior workspace state is gone.
type WorkspaceRecreatedInfo struct {
	// Reason is a short machine-readable code (e.g. "StorageClassChanged",
	// "PVCLost", "Manual").
	Reason string `json:"reason"`

	// At is when the recreation happened.
	At metav1.Time `json:"at"`

	// Message is an optional human-readable description.
	// +optional
	Message string `json:"message,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=ws
// +kubebuilder:printcolumn:name="Agent",type="string",JSONPath=".spec.agentRef"
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="PVC",type="string",JSONPath=".status.pvcName"
// +kubebuilder:printcolumn:name="LastTouched",type="date",JSONPath=".status.lastTouchedAt"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// WorkspaceSession is a persistent /workspace for one (Agent, sessionKey)
// pair. It owns the backing PVC and is owned by its parent Agent so that
// deleting the Agent cascades to all of its workspaces.
type WorkspaceSession struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkspaceSessionSpec   `json:"spec,omitempty"`
	Status WorkspaceSessionStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkspaceSessionList contains a list of WorkspaceSession.
type WorkspaceSessionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkspaceSession `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkspaceSession{}, &WorkspaceSessionList{})
}
