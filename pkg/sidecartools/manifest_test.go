package sidecartools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The same AgentRun field is enforced in agent-runner and in the skill tool
// server. A run whose tools differed depending on which process held them would
// make spec.toolPolicy meaningless, so the semantics are pinned here: deny wins,
// and a non-empty allow list is exclusive.
func TestFilterByPolicy(t *testing.T) {
	tools := []Tool{{Name: "read"}, {Name: "write"}, {Name: "delete"}}

	cases := []struct {
		name        string
		allow, deny string
		want        []string
	}{
		{"no policy keeps everything", "", "", []string{"read", "write", "delete"}},
		{"deny removes", "", "delete", []string{"read", "write"}},
		{"allow is exclusive", "read", "", []string{"read"}},
		{"deny wins over allow", "read,delete", "delete", []string{"read"}},
		{"whitespace is trimmed", " read , write ", "", []string{"read", "write"}},
		{"empty entries ignored", "read,,", "", []string{"read"}},
		{"allow list matching nothing yields nothing", "absent", "", nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FilterByPolicy(tools, c.allow, c.deny)
			if len(got) != len(c.want) {
				t.Fatalf("got %d tools %v, want %v", len(got), names(got), c.want)
			}
			for i := range c.want {
				if got[i].Name != c.want[i] {
					t.Errorf("tool[%d] = %q, want %q", i, got[i].Name, c.want[i])
				}
			}
		})
	}
}

func names(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func TestLoadManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sidecar-tools.json")
	if err := os.WriteFile(path, []byte(`{"tools":[{"name":"a","exec":["true"]}]}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	m, err := LoadManifest(path, time.Second)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if len(m.Tools) != 1 || m.Tools[0].Name != "a" {
		t.Errorf("tools = %+v, want one tool named a", m.Tools)
	}
}

// An absent manifest is an error, not an empty list: the caller only asks when
// the controller said there would be one, so nothing there means the ConfigMap
// did not mount.
func TestLoadManifest_AbsentIsAnError(t *testing.T) {
	if _, err := LoadManifest(filepath.Join(t.TempDir(), "nope.json"), 150*time.Millisecond); err == nil {
		t.Fatal("expected an error for an absent manifest")
	}
}

// The "--" marker and exact number formatting are the security-relevant parts,
// which is why this lives in one place both callers use.
func TestBuildExecRequest(t *testing.T) {
	tool := Tool{
		Name:           "kubectl_get",
		Target:         "  K8s-Ops ",
		Exec:           []string{"kubectl", "get"},
		Subcommand:     "pods",
		PositionalArgs: []string{"resource"},
	}
	req, err := BuildExecRequest(tool, `{"resource":"-n","other":3000000}`, nil)
	if err != nil {
		t.Fatalf("BuildExecRequest: %v", err)
	}

	want := []string{"kubectl", "get", "pods", "--", "-n"}
	if len(req.Argv) != len(want) {
		t.Fatalf("argv = %v, want %v", req.Argv, want)
	}
	for i := range want {
		if req.Argv[i] != want[i] {
			t.Errorf("argv[%d] = %q, want %q", i, req.Argv[i], want[i])
		}
	}
	if req.Target != "k8s-ops" {
		t.Errorf("target = %q, want the normalized form", req.Target)
	}
	// args mode: nothing goes to stdin.
	if req.Stdin != "" {
		t.Errorf("stdin = %q, want empty for an args-mode tool", req.Stdin)
	}
}

func TestBuildExecRequest_StdinMode(t *testing.T) {
	tool := Tool{
		Name:           "post",
		Target:         "svc",
		Exec:           []string{"post"},
		InputMode:      "stdin",
		PositionalArgs: []string{"path"},
	}
	req, err := BuildExecRequest(tool, `{"path":"/x","body":"hello"}`, nil)
	if err != nil {
		t.Fatalf("BuildExecRequest: %v", err)
	}
	// The positional was consumed into argv and must not also be on stdin.
	var payload map[string]any
	if err := json.Unmarshal([]byte(req.Stdin), &payload); err != nil {
		t.Fatalf("stdin is not JSON: %v", err)
	}
	if _, dup := payload["path"]; dup {
		t.Error("positional argument was sent on stdin as well as argv")
	}
	if payload["body"] != "hello" {
		t.Errorf("stdin = %s, want the remaining args", req.Stdin)
	}
}

func TestBuildExecRequest_BadJSON(t *testing.T) {
	if _, err := BuildExecRequest(Tool{Name: "t", Exec: []string{"true"}}, `{not json`, nil); err == nil {
		t.Fatal("expected an error for malformed arguments")
	}
}

// A large integer must keep its literal form: through float64 it would come
// back in scientific notation and the wrapped CLI would receive a different value.
func TestFormatPositionalArg_KeepsNumbersExact(t *testing.T) {
	dec := json.NewDecoder(strings.NewReader(`3000000`))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := FormatPositionalArg(v); got != "3000000" {
		t.Errorf("FormatPositionalArg = %q, want 3000000", got)
	}
}
