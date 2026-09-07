package v1alpha1

// CellnIssuanceStatus is a write-once payload and monotonic provisioning outcome.
// +kubebuilder:validation:XValidation:rule="self.phase == oldSelf.phase || (oldSelf.phase == 'Prepared' && self.phase == 'Issued')",message="Celln issuance phase cannot regress"
// +kubebuilder:validation:XValidation:rule="self.phase != 'Issued' || (has(self.result) && size(self.result) > 0)",message="issued outcome requires a durable result"
// +kubebuilder:validation:XValidation:rule="self.phase != 'Prepared' || !has(self.result) || size(self.result) == 0",message="prepared issuance cannot contain an outcome"
type CellnIssuanceStatus struct {
	// +kubebuilder:validation:Enum=Prepared;Issued
	// +kubebuilder:validation:MaxLength=8
	Phase string `json:"phase"`
	// Target pins the operator-configured issuer endpoint, not a tenant URL.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="issuer target is immutable"
	Target string `json:"target"`
	// Payload contains the frozen observation and expected execution candidate.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=393216
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="issuance payload is immutable"
	Payload string `json:"payload"`
	// +kubebuilder:validation:Pattern=`^sha256:[0-9a-f]{64}$`
	// +kubebuilder:validation:MaxLength=71
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="issuance payload hash is immutable"
	PayloadSHA256 string `json:"payloadSHA256"`
	// Result contains the validated remote outcome, never provider credentials.
	// +kubebuilder:validation:MaxLength=196608
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="issued result is immutable"
	// +optional
	Result string `json:"result,omitempty"`
}
