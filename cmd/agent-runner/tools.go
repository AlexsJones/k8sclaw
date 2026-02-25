package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/html"
)

// Tool name constants.
const (
	ToolExecuteCommand     = "execute_command"
	ToolReadFile           = "read_file"
	ToolWriteFile          = "write_file"
	ToolListDirectory      = "list_directory"
	ToolSendChannelMessage = "send_channel_message"
	ToolFetchURL           = "fetch_url"
	ToolScheduleTask       = "schedule_task"
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
		{
			Name: ToolSendChannelMessage,
			Description: "Send a message to the user via a connected channel (e.g. WhatsApp, Telegram, Discord, Slack). " +
				"Use this when the user asks you to notify them, send a summary, or deliver any text outside of the task result. " +
				"If no chatId is provided the message is sent to the device owner (self-chat).",
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
						"description": "A short unique name for this schedule (e.g. 'cluster-health-check', 'daily-report'). Used as the ClawSchedule resource name.",
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
	case ToolWriteFile:
		return writeFileTool(args)
	case ToolListDirectory:
		return listDirectoryTool(args)
	case ToolSendChannelMessage:
		return sendChannelMessageTool(args)
	case ToolFetchURL:
		return fetchURLTool(args)
	case ToolScheduleTask:
		return scheduleTaskTool(args)
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

// sendChannelMessageTool writes an outbound message to /ipc/messages/ for the
// IPC bridge to relay to the target channel (WhatsApp, Telegram, etc.).
func sendChannelMessageTool(args map[string]any) string {
	channel, _ := args["channel"].(string)
	text, _ := args["text"].(string)
	chatID, _ := args["chatId"].(string)

	if channel == "" {
		return "Error: 'channel' is required (whatsapp, telegram, discord, slack)"
	}
	if text == "" {
		return "Error: 'text' is required"
	}

	msg := struct {
		Channel string `json:"channel"`
		ChatID  string `json:"chatId,omitempty"`
		Text    string `json:"text"`
	}{
		Channel: channel,
		ChatID:  chatID,
		Text:    text,
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

	log.Printf("Wrote channel message: channel=%s chatId=%s len=%d", channel, chatID, len(text))
	target := chatID
	if target == "" {
		target = "owner (self)"
	}
	return fmt.Sprintf("Message sent to %s channel (target: %s)", channel, target)
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

	maxChars := 50_000
	if mc, ok := args["maxChars"].(float64); ok && mc > 0 {
		maxChars = int(mc)
	}
	if maxChars > 100_000 {
		maxChars = 100_000
	}

	client := &http.Client{
		Timeout: 30 * time.Second,
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
	req.Header.Set("User-Agent", "KubeClaw/1.0 (agent-runner)")
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

	// Security: restrict to writable paths.
	allowed := []string{"/workspace", "/tmp"}
	clean := filepath.Clean(path)
	ok := false
	for _, prefix := range allowed {
		if strings.HasPrefix(clean, prefix) {
			ok = true
			break
		}
	}
	if !ok {
		return fmt.Sprintf("Error: access denied — path must be under %s", strings.Join(allowed, ", "))
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

// --- Schedule task tool ---

// scheduleTaskTool writes a schedule request to /ipc/schedules/ for the
// IPC bridge to relay to the controller, which creates/updates a ClawSchedule.
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
