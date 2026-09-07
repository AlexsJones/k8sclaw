package cellnauthority

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func artifactFixture() ExecutionArtifacts {
	return ExecutionArtifacts{Mote: api.CellnImmutableRef{Hash: "blake3:" + strings.Repeat("a", 64)}, Closure: api.CellnImmutableRef{Hash: "blake3:" + strings.Repeat("b", 64)}}
}

func TestExecutionCandidateUsesOnlyFrozenSelectionAndLiveRun(t *testing.T) {
	ctx := context.Background()
	l, _, frozen := modelFixture(t)
	approval, err := l.Resolve(ctx, *frozen)
	if err != nil {
		t.Fatal(err)
	}
	first, err := l.BuildExecution(ctx, *frozen, *approval, artifactFixture())
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.BuildExecution(ctx, *frozen, *approval, artifactFixture())
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Request) != string(second.Request) {
		t.Fatal("retry retargeted request")
	}
	var r struct {
		APIVersion string                `json:"apiVersion"`
		ID         string                `json:"id"`
		Mote       api.CellnImmutableRef `json:"mote"`
		Tools      []api.CellnToolRef    `json:"tools"`
		Harness    struct {
			Task       string                  `json:"task"`
			ModelGrant api.CellnImmutableRef   `json:"modelGrant"`
			Tools      []api.CellnBorrowedTool `json:"borrowedTools"`
		} `json:"harness"`
	}
	if err := json.Unmarshal(first.Request, &r); err != nil {
		t.Fatal(err)
	}
	if r.APIVersion != "celln.dev/v1alpha3" || r.Mote != artifactFixture().Mote || r.Tools[0].Hash != frozen.Prepared.RuntimeExecutable.Hash || len(r.Harness.Tools) != len(frozen.Prepared.BorrowedTools) || r.Harness.Task != "use the lent tool" || r.Harness.ModelGrant.Hash != "blake3:"+strings.Repeat("0", 64) {
		t.Fatalf("wrong request: %s", first.Request)
	}
	bad := *approval
	bad.Caller = "other-tenant"
	if _, err := l.BuildExecution(ctx, *frozen, bad, artifactFixture()); err == nil {
		t.Fatal("accepted altered approval")
	}
	if _, err := l.BuildExecution(ctx, *frozen, *approval, ExecutionArtifacts{}); err == nil {
		t.Fatal("accepted missing artifacts")
	}
}

func TestExecutionCandidateRefusesUnsupportedFreshRunAndCapsTimeout(t *testing.T) {
	for _, mode := range []string{"env", "parent", "dry-run", "lifecycle", "server", "existing-celln", "blank-task", "task-too-long", "persona-too-long", "timeout-zero", "timeout-lower"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			l, c, old := modelFixture(t)
			key := types.NamespacedName{Namespace: old.Run.Namespace, Name: old.Run.Name}
			var run api.AgentRun
			if err := c.Get(ctx, key, &run); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "env":
				run.Spec.Env = map[string]string{"KEY": "value"}
			case "parent":
				run.Spec.Parent = &api.ParentRunRef{RunName: "parent"}
			case "dry-run":
				run.Spec.DryRun = true
			case "lifecycle":
				run.Spec.Lifecycle = &api.LifecycleHooks{}
			case "server":
				run.Spec.Mode = "server"
			case "existing-celln":
				run.Spec.Celln = &api.CellnExecutionSpec{}
			case "blank-task":
				run.Spec.Task = api.NewStringTask(" ")
			case "task-too-long":
				run.Spec.Task = api.NewStringTask(strings.Repeat("x", 2049))
			case "persona-too-long":
				run.Spec.SystemPrompt = strings.Repeat("x", 2049)
			case "timeout-zero":
				run.Spec.Timeout = &metav1.Duration{}
			case "timeout-lower":
				run.Spec.Timeout = &metav1.Duration{Duration: time.Second}
			}
			if err := c.Update(ctx, &run); err != nil {
				t.Fatal(err)
			}
			var selections []Selection
			for _, tool := range old.Snapshot.Tools {
				selections = append(selections, Selection{Name: tool.Identity.Name, Revision: tool.Identity.Revision})
			}
			frozen, err := l.Selection.FreezeRun(ctx, key, selections, old.Prepared.Composition.ImageBytes)
			if err != nil {
				t.Fatal(err)
			}
			approval, err := l.Resolve(ctx, *frozen)
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := l.BuildExecution(ctx, *frozen, *approval, artifactFixture())
			if mode != "timeout-lower" {
				if err == nil {
					t.Fatal("accepted unsupported run")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(candidate.Request, &wire); err != nil {
				t.Fatal(err)
			}
			if wire["capabilities"].(map[string]any)["timeoutMs"] != float64(1000) {
				t.Fatal("lost lower run timeout")
			}
		})
	}
}

func TestExecutionCandidateMatchesRealCellnParser(t *testing.T) {
	binary := os.Getenv("CELLN_COMPOSITION_BINARY")
	if binary == "" {
		t.Skip("explicit trusted Celln binary required")
	}
	if !filepath.IsAbs(binary) {
		t.Fatal("absolute trusted binary required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	l, _, frozen := modelFixture(t)
	approval, err := l.Resolve(ctx, *frozen)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := l.BuildExecution(ctx, *frozen, *approval, artifactFixture())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(path, candidate.Request, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "harness-binding", path)
	cmd.Env = []string{"LANG=C"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("real Celln refused: %v: %s", err, out)
	}
	var result struct {
		APIVersion          string `json:"apiVersion"`
		RequestBinding      string `json:"requestBinding"`
		ExecutionAuthorized bool   `json:"executionAuthorized"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	if result.APIVersion != "celln.dev/harness-request-binding-v1" || !hashPattern.MatchString(result.RequestBinding) || result.ExecutionAuthorized {
		t.Fatalf("unexpected binding: %s", out)
	}
}
