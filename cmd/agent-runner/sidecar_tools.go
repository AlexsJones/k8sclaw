package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/sympozium-ai/sympozium/pkg/sidecartools"
)

// sidecarToolEntry mirrors the JSON the controller writes into the read-only
// sidecar-tools manifest (see buildSidecarToolsManifest in
// internal/controller/agentrun_controller.go). The definitions originate from
// the SkillPack CRD and are admission-validated, so the running agent consumes
// them but cannot forge or alter them. In particular Exec (the binary) and
// Target (the IPC routing key) are controller-supplied, not model-supplied.
type sidecarToolEntry struct {
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Target         string         `json:"target"`
	Exec           []string       `json:"exec"`
	Subcommand     string         `json:"subcommand"`
	InputMode      string         `json:"inputMode"` // "args" (default) or "stdin"
	PositionalArgs []string       `json:"positionalArgs"`
	Parameters     map[string]any `json:"parameters"`
}

type sidecarToolManifest struct {
	Tools []sidecarToolEntry `json:"tools"`
}

var (
	sidecarToolRegistry   = map[string]sidecarToolEntry{}
	sidecarToolRegistryMu sync.RWMutex

	// sidecarToolsLoadTimeout is how long loadSidecarTools waits for the
	// ConfigMap-mounted manifest to appear. Overridable in tests.
	sidecarToolsLoadTimeout = 5 * time.Second
)

// loadSidecarTools reads the controller-written manifest at manifestPath and
// returns ToolDef entries for the LLM tool list, populating sidecarToolRegistry
// for dispatch. The manifest is mounted read-only from a ConfigMap, so unlike
// the legacy agent-dropped approach the agent cannot modify it. Waits up to 5
// seconds for the file to appear (the ConfigMap mount may lag pod start).
func loadSidecarTools(manifestPath string) []ToolDef {
	var data []byte
	deadline := time.Now().Add(sidecarToolsLoadTimeout)
	for {
		var err error
		data, err = os.ReadFile(manifestPath)
		if err == nil && len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			// The caller only invokes us when SIDECAR_TOOLS_MANIFEST_PATH is set,
			// so an absent manifest here means the controller-written ConfigMap
			// never mounted (mount lag, oversize/failed ConfigMap). Surface it
			// loudly rather than starting with native tools silently missing.
			log.Printf("sidecar_tools: WARNING manifest %s did not appear within %s — native sidecar tools are unavailable for this run",
				manifestPath, sidecarToolsLoadTimeout)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	var manifest sidecarToolManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		log.Printf("sidecar_tools: failed to parse %s: %v", manifestPath, err)
		return nil
	}

	var allTools []ToolDef
	sidecarToolRegistryMu.Lock()
	defer sidecarToolRegistryMu.Unlock()

	for _, entry := range manifest.Tools {
		// Runtime backstop against name shadowing. Admission already rejects
		// collisions with built-in/memory tools and duplicates across SkillPacks,
		// but MCP tool names are discovered dynamically and cannot be checked at
		// admission, so guard here too: skip rather than silently shadow.
		if _, dup := sidecarToolRegistry[entry.Name]; dup {
			log.Printf("sidecar_tools: skipping duplicate tool name %q", entry.Name)
			continue
		}
		if sidecarToolNameReserved(entry.Name) {
			log.Printf("sidecar_tools: skipping tool %q — name collides with a built-in, memory, or MCP tool", entry.Name)
			continue
		}

		params := entry.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		sidecarToolRegistry[entry.Name] = entry
		allTools = append(allTools, ToolDef{
			Name:        entry.Name,
			Description: entry.Description,
			Parameters:  params,
		})
		log.Printf("sidecar_tools: registered %s (target=%s, exec=%v, subcommand=%s)",
			entry.Name, entry.Target, entry.Exec, entry.Subcommand)
	}

	return allTools
}

// sidecarToolNameReserved reports whether name is already claimed by an
// earlier-dispatched tool (built-in, memory, workflow-memory, or a registered
// MCP tool), in which case a sidecar tool of the same name would be shadowed.
func sidecarToolNameReserved(name string) bool {
	switch name {
	case ToolExecuteCommand, ToolReadFile, ToolWriteFile, ToolListDirectory,
		ToolSendChannelMessage, ToolFetchURL, ToolScheduleTask,
		ToolDelegateToPersona, ToolSpawnSubagents:
		return true
	}
	if isMemoryTool(name) || isWorkflowMemoryTool(name) {
		return true
	}
	if _, ok := lookupMCPTool(name); ok {
		return true
	}
	return false
}

func lookupSidecarTool(name string) (sidecarToolEntry, bool) {
	sidecarToolRegistryMu.RLock()
	defer sidecarToolRegistryMu.RUnlock()
	entry, ok := sidecarToolRegistry[name]
	return entry, ok
}

// buildSidecarExecRequest converts a native tool call into an argv-mode
// execRequest. The executable and positional arguments are passed as discrete
// argv elements (no shell), so argument values can never inject shell syntax.
// For stdin-mode tools the remaining (non-positional) arguments are delivered as
// a JSON object on the process stdin rather than interpolated into a command.
func buildSidecarExecRequest(ctx context.Context, tool sidecarToolEntry, argsJSON string) (execRequest, error) {
	// The argv construction lives in pkg/sidecartools so this process and the
	// skill tool server (which serves the same tools to a harness) cannot
	// drift on it. The "--" marker and the exact number formatting are
	// security-relevant; see BuildExecRequest.
	shared, err := sidecartools.BuildExecRequest(sidecartools.Tool(tool), argsJSON, traceMetadata(ctx))
	if err != nil {
		return execRequest{}, err
	}
	return execRequest{
		ID:      shared.ID,
		Argv:    shared.Argv,
		Stdin:   shared.Stdin,
		WorkDir: shared.WorkDir,
		Timeout: shared.Timeout,
		Target:  shared.Target,
		Meta:    shared.Meta,
	}, nil
}

// executeSidecarTool dispatches a native sidecar tool through the gated exec IPC
// in argv mode, targeting the tool's owning sidecar.
func executeSidecarTool(ctx context.Context, tool sidecarToolEntry, argsJSON string) string {
	req, err := buildSidecarExecRequest(ctx, tool, argsJSON)
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	return dispatchExecRequest(req, fmt.Sprintf("%s %v", tool.Name, req.Argv))
}
