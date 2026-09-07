package cellnreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func compositionFixture(t *testing.T) (cellnauthority.Loader, *cellnauthority.FrozenSelection, ComposeOptions, runner) {
	t.Helper()
	ctx := context.Background()
	hash := "blake3:" + strings.Repeat("a", 64)
	ref := api.CellnImmutableRef{Hash: hash}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "agent", UID: "agent", Generation: 1}, Spec: api.AgentSpec{RuntimeRef: "runtime"}}
	rt := &api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "runtime", UID: "runtime", Generation: 1}, Spec: api.AgentRuntimeSpec{Celln: &api.AgentRuntimeCellnProfile{Revision: "v1", ContractVersion: "celln.json-tools/v1", Executable: ref, Closure: ref, Mote: ref, PublisherKey: strings.Repeat("b", 64), EntryPoint: "/harness", Platform: "linux/amd64", Lane: "agent", Lifecycle: "disposable-one-shot", JSON: &api.CellnHarnessJSONLimits{MaxTurns: 1, MaxCalls: 0}, Limits: api.AgentRuntimeCellnLimits{TimeoutMillis: 1000, MemoryBytes: 268435456, TaskBytes: 2048, OutputBytes: 1024, Workspace: "none"}}}}
	var run api.AgentRun
	if err := json.Unmarshal([]byte(`{"metadata":{"namespace":"tenant","name":"run","uid":"run","generation":1},"spec":{"agentRef":"agent","backend":"celln","task":"answer"}}`), &run); err != nil {
		t.Fatal(err)
	}
	aid, _ := cellnauthority.IdentifySubject("Agent", agent.ObjectMeta, agent.Spec)
	rid, _ := cellnauthority.IdentifySubject("AgentRuntime", rt.ObjectMeta, rt.Spec)
	objects := []client.Object{agent, rt, &run}
	for _, layer := range []string{"operator", "runtime", "agent"} {
		raw, _ := json.Marshal(cellnauthority.GrantDocument{APIVersion: "sympozium.ai/celln-grants-v1", Layer: layer, Agent: aid, Runtime: rid, Grants: []cellnauthority.Grant{}})
		objects = append(objects, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "operators", Name: layer, UID: types.UID(layer), ResourceVersion: "1"}, Data: map[string]string{"grants.json": string(raw)}})
	}
	scheme := runtime.NewScheme()
	_ = api.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.AgentRun{}).WithObjects(objects...).Build()
	loader := cellnauthority.Loader{Reader: c, OperatorSource: types.NamespacedName{Namespace: "operators", Name: "operator"}, RuntimeSource: types.NamespacedName{Namespace: "operators", Name: "runtime"}, AgentSource: types.NamespacedName{Namespace: "operators", Name: "agent"}}
	frozen, err := loader.FreezeRun(ctx, client.ObjectKeyFromObject(&run), nil, 33554432)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	options := ComposeOptions{Binary: filepath.Join(root, "celln"), PolicyRoot: root, KeyFile: filepath.Join(root, "seed"), OutputDir: filepath.Join(root, "output")}
	execute := func(_ context.Context, binary string, args ...string) ([]byte, error) {
		if binary != options.Binary {
			t.Fatal("untrusted binary substitution")
		}
		if args[3] == "verify" {
			return json.Marshal(closureReport{APIVersion: "celln.dev/closure-verification-v1", Scope: "descriptor-authenticity-only", Closure: hash, PolicyHash: hash, Publisher: rt.Spec.Celln.PublisherKey, EntryPoint: "/harness", ClosureEntryPoint: "/harness", Executable: hash, ArtifactReadiness: "not_checked", Conformance: "not_checked"})
		}
		if args[3] != "compose" {
			t.Fatalf("unexpected command: %v", args)
		}
		bytes, err := os.ReadFile(args[4])
		if err != nil {
			t.Fatal(err)
		}
		var plan cellnauthority.CompositionPlan
		if err := json.Unmarshal(bytes, &plan); err != nil {
			t.Fatal(err)
		}
		if plan.Sources[0] != hash || plan.ImageBytes != 33554432 {
			t.Fatal("plan changed")
		}
		return json.Marshal(CompositionReport{APIVersion: "celln.dev/composition-report-v1", PlanHash: hash, PolicyHash: hash, Closure: hash, Toolfs: hash, Sources: plan.Sources, ArtifactReadiness: "not_checked", Conformance: "not_checked"})
	}
	return loader, frozen, options, execute
}

func TestComposeChecksSourcesAndRevalidatesWithoutAdmission(t *testing.T) {
	l, f, o, run := compositionFixture(t)
	calls := 0
	var temporary string
	wrapped := func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		calls++
		if args[3] == "compose" {
			temporary = args[4]
		}
		return run(ctx, binary, args...)
	}
	report, err := compose(context.Background(), l, *f, o, wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || report.ArtifactReadiness != "not_checked" {
		t.Fatal("missing checks")
	}
	if _, err := os.Stat(temporary); !os.IsNotExist(err) {
		t.Fatal("temporary plan not cleaned up")
	}
}

func TestComposeRejectsUnboundReportsAndPostBuildWithdrawal(t *testing.T) {
	for _, mode := range []string{"publisher", "source-order", "policy", "readiness", "withdrawal", "existing-output"} {
		t.Run(mode, func(t *testing.T) {
			l, f, o, execute := compositionFixture(t)
			if mode == "existing-output" {
				if err := os.Mkdir(o.OutputDir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			wrapped := func(ctx context.Context, binary string, args ...string) ([]byte, error) {
				raw, err := execute(ctx, binary, args...)
				if err != nil {
					return nil, err
				}
				var report map[string]any
				_ = json.Unmarshal(raw, &report)
				if args[3] == "verify" && mode == "publisher" {
					report["publisher"] = "wrong"
				}
				if args[3] == "compose" {
					switch mode {
					case "source-order":
						report["sources"] = []string{}
					case "policy":
						report["policyHash"] = "blake3:" + strings.Repeat("f", 64)
					case "readiness":
						report["artifactReadiness"] = "ready"
					case "withdrawal":
						var cm corev1.ConfigMap
						c := l.Reader.(client.Client)
						if err := c.Get(ctx, l.AgentSource, &cm); err != nil {
							t.Fatal(err)
						}
						if err := c.Delete(ctx, &cm); err != nil {
							t.Fatal(err)
						}
					}
				}
				return json.Marshal(report)
			}
			if report, err := compose(context.Background(), l, *f, o, wrapped); err == nil || report != nil {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
}
