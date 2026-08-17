package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/sympozium-ai/sympozium/internal/ipc"
	"github.com/sympozium-ai/sympozium/internal/llmprovider"
)

// maxToolIterations is the maximum number of LLM reasoning rounds before
// the agent stops and returns whatever text it has. Each round consists of
// one LLM call (which may produce multiple parallel tool calls) followed by
// tool execution and feeding results back to the LLM.
var maxToolIterations = 50

// llmRequestTimeout is the per-request timeout for individual LLM API calls.
// For local providers (ollama, lm-studio, vllm) this prevents a single queued
// request from consuming the entire run budget. Cloud providers get no
// per-request timeout by default (they handle queuing server-side).
var llmRequestTimeout time.Duration // 0 means no per-request timeout

// llmMaxRetries is the maximum number of retries for LLM API calls.
// The SDK already uses exponential backoff (0.5s * 2^attempt, max 8s).
// Defaults: 2 for local providers, 5 for cloud providers.
var llmMaxRetries = -1 // -1 means "use provider-appropriate default"

// runTimeout is the overall context deadline for the entire agent run.
// Defaults: 10 minutes for cloud providers, 30 minutes for local providers.
// Override with RUN_TIMEOUT env var (Go duration string, e.g. "30m", "1h").
var runTimeout time.Duration // 0 means "use provider-appropriate default"

// AGENT_MODE values the agent-runner recognises. The controller sets
// AGENT_MODE for object-form spec.task entries (see internal/controller/taskmodes).
// Adding a new mode is a one-line change here + the corresponding handler in
// the taskmodes registry; the rest of the dispatch (logger + error message)
// is generated from this slice.
//
// Values must match the Mode() string each TaskModeHandler returns.
var (
	supportedAgentModePromptServer = "prompt-server"
)

// supportedAgentModes is the canonical list, ordered for stable logging.
// New modes append here so the on-disk log message is stable.
var supportedAgentModes = []string{
	supportedAgentModePromptServer,
}

// formatSupportedAgentModes returns the supported modes as a comma-separated
// string for human-readable error messages and startup logs.
func formatSupportedAgentModes() string {
	return strings.Join(supportedAgentModes, ", ")
}

func init() {
	if val := os.Getenv("MAX_TOOL_ITERATIONS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			maxToolIterations = n
		}
	}
	if val := os.Getenv("LLM_REQUEST_TIMEOUT"); val != "" {
		if d, err := time.ParseDuration(val); err == nil && d > 0 {
			llmRequestTimeout = d
		}
	}
	if val := os.Getenv("LLM_MAX_RETRIES"); val != "" {
		if n, err := strconv.Atoi(val); err == nil && n >= 0 {
			llmMaxRetries = n
		}
	}
	if val := os.Getenv("RUN_TIMEOUT"); val != "" {
		if d, err := time.ParseDuration(val); err == nil && d > 0 {
			runTimeout = d
		}
	}
}

// isLocalProvider returns true for providers that run inference locally
// (single-GPU, request queuing) where per-request timeouts matter.
func isLocalProvider(provider string) bool {
	return llmprovider.IsLocal(provider)
}

// effectiveMaxRetries returns the retry count for the given provider.
func effectiveMaxRetries(provider string) int {
	if llmMaxRetries >= 0 {
		return llmMaxRetries // explicit override
	}
	if isLocalProvider(provider) {
		return 2
	}
	return 5
}

// effectiveRequestTimeout returns the per-request timeout for the given provider.
func effectiveRequestTimeout(provider string) time.Duration {
	if llmRequestTimeout > 0 {
		return llmRequestTimeout // explicit override
	}
	if isLocalProvider(provider) {
		return 5 * time.Minute
	}
	return 0 // no per-request timeout for cloud providers
}

// effectiveRunTimeout returns the overall run context timeout.
func effectiveRunTimeout(provider string) time.Duration {
	if runTimeout > 0 {
		return runTimeout // explicit override via RUN_TIMEOUT env
	}
	if isLocalProvider(provider) {
		return 30 * time.Minute
	}
	return 10 * time.Minute
}

// delegateWaitBudget bounds how long delegate_to_persona and spawn_subagents
// block on a child's IPC result. The per-edge deadline lives in the Ensemble's
// relationships[].timeout and is enforced by the SpawnRouter, which unblocks the
// tool early with an error result. This is only the backstop that stops a tool
// call outliving the run itself, for when the router never answers. The
// 10-minute fallback applies to a context with no deadline (tests).
func delegateWaitBudget(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline).Round(time.Second)
	}
	return 10 * time.Minute
}

type agentResult struct {
	Status   string `json:"status"` // "success", "error", or "skipped" (see ipc.ResultStatusSkipped)
	Response string `json:"response,omitempty"`
	Error    string `json:"error,omitempty"`
	Metrics  struct {
		DurationMs   int64 `json:"durationMs"`
		InputTokens  int   `json:"inputTokens"`
		OutputTokens int   `json:"outputTokens"`
		ToolCalls    int   `json:"toolCalls"`
	} `json:"metrics"`
}

type streamChunk struct {
	Type    string `json:"type"`
	Content string `json:"content"`
	Index   int    `json:"index"`
}

// startupLogSupportedAgentModes prints the list of AGENT_MODE values this
// agent-runner binary understands, so an operator reading the pod logs can
// immediately see what the controller is allowed to set. Invoked once at
// startup, before the mode dispatch happens. Source-of-truth is the
// supportedAgentModes slice — adding a mode updates the log automatically.
func startupLogSupportedAgentModes() {
	log.Printf("supported AGENT_MODE values: %s", formatSupportedAgentModes())
}

// contextWithSignal returns a context that is cancelled when the parent
// context is cancelled OR when SIGTERM/SIGINT is received. Used by mode
// entry points (runPromptServer) that need to drain in-flight work on
// shutdown. The signal-based cancellation is necessary because the
// agent-runner is the long-lived primary process in prompt-server mode —
// without signal handling the controller's grace-period would just SIGKILL
// the pod and lose any in-flight prompt responses.
//
// Returns the context and a stop function the caller should defer to release
// the signal handler slot.
func contextWithSignal(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		select {
		case <-ctx.Done():
		case sig := <-stop:
			log.Printf("agent-runner: received signal %v, cancelling", sig)
			cancel()
		}
		// Drain the signal so a second SIGTERM during shutdown doesn't
		// immediately kill us. (We can't unregister a specific signal
		// cleanly across platforms, so just consume.)
		go func() {
			for range stop {
			}
		}()
	}()
	return ctx, func() {
		cancel()
		signal.Stop(stop)
		close(stop)
	}
}

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Println("agent-runner starting")
	startupLogSupportedAgentModes()

	// Skip mode: a preRun lifecycle hook wrote the skip marker on the shared
	// /ipc volume to signal there is no work to do. Short-circuit before the
	// LLM call (and before the empty-TASK guard below) so the run spends no
	// tokens; the controller marks the AgentRun as Skipped.
	if reason, skip := readSkipMarker(ipc.SkipMarkerPath); skip {
		log.Printf("SKIP mode — preRun hook requested skip: %s", reason)
		// Brief pause to let the ipc-bridge sidecar set up its fsnotify
		// watches on /ipc/output/ before we write the result and exit.
		time.Sleep(3 * time.Second)
		res := agentResult{
			Status:   ipc.ResultStatusSkipped,
			Response: reason,
		}
		_ = os.MkdirAll("/ipc/output", 0o755)
		writeJSON("/ipc/output/result.json", res)
		_ = os.WriteFile("/ipc/done", []byte("done"), 0o644)
		if markerBytes, err := json.Marshal(res); err == nil {
			fmt.Fprintf(os.Stdout, "\n__SYMPOZIUM_RESULT__%s__SYMPOZIUM_END__\n", string(markerBytes))
		}
		log.Println("agent-runner skipped")
		os.Exit(0)
	}

	if p := os.Getenv("DETAILED_LOG_PATH"); p != "" {
		maxSize, err := parseSize(os.Getenv("DETAILED_LOG_MAX_SIZE"), 50*1024*1024)
		if err != nil {
			log.Printf("WARNING: invalid DETAILED_LOG_MAX_SIZE: %v — using default 50MB", err)
		}
		dl, err := NewDetailedLogger(p, os.Getenv("AGENT_RUN_ID"), maxSize)
		if err != nil {
			log.Printf("WARNING: detailed logging disabled: %v", err)
		} else {
			detailedLog = dl
			defer dl.Close()
		}
	}

	task := getEnv("TASK", "")
	if task == "" {
		if b, err := os.ReadFile("/ipc/input/task.json"); err == nil {
			var input struct {
				Task string `json:"task"`
			}
			if json.Unmarshal(b, &input) == nil && input.Task != "" {
				task = input.Task
			}
		}
	}
	if task == "" {
		// AGENT_MODE selects the agent-runner's run-mode. The controller
		// sets AGENT_MODE for object-form spec.task entries (e.g.
		// {mode: "sidecar-driven"}); Path A (string-form task) leaves it
		// unset and the LLM-driven main agent loop runs below.
		//
		// Supported modes are listed in supportedAgentModes (declared at
		// package level so adding a new mode is a one-line change).
		// Unknown values fail with the supported list in the error so the
		// operator can see which mode they asked for vs. what's available.
		//
		// Object-mode handlers do not set TASK, so this branch fires for
		// every sidecar-driven run.
		switch mode := getEnv("AGENT_MODE", ""); mode {
		case "":
			// No AGENT_MODE and no TASK: classic misconfiguration.
			fatal("TASK env var is empty and no /ipc/input/task.json found (and AGENT_MODE is unset; supported modes: " + formatSupportedAgentModes() + ")")
		case supportedAgentModePromptServer:
			runCtx, cancel := contextWithSignal(context.Background())
			defer cancel()
			runPromptServer(runCtx)
			os.Exit(0)
		default:
			fatal(fmt.Sprintf("unsupported AGENT_MODE %q; supported: %v", mode, supportedAgentModes))
		}
	}

	// Dry run mode: produce a synthetic result without calling the LLM.
	// Used to trace sequential pipeline execution paths.
	if getEnv("DRY_RUN", "") == "true" {
		log.Println("DRY RUN mode — skipping LLM call")
		// Brief pause to let the ipc-bridge sidecar set up its fsnotify
		// watches on /ipc/output/ — the agent normally takes seconds to
		// minutes before writing results, but dry run exits in microseconds.
		time.Sleep(3 * time.Second)
		persona := getEnv("INSTANCE_NAME", "unknown")
		detailedLog.LogAgent("dry_run", map[string]any{"task": task, "persona": persona})
		res := agentResult{
			Status:   "success",
			Response: fmt.Sprintf("DRY RUN: [%s] would execute task: %s", persona, truncate(task, 300)),
		}
		_ = os.MkdirAll("/ipc/output", 0o755)
		writeJSON("/ipc/output/result.json", res)
		_ = os.WriteFile("/ipc/done", []byte("done"), 0o644)
		if markerBytes, err := json.Marshal(res); err == nil {
			fmt.Fprintf(os.Stdout, "\n__SYMPOZIUM_RESULT__%s__SYMPOZIUM_END__\n", string(markerBytes))
		}
		log.Println("agent-runner dry run finished")
		os.Exit(0)
	}

	// Canary mode: run deterministic health checks + one LLM connectivity test.
	if getEnv("CANARY_MODE", "") == "true" {
		result := runCanary(context.Background())
		emitCanaryResult(result)
		os.Exit(0)
	}

	systemPrompt := getEnv("SYSTEM_PROMPT", "You are a helpful AI assistant.")
	provider := strings.ToLower(getEnv("MODEL_PROVIDER", "openai"))
	modelName := getEnv("MODEL_NAME", "gpt-4o-mini")
	baseURL := strings.TrimRight(getEnv("MODEL_BASE_URL", ""), "/")
	memoryEnabled := getEnv("MEMORY_ENABLED", "") == "true"
	toolsEnabled := getEnv("TOOLS_ENABLED", "") == "true"

	var providerHeaders map[string]string
	if headersJSON := getEnv("MODEL_PROVIDER_HEADERS", ""); headersJSON != "" {
		if err := json.Unmarshal([]byte(headersJSON), &providerHeaders); err != nil {
			log.Printf("WARNING: failed to parse MODEL_PROVIDER_HEADERS: %v", err)
		} else {
			log.Printf("provider_headers: %d custom header(s) configured", len(providerHeaders))
		}
	}

	// Load skill files and build enhanced system prompt.
	skills := loadSkills(defaultSkillsDir)
	systemPrompt = buildSystemPrompt(systemPrompt, skills, toolsEnabled)

	// If this run was triggered from a channel, inject context so the
	// agent knows how to reply through the originating channel.
	sourceChannel := getEnv("SOURCE_CHANNEL", "")
	sourceChatID := getEnv("SOURCE_CHAT_ID", "")
	sourceThreadID := getEnv("SOURCE_THREAD_ID", "")
	if sourceChannel != "" {
		channelCtx := fmt.Sprintf(
			"\n\n## Channel Context\n\n"+
				"This task was received through the **%s** channel (chat ID: %s). "+
				"You can reply through this channel using the `send_channel_message` tool "+
				"with channel=%q and chatId=%q. Use it to deliver results, ask follow-up "+
				"questions, or send notifications to the user.",
			sourceChannel, sourceChatID, sourceChannel, sourceChatID,
		)
		if sourceThreadID != "" {
			channelCtx += fmt.Sprintf(
				" The originating message is in thread %q — pass threadId=%q to keep "+
					"replies in the same thread.",
				sourceThreadID, sourceThreadID,
			)
		}
		systemPrompt += channelCtx
		log.Printf("channel context injected: channel=%s chatId=%s threadId=%s", sourceChannel, sourceChatID, sourceThreadID)
	}

	// If this agent is part of an ensemble with relationships, inject
	// delegation/supervision guidance so the LLM knows when and how to
	// use delegate_to_persona. This works for both built-in and
	// user-created dynamic ensembles.
	if relJSON := getEnv("ENSEMBLE_RELATIONSHIPS", ""); relJSON != "" {
		if ctx := buildRelationshipContext(relJSON); ctx != "" {
			systemPrompt += ctx
			log.Printf("relationship context injected for persona %s", getEnv("PERSONA_NAME", "unknown"))
		}
	}

	// Resolve tool definitions.
	var tools []ToolDef
	if toolsEnabled {
		tools = defaultTools()
		// Load MCP tools from manifest if the mcp-bridge sidecar is running
		if mcpTools := loadMCPTools("/ipc/tools/mcp-tools.json"); len(mcpTools) > 0 {
			tools = append(tools, mcpTools...)

			// Group tools by server prefix
			serverTools := make(map[string][]string)
			for _, t := range mcpTools {
				parts := strings.SplitN(t.Name, "_", 2)
				prefix := parts[0]
				serverTools[prefix] = append(serverTools[prefix], t.Name)
			}

			var sb strings.Builder
			sb.WriteString("\n\n## Specialized MCP Tools\n\n")
			sb.WriteString(fmt.Sprintf("You have access to %d specialized MCP tools. ", len(mcpTools)))
			sb.WriteString("ALWAYS prefer MCP tools over execute_command when a relevant MCP tool exists.\n\n")
			sb.WriteString("### Diagnostic Methodology\n")
			sb.WriteString("1. **Start targeted**: Use the most specific MCP tool for the problem first\n")
			sb.WriteString("2. **Don't shotgun**: Avoid calling many tools to 'gather info' — diagnose step by step\n")
			sb.WriteString("3. **Read results carefully**: Each MCP tool returns structured diagnostic data. Analyze it before calling more tools.\n")
			sb.WriteString("4. **gRPC != HTTP**: For gRPC issues, check port naming (grpc-*), appProtocol, H2 settings, DestinationRules — NOT path routing\n")
			sb.WriteString("5. **Only fall back to execute_command** for tasks no MCP tool covers (e.g., reading app logs)\n\n")

			sb.WriteString("### Available Tool Groups\n")
			for prefix, tools := range serverTools {
				sb.WriteString(fmt.Sprintf("- **%s** (%d tools): %s\n", prefix, len(tools), strings.Join(tools, ", ")))
			}

			systemPrompt += sb.String()
		}

		// Load native sidecar tools from the controller-written, read-only
		// manifest (declared on SkillPack CRDs). These are presented to the
		// model as typed function-calling tools and dispatch through the gated
		// exec IPC targeting their owning sidecar.
		if manifestPath := getEnv("SIDECAR_TOOLS_MANIFEST_PATH", ""); manifestPath != "" {
			if sidecarTools := loadSidecarTools(manifestPath); len(sidecarTools) > 0 {
				tools = append(tools, sidecarTools...)

				var sb strings.Builder
				sb.WriteString("\n\n## Native Sidecar Tools\n\n")
				sb.WriteString(fmt.Sprintf("You have %d native sidecar tool(s) that accept structured JSON arguments. ", len(sidecarTools)))
				sb.WriteString("ALWAYS prefer a native sidecar tool over execute_command when one matches the task — ")
				sb.WriteString("they are more reliable than constructing shell commands.\n\n")
				sb.WriteString("Available: ")
				names := make([]string, 0, len(sidecarTools))
				for _, t := range sidecarTools {
					names = append(names, t.Name)
				}
				sb.WriteString(strings.Join(names, ", "))
				sb.WriteString("\n")
				systemPrompt += sb.String()
			}
		}
		log.Printf("tools enabled: %d tool(s) registered", len(tools))
	}

	// Establish the run timeout, observability, and the distributed trace
	// context BEFORE any memory auto-injection. The startup memory queries
	// below (queryMemoryContext / queryWorkflowMemoryContext) issue real
	// /search calls to the memory server; if the run span and the global OTel
	// propagator are not yet set up, those calls carry no W3C traceparent and
	// surface as orphaned single-span traces instead of nesting under the BMAD
	// chain (ISI-1406: board observed /search and /list disconnected while
	// /store was end-to-end). Wiring trace setup first lets the pre-flight
	// reads join the same trace as the rest of the run.
	rt := effectiveRunTimeout(provider)
	log.Printf("run_timeout=%s", rt)
	ctx, cancel := context.WithTimeout(context.Background(), rt)
	defer cancel()

	obs := initObservability(ctx)
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer shutdownCancel()
		if err := obs.shutdown(shutdownCtx); err != nil {
			log.Printf("failed to shutdown OTel providers: %v", err)
		}
	}()

	// Extract TRACEPARENT from env so the runner trace joins the controller trace.
	if tp := os.Getenv("TRACEPARENT"); tp != "" {
		debug := os.Getenv("DEBUG") == "true"
		if debug {
			log.Printf("TRACEPARENT env var found: %s", tp)
		}
		prop := propagation.TraceContext{}
		carrier := propagation.MapCarrier{"traceparent": tp}
		ctx = prop.Extract(ctx, carrier)
		if debug {
			sc := oteltrace.SpanContextFromContext(ctx)
			log.Printf("after extraction: traceID=%s spanID=%s remote=%v valid=%v", sc.TraceID(), sc.SpanID(), sc.IsRemote(), sc.IsValid())
		}
	} else if os.Getenv("DEBUG") == "true" {
		log.Println("TRACEPARENT env var not set")
	}

	ctx, runSpan := obs.startRunSpan(ctx,
		attribute.String("instance", getEnv("INSTANCE_NAME", "")),
		attribute.String("tenant.namespace", getEnv("AGENT_NAMESPACE", "")),
		attribute.String("model", modelName),
		attribute.String("task.summary", truncate(task, 200)),
	)
	writeTraceContextMetadata(ctx)
	detailedLog.LogAgent("span_start", map[string]any{"task": task})
	logWithTrace(ctx, "info", "agent run started", map[string]any{
		"instance":  getEnv("INSTANCE_NAME", ""),
		"namespace": getEnv("AGENT_NAMESPACE", ""),
		"provider":  provider,
		"model":     modelName,
	})

	// Load memory tools if the memory server is available (standalone deployment).
	if memoryTools := initMemoryTools(); len(memoryTools) > 0 {
		tools = append(tools, memoryTools...)

		// Auto-inject relevant memory context so the agent has immediate
		// awareness of past findings without relying on it to call memory_search.
		var memoryContextBlock string
		if memCtx := queryMemoryContext(ctx, task, 3); memCtx != "" {
			memoryContextBlock = "\n\n## Relevant Past Context (auto-retrieved)\n\n" +
				"The following memories were automatically retrieved based on your current task:\n\n" +
				memCtx
			log.Printf("auto-injected %d bytes of memory context", len(memCtx))
		}

		systemPrompt += "\n\n## Persistent Memory\n\n" +
			"You have access to persistent memory tools that survive across runs.\n"

		if memoryContextBlock != "" {
			systemPrompt += memoryContextBlock + "\n\n" +
				"Use `memory_search` for additional lookups beyond what was auto-loaded above.\n"
		} else {
			systemPrompt += "**Before starting any investigation**, call `memory_search` with relevant keywords " +
				"to check if similar issues have been diagnosed before.\n"
		}

		systemPrompt += "**After completing your task**, call `memory_store` to save key findings, " +
			"root causes, and resolution steps for future reference.\n" +
			"Be specific in stored content — include service names, namespaces, error messages, and timestamps."

		log.Printf("memory tools loaded: %d tool(s)", len(memoryTools))
	} else if memoryEnabled {
		// Fallback: legacy ConfigMap memory for backward compatibility.
		var memoryContent string
		if b, err := os.ReadFile("/memory/MEMORY.md"); err == nil {
			memoryContent = strings.TrimSpace(string(b))
			log.Printf("loaded legacy memory (%d bytes)", len(memoryContent))
		}
		if memoryContent != "" && memoryContent != "# Agent Memory\n\nNo memories recorded yet." {
			task = fmt.Sprintf("## Your Memory\nThe following is your persistent memory from prior interactions:\n\n%s\n\n## Current Task\n%s", memoryContent, task)
		}
		if memoryEnabled {
			memoryInstruction := "\n\nYou have persistent memory. After completing your task, " +
				"output a memory update block wrapped in markers like this:\n" +
				"__SYMPOZIUM_MEMORY__\n<your updated MEMORY.md content>\n__SYMPOZIUM_MEMORY_END__\n" +
				"Include key facts, preferences, and context from this and past interactions. " +
				"Keep it concise (under 256KB). Use markdown format."
			systemPrompt += memoryInstruction
		}
	}

	// Load shared workflow memory tools if configured (pack-level shared memory).
	if wfMemTools := initWorkflowMemoryTools(); len(wfMemTools) > 0 {
		tools = append(tools, wfMemTools...)

		// Auto-inject shared team memory context alongside private memory.
		if wfMemCtx := queryWorkflowMemoryContext(ctx, task, 3); wfMemCtx != "" {
			systemPrompt += "\n\n## Team Knowledge (Shared Workflow Memory)\n\n" +
				"The following shared memories were contributed by other personas in your team:\n\n" +
				wfMemCtx + "\n\n" +
				"Use `workflow_memory_search` for additional team knowledge lookups.\n"
			log.Printf("auto-injected %d bytes of shared workflow memory context", len(wfMemCtx))
		}

		systemPrompt += "\n\n## Shared Workflow Memory\n\n" +
			"You have access to shared team memory tools (`workflow_memory_search`, `workflow_memory_list`"
		if workflowMemoryAccess != "read-only" {
			systemPrompt += ", `workflow_memory_store`"
		}
		systemPrompt += ") that are shared across all personas in your team.\n"
		if workflowMemoryAccess != "read-only" {
			systemPrompt += "**After completing your task**, use `workflow_memory_store` to share key findings " +
				"with other team members. Your persona name is automatically attached for attribution.\n"
		}

		log.Printf("workflow memory tools loaded: %d tool(s) (access: %s)", len(wfMemTools), workflowMemoryAccess)
	}

	// Apply tool policy: allow/deny lists filter the assembled tool set.
	allowList := os.Getenv("TOOL_POLICY_ALLOW")
	denyList := os.Getenv("TOOL_POLICY_DENY")
	if allowList != "" || denyList != "" {
		tools = applyToolPolicy(tools, allowList, denyList)
	}

	apiKey := firstNonEmpty(
		os.Getenv("API_KEY"),
		os.Getenv("OPENAI_API_KEY"),
		os.Getenv("ANTHROPIC_API_KEY"),
		os.Getenv("AZURE_OPENAI_API_KEY"),
		os.Getenv("PROVIDER_API_KEY"),
	)

	log.Printf("provider=%s model=%s baseURL=%s tools=%v task=%q",
		provider, modelName, baseURL, toolsEnabled, truncate(task, 80))
	detailedLog.LogAgent("config", map[string]any{"provider": provider, "model": modelName, "base_url": baseURL, "tools_enabled": toolsEnabled, "task": task})
	reqTimeout := effectiveRequestTimeout(provider)
	retries := effectiveMaxRetries(provider)
	if reqTimeout > 0 {
		log.Printf("llm_request_timeout=%s llm_max_retries=%d max_tool_iterations=%d",
			reqTimeout, retries, maxToolIterations)
	} else {
		log.Printf("llm_request_timeout=none llm_max_retries=%d max_tool_iterations=%d",
			retries, maxToolIterations)
	}

	_ = os.MkdirAll("/ipc/output", 0o755)

	start := time.Now()

	var (
		responseText string
		inputTokens  int
		outputTokens int
		toolCalls    int
		err          error
	)

	// Launch the prompt service in parallel with the main agent loop. The
	// sidecar-initiated orchestrator drives its workflow from inside one of
	// the agent's tool calls and writes /ipc/prompts/request-*.json while the
	// main loop is still running, so the prompt service must be live *during*
	// the main loop, not only after it. The goroutine exits when /ipc/done is
	// written, the run context is cancelled, or the loop returns an error.
	// promptWg.Wait (deferred below) guarantees the prompt service has drained
	// before the agent-runner exits, so the sidecar's request poll never times
	// out waiting for a result that arrives after the runner tore down.
	var promptWg sync.WaitGroup
	if getEnv("ENABLE_PROMPT_SERVICE", "") == "true" {
		promptWg.Add(1)
		go func() {
			defer promptWg.Done()
			if promptErr := runPromptServiceLoop(promptServiceDeps{
				Ctx:             ctx,
				ProviderName:    provider,
				APIKey:          apiKey,
				BaseURL:         baseURL,
				Model:           modelName,
				SystemPrompt:    systemPrompt,
				Task:            task,
				Tools:           tools,
				ProviderHeaders: providerHeaders,
			}); promptErr != nil {
				log.Printf("prompt service exited with error: %v", promptErr)
			}
		}()
	}
	defer promptWg.Wait()

	switch provider {
	case "anthropic":
		responseText, inputTokens, outputTokens, toolCalls, err = callAnthropic(ctx, apiKey, baseURL, modelName, systemPrompt, task, tools, providerHeaders)
	case "bedrock":
		if len(providerHeaders) > 0 {
			log.Printf("WARNING: custom provider headers are not supported for the Bedrock provider")
		}
		responseText, inputTokens, outputTokens, toolCalls, err = callBedrock(ctx, modelName, systemPrompt, task, tools)
	default:
		// OpenAI, Azure OpenAI, Ollama, LM Studio, and any OpenAI-compatible provider
		responseText, inputTokens, outputTokens, toolCalls, err = callOpenAI(ctx, provider, apiKey, baseURL, modelName, systemPrompt, task, tools, providerHeaders)
	}

	// After the main agent loop completes, the prompt service continues to
	// serve sidecar-initiated LLM prompt requests until /ipc/done is
	// written (typically by the sidecar after it has finished its work) or
	// the run context is cancelled. Sidecars write /ipc/prompts/request-*.json
	// to ask the model a question; we answer with the same provider (so
	// context survives) and write the response to /ipc/prompts/result-*.json.
	// promptWg.Wait (above via defer) drains the goroutine before this
	// function returns.

	elapsed := time.Since(start)

	var res agentResult
	res.Metrics.DurationMs = elapsed.Milliseconds()
	res.Metrics.ToolCalls = toolCalls

	debugMode := getEnv("DEBUG", "") == "true"

	if err != nil {
		log.Printf("LLM call failed: %v", err)
		res.Status = "error"
		res.Error = err.Error()
		markSpanError(runSpan, err)
		runSpan.SetStatus(codes.Error, err.Error())
	} else {
		log.Printf("LLM call succeeded (tokens: in=%d out=%d, tool_calls=%d)", inputTokens, outputTokens, toolCalls)
		res.Status = "success"
		res.Response = responseText
		res.Metrics.InputTokens = inputTokens
		res.Metrics.OutputTokens = outputTokens
		runSpan.SetAttributes(
			attribute.Int("gen_ai.usage.input_tokens", inputTokens),
			attribute.Int("gen_ai.usage.output_tokens", outputTokens),
			attribute.Int("gen_ai.tool.call.count", toolCalls),
		)
		runSpan.SetStatus(codes.Ok, "")
	}

	// Auto-store task/response in memory server for future context.
	// Must be synchronous — the process exits shortly after this point,
	// so a goroutine would be killed before the HTTP POST completes.
	if res.Status == "success" && res.Response != "" {
		autoStoreMemory(ctx, task, res.Response)
	}

	// Extract and emit memory update before stripping markers from the response.
	if memoryEnabled && res.Response != "" {
		if memUpdate := extractMemoryUpdate(res.Response); memUpdate != "" {
			fmt.Fprintf(os.Stdout, "\n__SYMPOZIUM_MEMORY__%s__SYMPOZIUM_MEMORY_END__\n", memUpdate)
			log.Printf("emitted memory update (%d bytes)", len(memUpdate))
		}
	}

	// Strip memory markers from the response so they don't appear in the
	// TUI feed or channel messages. Keep them only if DEBUG is enabled.
	if !debugMode && res.Response != "" {
		res.Response = stripMemoryMarkers(res.Response)
	}

	if res.Response != "" {
		writeJSON("/ipc/output/stream-0.json", streamChunk{
			Type:    "text",
			Content: res.Response,
			Index:   0,
		})
	}

	writeJSON("/ipc/output/result.json", res)

	// Signal sidecars (tool-executor, etc.) to exit by writing a done sentinel.
	_ = os.WriteFile("/ipc/done", []byte("done"), 0o644)

	// Print a structured marker to stdout so the controller can extract
	// the result from pod logs even after the IPC volume is gone.
	if markerBytes, err := json.Marshal(res); err == nil {
		fmt.Fprintf(os.Stdout, "\n__SYMPOZIUM_RESULT__%s__SYMPOZIUM_END__\n", string(markerBytes))
	}

	if res.Status == "error" {
		obs.recordRunMetrics(ctx, "error", getEnv("INSTANCE_NAME", ""), modelName, getEnv("AGENT_NAMESPACE", ""), elapsed.Milliseconds(), inputTokens, outputTokens)
		logWithTrace(ctx, "error", "agent run failed", map[string]any{"error": res.Error})
		runSpan.End()
		log.Printf("agent-runner finished with error: %s", res.Error)
		os.Exit(1)
	}
	obs.recordRunMetrics(ctx, "success", getEnv("INSTANCE_NAME", ""), modelName, getEnv("AGENT_NAMESPACE", ""), elapsed.Milliseconds(), inputTokens, outputTokens)
	logWithTrace(ctx, "info", "agent run succeeded", map[string]any{
		"duration_ms":   elapsed.Milliseconds(),
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
		"tool_calls":    toolCalls,
	})
	runSpan.End()
	log.Println("agent-runner finished successfully")
}

// callAnthropic dispatches an agent run to the Anthropic provider.
// Retained as a thin wrapper around newAnthropicProvider + runAgentLoop for
// backward-compatible test coverage.
func callAnthropic(ctx context.Context, apiKey, baseURL, model, systemPrompt, task string, tools []ToolDef, headers map[string]string) (string, int, int, int, error) {
	p := newAnthropicProvider(apiKey, baseURL, model, systemPrompt, task, tools, headers)
	return runAgentLoop(ctx, p, tools)
}

// callOpenAI dispatches an agent run to the OpenAI-compatible provider path
// (OpenAI, LM Studio, Ollama, vLLM, Azure OpenAI, …).
func callOpenAI(ctx context.Context, provider, apiKey, baseURL, model, systemPrompt, task string, tools []ToolDef, headers map[string]string) (string, int, int, int, error) {
	p, err := newOpenAIProvider(provider, apiKey, baseURL, model, systemPrompt, task, tools, headers)
	if err != nil {
		return "", 0, 0, 0, err
	}
	return runAgentLoop(ctx, p, tools)
}

// callBedrock dispatches an agent run to the AWS Bedrock provider.
func callBedrock(ctx context.Context, model, systemPrompt, task string, tools []ToolDef) (string, int, int, int, error) {
	p, err := newBedrockProvider(ctx, model, systemPrompt, task, tools)
	if err != nil {
		return "", 0, 0, 0, err
	}
	return runAgentLoop(ctx, p, tools)
}

// callBedrockWithClient accepts a pre-built client; used by tests to inject
// a mock Bedrock client without hitting AWS.
func callBedrockWithClient(ctx context.Context, client bedrockClientAPI, model, systemPrompt, task string, tools []ToolDef) (string, int, int, int, error) {
	p, err := newBedrockProviderWithClient(client, model, systemPrompt, task, tools)
	if err != nil {
		return "", 0, 0, 0, err
	}
	return runAgentLoop(ctx, p, tools)
}

func writeJSON(path string, v any) {
	dir := filepath.Dir(path)
	_ = os.MkdirAll(dir, 0o755)
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Printf("WARNING: failed to marshal JSON for %s: %v", path, err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf("WARNING: failed to write %s: %v", path, err)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// readSkipMarker reports whether a preRun hook requested the run be skipped by
// writing the skip marker at path on the shared /ipc volume. The trimmed file
// contents are returned as the human-readable skip reason.
func readSkipMarker(path string) (string, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	reason := strings.TrimSpace(string(b))
	if reason == "" {
		reason = "preRun hook requested skip"
	}
	return reason, true
}

// applyToolPolicy filters tools based on allow/deny lists.
// - deny only: tools in the deny list are removed (blocklist mode)
// - allow only: all tools not in the allow list are denied (allowlist mode)
// - both: deny wins on conflict — a tool in both lists is denied (least privilege)
func applyToolPolicy(tools []ToolDef, allowList, denyList string) []ToolDef {
	allowed := make(map[string]bool)
	for _, name := range strings.Split(allowList, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			allowed[name] = true
		}
	}
	denied := make(map[string]bool)
	for _, name := range strings.Split(denyList, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			denied[name] = true
		}
	}
	useAllowlist := len(allowed) > 0
	filtered := make([]ToolDef, 0, len(tools))
	for _, t := range tools {
		if denied[t.Name] {
			log.Printf("tool policy: denied tool %q", t.Name)
			continue
		}
		if useAllowlist && !allowed[t.Name] {
			log.Printf("tool policy: tool %q not in allow list", t.Name)
			continue
		}
		filtered = append(filtered, t)
	}
	return filtered
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func fatal(msg string) {
	log.Println("FATAL: " + msg)
	_ = os.MkdirAll("/ipc/output", 0o755)
	_ = os.WriteFile("/ipc/done", []byte("done"), 0o644)
	writeJSON("/ipc/output/result.json", agentResult{
		Status: "error",
		Error:  msg,
	})
	os.Exit(1)
}

// extractMemoryUpdate looks for a memory update block in the LLM response.
// The agent is instructed to wrap its memory updates in:
//
//	__SYMPOZIUM_MEMORY__
//	<content>
//	__SYMPOZIUM_MEMORY_END__
func extractMemoryUpdate(response string) string {
	const startMarker = "__SYMPOZIUM_MEMORY__"
	const endMarker = "__SYMPOZIUM_MEMORY_END__"

	startIdx := strings.LastIndex(response, startMarker)
	if startIdx < 0 {
		return ""
	}
	payload := response[startIdx+len(startMarker):]
	endIdx := strings.Index(payload, endMarker)
	if endIdx < 0 {
		return ""
	}
	return strings.TrimSpace(payload[:endIdx])
}

// stripMemoryMarkers removes all __SYMPOZIUM_MEMORY__...END__ blocks from the
// response text so they don't appear in the TUI feed or channel messages.
func stripMemoryMarkers(response string) string {
	const startMarker = "__SYMPOZIUM_MEMORY__"
	const endMarker = "__SYMPOZIUM_MEMORY_END__"

	for {
		startIdx := strings.Index(response, startMarker)
		if startIdx < 0 {
			break
		}
		endIdx := strings.Index(response[startIdx:], endMarker)
		if endIdx < 0 {
			// Unclosed marker — strip from startMarker to end of string.
			response = strings.TrimSpace(response[:startIdx])
			break
		}
		// Remove the entire marker block.
		response = response[:startIdx] + response[startIdx+endIdx+len(endMarker):]
	}
	return strings.TrimSpace(response)
}

// buildRelationshipContext parses the ENSEMBLE_RELATIONSHIPS JSON and produces
// a system prompt section instructing the LLM how to use delegate_to_persona
// and what supervision edges exist.
func buildRelationshipContext(relJSON string) string {
	type rel struct {
		Target      string `json:"target"`
		DisplayName string `json:"displayName,omitempty"`
		Type        string `json:"type"`
		Condition   string `json:"condition,omitempty"`
	}
	var rels []rel
	if err := json.Unmarshal([]byte(relJSON), &rels); err != nil || len(rels) == 0 {
		return ""
	}

	var delegations, supervisions []rel
	for _, r := range rels {
		switch r.Type {
		case "delegation":
			delegations = append(delegations, r)
		case "supervision":
			supervisions = append(supervisions, r)
		}
	}

	if len(delegations) == 0 && len(supervisions) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## Team Relationships\n\n")

	if len(delegations) > 0 {
		sb.WriteString("You can delegate tasks to the following team members using the ")
		sb.WriteString("`delegate_to_persona` tool. When a task matches their expertise, ")
		sb.WriteString("delegate rather than handling it yourself.\n\n")
		for _, d := range delegations {
			label := d.Target
			if d.DisplayName != "" {
				label = fmt.Sprintf("%s (%s)", d.DisplayName, d.Target)
			}
			sb.WriteString(fmt.Sprintf("- **%s**", label))
			if d.Condition != "" {
				sb.WriteString(fmt.Sprintf(" — %s", d.Condition))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\nTo delegate, call `delegate_to_persona` with `targetPersona` set to ")
		sb.WriteString("the persona name (e.g. ")
		sb.WriteString(fmt.Sprintf("%q", delegations[0].Target))
		sb.WriteString(") and a clear `task` description with all necessary context.\n")
	}

	if len(supervisions) > 0 {
		sb.WriteString("\nYou supervise the following team members (read-only monitoring):\n\n")
		for _, s := range supervisions {
			label := s.Target
			if s.DisplayName != "" {
				label = fmt.Sprintf("%s (%s)", s.DisplayName, s.Target)
			}
			sb.WriteString(fmt.Sprintf("- **%s**", label))
			if s.Condition != "" {
				sb.WriteString(fmt.Sprintf(" — %s", s.Condition))
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}

// runPromptServer is the entry point for AGENT_MODE=prompt-server.
// The sidecar is the workflow driver, so the agent-runner skips the main
// agent loop entirely. It runs the prompt service loop in a goroutine so
// the sidecar can drive individual LLM calls via /ipc/prompts/, and exits
// when the sidecar signals completion by writing /ipc/done.
//
// The sidecar is the authoritative writer of /ipc/done: it writes /ipc/done
// only once its own work has fully completed (no in-flight prompt calls
// remain), so by the time /ipc/done appears the prompt service can drain
// safely without misclassifying responses.
//
// On exit the agent-runner copies the sidecar's /ipc/run-result.json
// payload to /ipc/output/result.json and emits the SYMPOZIUM_RESULT log
// line so the controller can surface the final payload on the AgentRun
// for `kubectl describe` to display.
func runPromptServer(ctx context.Context) {
	start := time.Now()

	_ = os.MkdirAll("/ipc/output", 0o755)

	provider := strings.ToLower(getEnv("MODEL_PROVIDER", "openai"))
	apiKey := firstNonEmpty(
		os.Getenv("API_KEY"),
		os.Getenv("OPENAI_API_KEY"),
		os.Getenv("ANTHROPIC_API_KEY"),
		os.Getenv("AZURE_OPENAI_API_KEY"),
		os.Getenv("PROVIDER_API_KEY"),
		os.Getenv("LLM_API_KEY"),
	)
	if fileKey, err := os.ReadFile("/secrets/api-key"); err == nil {
		apiKey = firstNonEmpty(apiKey, strings.TrimSpace(string(fileKey)))
	}
	baseURL := strings.TrimRight(getEnv("MODEL_BASE_URL", ""), "/")
	modelName := getEnv("MODEL_NAME", "gpt-4o-mini")
	systemPrompt := getEnv("SYSTEM_PROMPT", "You are a helpful AI assistant.")

	var providerHeaders map[string]string
	if headersJSON := getEnv("MODEL_PROVIDER_HEADERS", ""); headersJSON != "" {
		if err := json.Unmarshal([]byte(headersJSON), &providerHeaders); err != nil {
			log.Printf("WARNING: failed to parse MODEL_PROVIDER_HEADERS: %v", err)
		}
	}

	// In prompt-server mode the LLM has no conversational task — it just
	// answers action decisions issued by the sidecar. An empty system prompt
	// is fine; the sidecar passes the actual prompt on each request.
	log.Printf("prompt-server mode: provider=%s model=%s", provider, modelName)

	var promptWg sync.WaitGroup
	promptWg.Add(1)
	go func() {
		defer promptWg.Done()
		if err := runPromptServiceLoop(promptServiceDeps{
			Ctx:             ctx,
			ProviderName:    provider,
			APIKey:          apiKey,
			BaseURL:         baseURL,
			Model:           modelName,
			SystemPrompt:    systemPrompt,
			Task:            "",
			Tools:           nil,
			ProviderHeaders: providerHeaders,
		}); err != nil {
			log.Printf("prompt service exited with error: %v", err)
		}
	}()

	donePath := "/ipc/done"
	resultPath := "/ipc/run-result.json"
	outputPath := "/ipc/output/result.json"
	pollInterval := 500 * time.Millisecond
	poll := time.NewTicker(pollInterval)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("prompt-server: context cancelled, waiting for prompt service to drain")
			promptWg.Wait()
			return
		case <-poll.C:
			// Wait for the sidecar to signal completion by writing
			// /ipc/done. The sidecar writes /ipc/run-result.json first
			// and then /ipc/done, so by the time we observe /ipc/done
			// the run-result payload should already be on disk.
			if _, err := os.Stat(donePath); err != nil {
				continue
			}
			log.Printf("prompt-server: %s observed after %s, exiting", donePath, time.Since(start))

			result := loadAndEmitRunResult(resultPath, outputPath, start)
			// Do NOT write /ipc/done here — the sidecar owns /ipc/done.
			// Writing it here would race with in-flight prompt calls
			// the sidecar is still waiting on.

			if markerBytes, err := json.Marshal(result); err == nil {
				fmt.Fprintf(os.Stdout, "\n__SYMPOZIUM_RESULT__%s__SYMPOZIUM_END__\n", string(markerBytes))
			}
			log.Println("prompt-server finished")
			promptWg.Wait()
			return
		}
	}
}

// loadAndEmitRunResult reads the sidecar's run-result payload from
// resultPath (waiting briefly for the file to appear if the sidecar's
// write hasn't propagated through the shared volume yet), parses it into
// an agentResult (falling back to wrapping raw bytes or recording a
// minimal success result if parsing fails), and writes it to outputPath.
// Returns the resulting agentResult so the caller can emit the
// SYMPOZIUM_RESULT marker.
func loadAndEmitRunResult(resultPath, outputPath string, start time.Time) agentResult {
	var result agentResult
	data, err := os.ReadFile(resultPath)
	if err != nil {
		log.Printf("prompt-server: %s could not be read (%v); recording minimal success result", resultPath, err)
		result = agentResult{
			Status:   "success",
			Response: "sidecar finished but did not write /ipc/run-result.json",
		}
	} else if uerr := json.Unmarshal(data, &result); uerr != nil {
		log.Printf("prompt-server: %s was not a valid agentResult payload (%v); wrapping raw bytes", resultPath, uerr)
		result = agentResult{
			Status:   "success",
			Response: string(data),
		}
	}
	if result.Metrics.DurationMs == 0 {
		result.Metrics.DurationMs = time.Since(start).Milliseconds()
	}
	writeJSON(outputPath, result)
	return result
}
