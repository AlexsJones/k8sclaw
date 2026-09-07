package cellnauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// ModelPolicyDocument is independent of tool approvals. It lives under
// model-policy.json in a distinct operator-configured ConfigMap. CredentialProfile
// is an opaque host-side mapping name, never a credential, path or tenant Secret.
type ModelPolicyDocument struct {
	APIVersion           string  `json:"apiVersion"`
	Agent                Subject `json:"agent"`
	Runtime              Subject `json:"runtime"`
	Provider             string  `json:"provider"`
	Model                string  `json:"model"`
	URL                  string  `json:"url"`
	CredentialProfile    string  `json:"credentialProfile"`
	MaxRequests          int64   `json:"maxRequests"`
	MaxOutputTokens      int64   `json:"maxOutputTokens"`
	MaxTotalOutputTokens int64   `json:"maxTotalOutputTokens"`
}

// ModelApproval is a frozen control-plane policy observation, not a host grant.
// Issuance must additionally bind verified runnable artifact identities, resolve
// the profile through independent host configuration, and revalidate this record.
type ModelApproval struct {
	APIVersion      string              `json:"apiVersion"`
	SelectionSHA256 string              `json:"selectionSHA256"`
	Caller          string              `json:"caller"`
	Source          SourceRevision      `json:"source"`
	Policy          ModelPolicyDocument `json:"policy"`
}

type ModelLoader struct {
	Selection Loader
	Source    types.NamespacedName
}

var credentialProfileName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func (l ModelLoader) Resolve(ctx context.Context, frozen FrozenSelection) (*ModelApproval, error) {
	if l.Selection.Reader == nil || l.Source.Namespace == "" || l.Source.Name == "" {
		return nil, fmt.Errorf("independent configured model policy source required")
	}
	for _, toolSource := range []types.NamespacedName{l.Selection.OperatorSource, l.Selection.RuntimeSource, l.Selection.AgentSource} {
		if l.Source == toolSource {
			return nil, fmt.Errorf("model policy must be separate from tool approval sources")
		}
	}
	if err := l.Selection.Revalidate(ctx, frozen); err != nil {
		return nil, err
	}
	doc, revision, err := l.read(ctx)
	if err != nil {
		return nil, err
	}
	if doc.APIVersion != "sympozium.ai/celln-model-policy-v1" || doc.Agent != frozen.Snapshot.Agent || doc.Runtime != frozen.Snapshot.Runtime || doc.Provider != "deepseek" || doc.URL != "https://api.deepseek.com/chat/completions" || len(doc.Model) == 0 || len(doc.Model) > 128 || strings.TrimSpace(doc.Model) != doc.Model || strings.ContainsRune(doc.Model, '\x00') || !credentialProfileName.MatchString(doc.CredentialProfile) || doc.MaxRequests < 1 || doc.MaxRequests > 6 || doc.MaxRequests < frozen.Prepared.JSON.MaxTurns || doc.MaxOutputTokens != 512 || doc.MaxTotalOutputTokens < frozen.Prepared.JSON.MaxTurns*512 || doc.MaxTotalOutputTokens > 3072 {
		return nil, fmt.Errorf("model policy is stale or outside the supported host contract")
	}
	run, id, err := l.Selection.readRun(ctx, types.NamespacedName{Namespace: frozen.Run.Namespace, Name: frozen.Run.Name})
	if err != nil {
		return nil, err
	}
	m := run.Spec.Model
	if id != frozen.Run || m.Provider != doc.Provider || m.Model != doc.Model || m.AuthSecretRef != "" || (m.BaseURL != "" && m.BaseURL != "https://api.deepseek.com") || (m.Thinking != "" && m.Thinking != "off") || len(m.ProviderHeaders) != 0 || m.ProviderHeadersSecretRef != "" || m.ModelRef != "" || len(m.NodeSelector) != 0 {
		return nil, fmt.Errorf("run model or credential selection is not authorized")
	}
	// No credential contents are read. A second read detects observed withdrawal
	// or replacement, but does not create a transaction, lease or fleet guarantee.
	if err := l.Selection.Revalidate(ctx, frozen); err != nil {
		return nil, err
	}
	_, current, err := l.read(ctx)
	if err != nil {
		return nil, err
	}
	if current != revision {
		return nil, fmt.Errorf("model policy changed during resolution")
	}
	raw, err := json.Marshal(frozen)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	return &ModelApproval{APIVersion: "sympozium.ai/celln-model-approval-v1", SelectionSHA256: "sha256:" + hex.EncodeToString(digest[:]), Caller: fmt.Sprintf("sympozium:%s/%s", frozen.Run.Namespace, frozen.Run.Name), Source: revision, Policy: doc}, nil
}

func (l ModelLoader) Revalidate(ctx context.Context, frozen FrozenSelection, approval ModelApproval) error {
	current, err := l.Resolve(ctx, frozen)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(*current, approval) {
		return fmt.Errorf("frozen model approval changed")
	}
	return nil
}

func (l ModelLoader) read(ctx context.Context) (ModelPolicyDocument, SourceRevision, error) {
	var cm corev1.ConfigMap
	if err := l.Selection.Reader.Get(ctx, l.Source, &cm); err != nil {
		return ModelPolicyDocument{}, SourceRevision{}, err
	}
	raw, ok := cm.Data["model-policy.json"]
	if !ok || len(raw) > 65536 || cm.UID == "" || cm.ResourceVersion == "" || cm.DeletionTimestamp != nil {
		return ModelPolicyDocument{}, SourceRevision{}, fmt.Errorf("model policy source unavailable or invalid")
	}
	d := json.NewDecoder(bytes.NewBufferString(raw))
	d.DisallowUnknownFields()
	var doc ModelPolicyDocument
	if err := d.Decode(&doc); err != nil {
		return doc, SourceRevision{}, fmt.Errorf("invalid model policy document")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return doc, SourceRevision{}, fmt.Errorf("trailing model policy data")
	}
	digest := sha256.Sum256([]byte(raw))
	return doc, SourceRevision{cm.Namespace, cm.Name, cm.UID, cm.ResourceVersion, "sha256:" + hex.EncodeToString(digest[:])}, nil
}
