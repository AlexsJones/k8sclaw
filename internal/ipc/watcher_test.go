package ipc

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// TestWatch_ReconcilesPreExistingFiles covers the race a harness adapter
// exposes: the agent container can finish and write result.json before the
// bridge's inotify watch on /ipc/output is installed. inotify reports nothing
// for a file already present at Add() time, so without reconciliation the
// run would wedge in Running forever. Watch must synthesize an event for any
// file that predates registration.
func TestWatch_ReconcilesPreExistingFiles(t *testing.T) {
	dir := t.TempDir()
	preExisting := filepath.Join(dir, "result.json")
	if err := os.WriteFile(preExisting, []byte(`{"status":"success"}`), 0o600); err != nil {
		t.Fatalf("write pre-existing file: %v", err)
	}

	w, err := NewWatcher(dir, logr.Discard())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, err := w.Watch(ctx, dir)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	select {
	case fe := <-events:
		if fe.Path != preExisting {
			t.Fatalf("reconciled path = %q, want %q", fe.Path, preExisting)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reconciliation of pre-existing file")
	}
}
