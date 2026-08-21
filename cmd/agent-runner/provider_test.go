package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// mockProvider is a deterministic LLMProvider for testing the shared loop.
// It replays a scripted sequence of ChatResult values, one per Chat() call,
// and records every AddToolResults invocation for assertions.
type mockProvider struct {
	name        string
	model       string
	turns       []ChatResult
	turnErr     []error // err[i] returned on turn i (nil = no error)
	idx         int
	toolLog     [][]ToolResult
	chatCalls   int
	promptTurns []mockPromptTurn
	// replaceLog records every ReplaceToolResults call so elision tests can
	// assert both what was rewritten and how many passes it took.
	replaceLog []map[string]string
}

func (p *mockProvider) Name() string               { return p.name }
func (p *mockProvider) Model() string              { return p.model }
func (p *mockProvider) SupportsStrictSchema() bool { return true }

func (p *mockProvider) Chat(ctx context.Context) (ChatResult, error) {
	p.chatCalls++
	if p.idx >= len(p.turns) {
		return ChatResult{}, fmt.Errorf("mock exhausted after %d turns", len(p.turns))
	}
	res := p.turns[p.idx]
	var err error
	if p.idx < len(p.turnErr) {
		err = p.turnErr[p.idx]
	}
	p.idx++
	return res, err
}

func (p *mockProvider) AddToolResults(results []ToolResult) {
	p.toolLog = append(p.toolLog, results)
}

func (p *mockProvider) ReplaceToolResults(replacements map[string]string) {
	p.replaceLog = append(p.replaceLog, replacements)
}

// ResetContext is a no-op for the mock because the test loop
// already seeds messages per-turn; production providers clear conversation
// history so the next call behaves like a first-turn exchange.
func (p *mockProvider) ResetContext() {}

// Prompt returns canned output for the mock. Tests that need
// to drive Prompt directly populate p.promptTurns ahead of time.
func (p *mockProvider) Prompt(ctx context.Context, prompt string, useContext bool, schema json.RawMessage) (string, []byte, int, int, error) {
	if len(p.promptTurns) == 0 {
		return "", nil, 0, 0, fmt.Errorf("mockProvider: no promptTurns configured")
	}
	turn := p.promptTurns[0]
	p.promptTurns = p.promptTurns[1:]
	return turn.Content, turn.Parsed, turn.InTok, turn.OutTok, turn.Err
}

type mockPromptTurn struct {
	Content string
	Parsed  []byte
	InTok   int
	OutTok  int
	Err     error
}

// TestRunAgentLoop_TerminalTextOnly: a single turn with text and no tool calls
// exits cleanly with that text.
func TestRunAgentLoop_TerminalTextOnly(t *testing.T) {
	p := &mockProvider{
		name:  "mock",
		model: "mock-1",
		turns: []ChatResult{
			{Text: "final answer", InputTokens: 10, OutputTokens: 5},
		},
	}
	text, in, out, toolCalls, err := runAgentLoop(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "final answer" {
		t.Errorf("text = %q, want %q", text, "final answer")
	}
	if in != 10 || out != 5 {
		t.Errorf("tokens = (%d,%d), want (10,5)", in, out)
	}
	if toolCalls != 0 {
		t.Errorf("toolCalls = %d, want 0", toolCalls)
	}
	if p.chatCalls != 1 {
		t.Errorf("chatCalls = %d, want 1", p.chatCalls)
	}
}

// TestRunAgentLoop_ToolCallThenText: loop executes one tool call then returns
// text on the second turn. Also verifies token accumulation across turns.
func TestRunAgentLoop_ToolCallThenText(t *testing.T) {
	p := &mockProvider{
		name:  "mock",
		model: "mock-1",
		turns: []ChatResult{
			{
				Text:        "let me check",
				InputTokens: 20, OutputTokens: 30,
				ToolCalls: []ToolCall{
					{ID: "call-1", Name: "read_file", Input: `{"path":"/tmp/foo"}`},
				},
				FinishReason: "tool_calls",
			},
			{Text: "contents: hello", InputTokens: 40, OutputTokens: 10, FinishReason: "stop"},
		},
	}
	text, in, out, toolCalls, err := runAgentLoop(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "contents: hello" {
		t.Errorf("text = %q, want %q", text, "contents: hello")
	}
	if in != 60 {
		t.Errorf("input tokens = %d, want 60", in)
	}
	if out != 40 {
		t.Errorf("output tokens = %d, want 40", out)
	}
	if toolCalls != 1 {
		t.Errorf("toolCalls = %d, want 1", toolCalls)
	}
	if len(p.toolLog) != 1 || len(p.toolLog[0]) != 1 {
		t.Fatalf("toolLog = %v, want one batch with one result", p.toolLog)
	}
	if p.toolLog[0][0].CallID != "call-1" {
		t.Errorf("tool result CallID = %q, want %q", p.toolLog[0][0].CallID, "call-1")
	}
}

// TestRunAgentLoop_ToolFailuresDoNotBlock: even when every tool call fails
// for many consecutive iterations, the loop keeps going. The model (mock)
// eventually produces text and the response is surfaced. This is the
// regression guard for the circuit-breaker issue.
func TestRunAgentLoop_ToolFailuresDoNotBlock(t *testing.T) {
	// Build a turn that triggers a tool call pointing at a nonexistent file
	// so executeToolCall returns "Error: ...". Repeat 8 times, then terminate
	// with text. If the old circuit breaker were still in place, this would
	// return an error with empty text after 3 iterations.
	var turns []ChatResult
	for i := 0; i < 8; i++ {
		turns = append(turns, ChatResult{
			InputTokens: 1, OutputTokens: 1,
			ToolCalls: []ToolCall{
				{ID: fmt.Sprintf("c%d", i), Name: "read_file", Input: `{"path":"/nonexistent/xyz"}`},
			},
			FinishReason: "tool_calls",
		})
	}
	turns = append(turns, ChatResult{Text: "gave up and summarized", InputTokens: 2, OutputTokens: 3})

	p := &mockProvider{name: "mock", model: "mock-1", turns: turns}
	text, _, _, toolCalls, err := runAgentLoop(context.Background(), p)
	if err != nil {
		t.Fatalf("loop returned error despite tool failures: %v", err)
	}
	if text != "gave up and summarized" {
		t.Errorf("text = %q, want 'gave up and summarized'", text)
	}
	if toolCalls != 8 {
		t.Errorf("toolCalls = %d, want 8 (all iterations should have been executed)", toolCalls)
	}
	if len(p.toolLog) != 8 {
		t.Errorf("AddToolResults called %d times, want 8", len(p.toolLog))
	}
	// Every recorded result should be an error.
	for i, batch := range p.toolLog {
		for _, r := range batch {
			if !r.IsError {
				t.Errorf("iteration %d: expected IsError=true for nonexistent file", i)
			}
			if !strings.HasPrefix(r.Content, "Error:") {
				t.Errorf("iteration %d: expected Content to start with 'Error:', got %q", i, r.Content)
			}
		}
	}
}

// TestRunAgentLoop_EmptyTerminalFallsBackToAccumulated: models that emit
// reasoning text on tool-call turns but produce empty content on the terminal
// turn (observed with qwen3.5-9b on LM Studio) still surface useful text to
// the user via the accumulated fallback. This is the platform-team regression
// guard.
func TestRunAgentLoop_EmptyTerminalFallsBackToAccumulated(t *testing.T) {
	p := &mockProvider{
		name:  "mock",
		model: "mock-reasoning",
		turns: []ChatResult{
			{
				Text:        "I'll scan the cluster now.",
				InputTokens: 100, OutputTokens: 50,
				ToolCalls: []ToolCall{
					{ID: "scan", Name: "read_file", Input: `{"path":"/tmp/x"}`},
				},
				FinishReason: "tool_calls",
			},
			// Terminal turn: model emits tokens but Content is empty
			// (e.g. reasoning mode with exhausted budget, qwen3.5 quirk).
			{Text: "", InputTokens: 200, OutputTokens: 242, FinishReason: "stop"},
		},
	}
	text, in, out, toolCalls, err := runAgentLoop(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text == "" {
		t.Fatal("response is EMPTY — fallback did not kick in")
	}
	wantSynthetic := "(Agent completed its task via tool calls but did not produce a final text summary.)"
	if text != wantSynthetic {
		t.Errorf("fallback text = %q, want synthetic message %q", text, wantSynthetic)
	}
	if in != 300 || out != 292 {
		t.Errorf("tokens = (%d,%d), want (300,292) accumulated across turns", in, out)
	}
	if toolCalls != 1 {
		t.Errorf("toolCalls = %d, want 1", toolCalls)
	}
}

// TestRunAgentLoop_AllTurnsEmptyReturnsEmpty: if literally no turn produces
// text, the response IS empty (we don't fabricate content).
func TestRunAgentLoop_AllTurnsEmptyReturnsEmpty(t *testing.T) {
	p := &mockProvider{
		name:  "mock",
		model: "mock-silent",
		turns: []ChatResult{
			{
				Text:         "",
				ToolCalls:    []ToolCall{{ID: "a", Name: "read_file", Input: `{"path":"/tmp/x"}`}},
				FinishReason: "tool_calls",
			},
			{Text: "", FinishReason: "stop"},
		},
	}
	text, _, _, _, err := runAgentLoop(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "" {
		t.Errorf("text = %q, want empty when no turn produced text", text)
	}
}

// TestRunAgentLoop_TerminalTextPreferredOverAccumulated: when the terminal
// turn has non-empty text, it should be returned as-is (not merged with
// intermediate reasoning) so the final answer is clean.
func TestRunAgentLoop_TerminalTextPreferredOverAccumulated(t *testing.T) {
	p := &mockProvider{
		name:  "mock",
		model: "mock-1",
		turns: []ChatResult{
			{
				Text:         "checking",
				ToolCalls:    []ToolCall{{ID: "a", Name: "read_file", Input: `{"path":"/tmp/x"}`}},
				FinishReason: "tool_calls",
			},
			{Text: "final answer is 42", FinishReason: "stop"},
		},
	}
	text, _, _, _, err := runAgentLoop(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "final answer is 42" {
		t.Errorf("text = %q, want terminal turn text only", text)
	}
}

// TestRunAgentLoop_IterationBudget: if the model keeps calling tools past
// maxToolIterations, the loop returns a budget-exceeded error.
func TestRunAgentLoop_IterationBudget(t *testing.T) {
	// Save + restore global budget.
	orig := maxToolIterations
	maxToolIterations = 3
	defer func() { maxToolIterations = orig }()

	var turns []ChatResult
	for i := 0; i < 10; i++ {
		turns = append(turns, ChatResult{
			ToolCalls: []ToolCall{
				{ID: fmt.Sprintf("c%d", i), Name: "read_file", Input: `{"path":"/tmp/x"}`},
			},
			FinishReason: "tool_calls",
		})
	}
	p := &mockProvider{name: "mock", model: "mock-1", turns: turns}
	_, _, _, toolCalls, err := runAgentLoop(context.Background(), p)
	if err == nil {
		t.Fatal("expected budget-exceeded error")
	}
	if !strings.Contains(err.Error(), "exceeded maximum tool-call iterations") {
		t.Errorf("error = %v, want budget-exceeded message", err)
	}
	if toolCalls != 3 {
		t.Errorf("toolCalls = %d, want 3 (one per iteration of budget)", toolCalls)
	}
}

// TestRunAgentLoop_ChatErrorPropagates: if Chat returns an error, the loop
// returns immediately with accumulated tokens.
func TestRunAgentLoop_ChatErrorPropagates(t *testing.T) {
	p := &mockProvider{
		name:    "mock",
		model:   "mock-1",
		turns:   []ChatResult{{}},
		turnErr: []error{fmt.Errorf("provider rate limit")},
	}
	_, _, _, _, err := runAgentLoop(context.Background(), p)
	if err == nil {
		t.Fatal("expected error from Chat to propagate")
	}
	if !strings.Contains(err.Error(), "rate limit") {
		t.Errorf("error = %v, want to contain 'rate limit'", err)
	}
}

// TestRunAgentLoop_MultipleToolCallsPerTurn: many tool calls in one turn are
// all executed and results appended in order.
func TestRunAgentLoop_MultipleToolCallsPerTurn(t *testing.T) {
	p := &mockProvider{
		name:  "mock",
		model: "mock-1",
		turns: []ChatResult{
			{
				ToolCalls: []ToolCall{
					{ID: "a", Name: "read_file", Input: `{"path":"/tmp/a"}`},
					{ID: "b", Name: "read_file", Input: `{"path":"/tmp/b"}`},
					{ID: "c", Name: "read_file", Input: `{"path":"/tmp/c"}`},
				},
				FinishReason: "tool_calls",
			},
			{Text: "done", FinishReason: "stop"},
		},
	}
	_, _, _, toolCalls, err := runAgentLoop(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if toolCalls != 3 {
		t.Errorf("toolCalls = %d, want 3", toolCalls)
	}
	if len(p.toolLog) != 1 || len(p.toolLog[0]) != 3 {
		t.Fatalf("expected one batch of 3 results, got %v", p.toolLog)
	}
	for i, want := range []string{"a", "b", "c"} {
		if p.toolLog[0][i].CallID != want {
			t.Errorf("result[%d].CallID = %q, want %q", i, p.toolLog[0][i].CallID, want)
		}
	}
}

// TestRunAgentLoop_MaxTokensPerRun_Halt: when per-run token budget is set to
// "halt", the loop should stop early once token usage exceeds the limit.
func TestRunAgentLoop_MaxTokensPerRun_Halt(t *testing.T) {
	os.Setenv("WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN", "100")
	os.Setenv("WORKFLOW_MEMBRANE_TOKEN_BUDGET_ACTION", "halt")
	defer func() {
		os.Unsetenv("WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN")
		os.Unsetenv("WORKFLOW_MEMBRANE_TOKEN_BUDGET_ACTION")
	}()

	p := &mockProvider{
		name:  "mock",
		model: "mock-1",
		turns: []ChatResult{
			// Turn 1: 60+50=110 tokens → exceeds 100 limit → halt
			{
				Text:         "reasoning about the task",
				InputTokens:  60,
				OutputTokens: 50,
				ToolCalls: []ToolCall{
					{ID: "c1", Name: "read_file", Input: `{"path":"/tmp/x"}`},
				},
				FinishReason: "tool_calls",
			},
			// Turn 2: should NOT be reached
			{Text: "should not reach this", InputTokens: 100, OutputTokens: 100},
		},
	}

	text, in, out, _, err := runAgentLoop(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have stopped after turn 1.
	if p.chatCalls != 1 {
		t.Errorf("chatCalls = %d, want 1 (halt after first turn)", p.chatCalls)
	}
	if in != 60 || out != 50 {
		t.Errorf("tokens = (%d,%d), want (60,50)", in, out)
	}
	if text == "" {
		t.Error("expected non-empty text from halt")
	}
}

// TestRunAgentLoop_MaxTokensPerRun_Warn: in "warn" mode, the loop should
// continue past the budget and log a warning, not halt.
func TestRunAgentLoop_MaxTokensPerRun_Warn(t *testing.T) {
	os.Setenv("WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN", "100")
	os.Setenv("WORKFLOW_MEMBRANE_TOKEN_BUDGET_ACTION", "warn")
	defer func() {
		os.Unsetenv("WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN")
		os.Unsetenv("WORKFLOW_MEMBRANE_TOKEN_BUDGET_ACTION")
	}()

	p := &mockProvider{
		name:  "mock",
		model: "mock-1",
		turns: []ChatResult{
			// Turn 1: 60+50=110 tokens → exceeds 100 limit → warn but continue
			{
				Text:         "reasoning",
				InputTokens:  60,
				OutputTokens: 50,
				ToolCalls: []ToolCall{
					{ID: "c1", Name: "read_file", Input: `{"path":"/tmp/x"}`},
				},
				FinishReason: "tool_calls",
			},
			// Turn 2: should be reached because warn mode continues
			{Text: "final answer", InputTokens: 40, OutputTokens: 10, FinishReason: "stop"},
		},
	}

	text, in, out, _, err := runAgentLoop(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.chatCalls != 2 {
		t.Errorf("chatCalls = %d, want 2 (warn should not halt)", p.chatCalls)
	}
	if text != "final answer" {
		t.Errorf("text = %q, want 'final answer'", text)
	}
	if in != 100 || out != 60 {
		t.Errorf("tokens = (%d,%d), want (100,60)", in, out)
	}
}

// TestRunAgentLoop_MaxTokensPerRun_NotSet: when env var is not set, the loop
// should run normally without budget enforcement.
func TestRunAgentLoop_MaxTokensPerRun_NotSet(t *testing.T) {
	os.Unsetenv("WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN")
	os.Unsetenv("WORKFLOW_MEMBRANE_TOKEN_BUDGET_ACTION")

	p := &mockProvider{
		name:  "mock",
		model: "mock-1",
		turns: []ChatResult{
			{
				InputTokens: 5000, OutputTokens: 5000,
				ToolCalls:    []ToolCall{{ID: "c1", Name: "read_file", Input: `{"path":"/tmp/x"}`}},
				FinishReason: "tool_calls",
			},
			{Text: "done", InputTokens: 5000, OutputTokens: 5000, FinishReason: "stop"},
		},
	}

	text, _, _, _, err := runAgentLoop(context.Background(), p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "done" {
		t.Errorf("text = %q, want 'done'", text)
	}
	if p.chatCalls != 2 {
		t.Errorf("chatCalls = %d, want 2 (no budget = no halt)", p.chatCalls)
	}
}
