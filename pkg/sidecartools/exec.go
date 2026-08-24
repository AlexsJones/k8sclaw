package sidecartools

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ExecRequest is the JSON written to /ipc/tools/exec-request-*.json and read by
// tool-executor.sh in a skill sidecar. Only the fields a native sidecar tool
// needs are here; the legacy shell path (Command/Args) stays in agent-runner.
type ExecRequest struct {
	ID      string            `json:"id"`
	WorkDir string            `json:"workDir,omitempty"`
	Timeout int               `json:"timeout,omitempty"`
	Target  string            `json:"target,omitempty"`
	Argv    []string          `json:"argv,omitempty"`
	Stdin   string            `json:"stdin,omitempty"`
	Meta    map[string]string `json:"_meta,omitempty"`
}

// DefaultExecTimeout is the per-call timeout, in seconds, for a native sidecar
// tool.
const DefaultExecTimeout = 120

// BuildExecRequest turns a manifest tool plus the caller's JSON arguments into
// an argv-mode exec request.
//
// Shared rather than duplicated because the details are load-bearing:
//
//   - argv = Exec prefix + optional fixed Subcommand + "--" + positional values.
//     The "--" end-of-options marker means a model-supplied positional value
//     beginning with "-" is an operand, not a flag. Wrapped CLIs must honour it
//     (docs/guides/writing-sidecars.md).
//   - Argv mode is executed with no shell, so argument values cannot inject
//     shell syntax.
//   - Numbers keep their exact literal form; without json.Number a large id
//     would round-trip through float64 and come out in scientific notation.
//
// Every caller that can reach a skill sidecar must build requests this way.
func BuildExecRequest(tool Tool, argsJSON string, meta map[string]string) (ExecRequest, error) {
	args := map[string]any{}
	if argsJSON != "" {
		dec := json.NewDecoder(strings.NewReader(argsJSON))
		dec.UseNumber()
		if err := dec.Decode(&args); err != nil {
			return ExecRequest{}, fmt.Errorf("parsing sidecar tool arguments: %w", err)
		}
	}

	argv := append([]string{}, tool.Exec...)
	if tool.Subcommand != "" {
		argv = append(argv, tool.Subcommand)
	}
	if len(tool.PositionalArgs) > 0 {
		argv = append(argv, "--")
	}
	for _, key := range tool.PositionalArgs {
		if val, ok := args[key]; ok {
			argv = append(argv, FormatPositionalArg(val))
			// Remove so it is not also sent on stdin.
			delete(args, key)
		}
	}

	var stdin string
	if tool.InputMode == "stdin" {
		stdinJSON, err := json.Marshal(args)
		if err != nil {
			return ExecRequest{}, fmt.Errorf("marshalling sidecar tool stdin: %w", err)
		}
		stdin = string(stdinJSON)
	}

	return ExecRequest{
		ID:      fmt.Sprintf("%d", time.Now().UnixNano()),
		Argv:    argv,
		Stdin:   stdin,
		WorkDir: "/workspace",
		Timeout: DefaultExecTimeout,
		Target:  NormalizeTarget(tool.Target),
		Meta:    meta,
	}, nil
}

// FormatPositionalArg renders a decoded argument value as one argv element.
// Strings pass through verbatim; json.Number keeps its exact literal form (no
// float64 rounding or scientific notation); composites and bools become
// canonical JSON. The result is always a single element.
func FormatPositionalArg(val any) string {
	switch v := val.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case nil:
		return ""
	default:
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}
