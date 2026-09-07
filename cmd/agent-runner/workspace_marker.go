package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"time"
)

// runWorkspaceMarker writes /workspace/.sympozium/state.json before the agent
// container starts. Harness wrappers and the agent-runner read this marker to
// detect whether the workspace is fresh or carried over from a prior run.
//
// It preserves the previous marker as `previousRun` so wrappers can surface
// "your workspace was reclaimed" / "this is turn N" UX.
//
// This is invoked as `agent-runner workspace-marker` from an init container.
// It is implemented in Go rather than as a shell script because the
// agent-runner image is distroless and has no shell (/bin/sh does not exist).
func runWorkspaceMarker() {
	dir := "/workspace/.sympozium"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatalf("workspace-marker: failed to create %s: %v", dir, err)
	}

	statePath := filepath.Join(dir, "state.json")

	// Preserve the previous marker (raw JSON) if present.
	var previous json.RawMessage
	if b, err := os.ReadFile(statePath); err == nil && len(b) > 0 && json.Valid(b) {
		previous = json.RawMessage(b)
	}

	state := map[string]any{
		"runName":          os.Getenv("AGENT_RUN_ID"),
		"sessionKey":       os.Getenv("SESSION_KEY"),
		"agent":            os.Getenv("AGENT_NAME"),
		"namespace":        os.Getenv("AGENT_NAMESPACE"),
		"workspaceSession": os.Getenv("WORKSPACE_SESSION"),
		"workspacePVC":     os.Getenv("WORKSPACE_PVC"),
		"startedAt":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	if previous != nil {
		state["previousRun"] = previous
	}

	data, err := json.Marshal(state)
	if err != nil {
		log.Fatalf("workspace-marker: failed to marshal state: %v", err)
	}

	// Atomic write via temp file + rename.
	tmpPath := statePath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		log.Fatalf("workspace-marker: failed to write %s: %v", tmpPath, err)
	}
	if err := os.Rename(tmpPath, statePath); err != nil {
		log.Fatalf("workspace-marker: failed to move %s -> %s: %v", tmpPath, statePath, err)
	}

	log.Printf("workspace-marker: wrote %s", statePath)
}
