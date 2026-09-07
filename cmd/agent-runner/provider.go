package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// ToolCall is a provider-agnostic representation of an LLM-requested tool
// invocation. Input is the raw JSON arguments string the model emitted.
type ToolCall struct {
	ID    string
	Name  string
	Input string
}

// ToolResult is a provider-agnostic tool execution result that the loop
// feeds back into the model on the next turn.
type ToolResult struct {
	CallID  string
	Content string
	IsError bool
}

// ChatResult is what a provider returns from one round-trip with the model.
//
//   - If ToolCalls is empty, Text is the final response and the loop exits.
//   - If ToolCalls is non-empty, the loop executes them and calls Chat again.
//     Text may still be non-empty (reasoning preamble) but is ignored in that
//     case — the loop only surfaces text from the terminal turn.
type ChatResult struct {
	Text      string
	ToolCalls []ToolCall
	// InputTokens is the prompt size as the provider reports it. Note the
	// providers disagree on whether cached tokens are included: OpenAI counts
	// them inside prompt_tokens, Anthropic reports the uncached remainder in
	// input_tokens and the cached portion separately.
	InputTokens int
	// CachedInputTokens is the portion of the prompt served from the
	// provider's prefix cache. Without this the loop cannot tell an expensive
	// full read from a cheap cached one, which is the signal needed to judge
	// whether history rewriting is paying for itself.
	//
	// Only OpenAI (and OpenAI-compatible proxies such as OpenRouter) populates
	// this today, because it caches automatically above a token threshold.
	// Anthropic and Bedrock both require explicit cache breakpoints —
	// cache_control on a content block, and a cachePoint content block
	// respectively — which this runner does not yet emit, so the field is
	// always 0 on those paths. It is not a measurement bug: there is genuinely
	// no caching happening there to report.
	CachedInputTokens int
	OutputTokens      int
	FinishReason      string
}

// LLMProvider is a stateful adapter for one chat conversation with a
// particular LLM backend. Each provider owns its SDK client, its own
// message history, and its own tool-schema conversion. The agent loop
// treats providers as opaque: it only sees ChatResult/ToolCall/ToolResult.
//
// Implementations MUST be safe to use sequentially (no concurrent calls)
// and MUST preserve conversation state across Chat → AddToolResults → Chat
// cycles so the model sees a coherent history.
type LLMProvider interface {
	// Name identifies the provider system for telemetry (e.g. "openai",
	// "anthropic", "bedrock", "lm-studio").
	Name() string
	// Model returns the resolved model identifier.
	Model() string
	// Chat sends the current conversation state to the model and records
	// the assistant's reply (text + tool calls) internally for the next turn.
	Chat(ctx context.Context) (ChatResult, error)
	// AddToolResults records tool execution results in the conversation so
	// the next Chat call can reference them.
	AddToolResults(results []ToolResult)
	// ReplaceToolResults rewrites the content of previously-recorded tool
	// results, keyed by call ID, so the loop can reclaim context occupied by
	// stale output. Implementations MUST preserve the message and its call-ID
	// linkage and only swap the content — OpenAI rejects an assistant message
	// carrying tool_calls that has no matching tool reply. Unknown call IDs
	// are ignored.
	ReplaceToolResults(replacements map[string]string)

	// ResetContext wipes the conversation history. After
	// ResetContext the next Chat call behaves like a first-turn conversation.
	// Used when a sidecar issues clearContext() between independent units of
	// work (typically between services in a Collector batch) so each unit
	// starts token-flat rather than accumulating.
	ResetContext()

	// Prompt answers a single user prompt on behalf of a sidecar. The model
	// never returns tool calls — Text is the final answer. When useContext
	// is true the prompt is appended to existing history and the assistant
	// reply is recorded, so subsequent Prompt calls in the same loop see
	// the running conversation. When false the prompt is answered in
	// isolation: implementations reset the message slice to
	// [system, prompt] for the call and restore it after.
	//
	// Schema is an optional JSON Schema for structured output. When set
	// implementations attempt schema-validated output; on parse failure the
	// LLM call succeeded but the model failed to emit valid schema, and
	// callers receive an error.
	Prompt(ctx context.Context, prompt string, useContext bool, schema json.RawMessage) (string, []byte, int, int, error)
}

// runAgentLoop drives a provider through iterative tool calling until the
// model produces a terminal response or the iteration budget is exhausted.
// It owns telemetry, token accumulation, tool dispatch, and failure logging —
// providers just translate between SDK-specific types and the shared shapes.
//
// Accumulates text from every turn so that if the model's terminal turn has
// empty content (common with reasoning/instruct local models that exhaust
// their output budget on tool-call preamble), the user still sees the
// intermediate reasoning in the UX instead of a blank response.
//
// tools is the run's effective tool set, already filtered by tool policy. It is
// consulted only to decide whether elided output can be spilled somewhere the
// model can actually read it back from.
//
// Returns (responseText, inputTokens, outputTokens, toolCalls, error).
func runAgentLoop(ctx context.Context, p LLMProvider, tools []ToolDef) (string, int, int, int, error) {
	totalInputTokens := 0
	totalOutputTokens := 0
	totalCachedTokens := 0
	totalToolCalls := 0
	var accumulated strings.Builder

	// Context policy: bounds how much tool output accumulates in history and
	// is re-sent on every subsequent round.
	policy := loadContextPolicy()
	ledger := &toolResultLedger{}
	if policy.elisionEnabled() {
		spillDir := policy.SpillDir
		if spillDir != "" {
			if reason := spillUnavailable(spillDir, tools); reason != "" {
				log.Printf("WARNING: not spilling elided tool output — %s. "+
					"Elision stays destructive; set CONTEXT_ELISION_SPILL_DIR to a readable root "+
					"or allow %s to make elided results recoverable", reason, ToolReadFile)
				spillDir = ""
			} else {
				ledger.spill = dirSpiller{Dir: spillDir}
			}
		}
		log.Printf("context policy: per-result cap=%dB budget=%dB low=%dB keep_recent=%d spill_dir=%q",
			policy.ToolResultMaxBytes, policy.HistoryBudgetBytes, policy.HistoryLowBytes,
			policy.KeepRecent, spillDir)
	}

	// Report cached tokens separately rather than as a ratio: providers
	// disagree on whether cached tokens are inside the input count, so a
	// percentage would mean different things on OpenAI and Anthropic.
	defer func() {
		log.Printf("token_usage: input=%d cached=%d output=%d tool_result_bytes=%d",
			totalInputTokens, totalCachedTokens, totalOutputTokens, ledger.liveBytes())
	}()

	// Per-run token budget enforcement from membrane config.
	maxTokensPerRun := int64(0)
	tokenBudgetAction := "halt"
	if v := os.Getenv("WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			maxTokensPerRun = n
		}
	}
	if v := os.Getenv("WORKFLOW_MEMBRANE_TOKEN_BUDGET_ACTION"); v != "" {
		tokenBudgetAction = v
	}

	roundLogThreshold := int(float64(maxToolIterations) * 0.9)
	for i := 0; i < maxToolIterations; i++ {
		round := i + 1
		if round%10 == 0 || round >= roundLogThreshold {
			log.Printf("llm_round [%d/%d]", round, maxToolIterations)
		}

		chatCtx, chatSpan := obs.startChatSpan(ctx,
			attribute.String("gen_ai.system", p.Name()),
			attribute.String("gen_ai.request.model", p.Model()),
		)
		res, err := p.Chat(chatCtx)
		if err != nil {
			markSpanError(chatSpan, err)
			chatSpan.End()
			detailedLog.LogLLM("error", map[string]any{
				"provider": p.Name(),
				"model":    p.Model(),
				"round":    round,
				"error":    err.Error(),
			})
			return "", totalInputTokens, totalOutputTokens, totalToolCalls, err
		}

		detailedLog.LogLLM("response", map[string]any{
			"provider":            p.Name(),
			"model":               p.Model(),
			"round":               round,
			"finish_reason":       res.FinishReason,
			"text":                res.Text,
			"tool_calls":          len(res.ToolCalls),
			"input_tokens":        res.InputTokens,
			"cached_input_tokens": res.CachedInputTokens,
			"output_tokens":       res.OutputTokens,
			"tool_result_bytes":   ledger.liveBytes(),
		})

		totalInputTokens += res.InputTokens
		totalOutputTokens += res.OutputTokens
		totalCachedTokens += res.CachedInputTokens
		chatSpan.SetAttributes(
			attribute.Int("gen_ai.usage.input_tokens", res.InputTokens),
			attribute.Int("gen_ai.usage.cached_input_tokens", res.CachedInputTokens),
			attribute.Int("gen_ai.usage.output_tokens", res.OutputTokens),
		)
		if res.FinishReason != "" {
			chatSpan.SetAttributes(attribute.String("gen_ai.response.finish_reasons", res.FinishReason))
		}
		chatSpan.SetStatus(codes.Ok, "")
		chatSpan.End()

		// Per-run token budget check.
		if maxTokensPerRun > 0 {
			used := int64(totalInputTokens + totalOutputTokens)
			if used >= maxTokensPerRun {
				msg := fmt.Sprintf("Per-run token budget exceeded (%d/%d tokens used)", used, maxTokensPerRun)
				if tokenBudgetAction == "halt" {
					log.Printf("TOKEN BUDGET HALT: %s", msg)
					text := accumulated.String()
					if trimmed := strings.TrimSpace(res.Text); trimmed != "" {
						text = trimmed
					}
					if text == "" {
						text = msg
					}
					return text, totalInputTokens, totalOutputTokens, totalToolCalls, nil
				}
				log.Printf("TOKEN BUDGET WARN: %s", msg)
			}
		}

		// Accumulate this turn's text (reasoning preamble on tool-calling
		// turns, or the final answer on the terminal turn) so we can surface
		// something useful if the terminal text ends up empty.
		if trimmed := strings.TrimSpace(res.Text); trimmed != "" {
			if accumulated.Len() > 0 {
				accumulated.WriteString("\n\n")
			}
			accumulated.WriteString(trimmed)
		}

		// Terminal turn: no tool calls.
		if len(res.ToolCalls) == 0 {
			// Prefer the terminal turn's own text when non-empty; otherwise
			// fall back to the accumulated reasoning so the UX always shows
			// what the model produced during this run.
			if strings.TrimSpace(res.Text) != "" {
				return res.Text, totalInputTokens, totalOutputTokens, totalToolCalls, nil
			}
			if accumulated.Len() > 0 {
				log.Printf("WARNING: terminal turn had empty text after %d tool iterations; "+
					"discarding %d chars of intermediate reasoning", i, accumulated.Len())
				return "(Agent completed its task via tool calls but did not produce a final text summary.)",
					totalInputTokens, totalOutputTokens, totalToolCalls, nil
			}
			log.Printf("WARNING: terminal turn had empty text and no prior reasoning to fall back on")
			return "", totalInputTokens, totalOutputTokens, totalToolCalls, nil
		}

		// Execute each tool call and gather results for the next turn. The
		// model is informed of failures via the isError flag on each tool
		// result — no additional warning is emitted here.
		results := make([]ToolResult, 0, len(res.ToolCalls))
		for _, call := range res.ToolCalls {
			totalToolCalls++
			log.Printf("tool_call [%d]: %s id=%s", totalToolCalls, call.Name, call.ID)
			out := executeToolCallWithTelemetry(ctx, call.Name, call.Input, call.ID)
			isError := strings.HasPrefix(out, "Error:")

			// Clamp before insertion. This is the cheap half of the context
			// policy: it shapes content that has not been sent yet, so it
			// costs nothing in cache terms.
			clamped, dropped := policy.clampToolResult(call.Name, out)
			if dropped > 0 {
				log.Printf("tool_result clamped: %s dropped=%dB kept=%dB", call.Name, dropped, len(clamped))
			}

			results = append(results, ToolResult{
				CallID:  call.ID,
				Content: clamped,
				IsError: isError,
			})
			ledger.add(call.ID, call.Name, clamped, round)
		}
		p.AddToolResults(results)

		// Retroactive elision. Deliberately batched behind a high-water mark:
		// rewriting history invalidates the provider's cached prefix, so we
		// drain all the way to the low-water mark in one pass and pay that
		// cost once rather than trimming a little every round.
		if replacements, reclaimed := ledger.selectForElision(policy); len(replacements) > 0 {
			p.ReplaceToolResults(replacements)
			log.Printf("context elision: reclaimed %dB across %d tool result(s), live=%dB",
				reclaimed, len(replacements), ledger.liveBytes())
			detailedLog.LogLLM("elision", map[string]any{
				"provider":          p.Name(),
				"model":             p.Model(),
				"round":             round,
				"elided_count":      len(replacements),
				"reclaimed_bytes":   reclaimed,
				"tool_result_bytes": ledger.liveBytes(),
			})
		}
	}

	return "", totalInputTokens, totalOutputTokens, totalToolCalls,
		fmt.Errorf("exceeded maximum tool-call iterations (%d)", maxToolIterations)
}
