package cellnauthority

import (
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func fixture(t *testing.T) ToolRequest {
	t.Helper()
	h := api.CellnImmutableRef{Hash: "blake3:" + strings.Repeat("a", 64)}
	tool := api.CellnTool{
		ObjectMeta: metav1.ObjectMeta{Name: "add-v1", Namespace: "tenant", UID: "original-uid", Generation: 1},
		Spec:       api.CellnToolSpec{Revision: "v1", Description: "Add", SupportOwner: "operator", PublisherKey: strings.Repeat("b", 64), Executable: h, Closure: h, ArgumentsSchema: h, ResultSchema: h, EntryPoint: "/tools/add", InvocationABI: "celln.argv/v1", Platform: "linux/amd64", Lane: "tool", Limits: api.CellnToolLimits{TimeoutMillis: 1000, MemoryBytes: 65536, ArgumentBytes: 1024, OutputBytes: 1024, Workspace: "none", Effects: "none"}},
	}
	id, err := Identify(tool)
	if err != nil {
		t.Fatal(err)
	}
	g := Grant{Tool: id, Limits: tool.Spec.Limits}
	return ToolRequest{Namespace: "tenant", Selection: []Grant{g}, Catalogue: []api.CellnTool{tool}, Operator: []Grant{g}, Runtime: []Grant{g}, Agent: []Grant{g}}
}

func TestIntersectsEveryLayer(t *testing.T) {
	r := fixture(t)
	r.Operator[0].Limits.TimeoutMillis = 900
	r.Runtime[0].Limits.MemoryBytes = 4096
	r.Agent[0].Limits.ArgumentBytes = 128
	r.Selection[0].Limits.OutputBytes = 64
	got, err := ResolveTools(r)
	if err != nil {
		t.Fatal(err)
	}
	want := api.CellnToolLimits{TimeoutMillis: 900, MemoryBytes: 4096, ArgumentBytes: 128, OutputBytes: 64, Workspace: "none", Effects: "none"}
	if len(got) != 1 || got[0].Limits.TimeoutMillis != want.TimeoutMillis || got[0].Limits.MemoryBytes != want.MemoryBytes || got[0].Limits.ArgumentBytes != want.ArgumentBytes || got[0].Limits.OutputBytes != want.OutputBytes {
		t.Fatalf("wrong intersection: %+v", got)
	}
	r.Catalogue[0].Spec.Description = "mutated"
	if got[0].Spec.Description != "Add" {
		t.Fatal("resolved plan aliases catalogue")
	}
}

func TestEmptySelectionGrantsNothing(t *testing.T) {
	r := fixture(t)
	r.Selection = nil
	got, err := ResolveTools(r)
	if err != nil || len(got) != 0 {
		t.Fatalf("empty selection expanded: %+v %v", got, err)
	}
}

func TestRefusesUnsafeOrStaleResolution(t *testing.T) {
	cases := map[string]func(*ToolRequest){
		"missing operator":      func(r *ToolRequest) { r.Operator = nil },
		"missing runtime":       func(r *ToolRequest) { r.Runtime = nil },
		"missing agent":         func(r *ToolRequest) { r.Agent = nil },
		"cross namespace":       func(r *ToolRequest) { r.Selection[0].Tool.Namespace = "other" },
		"recreated UID":         func(r *ToolRequest) { r.Catalogue[0].UID = "replacement" },
		"stale generation":      func(r *ToolRequest) { r.Catalogue[0].Generation++ },
		"revision replaced":     func(r *ToolRequest) { r.Catalogue[0].Spec.Revision = "v2" },
		"schema replaced":       func(r *ToolRequest) { r.Catalogue[0].Spec.ArgumentsSchema.Hash = "blake3:" + strings.Repeat("c", 64) },
		"publisher replaced":    func(r *ToolRequest) { r.Catalogue[0].Spec.PublisherKey = strings.Repeat("c", 64) },
		"lane replaced":         func(r *ToolRequest) { r.Catalogue[0].Spec.Lane = "agent" },
		"deleted":               func(r *ToolRequest) { now := metav1.Now(); r.Catalogue[0].DeletionTimestamp = &now },
		"absent":                func(r *ToolRequest) { r.Catalogue = nil },
		"duplicate selection":   func(r *ToolRequest) { r.Selection = append(r.Selection, r.Selection[0]) },
		"duplicate catalogue":   func(r *ToolRequest) { r.Catalogue = append(r.Catalogue, r.Catalogue[0]) },
		"duplicate grant":       func(r *ToolRequest) { r.Operator = append(r.Operator, r.Operator[0]) },
		"stale grant":           func(r *ToolRequest) { r.Agent[0].Tool.UID = "old" },
		"invalid metadata":      func(r *ToolRequest) { r.Catalogue[0].Spec.EntryPoint = "/tools/../escape" },
		"zero selection budget": func(r *ToolRequest) { r.Selection[0].Limits.TimeoutMillis = 0 },
		"negative grant":        func(r *ToolRequest) { r.Runtime[0].Limits.MemoryBytes = -1 },
		"unsupported egress":    func(r *ToolRequest) { r.Agent[0].Limits.Egress = []string{"https://example.com"} },
		"workspace":             func(r *ToolRequest) { r.Operator[0].Limits.Workspace = "read-write" },
		"inputs":                func(r *ToolRequest) { r.Selection[0].Limits.Inputs = []api.CellnImmutableRef{{Hash: "ignored"}} },
		"too many tools":        func(r *ToolRequest) { r.Selection = make([]Grant, 17) },
		"missing namespace":     func(r *ToolRequest) { r.Namespace = "" },
		"forged Ready": func(r *ToolRequest) {
			r.Operator = nil
			r.Catalogue[0].Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := fixture(t)
			mutate(&r)
			got, err := ResolveTools(r)
			if err == nil || got != nil {
				t.Fatalf("unsafe resolution returned partial authority: %+v %v", got, err)
			}
		})
	}
}

func TestEffectsCannotBeSilentlyDowngraded(t *testing.T) {
	r := fixture(t)
	r.Catalogue[0].Spec.Limits.Effects = "external-side-effects"
	id, err := Identify(r.Catalogue[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, grants := range [][]Grant{r.Operator, r.Runtime, r.Agent, r.Selection} {
		grants[0].Tool = id
		grants[0].Limits.Effects = "external-side-effects"
	}
	if _, err := ResolveTools(r); err != nil {
		t.Fatal(err)
	}
	r.Operator[0].Limits.Effects = "none"
	if got, err := ResolveTools(r); err == nil || got != nil {
		t.Fatal("side effects silently downgraded")
	}
}

func TestOrderAndAtomicDenial(t *testing.T) {
	r := fixture(t)
	second := *r.Catalogue[0].DeepCopy()
	second.Name, second.UID, second.Spec.EntryPoint = "multiply-v1", "second-uid", "/tools/multiply"
	id, err := Identify(second)
	if err != nil {
		t.Fatal(err)
	}
	g := Grant{Tool: id, Limits: second.Spec.Limits}
	r.Catalogue = append(r.Catalogue, second)
	r.Operator = append(r.Operator, g)
	r.Runtime = append(r.Runtime, g)
	r.Agent = append(r.Agent, g)
	r.Selection = append([]Grant{g}, r.Selection...)
	got, err := ResolveTools(r)
	if err != nil || len(got) != 2 || got[0].Identity.Name != "multiply-v1" || got[1].Identity.Name != "add-v1" {
		t.Fatalf("order lost: %+v %v", got, err)
	}
	r.Operator = r.Operator[1:] // First selection still valid; second now denied.
	if got, err := ResolveTools(r); err == nil || got != nil {
		t.Fatal("partial grant leaked")
	}
}

func TestRevocationRecheck(t *testing.T) {
	r := fixture(t)
	if _, err := ResolveTools(r); err != nil {
		t.Fatal(err)
	}
	r.Agent = nil
	if got, err := ResolveTools(r); err == nil || got != nil {
		t.Fatal("removed Agent grant reused")
	}
}

func TestEntryPointCollision(t *testing.T) {
	r := fixture(t)
	second := *r.Catalogue[0].DeepCopy()
	second.Name, second.UID = "other", "other-uid"
	id, err := Identify(second)
	if err != nil {
		t.Fatal(err)
	}
	g := Grant{Tool: id, Limits: second.Spec.Limits}
	r.Catalogue = append(r.Catalogue, second)
	r.Selection = append(r.Selection, g)
	r.Operator = append(r.Operator, g)
	r.Runtime = append(r.Runtime, g)
	r.Agent = append(r.Agent, g)
	if got, err := ResolveTools(r); err == nil || got != nil {
		t.Fatal("colliding executable paths accepted")
	}
}

func FuzzCeilingsNeverExpand(f *testing.F) {
	f.Add(uint64(1), uint64(65536), uint64(8192), uint64(99))
	f.Add(uint64(300000), uint64(1), uint64(65536), uint64(65536))
	f.Fuzz(func(t *testing.T, a, b, c, d uint64) {
		r := fixture(t)
		values := []uint64{a, b, c, d}
		layers := [][]Grant{r.Operator, r.Runtime, r.Agent, r.Selection}
		want := r.Catalogue[0].Spec.Limits
		for i, grants := range layers {
			v := values[i]
			l := &grants[0].Limits
			l.TimeoutMillis = int64(v%300000) + 1
			l.MemoryBytes = int64(v%268435456) + 1
			l.ArgumentBytes = int64(v%65536) + 1
			l.OutputBytes = int64(v%65536) + 1
			want.TimeoutMillis = min(want.TimeoutMillis, l.TimeoutMillis)
			want.MemoryBytes = min(want.MemoryBytes, l.MemoryBytes)
			want.ArgumentBytes = min(want.ArgumentBytes, l.ArgumentBytes)
			want.OutputBytes = min(want.OutputBytes, l.OutputBytes)
		}
		got, err := ResolveTools(r)
		if err != nil || len(got) != 1 {
			t.Fatalf("valid intersection refused: %v", err)
		}
		l := got[0].Limits
		if l.TimeoutMillis != want.TimeoutMillis || l.MemoryBytes != want.MemoryBytes || l.ArgumentBytes != want.ArgumentBytes || l.OutputBytes != want.OutputBytes {
			t.Fatalf("intersection widened: %+v want %+v", l, want)
		}
	})
}
