package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tool name constants.
const (
	ToolExecuteCommand = "execute_command"
	ToolReadFile       = "read_file"
	ToolListDirectory  = "list_directory"
)

// ToolDef describes a tool for LLM function calling.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// defaultTools returns the set of tools available to the agent.
func defaultTools() []ToolDef {
	return []ToolDef{
		{
			Name: ToolExecuteCommand,
			Description: "Execute a shell command in the Kubernetes skill sidecar container. " +
				"Use this to run kubectl, bash scripts, curl, jq, and other CLI tools. " +
				"Commands execute in /workspace by default. " +
				"Always prefer this tool when the user asks you to inspect or manage Kubernetes resources.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The shell command to execute (e.g. 'kubectl get pods -n default')",
					},
					"workdir": map[string]any{
						"type":        "string",
						"description": "Working directory for the command. Defaults to /workspace.",
					},
					"timeout": map[string]any{
						"type":        "integer",
						"description": "Timeout in seconds (default 30, max 120).",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name:        ToolReadFile,
			Description: "Read the contents of a file from the pod filesystem. Paths under /workspace, /skills, /tmp, and /ipc are accessible.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the file to read.",
					},
				},
				"required": []string{"path"},
			},
		},
		{
			Name:        ToolListDirectory,
			Description: "List the contents of a directory on the pod filesystem.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the directory to list.",
					},
				},
				"required": []string{"path"},
			},
		},
	}
}

// executeToolCall dispatches a tool call and returns the result string.
func executeToolCall(name string, argsJSON string) string {
	log.Printf("tool call: %s args=%s", name, truncateStr(argsJSON, 200))

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("Error parsing tool arguments: %v", err)
	}

	switch name {
	case ToolExecuteCommand:
		return executeCommand(args)
	case ToolReadFile:
		return readFileTool(args)
	case ToolListDirectory:
		return listDirectoryTool(args)
	default:
		return fmt.Sprintf("Unknown tool: %s", name)
	}
}

// --- Native tools (run in the agent container) ---

func readFileTool(args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		return "Error: 'path' is required"
	}

	// Security: restrict to allowed paths.
	allowed := []string{"/workspace", "/skills", "/tmp", "/ipc"}
	ok := false
	for _, prefix := range allowed {
		if strings.HasPrefix(filepath.Clean(path), prefix) {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Sprintf("Error: access denied — path must be under %s", strings.Join(allowed, ", "))
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("Error reading file: %v", err)
	}

	content := string(data)
	if len(content) > 100_000 {
		content = content[:100_000] + fmt.Sprintf("\n... (truncated, file is %d bytes)", len(data))
	}
	return content
}

func listDirectoryTool(args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		return "Error: 'path' is required"
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Sprintf("Error listing directory: %v", err)
	}

	var sb strings.Builder
	for _, entry := range entries {
		info, _ := entry.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}
		kind := "file"
		if entry.IsDir() {
			kind = "dir"
		}
		sb.WriteString(fmt.Sprintf("%-6s %8d  %s\n", kind, size, entry.Name()))
	}
	return sb.String()
}

// --- IPC-based command execution (runs in the sidecar container) ---

// execRequest matches the IPC ExecRequest protocol.
type execRequest struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	WorkDir string   `json:"workDir,omitempty"`
	Timeout int      `json:"timeout,omitempty"`
}

// execResult matches the IPC ExecResult protocol.
type execResult struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timedOut,omitempty"`
}

func executeCommand(args map[string]any) string {
	command, _ := args["command"].(string)
	if command == "" {
		return "Error: 'command' is required"
	}

	workdir, _ := args["workdir"].(string)
	if workdir == "" {
		workdir = "/workspace"
	}

	timeoutSec := 30
	if t, ok := args["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}
	if timeoutSec > 120 {
		timeoutSec = 120
	}

	id := fmt.Sprintf("%d", time.Now().UnixNano())

	req := execRequest{
		ID:      id,
		Command: command,
		Args:    nil,
		WorkDir: workdir,
		Timeout: timeoutSec,
	}

	toolsDir := "/ipc/tools"
	reqPath := filepath.Join(toolsDir, fmt.Sprintf("exec-request-%s.json", id))
	resPath := filepath.Join(toolsDir, fmt.Sprintf("exec-result-%s.json", id))

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Sprintf("Error marshalling exec request: %v", err)
	}

	_ = os.MkdirAll(toolsDir, 0o755)
	if err := os.WriteFile(reqPath, data, 0o644); err != nil {
		return fmt.Sprintf("Error writing exec request: %v", err)
	}

	log.Printf("Wrote exec request %s: %s", id, truncateStr(command, 120))

	// Poll for result with a deadline.
	deadline := time.Now().Add(time.Duration(timeoutSec+10) * time.Second)
	for time.Now().Before(deadline) {
		resData, err := os.ReadFile(resPath)
		if err == nil {
			// Guard against reading a partially-written file: if the
			// content is empty or not valid JSON, wait and retry.
			if len(resData) == 0 {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			var result execResult
			if err := json.Unmarshal(resData, &result); err != nil {
				// Likely a partial write — retry a few times before giving up.
				time.Sleep(100 * time.Millisecond)
				resData2, err2 := os.ReadFile(resPath)
				if err2 != nil || json.Unmarshal(resData2, &result) != nil {
					return fmt.Sprintf("Error parsing exec result: %v", err)
				}
			}

			_ = os.Remove(reqPath)
			_ = os.Remove(resPath)

			return formatExecResult(result)
		}
		time.Sleep(150 * time.Millisecond)
	}

	return "Error: timed out waiting for command execution result. The skill sidecar may not be running."
}

func formatExecResult(r execResult) string {
	var sb strings.Builder
	if r.Stdout != "" {
		sb.WriteString(r.Stdout)
	}
	if r.Stderr != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString("STDERR: ")
		sb.WriteString(r.Stderr)
	}
	if r.TimedOut {
		sb.WriteString("\n(command timed out)")
	}
	if r.ExitCode != 0 {
		sb.WriteString(fmt.Sprintf("\n(exit code: %d)", r.ExitCode))
	}

	output := sb.String()
	if output == "" {
		output = "(no output)"
	}
	if len(output) > 50_000 {
		output = output[:50_000] + "\n... (output truncated)"
	}
	return output
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
