# Writing Tools for Sympozium

This guide explains how to add new tools to the Sympozium agent-runner. Tools are the fundamental building blocks that give agents the ability to _do things_ — execute commands, read files, send messages, call APIs, and interact with the world.

---

## Configuration

### MAX_TOOL_ITERATIONS

Environment variable to configure the maximum number of LLM round-trips (default: 50). Each round consists of one LLM call that may produce multiple tool calls, followed by the agent executing all of those tool calls and feeding the results back to the LLM. A single round can therefore contain several parallel tool calls -- the limit controls how many times the LLM gets to reason and respond, not how many individual tools are invoked.

```bash
export MAX_TOOL_ITERATIONS=50  # Allow up to 50 LLM rounds
sympozium serve --agent-runner
```

Useful when working with models that require more reasoning steps, such as LM Studio with Qwen3.5.

Alternatively, set it per-AgentRun via the `spec.env` field:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: my-run
spec:
  agentRef: my-instance
  agentId: primary
  sessionKey: run-1
  task: "Analyze this complex problem"
  model:
    provider: ollama
    model: qwen3.5
    baseURL: http://localhost:11434/v1
    authSecretRef: ollama-key
  env:
    MAX_TOOL_ITERATIONS: "50"  # Max LLM rounds (each round may invoke multiple tools)
```

This is useful when different runs need different iteration limits without rebuilding the agent-runner image.

---

## Concepts

A **Tool** is a function the LLM can call during an agent run. Each tool has:

| Component | Purpose |
|-----------|---------|
| **Name** | Unique identifier (e.g. `execute_command`) |
| **Description** | Natural-language explanation the LLM reads to decide when to call it |
| **Parameters** | JSON Schema describing the arguments the LLM must provide |
| **Handler** | Go function that executes the tool and returns a result string |

Tools are registered in [`cmd/agent-runner/tools.go`](https://github.com/sympozium-ai/sympozium/blob/main/cmd/agent-runner/tools.go) and are always available to every agent run (when `TOOLS_ENABLED=true`, which is the default).

### Tools vs Skills

| | Tools | Skills |
|--|-------|--------|
| **What** | Code that runs inside the agent pod | Markdown instructions + optional sidecar |
| **Where** | Compiled into the `agent-runner` binary | Mounted at `/skills/` from ConfigMaps |
| **Scope** | Global — every agent run has the same tools | Per-instance — toggled on/off per Agent |
| **Examples** | `execute_command`, `send_channel_message` | `k8s-ops`, `incident-response`, `code-review` |

Think of tools as the agent's **hands** and skills as its **training**. A skill might say "run `kubectl get pods`", but the `execute_command` tool is what actually runs the command.

---

## Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│  Agent Pod                                                       │
│                                                                  │
│  ┌─────────────┐    /ipc/tools/     ┌────────────────┐          │
│  │ agent-runner │ ──exec-request──→ │ skill sidecar  │          │
│  │              │ ←──exec-result──  │ (kubectl, etc) │          │
│  │  ┌────────┐  │                   └────────────────┘          │
│  │  │  Tool  │  │    /ipc/messages/  ┌────────────────┐         │
│  │  │Registry│  │ ──send-*.json───→ │  IPC bridge    │──→ NATS  │
│  │  └────────┘  │                   └────────────────┘          │
│  │              │    direct I/O                                  │
│  │              │ ──read/list────→  local filesystem             │
│  └─────────────┘                                                │
└──────────────────────────────────────────────────────────────────┘
```

Tools fall into three categories based on how they execute:

| Category | Mechanism | Examples |
|----------|-----------|---------|
| **Native** | Direct Go code in the agent container | `read_file`, `list_directory` |
| **IPC (sidecar)** | File-based request/response via `/ipc/tools/` | `execute_command` |
| **IPC (bridge)** | File drop to `/ipc/messages/` relayed by the IPC bridge | `send_channel_message` |

---

## Step 1: Define the Tool

Add a constant and `ToolDef` entry to `cmd/agent-runner/tools.go`.

### Tool name constant

```go
const (
    ToolExecuteCommand     = "execute_command"
    ToolReadFile           = "read_file"
    ToolListDirectory      = "list_directory"
    ToolSendChannelMessage = "send_channel_message"
    ToolMyNewTool          = "my_new_tool"          // ← add yours
)
```

### Tool definition

Add the definition to `defaultTools()`:

```go
{
    Name: ToolMyNewTool,
    Description: "One-sentence description of what this tool does. " +
        "Be specific — the LLM uses this to decide when to call the tool. " +
        "Mention when to prefer this over other tools.",
    Parameters: map[string]any{
        "type": "object",
        "properties": map[string]any{
            "requiredParam": map[string]any{
                "type":        "string",
                "description": "What this parameter does.",
            },
            "optionalParam": map[string]any{
                "type":        "integer",
                "description": "What this parameter does. Defaults to 42.",
            },
        },
        "required": []string{"requiredParam"},
    },
},
```

### Parameter schema guidelines

| Do | Don't |
|----|-------|
| Use JSON Schema types: `string`, `integer`, `number`, `boolean`, `array`, `object` | Use Go types like `int64` or `[]string` |
| Add `enum` for constrained string values | Leave it open-ended when only N values are valid |
| Mark truly required params in `required` | Make everything required "just in case" |
| Add `description` to every property | Assume the LLM will guess parameter semantics |
| Describe default values in the description | Silently default without telling the LLM |

### Description guidelines

The `Description` field is critical — it's the primary signal the LLM uses to decide whether to call a tool. Write it as if you're explaining to a colleague when to use this function:

- **First sentence**: what it does (e.g. "Send a message to the user via a connected channel")
- **Second sentence**: when to use it (e.g. "Use this when the user asks you to notify them")
- **Third sentence**: any important caveats (e.g. "If no chatId is provided the message is sent to the device owner")

---

## Step 2: Implement the Handler

Add a handler function and wire it into the `executeToolCall` dispatcher.

### Wire it up

```go
func executeToolCall(name string, argsJSON string) string {
    // ... existing parsing ...

    switch name {
    case ToolExecuteCommand:
        return executeCommand(args)
    case ToolReadFile:
        return readFileTool(args)
    case ToolListDirectory:
        return listDirectoryTool(args)
    case ToolSendChannelMessage:
        return sendChannelMessageTool(args)
    case ToolMyNewTool:                          // ← add your case
        return myNewTool(args)
    default:
        return fmt.Sprintf("Unknown tool: %s", name)
    }
}
```

### Handler signature

Every handler has the same shape:

```go
func myNewTool(args map[string]any) string {
    // 1. Extract and validate parameters
    param, _ := args["requiredParam"].(string)
    if param == "" {
        return "Error: 'requiredParam' is required"
    }

    // 2. Do the work
    result, err := doSomething(param)
    if err != nil {
        return fmt.Sprintf("Error: %v", err)
    }

    // 3. Return a human-readable result string
    return result
}
```

**Key rules:**

| Rule | Why |
|------|-----|
| Return a string, never panic | The result is sent back to the LLM as context |
| Prefix errors with `"Error: "` | The LLM recognises this pattern and can retry or report |
| Truncate large outputs (50KB max) | Context windows are finite |
| Log the call with `log.Printf` | Appears in pod logs for debugging |
| Validate all inputs | The LLM can hallucinate parameters |

---

## Category-Specific Patterns

### Pattern A: Native Tool (direct I/O)

For tools that only need the agent container's filesystem or Go standard library:

```go
func myNativeTool(args map[string]any) string {
    path, _ := args["path"].(string)
    if path == "" {
        return "Error: 'path' is required"
    }

    // Security: restrict to allowed paths
    allowed := []string{"/workspace", "/skills", "/tmp", "/ipc"}
    ok := false
    for _, prefix := range allowed {
        if strings.HasPrefix(filepath.Clean(path), prefix) {
            ok = true
            break
        }
    }
    if !ok {
        return fmt.Sprintf("Error: access denied — path must be under %s",
            strings.Join(allowed, ", "))
    }

    data, err := os.ReadFile(path)
    if err != nil {
        return fmt.Sprintf("Error reading file: %v", err)
    }
    return string(data)
}
```

**When to use:** Reading files, environment variables, computing checksums, formatting data — anything that doesn't need external binaries or network calls.

### Pattern B: IPC Sidecar Tool (exec in sidecar)

!!! tip "Prefer declarative native sidecar tools"
    If you just want to expose a sidecar CLI as a typed, function-calling tool, you usually don't need to write Go at all — declare it under `sidecar.tools` on the SkillPack and the controller wires it up (read-only manifest, argv dispatch, admission validation). See [Native Sidecar Tools](../sidecars/writing-tool-sidecars.md#native-sidecar-tools). Reach for the Go pattern below only when the tool needs custom in-agent logic.

For tools that need to run commands in the skill sidecar container:

```go
func myExecTool(args map[string]any) string {
    command, _ := args["command"].(string)
    if command == "" {
        return "Error: 'command' is required"
    }

    id := fmt.Sprintf("%d", time.Now().UnixNano())

    req := execRequest{
        ID:      id,
        Command: command,
        Timeout: 30,
    }

    toolsDir := "/ipc/tools"
    reqPath := filepath.Join(toolsDir, fmt.Sprintf("exec-request-%s.json", id))
    resPath := filepath.Join(toolsDir, fmt.Sprintf("exec-result-%s.json", id))

    data, _ := json.Marshal(req)
    _ = os.MkdirAll(toolsDir, 0o755)
    if err := os.WriteFile(reqPath, data, 0o644); err != nil {
        return fmt.Sprintf("Error: %v", err)
    }

    // Poll for result
    deadline := time.Now().Add(40 * time.Second)
    for time.Now().Before(deadline) {
        resData, err := os.ReadFile(resPath)
        if err == nil && len(resData) > 0 {
            var result execResult
            if json.Unmarshal(resData, &result) == nil {
                _ = os.Remove(reqPath)
                _ = os.Remove(resPath)
                return formatExecResult(result)
            }
        }
        time.Sleep(150 * time.Millisecond)
    }
    return "Error: timed out waiting for command result"
}
```

**File protocol:**
```
Agent writes:   /ipc/tools/exec-request-<id>.json
Sidecar writes: /ipc/tools/exec-result-<id>.json
```

**When to use:** Running shell commands, calling CLI tools, any operation that requires binaries not in the agent container.

### Pattern C: IPC Bridge Tool (NATS relay)

For tools that publish messages through the IPC bridge to NATS:

```go
func myBridgeTool(args map[string]any) string {
    channel, _ := args["channel"].(string)
    text, _ := args["text"].(string)

    msg := struct {
        Channel string `json:"channel"`
        Text    string `json:"text"`
    }{Channel: channel, Text: text}

    data, _ := json.Marshal(msg)

    dir := "/ipc/messages"
    _ = os.MkdirAll(dir, 0o755)
    id := fmt.Sprintf("%d", time.Now().UnixNano())
    path := filepath.Join(dir, fmt.Sprintf("send-%s.json", id))

    if err := os.WriteFile(path, data, 0o644); err != nil {
        return fmt.Sprintf("Error: %v", err)
    }
    return "Message sent"
}
```

**File protocol:**
```
Agent writes: /ipc/messages/send-<id>.json
Bridge reads: (via fsnotify) → publishes to NATS topic
```

The IPC bridge (`internal/ipc/bridge.go`) watches `/ipc/messages/` and publishes each file as an event to `channel.message.send` on NATS. Channel pods (WhatsApp, Telegram, etc.) subscribe to this topic and deliver the message.

**When to use:** Sending messages through channels, publishing events, any communication that needs to leave the pod via NATS.

---

## Step 3: Update the System Prompt (if needed)

If your tool introduces a new capability the agent should proactively know about, update the system prompt builder in `cmd/agent-runner/skills.go`:

```go
// In buildSystemPrompt(), within the toolsEnabled block:
sb.WriteString("\n\n### My New Capability\n\n")
sb.WriteString("You have a `my_new_tool` tool that does X. Use it when Y.\n")
```

This is especially important for tools that:
- The agent should use proactively (not just reactively)
- Have non-obvious usage patterns (e.g. channel context, formatting conventions)
- Interact with external systems the agent needs to know about

---

## Step 4: Handle Channel/Environment Context

Some tools depend on runtime context. The controller passes context as environment variables to the agent container. Currently supported:

| Env Var | Set When | Purpose |
|---------|----------|---------|
| `TOOLS_ENABLED` | Always | Enables tool registration |
| `SOURCE_CHANNEL` | Run came from a channel | Originating channel type (e.g. `whatsapp`) |
| `SOURCE_CHAT_ID` | Run came from a channel | Chat ID to reply to |
| `INSTANCE_NAME` | Always | Name of the Agent |
| `AGENT_RUN_ID` | Always | Name of the AgentRun CR |

To add new context, pass it as an env var from the controller (`internal/controller/agentrun_controller.go` in `buildContainers()`) and read it in the agent-runner.

---

## Step 5: Add IPC Protocol Types (if needed)

If your tool introduces a new file-based IPC protocol, add the types to `internal/ipc/protocol.go`:

```go
// MyToolRequest is written to /ipc/mytool/request-*.json by the agent.
type MyToolRequest struct {
    ID     string `json:"id"`
    Param1 string `json:"param1"`
}

// MyToolResult is written to /ipc/mytool/result-*.json with results.
type MyToolResult struct {
    ID     string `json:"id"`
    Output string `json:"output"`
    Error  string `json:"error,omitempty"`
}
```

Then add a watcher in the IPC bridge (`internal/ipc/bridge.go`):

```go
// In Start():
go b.watchMyTool(ctx)

// Handler:
func (b *Bridge) watchMyTool(ctx context.Context) {
    path := filepath.Join(b.BasePath, "mytool")
    events, err := b.Watcher.Watch(ctx, path)
    // ... handle events, publish to NATS ...
}
```

---

## Testing

### Unit test the handler

```go
func TestMyNewTool(t *testing.T) {
    result := myNewTool(map[string]any{
        "requiredParam": "hello",
    })
    if strings.HasPrefix(result, "Error:") {
        t.Errorf("unexpected error: %s", result)
    }
}

func TestMyNewTool_MissingParam(t *testing.T) {
    result := myNewTool(map[string]any{})
    if !strings.HasPrefix(result, "Error:") {
        t.Errorf("expected error for missing param, got: %s", result)
    }
}
```

### Integration test in a pod

```bash
# Create a simple AgentRun that uses your tool
kubectl apply -f - <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: test-tool
spec:
  agentRef: my-instance
  agentId: primary
  sessionKey: test-tool-1
  task: "Use the my_new_tool tool with requiredParam='hello' and tell me the result."
  model:
    provider: openai
    model: gpt-4o-mini
    authSecretRef: my-instance-openai-key
EOF

# Watch the result
kubectl get agentrun test-tool -o jsonpath='{.status.result}'
```

### Check pod logs

```bash
kubectl logs <pod-name> -c agent | grep "tool call: my_new_tool"
```

---

## Built-in Tools Reference

### `execute_command`

| | |
|--|--|
| **Category** | IPC (sidecar) |
| **Parameters** | `command` (required), `workdir` (optional, default `/workspace`), `timeout` (optional, default 30, max 120) |
| **Requires** | Skill sidecar with the target binaries |
| **Returns** | Combined stdout/stderr with exit code |

Writes an `ExecRequest` to `/ipc/tools/exec-request-<id>.json`. The skill sidecar's tool-executor reads and executes it, writing the result to `/ipc/tools/exec-result-<id>.json`.

### `read_file`

| | |
|--|--|
| **Category** | Native |
| **Parameters** | `path` (required) |
| **Requires** | Nothing — runs in the agent container |
| **Returns** | File contents (truncated to 100KB) |

Security-restricted to paths under `/workspace`, `/skills`, `/tmp`, and `/ipc`.

### `list_directory`

| | |
|--|--|
| **Category** | Native |
| **Parameters** | `path` (required) |
| **Requires** | Nothing — runs in the agent container |
| **Returns** | Directory listing with type, size, and name |

### `send_channel_message`

| | |
|--|--|
| **Category** | IPC (bridge) |
| **Parameters** | `channel` (required: `whatsapp`, `telegram`, `discord`, `slack`), `text` (required), `chatId` (optional) |
| **Requires** | IPC bridge + channel pod connected to the target channel |
| **Returns** | Confirmation string |

Writes an `OutboundMessage` to `/ipc/messages/send-<id>.json`. The IPC bridge relays it to NATS topic `channel.message.send`. The corresponding channel pod picks it up and delivers it.

If `chatId` is empty, the message goes to the device owner (self-chat for WhatsApp, DM for others).

### `fetch_url`

| | |
|--|--|
| **Category** | Native |
| **Parameters** | `url` (required), `maxChars` (optional, default 50000, max 100000), `headers` (optional object) |
| **Requires** | Nothing — runs in the agent container (needs network access) |
| **Returns** | Page content as plain text (HTML tags stripped) or raw JSON for API responses |

Fetches a URL using `net/http` with a 30-second timeout and up to 5 redirects. HTML responses are converted to readable plain text by stripping tags, suppressing `<script>`, `<style>`, and `<head>` content, and collapsing whitespace. JSON and plain text responses are returned as-is. Output is truncated to `maxChars`.

### `write_file`

| | |
|--|--|
| **Category** | Native |
| **Parameters** | `path` (required), `content` (required) |
| **Requires** | Nothing — runs in the agent container |
| **Returns** | Confirmation with byte count |

Creates or overwrites a file at the given path. Parent directories are created automatically. Security-restricted to paths under `/workspace` and `/tmp`.

---

## Checklist for Adding a New Tool

- [ ] Add a `Tool*` constant in `tools.go`
- [ ] Add a `ToolDef` entry to `defaultTools()` with clear description and JSON Schema params
- [ ] Implement the handler function
- [ ] Wire the handler into the `executeToolCall` switch
- [ ] Add IPC protocol types if the tool uses file-based IPC (`internal/ipc/protocol.go`)
- [ ] Add a bridge watcher if the tool publishes to NATS (`internal/ipc/bridge.go`)
- [ ] Update the system prompt in `skills.go` if the agent should proactively know about the tool
- [ ] Pass any required env vars from the controller (`agentrun_controller.go` → `buildContainers()`)
- [ ] Write unit tests for the handler
- [ ] Test end-to-end with a real AgentRun
- [ ] Update this doc with the new tool in the reference section
