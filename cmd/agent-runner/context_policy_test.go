package main

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

// An unconfigured run must behave exactly as it did before the context policy
// existed: nothing clamped, nothing elided.
func TestLoadContextPolicy_DefaultsAreInert(t *testing.T) {
	// Clear the knobs explicitly rather than trusting the ambient environment,
	// or this fails on a dev machine that happens to export them.
	for _, k := range []string{
		"CONTEXT_TOOL_RESULT_MAX_BYTES",
		"CONTEXT_HISTORY_BUDGET_BYTES",
		"CONTEXT_HISTORY_BUDGET_LOW_BYTES",
		"CONTEXT_KEEP_RECENT_RESULTS",
	} {
		t.Setenv(k, "")
	}

	cp := loadContextPolicy()
	if cp.ToolResultMaxBytes != 0 {
		t.Errorf("ToolResultMaxBytes = %d, want 0 — clamping must not be imposed by default", cp.ToolResultMaxBytes)
	}
	if cp.elisionEnabled() {
		t.Error("elision should be off by default — rewriting history has a cache cost that must be opted into")
	}

	// The inert policy must leave even a very large result untouched.
	long := strings.Repeat("x", 200_000)
	if got, dropped := cp.clampToolResult("fetch_url", long); got != long || dropped != 0 {
		t.Errorf("default policy altered a result: dropped=%d", dropped)
	}
}

func TestLoadContextPolicy_LowWaterDefaultsToHalf(t *testing.T) {
	t.Setenv("CONTEXT_HISTORY_BUDGET_BYTES", "10000")
	cp := loadContextPolicy()
	if cp.HistoryLowBytes != 5000 {
		t.Errorf("HistoryLowBytes = %d, want 5000", cp.HistoryLowBytes)
	}
}

// A low-water mark at or above the budget would make elision fire every round
// without ever making progress, thrashing the prefix cache.
func TestLoadContextPolicy_LowWaterClampedBelowBudget(t *testing.T) {
	t.Setenv("CONTEXT_HISTORY_BUDGET_BYTES", "10000")
	t.Setenv("CONTEXT_HISTORY_BUDGET_LOW_BYTES", "10000")
	cp := loadContextPolicy()
	if cp.HistoryLowBytes >= cp.HistoryBudgetBytes {
		t.Errorf("HistoryLowBytes = %d must be < budget %d", cp.HistoryLowBytes, cp.HistoryBudgetBytes)
	}
}

func TestLoadContextPolicy_InvalidValueFallsBackToDefault(t *testing.T) {
	t.Setenv("CONTEXT_TOOL_RESULT_MAX_BYTES", "not-a-number")
	if got := loadContextPolicy().ToolResultMaxBytes; got != defaultToolResultMaxBytes {
		t.Errorf("ToolResultMaxBytes = %d, want default %d", got, defaultToolResultMaxBytes)
	}
}

func TestLoadContextPolicy_ClampHonouredWhenSet(t *testing.T) {
	t.Setenv("CONTEXT_TOOL_RESULT_MAX_BYTES", "500")
	cp := loadContextPolicy()
	if cp.ToolResultMaxBytes != 500 {
		t.Fatalf("ToolResultMaxBytes = %d, want 500", cp.ToolResultMaxBytes)
	}
	if _, dropped := cp.clampToolResult("fetch_url", strings.Repeat("x", 1200)); dropped != 700 {
		t.Errorf("dropped = %d, want 700", dropped)
	}
}

func TestClampToolResult(t *testing.T) {
	cp := contextPolicy{ToolResultMaxBytes: 100}

	short := "small output"
	got, dropped := cp.clampToolResult("execute_command", short)
	if got != short || dropped != 0 {
		t.Errorf("short content should pass through unchanged, got dropped=%d", dropped)
	}

	long := strings.Repeat("x", 500)
	got, dropped = cp.clampToolResult("fetch_url", long)
	if dropped != 400 {
		t.Errorf("dropped = %d, want 400", dropped)
	}
	if !strings.HasPrefix(got, strings.Repeat("x", 100)) {
		t.Error("clamped content should retain the first ToolResultMaxBytes bytes")
	}
	if !strings.Contains(got, "fetch_url") {
		t.Error("truncation notice should name the tool so the model knows what was cut")
	}
}

// A byte-exact cut can land inside a multi-byte rune; json.Marshal would then
// substitute U+FFFD into the request. The clamp must back off to a boundary.
func TestClampToolResult_CutsOnRuneBoundary(t *testing.T) {
	cp := contextPolicy{ToolResultMaxBytes: 10}
	// Six 3-byte runes; a cap of 10 falls mid-rune.
	got, dropped := cp.clampToolResult("fetch_url", "日本語日本語")

	if !utf8.ValidString(got) {
		t.Errorf("clamp emitted invalid UTF-8: %q", got)
	}
	if !strings.HasPrefix(got, "日本語") {
		t.Errorf("should keep whole runes up to the cap, got %q", got)
	}
	// 18 bytes in, 9 kept after backing off the partial rune.
	if dropped != 9 {
		t.Errorf("dropped = %d, want 9 — the count must reflect the boundary back-off", dropped)
	}
}

func TestClampToolResult_Disabled(t *testing.T) {
	cp := contextPolicy{ToolResultMaxBytes: 0}
	long := strings.Repeat("x", 500)
	got, dropped := cp.clampToolResult("fetch_url", long)
	if got != long || dropped != 0 {
		t.Error("a zero cap must disable clamping entirely")
	}
}

func TestLedger_TracksLiveBytes(t *testing.T) {
	l := &toolResultLedger{}
	l.add("a", "execute_command", 100, 1)
	l.add("b", "fetch_url", 250, 2)
	if l.liveBytes() != 350 {
		t.Errorf("liveBytes = %d, want 350", l.liveBytes())
	}
}

func TestLedger_NoElisionBelowBudget(t *testing.T) {
	cp := contextPolicy{HistoryBudgetBytes: 1000, HistoryLowBytes: 500, KeepRecent: 1}
	l := &toolResultLedger{}
	l.add("a", "fetch_url", 300, 1)

	replacements, reclaimed := l.selectForElision(cp)
	if replacements != nil || reclaimed != 0 {
		t.Errorf("should not elide under budget, got %d replacements", len(replacements))
	}
}

func TestLedger_ElidesOldestDownToLowWaterMark(t *testing.T) {
	cp := contextPolicy{HistoryBudgetBytes: 1000, HistoryLowBytes: 500, KeepRecent: 1}
	l := &toolResultLedger{}
	l.add("a", "fetch_url", 600, 1)
	l.add("b", "fetch_url", 600, 2)
	l.add("c", "execute_command", 400, 3) // newest — protected by KeepRecent

	replacements, reclaimed := l.selectForElision(cp)
	if len(replacements) == 0 {
		t.Fatal("expected elision above budget")
	}
	if _, ok := replacements["c"]; ok {
		t.Error("newest result must be protected by KeepRecent")
	}
	if _, ok := replacements["a"]; !ok {
		t.Error("oldest result should be elided first")
	}
	// The low-water mark is a target, not a floor: protected entries and the
	// stubs themselves occupy space. What must hold is that the pass drops
	// below the budget, so it does not immediately re-fire.
	if l.liveBytes() > cp.HistoryBudgetBytes {
		t.Errorf("liveBytes = %d, should have drained below budget %d", l.liveBytes(), cp.HistoryBudgetBytes)
	}
	if reclaimed <= 0 {
		t.Errorf("reclaimed = %d, want > 0", reclaimed)
	}
}

// When nothing is protected and results are large, the drain does reach the
// low-water mark. Each result is from its own round so only the last is held
// back by the in-flight-round guard.
func TestLedger_ReachesLowWaterMarkWhenUnobstructed(t *testing.T) {
	cp := contextPolicy{HistoryBudgetBytes: 1000, HistoryLowBytes: 500, KeepRecent: 0}
	l := &toolResultLedger{}
	for i, id := range []string{"a", "b", "c", "d"} {
		l.add(id, "fetch_url", 400, i+1)
	}

	if _, reclaimed := l.selectForElision(cp); reclaimed == 0 {
		t.Fatal("expected elision")
	}
	// "d" is the in-flight round and survives, so the floor is its 400 bytes
	// plus the three stubs.
	if l.liveBytes() > cp.HistoryBudgetBytes {
		t.Errorf("liveBytes = %d, want <= budget %d", l.liveBytes(), cp.HistoryBudgetBytes)
	}
	if l.entries[3].elided {
		t.Error("the in-flight round's result must not be elided")
	}
}

// A round can issue more parallel tool calls than KeepRecent. Those results
// must still be safe: the model requested them and has not seen the answers
// yet, so replacing them with "re-run the tool" stubs invites a request loop.
func TestLedger_CurrentRoundIsNeverElided(t *testing.T) {
	cp := contextPolicy{HistoryBudgetBytes: 1000, HistoryLowBytes: 500, KeepRecent: 3}
	l := &toolResultLedger{}
	for _, id := range []string{"r1-a", "r1-b", "r1-c", "r1-d", "r1-e"} {
		l.add(id, "read_file", 400, 1)
	}

	if replacements, reclaimed := l.selectForElision(cp); replacements != nil || reclaimed != 0 {
		t.Errorf("elided results from the round in flight: %v", replacements)
	}

	// Once a later round arrives, round 1 becomes fair game — but only the
	// entries outside the KeepRecent window.
	l.add("r2-a", "read_file", 400, 2)
	replacements, _ := l.selectForElision(cp)
	if len(replacements) == 0 {
		t.Fatal("expected elision once the model had a turn to read round 1")
	}
	if _, ok := replacements["r2-a"]; ok {
		t.Error("round 2 is now the in-flight round and must be protected")
	}
	for _, id := range []string{"r1-d", "r1-e"} {
		if _, ok := replacements[id]; ok {
			t.Errorf("%s is inside the KeepRecent window and must be protected", id)
		}
	}
	if _, ok := replacements["r1-a"]; !ok {
		t.Error("oldest result should be elided first")
	}
}

// Elision must be a one-shot drain, not a per-round trim: a second pass with no
// new results should find nothing to do, so the cache is invalidated once.
func TestLedger_SecondPassIsNoOp(t *testing.T) {
	cp := contextPolicy{HistoryBudgetBytes: 1000, HistoryLowBytes: 500, KeepRecent: 1}
	l := &toolResultLedger{}
	l.add("a", "fetch_url", 600, 1)
	l.add("b", "fetch_url", 600, 2)
	l.add("c", "execute_command", 100, 3)

	if replacements, _ := l.selectForElision(cp); len(replacements) == 0 {
		t.Fatal("first pass should elide")
	}
	if replacements, reclaimed := l.selectForElision(cp); replacements != nil || reclaimed != 0 {
		t.Errorf("second pass should be a no-op, got %d replacements", len(replacements))
	}
}

// Replacing a small result with a longer stub would grow the request instead of
// shrinking it.
func TestLedger_SkipsResultsSmallerThanTheirStub(t *testing.T) {
	cp := contextPolicy{HistoryBudgetBytes: 10, HistoryLowBytes: 5, KeepRecent: 0}
	l := &toolResultLedger{}
	l.add("a", "execute_command", 12, 1)
	// A second round so "a" is genuinely eligible and the stub-size guard is
	// what rejects it, rather than the in-flight-round protection.
	l.add("b", "execute_command", 12, 2)

	replacements, _ := l.selectForElision(cp)
	if len(replacements) != 0 {
		t.Errorf("should skip results smaller than their stub, got %v", replacements)
	}
}

func TestLedger_KeepRecentLargerThanHistory(t *testing.T) {
	cp := contextPolicy{HistoryBudgetBytes: 100, HistoryLowBytes: 50, KeepRecent: 5}
	l := &toolResultLedger{}
	l.add("a", "fetch_url", 500, 1)

	if replacements, _ := l.selectForElision(cp); replacements != nil {
		t.Error("nothing is eligible when KeepRecent covers the whole history")
	}
}

func TestElisionStub_NamesToolAndSize(t *testing.T) {
	stub := elisionStub("fetch_url", 48213)
	if !strings.Contains(stub, "fetch_url") || !strings.Contains(stub, "48213") {
		t.Errorf("stub should name the tool and reclaimed size, got %q", stub)
	}
}

// TestRunAgentLoop_ElidesAtHighWaterMark exercises the full wiring: real tool
// execution produces oversized results, the ledger crosses the budget, and the
// loop hands the provider a rewrite. The newest result must never be rewritten.
func TestRunAgentLoop_ElidesAtHighWaterMark(t *testing.T) {
	path := writeReadFileFixture(t, strings.Repeat("abcdefghij", 600))

	t.Setenv("CONTEXT_HISTORY_BUDGET_BYTES", "10000")
	t.Setenv("CONTEXT_HISTORY_BUDGET_LOW_BYTES", "4000")
	t.Setenv("CONTEXT_KEEP_RECENT_RESULTS", "1")

	args := `{"path":"` + path + `"}`
	var turns []ChatResult
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		turns = append(turns, ChatResult{
			ToolCalls:    []ToolCall{{ID: id, Name: "read_file", Input: args}},
			FinishReason: "tool_calls",
		})
	}
	turns = append(turns, ChatResult{Text: "done", FinishReason: "stop"})

	p := &mockProvider{name: "mock", model: "mock-1", turns: turns}
	if _, _, _, _, err := runAgentLoop(context.Background(), p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(p.replaceLog) == 0 {
		t.Fatal("expected at least one ReplaceToolResults call once the budget was crossed")
	}
	for _, replacements := range p.replaceLog {
		if _, ok := replacements["c4"]; ok {
			t.Error("newest result c4 must be protected by CONTEXT_KEEP_RECENT_RESULTS")
		}
	}
}

// With no budget configured the loop must never rewrite history.
func TestRunAgentLoop_NoElisionWhenBudgetUnset(t *testing.T) {
	p := &mockProvider{
		name: "mock", model: "mock-1",
		turns: []ChatResult{
			{ToolCalls: []ToolCall{{ID: "c1", Name: "read_file", Input: `{"path":"/tmp/nope"}`}}, FinishReason: "tool_calls"},
			{Text: "done", FinishReason: "stop"},
		},
	}
	if _, _, _, _, err := runAgentLoop(context.Background(), p); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.replaceLog) != 0 {
		t.Errorf("elision must stay off by default, got %d rewrite(s)", len(p.replaceLog))
	}
}
