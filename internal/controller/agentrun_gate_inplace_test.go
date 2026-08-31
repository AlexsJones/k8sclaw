package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/eventbus"
	"github.com/sympozium-ai/sympozium/internal/ipc"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

// parkedRun builds an in-place retry run sitting in AwaitingGate on `attempt`,
// with one recorded attempt per completed attempt. That is the only state
// resolveInPlaceGate is reached from.
func parkedRun(name string, retry *sympoziumv1alpha1.RetrySpec, attempt int, usagePerAttempt int) *sympoziumv1alpha1.AgentRun {
	run := gatedRun(name, retry)
	run.Status.Phase = sympoziumv1alpha1.AgentRunPhaseAwaitingGate
	run.Status.PostRunJobName = gateJobNameForAttempt(run, attempt)
	run.Status.Attempt = attempt
	run.Status.PodName = name + "-pod"
	for i := 1; i <= attempt; i++ {
		entry := sympoziumv1alpha1.AttemptStatus{Attempt: i, Result: fmt.Sprintf("attempt %d output", i)}
		if usagePerAttempt > 0 {
			entry.TokenUsage = &sympoziumv1alpha1.TokenUsage{TotalTokens: usagePerAttempt}
		}
		run.Status.Attempts = append(run.Status.Attempts, entry)
	}
	return run
}

// fakeClientWithPod builds a client that can serve the agent pod as well as
// the run. retryScheme registers only the Sympozium types, and the gate-Job
// pinning needs to read a core/v1 Pod.
func fakeClientWithPod(t *testing.T, run *sympoziumv1alpha1.AgentRun, pod *corev1.Pod) client.Client {
	t.Helper()
	scheme := retryScheme(t)
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sympoziumv1alpha1.AgentRun{}).
		WithObjects(run, pod).
		Build()
}

func attemptMarker(t *testing.T, res attemptResult) string {
	t.Helper()
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal attempt result: %v", err)
	}
	return "some log line\n" + attemptMarkerStart + string(b) + attemptMarkerEnd + "\nmore logs\n"
}

func gateVerdicts(bus *recordingEventBus) []ipc.GateVerdict {
	var out []ipc.GateVerdict
	for _, e := range bus.published {
		if !strings.HasPrefix(e.Topic, eventbus.TopicGateVerdict+".") {
			continue
		}
		var v ipc.GateVerdict
		if err := json.Unmarshal(e.Event.Data, &v); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// ── attempt marker parsing ───────────────────────────────────────────────────

func TestParseAttemptResultFromLogs_MatchesTheRequestedAttempt(t *testing.T) {
	logs := attemptMarker(t, attemptResult{Attempt: 1, Status: "success", Response: "first"}) +
		attemptMarker(t, attemptResult{Attempt: 2, Status: "success", Response: "second"})

	got := parseAttemptResultFromLogs(logs, 1, logr.Discard())
	if got == nil || got.Response != "first" {
		t.Fatalf("attempt 1: got %+v, want the attempt-1 marker", got)
	}
	got = parseAttemptResultFromLogs(logs, 2, logr.Discard())
	if got == nil || got.Response != "second" {
		t.Fatalf("attempt 2: got %+v, want the attempt-2 marker", got)
	}
	if got := parseAttemptResultFromLogs(logs, 3, logr.Discard()); got != nil {
		t.Errorf("attempt 3 has not been published; got %+v", got)
	}
}

// A pod publishing attempt 1 while the controller is waiting on attempt 2 must
// not gate the stale answer — the whole point of matching on attempt number.
func TestParseAttemptResultFromLogs_IgnoresAStaleMarker(t *testing.T) {
	logs := attemptMarker(t, attemptResult{Attempt: 1, Status: "success", Response: "stale"})
	if got := parseAttemptResultFromLogs(logs, 2, logr.Discard()); got != nil {
		t.Fatalf("got %+v, want nil: attempt 2 has not parked yet", got)
	}
}

func TestParseAttemptResultFromLogs_ClampsHostileMetrics(t *testing.T) {
	res := attemptResult{Attempt: 1, Status: "success", Response: "x"}
	res.Metrics.InputTokens = -5
	res.Metrics.OutputTokens = 1 << 30

	got := parseAttemptResultFromLogs(attemptMarker(t, res), 1, logr.Discard())
	if got == nil {
		t.Fatal("expected the marker to parse")
	}
	if got.Metrics.InputTokens != 0 || got.Metrics.OutputTokens != 0 {
		t.Errorf("a negative metric must drop the whole set, got in=%d out=%d",
			got.Metrics.InputTokens, got.Metrics.OutputTokens)
	}
}

// ── retryChainTokens: the same-CR branch ─────────────────────────────────────

func TestRetryChainTokens_SumsAttemptsOnTheSameRun(t *testing.T) {
	run := parkedRun("inplace-1", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 5}, 3, 400)
	// A stale top-level aggregate must not be added on top of the per-attempt
	// entries, or the budget trips at half the configured ceiling.
	run.Status.TokenUsage = &sympoziumv1alpha1.TokenUsage{TotalTokens: 1200}
	r, _ := retryReconciler(t, run)

	if got := r.retryChainTokens(context.Background(), run); got != 1200 {
		t.Errorf("chain tokens = %d, want 1200 (3 attempts x 400)", got)
	}
}

// The retryOf walk stays in force for successor-clone chains, which have no
// attempts[] to sum.
func TestRetryChainTokens_FallsBackToTheRetryOfWalk(t *testing.T) {
	first := gatedRun("walk-1", nil)
	first.Status.TokenUsage = &sympoziumv1alpha1.TokenUsage{TotalTokens: 300}
	second := gatedRun("walk-2", nil)
	second.Status.RetryOf = "walk-1"
	second.Status.TokenUsage = &sympoziumv1alpha1.TokenUsage{TotalTokens: 200}

	r, _ := retryReconciler(t, first, second)
	if got := r.retryChainTokens(context.Background(), second); got != 500 {
		t.Errorf("chain tokens = %d, want 500", got)
	}
}

// ── resolveInPlaceGate ────────────────────────────────────────────────────────

func TestResolveInPlaceGate_ContinuesOnTheSameRun(t *testing.T) {
	run := parkedRun("inplace-2", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}, 1, 100)
	withVerdict(run, `{"action":"retry","reason":"tests fail","response":"FAIL: 3 cases"}`)
	r, bus := retryReconciler(t, run)

	if _, err := r.resolveInPlaceGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveInPlaceGate: %v", err)
	}

	got := getRun(t, r, "inplace-2")
	if got.Status.Phase != sympoziumv1alpha1.AgentRunPhaseRunning {
		t.Errorf("phase = %q, want Running", got.Status.Phase)
	}
	if got.Status.Attempt != 2 {
		t.Errorf("status.attempt = %d, want 2", got.Status.Attempt)
	}
	if len(got.Status.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2 (the judged one plus the one just started)", len(got.Status.Attempts))
	}
	if got.Status.Attempts[0].GateVerdict != "retried" {
		t.Errorf("attempt 1 verdict = %q, want retried", got.Status.Attempts[0].GateVerdict)
	}
	if got.Status.Attempts[1].StartedAt == nil {
		t.Error("attempt 2 has no startedAt, so the run timeout would re-anchor to the run start")
	}
	// The annotation must not survive: resolveGate reads it unconditionally, so
	// a leftover verdict would resolve attempt 2 against attempt 1's answer.
	if _, ok := got.Annotations["sympozium.ai/gate-verdict"]; ok {
		t.Error("gate-verdict annotation survived the handover to the next attempt")
	}

	verdicts := gateVerdicts(bus)
	if len(verdicts) != 1 {
		t.Fatalf("published %d gate verdicts, want 1", len(verdicts))
	}
	if verdicts[0].Action != ipc.GateVerdictActionContinue || verdicts[0].Attempt != 1 {
		t.Errorf("verdict = %+v, want a retry for attempt 1", verdicts[0])
	}
	if verdicts[0].Output != "FAIL: 3 cases" || verdicts[0].Reason != "tests fail" {
		t.Errorf("verdict lost the gate's feedback: %+v", verdicts[0])
	}
}

// No successor CR is created on this path: that is the whole difference from
// the clone-based retry.
func TestResolveInPlaceGate_CreatesNoSuccessor(t *testing.T) {
	run := parkedRun("inplace-3", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}, 1, 100)
	withVerdict(run, `{"action":"retry","reason":"again"}`)
	r, _ := retryReconciler(t, run)

	if _, err := r.resolveInPlaceGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveInPlaceGate: %v", err)
	}

	var list sympoziumv1alpha1.AgentRunList
	if err := r.List(context.Background(), &list); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("run count = %d, want 1: in-place retry must not clone a successor", len(list.Items))
	}
}

func TestResolveInPlaceGate_MaxAttemptsStopsTheChain(t *testing.T) {
	run := parkedRun("inplace-4", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2}, 2, 100)
	withVerdict(run, `{"action":"retry","reason":"still wrong"}`)
	r, bus := retryReconciler(t, run)

	if _, err := r.resolveInPlaceGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveInPlaceGate: %v", err)
	}

	got := getRun(t, r, "inplace-4")
	if got.Status.GateVerdict != "retries-exhausted" {
		t.Errorf("gateVerdict = %q, want retries-exhausted", got.Status.GateVerdict)
	}
	if got.Status.Phase != sympoziumv1alpha1.AgentRunPhaseSucceeded {
		t.Errorf("phase = %q, want Succeeded (the run resolves, the chain does not continue)", got.Status.Phase)
	}
	for _, v := range gateVerdicts(bus) {
		if v.Action == ipc.GateVerdictActionContinue {
			t.Error("published a retry verdict past maxAttempts")
		}
	}
}

// The disqualifying case from the design: with one CR and no per-attempt
// accounting the budget would never trip.
func TestResolveInPlaceGate_MaxChainTokensStopsTheChain(t *testing.T) {
	run := parkedRun("inplace-5", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 9, MaxChainTokens: 500}, 2, 300)
	withVerdict(run, `{"action":"retry","reason":"still wrong"}`)
	r, _ := retryReconciler(t, run)

	if _, err := r.resolveInPlaceGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveInPlaceGate: %v", err)
	}

	got := getRun(t, r, "inplace-5")
	if got.Status.GateVerdict != "retries-exhausted" {
		t.Errorf("gateVerdict = %q, want retries-exhausted at 600/500 chain tokens", got.Status.GateVerdict)
	}
	if got.Status.Attempt != 2 {
		t.Errorf("status.attempt = %d, want 2: the chain must not advance", got.Status.Attempt)
	}
}

func TestResolveInPlaceGate_ApproveReleasesThePod(t *testing.T) {
	run := parkedRun("inplace-6", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}, 1, 100)
	withVerdict(run, `{"action":"approve","reason":"looks good"}`)
	r, bus := retryReconciler(t, run)

	if _, err := r.resolveInPlaceGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveInPlaceGate: %v", err)
	}

	got := getRun(t, r, "inplace-6")
	if got.Status.Phase != sympoziumv1alpha1.AgentRunPhaseSucceeded {
		t.Errorf("phase = %q, want Succeeded", got.Status.Phase)
	}
	if got.Status.GateVerdict != "approved" {
		t.Errorf("gateVerdict = %q, want approved", got.Status.GateVerdict)
	}
	if got.Status.Attempts[0].GateVerdict != "approved" {
		t.Errorf("the attempt timeline was left unexplained: %+v", got.Status.Attempts[0])
	}

	var stops int
	for _, v := range gateVerdicts(bus) {
		if v.Action == ipc.GateVerdictActionStop {
			stops++
		}
	}
	if stops != 1 {
		t.Errorf("published %d stop verdicts, want 1: a parked pod that is never released hangs until its deadline", stops)
	}
}

// A stale browser tab that still shows attempt 1's controls must not resolve
// attempt 2 with a decision made about output nobody is looking at any more.
func TestResolveInPlaceGate_IgnoresAVerdictForASupersededAttempt(t *testing.T) {
	run := parkedRun("inplace-7", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 5}, 2, 100)
	withVerdict(run, `{"action":"approve","reason":"stale tab"}`)
	run.Annotations[gateVerdictAttemptAnnotation] = "1"
	r, bus := retryReconciler(t, run)

	if _, err := r.resolveInPlaceGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveInPlaceGate: %v", err)
	}

	got := getRun(t, r, "inplace-7")
	if got.Status.Phase != sympoziumv1alpha1.AgentRunPhaseAwaitingGate {
		t.Errorf("phase = %q, want AwaitingGate: the stale verdict must not resolve the run", got.Status.Phase)
	}
	if _, ok := got.Annotations["sympozium.ai/gate-verdict"]; ok {
		t.Error("the stale verdict was left in place and will be re-read next reconcile")
	}
	if len(gateVerdicts(bus)) != 0 {
		t.Error("a stale verdict reached the pod")
	}
}

// A gate hook's verdict carries no attempt stamp — it comes from the Job
// judging this very attempt, so it always applies.
func TestVerdictAppliesToAttempt_UnstampedVerdictAlwaysApplies(t *testing.T) {
	run := parkedRun("inplace-8", nil, 3, 0)
	if !verdictAppliesToAttempt(run, 3) {
		t.Error("an unstamped hook verdict must apply to the current attempt")
	}
	run.Annotations[gateVerdictAttemptAnnotation] = "3"
	if !verdictAppliesToAttempt(run, 3) {
		t.Error("a matching stamp must apply")
	}
	run.Annotations[gateVerdictAttemptAnnotation] = "2"
	if verdictAppliesToAttempt(run, 3) {
		t.Error("a stamp for an earlier attempt must be rejected")
	}
}

// ── attempt bookkeeping ──────────────────────────────────────────────────────

func TestSumAttemptUsage_AbsentWhenNothingWasReported(t *testing.T) {
	if got := sumAttemptUsage([]sympoziumv1alpha1.AttemptStatus{{Attempt: 1}}); got != nil {
		t.Errorf("got %+v, want nil: absence, never a real-looking zero", got)
	}
	got := sumAttemptUsage([]sympoziumv1alpha1.AttemptStatus{
		{Attempt: 1, TokenUsage: &sympoziumv1alpha1.TokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15, ToolCalls: 2}},
		{Attempt: 2, TokenUsage: &sympoziumv1alpha1.TokenUsage{InputTokens: 20, OutputTokens: 5, TotalTokens: 25, ToolCalls: 1}},
	})
	if got == nil || got.TotalTokens != 40 || got.ToolCalls != 3 {
		t.Errorf("got %+v, want the chain aggregate (40 tokens, 3 tool calls)", got)
	}
}

func TestEnsureAttemptEntry_IsIdempotent(t *testing.T) {
	run := &sympoziumv1alpha1.AgentRun{}
	first := ensureAttemptEntry(run, 1)
	first.Result = "one"
	again := ensureAttemptEntry(run, 1)
	if len(run.Status.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1: re-recording an attempt must not append", len(run.Status.Attempts))
	}
	if again.Result != "one" {
		t.Errorf("the existing entry was replaced instead of returned: %+v", again)
	}
}

func TestBoundAttemptText_MarksTheCut(t *testing.T) {
	long := strings.Repeat("x", attemptTextMaxChars+100)
	got := boundAttemptText(long)
	if len(got) <= attemptTextMaxChars {
		t.Fatal("expected a truncation notice to be appended")
	}
	if !strings.Contains(got, "truncated") {
		t.Error("a fragment must not read as the whole thing")
	}
	if short := boundAttemptText("fine"); short != "fine" {
		t.Errorf("short text was altered: %q", short)
	}
}

// Each attempt needs its own gate Job: a Job's spec is immutable, so reusing
// the name would leave the second attempt ungated.
func TestGateJobNameForAttempt_IsPerAttempt(t *testing.T) {
	run := gatedRun("demo", nil)
	if a, b := gateJobNameForAttempt(run, 1), gateJobNameForAttempt(run, 2); a == b {
		t.Errorf("attempts 1 and 2 share the gate Job name %q", a)
	}
	// A Job name becomes a label value on the pods it creates, so it must fit
	// in 63 characters or the Job is rejected outright.
	long := gatedRun(strings.Repeat("n", 250), nil)
	got := gateJobNameForAttempt(long, 7)
	if len(got) > jobNameMaxLen {
		t.Errorf("name is %d chars, over the %d label-value limit", len(got), jobNameMaxLen)
	}
	if !strings.HasSuffix(got, "-gate-7") {
		t.Errorf("name %q lost its attempt suffix to truncation", got)
	}
}

// ── workspace retention ──────────────────────────────────────────────────────

func TestWorkspaceNeedsPVC(t *testing.T) {
	cases := []struct {
		name      string
		lifecycle *sympoziumv1alpha1.LifecycleHooks
		want      bool
	}{
		{"no lifecycle", nil, false},
		{"postRun hooks", &sympoziumv1alpha1.LifecycleHooks{
			PostRun: []sympoziumv1alpha1.LifecycleHookContainer{{Name: "h", Image: "i"}},
		}, true},
		// Retry without a gate hook can never retry — gate: true is valid only
		// on postRun — so provisioning storage for it would buy nothing.
		{"retry without a gate hook", &sympoziumv1alpha1.LifecycleHooks{
			Retry: &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2},
		}, false},
		{"neither", &sympoziumv1alpha1.LifecycleHooks{GateDefault: "block"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := &sympoziumv1alpha1.AgentRun{Spec: sympoziumv1alpha1.AgentRunSpec{Lifecycle: tc.lifecycle}}
			if got := workspaceNeedsPVC(run); got != tc.want {
				t.Errorf("workspaceNeedsPVC = %v, want %v", got, tc.want)
			}
		})
	}
}

// The retried attempt has to read its own earlier work back, so an emptyDir
// workspace would defeat the whole exercise. An in-place run always declares
// a gate hook, which is a postRun hook, so it always lands on the PVC branch —
// this pins that the two conditions really do coincide.
func TestBuildVolumes_InPlaceRunGetsAWorkspacePVC(t *testing.T) {
	r := &AgentRunReconciler{}
	run := gatedRun("vol-1", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3})
	if !gateInPlaceEnabled(run) {
		t.Fatal("fixture is not an in-place retry run")
	}

	ws := findVolume(t, r.buildVolumes(run, false, nil, nil), "workspace")
	if ws.PersistentVolumeClaim == nil {
		t.Fatalf("workspace = %+v, want a PVC", ws.VolumeSource)
	}
	if ws.PersistentVolumeClaim.ClaimName != "vol-1-workspace" {
		t.Errorf("claim = %q, want vol-1-workspace", ws.PersistentVolumeClaim.ClaimName)
	}
}

func TestBuildVolumes_WorkspaceStaysEmptyDirWithoutHooks(t *testing.T) {
	r := &AgentRunReconciler{}
	run := &sympoziumv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "vol-2", Namespace: "default"}}

	ws := findVolume(t, r.buildVolumes(run, false, nil, nil), "workspace")
	if ws.EmptyDir == nil {
		t.Errorf("workspace = %+v, want an emptyDir", ws.VolumeSource)
	}
}

// Every path that can retry runs behind a postRun gate hook, so a run that can
// retry always has a PVC. If a future change makes retry reachable without one
// (failure-retry, say), this fails and the PVC condition has to grow with it.
func TestGateInPlaceAlwaysImpliesAWorkspacePVC(t *testing.T) {
	run := gatedRun("implies-1", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2})
	if gateInPlaceEnabled(run) && !workspaceNeedsPVC(run) {
		t.Error("a run can park for a gate without a persistent workspace")
	}

	// The same holds for the successor-clone path, which is gated by
	// hasResponseGateHook at every resolveGate call site.
	if hasResponseGateHook(run) && !workspaceNeedsPVC(run) {
		t.Error("a gated run has no persistent workspace")
	}
}

func findVolume(t *testing.T, vols []corev1.Volume, name string) corev1.Volume {
	t.Helper()
	for _, v := range vols {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("no %q volume in %d volumes", name, len(vols))
	return corev1.Volume{}
}

// ── pod wiring ───────────────────────────────────────────────────────────────

func TestGateInPlaceEnabled_NeedsBothAGateAndRetry(t *testing.T) {
	if gateInPlaceEnabled(gatedRun("a", nil)) {
		t.Error("a gate with no retry has nothing to continue into")
	}
	noGate := gatedRun("b", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2})
	noGate.Spec.Lifecycle.PostRun[0].Gate = false
	if gateInPlaceEnabled(noGate) {
		t.Error("retry with no gate has nothing to park for")
	}
	if !gateInPlaceEnabled(gatedRun("c", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2})) {
		t.Error("a gate plus retry must park")
	}
	onlyFailure := gatedRun("d", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2, On: []string{"failure"}})
	if gateInPlaceEnabled(onlyFailure) {
		t.Error(`retry on ["failure"] is not gate retry`)
	}
}

// lifecycle.retry.inPlace is the switch, not the backend name. There is no
// mutating webhook, so nil has to resolve to the documented default here.
func TestGateInPlaceEnabled_HonoursTheInPlaceSwitch(t *testing.T) {
	on := gatedRun("switch-1", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2})
	if !gateInPlaceEnabled(on) {
		t.Error("an unset inPlace must default to parking")
	}

	explicit := gatedRun("switch-2", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2, InPlace: boolPtr(true)})
	if !gateInPlaceEnabled(explicit) {
		t.Error("inPlace: true must park")
	}

	off := gatedRun("switch-3", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2, InPlace: boolPtr(false)})
	if gateInPlaceEnabled(off) {
		t.Error("inPlace: false must fall back to successor runs")
	}
}

// The Agent Sandbox backend renders the same pod template and records the same
// status.podName, so it parks exactly as the Job backend does. Keeping the two
// in sync is the point — a backend that cannot park excludes itself by having
// no pod, not by being named here.
func TestGateInPlaceEnabled_SandboxParksToo(t *testing.T) {
	sandbox := gatedRun("sandbox-1", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2})
	sandbox.Spec.AgentSandbox = &sympoziumv1alpha1.AgentSandboxSpec{Enabled: true}
	if !gateInPlaceEnabled(sandbox) {
		t.Error("the sandbox backend has a pod to park and must behave like the Job backend")
	}
}

// A run with no pod cannot park, and says so without anyone checking a backend
// name: checkParkedAttempt returns immediately on an empty status.podName.
func TestCheckParkedAttempt_NoPodIsNotHandled(t *testing.T) {
	run := parkedRun("nopod-1", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2}, 1, 0)
	run.Status.PodName = ""
	r, _ := retryReconciler(t, run)

	handled, _, err := r.checkParkedAttempt(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Fatalf("checkParkedAttempt: %v", err)
	}
	if handled {
		t.Error("a run with no pod was treated as parked")
	}
}

// The Job deadline has to cover the whole chain: sized off one attempt, the
// pod is reclaimed mid-chain and the conversation and workspace go with it.
func TestBuildJob_DeadlineCoversTheWholeChain(t *testing.T) {
	r := &AgentRunReconciler{}
	plain := gatedRun("dl-1", nil)
	plainJob, err := r.buildJob(context.Background(), plain, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildJob: %v", err)
	}

	inplace := gatedRun("dl-2", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3})
	inplaceJob, err := r.buildJob(context.Background(), inplace, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildJob: %v", err)
	}

	if *inplaceJob.Spec.ActiveDeadlineSeconds <= *plainJob.Spec.ActiveDeadlineSeconds {
		t.Errorf("in-place deadline %ds is not longer than the single-attempt %ds",
			*inplaceJob.Spec.ActiveDeadlineSeconds, *plainJob.Spec.ActiveDeadlineSeconds)
	}
	wantAtLeast := int64(3) * (*plainJob.Spec.ActiveDeadlineSeconds)
	if *inplaceJob.Spec.ActiveDeadlineSeconds < wantAtLeast {
		t.Errorf("in-place deadline %ds does not cover 3 attempts (>= %ds)",
			*inplaceJob.Spec.ActiveDeadlineSeconds, wantAtLeast)
	}
}

// ── run timeout anchor ───────────────────────────────────────────────────────

// Parked time is not run time: anchoring to the run start would let the
// watchdog kill a pod that is doing exactly what it was told to do.
func TestRunTimeoutAnchor_UsesTheCurrentAttemptStart(t *testing.T) {
	runStart := metav1.NewTime(metav1.Now().Add(-2 * 60 * 60 * 1e9))
	attemptStart := metav1.Now()
	run := &sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{
		StartedAt: &runStart,
		Attempts: []sympoziumv1alpha1.AttemptStatus{
			{Attempt: 1, StartedAt: &runStart},
			{Attempt: 2, StartedAt: &attemptStart},
		},
	}}
	if got := runTimeoutAnchor(run); got == nil || !got.Equal(&attemptStart) {
		t.Errorf("anchor = %v, want the attempt-2 start %v", got, attemptStart)
	}

	plain := &sympoziumv1alpha1.AgentRun{Status: sympoziumv1alpha1.AgentRunStatus{StartedAt: &runStart}}
	if got := runTimeoutAnchor(plain); got == nil || !got.Equal(&runStart) {
		t.Errorf("anchor = %v, want the run start for a single-attempt run", got)
	}
}

// resolveGate is reached with a retry verdict still on the annotation when a
// in-place chain runs out. It must not clone a successor there: the parked pod
// still holds the real workspace, and a successor would start a second live
// attempt for the same run against a fresh empty one.
func TestResolveGate_NeverClonesASuccessorForAnInPlaceChain(t *testing.T) {
	// maxAttempts is high enough that tryCreateRetryRun would happily create a
	// successor if it were consulted — the guard is what stops it, not the
	// attempt ceiling.
	run := parkedRun("inplace-9", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 9}, 1, 10)
	withVerdict(run, `{"action":"retry","reason":"gate says try again"}`)
	r, _ := retryReconciler(t, run)

	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	var list sympoziumv1alpha1.AgentRunList
	if err := r.List(context.Background(), &list); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("run count = %d, want 1: resolveGate cloned a successor for a parked run", len(list.Items))
	}
	if got := getRun(t, r, "inplace-9"); got.Status.GateVerdict != "retries-exhausted" {
		t.Errorf("gateVerdict = %q, want retries-exhausted", got.Status.GateVerdict)
	}
}

// A run with no attempts[] never parked, so the successor-clone path is still
// its retry mechanism — including a runner image too old to park.
func TestResolveGate_StillClonesForARunThatNeverParked(t *testing.T) {
	run := gatedRun("clone-1", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3})
	withVerdict(run, `{"action":"retry","reason":"gate says try again"}`)
	r, _ := retryReconciler(t, run)

	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	var list sympoziumv1alpha1.AgentRunList
	if err := r.List(context.Background(), &list); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("run count = %d, want 2: the clone path must stay intact", len(list.Items))
	}
}

// The workspace PVC is ReadWriteOnce and the parked pod is still holding it, so
// a gate scheduled onto another node sits Pending until it times out.
func TestPinGateJobToAgentNode_ConstrainsTheGateToThePodsNode(t *testing.T) {
	run := parkedRun("pin-1", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}, 1, 0)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: run.Status.PodName, Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: "node-a"},
	}

	r, _ := retryReconciler(t, run)
	r.Client = fakeClientWithPod(t, run, pod)

	job := r.buildPostRunJob(run, 0, "result")
	r.pinGateJobToAgentNode(context.Background(), logr.Discard(), run, job)

	aff := job.Spec.Template.Spec.Affinity
	if aff == nil || aff.NodeAffinity == nil || aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Fatal("the gate Job was left unpinned; it can land on a node that cannot mount the workspace")
	}
	terms := aff.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 || len(terms[0].MatchFields) != 1 {
		t.Fatalf("node selector = %+v, want one metadata.name match", terms)
	}
	if got := terms[0].MatchFields[0].Values; len(got) != 1 || got[0] != "node-a" {
		t.Errorf("pinned to %v, want [node-a]", got)
	}
}

// An unreadable pod must leave the Job unpinned rather than fail the attempt.
func TestPinGateJobToAgentNode_IsBestEffort(t *testing.T) {
	run := parkedRun("pin-2", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}, 1, 0)
	r, _ := retryReconciler(t, run)

	job := r.buildPostRunJob(run, 0, "result")
	r.pinGateJobToAgentNode(context.Background(), logr.Discard(), run, job)

	if job.Spec.Template.Spec.Affinity != nil {
		t.Errorf("affinity = %+v, want none when the pod cannot be read", job.Spec.Template.Spec.Affinity)
	}
}
