package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
)

// bedrockClientAPI is the subset of the Bedrock Runtime client used by the
// Bedrock provider. It exists so tests can inject a mock without hitting AWS.
type bedrockClientAPI interface {
	Converse(ctx context.Context, params *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

// newBedrockClient creates a real Bedrock Runtime client from the default AWS config.
// AWS SDK v2 auto-discovers credentials from AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY,
// AWS_SESSION_TOKEN, and AWS_REGION environment variables.
func newBedrockClient(ctx context.Context) (bedrockClientAPI, error) {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return bedrockruntime.NewFromConfig(cfg), nil
}

// bedrockProvider adapts the Bedrock Converse API to LLMProvider.
type bedrockProvider struct {
	client      bedrockClientAPI
	model       string
	system      string
	initialTask string
	messages    []types.Message
	tools       []types.Tool
	// toolsBytes is the serialized tool-schema size, fixed after construction.
	toolsBytes int
}

func newBedrockProvider(ctx context.Context, model, systemPrompt, task string, tools []ToolDef) (*bedrockProvider, error) {
	client, err := newBedrockClient(ctx)
	if err != nil {
		return nil, err
	}
	return newBedrockProviderWithClient(client, model, systemPrompt, task, tools)
}

func newBedrockProviderWithClient(client bedrockClientAPI, model, systemPrompt, task string, tools []ToolDef) (*bedrockProvider, error) {
	var bedrockTools []types.Tool
	for _, t := range tools {
		// Pass the schema map directly: smithy documents encode []byte /
		// json.RawMessage as a byte array, not a JSON object, which Bedrock
		// rejects with a ValidationException on toolConfig inputSchema.
		params := t.Parameters
		if params == nil {
			params = map[string]any{"type": "object"}
		}
		bedrockTools = append(bedrockTools, &types.ToolMemberToolSpec{
			Value: types.ToolSpecification{
				Name:        aws.String(t.Name),
				Description: aws.String(t.Description),
				InputSchema: &types.ToolInputSchemaMemberJson{
					Value: document.NewLazyDocument(params),
				},
			},
		})
	}

	return &bedrockProvider{
		client:      client,
		model:       model,
		system:      systemPrompt,
		initialTask: task,
		messages: []types.Message{
			{
				Role: types.ConversationRoleUser,
				Content: []types.ContentBlock{
					&types.ContentBlockMemberText{Value: task},
				},
			},
		},
		tools:      bedrockTools,
		toolsBytes: jsonBytes(bedrockTools),
	}, nil
}

func (p *bedrockProvider) Name() string               { return "bedrock" }
func (p *bedrockProvider) Model() string              { return p.model }
func (p *bedrockProvider) SupportsStrictSchema() bool { return false }

func (p *bedrockProvider) Chat(ctx context.Context) (ChatResult, error) {
	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(p.model),
		Messages: p.messages,
		System: []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: p.system},
		},
	}
	if len(p.tools) > 0 {
		input.ToolConfig = &types.ToolConfiguration{
			Tools: p.tools,
		}
	}

	converseCtx := ctx
	if t := effectiveRequestTimeout("bedrock"); t > 0 {
		var cancel context.CancelFunc
		converseCtx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}

	if detailedLog.Enabled() {
		detailedLog.LogLLM("request", map[string]any{
			"provider":       "bedrock",
			"model":          p.model,
			"messages_count": len(p.messages),
			"tools_count":    len(p.tools),
			"system_bytes":   len(p.system),
			"tools_bytes":    p.toolsBytes,
			"messages_bytes": jsonBytes(p.messages),
		})
	}
	output, err := p.client.Converse(converseCtx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			return ChatResult{}, fmt.Errorf("Bedrock API error (%s): %s",
				apiErr.ErrorCode(), apiErr.ErrorMessage())
		}
		return ChatResult{}, fmt.Errorf("Bedrock API error: %w", err)
	}

	result := ChatResult{
		FinishReason: string(output.StopReason),
	}
	if output.Usage != nil {
		result.InputTokens = int(aws.ToInt32(output.Usage.InputTokens))
		result.OutputTokens = int(aws.ToInt32(output.Usage.OutputTokens))
		// Stays 0 until the Converse request carries a cachePoint content
		// block; Bedrock caching is opt-in the same way Anthropic's is.
		result.CachedInputTokens = int(aws.ToInt32(output.Usage.CacheReadInputTokens))
	}

	outputMsg, ok := output.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return result, fmt.Errorf("unexpected Bedrock output shape")
	}

	var textContent string
	var toolUseBlocks []bedrockToolUse
	for _, block := range outputMsg.Value.Content {
		switch v := block.(type) {
		case *types.ContentBlockMemberText:
			textContent += v.Value
		case *types.ContentBlockMemberToolUse:
			inputBytes, _ := v.Value.Input.MarshalSmithyDocument()
			toolUseBlocks = append(toolUseBlocks, bedrockToolUse{
				ToolUseID: aws.ToString(v.Value.ToolUseId),
				Name:      aws.ToString(v.Value.Name),
				Input:     string(inputBytes),
			})
		}
	}
	result.Text = textContent

	if output.StopReason == types.StopReasonToolUse && len(toolUseBlocks) > 0 {
		for _, tu := range toolUseBlocks {
			result.ToolCalls = append(result.ToolCalls, ToolCall{
				ID:    tu.ToolUseID,
				Name:  tu.Name,
				Input: tu.Input,
			})
		}
		// Record the assistant's full content (text + tool_use) for history.
		p.messages = append(p.messages, types.Message{
			Role:    types.ConversationRoleAssistant,
			Content: append([]types.ContentBlock(nil), outputMsg.Value.Content...),
		})
	}

	return result, nil
}

func (p *bedrockProvider) AddToolResults(results []ToolResult) {
	var resultContent []types.ContentBlock
	for _, r := range results {
		toolResult := &types.ContentBlockMemberToolResult{
			Value: types.ToolResultBlock{
				ToolUseId: aws.String(r.CallID),
				Content: []types.ToolResultContentBlock{
					&types.ToolResultContentBlockMemberText{Value: r.Content},
				},
			},
		}
		if r.IsError {
			toolResult.Value.Status = types.ToolResultStatusError
		}
		resultContent = append(resultContent, toolResult)
	}
	p.messages = append(p.messages, types.Message{
		Role:    types.ConversationRoleUser,
		Content: resultContent,
	})
}

// ReplaceToolResults rewrites toolResult content blocks in place, keyed by
// toolUseId. The status is carried across so an elided error still reads as an
// error, and the block is rebuilt rather than mutated in place because the
// SDK's content blocks are interface values.
func (p *bedrockProvider) ReplaceToolResults(replacements map[string]string) {
	if len(replacements) == 0 {
		return
	}
	for mi := range p.messages {
		for bi, block := range p.messages[mi].Content {
			tr, ok := block.(*types.ContentBlockMemberToolResult)
			if !ok {
				continue
			}
			stub, ok := replacements[aws.ToString(tr.Value.ToolUseId)]
			if !ok {
				continue
			}
			p.messages[mi].Content[bi] = &types.ContentBlockMemberToolResult{
				Value: types.ToolResultBlock{
					ToolUseId: tr.Value.ToolUseId,
					Content: []types.ToolResultContentBlock{
						&types.ToolResultContentBlockMemberText{Value: stub},
					},
					Status: tr.Value.Status,
				},
			}
		}
	}
}

// ResetContext rebuilds the message slice to the seed state so
// the next Chat or Prompt call behaves as if the conversation just began.
func (p *bedrockProvider) ResetContext() {
	p.messages = []types.Message{
		{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{Value: p.initialTask},
			},
		},
	}
}

// Prompt issues a single Bedrock Converse call on behalf of a
// sidecar. With useContext false the message slice is temporarily reset
// (and restored via defer) so the answer is stateless. With useContext true
// the prompt is appended and the assistant reply recorded so context grows
// across Prompt calls within the loop. ToolConfig is omitted so the model is
// expected to answer in text only; structured output (when Schema is set)
// is parsed by the caller from the assistant's text block.
func (p *bedrockProvider) Prompt(ctx context.Context, prompt string, useContext bool, schema json.RawMessage) (string, []byte, int, int, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", nil, 0, 0, fmt.Errorf("prompt is empty")
	}

	var saved []types.Message
	rollbackUserTurn := false
	promptMsg := types.Message{
		Role: types.ConversationRoleUser,
		Content: []types.ContentBlock{
			&types.ContentBlockMemberText{Value: prompt},
		},
	}
	if !useContext {
		saved = p.messages
		p.messages = []types.Message{promptMsg}
		defer func() { p.messages = saved }()
	} else {
		// Append the user turn only after the call succeeds; on API failure
		// we pop it (see below) so a failed Prompt does not leave an orphan
		// user turn in history. Bedrock rejects back-to-back user turns
		// (role alternation violation), so a single transient error would
		// otherwise poison the run.
		rollbackUserTurn = true
		p.messages = append(p.messages, promptMsg)
		defer func() {
			if rollbackUserTurn && len(p.messages) > 0 {
				p.messages = p.messages[:len(p.messages)-1]
			}
		}()
	}

	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(p.model),
		Messages: p.messages,
		System: []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{Value: p.system},
		},
	}

	converseCtx := ctx
	if t := effectiveRequestTimeout("bedrock"); t > 0 {
		var cancel context.CancelFunc
		converseCtx, cancel = context.WithTimeout(ctx, t)
		defer cancel()
	}

	output, err := p.client.Converse(converseCtx, input)
	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			return "", nil, 0, 0, fmt.Errorf("Bedrock API error (%s): %s",
				apiErr.ErrorCode(), apiErr.ErrorMessage())
		}
		return "", nil, 0, 0, fmt.Errorf("Bedrock API error: %w", err)
	}

	inTok, outTok := 0, 0
	if output.Usage != nil {
		inTok = int(aws.ToInt32(output.Usage.InputTokens))
		outTok = int(aws.ToInt32(output.Usage.OutputTokens))
	}
	outputMsg, ok := output.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return "", nil, inTok, outTok, fmt.Errorf("unexpected Bedrock output shape")
	}
	if len(outputMsg.Value.Content) == 0 {
		return "", nil, inTok, outTok, fmt.Errorf("no content in Bedrock response")
	}
	var textContent string
	for _, block := range outputMsg.Value.Content {
		if tb, ok := block.(*types.ContentBlockMemberText); ok {
			textContent += tb.Value
		}
	}

	if useContext {
		p.messages = append(p.messages, types.Message{
			Role:    types.ConversationRoleAssistant,
			Content: append([]types.ContentBlock(nil), outputMsg.Value.Content...),
		})
		// Success: cancel the deferred rollback so the user turn stays in
		// history.
		rollbackUserTurn = false
	}

	var parsed []byte
	if len(schema) > 0 {
		trimmed := strings.TrimSpace(textContent)
		if json.Valid([]byte(trimmed)) {
			parsed = []byte(trimmed)
		} else {
			return textContent, nil, inTok, outTok, fmt.Errorf(
				"schema requested but model output was not valid JSON: %.200s", textContent)
		}
	}
	return textContent, parsed, inTok, outTok, nil
}

// bedrockToolUse holds the parsed fields from a Bedrock tool_use content block.
type bedrockToolUse struct {
	ToolUseID string
	Name      string
	Input     string
}
