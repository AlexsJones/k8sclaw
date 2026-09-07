package cellnauthority

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func loaderFixture(t *testing.T) (Loader, client.Client, types.NamespacedName, []Selection) {
	t.Helper()
	r := fixture(t)
	tool := r.Catalogue[0]
	tool.Spec.InvocationABI = "celln.json-stdio/v1"
	id, err := Identify(tool)
	if err != nil {
		t.Fatal(err)
	}
	grant := Grant{Tool: id, Limits: tool.Spec.Limits}
	agent := &api.Agent{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "agent", UID: "agent-uid", Generation: 1}, Spec: api.AgentSpec{RuntimeRef: "harness"}}
	hash := api.CellnImmutableRef{Hash: "blake3:" + strings.Repeat("c", 64)}
	harness := &api.AgentRuntime{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: "harness", UID: "runtime-uid", Generation: 1}, Spec: api.AgentRuntimeSpec{Celln: &api.AgentRuntimeCellnProfile{
		ContractVersion: "celln.json-tools/v1", Revision: "v1", Platform: "linux/amd64", Lane: "agent", Lifecycle: "disposable-one-shot",
		Executable: hash, Closure: hash, Mote: hash, PublisherKey: strings.Repeat("d", 64), EntryPoint: "/harness",
		JSON: &api.CellnHarnessJSONLimits{MaxTurns: 3, MaxCalls: 2}, Limits: api.AgentRuntimeCellnLimits{TimeoutMillis: 90000, MemoryBytes: 268435456, TaskBytes: 2048, OutputBytes: 65536, Workspace: "none"},
	}}}
	agentID, _ := IdentifySubject("Agent", agent.ObjectMeta, agent.Spec)
	runtimeID, _ := IdentifySubject("AgentRuntime", harness.ObjectMeta, harness.Spec)
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := []client.Object{agent, harness, &tool}
	for _, layer := range []string{"operator", "runtime", "agent"} {
		raw, err := json.Marshal(GrantDocument{APIVersion: "sympozium.ai/celln-grants-v1", Layer: layer, Agent: agentID, Runtime: runtimeID, Grants: []Grant{grant}})
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "operator", Name: layer, UID: types.UID(layer + "-uid"), ResourceVersion: "1"}, Data: map[string]string{"grants.json": string(raw)}})
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	l := Loader{Reader: c, OperatorSource: types.NamespacedName{Namespace: "operator", Name: "operator"}, RuntimeSource: types.NamespacedName{Namespace: "operator", Name: "runtime"}, AgentSource: types.NamespacedName{Namespace: "operator", Name: "agent"}}
	return l, c, client.ObjectKeyFromObject(agent), []Selection{{Name: tool.Name, Revision: tool.Spec.Revision}}
}

func TestLoaderReadsAllLayersAndPinsSources(t *testing.T) {
	l, _, agent, selection := loaderFixture(t)
	got, err := l.Resolve(context.Background(), agent, selection)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 1 || len(got.Sources) != 3 || got.Runtime.UID != "runtime-uid" {
		t.Fatalf("bad snapshot: %+v", got)
	}
	selection[0].Limits = &api.CellnToolLimits{TimeoutMillis: 100, MemoryBytes: 1024, ArgumentBytes: 64, OutputBytes: 64, Workspace: "none", Effects: "none"}
	got, err = l.Resolve(context.Background(), agent, selection)
	if err != nil {
		t.Fatal(err)
	}
	if got.Tools[0].Limits.TimeoutMillis != 100 {
		t.Fatal("selection ceiling ignored")
	}
	got, err = l.Resolve(context.Background(), agent, nil)
	if err != nil || len(got.Tools) != 0 {
		t.Fatalf("empty selection: %+v %v", got, err)
	}
}

func TestLoaderRefusesWithdrawalStaleSubjectsAndUntrustedLookalikes(t *testing.T) {
	for _, mode := range []string{"withdrawn", "wrong-layer", "stale-agent", "stale-runtime", "wrong-revision", "missing-source", "trailing-json", "unknown-field", "tenant-lookalike", "duplicate-source"} {
		t.Run(mode, func(t *testing.T) {
			l, c, agent, selection := loaderFixture(t)
			ctx := context.Background()
			var cm corev1.ConfigMap
			if err := c.Get(ctx, l.AgentSource, &cm); err != nil {
				t.Fatal(err)
			}
			var doc GrantDocument
			if err := json.Unmarshal([]byte(cm.Data["grants.json"]), &doc); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "withdrawn":
				doc.Grants = []Grant{}
			case "wrong-layer":
				doc.Layer = "operator"
			case "stale-agent":
				doc.Agent.UID = "recreated"
			case "stale-runtime":
				doc.Runtime.SpecSHA256 = "changed"
			case "wrong-revision":
				selection[0].Revision = "v2"
			case "missing-source", "tenant-lookalike":
				if err := c.Delete(ctx, &cm); err != nil {
					t.Fatal(err)
				}
				if mode == "tenant-lookalike" {
					cm.Namespace = "tenant"
					cm.ResourceVersion = ""
					cm.UID = "tenant-forged"
					if err := c.Create(ctx, &cm); err != nil {
						t.Fatal(err)
					}
				}
			case "duplicate-source":
				l.AgentSource = l.RuntimeSource
			}
			if mode != "missing-source" && mode != "tenant-lookalike" {
				raw, _ := json.Marshal(doc)
				if mode == "trailing-json" {
					raw = append(raw, []byte(" {}")...)
				}
				if mode == "unknown-field" {
					raw = append([]byte(`{"approval":true,`), raw[1:]...)
				}
				cm.Data["grants.json"] = string(raw)
				if err := c.Update(ctx, &cm); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := l.Resolve(ctx, agent, selection); err == nil || got != nil {
				t.Fatalf("accepted %s: %+v", mode, got)
			}
		})
	}
}

type changingReader struct {
	client.Reader
	reads int
}

func (r *changingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := r.Reader.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if cm, ok := obj.(*corev1.ConfigMap); ok && key.Name == "operator" {
		r.reads++
		if r.reads > 1 {
			cm.ResourceVersion = "changed"
		}
	}
	return nil
}
func TestLoaderRefusesObservedPolicyChange(t *testing.T) {
	l, c, agent, selection := loaderFixture(t)
	l.Reader = &changingReader{Reader: c}
	if got, err := l.Resolve(context.Background(), agent, selection); err == nil || got != nil {
		t.Fatal("accepted changing source")
	}
}
