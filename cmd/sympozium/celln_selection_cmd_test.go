package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSelectionPlanRequiresExplicitSourcesAndWellFormedSelections(t *testing.T) {
	for _, args := range [][]string{
		{"agent"},
		{"agent", "--grant-namespace", "operator", "--operator-grants", "ops", "--runtime-grants", "runtime", "--agent-grants", "agent", "--tool", "bare-name"},
		{"agent", "--grant-namespace", "operator", "--operator-grants", "ops", "--runtime-grants", "runtime", "--agent-grants", "agent", "--tool", "tool@v1@v2"},
	} {
		cmd := newCellnSelectionPlanCmd()
		cmd.SetArgs(args)
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		err := cmd.Execute()
		if err == nil {
			t.Fatalf("accepted %v", args)
		}
		if !strings.Contains(err.Error(), "required flag") && !strings.Contains(err.Error(), "NAME@REVISION") {
			t.Fatalf("wrong refusal: %v", err)
		}
	}
}

func TestModelPolicyReviewRequiresRunBeforeReadingAPI(t *testing.T) {
	cmd := newCellnSelectionPlanCmd()
	cmd.SetArgs([]string{"agent", "--grant-namespace", "operator", "--operator-grants", "ops", "--runtime-grants", "runtime", "--agent-grants", "agent", "--model-policy", "models"})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "requires --run") {
		t.Fatalf("wrong refusal: %v", err)
	}
}

func TestExecutionCandidateRequiresAllAuthorityAndArtifactInputs(t *testing.T) {
	for _, flags := range [][]string{
		{"--execution-mote", "blake3:incomplete"},
		{"--execution-mote", "blake3:incomplete", "--execution-closure", "blake3:incomplete", "--run", "run"},
	} {
		cmd := newCellnSelectionPlanCmd()
		args := []string{"agent", "--grant-namespace", "operator", "--operator-grants", "ops", "--runtime-grants", "runtime", "--agent-grants", "agent"}
		cmd.SetArgs(append(args, flags...))
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "execution candidate requires") {
			t.Fatalf("wrong refusal: %v", err)
		}
	}
}
