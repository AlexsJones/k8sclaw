package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// useTempIPC points the gate loop at a temp directory so tests never touch
// the pod's real /ipc mount.
func useTempIPC(t *testing.T) (gate, output string) {
	t.Helper()
	root := t.TempDir()
	gate = filepath.Join(root, "gate")
	output = filepath.Join(root, "output")
	for _, d := range []string{gate, output} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	origGate, origOutput, origDone := gateDir, ipcOutputDir, ipcDoneFile
	gateDir, ipcOutputDir, ipcDoneFile = gate, output, filepath.Join(root, "done")
	t.Cleanup(func() {
		gateDir, ipcOutputDir, ipcDoneFile = origGate, origOutput, origDone
	})
	return gate, output
}

func writeVerdict(t *testing.T, dir string, v ipcGateVerdict) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal verdict: %v", err)
	}
	path := filepath.Join(dir, "verdict-"+itoa(v.Attempt)+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// ── waitForGateVerdict ─────────────────────────────────────────────────────

func TestWaitForGateVerdict_ReturnsTheMatchingVerdict(t *testing.T) {
	gate, _ := useTempIPC(t)
	writeVerdict(t, gate, ipcGateVerdict{Attempt: 2, Action: "continue", Reason: "nope"})

	got, err := waitForGateVerdict(context.Background(), 2, time.Second)
	if err != nil {
		t.Fatalf("waitForGateVerdict: %v", err)
	}
	if got.Action != "continue" || got.Reason != "nope" {
		t.Errorf("got %+v, want the attempt-2 retry verdict", got)
	}
}

// A verdict for an attempt that is already resolved must not be consumed: the
// runner is parked on a later attempt and acting on the older decision would
// run an attempt nobody asked for.
func TestWaitForGateVerdict_IgnoresAnAlreadyConsumedAttempt(t *testing.T) {
	gate, _ := useTempIPC(t)
	writeVerdict(t, gate, ipcGateVerdict{Attempt: 1, Action: "continue", Reason: "stale"})

	_, err := waitForGateVerdict(context.Background(), 2, 400*time.Millisecond)
	if err == nil {
		t.Fatal("attempt 2 consumed attempt 1's verdict")
	}
	if !strings.Contains(err.Error(), "no verdict within") {
		t.Errorf("err = %v, want the park budget to expire", err)
	}
}

func TestWaitForGateVerdict_StopSentinelReleasesThePark(t *testing.T) {
	gate, _ := useTempIPC(t)
	if err := os.WriteFile(filepath.Join(gate, gateStopName), []byte("stop"), 0o644); err != nil {
		t.Fatalf("write stop: %v", err)
	}

	if _, err := waitForGateVerdict(context.Background(), 1, 10*time.Second); err == nil {
		t.Fatal("the stop sentinel did not release the park")
	}
}

func TestWaitForGateVerdict_DoneSentinelReleasesThePark(t *testing.T) {
	useTempIPC(t)
	if err := os.WriteFile(ipcDoneFile, []byte("done"), 0o644); err != nil {
		t.Fatalf("write done: %v", err)
	}

	if _, err := waitForGateVerdict(context.Background(), 1, 10*time.Second); err == nil {
		t.Fatal("/ipc/done did not release the park")
	}
}

func TestWaitForGateVerdict_CancelledContextReturns(t *testing.T) {
	useTempIPC(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := waitForGateVerdict(ctx, 1, time.Minute); err == nil {
		t.Fatal("a cancelled context must not park")
	}
}

// ── driveInPlaceGate: the park/continue state machine ─────────────────────────

// The load-bearing property: the verdict arrives as a turn on the *existing*
// conversation, so the model corrects work it can still see. A successor run
// would have neither the tool results nor the reasoning that produced them.
func TestDriveInPlaceGate_InjectsTheVerdictOnTheLiveConversation(t *testing.T) {
	gate, output := useTempIPC(t)

	p := &mockProvider{
		name:  "mock",
		model: "m",
		turns: []ChatResult{
			{Text: "first draft", InputTokens: 10, OutputTokens: 3},
			{Text: "corrected", InputTokens: 12, OutputTokens: 4},
		},
	}

	// Deliver the verdict as soon as the runner parks: it publishes the
	// attempt result first, so the result file is the signal that it is
	// waiting.
	go func() {
		waitForFile(t, filepath.Join(output, "result-1.json"), 2*time.Second)
		writeVerdict(t, gate, ipcGateVerdict{
			Attempt: 1, Action: "continue", Reason: "missing the edge case",
			Output: "FAIL: empty input", MaxAttempts: 3,
		})
		// Approve the correction, so the chain ends on a verdict rather than
		// on the park budget expiring.
		waitForFile(t, filepath.Join(output, "result-2.json"), 2*time.Second)
		writeVerdict(t, gate, ipcGateVerdict{Attempt: 2, Action: "stop"})
	}()

	text, in, out, _, err := driveInPlaceGate(context.Background(), p, 3, time.Minute, 5*time.Second)
	if err != nil {
		t.Fatalf("driveInPlaceGate: %v", err)
	}
	if text != "corrected" {
		t.Errorf("text = %q, want the second attempt's answer", text)
	}
	if in != 22 || out != 7 {
		t.Errorf("tokens in=%d out=%d, want the sum across both attempts (22/7)", in, out)
	}
	if p.chatCalls != 2 {
		t.Errorf("chat calls = %d, want 2: one provider, two attempts", p.chatCalls)
	}
	if len(p.userMessages) != 1 {
		t.Fatalf("injected %d user messages, want 1", len(p.userMessages))
	}
	card := p.userMessages[0]
	for _, want := range []string{"missing the edge case", "FAIL: empty input", "Attempt 2 of 3"} {
		if !strings.Contains(card, want) {
			t.Errorf("retry card is missing %q:\n%s", want, card)
		}
	}
}

// The last permitted attempt has nothing to wait for, so it must not park —
// the controller resolves the gate terminally either way.
func TestDriveInPlaceGate_DoesNotParkOnTheLastAttempt(t *testing.T) {
	_, output := useTempIPC(t)
	p := &mockProvider{name: "mock", model: "m", turns: []ChatResult{{Text: "only", InputTokens: 1}}}

	start := time.Now()
	text, _, _, _, err := driveInPlaceGate(context.Background(), p, 1, time.Minute, 5*time.Second)
	if err != nil {
		t.Fatalf("driveInPlaceGate: %v", err)
	}
	if text != "only" {
		t.Errorf("text = %q, want %q", text, "only")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("parked for %s on the last permitted attempt", elapsed)
	}
	if _, err := os.Stat(filepath.Join(output, "result-1.json")); err == nil {
		t.Error("published an attempt result for an attempt nothing will judge")
	}
}

// A park that never receives a verdict has to end: a pod that holds forever is
// the failure mode this design exists to avoid.
func TestDriveInPlaceGate_ParkTimeoutFinishesWithWhatItHas(t *testing.T) {
	useTempIPC(t)
	p := &mockProvider{name: "mock", model: "m", turns: []ChatResult{{Text: "unjudged", InputTokens: 1}}}

	text, _, _, _, err := driveInPlaceGate(context.Background(), p, 5, time.Minute, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("driveInPlaceGate: %v", err)
	}
	if text != "unjudged" {
		t.Errorf("text = %q, want the attempt already produced", text)
	}
}

// A "stop" verdict ends the chain on the attempt that was judged.
func TestDriveInPlaceGate_StopVerdictEndsTheChain(t *testing.T) {
	gate, output := useTempIPC(t)
	p := &mockProvider{name: "mock", model: "m", turns: []ChatResult{{Text: "approved answer", InputTokens: 5}}}

	go func() {
		waitForFile(t, filepath.Join(output, "result-1.json"), 2*time.Second)
		writeVerdict(t, gate, ipcGateVerdict{Attempt: 1, Action: "stop"})
	}()

	text, _, _, _, err := driveInPlaceGate(context.Background(), p, 5, time.Minute, 5*time.Second)
	if err != nil {
		t.Fatalf("driveInPlaceGate: %v", err)
	}
	if text != "approved answer" {
		t.Errorf("text = %q, want the judged attempt's answer", text)
	}
	if len(p.userMessages) != 0 {
		t.Errorf("a stop verdict injected %d turns, want 0", len(p.userMessages))
	}
}

// A failed attempt ends the chain rather than parking: the gate judges agent
// output and there is none to judge.
func TestDriveInPlaceGate_LoopErrorEndsTheChain(t *testing.T) {
	_, output := useTempIPC(t)
	p := &mockProvider{
		name:    "mock",
		model:   "m",
		turns:   []ChatResult{{}},
		turnErr: []error{context.DeadlineExceeded},
	}

	if _, _, _, _, err := driveInPlaceGate(context.Background(), p, 5, time.Minute, 5*time.Second); err == nil {
		t.Fatal("a failed attempt must surface its error, not park")
	}
	if _, err := os.Stat(filepath.Join(output, "result-1.json")); err == nil {
		t.Error("published an attempt result for an attempt that failed")
	}
}

// ── the attempt marker the controller reads ──────────────────────────────────

func TestPublishAttemptResult_WritesTheResultFile(t *testing.T) {
	_, output := useTempIPC(t)

	res := ipcAttemptResult{Attempt: 3, Status: "success", Response: "hello"}
	res.Metrics.InputTokens = 7
	publishAttemptResult(res)

	data, err := os.ReadFile(filepath.Join(output, "result-3.json"))
	if err != nil {
		t.Fatalf("read attempt result: %v", err)
	}
	var got ipcAttemptResult
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Attempt != 3 || got.Response != "hello" || got.Metrics.InputTokens != 7 {
		t.Errorf("got %+v, want the published attempt verbatim", got)
	}
}

// ── the retry card ──────────────────────────────────────────────────────────

func TestBuildRetryCard_FallsBackWhenTheGateGaveNoReason(t *testing.T) {
	card := buildRetryCard(&ipcGateVerdict{Attempt: 1, Action: "continue"}, 2)
	if !strings.Contains(card, "without giving a reason") {
		t.Errorf("card leaves the model guessing:\n%s", card)
	}
	if strings.Contains(card, "### Gate Output") {
		t.Error("an empty gate output must not get a heading")
	}
	if !strings.Contains(card, "## Attempt 2") {
		t.Errorf("card does not say which attempt this is:\n%s", card)
	}
}

func TestGateInPlaceEnv_ReadsTheEnv(t *testing.T) {
	t.Setenv("GATE_IN_PLACE_ENABLED", "true")
	if !gateInPlaceEnabled() {
		t.Error("GATE_IN_PLACE_ENABLED=true must enable parking")
	}
	t.Setenv("GATE_IN_PLACE_ENABLED", "")
	if gateInPlaceEnabled() {
		t.Error("an unset env must keep the single-attempt path")
	}
}

func TestParkTimeout_HonoursTheOverride(t *testing.T) {
	t.Setenv("GATE_PARK_TIMEOUT", "90s")
	if got := parkTimeout(); got != 90*time.Second {
		t.Errorf("parkTimeout = %s, want 90s", got)
	}
	t.Setenv("GATE_PARK_TIMEOUT", "not-a-duration")
	if got := parkTimeout(); got != defaultParkTimeout {
		t.Errorf("parkTimeout = %s, want the default on a malformed value", got)
	}
}

func waitForFile(t *testing.T, path string, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("timed out waiting for %s", path)
}
