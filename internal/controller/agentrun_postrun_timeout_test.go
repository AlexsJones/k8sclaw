package controller

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestPostRunJobStart(t *testing.T) {
	started := metav1.NewTime(time.Now().Add(-5 * time.Minute))
	created := metav1.NewTime(time.Now().Add(-9 * time.Minute))

	cases := []struct {
		name string
		job  *batchv1.Job
		want time.Time
	}{
		{
			"prefers the Job's own start time, which is what ActiveDeadlineSeconds measures",
			&batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{CreationTimestamp: created},
				Status:     batchv1.JobStatus{StartTime: &started},
			},
			started.Time,
		},
		{
			"falls back to creation for a Job that never started",
			&batchv1.Job{ObjectMeta: metav1.ObjectMeta{CreationTimestamp: created}},
			created.Time,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := postRunJobStart(tc.job); !got.Equal(tc.want) {
				t.Errorf("postRunJobStart() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPostRunBudget(t *testing.T) {
	hook := func(timeout time.Duration) sympoziumv1alpha1.LifecycleHookContainer {
		h := sympoziumv1alpha1.LifecycleHookContainer{Name: "h", Image: "i"}
		if timeout > 0 {
			h.Timeout = &metav1.Duration{Duration: timeout}
		}
		return h
	}

	cases := []struct {
		name      string
		lifecycle *sympoziumv1alpha1.LifecycleHooks
		want      time.Duration
	}{
		{"no lifecycle at all", nil, postRunMinTimeout},
		{
			"a single undeclared hook keeps the historical budget, not half of it",
			&sympoziumv1alpha1.LifecycleHooks{PostRun: []sympoziumv1alpha1.LifecycleHookContainer{hook(0)}},
			postRunMinTimeout,
		},
		{
			"undeclared hooks each get the documented 5m default",
			&sympoziumv1alpha1.LifecycleHooks{PostRun: []sympoziumv1alpha1.LifecycleHookContainer{hook(0), hook(0), hook(0)}},
			15 * time.Minute,
		},
		{
			"sequential hooks sum",
			&sympoziumv1alpha1.LifecycleHooks{PostRun: []sympoziumv1alpha1.LifecycleHookContainer{hook(20 * time.Minute), hook(10 * time.Minute)}},
			30 * time.Minute,
		},
		{
			"a declared timeout below the floor cannot tighten it",
			&sympoziumv1alpha1.LifecycleHooks{PostRun: []sympoziumv1alpha1.LifecycleHookContainer{hook(30 * time.Second)}},
			postRunMinTimeout,
		},
		{
			"a human-in-the-loop gate gets the day it asks for",
			&sympoziumv1alpha1.LifecycleHooks{PostRun: []sympoziumv1alpha1.LifecycleHookContainer{hook(24 * time.Hour)}},
			24 * time.Hour,
		},
		{
			"a mix of declared and defaulted",
			&sympoziumv1alpha1.LifecycleHooks{PostRun: []sympoziumv1alpha1.LifecycleHookContainer{hook(time.Hour), hook(0)}},
			time.Hour + defaultPostRunHookTimeout,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := postRunBudget(tc.lifecycle); got != tc.want {
				t.Errorf("postRunBudget() = %v, want %v", got, tc.want)
			}
		})
	}
}

// postRunFixture parks a gated run in PostRunning alongside its postRun Job.
// runAge is how long ago the agent run started; jobAge how long ago the postRun
// Job did. The two are independent; conflating them was the bug.
//
// Any mutators run before the run is stored: reconcilePostRunning re-reads the
// run from the cluster, so mutating the returned pointer afterwards would be
// silently discarded.
func postRunFixture(
	t *testing.T, runAge, jobAge time.Duration,
	mutators ...func(*sympoziumv1alpha1.AgentRun),
) (*AgentRunReconciler, *sympoziumv1alpha1.AgentRun) {
	t.Helper()
	scheme := retryScheme(t)
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add batch scheme: %v", err)
	}

	run := gatedRun("demo-run", nil)
	startedAt := metav1.NewTime(time.Now().Add(-runAge))
	run.Status.StartedAt = &startedAt
	run.Status.PostRunJobName = "demo-run-postrun"
	for _, mutate := range mutators {
		mutate(run)
	}

	jobStart := metav1.NewTime(time.Now().Add(-jobAge))
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "demo-run-postrun",
			Namespace:         "default",
			CreationTimestamp: jobStart,
		},
		Status: batchv1.JobStatus{StartTime: &jobStart},
	}

	r := &AgentRunReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(run, job).
			WithStatusSubresource(&sympoziumv1alpha1.AgentRun{}).
			Build(),
		Scheme:   scheme,
		Log:      logr.Discard(),
		EventBus: &recordingEventBus{},
	}
	return r, run
}

// The regression this guards: the timeout used to be measured from the agent
// run's start, so any run longer than postRunTimeout had its gate Job killed on
// the very first PostRunning reconcile. A long autonomous run could never
// receive a verdict — it always resolved as "timeout" and fell to gateDefault.
func TestReconcilePostRunning_LongRunStillGetsItsFullGateBudget(t *testing.T) {
	r, run := postRunFixture(t, 2*time.Hour, 5*time.Second)

	res, err := r.reconcilePostRunning(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Fatalf("reconcilePostRunning: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the gate is still running")
	}

	got := getRun(t, r, "demo-run")
	if got.Status.GateVerdict != "" {
		t.Errorf("gateVerdict = %q, want empty — the gate had barely started", got.Status.GateVerdict)
	}
	if got.Status.Phase != sympoziumv1alpha1.AgentRunPhasePostRunning {
		t.Errorf("phase = %q, want %q", got.Status.Phase, sympoziumv1alpha1.AgentRunPhasePostRunning)
	}

	var job batchv1.Job
	key := types.NamespacedName{Name: "demo-run-postrun", Namespace: "default"}
	if err := r.Get(context.Background(), key, &job); err != nil {
		t.Fatalf("the postRun Job was deleted despite having just started: %v", err)
	}
}

// The bound still has to bite once the gate itself has run long enough.
//
// The verdict label is "error" rather than "timeout": the timeout call site
// passes hookFailed=true, and resolveGate reserves "timeout" for a gate Job
// that finished without writing a verdict. The two read backwards from their
// names, but that is pre-existing and visible in the UI, so this pins the
// behaviour as it is rather than asserting what the names suggest.
func TestReconcilePostRunning_StaleGateJobTimesOut(t *testing.T) {
	r, run := postRunFixture(t, 2*time.Hour, postRunMinTimeout+postRunTimeoutGrace+time.Minute)

	if _, err := r.reconcilePostRunning(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("reconcilePostRunning: %v", err)
	}

	if got := getRun(t, r, "demo-run"); got.Status.GateVerdict != "error" {
		t.Errorf("gateVerdict = %q, want %q", got.Status.GateVerdict, "error")
	}

	var job batchv1.Job
	key := types.NamespacedName{Name: "demo-run-postrun", Namespace: "default"}
	if err := r.Get(context.Background(), key, &job); err == nil {
		t.Error("expected the timed-out postRun Job to be deleted")
	}
}

// The human-in-the-loop case: a gate declaring a day must survive past the
// default, so a run left for overnight review is still awaiting a verdict in
// the morning rather than decided by gateDefault.
func TestReconcilePostRunning_DeclaredHookTimeoutExtendsTheBudget(t *testing.T) {
	r, run := postRunFixture(t, 2*time.Hour, postRunMinTimeout+time.Hour,
		func(run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Lifecycle.PostRun[0].Timeout = &metav1.Duration{Duration: 24 * time.Hour}
		})

	res, err := r.reconcilePostRunning(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Fatalf("reconcilePostRunning: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the gate is still waiting for a human")
	}
	if got := getRun(t, r, "demo-run"); got.Status.GateVerdict != "" {
		t.Errorf("gateVerdict = %q, want empty — the gate declared 24h", got.Status.GateVerdict)
	}
}

// The declared timeout must reach the Job too, or the Job's own
// ActiveDeadlineSeconds kills the hook before the controller's backstop.
func TestBuildPostRunJob_DeadlineFollowsHookTimeout(t *testing.T) {
	r := &AgentRunReconciler{}
	run := gatedRun("demo-run", nil)
	run.Spec.Lifecycle.PostRun[0].Timeout = &metav1.Duration{Duration: 24 * time.Hour}

	job := r.buildPostRunJob(run, 0, "")
	if job.Spec.ActiveDeadlineSeconds == nil {
		t.Fatal("postRun Job has no ActiveDeadlineSeconds")
	}
	if want := int64((24 * time.Hour).Seconds()); *job.Spec.ActiveDeadlineSeconds != want {
		t.Errorf("ActiveDeadlineSeconds = %d, want %d", *job.Spec.ActiveDeadlineSeconds, want)
	}
}

// A short run is unaffected: the previous anchor and the new one agree there,
// which is why the bug went unnoticed.
func TestReconcilePostRunning_ShortRunUnchanged(t *testing.T) {
	r, run := postRunFixture(t, 30*time.Second, 5*time.Second)

	res, err := r.reconcilePostRunning(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Fatalf("reconcilePostRunning: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("expected a requeue while the gate is still running")
	}
	if got := getRun(t, r, "demo-run"); got.Status.GateVerdict != "" {
		t.Errorf("gateVerdict = %q, want empty", got.Status.GateVerdict)
	}
}
