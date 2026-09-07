package cellnauthority

import (
	"context"
	"encoding/json"
	"testing"
)

func TestPrepareBindsCompositorAndJSONToolWire(t *testing.T) {
	l, _, agent, selection := loaderFixture(t)
	snapshot, err := l.Resolve(context.Background(), agent, selection)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Prepare(*snapshot, 33554432)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Composition.Sources) != 2 || plan.Composition.Sources[0] != snapshot.RuntimeSpec.Celln.Closure.Hash || plan.Composition.Sources[1] != snapshot.Tools[0].Spec.Closure.Hash {
		t.Fatal("source ordering/identity lost")
	}
	if plan.Limits.MemoryBytes != snapshot.Tools[0].Limits.MemoryBytes {
		t.Fatal("whole-cell memory did not honor tool ceiling")
	}
	tool := plan.BorrowedTools[0]
	if tool.Name != selection[0].Name || tool.JSONStdio.InputSchema != snapshot.Tools[0].Spec.ArgumentsSchema.Hash || tool.JSONStdio.TimeoutMs != snapshot.Tools[0].Limits.TimeoutMillis {
		t.Fatal("tool mapping lost authority")
	}
	raw, err := json.Marshal(plan.Composition)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 3 || wire["apiVersion"] != "celln.dev/composition-plan-v1" || wire["imageBytes"] != float64(33554432) {
		t.Fatalf("wrong compositor wire: %s", raw)
	}
	snapshot.Tools = nil
	empty, err := Prepare(*snapshot, 33554432)
	if err != nil || len(empty.BorrowedTools) != 0 || len(empty.Composition.Sources) != 1 {
		t.Fatal("empty selection expanded")
	}
}

func TestPrepareRefusesMutationAndUnsupportedBounds(t *testing.T) {
	for _, mode := range []string{"runtime-mutation", "tool-mutation", "grant-expansion", "duplicate", "small-image", "cross-namespace"} {
		t.Run(mode, func(t *testing.T) {
			l, _, agent, selection := loaderFixture(t)
			snapshot, err := l.Resolve(context.Background(), agent, selection)
			if err != nil {
				t.Fatal(err)
			}
			image := int64(33554432)
			switch mode {
			case "runtime-mutation":
				snapshot.RuntimeSpec.Celln.EntryPoint = "/replacement"
			case "tool-mutation":
				snapshot.Tools[0].Spec.EntryPoint = "/replacement"
			case "grant-expansion":
				snapshot.Tools[0].Limits.MemoryBytes++
			case "duplicate":
				snapshot.Tools = append(snapshot.Tools, snapshot.Tools[0])
			case "small-image":
				image = 4096
			case "cross-namespace":
				snapshot.Agent.Namespace = "other"
			}
			if plan, err := Prepare(*snapshot, image); err == nil || plan != nil {
				t.Fatalf("accepted %s", mode)
			}
		})
	}
}
