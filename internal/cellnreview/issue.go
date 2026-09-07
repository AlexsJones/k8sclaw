package cellnreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
)

// IssueOptions are operator/deployment configuration, never tenant fields.
type IssueOptions struct{ Binary, PolicyRoot, ComposerPublisher string }

type IssuedSelection struct {
	APIVersion    string                            `json:"apiVersion"`
	Candidate     cellnauthority.ExecutionCandidate `json:"candidate"`
	Request       json.RawMessage                   `json:"request"`
	Grant         string                            `json:"grant"`
	Profile       string                            `json:"profile"`
	ProfileSHA256 string                            `json:"profileSHA256"`
}

// Issue provisions a local host profile and invokes the real Celln issuer.
// It does not submit an execution or advertise readiness. The caller must
// persist the returned identities and arrange continued withdrawal checks.
func Issue(ctx context.Context, l cellnauthority.ModelLoader, frozen cellnauthority.FrozenSelection, approval cellnauthority.ModelApproval, artifacts cellnauthority.ExecutionArtifacts, o IssueOptions) (*IssuedSelection, error) {
	return issue(ctx, l, frozen, approval, artifacts, o, func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		return runBudget(ctx, 45*time.Second, binary, args...)
	})
}

func issue(ctx context.Context, l cellnauthority.ModelLoader, frozen cellnauthority.FrozenSelection, approval cellnauthority.ModelApproval, artifacts cellnauthority.ExecutionArtifacts, o IssueOptions, execute runner) (result *IssuedSelection, err error) {
	if !filepath.IsAbs(o.Binary) || !filepath.IsAbs(o.PolicyRoot) || len(o.ComposerPublisher) != 64 {
		return nil, fmt.Errorf("absolute operator paths and composer publisher required")
	}
	if _, e := hex.DecodeString(o.ComposerPublisher); e != nil {
		return nil, fmt.Errorf("invalid composer publisher")
	}
	unlock, err := lockIssuer(o.PolicyRoot)
	if err != nil {
		return nil, err
	}
	defer unlock()
	candidate, err := l.BuildExecution(ctx, frozen, approval, artifacts)
	if err != nil {
		return nil, err
	}
	if err := verifyComposition(ctx, execute, o, frozen, artifacts.Closure.Hash); err != nil {
		return nil, err
	}
	credentials, credentialRevision, err := readCredentialMapping(o.PolicyRoot, approval.Policy.CredentialProfile)
	if err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(o.PolicyRoot, ".sympozium-issue-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	requestFile := filepath.Join(stage, "request.json")
	if err := os.WriteFile(requestFile, candidate.Request, 0600); err != nil {
		return nil, err
	}
	var binding struct {
		APIVersion          string `json:"apiVersion"`
		RequestBinding      string `json:"requestBinding"`
		ExecutionAuthorized bool   `json:"executionAuthorized"`
	}
	if err := verify(ctx, execute, o.Binary, []string{"harness-binding", requestFile}, &binding); err != nil {
		return nil, err
	}
	if binding.APIVersion != "celln.dev/harness-request-binding-v1" || !artifactHash(binding.RequestBinding) || binding.ExecutionAuthorized {
		return nil, fmt.Errorf("invalid host request binding")
	}
	p := approval.Policy
	profileBytes, err := json.Marshal(map[string]any{"apiVersion": "celln.dev/model-issuer-profile-v1", "requestBinding": binding.RequestBinding, "credentialFile": credentials, "model": p.Model, "url": p.URL, "maxRequests": p.MaxRequests, "maxOutputTokens": p.MaxOutputTokens, "maxTotalOutputTokens": p.MaxTotalOutputTokens})
	if err != nil {
		return nil, err
	}
	profileDigest := sha256.Sum256(profileBytes)
	name := "symp-" + hex.EncodeToString(profileDigest[:])[:58]
	directory := filepath.Join(o.PolicyRoot, "trusted-model-profiles")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	profilePath := filepath.Join(directory, name+".json")
	if err := l.Revalidate(ctx, frozen, approval); err != nil {
		return nil, err
	}
	// Any later failure withdraws exactly this profile, even if a previous
	// identical retry created it. Existing grant bytes remain inert under v3.
	defer func() {
		if err != nil {
			err = errors.Join(err, removeProfile(profilePath, profileBytes))
		}
	}()
	if err := publishProfile(profilePath, profileBytes); err != nil {
		return nil, err
	}
	var issued struct {
		APIVersion        string          `json:"apiVersion"`
		Grant             string          `json:"grant"`
		Request           json.RawMessage `json:"request"`
		ArtifactReadiness string          `json:"artifactReadiness"`
		Conformance       string          `json:"conformance"`
		Executed          bool            `json:"executed"`
	}
	if err = verify(ctx, execute, o.Binary, []string{"--root", o.PolicyRoot, "harness-grant", requestFile, "--profile", name}, &issued); err != nil {
		return nil, err
	}
	if issued.APIVersion != "celln.dev/harness-issuance-v1" || !artifactHash(issued.Grant) || issued.Executed || issued.ArtifactReadiness != "not_checked" || issued.Conformance != "not_checked" {
		return nil, fmt.Errorf("invalid host issuance report")
	}
	// Re-normalize the returned request with the actual Rust parser. This
	// tolerates wire defaults, but not changed task/identity/authority fields.
	var returned map[string]any
	if err = json.Unmarshal(issued.Request, &returned); err != nil {
		return nil, err
	}
	h, ok := returned["harness"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("issued request missing Harness")
	}
	g, ok := h["modelGrant"].(map[string]any)
	if !ok || g["hash"] != issued.Grant {
		return nil, fmt.Errorf("issued grant mismatch")
	}
	if err = os.WriteFile(requestFile, issued.Request, 0600); err != nil {
		return nil, err
	}
	var rebound struct {
		APIVersion          string `json:"apiVersion"`
		RequestBinding      string `json:"requestBinding"`
		ExecutionAuthorized bool   `json:"executionAuthorized"`
	}
	if err = verify(ctx, execute, o.Binary, []string{"harness-binding", requestFile}, &rebound); err != nil {
		return nil, err
	}
	if rebound.APIVersion != binding.APIVersion || rebound.RequestBinding != binding.RequestBinding || rebound.ExecutionAuthorized {
		return nil, fmt.Errorf("issuer changed the reviewed request")
	}
	if err = l.Revalidate(ctx, frozen, approval); err != nil {
		return nil, err
	}
	_, current, readErr := readCredentialMapping(o.PolicyRoot, p.CredentialProfile)
	if readErr != nil {
		return nil, readErr
	}
	if current != credentialRevision {
		return nil, fmt.Errorf("host credential mapping changed during issuance")
	}
	if err = verifyComposition(ctx, execute, o, frozen, artifacts.Closure.Hash); err != nil {
		return nil, err
	}
	return &IssuedSelection{APIVersion: "sympozium.ai/celln-issued-selection-v1", Candidate: *candidate, Request: issued.Request, Grant: issued.Grant, Profile: name, ProfileSHA256: "sha256:" + hex.EncodeToString(profileDigest[:])}, nil
}

func readCredentialMapping(root, name string) (string, [32]byte, error) {
	raw, err := readLimit(filepath.Join(root, "model-credentials.json"), 65536)
	if err != nil {
		return "", [32]byte{}, err
	}
	var doc struct {
		APIVersion string            `json:"apiVersion"`
		Profiles   map[string]string `json:"profiles"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&doc); err != nil {
		return "", [32]byte{}, fmt.Errorf("invalid host credential mapping")
	}
	if d.Decode(new(any)) != io.EOF {
		return "", [32]byte{}, fmt.Errorf("trailing host credential mapping")
	}
	path, ok := doc.Profiles[name]
	if doc.APIVersion != "sympozium.ai/celln-host-credentials-v1" || !ok || !filepath.IsAbs(path) {
		return "", [32]byte{}, fmt.Errorf("credential profile not mapped by host operator")
	}
	return path, sha256.Sum256(raw), nil
}

func verifyComposition(ctx context.Context, execute runner, o IssueOptions, frozen cellnauthority.FrozenSelection, hash string) error {
	path, err := storeObject(o.PolicyRoot, "closures", hash)
	if err != nil {
		return err
	}
	raw, err := readLimit(path, 262144)
	if err != nil {
		return err
	}
	var descriptor struct {
		Closure struct {
			APIVersion string `json:"apiVersion"`
			Sources    []struct {
				Hash string `json:"hash"`
			} `json:"sources"`
		} `json:"closure"`
	}
	if err := json.Unmarshal(raw, &descriptor); err != nil {
		return fmt.Errorf("invalid composed descriptor")
	}
	sources := []string{}
	for _, s := range descriptor.Closure.Sources {
		sources = append(sources, s.Hash)
	}
	if descriptor.Closure.APIVersion != "celln.dev/closure-v2" || !reflect.DeepEqual(sources, frozen.Prepared.Composition.Sources) {
		return fmt.Errorf("composed sources differ from reviewed selection")
	}
	var report closureReport
	// Verify the very bytes whose source list was compared above, not a store
	// pathname that could be replaced between our read and the subprocess read.
	copy, err := os.CreateTemp(o.PolicyRoot, ".verify-composition-")
	if err != nil {
		return err
	}
	defer os.Remove(copy.Name())
	defer copy.Close()
	if _, err := copy.Write(raw); err != nil {
		return err
	}
	if err := copy.Sync(); err != nil {
		return err
	}
	if err := verify(ctx, execute, o.Binary, []string{"--root", o.PolicyRoot, "closure", "verify", copy.Name(), "--expected-hash", hash, "--publisher", o.ComposerPublisher, "--entry-point", frozen.Prepared.RuntimeEntryPoint, "--executable", frozen.Prepared.RuntimeExecutable.Hash}, &report); err != nil {
		return err
	}
	if report.APIVersion != "celln.dev/closure-verification-v1" || report.Scope != "descriptor-authenticity-only" || report.Closure != hash || report.Publisher != o.ComposerPublisher || report.EntryPoint != frozen.Prepared.RuntimeEntryPoint || report.ClosureEntryPoint != frozen.Prepared.RuntimeEntryPoint || report.Executable != frozen.Prepared.RuntimeExecutable.Hash || report.Interpreter || !artifactHash(report.PolicyHash) || report.Conformance != "not_checked" || report.ArtifactReadiness != "not_checked" {
		return fmt.Errorf("unbound composed descriptor verification")
	}
	after, err := readLimit(path, 262144)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, after) {
		return fmt.Errorf("composition changed during verification")
	}
	return nil
}

func readLimit(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("operator document exceeds byte ceiling")
	}
	return b, nil
}

func publishProfile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".profile-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		if !os.IsExist(err) {
			return err
		}
		old, err := readLimit(path, 65536)
		if err != nil {
			return err
		}
		if !bytes.Equal(old, data) {
			return fmt.Errorf("refusing to overwrite existing host profile")
		}
	}
	return syncDirectory(filepath.Dir(path))
}

func removeProfile(path string, expected []byte) error {
	actual, err := readLimit(path, 65536)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("host profile changed; refusing unrelated deletion")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// Withdraw removes only the exact profile named by a persisted issuer result.
// Serialize with issuance and never delete grant/audit records or credentials.
func Withdraw(root string, issued IssuedSelection) error {
	if !filepath.IsAbs(root) || !strings.HasPrefix(issued.Profile, "symp-") || len(issued.Profile) != 63 || len(issued.ProfileSHA256) != 71 || !strings.HasPrefix(issued.ProfileSHA256, "sha256:") {
		return fmt.Errorf("invalid issued profile identity")
	}
	if _, err := hex.DecodeString(issued.Profile[5:]); err != nil {
		return err
	}
	unlock, err := lockIssuer(root)
	if err != nil {
		return err
	}
	defer unlock()
	path := filepath.Join(root, "trusted-model-profiles", issued.Profile+".json")
	data, err := readLimit(path, 65536)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	if "sha256:"+hex.EncodeToString(digest[:]) != issued.ProfileSHA256 {
		return fmt.Errorf("profile revision changed")
	}
	return removeProfile(path, data)
}
