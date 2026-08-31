package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// In-place gate retry. A run with both a response gate and lifecycle.retry
// parks instead of exiting: it writes the attempt's result, holds the live
// LLMProvider, and waits for a verdict on /ipc/gate/. A "continue" verdict
// becomes the next user turn and the agent loop is re-entered, so the model
// corrects work it can still see — its reasoning, its tool calls and results,
// and the files it wrote to /workspace.
//
// Second consumer of the runPromptServiceLoop pattern: poll an /ipc directory,
// act on new files, exit on a sentinel. Shares buildLLMProvider with it.

// Paths are variables, not constants, so tests can point the loop at a temp
// directory instead of the pod's /ipc mount.
var (
	gateDir      = "/ipc/gate"
	ipcOutputDir = "/ipc/output"
	ipcDoneFile  = "/ipc/done"
)

const (
	gateStopName  = "stop"
	gatePollEvery = 250 * time.Millisecond

	// defaultParkTimeout bounds one park. The controller is the authority on
	// how long AwaitingGate may last; this is the backstop for a controller
	// that dies mid-chain.
	defaultParkTimeout = 30 * time.Minute

	// attemptMarkerStart/End bracket the per-attempt result on stdout. The
	// controller reads attempts from pod logs because it cannot reach /ipc. A
	// parked pod never terminates, so this marker is an attempt's only
	// completion signal.
	attemptMarkerStart = "__SYMPOZIUM_ATTEMPT__"
	attemptMarkerEnd   = "__SYMPOZIUM_ATTEMPT_END__"
)

// gateInPlaceEnabled reports whether the controller asked this run to park.
// Set only for a Job-backend run with a gate hook and gate retry; everything
// else keeps the single-attempt path and successor-clone retry behind it.
func gateInPlaceEnabled() bool {
	return strings.EqualFold(os.Getenv("GATE_IN_PLACE_ENABLED"), "true")
}

// parkTimeout returns how long one park may last before the runner gives up
// and finishes the run with the attempt it already has.
func parkTimeout() time.Duration {
	if v := os.Getenv("GATE_PARK_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultParkTimeout
}

// inPlaceMaxAttempts mirrors lifecycle.retry.maxAttempts so the runner skips a
// final park it would only time out of. The controller still enforces it.
func inPlaceMaxAttempts() int {
	if n := envInt("GATE_IN_PLACE_MAX_ATTEMPTS", 0); n > 0 {
		return n
	}
	return 0
}

// ipcAttemptResult mirrors ipc.AttemptResult locally, as prompt_service.go
// mirrors the prompt protocol: the runner stays independent of the bridge
// package, and field names are the contract.
type ipcAttemptResult struct {
	Attempt  int    `json:"attempt"`
	Status   string `json:"status"`
	Response string `json:"response,omitempty"`
	Error    string `json:"error,omitempty"`
	Metrics  struct {
		DurationMs   int64 `json:"durationMs"`
		InputTokens  int   `json:"inputTokens"`
		OutputTokens int   `json:"outputTokens"`
		ToolCalls    int   `json:"toolCalls"`
	} `json:"metrics"`
}

// ipcGateVerdict mirrors ipc.GateVerdict locally.
type ipcGateVerdict struct {
	Attempt     int    `json:"attempt"`
	Action      string `json:"action"`
	Reason      string `json:"reason,omitempty"`
	Output      string `json:"output,omitempty"`
	MaxAttempts int    `json:"maxAttempts,omitempty"`
}

// attemptTally accumulates one attempt's usage into the run totals.
type attemptTally struct {
	inputTokens  int
	outputTokens int
	toolCalls    int
}

// runInPlaceGateLoop drives a gated run across attempts on one live provider.
//
// Returns the final attempt's response plus the run's cumulative token and
// tool-call counts, matching the single-attempt path so main is unchanged.
// Per-attempt figures reach the CR through the markers instead; that is what
// maxChainTokens sums.
//
// ctx must NOT carry the run timeout — parked time is not run time. Each
// attempt gets attemptTimeout of its own.
func runInPlaceGateLoop(
	ctx context.Context, deps promptServiceDeps, attemptTimeout time.Duration,
) (string, int, int, int, error) {
	if err := os.MkdirAll(gateDir, 0o755); err != nil {
		return "", 0, 0, 0, fmt.Errorf("creating %s: %w", gateDir, err)
	}

	p, cleanup, err := buildLLMProvider(deps)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("build provider: %w", err)
	}
	defer cleanup()

	return driveInPlaceGate(ctx, p, inPlaceMaxAttempts(), attemptTimeout, parkTimeout())
}

// driveInPlaceGate is runInPlaceGateLoop with the provider already built: one
// provider, many attempts, a park between each.
func driveInPlaceGate(
	ctx context.Context, p LLMProvider,
	maxAttempts int, attemptTimeout, park time.Duration,
) (string, int, int, int, error) {
	log.Printf("gate retry: parking between attempts (maxAttempts=%d attemptTimeout=%s parkTimeout=%s)",
		maxAttempts, attemptTimeout, park)

	var total attemptTally
	var lastText string

	for attempt := 1; ; attempt++ {
		attemptCtx, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
		text, inTok, outTok, calls, loopErr := runAgentLoop(attemptCtx, p)
		cancelAttempt()

		total.inputTokens += inTok
		total.outputTokens += outTok
		total.toolCalls += calls
		if text != "" {
			lastText = text
		}

		// A failed attempt ends the chain rather than parking: the gate judges
		// agent output, and there is none to judge.
		if loopErr != nil {
			return lastText, total.inputTokens, total.outputTokens, total.toolCalls, loopErr
		}

		// The last permitted attempt has nothing to wait for — the controller
		// resolves the gate terminally either way.
		if maxAttempts > 0 && attempt >= maxAttempts {
			log.Printf("gate retry: attempt %d is the last permitted; not parking", attempt)
			return lastText, total.inputTokens, total.outputTokens, total.toolCalls, nil
		}

		res := ipcAttemptResult{Attempt: attempt, Status: "success", Response: text}
		res.Metrics.InputTokens = inTok
		res.Metrics.OutputTokens = outTok
		res.Metrics.ToolCalls = calls
		publishAttemptResult(res)

		verdict, err := waitForGateVerdict(ctx, attempt, park)
		if err != nil {
			// Finish with what we have rather than hold the pod. The
			// controller already has this attempt from the marker.
			log.Printf("gate retry: no verdict for attempt %d (%v); finishing with this attempt", attempt, err)
			return lastText, total.inputTokens, total.outputTokens, total.toolCalls, nil
		}

		if verdict.Action != ipcGateVerdictContinue {
			log.Printf("gate retry: verdict %q for attempt %d ends the chain", verdict.Action, attempt)
			return lastText, total.inputTokens, total.outputTokens, total.toolCalls, nil
		}

		card := buildRetryCard(verdict, attempt+1)
		p.AddUserMessage(card)
		log.Printf("gate retry: injected verdict for attempt %d, starting attempt %d (cardLen=%d)",
			attempt, attempt+1, len(card))
	}
}

// ipcGateVerdictContinue mirrors ipc.GateVerdictActionContinue. Anything else —
// "stop", or an unrecognised action — ends the chain, so a verdict the runner
// cannot interpret releases the pod instead of holding it.
const ipcGateVerdictContinue = "continue"

// publishAttemptResult writes the attempt result to /ipc/output (for the
// bridge) and prints it to stdout (for the controller). Both best-effort — the
// controller re-reads the log on every reconcile.
func publishAttemptResult(res ipcAttemptResult) {
	writeJSON(filepath.Join(ipcOutputDir, fmt.Sprintf("result-%d.json", res.Attempt)), res)
	if b, err := json.Marshal(res); err == nil {
		fmt.Fprintf(os.Stdout, "\n%s%s%s\n", attemptMarkerStart, string(b), attemptMarkerEnd)
	}
	log.Printf("gate retry: parked after attempt %d (in=%d out=%d tools=%d)",
		res.Attempt, res.Metrics.InputTokens, res.Metrics.OutputTokens, res.Metrics.ToolCalls)
}

// waitForGateVerdict blocks until the bridge writes this attempt's verdict,
// the run is told to stop, or the park budget runs out.
//
// It only reads verdict-{attempt}.json, so a stale or duplicated verdict for
// another attempt is never consumed. That is the runner's half of the
// idempotency guarantee; the controller refuses to publish the other half.
func waitForGateVerdict(ctx context.Context, attempt int, budget time.Duration) (*ipcGateVerdict, error) {
	path := filepath.Join(gateDir, fmt.Sprintf("verdict-%d.json", attempt))
	deadline := time.Now().Add(budget)

	poll := time.NewTicker(gatePollEvery)
	defer poll.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-poll.C:
			if data, err := os.ReadFile(path); err == nil {
				var v ipcGateVerdict
				if err := json.Unmarshal(data, &v); err != nil {
					return nil, fmt.Errorf("verdict for attempt %d is not valid JSON: %w", attempt, err)
				}
				if v.Attempt != attempt {
					// The controller names the file, so this should not
					// happen. Treat it as unusable rather than guess.
					return nil, fmt.Errorf("verdict file for attempt %d carries attempt %d", attempt, v.Attempt)
				}
				return &v, nil
			}
			// Either sentinel releases the pod: /ipc/gate/stop is this
			// protocol's, /ipc/done the one every sidecar already writes.
			if _, err := os.Stat(filepath.Join(gateDir, gateStopName)); err == nil {
				return nil, fmt.Errorf("stop requested while parked on attempt %d", attempt)
			}
			if _, err := os.Stat(ipcDoneFile); err == nil {
				return nil, fmt.Errorf("/ipc/done observed while parked on attempt %d", attempt)
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("no verdict within %s", budget)
			}
		}
	}
}

// buildRetryCard renders the verdict as the user turn that starts the next
// attempt. Much shorter than buildRetryTask: a successor has to be told the
// original task and shown its predecessor's output, this conversation holds
// both already.
//
// The controller bounds the gate output before publishing, so nothing is
// truncated here.
func buildRetryCard(v *ipcGateVerdict, nextAttempt int) string {
	reason := v.Reason
	if reason == "" {
		reason = "The response gate rejected that attempt without giving a reason."
	}

	header := fmt.Sprintf("## Attempt %d", nextAttempt)
	if v.MaxAttempts > 0 {
		header = fmt.Sprintf("## Attempt %d of %d", nextAttempt, v.MaxAttempts)
	}

	card := header + "\n\nYour previous response was rejected by the response gate. " +
		"Your earlier work is still here — the files you wrote and the tool results above are unchanged, " +
		"so correct that work rather than starting over.\n\n### Why It Was Rejected\n" + reason
	if v.Output != "" {
		card += "\n\n### Gate Output\n" + v.Output
	}
	return card
}
