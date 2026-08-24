package sidecartools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// Tool mirrors one entry in the read-only sidecar-tools manifest the controller
// writes (buildSidecarToolsManifest in internal/controller/agentrun_controller.go).
//
// The definitions originate from the SkillPack CRD and are admission-validated,
// so a running agent consumes them but can neither forge nor alter them. Exec
// (the binary) and Target (the IPC routing key) in particular are
// controller-supplied, never model-supplied.
type Tool struct {
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Target         string         `json:"target"`
	Exec           []string       `json:"exec"`
	Subcommand     string         `json:"subcommand"`
	InputMode      string         `json:"inputMode"` // "args" (default) or "stdin"
	PositionalArgs []string       `json:"positionalArgs"`
	Parameters     map[string]any `json:"parameters"`
}

// Manifest is the document at SIDECAR_TOOLS_MANIFEST_PATH.
type Manifest struct {
	Tools []Tool `json:"tools"`
}

// DefaultLoadTimeout is how long LoadManifest waits for the ConfigMap-mounted
// file to appear. The mount can lag pod start by a moment.
const DefaultLoadTimeout = 5 * time.Second

// LoadManifest reads the controller-written manifest, waiting up to timeout for
// the file to appear. A missing file after the deadline is an error: the caller
// only asks when the controller said there would be one, so an absent manifest
// means the ConfigMap did not mount, not that there are no tools.
func LoadManifest(path string, timeout time.Duration) (Manifest, error) {
	if timeout <= 0 {
		timeout = DefaultLoadTimeout
	}
	deadline := time.Now().Add(timeout)

	var data []byte
	for {
		var err error
		data, err = os.ReadFile(path)
		if err == nil && len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			return Manifest{}, fmt.Errorf("sidecar tools manifest %s did not appear within %s", path, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parsing sidecar tools manifest %s: %w", path, err)
	}
	return m, nil
}

// FilterByPolicy applies spec.toolPolicy to a tool list. allowList and denyList
// are the comma-separated TOOL_POLICY_ALLOW / TOOL_POLICY_DENY values.
//
// The semantics match agent-runner's applyToolPolicy exactly, and must keep
// matching: the same AgentRun field is enforced in both places, and a run that
// behaved differently depending on which process held the tools would make the
// policy meaningless. Deny wins over allow, and a non-empty allow list is
// exclusive.
func FilterByPolicy(tools []Tool, allowList, denyList string) []Tool {
	allowed := splitSet(allowList)
	denied := splitSet(denyList)
	useAllowlist := len(allowed) > 0

	filtered := make([]Tool, 0, len(tools))
	for _, t := range tools {
		if denied[t.Name] {
			continue
		}
		if useAllowlist && !allowed[t.Name] {
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}

func splitSet(list string) map[string]bool {
	out := map[string]bool{}
	for _, name := range strings.Split(list, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out[name] = true
		}
	}
	return out
}
