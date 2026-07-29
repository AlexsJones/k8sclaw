package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// newAgentRunTestScheme registers every group the AgentRun reconcile paths touch:
// core (pods/configmaps/secrets/SAs), batch (Jobs), apps (the memory Deployment
// readiness gate), rbac (skill and lifecycle RBAC), and the Sympozium CRDs.
func newAgentRunTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = batchv1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}
	return scheme
}

// newAgentRunTestReconciler builds an AgentRunReconciler backed by a fake
// client. Both Client and APIReader point at the same fake so tests can mutate
// objects via either field.
//
// DynamicClient is wired to a fake too, so the agent-sandbox backend
// (reconcilePendingAgentSandbox) is drivable without a cluster — see
// agentrun_backend_parity_test.go.
func newAgentRunTestReconciler(t *testing.T, objs ...client.Object) *AgentRunReconciler {
	t.Helper()

	scheme := newAgentRunTestScheme(t)

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&sympoziumv1alpha1.AgentRun{}).
		Build()

	// The agent-sandbox CRDs are not in our scheme (they belong to
	// kubernetes-sigs/agent-sandbox), so the list kinds have to be declared.
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		scheme,
		map[schema.GroupVersionResource]string{
			sandboxGVR:      "SandboxList",
			sandboxClaimGVR: "SandboxClaimList",
			warmPoolGVR:     "SandboxWarmPoolList",
		},
	)

	return &AgentRunReconciler{
		Client:        cl,
		APIReader:     cl,
		Scheme:        scheme,
		Log:           logr.Discard(),
		DynamicClient: dyn,
	}
}

// TestReconcileRunning_JobNotFoundGuard_DoesNotOverrideSucceeded is the
// regression guard for the race in which `succeedRun` had already committed
// status.phase=Succeeded (and deleted the Job to kill sidecars) when a stale
// reconcile of the same AgentRun fires, can't find the Job, and would
// previously flip the phase to Failed with "Job not found". With APIReader
// in place, the guard sees the real phase and refuses to override.
func TestReconcileRunning_JobNotFoundGuard_DoesNotOverrideSucceeded(t *testing.T) {
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "regression-run", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "regression-inst",
		},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase:   sympoziumv1alpha1.AgentRunPhaseSucceeded,
			JobName: "regression-run-job",
		},
	}
	// Note: no Job object is seeded — simulating the post-cleanup race.
	r := newAgentRunTestReconciler(t, run)

	// The guard inside reconcileRunning runs via r.Get(Job) failing with
	// NotFound. We call the public Reconcile entrypoint using the run's
	// key via the phase dispatcher — but to keep the test focused we call
	// reconcileRunning directly with a pointer-copy of the object.
	running := run.DeepCopy()
	running.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning // pretend this reconcile still sees Running
	res, err := r.reconcileRunning(context.Background(), logr.Discard(), running)
	if err != nil {
		t.Fatalf("reconcileRunning returned error: %v", err)
	}
	if res.RequeueAfter != 0 {
		t.Errorf("expected no requeue for terminal-phase guard; got RequeueAfter=%v", res.RequeueAfter)
	}

	// Most important assertion: the stored AgentRun status MUST still be
	// Succeeded — not overridden to Failed.
	var stored sympoziumv1alpha1.AgentRun
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatalf("get stored: %v", err)
	}
	if stored.Status.Phase != sympoziumv1alpha1.AgentRunPhaseSucceeded {
		t.Errorf(
			"REGRESSION: stored phase overridden to %q (expected Succeeded)",
			stored.Status.Phase,
		)
	}
	if stored.Status.Error != "" {
		t.Errorf(
			"REGRESSION: stored error populated to %q (should stay empty on Succeeded run)",
			stored.Status.Error,
		)
	}
}

// TestReconcileRunning_JobNotFoundGuard_DoesNotOverrideFailed: similarly,
// a run that has already been marked Failed by another code path (with a
// more specific error message) must not be clobbered with "Job not found".
func TestReconcileRunning_JobNotFoundGuard_DoesNotOverrideFailed(t *testing.T) {
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "failed-run", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentRunSpec{AgentRef: "x"},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase:   sympoziumv1alpha1.AgentRunPhaseFailed,
			Error:   "agent container exited with code 137 (OOMKilled)",
			JobName: "failed-run-job",
		},
	}
	r := newAgentRunTestReconciler(t, run)

	stale := run.DeepCopy()
	stale.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
	_, err := r.reconcileRunning(context.Background(), logr.Discard(), stale)
	if err != nil {
		t.Fatalf("reconcileRunning returned error: %v", err)
	}

	var stored sympoziumv1alpha1.AgentRun
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatalf("get stored: %v", err)
	}
	if stored.Status.Error != "agent container exited with code 137 (OOMKilled)" {
		t.Errorf(
			"REGRESSION: stored error overridden to %q (expected OOMKilled message to be preserved)",
			stored.Status.Error,
		)
	}
}

// TestReconcileRunning_JobNotFoundGuard_RequeuesForPostRunning: if the run
// moved to PostRunning (the lifecycle hook is still executing), we should
// requeue rather than silently drop — otherwise the postRun progress is
// only driven by watches, which are best-effort.
func TestReconcileRunning_JobNotFoundGuard_RequeuesForPostRunning(t *testing.T) {
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "postrun-run", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentRunSpec{AgentRef: "x"},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase:   sympoziumv1alpha1.AgentRunPhasePostRunning,
			JobName: "postrun-run-job",
		},
	}
	r := newAgentRunTestReconciler(t, run)

	stale := run.DeepCopy()
	stale.Status.Phase = sympoziumv1alpha1.AgentRunPhaseRunning
	res, err := r.reconcileRunning(context.Background(), logr.Discard(), stale)
	if err != nil {
		t.Fatalf("reconcileRunning returned error: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("expected RequeueAfter>0 for PostRunning guard; got %v", res.RequeueAfter)
	}
}

// TestReconcileRunning_JobNotFoundGuard_FailsWhenReallyRunning: when the
// phase really is still Running (not an overtaken race), the guard must
// still call failRun with the "Job not found" message — otherwise stuck
// runs would loop forever.
func TestReconcileRunning_JobNotFoundGuard_FailsWhenReallyRunning(t *testing.T) {
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "stuck-run", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentRunSpec{AgentRef: "x"},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase:   sympoziumv1alpha1.AgentRunPhaseRunning,
			JobName: "stuck-run-job",
		},
	}
	r := newAgentRunTestReconciler(t, run)

	_, err := r.reconcileRunning(context.Background(), logr.Discard(), run.DeepCopy())
	if err != nil {
		t.Fatalf("reconcileRunning returned error: %v", err)
	}

	var stored sympoziumv1alpha1.AgentRun
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatalf("get stored: %v", err)
	}
	if stored.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed {
		t.Errorf("expected phase Failed, got %q", stored.Status.Phase)
	}
	if stored.Status.Error != "Job not found" {
		t.Errorf("expected error 'Job not found', got %q", stored.Status.Error)
	}
}
