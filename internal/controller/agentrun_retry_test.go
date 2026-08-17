package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// ── fixtures ─────────────────────────────────────────────────────────────────

func retryScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}
	return scheme
}

// gatedRun builds a run parked in PostRunning with a gate hook, which is the
// only state resolveGate is ever reached from.
func gatedRun(name string, retry *sympoziumv1alpha1.RetrySpec) *sympoziumv1alpha1.AgentRun {
	exit := int32(0)
	return &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "default",
			Labels:      map[string]string{"sympozium.ai/instance": "demo"},
			Annotations: map[string]string{},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "demo",
			Task:     sympoziumv1alpha1.NewStringTask("Ship the feature"),
			Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
				GateDefault: "block",
				Retry:       retry,
				PostRun: []sympoziumv1alpha1.LifecycleHookContainer{
					{Name: "check", Image: "check:latest", Gate: true},
				},
			},
		},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase:    sympoziumv1alpha1.AgentRunPhasePostRunning,
			Result:   "here is my first attempt",
			ExitCode: &exit,
		},
	}
}

func withVerdict(run *sympoziumv1alpha1.AgentRun, raw string) *sympoziumv1alpha1.AgentRun {
	run.Annotations["sympozium.ai/gate-verdict"] = raw
	return run
}

func retryReconciler(t *testing.T, objs ...*sympoziumv1alpha1.AgentRun) (*AgentRunReconciler, *recordingEventBus) {
	t.Helper()
	scheme := retryScheme(t)
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sympoziumv1alpha1.AgentRun{})
	for _, o := range objs {
		builder = builder.WithObjects(o)
	}
	bus := &recordingEventBus{}
	return &AgentRunReconciler{
		Client:   builder.Build(),
		Scheme:   scheme,
		Log:      logr.Discard(),
		EventBus: bus,
	}, bus
}

// retryReconcilerWithLaggingCache mirrors the production client topology: the
// reconciler's Client reads through an informer cache that has not observed
// laggingName, while APIReader goes straight to the apiserver. A plain fake
// client cannot express that split, which is why the Get-after-Create hazard
// never showed up in unit tests.
func retryReconcilerWithLaggingCache(t *testing.T, laggingName string, objs ...*sympoziumv1alpha1.AgentRun) (*AgentRunReconciler, client.Client) {
	t.Helper()
	scheme := retryScheme(t)
	builder := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(&sympoziumv1alpha1.AgentRun{})
	for _, o := range objs {
		builder = builder.WithObjects(o)
	}
	direct := builder.Build()
	cached := interceptor.NewClient(direct, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name == laggingName {
				return apierrors.NewNotFound(
					schema.GroupResource{Group: sympoziumv1alpha1.GroupVersion.Group, Resource: "agentruns"}, key.Name)
			}
			return c.Get(ctx, key, obj, opts...)
		},
	})
	return &AgentRunReconciler{
		Client:    cached,
		APIReader: direct,
		Scheme:    scheme,
		Log:       logr.Discard(),
		EventBus:  &recordingEventBus{},
	}, direct
}

func getRun(t *testing.T, r *AgentRunReconciler, name string) *sympoziumv1alpha1.AgentRun {
	t.Helper()
	var got sympoziumv1alpha1.AgentRun
	if err := r.Get(context.Background(), types.NamespacedName{Name: name, Namespace: "default"}, &got); err != nil {
		t.Fatalf("get run %s: %v", name, err)
	}
	return &got
}

// ── resolveGate: the retry branch ────────────────────────────────────────────

func TestResolveGate_RetryCreatesSuccessor(t *testing.T) {
	run := withVerdict(
		gatedRun("demo-run", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}),
		`{"action":"retry","reason":"build failed","response":"npm ERR! missing script"}`)
	run.Annotations["otel.dev/traceparent"] = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	run.Annotations["sympozium.ai/reply-channel"] = "slack"
	run.Annotations["sympozium.ai/reply-chat-id"] = "C123"
	run.Labels["sympozium.ai/source"] = "channel"

	r, bus := retryReconciler(t, run)

	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	successor := getRun(t, r, "demo-run-retry-2")

	if successor.Status.RetryOf != "demo-run" {
		t.Errorf("successor retryOf = %q, want %q", successor.Status.RetryOf, "demo-run")
	}
	if successor.Status.Attempt != 2 {
		t.Errorf("successor attempt = %d, want 2", successor.Status.Attempt)
	}
	if successor.Labels[retryOfLabel] != "demo-run" || successor.Labels[retryAttemptLabel] != "2" {
		t.Errorf("successor lineage labels = %v", successor.Labels)
	}

	task := successor.Spec.Task.GetPrompt()
	for _, want := range []string{"## Retry 2 of 3", "Ship the feature", "here is my first attempt", "build failed", "npm ERR! missing script"} {
		if !strings.Contains(task, want) {
			t.Errorf("successor task is missing %q:\n%s", want, task)
		}
	}

	// Carrying the verdict forward would let the successor resolve instantly
	// against its predecessor's verdict — an unbounded loop in one reconcile.
	if _, ok := successor.Annotations["sympozium.ai/gate-verdict"]; ok {
		t.Error("successor inherited the gate-verdict annotation")
	}

	// A Slack-triggered chain must keep its reply address, or it goes silent.
	if successor.Annotations["sympozium.ai/reply-channel"] != "slack" ||
		successor.Annotations["sympozium.ai/reply-chat-id"] != "C123" ||
		successor.Labels["sympozium.ai/source"] != "channel" {
		t.Errorf("successor lost channel provenance: labels=%v annotations=%v", successor.Labels, successor.Annotations)
	}
	if successor.Annotations["otel.dev/traceparent"] == "" {
		t.Error("successor lost the traceparent, breaking the chain's trace")
	}

	// The superseded attempt.
	predecessor := getRun(t, r, "demo-run")
	if predecessor.Status.GateVerdict != "retried" {
		t.Errorf("predecessor gateVerdict = %q, want %q", predecessor.Status.GateVerdict, "retried")
	}
	if predecessor.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed {
		t.Errorf("predecessor phase = %q, want %q", predecessor.Status.Phase, sympoziumv1alpha1.AgentRunPhaseFailed)
	}
	if predecessor.Status.Error != "" {
		t.Errorf("predecessor status.error = %q, want empty (superseded, not errored)", predecessor.Status.Error)
	}
	var retried *metav1.Condition
	for i := range predecessor.Status.Conditions {
		if predecessor.Status.Conditions[i].Type == "Retried" {
			retried = &predecessor.Status.Conditions[i]
		}
	}
	if retried == nil {
		t.Fatalf("predecessor is missing the Retried condition: %+v", predecessor.Status.Conditions)
	}
	if !strings.Contains(retried.Message, "demo-run-retry-2") {
		t.Errorf("Retried condition does not name the successor: %q", retried.Message)
	}

	// Nothing may reach the user mid-chain: publishing the completion would
	// post the rejected answer, publishing the failure a spurious error.
	if len(bus.published) != 0 {
		t.Errorf("intermediate attempt published %d event(s): %+v", len(bus.published), bus.published)
	}
}

func TestResolveGate_RetryChainNamesDoNotCompound(t *testing.T) {
	run := withVerdict(
		gatedRun("demo-run-retry-2", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}),
		`{"action":"retry","reason":"still failing"}`)
	run.Status.Attempt = 2
	run.Status.RetryOf = "demo-run"

	r, _ := retryReconciler(t, run)
	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	successor := getRun(t, r, "demo-run-retry-3")
	if successor.Status.Attempt != 3 {
		t.Errorf("attempt = %d, want 3", successor.Status.Attempt)
	}
	if !strings.HasPrefix(successor.Spec.Task.GetPrompt(), "## Retry 3 of 3") {
		t.Errorf("unexpected card header:\n%s", successor.Spec.Task.GetPrompt())
	}
}

// The lineage write targets an object created microseconds earlier, so a
// cached Get can miss it. When it did, every real chain rendered an empty
// Attempt / Retry Of column while the labels looked correct.
func TestResolveGate_RetryLineageSurvivesACacheMiss(t *testing.T) {
	run := withVerdict(
		gatedRun("demo-run", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}),
		`{"action":"retry","reason":"tests failed"}`)

	r, direct := retryReconcilerWithLaggingCache(t, "demo-run-retry-2", run)
	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	var successor sympoziumv1alpha1.AgentRun
	if err := direct.Get(context.Background(),
		types.NamespacedName{Name: "demo-run-retry-2", Namespace: "default"}, &successor); err != nil {
		t.Fatalf("get successor: %v", err)
	}
	if successor.Status.Attempt != 2 {
		t.Errorf("successor status.attempt = %d, want 2 — the lineage write was lost to the cache miss", successor.Status.Attempt)
	}
	if successor.Status.RetryOf != "demo-run" {
		t.Errorf("successor status.retryOf = %q, want %q", successor.Status.RetryOf, "demo-run")
	}
}

// Even with the write repaired, the bound must not depend on it. Without the
// label fallback this run names itself as its own successor and the chain
// dead-ends silently.
func TestResolveGate_AttemptLabelBoundsAChainMissingItsStatus(t *testing.T) {
	run := withVerdict(
		gatedRun("demo-run-retry-2", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2}),
		`{"action":"retry","reason":"still failing"}`)
	run.Labels[retryOfLabel] = "demo-run"
	run.Labels[retryAttemptLabel] = "2"
	// status.attempt deliberately unset — this is the run the bug produced.

	r, _ := retryReconciler(t, run)
	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(context.Background(), &runs); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("expected no successor once attempts are exhausted, got %d runs", len(runs.Items))
	}
	if got := getRun(t, r, "demo-run-retry-2"); got.Status.GateVerdict != "retries-exhausted" {
		t.Errorf("gateVerdict = %q, want %q", got.Status.GateVerdict, "retries-exhausted")
	}
}

// ...and the third attempt a maxAttempts:3 chain is owed still gets created.
func TestResolveGate_AttemptLabelContinuesAChainMissingItsStatus(t *testing.T) {
	run := withVerdict(
		gatedRun("demo-run-retry-2", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}),
		`{"action":"retry","reason":"still failing"}`)
	run.Labels[retryOfLabel] = "demo-run"
	run.Labels[retryAttemptLabel] = "2"

	r, _ := retryReconciler(t, run)
	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}
	successor := getRun(t, r, "demo-run-retry-3")
	if successor.Labels[retryAttemptLabel] != "3" {
		t.Errorf("successor attempt label = %q, want %q", successor.Labels[retryAttemptLabel], "3")
	}
	if !strings.HasPrefix(successor.Spec.Task.GetPrompt(), "## Retry 3 of 3") {
		t.Errorf("unexpected card header:\n%s", successor.Spec.Task.GetPrompt())
	}
}

func TestResolveGate_RetryExhaustedFallsThroughToReject(t *testing.T) {
	run := withVerdict(
		gatedRun("demo-run-retry-2", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2}),
		`{"action":"retry","reason":"still failing","response":"gate says no"}`)
	run.Status.Attempt = 2

	r, bus := retryReconciler(t, run)
	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(context.Background(), &runs); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs.Items) != 1 {
		t.Fatalf("expected no successor once attempts are exhausted, got %d runs", len(runs.Items))
	}

	got := getRun(t, r, "demo-run-retry-2")
	if got.Status.GateVerdict != "retries-exhausted" {
		t.Errorf("gateVerdict = %q, want %q", got.Status.GateVerdict, "retries-exhausted")
	}
	// A retry verdict's `response` is gate output written for the agent, not a
	// message for whoever asked the question — it must not become the answer.
	if strings.Contains(got.Status.Result, "gate says no") {
		t.Errorf("result leaked the gate's agent-facing output: %q", got.Status.Result)
	}
	if !strings.Contains(got.Status.Result, "rejected all 2 attempts") {
		t.Errorf("result = %q, want it to state the chain was exhausted", got.Status.Result)
	}
	if len(bus.published) == 0 {
		t.Error("a terminal attempt must publish its completion")
	}
}

func TestResolveGate_RetryChainTokenBudgetStopsTheChain(t *testing.T) {
	predecessor := gatedRun("demo-run", nil)
	predecessor.Status.TokenUsage = &sympoziumv1alpha1.TokenUsage{TotalTokens: 800}

	run := withVerdict(
		gatedRun("demo-run-retry-2", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 5, MaxChainTokens: 1000}),
		`{"action":"retry","reason":"nope"}`)
	run.Status.Attempt = 2
	run.Status.RetryOf = "demo-run"
	run.Status.TokenUsage = &sympoziumv1alpha1.TokenUsage{TotalTokens: 400}

	r, _ := retryReconciler(t, predecessor, run)
	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	if got := getRun(t, r, "demo-run-retry-2"); got.Status.GateVerdict != "retries-exhausted" {
		t.Errorf("gateVerdict = %q, want %q (800+400 exceeds the 1000 budget)", got.Status.GateVerdict, "retries-exhausted")
	}
}

// A gate asking for a retry the spec does not allow must resolve as a reject.
// Treating it as an approval would let a misconfigured gate publish output it
// explicitly refused.
func TestResolveGate_RetryWithoutConfigIsRejectNotApprove(t *testing.T) {
	run := withVerdict(
		gatedRun("demo-run", nil),
		`{"action":"retry","reason":"nope","response":"not good enough"}`)

	r, _ := retryReconciler(t, run)
	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	got := getRun(t, r, "demo-run")
	if got.Status.GateVerdict != "rejected" {
		t.Errorf("gateVerdict = %q, want %q", got.Status.GateVerdict, "rejected")
	}
	if strings.Contains(got.Status.Result, "not good enough") {
		t.Errorf("result leaked the gate's agent-facing output: %q", got.Status.Result)
	}
	if !strings.Contains(got.Status.Result, "Response blocked") {
		t.Errorf("result = %q, want a blocked message", got.Status.Result)
	}
}

func TestResolveGate_RetryOnFailureOnlyIsNotGateRetry(t *testing.T) {
	run := withVerdict(
		gatedRun("demo-run", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3, On: []string{"failure"}}),
		`{"action":"retry","reason":"nope"}`)

	r, _ := retryReconciler(t, run)
	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}
	if got := getRun(t, r, "demo-run"); got.Status.GateVerdict != "rejected" {
		t.Errorf("gateVerdict = %q, want %q", got.Status.GateVerdict, "rejected")
	}
}

func TestResolveGate_RetryBackoffStampsNotBefore(t *testing.T) {
	run := withVerdict(
		gatedRun("demo-run", &sympoziumv1alpha1.RetrySpec{
			MaxAttempts: 3,
			Backoff:     &metav1.Duration{Duration: 30 * time.Second},
		}),
		`{"action":"retry","reason":"nope"}`)

	r, _ := retryReconciler(t, run)
	if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
		t.Fatalf("resolveGate: %v", err)
	}

	successor := getRun(t, r, "demo-run-retry-2")
	raw := successor.Annotations[retryNotBeforeAnnotation]
	if raw == "" {
		t.Fatal("successor is missing the retry-not-before annotation")
	}
	notBefore, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("retry-not-before %q is not RFC3339: %v", raw, err)
	}
	if !notBefore.After(time.Now().Add(20 * time.Second)) {
		t.Errorf("retry-not-before = %v, want ~30s out", notBefore)
	}
}

// Retrying must remain idempotent: reconcile can run the same resolution twice.
func TestResolveGate_RetryIsIdempotent(t *testing.T) {
	run := withVerdict(
		gatedRun("demo-run", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 3}),
		`{"action":"retry","reason":"nope"}`)

	r, _ := retryReconciler(t, run)
	for i := 0; i < 2; i++ {
		if _, err := r.resolveGate(context.Background(), logr.Discard(), run, true, false); err != nil {
			t.Fatalf("resolveGate (pass %d): %v", i+1, err)
		}
	}

	var runs sympoziumv1alpha1.AgentRunList
	if err := r.List(context.Background(), &runs); err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs.Items) != 2 {
		t.Fatalf("expected exactly one successor, got %d runs total", len(runs.Items))
	}
}

// ── pure functions ───────────────────────────────────────────────────────────

func TestParseGateVerdict_Actions(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string // empty means the verdict must be rejected as invalid
	}{
		{"approve", `{"action":"approve"}`, "approve"},
		{"reject", `{"action":"reject","response":"no"}`, "reject"},
		{"rewrite", `{"action":"rewrite","response":"clean"}`, "rewrite"},
		{"retry", `{"action":"retry","reason":"why","response":"output"}`, "retry"},
		{"unknown action is rejected", `{"action":"retryy"}`, ""},
		{"empty action is rejected", `{"action":""}`, ""},
		{"malformed JSON is rejected", `not json`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := &sympoziumv1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{"sympozium.ai/gate-verdict": tc.raw},
				},
			}
			got := parseGateVerdict(run)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("parseGateVerdict = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("parseGateVerdict = nil, want action %q", tc.want)
			}
			if got.Action != tc.want {
				t.Errorf("action = %q, want %q", got.Action, tc.want)
			}
		})
	}
}

func TestExtractOriginalTask_StripsCards(t *testing.T) {
	handoff := buildHandoffTask("author", "Write the report", "a draft", "Review it")
	retry := buildRetryTask("Write the report", "a draft",
		&gateVerdict{Action: "retry", Reason: "too short", Response: "wc -w = 12"}, 2, 3)
	// The case that matters most: a retry of a run whose task was already a
	// handoff card. Without stripping, every attempt compounds.
	retryOfHandoff := buildRetryTask(handoff, "a draft",
		&gateVerdict{Action: "retry", Reason: "too short"}, 2, 3)

	cases := []struct {
		name string
		task string
		want string
	}{
		{"plain task is untouched", "Write the report", "Write the report"},
		{"handoff card", handoff, "Write the report"},
		{"retry card", retry, "Write the report"},
		{"retry of a handoff card", retryOfHandoff, "Write the report"},
		{"a task that merely mentions retry is untouched", "Retry the deploy", "Retry the deploy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractOriginalTask(tc.task); got != tc.want {
				t.Errorf("extractOriginalTask() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRetryGateOutputLimit(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"unset", "", defaultRetryGateOutputMaxChars},
		{"override", "250", 250},
		{"non-numeric falls back", "lots", defaultRetryGateOutputMaxChars},
		{"zero falls back", "0", defaultRetryGateOutputMaxChars},
		{"negative falls back", "-5", defaultRetryGateOutputMaxChars},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(retryGateOutputMaxCharsEnv, tc.env)
			if got := retryGateOutputLimit(); got != tc.want {
				t.Errorf("retryGateOutputLimit() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestBuildRetryTask_HonoursGateOutputEnvOverride(t *testing.T) {
	t.Setenv(retryGateOutputMaxCharsEnv, "100")
	output := strings.Repeat("y", 500)
	card := buildRetryTask("Do the thing", "attempt",
		&gateVerdict{Action: "retry", Reason: "too long", Response: output}, 2, 3)

	if strings.Contains(card, strings.Repeat("y", 101)) {
		t.Error("gate output was not clipped to the overridden limit")
	}
	if !strings.Contains(card, "carries the first 100") {
		t.Errorf("truncation notice does not report the overridden limit:\n%s", card)
	}
}

func TestBuildRetryTask_TruncatesAndAnnouncesIt(t *testing.T) {
	long := strings.Repeat("x", handoffResultMaxChars+50)
	card := buildRetryTask("Do the thing", long,
		&gateVerdict{Action: "retry", Reason: "too long", Response: strings.Repeat("y", defaultRetryGateOutputMaxChars+50)}, 2, 3)

	if !strings.Contains(card, "[truncated: your previous attempt produced") {
		t.Error("a clipped previous attempt must say so, or the agent acts on a fragment")
	}
	if !strings.Contains(card, "[truncated: the gate produced") {
		t.Error("clipped gate output must say so")
	}
	if strings.Count(card, "## Retry") != 1 {
		t.Errorf("expected exactly one card header:\n%s", card)
	}
}

func TestBuildRetryTask_OmitsEmptySections(t *testing.T) {
	card := buildRetryTask("Do the thing", "", &gateVerdict{Action: "retry"}, 2, 2)

	if strings.Contains(card, "### Gate Output") {
		t.Error("an empty gate output should not render a heading")
	}
	if !strings.Contains(card, "(the previous attempt produced no output)") {
		t.Error("an empty previous attempt should be stated, not left blank")
	}
	if !strings.Contains(card, "without giving a reason") {
		t.Error("a missing reason should be stated, not left blank")
	}
}

func TestRetryChainName(t *testing.T) {
	cases := []struct {
		name      string
		predecess string
		attempt   int
		want      string
	}{
		{"first retry", "foo", 2, "foo-retry-2"},
		{"suffixes do not compound", "foo-retry-2", 3, "foo-retry-3"},
		{"deep chain", "foo-retry-9", 10, "foo-retry-10"},
		{"a name that merely contains retry is preserved", "retry-foo", 2, "retry-foo-retry-2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryChainName(tc.predecess, tc.attempt); got != tc.want {
				t.Errorf("retryChainName(%q, %d) = %q, want %q", tc.predecess, tc.attempt, got, tc.want)
			}
		})
	}
}

func TestRetryChainName_RespectsDNSLimit(t *testing.T) {
	got := retryChainName(strings.Repeat("a", maxRunNameLen), 2)
	if len(got) > maxRunNameLen {
		t.Errorf("name length = %d, want <= %d", len(got), maxRunNameLen)
	}
	if !strings.HasSuffix(got, "-retry-2") {
		t.Errorf("truncation dropped the suffix: %q", got)
	}
}

// A corrupted retryOf cycle must cost a bounded number of Gets, not spin.
func TestRetryChainTokens_TerminatesOnCycle(t *testing.T) {
	a := gatedRun("a", nil)
	a.Status.RetryOf = "b"
	a.Status.TokenUsage = &sympoziumv1alpha1.TokenUsage{TotalTokens: 10}
	b := gatedRun("b", nil)
	b.Status.RetryOf = "a"
	b.Status.TokenUsage = &sympoziumv1alpha1.TokenUsage{TotalTokens: 20}

	r, _ := retryReconciler(t, a, b)

	done := make(chan int64, 1)
	go func() { done <- r.retryChainTokens(context.Background(), a) }()
	select {
	case total := <-done:
		if total != 30 {
			t.Errorf("chain tokens = %d, want 30 (each run counted once)", total)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retryChainTokens did not terminate on a cyclic chain")
	}
}

// The chain token budget walks the same lineage, so it needs the same fallback.
func TestRetryChainTokens_WalksTheLineageLabel(t *testing.T) {
	first := gatedRun("demo-run", nil)
	first.Status.TokenUsage = &sympoziumv1alpha1.TokenUsage{TotalTokens: 800}

	second := gatedRun("demo-run-retry-2", nil)
	second.Labels[retryOfLabel] = "demo-run" // status.retryOf never landed
	second.Status.TokenUsage = &sympoziumv1alpha1.TokenUsage{TotalTokens: 400}

	r, _ := retryReconciler(t, first, second)
	if got := r.retryChainTokens(context.Background(), second); got != 1200 {
		t.Errorf("chain tokens = %d, want 1200", got)
	}
}

func TestCurrentAttempt_FallsBackToTheLabel(t *testing.T) {
	cases := []struct {
		name   string
		status int
		label  string
		want   int
	}{
		{"no lineage at all", 0, "", 1},
		{"status wins when both are set", 3, "2", 3},
		{"label carries a lost status write", 0, "2", 2},
		{"a junk label reads as attempt 1", 0, "two", 1},
		{"a zero label reads as attempt 1", 0, "0", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := gatedRun("r", nil)
			run.Status.Attempt = tc.status
			run.Labels[retryAttemptLabel] = tc.label
			if got := currentAttempt(run); got != tc.want {
				t.Errorf("currentAttempt() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestGateRetrySpec_OnDefaultsToGate(t *testing.T) {
	cases := []struct {
		name string
		on   []string
		want bool
	}{
		{"unset means gate", nil, true},
		{"explicit gate", []string{"gate"}, true},
		{"gate among others", []string{"failure", "gate"}, true},
		{"failure only is not wired", []string{"failure"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := gatedRun("r", &sympoziumv1alpha1.RetrySpec{MaxAttempts: 2, On: tc.on})
			if got := gateRetrySpec(run) != nil; got != tc.want {
				t.Errorf("gateRetrySpec() enabled = %v, want %v", got, tc.want)
			}
		})
	}
}
