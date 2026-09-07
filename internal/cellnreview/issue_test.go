package cellnreview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type issuerFixture struct {
	l         cellnauthority.ModelLoader
	f         *cellnauthority.FrozenSelection
	a         *cellnauthority.ModelApproval
	artifacts cellnauthority.ExecutionArtifacts
	o         IssueOptions
	c         client.Client
	run       runner
}

func provisionFixture(t *testing.T) issuerFixture {
	t.Helper()
	ctx := context.Background()
	l, old, o, _ := compositionFixture(t)
	c := l.Reader.(client.Client)
	key := types.NamespacedName{Namespace: old.Run.Namespace, Name: old.Run.Name}
	var run api.AgentRun
	if err := c.Get(ctx, key, &run); err != nil {
		t.Fatal(err)
	}
	run.Spec.Model = api.ModelSpec{Provider: "deepseek", Model: "deepseek-chat"}
	if err := c.Update(ctx, &run); err != nil {
		t.Fatal(err)
	}
	f, err := l.FreezeRun(ctx, key, nil, 33554432)
	if err != nil {
		t.Fatal(err)
	}
	doc := cellnauthority.ModelPolicyDocument{APIVersion: "sympozium.ai/celln-model-policy-v1", Agent: f.Snapshot.Agent, Runtime: f.Snapshot.Runtime, Provider: "deepseek", Model: "deepseek-chat", URL: "https://api.deepseek.com/chat/completions", CredentialProfile: "host-deepseek", MaxRequests: 1, MaxOutputTokens: 512, MaxTotalOutputTokens: 512}
	raw, _ := json.Marshal(doc)
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "operators", Name: "model", UID: "model-policy"}, Data: map[string]string{"model-policy.json": string(raw)}}
	if err := c.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	ml := cellnauthority.ModelLoader{Selection: l, Source: client.ObjectKeyFromObject(cm)}
	a, err := ml.Resolve(ctx, *f)
	if err != nil {
		t.Fatal(err)
	}
	hash := "blake3:" + strings.Repeat("c", 64)
	artifacts := cellnauthority.ExecutionArtifacts{Mote: api.CellnImmutableRef{Hash: hash}, Closure: api.CellnImmutableRef{Hash: hash}}
	options := IssueOptions{Binary: o.Binary, PolicyRoot: o.PolicyRoot, ComposerPublisher: strings.Repeat("d", 64)}
	path, err := storeObject(o.PolicyRoot, "closures", hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(map[string]any{"closure": map[string]any{"apiVersion": "celln.dev/closure-v2", "sources": []map[string]string{{"hash": f.Prepared.Composition.Sources[0]}}}})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.PolicyRoot, "model-credentials.json"), []byte(`{"apiVersion":"sympozium.ai/celln-host-credentials-v1","profiles":{"host-deepseek":"/never-read-host-credential"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	execute := func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary != o.Binary {
			t.Fatal("binary substituted")
		}
		if args[0] == "harness-binding" {
			// A deterministic stand-in tests orchestration, not BLAKE3 or the
			// real Rust parser. Real CLI/KVM proof is a separate test gate.
			raw, err := os.ReadFile(args[1])
			if err != nil {
				return nil, err
			}
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, err
			}
			value["harness"].(map[string]any)["modelGrant"] = map[string]any{"hash": "placeholder"}
			normalized, _ := json.Marshal(value)
			h := sha256.Sum256(normalized)
			return json.Marshal(map[string]any{"apiVersion": "celln.dev/harness-request-binding-v1", "requestBinding": "blake3:" + hex.EncodeToString(h[:]), "executionAuthorized": false})
		}
		if args[2] == "closure" {
			if args[4] == path {
				t.Fatal("verification must use captured descriptor bytes")
			}
			return json.Marshal(closureReport{APIVersion: "celln.dev/closure-verification-v1", Scope: "descriptor-authenticity-only", Closure: hash, Publisher: options.ComposerPublisher, EntryPoint: f.Prepared.RuntimeEntryPoint, ClosureEntryPoint: f.Prepared.RuntimeEntryPoint, Executable: f.Prepared.RuntimeExecutable.Hash, PolicyHash: hash, ArtifactReadiness: "not_checked", Conformance: "not_checked"})
		}
		if args[2] != "harness-grant" {
			return nil, fmt.Errorf("unexpected command")
		}
		profilePath := filepath.Join(o.PolicyRoot, "trusted-model-profiles", args[5]+".json")
		profile, err := os.ReadFile(profilePath)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(string(profile), "/never-read-host-credential") {
			t.Fatal("operator credential mapping missing")
		}
		raw, err := os.ReadFile(args[3])
		if err != nil {
			return nil, err
		}
		var value map[string]any
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		value["harness"].(map[string]any)["modelGrant"] = map[string]any{"hash": hash}
		if err := os.WriteFile(filepath.Join(o.PolicyRoot, "grant-audit-sentinel"), []byte("retain"), 0600); err != nil {
			return nil, err
		}
		return json.Marshal(map[string]any{"apiVersion": "celln.dev/harness-issuance-v1", "grant": hash, "request": value, "artifactReadiness": "not_checked", "conformance": "not_checked", "executed": false})
	}
	return issuerFixture{ml, f, a, artifacts, options, c, execute}
}

func TestProvisioningPublishesRetriesAndWithdrawsExactProfile(t *testing.T) {
	f := provisionFixture(t)
	ctx := context.Background()
	first, err := issue(ctx, f.l, *f.f, *f.a, f.artifacts, f.o, f.run)
	if err != nil {
		t.Fatal(err)
	}
	second, err := issue(ctx, f.l, *f.f, *f.a, f.artifacts, f.o, f.run)
	if err != nil {
		t.Fatal(err)
	}
	if first.Profile != second.Profile || first.Grant != second.Grant {
		t.Fatal("retry retargeted issuance")
	}
	if err := Withdraw(f.o.PolicyRoot, *first); err != nil {
		t.Fatal(err)
	}
	if err := Withdraw(f.o.PolicyRoot, *first); err != nil {
		t.Fatal(err)
	}
	assertNoProfiles(t, f.o.PolicyRoot)
	if data, err := os.ReadFile(filepath.Join(f.o.PolicyRoot, "grant-audit-sentinel")); err != nil || string(data) != "retain" {
		t.Fatal("withdrawal modified audit/grant data")
	}
}

func assertNoProfiles(t *testing.T, root string) {
	t.Helper()
	files, err := os.ReadDir(filepath.Join(root, "trusted-model-profiles"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Fatalf("live profiles retained: %v", files)
	}
}

func TestProvisioningFailuresRemoveProfileAndReleaseLock(t *testing.T) {
	for _, mode := range []string{"issuer-failed", "bad-report", "request-substitution", "approval-withdrawn", "credential-mapping-changed", "second-composition-check"} {
		t.Run(mode, func(t *testing.T) {
			f := provisionFixture(t)
			checks := 0
			execute := func(ctx context.Context, binary string, args ...string) ([]byte, error) {
				if args[0] == "--root" && args[2] == "closure" {
					checks++
					if mode == "second-composition-check" && checks == 2 {
						return nil, fmt.Errorf("policy withdrawn")
					}
				}
				out, err := f.run(ctx, binary, args...)
				if err != nil {
					return nil, err
				}
				if args[0] == "--root" && args[2] == "harness-grant" {
					switch mode {
					case "issuer-failed":
						return nil, fmt.Errorf("issuer interrupted after publication")
					case "bad-report":
						return []byte(`{"apiVersion":"wrong"}`), nil
					case "request-substitution":
						var v map[string]any
						_ = json.Unmarshal(out, &v)
						v["request"].(map[string]any)["harness"].(map[string]any)["task"] = "other task"
						return json.Marshal(v)
					case "approval-withdrawn":
						var cm corev1.ConfigMap
						if err := f.c.Get(ctx, f.l.Source, &cm); err != nil {
							t.Fatal(err)
						}
						if err := f.c.Delete(ctx, &cm); err != nil {
							t.Fatal(err)
						}
					case "credential-mapping-changed":
						if err := os.WriteFile(filepath.Join(f.o.PolicyRoot, "model-credentials.json"), []byte("{}"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				}
				return out, nil
			}
			if _, err := issue(context.Background(), f.l, *f.f, *f.a, f.artifacts, f.o, execute); err == nil {
				t.Fatal("accepted failed issuance")
			}
			assertNoProfiles(t, f.o.PolicyRoot)
			unlock, err := lockIssuer(f.o.PolicyRoot)
			if err != nil {
				t.Fatal("lock leaked")
			}
			unlock()
		})
	}
}

func TestProvisioningLockAndWithdrawRefuseUnrelatedState(t *testing.T) {
	f := provisionFixture(t)
	unlock, err := lockIssuer(f.o.PolicyRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issue(context.Background(), f.l, *f.f, *f.a, f.artifacts, f.o, f.run); err == nil {
		t.Fatal("concurrent issuer admitted")
	}
	unlock()
	issued, err := issue(context.Background(), f.l, *f.f, *f.a, f.artifacts, f.o, f.run)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.o.PolicyRoot, "trusted-model-profiles", issued.Profile+".json")
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Withdraw(f.o.PolicyRoot, *issued); err == nil {
		t.Fatal("removed changed profile")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "replacement" {
		t.Fatal("unrelated state changed")
	}
}
