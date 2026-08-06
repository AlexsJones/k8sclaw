package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	anthropicoption "github.com/anthropics/anthropic-sdk-go/option"
)

// anthropicProvider adapts the Anthropic Messages API to LLMProvider.
type anthropicProvider struct {
	client      anthropic.Client
	model       string
	system      string
	initialTask string
	messages    []anthropic.MessageParam
	tools       []anthropic.ToolUnionParam
	// toolsBytes is the serialized tool-schema size, fixed after construction.
	toolsBytes int
}

func newAnthropicProvider(apiKey, baseURL, model, systemPrompt, task string, tools []ToolDef, headers map[string]string) *anthropicProvider {
	opts := []anthropicoption.RequestOption{
		anthropicoption.WithMaxRetries(effectiveMaxRetries("anthropic")),
	}
	if t := effectiveRequestTimeout("anthropic"); t > 0 {
		opts = append(opts, anthropicoption.WithRequestTimeout(t))
	}
	if apiKey != "" {
		opts = append(opts, anthropicoption.WithAPIKey(apiKey))
	}
	if baseURL != "" {
		opts = append(opts, anthropicoption.WithBaseURL(baseURL))
	}
	for k, v := range headers {
		opts = append(opts, anthropicoption.WithHeader(k, v))
	}

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

	return &anthropicProvider{
		client:      anthropic.NewClient(opts...),
		model:       model,
		system:      systemPrompt,
		initialTask: task,
		messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(task)),
		},
		tools:      anthropicTools,
		toolsBytes: jsonBytes(anthropicTools),
	}
}

func (p *anthropicProvider) Name() string  { return "anthropic" }
func (p *anthropicProvider) Model() string { return p.model }

func (p *anthropicProvider) Chat(ctx context.Context) (ChatResult, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: int64(8192),
		System: []anthropic.TextBlockParam{
			{Text: p.system},
		},
		Messages: p.messages,
	}
	if len(p.tools) > 0 {
		params.Tools = p.tools
	}

	if detailedLog.Enabled() {
		detailedLog.LogLLM("request", map[string]any{
			"provider":       "anthropic",
			"model":          p.model,
			"messages_count": len(p.messages),
			"tools_count":    len(p.tools),
			"system_bytes":   len(p.system),
			"tools_bytes":    p.toolsBytes,
			"messages_bytes": jsonBytes(p.messages),
		})
	}
	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return ChatResult{}, fmt.Errorf("Anthropic API error (HTTP %d): %s",
				apiErr.StatusCode, truncate(apiErr.Error(), 500))
		}
		return ChatResult{}, fmt.Errorf("Anthropic API error: %w", err)
	}

	var textContent strings.Builder
	var toolUseBlocks []anthropic.ToolUseBlock
	for _, block := range msg.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			textContent.WriteString(v.Text)
		case anthropic.ToolUseBlock:
			toolUseBlocks = append(toolUseBlocks, v)
		}
	}

	result := ChatResult{
		Text:        textContent.String(),
		InputTokens: int(msg.Usage.InputTokens),
		// Anthropic reports input_tokens as the *uncached* remainder, with the
		// cached portion counted separately — the opposite of OpenAI, where
		// cached tokens are a subset of prompt_tokens.
		//
		// This stays 0 until the request carries cache_control breakpoints:
		// Anthropic caching is opt-in per content block, and none are set here
		// yet. Wired up now so enabling caching later needs no plumbing change.
		CachedInputTokens: int(msg.Usage.CacheReadInputTokens),
		OutputTokens:      int(msg.Usage.OutputTokens),
		FinishReason:      string(msg.StopReason),
	}

	// Continue the loop only when the model explicitly stopped to call
	// tools. Anthropic guarantees StopReasonToolUse when tool_use blocks
	// should trigger follow-up tool execution.
	if msg.StopReason == anthropic.StopReasonToolUse && len(toolUseBlocks) > 0 {
		for _, tu := range toolUseBlocks {
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:    tu.ID,
				Name:  tu.Name,
				Input: string(tu.Input),
			})
		}
		// Append the assistant message (text + tool_use) to history.
		var assistantBlocks []anthropic.ContentBlockParamUnion
		for _, block := range msg.Content {
			switch v := block.AsAny().(type) {
			case anthropic.TextBlock:
				assistantBlocks = append(assistantBlocks, anthropic.NewTextBlock(v.Text))
			case anthropic.ToolUseBlock:
				assistantBlocks = append(assistantBlocks,
					anthropic.NewToolUseBlock(v.ID, json.RawMessage(v.Input), v.Name))
			}
		}
		p.messages = append(p.messages, anthropic.NewAssistantMessage(assistantBlocks...))
	}

	return result, nil
}

func (p *anthropicProvider) AddToolResults(results []ToolResult) {
	var blocks []anthropic.ContentBlockParamUnion
	for _, r := range results {
		blocks = append(blocks, anthropic.NewToolResultBlock(r.CallID, r.Content, r.IsError))
	}
	p.messages = append(p.messages, anthropic.NewUserMessage(blocks...))
}

// ReplaceToolResults rewrites tool_result blocks in place, keyed by
// tool_use_id. Anthropic carries tool results as content blocks inside user
// messages, so this walks blocks rather than messages. The is_error flag is
// carried across so an elided failure still reads as a failure.
//
// The block is rebuilt text-only: NewToolResultBlock takes a string, and every
// result this runner produces today is text. If tool_result content ever grows
// image blocks, this drops them silently — the replacement would then have to
// carry a content union rather than a string.
func (p *anthropicProvider) ReplaceToolResults(replacements map[string]string) {
	if len(replacements) == 0 {
		return
	}
	for mi := range p.messages {
		for bi := range p.messages[mi].Content {
			tr := p.messages[mi].Content[bi].OfToolResult
			if tr == nil {
				continue
			}
			stub, ok := replacements[tr.ToolUseID]
			if !ok {
				continue
			}
			p.messages[mi].Content[bi] = anthropic.NewToolResultBlock(
				tr.ToolUseID, stub, tr.IsError.Or(false))
		}
	}
}

// ResetContext rebuilds the message slice to the seed state so
// the next Chat or Prompt call behaves as if the conversation just began.
func (p *anthropicProvider) ResetContext() {
	p.messages = []anthropic.MessageParam{
		anthropic.NewUserMessage(anthropic.NewTextBlock(p.initialTask)),
	}
}

// Prompt issues a single Anthropic Messages call on behalf of a
// sidecar. With useContext false the message slice is temporarily reset for
// the call (and restored via defer) so the answer is stateless. With
// useContext true the prompt is appended and the assistant reply recorded
// so context grows across Prompt calls within the loop. Tool-use is
// suppressed via tool_choice=none — the model is expected to answer in text
// only; structured output (when Schema is set) is parsed by the caller from
// the assistant's text block.
func (p *anthropicProvider) Prompt(ctx context.Context, prompt string, useContext bool, schema json.RawMessage) (string, []byte, int, int, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", nil, 0, 0, fmt.Errorf("prompt is empty")
	}

	var saved []anthropic.MessageParam
	rollbackUserTurn := false
	if !useContext {
		saved = p.messages
		p.messages = []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		}
		defer func() { p.messages = saved }()
	} else {
		// Append the user turn only after the call succeeds; on API failure
		// we pop it (see below) so a failed Prompt does not leave an orphan
		// user turn in history. Anthropic rejects back-to-back user turns,
		// so a single transient error would otherwise poison the run.
		rollbackUserTurn = true
		p.messages = append(p.messages, anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)))
		defer func() {
			if rollbackUserTurn && len(p.messages) > 0 {
				p.messages = p.messages[:len(p.messages)-1]
			}
		}()
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.model),
		MaxTokens: int64(8192),
		System: []anthropic.TextBlockParam{
			{Text: p.system},
		},
		Messages: p.messages,
		// Suppress tool use so a sidecar-driven prompt returns text only.
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfNone: &anthropic.ToolChoiceNoneParam{},
		},
	}

	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return "", nil, 0, 0, fmt.Errorf("Anthropic API error (HTTP %d): %s",
				apiErr.StatusCode, truncate(apiErr.Error(), 500))
		}
		return "", nil, 0, 0, fmt.Errorf("Anthropic API error: %w", err)
	}

	inTok := int(msg.Usage.InputTokens)
	outTok := int(msg.Usage.OutputTokens)
	var textContent strings.Builder
	for _, block := range msg.Content {
		if tb, ok := block.AsAny().(anthropic.TextBlock); ok {
			textContent.WriteString(tb.Text)
		}
	}
	text := textContent.String()

	if useContext {
		p.messages = append(p.messages, anthropic.NewAssistantMessage(anthropic.NewTextBlock(text)))
		// Success: cancel the deferred rollback so the user turn stays in
		// history.
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
