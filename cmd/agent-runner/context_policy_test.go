package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

// Invalid UTF-8 *before* the cap must not drag the cut backwards: the back-off
// exists for a rune split by the cut, not for byte soup earlier in the output.
// Binary execute_command output would otherwise collapse to almost nothing.
func TestClampToolResult_InvalidUTF8BeforeCapKeepsPrefix(t *testing.T) {
	cp := contextPolicy{ToolResultMaxBytes: 100}
	content := "\xff\xfe binary header" + strings.Repeat("a", 500)
	got, dropped := cp.clampToolResult("execute_command", content)

	if dropped != len(content)-100 {
		t.Errorf("dropped = %d, want %d — the cut must not rewind past the cap", dropped, len(content)-100)
	}
	if !strings.HasPrefix(got, content[:100]) {
		t.Error("clamp discarded valid bytes following an earlier invalid byte")
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

// body returns n bytes of filler so ledger tests can keep expressing sizes.
func body(n int) string { return strings.Repeat("x", n) }

func TestLedger_TracksLiveBytes(t *testing.T) {
	l := &toolResultLedger{}
	l.add("a", "execute_command", body(100), 1)
	l.add("b", "fetch_url", body(250), 2)
	if l.liveBytes() != 350 {
		t.Errorf("liveBytes = %d, want 350", l.liveBytes())
	}
}

func TestLedger_NoElisionBelowBudget(t *testing.T) {
	cp := contextPolicy{HistoryBudgetBytes: 1000, HistoryLowBytes: 500, KeepRecent: 1}
	l := &toolResultLedger{}
	l.add("a", "fetch_url", body(300), 1)

	replacements, reclaimed := l.selectForElision(cp)
	if replacements != nil || reclaimed != 0 {
		t.Errorf("should not elide under budget, got %d replacements", len(replacements))
	}
}

func TestLedger_ElidesOldestDownToLowWaterMark(t *testing.T) {
	cp := contextPolicy{HistoryBudgetBytes: 1000, HistoryLowBytes: 500, KeepRecent: 1}
	l := &toolResultLedger{}
	l.add("a", "fetch_url", body(600), 1)
	l.add("b", "fetch_url", body(600), 2)
	l.add("c", "execute_command", body(400), 3) // newest — protected by KeepRecent

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
		l.add(id, "fetch_url", body(400), i+1)
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
		l.add(id, "read_file", body(400), 1)
	}

	if replacements, reclaimed := l.selectForElision(cp); replacements != nil || reclaimed != 0 {
		t.Errorf("elided results from the round in flight: %v", replacements)
	}

	// Once a later round arrives, round 1 becomes fair game — but only the
	// entries outside the KeepRecent window.
	l.add("r2-a", "read_file", body(400), 2)
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
	l.add("a", "fetch_url", body(600), 1)
	l.add("b", "fetch_url", body(600), 2)
	l.add("c", "execute_command", body(100), 3)

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
	l.add("a", "execute_command", body(12), 1)
	// A second round so "a" is genuinely eligible and the stub-size guard is
	// what rejects it, rather than the in-flight-round protection.
	l.add("b", "execute_command", body(12), 2)

	replacements, _ := l.selectForElision(cp)
	if len(replacements) != 0 {
		t.Errorf("should skip results smaller than their stub, got %v", replacements)
	}
}

func TestLedger_KeepRecentLargerThanHistory(t *testing.T) {
	cp := contextPolicy{HistoryBudgetBytes: 100, HistoryLowBytes: 50, KeepRecent: 5}
	l := &toolResultLedger{}
	l.add("a", "fetch_url", body(500), 1)

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

func TestLoadContextPolicy_SpillDirDefaultsAndOptOut(t *testing.T) {
	t.Setenv("CONTEXT_ELISION_SPILL_DIR", "")
	if got := loadContextPolicy().SpillDir; got != defaultElisionSpillDir {
		t.Errorf("SpillDir = %q, want default %q", got, defaultElisionSpillDir)
	}

	t.Setenv("CONTEXT_ELISION_SPILL_DIR", "off")
	if got := loadContextPolicy().SpillDir; got != "" {
		t.Errorf(`SpillDir = %q, want "" — "off" is the explicit opt-out`, got)
	}

	t.Setenv("CONTEXT_ELISION_SPILL_DIR", "/tmp/custom")
	if got := loadContextPolicy().SpillDir; got != "/tmp/custom" {
		t.Errorf("SpillDir = %q, want /tmp/custom", got)
	}
}

// Spilling is only useful if the stub's instruction can actually be followed.
func TestSpillUnavailable(t *testing.T) {
	withRead := []ToolDef{{Name: ToolExecuteCommand}, {Name: ToolReadFile}}

	if reason := spillUnavailable(defaultElisionSpillDir, withRead); reason != "" {
		t.Errorf("default config should be spillable, got %q", reason)
	}

	// A tool policy that denies read_file leaves the model no way back to the file.
	noRead := []ToolDef{{Name: ToolExecuteCommand}}
	if reason := spillUnavailable(defaultElisionSpillDir, noRead); reason == "" {
		t.Error("expected a reason when read_file is filtered out by tool policy")
	} else if !strings.Contains(reason, ToolReadFile) {
		t.Errorf("reason %q should name the missing tool", reason)
	}

	// read_file refuses paths outside its readable roots, so a spill directory
	// there produces files the agent cannot open.
	if reason := spillUnavailable("/var/log/agent", withRead); reason == "" {
		t.Error("expected a reason for a spill dir outside read_file's roots")
	}

	// Every readable root is a legitimate destination.
	for _, root := range readableRoots {
		if reason := spillUnavailable(root+"/elided", withRead); reason != "" {
			t.Errorf("%s should be spillable, got %q", root, reason)
		}
	}
}

// When the promise cannot be kept, elision must still happen — the context
// pressure is real either way — but without pointing at an unreachable file.
func TestRunAgentLoop_NoSpillWhenReadFileUnavailable(t *testing.T) {
	spillDir, err := os.MkdirTemp("/tmp", "spill")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(spillDir) })

	t.Setenv("CONTEXT_HISTORY_BUDGET_BYTES", "10000")
	t.Setenv("CONTEXT_HISTORY_BUDGET_LOW_BYTES", "4000")
	t.Setenv("CONTEXT_KEEP_RECENT_RESULTS", "1")
	t.Setenv("CONTEXT_ELISION_SPILL_DIR", spillDir)

	path := writeReadFileFixture(t, strings.Repeat("abcdefghij", 600))
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
	// Tool policy stripped read_file from this run.
	onlyExec := []ToolDef{{Name: ToolExecuteCommand}}
	if _, _, _, _, err := runAgentLoop(context.Background(), p, onlyExec); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(p.replaceLog) == 0 {
		t.Fatal("elision must still fire when spilling is unavailable")
	}
	if stub := p.replaceLog[0]["c1"]; strings.Contains(stub, spillDir) {
		t.Errorf("stub %q points at a file the run cannot read", stub)
	}
	if entries, err := os.ReadDir(spillDir); err == nil && len(entries) != 0 {
		t.Errorf("wrote %d spill file(s) the run cannot read back", len(entries))
	}
}

// The spill filename is built from the ledger index alone. Nothing the model or
// an MCP server chooses — tool name, call ID — may reach it, so there is no
// input to traverse with. Reintroducing such a component would break this.
func TestDirSpiller_PathIsDerivedOnlyFromIndex(t *testing.T) {
	const dir = "/workspace/.sympozium/elided"
	s := dirSpiller{Dir: dir}

	got := s.Path(3)
	if want := filepath.Join(dir, "0003.txt"); got != want {
		t.Errorf("Path(3) = %q, want %q", got, want)
	}
	// One path element under Dir, so filepath.Join has nothing to resolve away.
	if filepath.Dir(got) != dir {
		t.Errorf("Path(3) = %q, escaped the spill directory", got)
	}
	// Zero-padding keeps directory listings in ledger order past entry 10.
	if base := filepath.Base(s.Path(12)); base != "0012.txt" {
		t.Errorf("Path(12) base = %q, want 0012.txt", base)
	}
	if s.Path(3) == s.Path(4) {
		t.Error("distinct ledger entries must not share a file")
	}
}

// The whole point of the feature: an elided result is moved to disk intact,
// and the stub tells the model where to find it.
func TestLedger_ElisionSpillsFullOutput(t *testing.T) {
	dir := t.TempDir()
	cp := contextPolicy{HistoryBudgetBytes: 1000, HistoryLowBytes: 500, KeepRecent: 0}
	l := &toolResultLedger{spill: dirSpiller{Dir: dir}}

	original := strings.Repeat("finding-", 100) // 800 bytes
	l.add("a", "execute_command", original, 1)
	l.add("b", "fetch_url", body(800), 2)

	replacements, reclaimed := l.selectForElision(cp)
	if len(replacements) == 0 {
		t.Fatal("expected elision above budget")
	}
	if reclaimed <= 0 {
		t.Fatalf("reclaimed = %d, want > 0", reclaimed)
	}

	want := dirSpiller{Dir: dir}.Path(0)
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("elided output was not spilled: %v", err)
	}
	if string(got) != original {
		t.Errorf("spilled content does not match the original result (%d vs %d bytes)", len(got), len(original))
	}
	if !strings.Contains(replacements["a"], want) {
		t.Errorf("stub %q should name the spill path %q", replacements["a"], want)
	}

	// The ledger must not keep holding output it has already written out.
	if l.entries[0].content != "" {
		t.Error("elided entry still retains its content")
	}
}

// A read-only or absent /workspace must degrade to the old behaviour rather
// than abandoning elision — the context pressure is real either way.
func TestLedger_SpillFailureFallsBackToBareStub(t *testing.T) {
	cp := contextPolicy{HistoryBudgetBytes: 1000, HistoryLowBytes: 500, KeepRecent: 0}
	l := &toolResultLedger{spill: failingSpiller{}}
	l.add("a", "execute_command", body(800), 1)
	l.add("b", "fetch_url", body(800), 2)

	replacements, reclaimed := l.selectForElision(cp)
	if len(replacements) == 0 {
		t.Fatal("elision must still happen when spilling fails")
	}
	if reclaimed <= 0 {
		t.Errorf("reclaimed = %d, want > 0", reclaimed)
	}
	if strings.Contains(replacements["a"], "saved to") {
		t.Errorf("stub %q promises a spill file that was never written", replacements["a"])
	}
	if !strings.Contains(replacements["a"], "Re-run the tool") {
		t.Errorf("stub %q should fall back to the re-run hint", replacements["a"])
	}
}

// An entry too small to be worth eliding must not leave a file behind.
func TestLedger_SkippedEntryLeavesNoSpillFile(t *testing.T) {
	dir := t.TempDir()
	cp := contextPolicy{HistoryBudgetBytes: 10, HistoryLowBytes: 5, KeepRecent: 0}
	l := &toolResultLedger{spill: dirSpiller{Dir: dir}}
	l.add("a", "execute_command", body(12), 1)
	l.add("b", "execute_command", body(12), 2)

	if replacements, _ := l.selectForElision(cp); len(replacements) != 0 {
		t.Fatalf("should skip results smaller than their stub, got %v", replacements)
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) != 0 {
		t.Errorf("skipped entry left %d orphan file(s) behind", len(entries))
	}
}

// failingSpiller stands in for an unwritable spill directory.
type failingSpiller struct{}

func (failingSpiller) Path(seq int) string              { return "/nope/x.txt" }
func (failingSpiller) Write(path, content string) error { return errors.New("read-only file system") }

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
	if _, _, _, _, err := runAgentLoop(context.Background(), p, mockTools); err != nil {
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

// End-to-end proof that elision is no longer lossy: run the loop until the
// budget forces a rewrite, then recover the elided output through the same
// read_file tool the stub points the model at.
func TestRunAgentLoop_ElidedOutputRecoverableViaReadFile(t *testing.T) {
	fixture := strings.Repeat("abcdefghij", 600) // 6000 bytes, under read_file's cap
	path := writeReadFileFixture(t, fixture)

	// The spill dir has to sit under a readable root or read_file will refuse
	// to open it — which is exactly the constraint production runs are under.
	spillDir, err := os.MkdirTemp("/tmp", "spill")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(spillDir) })

	t.Setenv("CONTEXT_HISTORY_BUDGET_BYTES", "10000")
	t.Setenv("CONTEXT_HISTORY_BUDGET_LOW_BYTES", "4000")
	t.Setenv("CONTEXT_KEEP_RECENT_RESULTS", "1")
	t.Setenv("CONTEXT_ELISION_SPILL_DIR", spillDir)

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
	if _, _, _, _, err := runAgentLoop(context.Background(), p, mockTools); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.replaceLog) == 0 {
		t.Fatal("expected elision once the budget was crossed")
	}

	// c1 is the oldest result and the first elided, so it is ledger entry 0.
	spilled := dirSpiller{Dir: spillDir}.Path(0)
	stub := p.replaceLog[0]["c1"]
	if !strings.Contains(stub, spilled) {
		t.Fatalf("stub %q should point at %q", stub, spilled)
	}

	recovered := readFileTool(map[string]any{"path": spilled})
	if recovered != fixture {
		t.Errorf("read_file recovered %d bytes, want the original %d", len(recovered), len(fixture))
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
	if _, _, _, _, err := runAgentLoop(context.Background(), p, mockTools); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.replaceLog) != 0 {
		t.Errorf("elision must stay off by default, got %d rewrite(s)", len(p.replaceLog))
	}
}
