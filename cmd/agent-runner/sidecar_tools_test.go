package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sympozium-ai/sympozium/pkg/sidecartools"
)

func TestBuildSidecarExecRequest_ArgsMode(t *testing.T) {
	tool := sidecarToolEntry{
		Name:           "sd_evaluate",
		Target:         "service-discovery",
		Exec:           []string{"node", "/app/dist/cli.js"},
		Subcommand:     "evaluate-changes",
		InputMode:      "args",
		PositionalArgs: []string{"serviceIdentifier"},
	}

	req, err := buildSidecarExecRequest(context.Background(), tool, `{"serviceIdentifier":"web","ignored":"x"}`)
	if err != nil {
		t.Fatalf("buildSidecarExecRequest: %v", err)
	}

	wantArgv := []string{"node", "/app/dist/cli.js", "evaluate-changes", "--", "web"}
	if !reflect.DeepEqual(req.Argv, wantArgv) {
		t.Errorf("argv = %v, want %v", req.Argv, wantArgv)
	}
	if req.Stdin != "" {
		t.Errorf("args mode must not set stdin, got %q", req.Stdin)
	}
	if req.Command != "" {
		t.Errorf("argv-mode request must not set Command, got %q", req.Command)
	}
	if req.Target != "service-discovery" {
		t.Errorf("target = %q, want service-discovery", req.Target)
	}
}

func TestBuildSidecarExecRequest_StdinMode(t *testing.T) {
	tool := sidecarToolEntry{
		Name:           "sd_evaluate",
		Target:         "service-discovery",
		Exec:           []string{"node", "/app/dist/cli.js"},
		Subcommand:     "evaluate-changes",
		InputMode:      "stdin",
		PositionalArgs: []string{"serviceIdentifier"},
	}

	req, err := buildSidecarExecRequest(context.Background(), tool, `{"serviceIdentifier":"web","catalog":{"k":"v"}}`)
	if err != nil {
		t.Fatalf("buildSidecarExecRequest: %v", err)
	}

	wantArgv := []string{"node", "/app/dist/cli.js", "evaluate-changes", "--", "web"}
	if !reflect.DeepEqual(req.Argv, wantArgv) {
		t.Errorf("argv = %v, want %v", req.Argv, wantArgv)
	}
	// The positional arg must be stripped from the stdin payload; the remaining
	// object (catalog) is delivered on stdin.
	if req.Stdin != `{"catalog":{"k":"v"}}` {
		t.Errorf("stdin = %q, want %q", req.Stdin, `{"catalog":{"k":"v"}}`)
	}
}

// TestBuildSidecarExecRequest_InjectionSafe verifies that shell metacharacters
// in an argument value remain a single argv element and are never interpolated
// into a shell string.
func TestBuildSidecarExecRequest_InjectionSafe(t *testing.T) {
	tool := sidecarToolEntry{
		Name:           "danger",
		Target:         "svc",
		Exec:           []string{"echo"},
		InputMode:      "args",
		PositionalArgs: []string{"payload"},
	}

	req, err := buildSidecarExecRequest(context.Background(), tool, `{"payload":"a; rm -rf / $(whoami) `+"`id`"+`"}`)
	if err != nil {
		t.Fatalf("buildSidecarExecRequest: %v", err)
	}

	// echo + "--" + one payload element.
	if len(req.Argv) != 3 {
		t.Fatalf("argv length = %d, want 3 (echo + -- + one payload element); got %v", len(req.Argv), req.Argv)
	}
	if req.Argv[1] != "--" {
		t.Errorf("expected -- end-of-options marker before positional values, got %q", req.Argv[1])
	}
	if req.Argv[2] != "a; rm -rf / $(whoami) `id`" {
		t.Errorf("payload argv element = %q, want it preserved verbatim as a single element", req.Argv[2])
	}
}

func TestBuildSidecarExecRequest_NumericPositional(t *testing.T) {
	tool := sidecarToolEntry{
		Name:           "t",
		Target:         "svc",
		Exec:           []string{"svc-tool"},
		InputMode:      "args",
		PositionalArgs: []string{"count", "ratio"},
	}
	// 3_000_000 must NOT become "3e+06"; a float keeps its literal form.
	req, err := buildSidecarExecRequest(context.Background(), tool, `{"count":3000000,"ratio":1.5}`)
	if err != nil {
		t.Fatalf("buildSidecarExecRequest: %v", err)
	}
	want := []string{"svc-tool", "--", "3000000", "1.5"}
	if !reflect.DeepEqual(req.Argv, want) {
		t.Errorf("argv = %v, want %v (no scientific notation)", req.Argv, want)
	}
}

func TestFormatPositionalArg(t *testing.T) {
	cases := []struct {
		in   string // JSON value
		want string
	}{
		{`"hello"`, "hello"},
		{`42`, "42"},
		{`3000000`, "3000000"},
		{`true`, "true"},
		{`[1,2,3]`, "[1,2,3]"},
		{`{"k":"v"}`, `{"k":"v"}`},
	}
	for _, c := range cases {
		dec := json.NewDecoder(strings.NewReader(c.in))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("decode %s: %v", c.in, err)
		}
		if got := sidecartools.FormatPositionalArg(v); got != c.want {
			t.Errorf("FormatPositionalArg(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBuildSidecarExecRequest_NormalizesTarget(t *testing.T) {
	tool := sidecarToolEntry{
		Name:   "t",
		Target: "  My-Pack ",
		Exec:   []string{"true"},
	}
	req, err := buildSidecarExecRequest(context.Background(), tool, `{}`)
	if err != nil {
		t.Fatalf("buildSidecarExecRequest: %v", err)
	}
	if req.Target != "my-pack" {
		t.Errorf("target = %q, want my-pack (lowercased, trimmed)", req.Target)
	}
}

func TestBuildSidecarExecRequest_BadJSON(t *testing.T) {
	tool := sidecarToolEntry{Name: "t", Target: "svc", Exec: []string{"true"}}
	if _, err := buildSidecarExecRequest(context.Background(), tool, `{not json`); err == nil {
		t.Fatal("expected error for malformed args JSON, got nil")
	}
}

func TestLoadSidecarTools(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sidecar-tools.json")
	manifest := `{"tools":[
		{"name":"sd_eval","description":"Evaluate changes","target":"service-discovery",
		 "exec":["node","/app/dist/cli.js"],"subcommand":"evaluate-changes","inputMode":"stdin",
		 "positionalArgs":["serviceIdentifier"],
		 "parameters":{"type":"object","properties":{"serviceIdentifier":{"type":"string"}}}}
	]}`
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	// Reset registry for test isolation.
	sidecarToolRegistryMu.Lock()
	sidecarToolRegistry = map[string]sidecarToolEntry{}
	sidecarToolRegistryMu.Unlock()

	defs := loadSidecarTools(path)
	if len(defs) != 1 {
		t.Fatalf("loadSidecarTools returned %d defs, want 1", len(defs))
	}
	if defs[0].Name != "sd_eval" || defs[0].Description != "Evaluate changes" {
		t.Errorf("unexpected ToolDef: %+v", defs[0])
	}
	if defs[0].Parameters == nil {
		t.Error("ToolDef.Parameters should be populated from the manifest")
	}

	entry, ok := lookupSidecarTool("sd_eval")
	if !ok {
		t.Fatal("sd_eval not registered for dispatch")
	}
	if entry.Target != "service-discovery" || entry.Subcommand != "evaluate-changes" {
		t.Errorf("registered entry mismatch: %+v", entry)
	}
}

func TestLoadSidecarTools_MissingFile(t *testing.T) {
	orig := sidecarToolsLoadTimeout
	sidecarToolsLoadTimeout = 10 * time.Millisecond
	defer func() { sidecarToolsLoadTimeout = orig }()

	if defs := loadSidecarTools(filepath.Join(t.TempDir(), "does-not-exist.json")); defs != nil {
		t.Errorf("expected nil for missing manifest, got %v", defs)
	}
}
