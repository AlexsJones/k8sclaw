package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/azure"
	openaioption "github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// maxToolIterations is the maximum number of tool-call round-trips before
// the agent stops and returns whatever text it has.
const maxToolIterations = 25

type agentResult struct {
	Status   string `json:"status"`
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

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)
	log.Println("agent-runner starting")

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
		fatal("TASK env var is empty and no /ipc/input/task.json found")
	}

	systemPrompt := getEnv("SYSTEM_PROMPT", "You are a helpful AI assistant.")
	provider := strings.ToLower(getEnv("MODEL_PROVIDER", "openai"))
	modelName := getEnv("MODEL_NAME", "gpt-4o-mini")
	baseURL := strings.TrimRight(getEnv("MODEL_BASE_URL", ""), "/")
	memoryEnabled := getEnv("MEMORY_ENABLED", "") == "true"
	toolsEnabled := getEnv("TOOLS_ENABLED", "") == "true"

	// Load skill files and build enhanced system prompt.
	skills := loadSkills(defaultSkillsDir)
	systemPrompt = buildSystemPrompt(systemPrompt, skills, toolsEnabled)

	// If this run was triggered from a channel, inject context so the
	// agent knows how to reply through the originating channel.
	sourceChannel := getEnv("SOURCE_CHANNEL", "")
	sourceChatID := getEnv("SOURCE_CHAT_ID", "")
	if sourceChannel != "" {
		channelCtx := fmt.Sprintf(
			"\n\n## Channel Context\n\n"+
				"This task was received through the **%s** channel (chat ID: %s). "+
				"You can reply through this channel using the `send_channel_message` tool "+
				"with channel=%q and chatId=%q. Use it to deliver results, ask follow-up "+
				"questions, or send notifications to the user.",
			sourceChannel, sourceChatID, sourceChannel, sourceChatID,
		)
		systemPrompt += channelCtx
		log.Printf("channel context injected: channel=%s chatId=%s", sourceChannel, sourceChatID)
	}

	// Resolve tool definitions.
	var tools []ToolDef
	if toolsEnabled {
		tools = defaultTools()
		log.Printf("tools enabled: %d tool(s) registered", len(tools))
	}

	// Read existing memory if available.
	var memoryContent string
	if memoryEnabled {
		if b, err := os.ReadFile("/memory/MEMORY.md"); err == nil {
			memoryContent = strings.TrimSpace(string(b))
			log.Printf("loaded memory (%d bytes)", len(memoryContent))
		}
	}

	// Prepend memory context to the task if present.
	if memoryContent != "" && memoryContent != "# Agent Memory\n\nNo memories recorded yet." {
		task = fmt.Sprintf("## Your Memory\nThe following is your persistent memory from prior interactions:\n\n%s\n\n## Current Task\n%s", memoryContent, task)
	}

	// If memory is enabled, add memory instructions to system prompt.
	if memoryEnabled {
		memoryInstruction := "\n\nYou have persistent memory. After completing your task, " +
			"output a memory update block wrapped in markers like this:\n" +
			"__KUBECLAW_MEMORY__\n<your updated MEMORY.md content>\n__KUBECLAW_MEMORY_END__\n" +
			"Include key facts, preferences, and context from this and past interactions. " +
			"Keep it concise (under 256KB). Use markdown format."
		systemPrompt += memoryInstruction
	}

	apiKey := firstNonEmpty(
		os.Getenv("API_KEY"),
		os.Getenv("OPENAI_API_KEY"),
		os.Getenv("ANTHROPIC_API_KEY"),
		os.Getenv("AZURE_OPENAI_API_KEY"),
	)

	log.Printf("provider=%s model=%s baseURL=%s tools=%v task=%q",
		provider, modelName, baseURL, toolsEnabled, truncate(task, 80))

	_ = os.MkdirAll("/ipc/output", 0o755)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	start := time.Now()

	var (
		responseText string
		inputTokens  int
		outputTokens int
		toolCalls    int
		err          error
	)

	switch provider {
	case "anthropic":
		responseText, inputTokens, outputTokens, toolCalls, err = callAnthropic(ctx, apiKey, baseURL, modelName, systemPrompt, task, tools)
	default:
		// OpenAI, Azure OpenAI, Ollama, and any OpenAI-compatible provider
		responseText, inputTokens, outputTokens, toolCalls, err = callOpenAI(ctx, provider, apiKey, baseURL, modelName, systemPrompt, task, tools)
	}

	elapsed := time.Since(start)

	var res agentResult
	res.Metrics.DurationMs = elapsed.Milliseconds()
	res.Metrics.ToolCalls = toolCalls

	debugMode := getEnv("DEBUG", "") == "true"

	if err != nil {
		log.Printf("LLM call failed: %v", err)
		res.Status = "error"
		res.Error = err.Error()
	} else {
		log.Printf("LLM call succeeded (tokens: in=%d out=%d, tool_calls=%d)", inputTokens, outputTokens, toolCalls)
		res.Status = "success"
		res.Response = responseText
		res.Metrics.InputTokens = inputTokens
		res.Metrics.OutputTokens = outputTokens
	}

	// Extract and emit memory update before stripping markers from the response.
	if memoryEnabled && res.Response != "" {
		if memUpdate := extractMemoryUpdate(res.Response); memUpdate != "" {
			fmt.Fprintf(os.Stdout, "\n__KUBECLAW_MEMORY__%s__KUBECLAW_MEMORY_END__\n", memUpdate)
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
		fmt.Fprintf(os.Stdout, "\n__KUBECLAW_RESULT__%s__KUBECLAW_END__\n", string(markerBytes))
	}

	if res.Status == "error" {
		log.Printf("agent-runner finished with error: %s", res.Error)
		os.Exit(1)
	}
	log.Println("agent-runner finished successfully")
}

// callAnthropic uses the official Anthropic Go SDK with optional tool calling.
// When tools is non-empty, the function enters a loop: call the LLM, execute
// any tool_use blocks, feed results back, and repeat until the model produces
// a final text response or the iteration limit is reached.
func callAnthropic(ctx context.Context, apiKey, baseURL, model, systemPrompt, task string, tools []ToolDef) (string, int, int, int, error) {
	opts := []anthropicoption.RequestOption{
		anthropicoption.WithMaxRetries(5),
	}
	if apiKey != "" {
		opts = append(opts, anthropicoption.WithAPIKey(apiKey))
	}
	if baseURL != "" {
		opts = append(opts, anthropicoption.WithBaseURL(baseURL))
	}

	client := anthropic.NewClient(opts...)

	// Build Anthropic tool definitions.
	var anthropicTools []anthropic.ToolUnionParam
	for _, t := range tools {
		schema := anthropic.ToolInputSchemaParam{
			Properties: t.Parameters["properties"],
		}
		if req, ok := t.Parameters["required"].([]string); ok {
			schema.Required = req
		}
		tool := anthropic.ToolUnionParamOfTool(schema, t.Name)
		tool.OfTool.Description = anthropic.String(t.Description)
		anthropicTools = append(anthropicTools, tool)
	}

	messages := []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(task)),
	}

	totalInputTokens := 0
	totalOutputTokens := 0
	totalToolCalls := 0

	for i := 0; i < maxToolIterations; i++ {
		params := anthropic.MessageNewParams{
			Model:     anthropic.Model(model),
			MaxTokens: int64(8192),
			System: []anthropic.TextBlockParam{
				{Text: systemPrompt},
			},
			Messages: messages,
		}
		if len(anthropicTools) > 0 {
			params.Tools = anthropicTools
		}

		message, err := client.Messages.New(ctx, params)
		if err != nil {
			var apiErr *anthropic.Error
			if errors.As(err, &apiErr) {
				return "", totalInputTokens, totalOutputTokens, totalToolCalls,
					fmt.Errorf("Anthropic API error (HTTP %d): %s", apiErr.StatusCode, truncate(apiErr.Error(), 500))
			}
			return "", totalInputTokens, totalOutputTokens, totalToolCalls,
				fmt.Errorf("Anthropic API error: %w", err)
		}

		totalInputTokens += int(message.Usage.InputTokens)
		totalOutputTokens += int(message.Usage.OutputTokens)

		// Separate text blocks and tool-use blocks.
		var textContent strings.Builder
		var toolUseBlocks []anthropic.ToolUseBlock
		for _, block := range message.Content {
			switch v := block.AsAny().(type) {
			case anthropic.TextBlock:
				textContent.WriteString(v.Text)
			case anthropic.ToolUseBlock:
				toolUseBlocks = append(toolUseBlocks, v)
			}
		}

		// If no tool calls, return the text.
		if message.StopReason != anthropic.StopReasonToolUse || len(toolUseBlocks) == 0 {
			return textContent.String(), totalInputTokens, totalOutputTokens, totalToolCalls, nil
		}

		// Build the assistant message with all content blocks (text + tool_use).
		var assistantBlocks []anthropic.ContentBlockParamUnion
		for _, block := range message.Content {
			switch v := block.AsAny().(type) {
			case anthropic.TextBlock:
				assistantBlocks = append(assistantBlocks, anthropic.NewTextBlock(v.Text))
			case anthropic.ToolUseBlock:
				assistantBlocks = append(assistantBlocks,
					anthropic.NewToolUseBlock(v.ID, json.RawMessage(v.Input), v.Name))
			}
		}
		messages = append(messages, anthropic.NewAssistantMessage(assistantBlocks...))

		// Execute each tool call and build tool_result blocks.
		var resultBlocks []anthropic.ContentBlockParamUnion
		for _, tu := range toolUseBlocks {
			totalToolCalls++
			log.Printf("tool_use [%d]: %s id=%s", totalToolCalls, tu.Name, tu.ID)

			result := executeToolCall(tu.Name, string(tu.Input))
			isErr := strings.HasPrefix(result, "Error:")
			resultBlocks = append(resultBlocks, anthropic.NewToolResultBlock(tu.ID, result, isErr))
		}
		messages = append(messages, anthropic.NewUserMessage(resultBlocks...))
	}

	return "", totalInputTokens, totalOutputTokens, totalToolCalls,
		fmt.Errorf("exceeded maximum tool-call iterations (%d)", maxToolIterations)
}

// callOpenAI uses the official OpenAI Go SDK with optional tool calling.
// When tools is non-empty, the function enters a loop: call the LLM, execute
// any tool_calls, feed results back, and repeat until the model produces a
// final text response or the iteration limit is reached.
func callOpenAI(ctx context.Context, provider, apiKey, baseURL, model, systemPrompt, task string, tools []ToolDef) (string, int, int, int, error) {
	opts := []openaioption.RequestOption{
		openaioption.WithMaxRetries(5),
	}

	switch provider {
	case "azure-openai":
		if baseURL == "" {
			return "", 0, 0, 0, fmt.Errorf("Azure OpenAI requires MODEL_BASE_URL to be set")
		}
		apiVersion := getEnv("AZURE_OPENAI_API_VERSION", "2024-06-01")
		opts = append(opts,
			azure.WithEndpoint(baseURL, apiVersion),
			azure.WithAPIKey(apiKey),
		)
	default:
		if apiKey != "" {
			opts = append(opts, openaioption.WithAPIKey(apiKey))
		}
		if baseURL != "" {
			opts = append(opts, openaioption.WithBaseURL(baseURL))
		} else if provider == "ollama" {
			opts = append(opts, openaioption.WithBaseURL("http://ollama.default.svc:11434/v1"))
		}
	}

	client := openai.NewClient(opts...)

	// Build OpenAI tool definitions.
	var oaiTools []openai.ChatCompletionToolUnionParam
	for _, t := range tools {
		oaiTools = append(oaiTools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  shared.FunctionParameters(t.Parameters),
		}))
	}

	messages := []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(systemPrompt),
		openai.UserMessage(task),
	}

	totalInputTokens := 0
	totalOutputTokens := 0
	totalToolCalls := 0

	for i := 0; i < maxToolIterations; i++ {
		params := openai.ChatCompletionNewParams{
			Model:    openai.ChatModel(model),
			Messages: messages,
		}
		if len(oaiTools) > 0 {
			params.Tools = oaiTools
		}

		completion, err := client.Chat.Completions.New(ctx, params)
		if err != nil {
			var apiErr *openai.Error
			if errors.As(err, &apiErr) {
				return "", totalInputTokens, totalOutputTokens, totalToolCalls,
					fmt.Errorf("OpenAI API error (HTTP %d): %s", apiErr.StatusCode, truncate(apiErr.Error(), 500))
			}
			return "", totalInputTokens, totalOutputTokens, totalToolCalls,
				fmt.Errorf("OpenAI API error: %w", err)
		}

		totalInputTokens += int(completion.Usage.PromptTokens)
		totalOutputTokens += int(completion.Usage.CompletionTokens)

		if len(completion.Choices) == 0 {
			return "", totalInputTokens, totalOutputTokens, totalToolCalls,
				fmt.Errorf("no choices in completion response")
		}
		choice := completion.Choices[0]

		// If model made tool calls, execute them and loop.
		if choice.FinishReason == "tool_calls" && len(choice.Message.ToolCalls) > 0 {
			// Add the assistant message (with tool calls) to history.
			messages = append(messages, choice.Message.ToParam())

			// Execute each tool call and add results.
			for _, tc := range choice.Message.ToolCalls {
				fc := tc.AsFunction()
				totalToolCalls++
				log.Printf("tool_call [%d]: %s id=%s", totalToolCalls, fc.Function.Name, fc.ID)

				result := executeToolCall(fc.Function.Name, fc.Function.Arguments)
				messages = append(messages, openai.ToolMessage(result, fc.ID))
			}
			continue
		}

		// No tool calls — return the text response.
		return choice.Message.Content, totalInputTokens, totalOutputTokens, totalToolCalls, nil
	}

	return "", totalInputTokens, totalOutputTokens, totalToolCalls,
		fmt.Errorf("exceeded maximum tool-call iterations (%d)", maxToolIterations)
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
//	__KUBECLAW_MEMORY__
//	<content>
//	__KUBECLAW_MEMORY_END__
func extractMemoryUpdate(response string) string {
	const startMarker = "__KUBECLAW_MEMORY__"
	const endMarker = "__KUBECLAW_MEMORY_END__"

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

// stripMemoryMarkers removes all __KUBECLAW_MEMORY__...END__ blocks from the
// response text so they don't appear in the TUI feed or channel messages.
func stripMemoryMarkers(response string) string {
	const startMarker = "__KUBECLAW_MEMORY__"
	const endMarker = "__KUBECLAW_MEMORY_END__"

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
