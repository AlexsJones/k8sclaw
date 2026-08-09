package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	"github.com/openai/openai-go/v3"
	openaioption "github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/shared"
)

// openaiProvider adapts the OpenAI Go SDK to the LLMProvider interface.
// It also handles all OpenAI-compatible backends: LM Studio, Ollama, vLLM,
// llamacpp, Azure OpenAI, and any OpenAI-schema provider.
type openaiProvider struct {
	client      openai.Client
	provider    string // provider identifier for telemetry ("openai", "lm-studio", …)
	model       string
	messages    []openai.ChatCompletionMessageParamUnion
	system      string // system prompt kept separately so ResetContext can rebuild
	initialTask string // initial user turn (kept for ResetContext to restore)
	tools       []openai.ChatCompletionToolUnionParam
	// toolsBytes is the serialized size of the tool schemas, computed once
	// because the tool set never changes after construction. It is the static
	// half of the request that prefix caching is meant to cover.
	toolsBytes int
}

// newOpenAIProvider constructs an openaiProvider with the given config.
// The provider string determines SDK defaults and telemetry tags
// ("openai" | "lm-studio" | "ollama" | "azure-openai" | …).
func newOpenAIProvider(provider, apiKey, baseURL, model, systemPrompt, task string, tools []ToolDef, headers map[string]string) (*openaiProvider, error) {
	retries := effectiveMaxRetries(provider)
	reqTimeout := effectiveRequestTimeout(provider)

	opts := []openaioption.RequestOption{
		openaioption.WithMaxRetries(retries),
	}
	if reqTimeout > 0 {
		opts = append(opts, openaioption.WithRequestTimeout(reqTimeout))
	}

	switch provider {
	case "azure-openai":
		if baseURL == "" {
			return nil, fmt.Errorf("Azure OpenAI requires MODEL_BASE_URL to be set")
		}
		if model == "" {
			return nil, fmt.Errorf("Azure OpenAI requires MODEL_NAME to be set")
		}
		apiVersion := getEnv("AZURE_OPENAI_API_VERSION", "2024-06-01")
		// azure.WithEndpoint path transform not applied in openai-go v3.22.0 — construct manually
		deploymentURL := strings.TrimRight(baseURL, "/") + "/openai/deployments/" + model
		opts = append(opts,
			openaioption.WithBaseURL(deploymentURL),
			openaioption.WithAPIKey(apiKey),
			openaioption.WithHeader("Api-Key", apiKey),
		)
		// Add API version as query parameter for all requests
		opts = append(opts, openaioption.WithQueryAdd("api-version", apiVersion))
	default:
		if apiKey != "" {
			opts = append(opts, openaioption.WithAPIKey(apiKey))
		}
		if baseURL != "" {
			opts = append(opts, openaioption.WithBaseURL(baseURL))
		} else if provider == "ollama" {
			opts = append(opts, openaioption.WithBaseURL("http://ollama.default.svc:11434/v1"))
		} else if provider == "lm-studio" {
			opts = append(opts, openaioption.WithBaseURL("http://localhost:1234/v1"))
		} else if provider == "llama-server" {
			opts = append(opts, openaioption.WithBaseURL("http://localhost:8080/v1"))
		} else if provider == "unsloth" {
			opts = append(opts, openaioption.WithBaseURL("http://localhost:8080/v1"))
		}
	}

	for k, v := range headers {
		opts = append(opts, openaioption.WithHeader(k, v))
	}

	var oaiTools []openai.ChatCompletionToolUnionParam
	for _, t := range tools {
		oaiTools = append(oaiTools, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
			Name:        t.Name,
			Description: openai.String(t.Description),
			Parameters:  shared.FunctionParameters(t.Parameters),
		}))
	}

	p := &openaiProvider{
		client:      openai.NewClient(opts...),
		provider:    provider,
		model:       model,
		system:      systemPrompt,
		initialTask: task,
		messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(task),
		},
		tools:      oaiTools,
		toolsBytes: jsonBytes(oaiTools),
	}
	return p, nil
}

func (p *openaiProvider) Name() string  { return p.provider }
func (p *openaiProvider) Model() string { return p.model }

func (p *openaiProvider) Chat(ctx context.Context) (ChatResult, error) {
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(p.model),
		Messages: p.messages,
	}
	if len(p.tools) > 0 {
		params.Tools = p.tools
	}

	if detailedLog.Enabled() {
		detailedLog.LogLLM("request", map[string]any{
			"provider":       p.provider,
			"model":          p.model,
			"messages_count": len(p.messages),
			"tools_count":    len(p.tools),
			"tools_bytes":    p.toolsBytes,
			"messages_bytes": jsonBytes(p.messages),
		})
	}
	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			return ChatResult{}, fmt.Errorf("OpenAI API error (HTTP %d): %s",
				apiErr.StatusCode, truncate(apiErr.Error(), 500))
		}
		return ChatResult{}, fmt.Errorf("OpenAI API error: %w", err)
	}

	if len(completion.Choices) == 0 {
		return ChatResult{
				InputTokens:  int(completion.Usage.PromptTokens),
				OutputTokens: int(completion.Usage.CompletionTokens),
			},
			fmt.Errorf("no choices in completion response")
	}

	choice := completion.Choices[0]
	text := choice.Message.Content
	var extraToolCalls []ToolCall
	// Reasoning models (qwen3.x, deepseek-r1, …) served by LM Studio and
	// Ollama sometimes emit their final answer — and even more tool calls —
	// inside a non-standard `reasoning_content` field that the OpenAI SDK
	// doesn't surface. When Content is empty, pull reasoning_content out of
	// the raw JSON and:
	//   - parse any qwen-native <tool_call>…</tool_call> blocks back into
	//     structured ToolCalls so the loop can continue dispatching them
	//   - sanitize the remaining text for user display
	if text == "" {
		if rc := extractReasoningContentRaw(choice.Message.RawJSON()); rc != "" {
			extraToolCalls = parseQwenToolCalls(rc)
			text = sanitizeReasoningArtifacts(rc)
			log.Printf("openai.Chat: Content empty; reasoning_content=%dchars recovered_tool_calls=%d clean_text=%dchars",
				len(rc), len(extraToolCalls), len(text))
		}
	}

	result := ChatResult{
		Text:        text,
		InputTokens: int(completion.Usage.PromptTokens),
		// OpenAI (and OpenRouter-proxied models) count cached tokens inside
		// prompt_tokens, so this is a breakdown of InputTokens, not an addition.
		CachedInputTokens: int(completion.Usage.PromptTokensDetails.CachedTokens),
		OutputTokens:      int(completion.Usage.CompletionTokens),
		FinishReason:      choice.FinishReason,
	}

	// Extract tool calls whenever present, regardless of finish_reason.
	// OpenRouter-proxied models (Qwen, Gemini, Mistral) often return
	// finish_reason="stop" with valid tool calls in the same message.
	if len(choice.Message.ToolCalls) > 0 {
		for _, tc := range choice.Message.ToolCalls {
			fc := tc.AsFunction()
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:    fc.ID,
				Name:  fc.Function.Name,
				Input: fc.Function.Arguments,
			})
		}
		// Record the assistant message with tool_calls so the next Chat
		// call includes it in history.
		p.messages = append(p.messages, choice.Message.ToParam())
	} else if len(extraToolCalls) > 0 {
		// Qwen-native tool calls recovered from reasoning_content (LM Studio
		// failed to parse them into structured tool_calls). Surface them so
		// the loop keeps dispatching and the task can actually complete.
		result.ToolCalls = extraToolCalls
		// Record a synthetic assistant message with these tool_calls so the
		// subsequent tool-result message has something to link against.
		synth := openai.ChatCompletionAssistantMessageParam{}
		if text != "" {
			synth.Content.OfString = openai.String(text)
		}
		for _, call := range extraToolCalls {
			synth.ToolCalls = append(synth.ToolCalls,
				openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID: call.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      call.Name,
							Arguments: call.Input,
						},
					},
				})
		}
		p.messages = append(p.messages,
			openai.ChatCompletionMessageParamUnion{OfAssistant: &synth})
	}

	return result, nil
}

func (p *openaiProvider) AddToolResults(results []ToolResult) {
	for _, r := range results {
		p.messages = append(p.messages, openai.ToolMessage(r.Content, r.CallID))
	}
}

// ReplaceToolResults swaps the content of already-recorded tool messages,
// keyed by tool_call_id. The message itself is rebuilt through the same
// constructor so the tool_call_id linkage the API requires is preserved —
// dropping the message outright would orphan the assistant's tool_calls entry
// and the request would be rejected.
func (p *openaiProvider) ReplaceToolResults(replacements map[string]string) {
	if len(replacements) == 0 {
		return
	}
	for i := range p.messages {
		tm := p.messages[i].OfTool
		if tm == nil {
			continue
		}
		stub, ok := replacements[tm.ToolCallID]
		if !ok {
			continue
		}
		p.messages[i] = openai.ToolMessage(stub, tm.ToolCallID)
	}
}

// ResetContext rebuilds the message slice to the seed state so
// the next Chat or Prompt call behaves as if the conversation just began.
// Used by the sidecar-initiated clearContext IPC between independent units
// of work so token accumulation does not leak across boundaries.
func (p *openaiProvider) ResetContext() {
	p.messages = []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(p.system),
		openai.UserMessage(p.initialTask),
	}
}

// Prompt issues a single LLM call on behalf of a sidecar. When
// useContext is false the call is stateless: messages are temporarily reset
// to [system, prompt] for the call and restored on exit. Otherwise the
// prompt is appended and the assistant reply recorded, so the conversation
// history grows across Prompt calls within the loop. Schema is forwarded
// via OpenAI's response_format json_schema when provided. The first return
// is the raw text; the second is the parsed JSON payload when Schema was
// set and the model emitted JSON.
func (p *openaiProvider) Prompt(ctx context.Context, prompt string, useContext bool, schema json.RawMessage) (string, []byte, int, int, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", nil, 0, 0, fmt.Errorf("prompt is empty")
	}

	var saved []openai.ChatCompletionMessageParamUnion
	rollbackUserTurn := false
	if !useContext {
		saved = p.messages
		p.messages = []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(p.system),
			openai.UserMessage(prompt),
		}
		defer func() { p.messages = saved }()
	} else {
		// Append the user turn only after the call succeeds; on API failure
		// we pop it (see below) so a failed Prompt does not leave an orphan
		// user turn in history that would be re-sent on every subsequent
		// Prompt. Anthropic in particular rejects back-to-back user turns,
		// so a single transient error would otherwise poison the run.
		rollbackUserTurn = true
		p.messages = append(p.messages, openai.UserMessage(prompt))
		defer func() {
			if rollbackUserTurn && len(p.messages) > 0 {
				p.messages = p.messages[:len(p.messages)-1]
			}
		}()
	}

	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(p.model),
		Messages: p.messages,
	}
	if len(schema) > 0 {
		var format struct {
			Type   string          `json:"type"`
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
			Strict *bool           `json:"strict,omitempty"`
		}
		if err := json.Unmarshal(schema, &format); err == nil && format.Type == "json_schema" {
			params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
				OfJSONSchema: &openai.ResponseFormatJSONSchemaParam{
					JSONSchema: openai.ResponseFormatJSONSchemaJSONSchemaParam{
						Name:   format.Name,
						Schema: format.Schema,
						Strict: openai.Bool(format.Strict == nil || *format.Strict),
					},
				},
			}
		}
	}

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		var apiErr *openai.Error
		if errors.As(err, &apiErr) {
			return "", nil, 0, 0, fmt.Errorf("OpenAI API error (HTTP %d): %s",
				apiErr.StatusCode, truncate(apiErr.Error(), 500))
		}
		return "", nil, 0, 0, fmt.Errorf("OpenAI API error: %w", err)
	}

	inTok := int(completion.Usage.PromptTokens)
	outTok := int(completion.Usage.CompletionTokens)
	if len(completion.Choices) == 0 {
		return "", nil, inTok, outTok, fmt.Errorf("no choices in completion response")
	}
	choice := completion.Choices[0]
	text := choice.Message.Content

	if useContext {
		// Record assistant reply so subsequent Prompt calls retain context.
		p.messages = append(p.messages, openai.ChatCompletionMessageParamUnion{
			OfAssistant: &openai.ChatCompletionAssistantMessageParam{
				Content: openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(text),
				},
			},
		})
		// Success: cancel the deferred rollback so the user turn stays in
		// history. (The deferred capture still runs to completion, but the
		// guard short-circuits the pop.)
		rollbackUserTurn = false
	}

	var parsed []byte
	if len(schema) > 0 {
		trimmed := strings.TrimSpace(text)
		if json.Valid([]byte(trimmed)) {
			parsed = []byte(trimmed)
		} else {
			return text, nil, inTok, outTok, fmt.Errorf(
				"schema requested but model output was not valid JSON: %.200s", text)
		}
	}
	return text, parsed, inTok, outTok, nil
}

// extractReasoningContent pulls the sanitized `reasoning_content` out of a
// raw assistant-message JSON blob. Reasoning models (qwen3.x, deepseek-r1,
// …) served over OpenAI-compatible endpoints put their final answer there
// when standard `content` is empty.
func extractReasoningContent(raw string) string {
	return sanitizeReasoningArtifacts(extractReasoningContentRaw(raw))
}

// extractReasoningContentRaw returns reasoning_content without sanitizing so
// callers can parse qwen-native <tool_call> blocks out of it first.
func extractReasoningContentRaw(raw string) string {
	if raw == "" {
		return ""
	}
	var probe struct {
		ReasoningContent string `json:"reasoning_content"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return ""
	}
	return probe.ReasoningContent
}

// parseQwenToolCalls finds qwen-native tool-call blocks in a reasoning string
// and converts them to structured ToolCall values. The qwen format is:
//
//	<tool_call>
//	<function=NAME>
//	<parameter=ARG>VALUE</parameter>
//	…
//	</function>
//	</tool_call>
//
// LM Studio occasionally fails to parse this into the standard tool_calls
// field; recovering them here lets the agent loop continue dispatching.
var qwenToolCallBlock = regexp.MustCompile(`(?is)<tool_call>(.*?)</tool_call>`)
var qwenFunctionTag = regexp.MustCompile(`(?is)<function=([^>\s]+)[^>]*>(.*?)</function>`)
var qwenParameterTag = regexp.MustCompile(`(?is)<parameter=([^>\s]+)[^>]*>(.*?)</parameter>`)

func parseQwenToolCalls(s string) []ToolCall {
	var calls []ToolCall
	for i, block := range qwenToolCallBlock.FindAllStringSubmatch(s, -1) {
		fn := qwenFunctionTag.FindStringSubmatch(block[1])
		if len(fn) < 3 {
			continue
		}
		name := strings.TrimSpace(fn[1])
		args := map[string]any{}
		for _, p := range qwenParameterTag.FindAllStringSubmatch(fn[2], -1) {
			key := strings.TrimSpace(p[1])
			val := strings.TrimSpace(p[2])
			args[key] = coerceScalar(val)
		}
		jsonArgs, err := json.Marshal(args)
		if err != nil {
			continue
		}
		calls = append(calls, ToolCall{
			ID:    fmt.Sprintf("qwen-recovered-%d", i),
			Name:  name,
			Input: string(jsonArgs),
		})
	}
	return calls
}

// coerceScalar converts a string parameter value into the most natural JSON
// scalar type (int, float, bool, or string) so tool-call arguments roundtrip
// through strict schemas (e.g. timeout: integer).
func coerceScalar(s string) any {
	if s == "true" {
		return true
	}
	if s == "false" {
		return false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}

// sanitizeReasoningArtifacts strips model-specific control blocks that leak
// into reasoning_content: <tool_call>…</tool_call>, <think>…</think>, and
// similar wrappers that should never be shown to users. Unclosed blocks are
// truncated from the opening tag. Whitespace is collapsed.
//
// Go's regexp (RE2) doesn't support backreferences, so we match each tag
// independently rather than with a single alternation + \1.
var reasoningArtifactTags = []string{"tool_call", "think", "tool_use"}
var multipleBlankLines = regexp.MustCompile(`\n{3,}`)

func sanitizeReasoningArtifacts(s string) string {
	for _, tag := range reasoningArtifactTags {
		// Closed block: <tag>…</tag>
		closed := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>.*?</` + tag + `>`)
		s = closed.ReplaceAllString(s, "")
		// Unclosed: <tag> with no matching close — drop to end of string.
		open := regexp.MustCompile(`(?is)<` + tag + `\b[^>]*>.*`)
		s = open.ReplaceAllString(s, "")
	}
	s = multipleBlankLines.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
