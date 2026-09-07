// Package cellnreview implements operator-driven catalogue publication. It
// never distributes artifacts, grants execution, or writes positive readiness.
package cellnreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Options struct {
	Namespace, Name string
	// Both are explicit operator acknowledgements, not values taken from a
	// submission annotation or status. Inspect, review behavior, then approve.
	SubmissionUID      types.UID
	ReviewedSpecSHA256 string
	// All paths are supplied by the operator, never by the submission.
	Binary, PolicyRoot, BundleDir string
}

type runner func(context.Context, string, ...string) ([]byte, error)

func Inspect(ctx context.Context, c client.Client, namespace, name string) (*api.CellnToolSubmission, cellnauthority.ToolIdentity, error) {
	var s api.CellnToolSubmission
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &s); err != nil {
		return nil, cellnauthority.ToolIdentity{}, err
	}
	id, err := cellnauthority.Identify(api.CellnTool{ObjectMeta: s.ObjectMeta, Spec: s.Spec})
	return &s, id, err
}

// Approve creates one catalogue revision under the caller's Kubernetes identity.
// The API server still requires reviewer RBAC. No controller/service credential
// is borrowed and no existing catalogue object is modified on conflict.
func Approve(ctx context.Context, c client.Client, o Options) (*api.CellnTool, error) {
	return approve(ctx, c, o, run)
}

func approve(ctx context.Context, c client.Client, o Options, execute runner) (*api.CellnTool, error) {
	if o.Namespace == "" || o.Name == "" || o.SubmissionUID == "" || o.ReviewedSpecSHA256 == "" {
		return nil, fmt.Errorf("namespace, submission name, reviewed UID and spec SHA256 are required")
	}
	for _, path := range []string{o.Binary, o.PolicyRoot, o.BundleDir} {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("operator binary, policy root and bundle paths must be absolute")
		}
	}
	submission, identity, err := Inspect(ctx, c, o.Namespace, o.Name)
	if err != nil {
		return nil, err
	}
	if identity.UID != o.SubmissionUID || identity.SpecSHA256 != o.ReviewedSpecSHA256 {
		return nil, fmt.Errorf("submission changed since operator review")
	}
	s := submission.Spec
	closureArgs := []string{"--root", o.PolicyRoot, "closure", "verify", filepath.Join(o.BundleDir, "closure.json"),
		"--expected-hash", s.Closure.Hash, "--publisher", s.PublisherKey, "--entry-point", s.EntryPoint,
		"--executable", s.Executable.Hash, "--toolfs", filepath.Join(o.BundleDir, "toolfs.ext2")}
	verifyClosure := func() (closureReport, error) {
		var report closureReport
		err := verify(ctx, execute, o.Binary, closureArgs, &report)
		if err != nil {
			return report, err
		}
		if report.APIVersion != "celln.dev/closure-verification-v1" || report.Scope != "descriptor-and-local-toolfs-bytes" ||
			report.Closure != s.Closure.Hash || report.Publisher != s.PublisherKey || report.EntryPoint != s.EntryPoint || report.Executable != s.Executable.Hash ||
			!report.LocalToolfsVerified || report.LocalToolfsBytes < 1 || report.LocalToolfsBytes > 512<<20 ||
			report.ArtifactReadiness != "not_checked" || report.Conformance != "not_checked" || !artifactHash(report.PolicyHash) || !artifactHash(report.Toolfs) {
			return report, fmt.Errorf("incompatible or unbound closure verification report")
		}
		if report.Interpreter && s.Lane == "tool" {
			return report, fmt.Errorf("interpreter closure cannot be published as tool-lane code")
		}
		return report, nil
	}
	closure, err := verifyClosure()
	if err != nil {
		return nil, err
	}
	for _, schema := range []struct{ name, hash string }{{"arguments.schema.json", s.ArgumentsSchema.Hash}, {"result.schema.json", s.ResultSchema.Hash}} {
		var report schemaReport
		if err := verify(ctx, execute, o.Binary, []string{"schema", "verify", filepath.Join(o.BundleDir, schema.name), "--expected-hash", schema.hash}, &report); err != nil {
			return nil, err
		}
		if report.APIVersion != "celln.dev/tool-schema-verification-v1" || report.Profile != "celln.tool-schema/v1" || report.Scope != "schema-and-data-only" || report.Schema != schema.hash || report.ValueValidated {
			return nil, fmt.Errorf("incompatible or unbound schema verification report")
		}
	}
	// Re-read policy/artifact identity after schema verification and object
	// identity immediately before publication. A snapshot is not a future lease.
	current, err := verifyClosure()
	if err != nil {
		return nil, err
	}
	if current != closure {
		return nil, fmt.Errorf("closure or policy changed during review")
	}
	_, latest, err := Inspect(ctx, c, o.Namespace, o.Name)
	if err != nil {
		return nil, err
	}
	if latest != identity {
		return nil, fmt.Errorf("submission changed during verification")
	}
	tool := &api.CellnTool{
		TypeMeta: metav1.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "CellnTool"},
		ObjectMeta: metav1.ObjectMeta{Namespace: o.Namespace, Name: o.Name, Annotations: map[string]string{
			"celln.sympozium.ai/reviewed-submission-uid": string(identity.UID),
			"celln.sympozium.ai/reviewed-spec-sha256":    identity.SpecSHA256,
			"celln.sympozium.ai/review-policy-hash":      closure.PolicyHash,
			"celln.sympozium.ai/review-toolfs-hash":      closure.Toolfs,
			"celln.sympozium.ai/review-scope":            "operator-reviewed-local-bytes-and-schemas-only",
		}},
		Spec: *s.DeepCopy(),
	}
	// Do not copy labels, owner references, annotations or forged status from
	// the untrusted submission. Publication is not conformance or Ready status.
	if err := c.Create(ctx, tool); err != nil {
		return nil, err
	}
	return tool, nil
}

type closureReport struct {
	Interpreter         bool   `json:"interpreter"`
	APIVersion          string `json:"apiVersion"`
	Scope               string `json:"scope"`
	Closure             string `json:"closure"`
	PolicyHash          string `json:"policyHash"`
	Publisher           string `json:"publisher"`
	EntryPoint          string `json:"entryPoint"`
	ClosureEntryPoint   string `json:"closureEntryPoint"`
	Executable          string `json:"executable"`
	Toolfs              string `json:"toolfs"`
	LocalToolfsVerified bool   `json:"localToolfsVerified"`
	LocalToolfsBytes    int64  `json:"localToolfsBytes"`
	ArtifactReadiness   string `json:"artifactReadiness"`
	Conformance         string `json:"conformance"`
}
type schemaReport struct {
	APIVersion     string `json:"apiVersion"`
	Profile        string `json:"profile"`
	Schema         string `json:"schema"`
	Scope          string `json:"scope"`
	ValueValidated bool   `json:"valueValidated"`
}

func artifactHash(s string) bool {
	if len(s) != 71 || s[:7] != "blake3:" {
		return false
	}
	for _, ch := range s[7:] {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func verify(ctx context.Context, execute runner, binary string, args []string, report any) error {
	data, err := execute(ctx, binary, args...)
	if err != nil {
		return fmt.Errorf("local Celln verification refused: %w", err)
	}
	if len(data) > 1<<20 {
		return fmt.Errorf("verification report exceeds byte ceiling")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(report); err != nil {
		return fmt.Errorf("invalid verification report: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing verification output")
	}
	return nil
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > (1<<20)-b.Len() {
		return 0, fmt.Errorf("verification output exceeds byte ceiling")
	}
	return b.Buffer.Write(p)
}

func run(ctx context.Context, binary string, args ...string) ([]byte, error) {
	return runBudget(ctx, 30*time.Second, binary, args...)
}

func runBudget(ctx context.Context, budget time.Duration, binary string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	// In particular, never pass provider credentials or tenant-selected env.
	cmd.Env = []string{"LANG=C"}
	cmd.WaitDelay = time.Second
	var stdout, stderr boundedOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// External stderr can contain local paths; do not publish it in status.
		return nil, err
	}
	return stdout.Bytes(), nil
}
