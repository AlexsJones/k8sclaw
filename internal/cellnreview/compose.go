package cellnreview

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
)

type ComposeOptions struct {
	Binary     string
	PolicyRoot string
	KeyFile    string
	OutputDir  string
}

type CompositionReport struct {
	APIVersion        string   `json:"apiVersion"`
	PlanHash          string   `json:"planHash"`
	PolicyHash        string   `json:"policyHash"`
	Closure           string   `json:"closure"`
	Toolfs            string   `json:"toolfs"`
	Sources           []string `json:"sources"`
	ArtifactReadiness string   `json:"artifactReadiness"`
	Conformance       string   `json:"conformance"`
}

// Compose builds local artifacts only. The caller supplies operator-owned paths
// and grant sources; no request-provided command, source URL or credential is
// executed. A refused post-build revalidation leaves diagnostic artifacts, not
// an admission or runnable plan. No Kubernetes state is written.
func Compose(ctx context.Context, loader cellnauthority.Loader, frozen cellnauthority.FrozenSelection, options ComposeOptions) (*CompositionReport, error) {
	return compose(ctx, loader, frozen, options, func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		return runBudget(ctx, 60*time.Second, binary, args...)
	})
}

func compose(ctx context.Context, loader cellnauthority.Loader, frozen cellnauthority.FrozenSelection, o ComposeOptions, execute runner) (*CompositionReport, error) {
	for _, path := range []string{o.Binary, o.PolicyRoot, o.KeyFile, o.OutputDir} {
		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("composition paths must be absolute operator paths")
		}
	}
	if _, err := os.Lstat(o.OutputDir); err == nil {
		return nil, fmt.Errorf("composition output already exists")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := loader.Revalidate(ctx, frozen); err != nil {
		return nil, err
	}
	p := frozen.Snapshot.RuntimeSpec.Celln
	if p == nil {
		return nil, fmt.Errorf("missing runtime profile")
	}
	var policyHash string
	check := func(hash, publisher, entry, executable string) error {
		path, err := storeObject(o.PolicyRoot, "closures", hash)
		if err != nil {
			return err
		}
		var report closureReport
		args := []string{"--root", o.PolicyRoot, "closure", "verify", path, "--expected-hash", hash, "--publisher", publisher, "--entry-point", entry, "--executable", executable}
		if err := verify(ctx, execute, o.Binary, args, &report); err != nil {
			return err
		}
		if report.APIVersion != "celln.dev/closure-verification-v1" || report.Scope != "descriptor-authenticity-only" || report.Interpreter || report.Closure != hash || report.Publisher != publisher || report.EntryPoint != entry || report.ClosureEntryPoint != entry || report.Executable != executable || !artifactHash(report.PolicyHash) || report.ArtifactReadiness != "not_checked" || report.Conformance != "not_checked" {
			return fmt.Errorf("source verification identity mismatch")
		}
		if policyHash != "" && policyHash != report.PolicyHash {
			return fmt.Errorf("source policy changed during composition")
		}
		policyHash = report.PolicyHash
		return nil
	}
	if err := check(p.Closure.Hash, p.PublisherKey, p.EntryPoint, p.Executable.Hash); err != nil {
		return nil, err
	}
	for _, tool := range frozen.Snapshot.Tools {
		s := tool.Spec
		if err := check(s.Closure.Hash, s.PublisherKey, s.EntryPoint, s.Executable.Hash); err != nil {
			return nil, err
		}
		for _, hash := range []string{s.ArgumentsSchema.Hash, s.ResultSchema.Hash} {
			path, err := storeObject(o.PolicyRoot, "tool-schemas", hash)
			if err != nil {
				return nil, err
			}
			var report schemaReport
			if err := verify(ctx, execute, o.Binary, []string{"schema", "verify", path, "--expected-hash", hash}, &report); err != nil {
				return nil, err
			}
			if report.Schema != hash || report.APIVersion != "celln.dev/tool-schema-verification-v1" || report.Profile != "celln.tool-schema/v1" || report.Scope != "schema-and-data-only" || report.ValueValidated {
				return nil, fmt.Errorf("schema verification identity mismatch")
			}
		}
	}
	staging, err := os.MkdirTemp("", "sympozium-celln-composition-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	bytes, err := json.Marshal(frozen.Prepared.Composition)
	if err != nil {
		return nil, err
	}
	planFile := filepath.Join(staging, "plan.json")
	if err := os.WriteFile(planFile, bytes, 0600); err != nil {
		return nil, err
	}
	if err := loader.Revalidate(ctx, frozen); err != nil {
		return nil, err
	}
	var report CompositionReport
	if err := verify(ctx, execute, o.Binary, []string{"--root", o.PolicyRoot, "closure", "compose", planFile, "--key-file", o.KeyFile, "--output-dir", o.OutputDir}, &report); err != nil {
		return nil, err
	}
	if report.APIVersion != "celln.dev/composition-report-v1" || !artifactHash(report.PlanHash) || !artifactHash(report.Closure) || !artifactHash(report.Toolfs) || report.PolicyHash != policyHash || !reflect.DeepEqual(report.Sources, frozen.Prepared.Composition.Sources) || report.ArtifactReadiness != "not_checked" || report.Conformance != "not_checked" {
		return nil, fmt.Errorf("composition report mismatch")
	}
	if err := loader.Revalidate(ctx, frozen); err != nil {
		return nil, err
	}
	return &report, nil
}

func storeObject(root, store, hash string) (string, error) {
	if !artifactHash(hash) {
		return "", fmt.Errorf("invalid artifact identity")
	}
	hex := hash[7:]
	return filepath.Join(root, store, "objects", hex[:2], hex), nil
}
