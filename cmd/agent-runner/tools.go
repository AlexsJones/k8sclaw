package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/sympozium-ai/sympozium/internal/ipc"
	"github.com/sympozium-ai/sympozium/pkg/sidecartools"
	"golang.org/x/net/html"
)

// ssrfGuardedDialContext returns a DialContext that rejects connections to
// loopback, link-local, private, and unspecified addresses. It is checked
// against the resolved IP at dial time, so it also defeats DNS-rebinding that
// would slip past a resolve-then-connect check. Agents are treated as
// adversarial (prompt injection), so fetch_url must not become an SSRF pivot
// into the node metadata service, NATS, or the Kubernetes API. Operators who
// genuinely need agents to reach cluster-internal hosts can opt out by setting
// SYMPOZIUM_FETCH_URL_ALLOW_PRIVATE=true.
func ssrfGuardedDialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	if strings.EqualFold(os.Getenv("SYMPOZIUM_FETCH_URL_ALLOW_PRIVATE"), "true") {
		return (&net.Dialer{Timeout: 30 * time.Second}).DialContext
	}
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ipAddr := range ips {
			ip := ipAddr.IP
			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
				ip.IsPrivate() || ip.IsUnspecified() {
				return nil, fmt.Errorf("refusing to connect to disallowed address %s", ip)
			}
		}
		// Dial the vetted IP directly so a rebind between resolve and connect
		// cannot substitute a blocked address.
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
}

// Tool name constants.
const (
	ToolExecuteCommand     = "execute_command"
	ToolReadFile           = "read_file"
	ToolWriteFile          = "write_file"
	ToolListDirectory      = "list_directory"
	ToolSendChannelMessage = "send_channel_message"
	ToolFetchURL           = "fetch_url"
	ToolScheduleTask       = "schedule_task"
	ToolDelegateToPersona  = "delegate_to_persona"
	ToolSpawnSubagents     = "spawn_subagents"
)

// ToolDef describes a tool for LLM function calling.
type ToolDef struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// defaultTools returns the set of tools available to the agent.
func defaultTools() []ToolDef {
	tools := []ToolDef{
		{
			Name: ToolExecuteCommand,
			Description: "Execute a shell command in a Kubernetes skill sidecar container. " +
				"Use this to run kubectl, bash scripts, curl, jq, and other CLI tools. " +
				"Commands execute in /workspace by default. " +
				"If specialized MCP tools are available for the task, prefer those instead. " +
				"When multiple skill sidecars are attached, set 'target' to the name of the skill " +
				"that owns the tool you need (e.g. 'github-gitops' for `gh`, 'k8s-ops' for `kubectl`). " +
				"If 'target' is omitted, any sidecar may serve the request (legacy behavior).",
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
					"target": map[string]any{
						"type": "string",
						"description": "Optional. Name of the skill sidecar that should execute this command " +
							"(must match a SkillPack name attached to this agent, e.g. 'github-gitops'). " +
							"Leave empty to allow any attached sidecar to claim the request.",
					},
				},
				"required": []string{"command"},
			},
		},
		{
			Name: ToolReadFile,
			Description: "Read the contents of a file from the pod filesystem. Paths under /workspace, /skills, /tmp, and /ipc are accessible. " +
				"Responses are capped at 8000 bytes; for larger files use 'offset' and 'limit' to read a specific line range, " +
				"following the continuation hint appended to truncated output.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the file to read.",
					},
					"offset": map[string]any{
						"type":        "integer",
						"description": "1-based line number to start reading from. Defaults to 1.",
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": "Maximum number of lines to return. Defaults to the whole file (subject to the 8000-byte response cap).",
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
		{
			Name: ToolSendChannelMessage,
			Description: "Send a message to the user via a connected channel (e.g. WhatsApp, Telegram, Discord, Slack). " +
				"Use this when the user asks you to notify them, send a summary, or deliver any text outside of the task result. " +
				"If no chatId is provided the message is sent to the device owner (self-chat). " +
				"Optionally pass threadId to post into a specific thread (Slack thread_ts, Discord thread id, etc.).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"channel": map[string]any{
						"type":        "string",
						"description": "Channel type to send through: whatsapp, telegram, discord, or slack.",
						"enum":        []string{"whatsapp", "telegram", "discord", "slack"},
					},
					"text": map[string]any{
						"type":        "string",
						"description": "The message text to send.",
					},
					"chatId": map[string]any{
						"type":        "string",
						"description": "Target chat or group ID. Leave empty to send to the device owner (self-chat).",
					},
					"threadId": map[string]any{
						"type":        "string",
						"description": "Optional thread identifier to reply within an existing thread (Slack thread_ts, Discord thread id). Omit to post at the channel root.",
					},
				},
				"required": []string{"channel", "text"},
			},
		},
		{
			Name: ToolFetchURL,
			Description: "Fetch the content of a web page or API endpoint. " +
				"Returns the page content as readable plain text with HTML tags stripped. " +
				"Use this to read documentation, check web services, download data, or gather information from the internet. " +
				"For APIs that return JSON, the raw JSON is returned as-is.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{
						"type":        "string",
						"description": "The URL to fetch (must start with http:// or https://).",
					},
					"maxChars": map[string]any{
						"type":        "integer",
						"description": "Maximum characters to return (default 50000, max 100000). Content is truncated beyond this limit.",
					},
					"headers": map[string]any{
						"type":        "object",
						"description": "Optional HTTP headers to send with the request (e.g. {\"Authorization\": \"Bearer token\"}).",
					},
				},
				"required": []string{"url"},
			},
		},
		{
			Name: ToolWriteFile,
			Description: "Write content to a file on the pod filesystem. Creates the file if it doesn't exist, " +
				"or overwrites it if it does. Parent directories are created automatically. " +
				"Paths under /workspace and /tmp are writable.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the file to write.",
					},
					"content": map[string]any{
						"type":        "string",
						"description": "The content to write to the file.",
					},
				},
				"required": []string{"path", "content"},
			},
		},
		{
			Name: ToolEditFile,
			Description: "Apply one or more exact-string replacements to an existing file. Prefer this over " +
				"write_file for targeted changes — it does not rewrite the whole file. " +
				"Pass an ordered 'edits' array; a single change is just a one-element array. " +
				"Each 'old_string' must match the current file contents exactly (including whitespace and indentation) " +
				"and be UNIQUE: if it appears more than once the edit is rejected with the matching line numbers, so " +
				"include enough surrounding context to pick out the one occurrence you mean, or set 'replace_all' to " +
				"change every one. Read the file first (e.g. with read_file) if you are unsure of the exact text. " +
				"Use an empty 'new_string' to delete the matched text. " +
				"Edits are applied sequentially — each sees the result of the previous ones — and the batch is " +
				"all-or-nothing: if any edit fails to match (or is ambiguous), the file is left completely unchanged. " +
				"Paths under /workspace and /tmp are editable.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{
						"type":        "string",
						"description": "Absolute path to the file to edit.",
					},
					"edits": map[string]any{
						"type":        "array",
						"description": "Ordered list of replacements to apply to the file. For a single change, pass a one-element array.",
						"minItems":    1,
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"old_string": map[string]any{
									"type":        "string",
									"description": "The exact text to replace. Must be unique in the working text unless replace_all is set.",
								},
								"new_string": map[string]any{
									"type":        "string",
									"description": "The replacement text. Use an empty string to delete old_string.",
								},
								"replace_all": map[string]any{
									"type":        "boolean",
									"description": "Replace every occurrence of this old_string instead of requiring a unique match. Defaults to false.",
								},
							},
							"required": []string{"old_string", "new_string"},
						},
					},
				},
				"required": []string{"path", "edits"},
			},
		},
		{
			Name: ToolScheduleTask,
			Description: "Create, update, or delete a recurring scheduled task. " +
				"Use this to set up heartbeats, periodic checks, or any repeating work. " +
				"The schedule fires automatically and creates a new AgentRun each time. " +
				"You can adjust the interval, update the task description, pause, or delete a schedule.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "A short unique name for this schedule (e.g. 'cluster-health-check', 'daily-report'). Used as the SympoziumSchedule resource name.",
					},
					"schedule": map[string]any{
						"type":        "string",
						"description": "Cron expression for how often to run (e.g. '0 */3 * * *' for every 3 hours, '*/30 * * * *' for every 30 minutes, '0 9 * * 1-5' for weekdays at 9am). Standard 5-field cron format: minute hour day-of-month month day-of-week.",
					},
					"task": map[string]any{
						"type":        "string",
						"description": "The task description the agent will receive each time the schedule fires. Be specific and self-contained — each run is independent.",
					},
					"action": map[string]any{
						"type":        "string",
						"description": "What to do: 'create' (new schedule), 'update' (change schedule/task), 'suspend' (pause), 'resume' (unpause), or 'delete' (remove).",
						"enum":        []string{"create", "update", "suspend", "resume", "delete"},
					},
				},
				"required": []string{"name", "action"},
			},
		},
	}

	// Conditionally add delegate_to_persona tool when outgoing delegation/sequential edges exist.
	if targets := delegationTargets(); len(targets) > 0 {
		targetList := strings.Join(targets, ", ")
		tools = append(tools, ToolDef{
			Name: ToolDelegateToPersona,
			Description: "Delegate a task to another persona in your team (Ensemble). " +
				"Use this when your task requires expertise from another team member. " +
				"The target persona will receive the task, execute it, and the result will be " +
				"delivered back to you. Valid targets for this Ensemble: " + targetList + ".",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"targetPersona": map[string]any{
						"type":        "string",
						"description": "The name of the persona to delegate to. Must be one of: " + targetList + ".",
						"enum":        targets,
					},
					"task": map[string]any{
						"type":        "string",
						"description": "The task description for the target persona. Be specific and include all necessary context — the target persona runs independently.",
					},
				},
				"required": []string{"targetPersona", "task"},
			},
		})
	}

	// Conditionally add spawn_subagents tool when subagents are enabled.
	if os.Getenv("SUBAGENTS_ENABLED") == "true" {
		maxChildren := 3
		if v, err := strconv.Atoi(os.Getenv("SUBAGENTS_MAX_CHILDREN")); err == nil && v > 0 {
			maxChildren = v
		}
		tools = append(tools, ToolDef{
			Name: ToolSpawnSubagents,
			Description: "Spawn sub-agents to execute tasks in parallel or sequentially. " +
				"Each sub-agent runs independently as its own AgentRun and returns a result. " +
				"Use this to break complex work into independent subtasks (parallel) or " +
				"dependent pipeline steps (sequential). Sub-agents inherit your model, skills, " +
				fmt.Sprintf("and configuration. Results are returned as an ordered array matching your task order. Maximum tasks per batch: %d.", maxChildren),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"tasks": map[string]any{
						"type": "array",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"id": map[string]any{
									"type":        "string",
									"description": "Unique identifier for this task (used to correlate results)",
								},
								"task": map[string]any{
									"type":        "string",
									"description": "Task description for the sub-agent. Be specific and self-contained.",
								},
								"systemPrompt": map[string]any{
									"type":        "string",
									"description": "Optional system prompt override for this sub-agent",
								},
								"timeout": map[string]any{
									"type":        "string",
									"description": "Optional timeout override (e.g. '5m', '10m')",
								},
							},
							"required": []string{"id", "task"},
						},
						"minItems":    1,
						"maxItems":    maxChildren,
						"description": fmt.Sprintf("Array of tasks to execute as sub-agents. Maximum tasks: %d.", maxChildren),
					},
					"strategy": map[string]any{
						"type":        "string",
						"enum":        []string{"parallel", "sequential"},
						"description": "Execution strategy: 'parallel' runs all at once, 'sequential' runs one after another. Default: parallel.",
					},
					"failurePolicy": map[string]any{
						"type":        "string",
						"enum":        []string{"continue", "fail-fast"},
						"description": "How to handle failures: 'continue' runs all tasks regardless, 'fail-fast' stops on first failure. Default: continue for parallel, fail-fast for sequential.",
					},
				},
				"required": []string{"tasks"},
			},
		})
	}

	return tools
}


// delegationTargets parses ENSEMBLE_RELATIONSHIPS and returns the persona names
// reachable via delegation or sequential edges. Returns nil when no outgoing
// delegation edges are defined — callers use len(targets) > 0 to gate registration.
func delegationTargets() []string {
	relJSON := os.Getenv("ENSEMBLE_RELATIONSHIPS")
	if relJSON == "" {
		return nil
	}
	var rels []struct {
		Target string `json:"target"`
		Type   string `json:"type"`
	}
	if err := json.Unmarshal([]byte(relJSON), &rels); err != nil {
		return nil
	}
	var targets []string
	for _, r := range rels {
		if r.Type == "delegation" || r.Type == "sequential" {
			targets = append(targets, r.Target)
		}
	}
	return targets
}

// executeToolCall dispatches a tool call and returns the result string.
func executeToolCall(ctx context.Context, name string, argsJSON string) string {
	log.Printf("tool call: %s args=%s", name, truncateStr(argsJSON, 200))
	detailedLog.LogAgent("tool_call", map[string]any{"tool": name, "args": argsJSON})

	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return fmt.Sprintf("Error parsing tool arguments: %v", err)
	}

	switch name {
	case ToolExecuteCommand:
		return executeCommand(ctx, args)
	case ToolReadFile:
		return readFileTool(args)
	case ToolWriteFile:
		return writeFileTool(args)
	case ToolEditFile:
		return editFileTool(ctx, args)
	case ToolListDirectory:
		return listDirectoryTool(args)
	case ToolSendChannelMessage:
		return sendChannelMessageTool(args)
	case ToolFetchURL:
		return fetchURLTool(args)
	case ToolScheduleTask:
		return scheduleTaskTool(args)
	case ToolDelegateToPersona:
		return delegateToPersonaTool(ctx, args)
	case ToolSpawnSubagents:
		return spawnSubagentsTool(ctx, args)
	default:
		// Check if this is a memory tool from the memory-server sidecar.
		if isMemoryTool(name) {
			return executeMemoryTool(ctx, name, argsJSON)
		}
		// Check if this is a shared workflow memory tool.
		if isWorkflowMemoryTool(name) {
			return executeWorkflowMemoryTool(ctx, name, argsJSON)
		}
		// Check if this is an MCP tool from the manifest
		if mcpTool, ok := lookupMCPTool(name); ok {
			return executeMCPTool(ctx, mcpTool, argsJSON)
		}
		// Check if this is a native sidecar tool from the controller-written manifest
		if sidecarTool, ok := lookupSidecarTool(name); ok {
			return executeSidecarTool(ctx, sidecarTool, argsJSON)
		}
		return fmt.Sprintf("Unknown tool: %s", name)
	}
}

// --- Native tools (run in the agent container) ---

func readFileTool(args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		return "Error: 'path' is required"
	}

	// Security: restrict to allowed paths, symlink-resolved so a link under an
	// allowed root cannot read outside it.
	clean, err := resolvePath(path, readableRoots)
	if err != nil {
		return "Error: " + err.Error()
	}

	data, err := os.ReadFile(clean)
	if err != nil {
		return fmt.Sprintf("Error reading file: %v", err)
	}

	offset := 1
	if v, ok := args["offset"].(float64); ok && v > 1 {
		offset = int(v)
	}
	limit := 0 // 0 means no line limit
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}

	content := string(data)
	if offset == 1 && limit == 0 && len(content) <= 8_000 {
		return content
	}

	lines := strings.Split(content, "\n")
	total := len(lines)
	if strings.HasSuffix(content, "\n") {
		total-- // don't count the empty tail after a trailing newline
	}

	if offset > total {
		return fmt.Sprintf("Error: offset %d is past the end of the file (%d lines)", offset, total)
	}

	end := total
	if limit > 0 && offset-1+limit < end {
		end = offset - 1 + limit
	}
	selected := strings.Join(lines[offset-1:end], "\n")

	// Cap the response, cutting at the last complete line so the
	// continuation hint stays exact.
	if len(selected) > 8_000 {
		cut := strings.LastIndexByte(selected[:8_000], '\n')
		if cut < 0 {
			// A single line longer than the cap: hard-cut, no line-based continuation possible.
			return selected[:8_000] + fmt.Sprintf("\n... (truncated mid-line %d of %d; line exceeds the 8000-byte cap)", offset, total)
		}
		lastLine := offset - 1 + strings.Count(selected[:cut], "\n") + 1
		return selected[:cut] + fmt.Sprintf("\n... (truncated at line %d of %d; continue with offset=%d)", lastLine, total, lastLine+1)
	}

	if end < total {
		selected += fmt.Sprintf("\n... (showing lines %d-%d of %d; continue with offset=%d)", offset, end, total, end+1)
	}
	return selected
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

// sendChannelMessageTool writes an outbound message to /ipc/messages/ for the
// IPC bridge to relay to the target channel (WhatsApp, Telegram, etc.).
func sendChannelMessageTool(args map[string]any) string {
	channel, _ := args["channel"].(string)
	text, _ := args["text"].(string)
	chatID, _ := args["chatId"].(string)
	threadID, _ := args["threadId"].(string)

	if channel == "" {
		return "Error: 'channel' is required (whatsapp, telegram, discord, slack)"
	}
	if text == "" {
		return "Error: 'text' is required"
	}

	msg := struct {
		Channel  string `json:"channel"`
		ChatID   string `json:"chatId,omitempty"`
		ThreadID string `json:"threadId,omitempty"`
		Text     string `json:"text"`
	}{
		Channel:  channel,
		ChatID:   chatID,
		ThreadID: threadID,
		Text:     text,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Sprintf("Error marshalling message: %v", err)
	}

	dir := "/ipc/messages"
	_ = os.MkdirAll(dir, 0o755)
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	path := filepath.Join(dir, fmt.Sprintf("send-%s.json", id))

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Sprintf("Error writing message file: %v", err)
	}

	log.Printf("Wrote channel message: channel=%s chatId=%s threadId=%s len=%d", channel, chatID, threadID, len(text))
	target := chatID
	if target == "" {
		target = "owner (self)"
	}
	if threadID != "" {
		return fmt.Sprintf("Message sent to %s channel (target: %s, thread: %s)", channel, target, threadID)
	}
	return fmt.Sprintf("Message sent to %s channel (target: %s)", channel, target)
}

// --- Spawn IPC round-trip (shared by delegate_to_persona and spawn_subagents) ---

// spawnDir is where the agent and the IPC bridge exchange spawn requests and
// their results. spawnPollInterval is how often the agent restats the result.
const (
	spawnDir          = "/ipc/spawn"
	spawnPollInterval = 2 * time.Second
)

// ipcRoundTrip writes req as JSON to reqPath, then polls resPath every poll
// until it decodes into out, the run context is cancelled, or timeout elapses.
// A result that fails to decode is a partially written file, so the poll
// continues. Both files are removed once the result decodes; on failure the
// request is left in place, since the SpawnRouter may still be acting on it.
func ipcRoundTrip(ctx context.Context, reqPath, resPath string, poll, timeout time.Duration, req, out any) error {
	if timeout <= 0 {
		return fmt.Errorf("no time left in the run budget")
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshalling request: %w", err)
	}
	_ = os.MkdirAll(filepath.Dir(reqPath), 0o755)
	if err := os.WriteFile(reqPath, data, 0o644); err != nil {
		return fmt.Errorf("writing request: %w", err)
	}

	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	for {
		if res, err := os.ReadFile(resPath); err == nil && len(res) > 0 && json.Unmarshal(res, out) == nil {
			_ = os.Remove(reqPath)
			_ = os.Remove(resPath)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("timed out after %s", timeout)
		case <-ticker.C:
		}
	}
}

// --- Delegate to persona tool (IPC-based) ---

// delegateToPersonaTool writes a spawn request to /ipc/spawn/ and blocks
// until the delegate child run completes, returning the result to the LLM.
// The IPC bridge forwards the request to the SpawnRouter which creates the
// child AgentRun. When the child finishes, the SpawnRouter publishes the
// result back through the bridge to /ipc/spawn/result-{requestID}.json.
func delegateToPersonaTool(ctx context.Context, args map[string]any) string {
	targetPersona, _ := args["targetPersona"].(string)
	task, _ := args["task"].(string)

	if targetPersona == "" {
		return "Error: 'targetPersona' is required — specify the persona name to delegate to (e.g. 'writer')"
	}
	if task == "" {
		return "Error: 'task' is required — describe what the target persona should do"
	}

	packName := os.Getenv("ENSEMBLE_NAME")
	if packName == "" {
		return "Error: this agent is not part of a Ensemble — delegation requires a pack context. " +
			"ENSEMBLE_NAME environment variable is not set."
	}

	requestID := fmt.Sprintf("%d", time.Now().UnixNano())
	req := ipc.SpawnRequest{
		RequestID:     requestID,
		Task:          task,
		AgentID:       "default",
		TargetPersona: targetPersona,
		PackName:      packName,
	}

	reqPath := filepath.Join(spawnDir, fmt.Sprintf("request-%s.json", requestID))
	resPath := filepath.Join(spawnDir, fmt.Sprintf("result-%s.json", requestID))

	log.Printf("Delegating to persona %q in pack %q (requestID=%s): task=%s",
		targetPersona, packName, requestID, truncateStr(task, 100))
	detailedLog.LogAgent("delegate", map[string]any{"persona": targetPersona, "pack": packName, "request_id": requestID, "task": task})

	var result ipc.DelegateResult
	if err := ipcRoundTrip(ctx, reqPath, resPath, spawnPollInterval,
		delegateWaitBudget(ctx), req, &result); err != nil {
		log.Printf("Delegation to %q did not complete: %v", targetPersona, err)
		return fmt.Sprintf("Error: delegation to '%s' did not complete: %v", targetPersona, err)
	}

	if result.Status == "error" {
		log.Printf("Delegation to %q failed: %s", targetPersona, result.Error)
		return fmt.Sprintf("Delegation to '%s' failed: %s", targetPersona, result.Error)
	}
	log.Printf("Delegation to %q succeeded (%d bytes)", targetPersona, len(result.Response))
	return fmt.Sprintf("Result from %s:\n\n%s", targetPersona, result.Response)
}

// --- Spawn subagents tool (IPC-based) ---

// spawnSubagentsTool writes a batch spawn request to /ipc/spawn/ and blocks
// until all sub-agent children complete, returning the aggregated results.
func spawnSubagentsTool(ctx context.Context, args map[string]any) string {
	tasksRaw, ok := args["tasks"]
	if !ok {
		return "Error: 'tasks' is required — provide an array of {id, task} objects"
	}
	tasksSlice, ok := tasksRaw.([]any)
	if !ok || len(tasksSlice) == 0 {
		return "Error: 'tasks' must be a non-empty array"
	}

	strategy, _ := args["strategy"].(string)
	if strategy == "" {
		strategy = "parallel"
	}
	failurePolicy, _ := args["failurePolicy"].(string)

	var tasks []ipc.SubagentTask
	for i, t := range tasksSlice {
		m, ok := t.(map[string]any)
		if !ok {
			return fmt.Sprintf("Error: tasks[%d] is not an object", i)
		}
		id, _ := m["id"].(string)
		task, _ := m["task"].(string)
		if id == "" || task == "" {
			return fmt.Sprintf("Error: tasks[%d] requires both 'id' and 'task' fields", i)
		}
		entry := ipc.SubagentTask{
			ID:   id,
			Task: task,
		}
		if sp, ok := m["systemPrompt"].(string); ok {
			entry.SystemPrompt = sp
		}
		if to, ok := m["timeout"].(string); ok {
			entry.Timeout = to
		}
		tasks = append(tasks, entry)
	}

	batchID := fmt.Sprintf("%d", time.Now().UnixNano())
	req := ipc.SubagentSpawnRequest{
		BatchID:       batchID,
		Strategy:      strategy,
		FailurePolicy: failurePolicy,
		Tasks:         tasks,
	}

	reqPath := filepath.Join(spawnDir, fmt.Sprintf("subagent-request-%s.json", batchID))
	resPath := filepath.Join(spawnDir, fmt.Sprintf("subagent-result-%s.json", batchID))

	log.Printf("Spawning %d subagents (strategy=%s, failurePolicy=%s, batchID=%s)",
		len(tasks), strategy, failurePolicy, batchID)

	var result ipc.SubagentBatchResult
	if err := ipcRoundTrip(ctx, reqPath, resPath, spawnPollInterval,
		delegateWaitBudget(ctx), req, &result); err != nil {
		log.Printf("Subagent batch %s did not complete: %v", batchID, err)
		return fmt.Sprintf("Error: subagent batch did not complete: %v (batchId=%s)", err, batchID)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Subagent batch %s (status: %s)\n\n", batchID, result.Status))
	for _, r := range result.Results {
		sb.WriteString(fmt.Sprintf("### Task: %s", r.ID))
		if r.RunName != "" {
			sb.WriteString(fmt.Sprintf(" (run: %s)", r.RunName))
		}
		sb.WriteString("\n")
		if r.Status == "error" {
			sb.WriteString(fmt.Sprintf("**Error:** %s\n\n", r.Error))
		} else {
			sb.WriteString(fmt.Sprintf("%s\n\n", r.Response))
		}
	}

	log.Printf("Subagent batch %s completed (status=%s, results=%d)", batchID, result.Status, len(result.Results))
	return sb.String()
}

// --- Web fetch tool (runs in the agent container) ---

// fetchURLTool fetches a URL and returns the content as readable text.
// HTML pages are converted to plain text by stripping tags.
// JSON responses are returned as-is.
func fetchURLTool(args map[string]any) string {
	rawURL, _ := args["url"].(string)
	if rawURL == "" {
		return "Error: 'url' is required"
	}
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		return "Error: url must start with http:// or https://"
	}

	// Whatever a fetch returns is appended to the conversation and re-sent on
	// every subsequent round, so a large page is paid for many times over.
	// Bounding that is the job of CONTEXT_TOOL_RESULT_MAX_BYTES, which applies
	// to every tool rather than singling this one out.
	maxChars := 50_000
	if mc, ok := args["maxChars"].(float64); ok && mc > 0 {
		maxChars = int(mc)
	}
	if maxChars > 100_000 {
		maxChars = 100_000
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			DialContext: ssrfGuardedDialContext(),
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects")
			}
			return nil
		},
	}

	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return fmt.Sprintf("Error creating request: %v", err)
	}
	req.Header.Set("User-Agent", "Sympozium/1.0 (agent-runner)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json,text/plain,*/*")

	// Apply custom headers if provided.
	if hdrs, ok := args["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if sv, ok := v.(string); ok {
				req.Header.Set(k, sv)
			}
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Sprintf("Error fetching URL: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2000))
		return fmt.Sprintf("HTTP %d %s\n%s", resp.StatusCode, resp.Status, string(body))
	}

	// Limit read to 2MB to avoid memory issues.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return fmt.Sprintf("Error reading response body: %v", err)
	}

	contentType := resp.Header.Get("Content-Type")
	content := string(body)

	// If content looks like HTML, convert to readable text.
	if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "xhtml") ||
		(strings.Contains(contentType, "text/") == false && strings.HasPrefix(strings.TrimSpace(content), "<")) {
		content = htmlToText(content)
	}

	if len(content) > maxChars {
		content = content[:maxChars] + fmt.Sprintf("\n\n... (truncated at %d chars, total ~%d)", maxChars, len(string(body)))
	}

	log.Printf("Fetched URL %s: status=%d content-type=%s len=%d", rawURL, resp.StatusCode, contentType, len(content))
	return content
}

// htmlToText converts an HTML document to readable plain text by stripping
// tags, extracting text content, and cleaning up whitespace. Block-level
// elements produce line breaks. Script, style, and head elements are skipped.
func htmlToText(rawHTML string) string {
	tokenizer := html.NewTokenizer(strings.NewReader(rawHTML))

	var sb strings.Builder
	var skipDepth int // depth inside elements we want to skip

	// Elements whose content should be suppressed.
	skipTags := map[string]bool{
		"script":   true,
		"style":    true,
		"head":     true,
		"noscript": true,
		"svg":      true,
	}

	// Block-level elements that produce line breaks.
	blockTags := map[string]bool{
		"p": true, "div": true, "br": true, "hr": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
		"li": true, "tr": true, "blockquote": true, "pre": true,
		"section": true, "article": true, "header": true, "footer": true,
		"nav": true, "main": true, "aside": true, "figure": true,
		"figcaption": true, "details": true, "summary": true,
		"table": true, "thead": true, "tbody": true,
	}

	for {
		tt := tokenizer.Next()

		switch tt {
		case html.ErrorToken:
			goto done

		case html.StartTagToken, html.SelfClosingTagToken:
			tn, _ := tokenizer.TagName()
			tag := string(tn)

			if skipTags[tag] {
				skipDepth++
			}
			if skipDepth == 0 {
				if blockTags[tag] {
					sb.WriteString("\n")
				}
				if tag == "br" || tag == "hr" {
					sb.WriteString("\n")
				}
				// For links, try to extract href for context.
				if tag == "a" {
					href := getAttr(tokenizer, "href")
					if href != "" && !strings.HasPrefix(href, "#") && !strings.HasPrefix(href, "javascript:") {
						// We'll output the link text followed by the URL.
						// The text will come from TextToken below.
					}
				}
				if tag == "td" || tag == "th" {
					sb.WriteString("\t")
				}
			}

		case html.EndTagToken:
			tn, _ := tokenizer.TagName()
			tag := string(tn)

			if skipTags[tag] && skipDepth > 0 {
				skipDepth--
			}
			if skipDepth == 0 && blockTags[tag] {
				sb.WriteString("\n")
			}

		case html.TextToken:
			if skipDepth == 0 {
				text := string(tokenizer.Text())
				text = strings.TrimSpace(text)
				if text != "" {
					sb.WriteString(text)
					sb.WriteString(" ")
				}
			}
		}
	}

done:
	// Clean up excessive whitespace.
	result := sb.String()
	result = collapseWhitespace(result)
	return strings.TrimSpace(result)
}

// getAttr returns the value of the named attribute from the current token.
func getAttr(t *html.Tokenizer, name string) string {
	for {
		key, val, more := t.TagAttr()
		if string(key) == name {
			return string(val)
		}
		if !more {
			break
		}
	}
	return ""
}

// collapseWhitespace reduces runs of whitespace. Multiple blank lines become
// a single blank line; runs of spaces/tabs become a single space.
func collapseWhitespace(s string) string {
	var sb strings.Builder
	sb.Grow(len(s) / 2)

	lines := strings.Split(s, "\n")
	blankCount := 0
	for _, line := range lines {
		trimmed := strings.TrimRightFunc(line, unicode.IsSpace)
		// Collapse horizontal whitespace within the line.
		trimmed = collapseSpaces(trimmed)
		if trimmed == "" {
			blankCount++
			if blankCount <= 2 {
				sb.WriteString("\n")
			}
			continue
		}
		blankCount = 0
		sb.WriteString(trimmed)
		sb.WriteString("\n")
	}
	return sb.String()
}

// collapseSpaces reduces runs of spaces/tabs within a line to a single space.
func collapseSpaces(s string) string {
	var sb strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !prevSpace {
				sb.WriteRune(' ')
			}
			prevSpace = true
		} else {
			sb.WriteRune(r)
			prevSpace = false
		}
	}
	return sb.String()
}

// --- Write file tool (runs in the agent container) ---

func writeFileTool(args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		return "Error: 'path' is required"
	}
	content, _ := args["content"].(string)

	// Security: restrict to writable paths, symlink-resolved (via the deepest
	// existing ancestor) so a link under an allowed root cannot redirect the
	// write outside it.
	clean, err := resolveWritableTarget(path)
	if err != nil {
		return "Error: " + err.Error()
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(clean)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Sprintf("Error creating directory %s: %v", dir, err)
	}

	if err := os.WriteFile(clean, []byte(content), 0o644); err != nil {
		return fmt.Sprintf("Error writing file: %v", err)
	}

	log.Printf("Wrote file %s (%d bytes)", clean, len(content))
	return fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), clean)
}

// --- IPC-based command execution (runs in the sidecar container) ---

// execRequest matches the IPC ExecRequest protocol.
//
// Target is optional. When set, only the skill sidecar whose
// SYMPOZIUM_SKILL_PACK env var matches this value will claim the request.
// When empty, any attached sidecar may claim it (legacy behavior — racy in
// multi-sidecar pods).
type execRequest struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	WorkDir string   `json:"workDir,omitempty"`
	Timeout int      `json:"timeout,omitempty"`
	Target  string   `json:"target,omitempty"`
	// Argv, when non-empty, is executed directly as an argument vector by the
	// sidecar tool-executor WITHOUT a shell. This is the safe path used by
	// native sidecar tools: the executable and its arguments are passed as
	// discrete elements, so argument values cannot inject shell syntax. When
	// Argv is set, Command/Args are ignored.
	Argv []string `json:"argv,omitempty"`
	// Stdin is piped to the process when Argv is set. Used by stdin-mode native
	// tools to deliver a JSON payload without interpolating it into a command.
	Stdin string            `json:"stdin,omitempty"`
	Meta  map[string]string `json:"_meta,omitempty"`
}

// normalizeSidecarTarget returns the canonical form of a SkillPack target name
// used in execRequest.Target. It delegates to the shared helper so the
// agent-runner and the controller cannot drift on the normalization contract.
func normalizeSidecarTarget(s string) string {
	return sidecartools.NormalizeTarget(s)
}

// execResult matches the IPC ExecResult protocol.
type execResult struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	TimedOut bool   `json:"timedOut,omitempty"`
}

func executeCommand(ctx context.Context, args map[string]any) string {
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

	target, _ := args["target"].(string)
	// Normalize: trim whitespace and lowercase so SkillPack name comparisons
	// are tolerant of LLM casing variations ("Github-Gitops" vs "github-gitops").
	target = normalizeSidecarTarget(target)

	id := fmt.Sprintf("%d", time.Now().UnixNano())

	req := execRequest{
		ID:      id,
		Command: command,
		Args:    nil,
		WorkDir: workdir,
		Timeout: timeoutSec,
		Target:  target,
	}
	req.Meta = traceMetadata(ctx)

	return dispatchExecRequest(req, truncateStr(command, 120))
}

// dispatchExecRequest writes an exec request to the IPC tools directory, waits
// for the sidecar's result, and returns the formatted output. It is shared by
// execute_command (shell mode) and native sidecar tools (argv mode); the only
// difference is how the execRequest is constructed. label is a short
// human-readable summary for logging.
func dispatchExecRequest(req execRequest, label string) string {
	timeoutSec := req.Timeout
	if timeoutSec <= 0 {
		timeoutSec = 30
	}

	toolsDir := "/ipc/tools"
	reqPath := filepath.Join(toolsDir, fmt.Sprintf("exec-request-%s.json", req.ID))
	resPath := filepath.Join(toolsDir, fmt.Sprintf("exec-result-%s.json", req.ID))

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Sprintf("Error marshalling exec request: %v", err)
	}

	_ = os.MkdirAll(toolsDir, 0o755)
	if err := os.WriteFile(reqPath, data, 0o644); err != nil {
		return fmt.Sprintf("Error writing exec request: %v", err)
	}

	log.Printf("Wrote exec request %s: %s", req.ID, label)
	detailedLog.LogAgent("exec_request", map[string]any{"request_id": req.ID, "command": label})

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
	if len(output) > 8_000 {
		output = output[:8_000] + "\n... (output truncated)"
	}
	return output
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// --- Schedule task tool ---

// scheduleTaskTool writes a schedule request to /ipc/schedules/ for the
// IPC bridge to relay to the controller, which creates/updates a SympoziumSchedule.
func scheduleTaskTool(args map[string]any) string {
	name, _ := args["name"].(string)
	action, _ := args["action"].(string)
	schedule, _ := args["schedule"].(string)
	task, _ := args["task"].(string)

	if name == "" {
		return "Error: 'name' is required — a short unique name for this schedule"
	}
	if action == "" {
		return "Error: 'action' is required (create, update, suspend, resume, delete)"
	}

	// Validate required fields per action.
	switch action {
	case "create":
		if schedule == "" {
			return "Error: 'schedule' is required for create (cron expression, e.g. '0 */3 * * *')"
		}
		if task == "" {
			return "Error: 'task' is required for create — what should the agent do each time?"
		}
	case "update":
		if schedule == "" && task == "" {
			return "Error: 'schedule' and/or 'task' required for update — provide what you want to change"
		}
	case "suspend", "resume", "delete":
		// Only name + action needed.
	default:
		return fmt.Sprintf("Error: unknown action '%s' — use create, update, suspend, resume, or delete", action)
	}

	req := struct {
		Name     string `json:"name"`
		Action   string `json:"action"`
		Schedule string `json:"schedule,omitempty"`
		Task     string `json:"task,omitempty"`
	}{
		Name:     name,
		Action:   action,
		Schedule: schedule,
		Task:     task,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Sprintf("Error marshalling schedule request: %v", err)
	}

	dir := "/ipc/schedules"
	_ = os.MkdirAll(dir, 0o755)
	id := fmt.Sprintf("%d", time.Now().UnixNano())
	path := filepath.Join(dir, fmt.Sprintf("schedule-%s.json", id))

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Sprintf("Error writing schedule file: %v", err)
	}

	log.Printf("Wrote schedule request: name=%s action=%s schedule=%s", name, action, schedule)

	switch action {
	case "create":
		return fmt.Sprintf("Schedule '%s' created with cron '%s'. The task will run automatically on this interval.", name, schedule)
	case "update":
		parts := []string{}
		if schedule != "" {
			parts = append(parts, fmt.Sprintf("schedule='%s'", schedule))
		}
		if task != "" {
			parts = append(parts, "task updated")
		}
		return fmt.Sprintf("Schedule '%s' updated: %s", name, strings.Join(parts, ", "))
	case "suspend":
		return fmt.Sprintf("Schedule '%s' suspended. It will not fire until resumed.", name)
	case "resume":
		return fmt.Sprintf("Schedule '%s' resumed. Next run will fire according to the cron expression.", name)
	case "delete":
		return fmt.Sprintf("Schedule '%s' deleted.", name)
	default:
		return fmt.Sprintf("Schedule '%s' action '%s' submitted.", name, action)
	}
}
