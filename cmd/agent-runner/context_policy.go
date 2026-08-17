package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// defaultToolResultMaxBytes is 0 — clamping is off unless asked for.
	// Individual tools already cap their own output (execute_command and
	// read_file at 8 KB, fetch_url at 50 KB); a default here would silently
	// override those, so the whole policy is opt-in and a run behaves exactly
	// as it did before unless one of the CONTEXT_* vars is set.
	defaultToolResultMaxBytes = 0
	defaultKeepRecentResults  = 3

	// defaultElisionSpillDir is where elided tool output is parked so the model
	// can read it back. It sits under the same /workspace/.sympozium prefix the
	// runner already uses for trace-context.json, and is only ever created when
	// elision actually fires.
	defaultElisionSpillDir = "/workspace/.sympozium/elided"
)

// contextPolicy bounds how much tool-result content accumulates in the
// conversation history.
//
// Tool results are appended to the message array and re-sent on every
// subsequent round, so a result added at round 5 is billed again on each of
// the remaining rounds — with maxToolIterations at 50 that multiplier, not the
// static system prompt, dominates a run's token spend.
//
// Two independent mechanisms, deliberately separated by their effect on the
// provider's prefix cache:
//
//   - ToolResultMaxBytes clamps each result as it is inserted. Cheap, and it
//     never disturbs the cache because it only shapes content that has not
//     been sent yet.
//   - HistoryBudgetBytes retroactively elides older results once the running
//     total crosses a high-water mark. This rewrites history mid-array, which
//     invalidates the cached prefix from that point on, so it is deliberately
//     batched: we elide all the way down to HistoryLowBytes in one pass rather
//     than trimming a little each round. That pays one cache write and then
//     runs every remaining round against a much smaller prefix.
//
// Elision moves output out of the conversation; it does not destroy it. The
// full result is written to SpillDir first and the placeholder names that path,
// so the model recovers detail with read_file (which paginates) instead of
// re-running a tool whose side effects may not be repeatable.
type contextPolicy struct {
	// ToolResultMaxBytes clamps a single tool result at insertion. 0 disables.
	ToolResultMaxBytes int
	// HistoryBudgetBytes is the high-water mark of live tool-result bytes that
	// triggers elision. 0 disables elision entirely (today's behaviour).
	HistoryBudgetBytes int
	// HistoryLowBytes is the low-water mark elision drains down to.
	HistoryLowBytes int
	// KeepRecent is the number of newest results never eligible for elision —
	// those are what the model is actively reasoning over. The current round's
	// results are protected regardless of this value; see protectedCutoff.
	KeepRecent int
	// SpillDir is where an elided result's full output is written so the model
	// can recover it with read_file instead of re-running the tool. Empty
	// disables spilling, and elision falls back to a bare placeholder.
	SpillDir string
}

// elisionEnabled reports whether retroactive elision is configured.
func (cp contextPolicy) elisionEnabled() bool { return cp.HistoryBudgetBytes > 0 }

// loadContextPolicy reads the policy from the environment. Every mechanism is
// off by default: an unconfigured run sends exactly what it sent before this
// policy existed. Both knobs change what the model sees — clamping cuts tool
// output, elision drops it retroactively — so neither is imposed silently.
func loadContextPolicy() contextPolicy {
	cp := contextPolicy{
		ToolResultMaxBytes: envInt("CONTEXT_TOOL_RESULT_MAX_BYTES", defaultToolResultMaxBytes),
		HistoryBudgetBytes: envInt("CONTEXT_HISTORY_BUDGET_BYTES", 0),
		HistoryLowBytes:    envInt("CONTEXT_HISTORY_BUDGET_LOW_BYTES", 0),
		KeepRecent:         envInt("CONTEXT_KEEP_RECENT_RESULTS", defaultKeepRecentResults),
		SpillDir:           getEnv("CONTEXT_ELISION_SPILL_DIR", defaultElisionSpillDir),
	}
	// "off" is the explicit opt-out: an empty env var is indistinguishable from
	// an unset one, so it cannot carry that meaning.
	if cp.SpillDir == "off" {
		cp.SpillDir = ""
	}
	if cp.ToolResultMaxBytes < 0 {
		cp.ToolResultMaxBytes = 0
	}
	if cp.KeepRecent < 0 {
		cp.KeepRecent = 0
	}
	if cp.HistoryBudgetBytes < 0 {
		cp.HistoryBudgetBytes = 0
	}
	// Default the low-water mark to half the budget, and keep it strictly
	// below the budget so elision always makes progress (otherwise a budget
	// crossing would re-fire every round and thrash the cache).
	if cp.HistoryBudgetBytes > 0 {
		if cp.HistoryLowBytes <= 0 || cp.HistoryLowBytes >= cp.HistoryBudgetBytes {
			cp.HistoryLowBytes = cp.HistoryBudgetBytes / 2
		}
	}
	return cp
}

// envInt reads an integer environment variable, falling back to def when unset
// or unparseable.
func envInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("WARNING: %s=%q is not an integer; using default %d", key, v, def)
		return def
	}
	return n
}

// jsonBytes reports the serialized size of v, for request-size accounting.
// Callers should gate this behind detailedLog.Enabled() — it re-marshals the
// whole message array and is only worth paying for when someone is measuring.
// Returns 0 rather than an error: this feeds diagnostics, never control flow.
func jsonBytes(v any) int {
	b, err := json.Marshal(v)
	if err != nil {
		return 0
	}
	return len(b)
}

// clampToolResult truncates content to the policy's per-result cap, appending a
// notice so the model knows output was cut rather than silently short. Returns
// the (possibly unchanged) content and the number of bytes dropped.
//
// The cut backs off to a rune boundary: a byte-exact slice can split a
// multi-byte rune, and json.Marshal would then silently substitute U+FFFD into
// the request. Only the trailing rune is examined: validating the whole kept
// prefix would be quadratic, and a single invalid byte anywhere earlier — the
// first byte of binary execute_command output, say — would rewind the cut back
// to it and throw away everything after.
func (cp contextPolicy) clampToolResult(tool, content string) (string, int) {
	if cp.ToolResultMaxBytes <= 0 || len(content) <= cp.ToolResultMaxBytes {
		return content, 0
	}
	kept := content[:cp.ToolResultMaxBytes]
	// A rune split by the cut orphans at most UTFMax-1 bytes, so the back-off
	// is bounded. Content that is invalid at the cut for any other reason is
	// left alone rather than chased backwards.
	for i := 0; i < utf8.UTFMax-1 && len(kept) > 0; i++ {
		if r, size := utf8.DecodeLastRuneInString(kept); r != utf8.RuneError || size > 1 {
			break
		}
		kept = kept[:len(kept)-1]
	}
	dropped := len(content) - len(kept)
	return kept + fmt.Sprintf(
		"\n... (truncated: %d of %d bytes from %s omitted; narrow the request or use pagination to see the rest)",
		dropped, len(content), tool), dropped
}

// elisionStub is the placeholder left in history in place of an elided result
// whose output could not be spilled to disk. It names the tool and the
// reclaimed size so the model can decide whether to re-run rather than assuming
// the tool returned nothing.
func elisionStub(tool string, size int) string {
	return fmt.Sprintf(
		"[earlier %s result elided to reclaim context — %d bytes. Re-run the tool if you still need this output.]",
		tool, size)
}

// elisionStubSpilled is the placeholder for a result whose full output was
// written to path. Every elided entry carries one of these for the rest of the
// run, so it stays terse: read_file's own description covers pagination, and
// re-running the tool is not worth suggesting once the output is on disk.
func elisionStubSpilled(tool string, size int, path string) string {
	return fmt.Sprintf("[earlier %s result elided — %d bytes saved to %s; read_file to recover.]",
		tool, size, path)
}

// resultSpiller persists elided tool output. Path is separate from Write so
// selectForElision can size a stub against the path it would use before
// committing to the write, and skip entries too small to be worth eliding
// without leaving an orphan file behind.
type resultSpiller interface {
	// Path is pure and deterministic: the same seq always maps to the same
	// location.
	Path(seq int) string
	// Write persists content at a path previously returned by Path, creating
	// parent directories as needed.
	Write(path, content string) error
}

// spillUnavailable reports why an elided result could not be read back again,
// or "" when it can. Spilling is only worth doing if the stub's instruction is
// actionable: read_file has to survive this run's tool policy, and the spill
// directory has to sit under a root read_file will open. When it isn't, the
// caller keeps the destructive placeholder rather than pointing the model at a
// file it cannot reach.
func spillUnavailable(dir string, tools []ToolDef) string {
	found := false
	for _, t := range tools {
		if t.Name == ToolReadFile {
			found = true
			break
		}
	}
	if !found {
		return ToolReadFile + " is not available to this run"
	}
	if !underAnyPrefix(filepath.Clean(dir), readableRoots) {
		return fmt.Sprintf("%s is outside the roots %s can read (%s)",
			dir, ToolReadFile, strings.Join(readableRoots, ", "))
	}
	return ""
}

// dirSpiller writes each elided result to its own file under Dir.
type dirSpiller struct {
	Dir string
}

// Path names the file for the seq-th ledger entry. The filename is built
// entirely from the ledger index — no tool name, no call ID, nothing the model
// or an MCP server can influence — so it cannot traverse out of Dir by
// construction rather than by sanitizing. The tool a file came from is recorded
// where it is actually read: the stub that replaces the result, and the elision
// log event.
func (s dirSpiller) Path(seq int) string {
	return filepath.Join(s.Dir, fmt.Sprintf("%04d.txt", seq))
}

// Write persists content. Permissions match the detailed logger's (0o700/0o600):
// tool output can carry whatever a command echoed, so it stays readable only by
// the agent container's own user.
func (s dirSpiller) Write(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

// ledgerEntry records one tool result that was handed to the provider.
type ledgerEntry struct {
	callID string
	tool   string
	bytes  int
	round  int
	elided bool
	// content is the result as inserted. Held so elision can spill it; it is
	// the same string the provider already retains, so this costs a reference
	// rather than a copy, and is released once the entry is elided.
	content string
}

// toolResultLedger tracks the live byte cost of every tool result inserted into
// the conversation, in insertion order. It lets the loop decide *what* to elide
// without knowing anything about a provider's message representation — the
// provider only has to honour a callID→replacement map.
type toolResultLedger struct {
	entries []ledgerEntry
	live    int
	// spill persists elided output. Nil leaves elision destructive, which is
	// the behaviour when no spill directory is configured.
	spill resultSpiller
	// spillFailed suppresses repeat logging once a write has failed — a
	// read-only or absent /workspace fails identically for every entry.
	spillFailed bool
}

// add records a tool result inserted at the given round. round is load-bearing,
// not just diagnostic: protectedCutoff uses it to keep the current round's
// results out of the elision window. Callers must pass a non-decreasing round.
func (l *toolResultLedger) add(callID, tool, content string, round int) {
	l.entries = append(l.entries, ledgerEntry{
		callID:  callID,
		tool:    tool,
		bytes:   len(content),
		round:   round,
		content: content,
	})
	l.live += len(content)
}

// liveBytes is the current tool-result contribution to every outgoing request.
func (l *toolResultLedger) liveBytes() int { return l.live }

// protectedCutoff returns the exclusive index below which entries may be
// elided. Two windows are off-limits, and the stricter one wins:
//
//   - the newest KeepRecent entries, which the model is actively reasoning over;
//   - every entry from the most recent round.
//
// The second window is not implied by the first. A single round can emit more
// parallel tool calls than KeepRecent, and the loop records all of them before
// asking for an elision pass — so a KeepRecent window alone would hand the
// model an "elided, re-run the tool" stub for a call it made moments ago and
// has never seen answered. That invites a re-request loop that burns rounds
// against maxToolIterations. Results become eligible only once the model has
// had a turn to read them.
func (l *toolResultLedger) protectedCutoff(cp contextPolicy) int {
	if len(l.entries) == 0 {
		return 0
	}
	cutoff := len(l.entries) - cp.KeepRecent

	// Rounds are non-decreasing in insertion order, so the newest round is a
	// contiguous suffix — walk back over it.
	newest := l.entries[len(l.entries)-1].round
	roundStart := len(l.entries)
	for roundStart > 0 && l.entries[roundStart-1].round == newest {
		roundStart--
	}
	if roundStart < cutoff {
		cutoff = roundStart
	}
	return cutoff
}

// selectForElision picks the oldest results to elide until live bytes fall to
// the policy's low-water mark, never touching the protected window (see
// protectedCutoff). It mutates the ledger to reflect the new sizes and returns
// the callID→stub map to hand to the provider, plus the bytes reclaimed.
//
// HistoryLowBytes is a target, not a guarantee: the protected window and the
// stubs' own size can put it out of reach. Cache thrashing is prevented not
// by always reaching the mark but by never eliding the same entry twice — a
// pass over unchanged history returns nothing, so the only way elision re-fires
// is when new results have actually arrived.
//
// Returns a nil map when nothing is eligible, which the caller must treat as
// "do not rewrite history" — an empty rewrite would still cost a cache write.
func (l *toolResultLedger) selectForElision(cp contextPolicy) (map[string]string, int) {
	if !cp.elisionEnabled() || l.live <= cp.HistoryBudgetBytes {
		return nil, 0
	}

	cutoff := l.protectedCutoff(cp)
	if cutoff <= 0 {
		return nil, 0
	}

	replacements := make(map[string]string)
	reclaimed := 0
	for i := 0; i < cutoff && l.live > cp.HistoryLowBytes; i++ {
		e := &l.entries[i]
		if e.elided {
			continue
		}

		var path string
		stub := elisionStub(e.tool, e.bytes)
		if l.spill != nil {
			path = l.spill.Path(i)
			stub = elisionStubSpilled(e.tool, e.bytes, path)
		}
		// Eliding a result that is already smaller than its own stub would
		// grow the request rather than shrink it. Checked before the write so
		// a skipped entry leaves no orphan file.
		if len(stub) >= e.bytes {
			continue
		}

		if l.spill != nil {
			if err := l.spill.Write(path, e.content); err != nil {
				if !l.spillFailed {
					l.spillFailed = true
					log.Printf("WARNING: cannot spill elided tool output to %s: %v — "+
						"elision continues without it and elided results are unrecoverable", path, err)
				}
				// Fall back to the bare stub. It is not necessarily shorter
				// than the spilled one (a short spill dir makes it longer), so
				// the size guard has to be re-applied.
				stub = elisionStub(e.tool, e.bytes)
				if len(stub) >= e.bytes {
					continue
				}
			}
		}

		replacements[e.callID] = stub
		reclaimed += e.bytes - len(stub)
		l.live -= e.bytes - len(stub)
		e.elided = true
		e.bytes = len(stub)
		// The provider is about to drop its reference to the original; release
		// ours so a long run does not retain every elided result.
		e.content = ""
	}

	if len(replacements) == 0 {
		return nil, 0
	}
	return replacements, reclaimed
}
