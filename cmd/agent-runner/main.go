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
)

type agentResult struct {
	Status   string `json:"status"`
	Response string `json:"response,omitempty"`
	Error    string `json:"error,omitempty"`
	Metrics  struct {
		DurationMs   int64 `json:"durationMs"`
		InputTokens  int   `json:"inputTokens"`
		OutputTokens int   `json:"outputTokens"`
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
			"__K8SCLAW_MEMORY__\n<your updated MEMORY.md content>\n__K8SCLAW_MEMORY_END__\n" +
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

	log.Printf("provider=%s model=%s baseURL=%s task=%q", provider, modelName, baseURL, truncate(task, 80))

	_ = os.MkdirAll("/ipc/output", 0o755)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	start := time.Now()

	var (
		responseText string
		inputTokens  int
		outputTokens int
		err          error
	)

	switch provider {
	case "anthropic":
		responseText, inputTokens, outputTokens, err = callAnthropic(ctx, apiKey, baseURL, modelName, systemPrompt, task)
	default:
		// OpenAI, Azure OpenAI, Ollama, and any OpenAI-compatible provider
		responseText, inputTokens, outputTokens, err = callOpenAI(ctx, provider, apiKey, baseURL, modelName, systemPrompt, task)
	}

	elapsed := time.Since(start)

	var res agentResult
	res.Metrics.DurationMs = elapsed.Milliseconds()

	if err != nil {
		log.Printf("LLM call failed: %v", err)
		res.Status = "error"
		res.Error = err.Error()
	} else {
		log.Printf("LLM call succeeded (tokens: in=%d out=%d)", inputTokens, outputTokens)
		res.Status = "success"
		res.Response = responseText
		res.Metrics.InputTokens = inputTokens
		res.Metrics.OutputTokens = outputTokens
	}

	if res.Response != "" {
		writeJSON("/ipc/output/stream-0.json", streamChunk{
			Type:    "text",
			Content: res.Response,
			Index:   0,
		})
	}

	writeJSON("/ipc/output/result.json", res)

	// Print a structured marker to stdout so the controller can extract
	// the result from pod logs even after the IPC volume is gone.
	if markerBytes, err := json.Marshal(res); err == nil {
		fmt.Fprintf(os.Stdout, "\n__K8SCLAW_RESULT__%s__K8SCLAW_END__\n", string(markerBytes))
	}

	// Extract and emit memory update if the LLM produced one.
	if memoryEnabled && res.Response != "" {
		if memUpdate := extractMemoryUpdate(res.Response); memUpdate != "" {
			fmt.Fprintf(os.Stdout, "\n__K8SCLAW_MEMORY__%s__K8SCLAW_MEMORY_END__\n", memUpdate)
			log.Printf("emitted memory update (%d bytes)", len(memUpdate))
		}
	}

	if res.Status == "error" {
		log.Printf("agent-runner finished with error: %s", res.Error)
		os.Exit(1)
	}
	log.Println("agent-runner finished successfully")
}

// callAnthropic uses the official Anthropic Go SDK.
func callAnthropic(ctx context.Context, apiKey, baseURL, model, systemPrompt, task string) (string, int, int, error) {
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

	message, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(8192),
		System: []anthropic.TextBlockParam{
			{Text: systemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(task)),
		},
	})
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return "", 0, 0, fmt.Errorf("Anthropic API error (HTTP %d): %s", apiErr.StatusCode, truncate(apiErr.Error(), 500))
		}
		return "", 0, 0, fmt.Errorf("Anthropic API error: %w", err)
	}

	var text strings.Builder
	for _, block := range message.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(tb.Text)
		}
	}

	return text.String(), int(message.Usage.InputTokens), int(message.Usage.OutputTokens), nil
}

// callOpenAI uses the official OpenAI Go SDK for OpenAI, Azure OpenAI, Ollama, and other compatible providers.
func callOpenAI(ctx context.Context, provider, apiKey, baseURL, model, systemPrompt, task string) (string, int, int, error) {
	opts := []openaioption.RequestOption{
		openaioption.WithMaxRetries(5),
	}

	switch provider {
	case "azure-openai":
		if baseURL == "" {
			return "", 0, 0, fmt.Errorf("Azure OpenAI requires MODEL_BASE_URL to be set")
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

	completion, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(task),
		},
	})
	if err != nil {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			return "", 0, 0, fmt.Errorf("OpenAI API error (HTTP %d): %s", apiErr.StatusCode, truncate(apiErr.Error(), 500))
		}
		return "", 0, 0, fmt.Errorf("OpenAI API error: %w", err)
	}

	var text string
	if len(completion.Choices) > 0 {
		text = completion.Choices[0].Message.Content
	}

	return text, int(completion.Usage.PromptTokens), int(completion.Usage.CompletionTokens), nil
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
	writeJSON("/ipc/output/result.json", agentResult{
		Status: "error",
		Error:  msg,
	})
	os.Exit(1)
}

// extractMemoryUpdate looks for a memory update block in the LLM response.
// The agent is instructed to wrap its memory updates in:
//
//	__K8SCLAW_MEMORY__
//	<content>
//	__K8SCLAW_MEMORY_END__
func extractMemoryUpdate(response string) string {
	const startMarker = "__K8SCLAW_MEMORY__"
	const endMarker = "__K8SCLAW_MEMORY_END__"

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
