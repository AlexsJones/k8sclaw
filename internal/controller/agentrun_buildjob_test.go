package controller

import (
	"context"
	"strings"
	"testing"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller/taskmodes"
)

// ── buildJob error path tests ─────────────────────────────────────────────────
//
// The buildContainers signature was updated to return an error for unknown
// modes and validation failures. This test pins that buildJob propagates
// the error AND that the reconcile loop can surface it on
// AgentRun.status.error.

func TestBuildJob_UnknownMode_ReturnsError(t *testing.T) {
	r := &AgentRunReconciler{}
	run := objectModeRun("bogus-mode", "anything", nil)

	job, err := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("buildJob: expected error for unknown mode, got nil")
	}
	if job != nil {
		t.Errorf("buildJob: expected nil Job on error, got %+v", job)
	}
	if !strings.Contains(err.Error(), `unknown task.mode "bogus-mode"`) {
		t.Errorf("error message %q does not name the unknown mode", err.Error())
	}
	if !strings.Contains(err.Error(), taskmodes.SidecarDriven) {
		t.Errorf("error message %q does not list supported modes", err.Error())
	}
}

func TestBuildJob_StringTaskSucceeds(t *testing.T) {
	r := &AgentRunReconciler{}
	run := stringModeRun("do the thing")

	job, err := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildJob: %v", err)
	}
	if job == nil {
		t.Fatal("buildJob: expected non-nil Job for string-form task")
	}
	if job.Name != run.Name {
		t.Errorf("job.Name = %q, want %q", job.Name, run.Name)
	}
}

func TestBuildJob_ObjectTaskSidecarDriven_SucceedsWithMatchingTool(t *testing.T) {
	r := &AgentRunReconciler{}
	run := objectModeRun(taskmodes.SidecarDriven, "primary", map[string]string{
		"batchSize": "1",
	})
	sidecars := []resolvedSidecar{
		{
			skillPackName: "skill-scout",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Tools: []sympoziumv1alpha1.SidecarTool{
					{Name: "primary", Exec: []string{"node", "/app/dist/cli.js"}, Subcommand: "primary"},
				},
			},
		},
	}

	job, err := r.buildJob(context.Background(), run, false, nil, sidecars, nil, nil)
	if err != nil {
		t.Fatalf("buildJob: %v", err)
	}
	if job == nil {
		t.Fatal("buildJob: expected non-nil Job")
	}
}

func TestBuildJob_ObjectTaskSidecarDriven_FailsWhenNoMatchingTool(t *testing.T) {
	r := &AgentRunReconciler{}
	run := objectModeRun(taskmodes.SidecarDriven, "nonexistent-tool", nil)
	sidecars := []resolvedSidecar{
		{
			skillPackName: "skill-scout",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Tools: []sympoziumv1alpha1.SidecarTool{
					{Name: "primary", Exec: []string{"node", "/app/dist/cli.js"}},
				},
			},
		},
	}

	job, err := r.buildJob(context.Background(), run, false, nil, sidecars, nil, nil)
	if err == nil {
		t.Fatal("buildJob: expected error for missing tool, got nil")
	}
	if job != nil {
		t.Errorf("buildJob: expected nil Job on error, got %+v", job)
	}
	if !strings.Contains(err.Error(), "no sidecar declares tool") {
		t.Errorf("error message %q does not mention missing tool", err.Error())
	}
}

func TestBuildJob_ObjectTaskSidecarDriven_FailsWithoutToolField(t *testing.T) {
	r := &AgentRunReconciler{}
	// task.tool is empty → handler.Validate should reject.
	run := objectModeRun(taskmodes.SidecarDriven, "", nil)

	job, err := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)
	if err == nil {
		t.Fatal("buildJob: expected error for missing Tool field, got nil")
	}
	if job != nil {
		t.Errorf("buildJob: expected nil Job on error, got %+v", job)
	}
	if !strings.Contains(err.Error(), "task.tool is required") {
		t.Errorf("error message %q does not mention required Tool field", err.Error())
	}
}
