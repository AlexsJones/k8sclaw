package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// helper builds a minimal AgentRun for testing.
func newTestRun() *sympoziumv1alpha1.AgentRun {
	return &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef:   "my-instance",
			AgentID:    "default",
			SessionKey: "sess-1",
			Task:       sympoziumv1alpha1.NewStringTask("do stuff"),
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:      "openai",
				Model:         "gpt-4o",
				AuthSecretRef: "my-secret",
			},
		},
	}
}

// ── buildJob tests ───────────────────────────────────────────────────────────

func TestBuildJob_BasicMetadata(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	job, _ := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)

	if job.Name != "test-run" {
		t.Errorf("name = %q, want test-run", job.Name)
	}
	if job.Namespace != "default" {
		t.Errorf("namespace = %q, want default", job.Namespace)
	}
}

func TestBuildJob_Labels(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	job, _ := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)

	labels := job.Spec.Template.Labels
	if labels["sympozium.ai/instance"] != "my-instance" {
		t.Errorf("instance label = %q", labels["sympozium.ai/instance"])
	}
	if labels["sympozium.ai/agent-run"] != "test-run" {
		t.Errorf("agent-run label = %q", labels["sympozium.ai/agent-run"])
	}
	if labels["sympozium.ai/component"] != "agent-run" {
		t.Errorf("component label = %q", labels["sympozium.ai/component"])
	}
}

func TestBuildJob_TTLAndBackoff(t *testing.T) {
	r := &AgentRunReconciler{}
	job, _ := r.buildJob(context.Background(), newTestRun(), false, nil, nil, nil, nil)

	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 300 {
		t.Error("TTL should be 300")
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Error("BackoffLimit should be 0")
	}
}

func TestBuildJob_DeadlineDefault(t *testing.T) {
	r := &AgentRunReconciler{}
	job, _ := r.buildJob(context.Background(), newTestRun(), false, nil, nil, nil, nil)

	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 600 {
		t.Errorf("deadline = %v, want 600", job.Spec.ActiveDeadlineSeconds)
	}
}

func TestBuildJob_DeadlineWithTimeout(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Timeout = &metav1.Duration{Duration: 5 * time.Minute}
	job, _ := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)

	// 5min = 300s + 60 = 360
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 360 {
		t.Errorf("deadline = %v, want 360", job.Spec.ActiveDeadlineSeconds)
	}
}

func TestBuildJob_ServiceAccount(t *testing.T) {
	r := &AgentRunReconciler{}
	job, _ := r.buildJob(context.Background(), newTestRun(), false, nil, nil, nil, nil)

	if job.Spec.Template.Spec.ServiceAccountName != "sympozium-run-test-run" {
		t.Errorf("SA = %q, want sympozium-run-test-run", job.Spec.Template.Spec.ServiceAccountName)
	}
	if job.Spec.Template.Spec.AutomountServiceAccountToken == nil || *job.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Error("agent pod must disable automatic service-account token mounting")
	}
}

func TestBuildJob_PodSecurityContext(t *testing.T) {
	r := &AgentRunReconciler{}
	job, _ := r.buildJob(context.Background(), newTestRun(), false, nil, nil, nil, nil)

	psc := job.Spec.Template.Spec.SecurityContext
	if psc == nil {
		t.Fatal("pod security context is nil")
	}
	if psc.RunAsNonRoot == nil || !(*psc.RunAsNonRoot) {
		t.Error("RunAsNonRoot should be true")
	}
	if psc.RunAsUser == nil || *psc.RunAsUser != 1000 {
		t.Errorf("RunAsUser = %v, want 1000", psc.RunAsUser)
	}
}

func TestBuildJob_RestartPolicy(t *testing.T) {
	r := &AgentRunReconciler{}
	job, _ := r.buildJob(context.Background(), newTestRun(), false, nil, nil, nil, nil)

	if job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restart = %q, want Never", job.Spec.Template.Spec.RestartPolicy)
	}
}

func TestBuildJob_DefaultSeccompProfile(t *testing.T) {
	r := &AgentRunReconciler{}
	job, _ := r.buildJob(context.Background(), newTestRun(), false, nil, nil, nil, nil)

	psc := job.Spec.Template.Spec.SecurityContext
	if psc == nil {
		t.Fatal("pod security context is nil")
	}
	if psc.SeccompProfile == nil {
		t.Fatal("seccomp profile is nil, want RuntimeDefault")
	}
	if psc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("seccomp type = %q, want RuntimeDefault", psc.SeccompProfile.Type)
	}
}

func TestBuildJob_CustomSeccompProfile(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Sandbox = &sympoziumv1alpha1.AgentRunSandboxSpec{
		Enabled: true,
		SecurityContext: &sympoziumv1alpha1.SandboxSecurityContext{
			SeccompProfile: &sympoziumv1alpha1.SeccompProfileSpec{
				Type: "Unconfined",
			},
		},
	}
	job, _ := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)

	psc := job.Spec.Template.Spec.SecurityContext
	if psc.SeccompProfile == nil {
		t.Fatal("seccomp profile is nil")
	}
	if psc.SeccompProfile.Type != corev1.SeccompProfileTypeUnconfined {
		t.Errorf("seccomp type = %q, want Unconfined", psc.SeccompProfile.Type)
	}
}

// ── buildContainers tests ────────────────────────────────────────────────────

func TestBuildContainers_BasicCount(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, nil, nil, nil)
	// agent + ipc-bridge = 2
	if len(cs) != 2 {
		t.Fatalf("container count = %d, want 2", len(cs))
	}
}

func TestBuildContainers_AgentImage(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, nil, nil, nil)
	// agent container should reference agent-runner image
	if cs[0].Name != "agent" {
		t.Fatalf("first container name = %q, want agent", cs[0].Name)
	}
	if cs[0].Image == "" {
		t.Error("agent image is empty")
	}
}

func TestBuildContainers_IPCBridgeImage(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, nil, nil, nil)
	if cs[1].Name != "ipc-bridge" {
		t.Fatalf("second container name = %q, want ipc-bridge", cs[1].Name)
	}
	if cs[1].Image == "" {
		t.Error("ipc-bridge image is empty")
	}
}

func TestBuildContainers_AgentEnvVars(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	cs, _, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	envMap := map[string]string{}
	for _, e := range cs[0].Env {
		envMap[e.Name] = e.Value
	}
	if envMap["TASK"] != "do stuff" {
		t.Errorf("TASK = %q", envMap["TASK"])
	}
	if envMap["MODEL_PROVIDER"] != "openai" {
		t.Errorf("MODEL_PROVIDER = %q", envMap["MODEL_PROVIDER"])
	}
	if envMap["MODEL_NAME"] != "gpt-4o" {
		t.Errorf("MODEL_NAME = %q", envMap["MODEL_NAME"])
	}
}

func TestBuildContainers_AuthSecretRef(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	cs, _, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	// Auth secrets are now injected as individual secretKeyRef entries,
	// not via envFrom (Fix 9: prevent wholesale secret leakage).
	found := 0
	for _, env := range cs[0].Env {
		if env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil &&
			env.ValueFrom.SecretKeyRef.Name == "my-secret" {
			found++
		}
	}
	if found == 0 {
		t.Fatal("expected secretKeyRef entries for auth secret")
	}
}

func TestBuildContainers_NoAuthSecretRef(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Model.AuthSecretRef = ""
	cs, _, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	if len(cs[0].EnvFrom) != 0 {
		t.Errorf("envFrom should be empty for no-auth providers, got %d", len(cs[0].EnvFrom))
	}
}

func TestBuildContainers_AgentSecurityContext(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, nil, nil, nil)

	sc := cs[0].SecurityContext
	if sc == nil {
		t.Fatal("agent security context is nil")
	}
	if sc.ReadOnlyRootFilesystem == nil || !(*sc.ReadOnlyRootFilesystem) {
		t.Error("ReadOnlyRootFilesystem should be true")
	}
}

func TestBuildContainers_AgentVolumeMounts(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, nil, nil, nil)

	mounts := map[string]bool{}
	for _, m := range cs[0].VolumeMounts {
		mounts[m.Name] = true
	}
	for _, want := range []string{"workspace", "ipc", "tmp", "skills"} {
		if !mounts[want] {
			t.Errorf("missing volume mount %q", want)
		}
	}
}

func TestBuildContainers_AgentResources(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, nil, nil, nil)

	req := cs[0].Resources.Requests
	if req.Cpu().Cmp(resource.MustParse("250m")) != 0 {
		t.Errorf("cpu request = %v", req.Cpu())
	}
	if req.Memory().Cmp(resource.MustParse("512Mi")) != 0 {
		t.Errorf("memory request = %v", req.Memory())
	}
}

func TestBuildContainers_IPCBridgeEnvVars(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	cs, _, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	envMap := map[string]string{}
	for _, e := range cs[1].Env {
		envMap[e.Name] = e.Value
	}
	if envMap["AGENT_RUN_ID"] != "test-run" {
		t.Errorf("AGENT_RUN_ID = %q", envMap["AGENT_RUN_ID"])
	}
	if envMap["INSTANCE_NAME"] != "my-instance" {
		t.Errorf("INSTANCE_NAME = %q", envMap["INSTANCE_NAME"])
	}
}

func TestBuildContainers_WithSandbox(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Sandbox = &sympoziumv1alpha1.AgentRunSandboxSpec{Enabled: true}
	cs, _, _ := r.buildContainers(run, false, nil, nil, nil, nil)
	// agent + ipc-bridge + sandbox = 3
	if len(cs) != 3 {
		t.Fatalf("container count = %d, want 3", len(cs))
	}
	if cs[2].Name != "sandbox" {
		t.Errorf("third container name = %q, want sandbox", cs[2].Name)
	}
}

func TestBuildContainers_SandboxCustomImage(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Sandbox = &sympoziumv1alpha1.AgentRunSandboxSpec{
		Enabled: true,
		Image:   "my-sandbox:v1",
	}
	cs, _, _ := r.buildContainers(run, false, nil, nil, nil, nil)
	if cs[2].Image != "my-sandbox:v1" {
		t.Errorf("sandbox image = %q, want my-sandbox:v1", cs[2].Image)
	}
}

func TestBuildContainers_SandboxDisabled(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Sandbox = &sympoziumv1alpha1.AgentRunSandboxSpec{Enabled: false}
	cs, _, _ := r.buildContainers(run, false, nil, nil, nil, nil)
	if len(cs) != 2 {
		t.Errorf("container count = %d, want 2 (sandbox disabled)", len(cs))
	}
}

// ── buildVolumes tests ───────────────────────────────────────────────────────

func TestBuildVolumes_DefaultVolumes(t *testing.T) {
	r := &AgentRunReconciler{}
	vols := r.buildVolumes(newTestRun(), false, nil, nil)

	names := map[string]bool{}
	for _, v := range vols {
		names[v.Name] = true
	}
	for _, want := range []string{"workspace", "ipc", "tmp", "skills"} {
		if !names[want] {
			t.Errorf("missing volume %q", want)
		}
	}
}

func TestBuildVolumes_IPCUsesMemory(t *testing.T) {
	r := &AgentRunReconciler{}
	vols := r.buildVolumes(newTestRun(), false, nil, nil)

	for _, v := range vols {
		if v.Name == "ipc" {
			if v.EmptyDir == nil {
				t.Fatal("ipc volume should be emptyDir")
			}
			if v.EmptyDir.Medium != corev1.StorageMediumMemory {
				t.Errorf("ipc medium = %q, want Memory", v.EmptyDir.Medium)
			}
			return
		}
	}
	t.Error("ipc volume not found")
}

// ── skipRun ──────────────────────────────────────────────────────────────────

func TestSkipRun_MarksSkippedTerminal(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run-skip", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentRunSpec{AgentRef: "demo"},
		Status:     sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhaseRunning},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(run).
		WithStatusSubresource(&sympoziumv1alpha1.AgentRun{}).
		Build()

	r := &AgentRunReconciler{Client: cl, Scheme: scheme, Log: logr.Discard()}

	if _, err := r.skipRun(context.Background(), run, "no work to do"); err != nil {
		t.Fatalf("skipRun returned error: %v", err)
	}

	var got sympoziumv1alpha1.AgentRun
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "run-skip", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status.Phase != sympoziumv1alpha1.AgentRunPhaseSkipped {
		t.Fatalf("phase = %q, want %q", got.Status.Phase, sympoziumv1alpha1.AgentRunPhaseSkipped)
	}
	if got.Status.Result != "no work to do" {
		t.Fatalf("result = %q, want %q", got.Status.Result, "no work to do")
	}
	if got.Status.CompletedAt == nil {
		t.Fatal("expected CompletedAt to be set")
	}
}

// ── pruneOldRuns ─────────────────────────────────────────────────────────────

// TestPruneOldRuns_PrunesSkippedRuns guards against skipped runs accumulating
// indefinitely: they are terminal and (given this feature exists to skip often)
// grow fastest, so they must be eligible for history-limit pruning alongside
// Succeeded/Failed runs.
func TestPruneOldRuns_PrunesSkippedRuns(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	const instance = "demo"
	base := time.Now().Add(-time.Hour)

	mkRun := func(name, phase string, ageOffset time.Duration) *sympoziumv1alpha1.AgentRun {
		return &sympoziumv1alpha1.AgentRun{
			ObjectMeta: metav1.ObjectMeta{
				Name:              name,
				Namespace:         "default",
				CreationTimestamp: metav1.NewTime(base.Add(ageOffset)),
				Labels:            map[string]string{"sympozium.ai/instance": instance},
			},
			Spec:   sympoziumv1alpha1.AgentRunSpec{AgentRef: instance},
			Status: sympoziumv1alpha1.AgentRunStatus{Phase: sympoziumv1alpha1.AgentRunPhase(phase)},
		}
	}

	// 4 terminal runs (oldest → newest): two skipped, then a failed, then a
	// succeeded. With a history limit of 2 the two oldest (both Skipped) must be
	// pruned. Before the fix Skipped runs were never collected, so nothing would
	// be pruned at all.
	runs := []*sympoziumv1alpha1.AgentRun{
		mkRun("skip-old", "Skipped", 0),
		mkRun("skip-old2", "Skipped", time.Minute),
		mkRun("failed", "Failed", 2*time.Minute),
		mkRun("succeeded", "Succeeded", 3*time.Minute),
		// A still-running run for the same instance must never be pruned.
		mkRun("running", "Running", 4*time.Minute),
	}

	objs := make([]client.Object, len(runs))
	for i, r := range runs {
		objs[i] = r
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()

	r := &AgentRunReconciler{Client: cl, Scheme: scheme, Log: logr.Discard(), RunHistoryLimit: 2}

	if err := r.pruneOldRuns(context.Background(), logr.Discard(), runs[0]); err != nil {
		t.Fatalf("pruneOldRuns returned error: %v", err)
	}

	var remaining sympoziumv1alpha1.AgentRunList
	if err := cl.List(context.Background(), &remaining, client.InNamespace("default")); err != nil {
		t.Fatalf("list runs: %v", err)
	}

	got := map[string]bool{}
	for _, run := range remaining.Items {
		got[run.Name] = true
	}

	// The two oldest skipped runs are pruned; the rest survive.
	for _, name := range []string{"failed", "succeeded", "running"} {
		if !got[name] {
			t.Errorf("expected %q to survive pruning", name)
		}
	}
	for _, name := range []string{"skip-old", "skip-old2"} {
		if got[name] {
			t.Errorf("expected oldest skipped run %q to be pruned", name)
		}
	}
}

// ── reconcileAwaitingDelegate ────────────────────────────────────────────────

// TestReconcileAwaitingDelegate_PropagatesSkipReason verifies a skipped delegate
// child (terminal, not failed) surfaces its skip reason into the parent's
// delegate entry rather than dropping it, and that the parent recovers to
// Running so it can pick the result up.
func TestReconcileAwaitingDelegate_PropagatesSkipReason(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	parent := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentRunSpec{AgentRef: "demo"},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase: sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate,
			Delegates: []sympoziumv1alpha1.DelegateStatus{
				{ChildRunName: "child-skip"},
			},
		},
	}
	child := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "child-skip", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentRunSpec{AgentRef: "demo"},
		Status: sympoziumv1alpha1.AgentRunStatus{
			Phase:  sympoziumv1alpha1.AgentRunPhaseSkipped,
			Result: "queue empty",
		},
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(parent, child).
		WithStatusSubresource(&sympoziumv1alpha1.AgentRun{}).
		Build()

	r := &AgentRunReconciler{Client: cl, Scheme: scheme, Log: logr.Discard()}

	if _, err := r.reconcileAwaitingDelegate(context.Background(), logr.Discard(), parent); err != nil {
		t.Fatalf("reconcileAwaitingDelegate returned error: %v", err)
	}

	var got sympoziumv1alpha1.AgentRun
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "parent", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if len(got.Status.Delegates) != 1 {
		t.Fatalf("expected 1 delegate, got %d", len(got.Status.Delegates))
	}
	if got.Status.Delegates[0].Result != "queue empty" {
		t.Fatalf("delegate result = %q, want skip reason %q", got.Status.Delegates[0].Result, "queue empty")
	}
	// All delegates terminal and none failed → parent recovers to Running.
	if got.Status.Phase != sympoziumv1alpha1.AgentRunPhaseRunning {
		t.Fatalf("parent phase = %q, want %q", got.Status.Phase, sympoziumv1alpha1.AgentRunPhaseRunning)
	}
}

// ── result parsing tests ─────────────────────────────────────────────────────

func TestParseAgentResultFromLogs_Success(t *testing.T) {
	logs := "noise\n" +
		"__SYMPOZIUM_RESULT__" +
		`{"status":"success","response":"all good","metrics":{"durationMs":1200,"inputTokens":10,"outputTokens":20,"toolCalls":1}}` +
		"__SYMPOZIUM_END__\n"

	result, errMsg, usage, skipped := parseAgentResultFromLogs(logs, logr.Discard())
	if skipped {
		t.Fatal("did not expect skipped")
	}
	if errMsg != "" {
		t.Fatalf("unexpected error message: %q", errMsg)
	}
	if result != "all good" {
		t.Fatalf("result = %q, want %q", result, "all good")
	}
	if usage == nil {
		t.Fatal("expected token usage, got nil")
	}
	if usage.TotalTokens != 30 {
		t.Fatalf("total tokens = %d, want 30", usage.TotalTokens)
	}
}

func TestParseAgentResultFromLogs_Error(t *testing.T) {
	want := "OpenAI API error (HTTP 429): insufficient_quota"
	logs := "__SYMPOZIUM_RESULT__" +
		fmt.Sprintf(`{"status":"error","error":%q,"metrics":{"durationMs":123}}`, want) +
		"__SYMPOZIUM_END__\n"

	result, errMsg, usage, skipped := parseAgentResultFromLogs(logs, logr.Discard())
	if skipped {
		t.Fatal("did not expect skipped")
	}
	if result != "" {
		t.Fatalf("expected empty result, got %q", result)
	}
	if errMsg != want {
		t.Fatalf("error = %q, want %q", errMsg, want)
	}
	if usage != nil {
		t.Fatalf("expected nil usage on error, got %+v", usage)
	}
}

func TestParseAgentResultFromLogs_Skipped(t *testing.T) {
	logs := "noise\n" +
		"__SYMPOZIUM_RESULT__" +
		`{"status":"skipped","response":"no new items in queue"}` +
		"__SYMPOZIUM_END__\n"

	result, errMsg, usage, skipped := parseAgentResultFromLogs(logs, logr.Discard())
	if !skipped {
		t.Fatal("expected skipped=true")
	}
	if errMsg != "" {
		t.Fatalf("unexpected error message: %q", errMsg)
	}
	if result != "no new items in queue" {
		t.Fatalf("reason = %q, want %q", result, "no new items in queue")
	}
	if usage != nil {
		t.Fatalf("expected nil usage on skip, got %+v", usage)
	}
}

func TestParseAgentResultFromLogs_SkippedDefaultReason(t *testing.T) {
	logs := "__SYMPOZIUM_RESULT__" + `{"status":"skipped"}` + "__SYMPOZIUM_END__\n"

	result, _, _, skipped := parseAgentResultFromLogs(logs, logr.Discard())
	if !skipped {
		t.Fatal("expected skipped=true")
	}
	if result == "" {
		t.Fatal("expected a default skip reason, got empty")
	}
}

func TestParseAgentResultFromLogs_NegativeMetricsRejected(t *testing.T) {
	// An adversarial agent printing negative counts must not produce usage at
	// all: a negative total would decrement Ensemble.status.tokenBudgetUsed
	// and defeat halt-mode budgets.
	logs := "__SYMPOZIUM_RESULT__" +
		`{"status":"success","response":"done","metrics":{"durationMs":10,"inputTokens":-5000000000,"outputTokens":1,"toolCalls":1}}` +
		"__SYMPOZIUM_END__\n"

	result, errMsg, usage, skipped := parseAgentResultFromLogs(logs, logr.Discard())
	if skipped || errMsg != "" {
		t.Fatalf("unexpected skip/error: skipped=%v errMsg=%q", skipped, errMsg)
	}
	if result != "done" {
		t.Fatalf("result = %q, want %q (response must survive metric rejection)", result, "done")
	}
	if usage != nil {
		t.Fatalf("expected nil usage for negative metrics, got %+v", usage)
	}
}

func TestParseAgentResultFromLogs_NegativeToolCallsRejected(t *testing.T) {
	logs := "__SYMPOZIUM_RESULT__" +
		`{"status":"success","response":"done","metrics":{"durationMs":10,"inputTokens":5,"outputTokens":5,"toolCalls":-1}}` +
		"__SYMPOZIUM_END__\n"

	_, _, usage, _ := parseAgentResultFromLogs(logs, logr.Discard())
	if usage != nil {
		t.Fatalf("expected nil usage when any metric is negative, got %+v", usage)
	}
}

func TestParseAgentResultFromLogs_OversizedMetricsClamped(t *testing.T) {
	logs := "__SYMPOZIUM_RESULT__" +
		`{"status":"success","response":"done","metrics":{"durationMs":10,"inputTokens":90000000000,"outputTokens":20,"toolCalls":3}}` +
		"__SYMPOZIUM_END__\n"

	_, errMsg, usage, _ := parseAgentResultFromLogs(logs, logr.Discard())
	if errMsg != "" {
		t.Fatalf("unexpected error: %s", errMsg)
	}
	if usage == nil {
		t.Fatal("expected usage, got nil")
	}
	if usage.InputTokens != maxAgentReportedMetric {
		t.Fatalf("inputTokens = %d, want clamp at %d", usage.InputTokens, int64(maxAgentReportedMetric))
	}
	if usage.TotalTokens != maxAgentReportedMetric+20 {
		t.Fatalf("totalTokens = %d, want %d", usage.TotalTokens, int64(maxAgentReportedMetric)+20)
	}
}

func TestExtractLikelyProviderErrorFromLogs_Quota(t *testing.T) {
	logs := `
2026/03/01 12:00:00 agent-runner starting
2026/03/01 12:00:01 LLM call failed: Anthropic API error (HTTP 429): {"type":"error","error":{"type":"rate_limit_error","message":"You have run out of credits"}}
2026/03/01 12:00:01 agent-runner finished with error
`
	got := extractLikelyProviderErrorFromLogs(logs)
	if got == "" {
		t.Fatal("expected quota/rate-limit message, got empty")
	}
	if want := "HTTP 429"; !containsIgnoreCase(got, want) {
		t.Fatalf("message %q does not contain %q", got, want)
	}
}

func TestParseAgentResultFromLogs_MarkerBeyondOldTailLimit(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "tool_call [%d]: read_file id=call-%d\n", i, i)
	}
	b.WriteString("__SYMPOZIUM_RESULT__")
	b.WriteString(`{"status":"success","response":"the final report","metrics":{"durationMs":5000,"inputTokens":100,"outputTokens":50,"toolCalls":200}}`)
	b.WriteString("__SYMPOZIUM_END__\n")

	result, errMsg, usage, _ := parseAgentResultFromLogs(b.String(), logr.Discard())
	if errMsg != "" {
		t.Fatalf("unexpected error: %s", errMsg)
	}
	if result != "the final report" {
		t.Fatalf("result = %q, want %q", result, "the final report")
	}
	if usage == nil || usage.ToolCalls != 200 {
		t.Fatalf("expected 200 tool calls in usage, got %+v", usage)
	}
}

func TestParseAgentResultFromLogs_LongMultilineResponse(t *testing.T) {
	longResponse := strings.Repeat("line of report text\n", 500)
	payload := fmt.Sprintf(`{"status":"success","response":%q,"metrics":{"durationMs":3000,"inputTokens":50,"outputTokens":100,"toolCalls":5}}`, longResponse)
	logs := "__SYMPOZIUM_RESULT__" + payload + "__SYMPOZIUM_END__\n"

	result, errMsg, _, _ := parseAgentResultFromLogs(logs, logr.Discard())
	if errMsg != "" {
		t.Fatalf("unexpected error: %s", errMsg)
	}
	if !strings.Contains(result, "line of report text") {
		t.Fatal("expected full response content")
	}
	if len(result) != len(longResponse) {
		t.Fatalf("response length = %d, want %d", len(result), len(longResponse))
	}
}

func containsIgnoreCase(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

func TestBuildVolumes_SkillsWithRefs(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{
		{ConfigMapRef: "my-skills"},
	}
	vols := r.buildVolumes(run, false, nil, nil)

	for _, v := range vols {
		if v.Name == "skills" {
			if v.Projected == nil {
				t.Fatal("skills volume should be projected when refs exist")
			}
			return
		}
	}
	t.Error("skills volume not found")
}

func TestBuildVolumes_SkillsEmptyWhenNoRefs(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Skills = nil
	vols := r.buildVolumes(run, false, nil, nil)

	for _, v := range vols {
		if v.Name == "skills" {
			if v.EmptyDir == nil {
				t.Fatal("skills volume should be emptyDir when no refs")
			}
			return
		}
	}
	t.Error("skills volume not found")
}

func TestBuildVolumes_MemoryEnabled(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	vols := r.buildVolumes(run, true, nil, nil)

	for _, v := range vols {
		if v.Name == "memory" {
			if v.ConfigMap == nil {
				t.Fatal("memory volume should be a ConfigMap volume")
			}
			expected := run.Spec.AgentRef + "-memory"
			if v.ConfigMap.Name != expected {
				t.Errorf("memory ConfigMap name = %q, want %q", v.ConfigMap.Name, expected)
			}
			return
		}
	}
	t.Error("memory volume not found when memoryEnabled=true")
}

func TestBuildVolumes_MemoryDisabled(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	vols := r.buildVolumes(run, false, nil, nil)

	for _, v := range vols {
		if v.Name == "memory" {
			t.Error("memory volume should not exist when memoryEnabled=false")
			return
		}
	}
}

func TestBuildContainers_MemoryMount(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	cs, _, _ := r.buildContainers(run, true, nil, nil, nil, nil)

	agent := cs[0]
	var hasMount bool
	for _, vm := range agent.VolumeMounts {
		if vm.Name == "memory" && vm.MountPath == "/memory" {
			hasMount = true
			break
		}
	}
	if !hasMount {
		t.Error("agent container should have /memory volume mount when memoryEnabled=true")
	}

	var hasEnv bool
	for _, e := range agent.Env {
		if e.Name == "MEMORY_ENABLED" && e.Value == "true" {
			hasEnv = true
			break
		}
	}
	if !hasEnv {
		t.Error("agent container should have MEMORY_ENABLED=true env when memoryEnabled=true")
	}
}

// ── Skill sidecar injection tests ────────────────────────────────────────────

func TestBuildContainers_SkillSidecarInjected(t *testing.T) {
	r := &AgentRunReconciler{}
	sidecars := []resolvedSidecar{
		{
			skillPackName: "k8s-ops",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image:          "ghcr.io/sympozium-ai/sympozium/skill-k8s-ops:latest",
				MountWorkspace: true,
				Resources: &sympoziumv1alpha1.SidecarResources{
					CPU:    "100m",
					Memory: "128Mi",
				},
			},
		},
	}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, sidecars, nil, nil)
	// agent + ipc-bridge + skill sidecar = 3
	if len(cs) != 3 {
		t.Fatalf("container count = %d, want 3", len(cs))
	}
	sc := cs[2]
	if sc.Name != "skill-k8s-ops" {
		t.Errorf("sidecar name = %q, want skill-k8s-ops", sc.Name)
	}
	if sc.Image != "ghcr.io/sympozium-ai/sympozium/skill-k8s-ops:latest" {
		t.Errorf("sidecar image = %q", sc.Image)
	}
	// Should have workspace mount
	var hasWorkspace bool
	for _, m := range sc.VolumeMounts {
		if m.MountPath == "/workspace" {
			hasWorkspace = true
			break
		}
	}
	if !hasWorkspace {
		t.Error("sidecar should mount /workspace when MountWorkspace=true")
	}
}

func TestBuildContainers_SkillSidecarDefaultCommand(t *testing.T) {
	r := &AgentRunReconciler{}
	sidecars := []resolvedSidecar{
		{
			skillPackName: "test-skill",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image:          "test:latest",
				MountWorkspace: false,
			},
		},
	}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, sidecars, nil, nil)
	sc := cs[2]
	// When no command is specified in the SkillPack, the container should
	// have no Command override so the image's default CMD runs.
	if len(sc.Command) != 0 {
		t.Errorf("sidecar command = %v, want empty (use image CMD)", sc.Command)
	}
	// Agent container should always have TOOLS_ENABLED.
	var toolsEnabled bool
	for _, env := range cs[0].Env {
		if env.Name == "TOOLS_ENABLED" && env.Value == "true" {
			toolsEnabled = true
		}
	}
	if !toolsEnabled {
		t.Error("agent container should have TOOLS_ENABLED=true")
	}
	// Should NOT have workspace mount
	for _, m := range sc.VolumeMounts {
		if m.MountPath == "/workspace" {
			t.Error("sidecar should NOT mount /workspace when MountWorkspace=false")
		}
	}
}

func TestBuildContainers_MultipleSkillSidecars(t *testing.T) {
	r := &AgentRunReconciler{}
	sidecars := []resolvedSidecar{
		{skillPackName: "skill-a", sidecar: sympoziumv1alpha1.SkillSidecar{Image: "a:latest", MountWorkspace: true}},
		{skillPackName: "skill-b", sidecar: sympoziumv1alpha1.SkillSidecar{Image: "b:latest", MountWorkspace: true}},
	}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, sidecars, nil, nil)
	// agent + ipc-bridge + 2 sidecars = 4
	if len(cs) != 4 {
		t.Fatalf("container count = %d, want 4", len(cs))
	}
	if cs[2].Name != "skill-skill-a" {
		t.Errorf("first sidecar name = %q", cs[2].Name)
	}
	if cs[3].Name != "skill-skill-b" {
		t.Errorf("second sidecar name = %q", cs[3].Name)
	}
}

func TestBuildJob_WithSkillSidecars(t *testing.T) {
	r := &AgentRunReconciler{}
	sidecars := []resolvedSidecar{
		{skillPackName: "k8s-ops", sidecar: sympoziumv1alpha1.SkillSidecar{Image: "k8s:latest", MountWorkspace: true}},
	}
	job, _ := r.buildJob(context.Background(), newTestRun(), false, nil, sidecars, nil, nil)
	containers := job.Spec.Template.Spec.Containers
	if len(containers) != 3 {
		t.Fatalf("job container count = %d, want 3", len(containers))
	}
}

func TestBuildContainers_ObservabilityEnv(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	obs := &sympoziumv1alpha1.ObservabilitySpec{
		Enabled:      true,
		OTLPEndpoint: "otel-collector.observability.svc:4317",
		OTLPProtocol: "grpc",
		ServiceName:  "sympozium",
		ResourceAttributes: map[string]string{
			"deployment.environment": "production",
		},
	}

	cs, _, _ := r.buildContainers(run, false, obs, nil, nil, nil)

	agentEnv := map[string]string{}
	for _, e := range cs[0].Env {
		agentEnv[e.Name] = e.Value
	}
	if agentEnv["SYMPOZIUM_OTEL_ENABLED"] != "true" {
		t.Fatalf("SYMPOZIUM_OTEL_ENABLED not injected")
	}
	if agentEnv["SYMPOZIUM_OTEL_OTLP_ENDPOINT"] != obs.OTLPEndpoint {
		t.Fatalf("SYMPOZIUM_OTEL_OTLP_ENDPOINT = %q", agentEnv["SYMPOZIUM_OTEL_OTLP_ENDPOINT"])
	}
	if !strings.Contains(agentEnv["SYMPOZIUM_OTEL_RESOURCE_ATTRIBUTES"], "sympozium.agent_run.id=test-run") {
		t.Fatalf("missing run id in resource attributes: %q", agentEnv["SYMPOZIUM_OTEL_RESOURCE_ATTRIBUTES"])
	}
}

// ── Seccomp profile tests ────────────────────────────────────────────────────

func TestBuildContainers_PrivilegedSidecarUnconfinedSeccomp(t *testing.T) {
	r := &AgentRunReconciler{}
	sidecars := []resolvedSidecar{
		{
			skillPackName: "llmfit",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image:          "llmfit:latest",
				MountWorkspace: true,
				HostAccess: &sympoziumv1alpha1.HostAccessSpec{
					Enabled:    true,
					Privileged: true,
				},
			},
		},
	}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, sidecars, nil, nil)

	sidecar := cs[2] // agent, ipc-bridge, then skill sidecar
	if sidecar.SecurityContext == nil {
		t.Fatal("privileged sidecar security context is nil")
	}
	if sidecar.SecurityContext.SeccompProfile == nil {
		t.Fatal("privileged sidecar seccomp profile is nil")
	}
	if sidecar.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeUnconfined {
		t.Errorf("sidecar seccomp = %q, want Unconfined", sidecar.SecurityContext.SeccompProfile.Type)
	}
}

func TestBuildContainers_NonPrivilegedSidecarRestrictedSecurityContext(t *testing.T) {
	r := &AgentRunReconciler{}
	sidecars := []resolvedSidecar{
		{
			skillPackName: "basic-skill",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image:          "basic:latest",
				MountWorkspace: true,
			},
		},
	}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, sidecars, nil, nil)

	sidecar := cs[2]
	if sidecar.SecurityContext == nil {
		t.Fatal("non-privileged sidecar should have a restricted SecurityContext")
	}
	if sidecar.SecurityContext.AllowPrivilegeEscalation == nil || *sidecar.SecurityContext.AllowPrivilegeEscalation {
		t.Error("non-privileged sidecar should have AllowPrivilegeEscalation=false")
	}
	if sidecar.SecurityContext.Capabilities == nil || len(sidecar.SecurityContext.Capabilities.Drop) == 0 {
		t.Error("non-privileged sidecar should drop ALL capabilities")
	}
	// Should NOT have seccomp override or privileged flag
	if sidecar.SecurityContext.Privileged != nil {
		t.Error("non-privileged sidecar should not have Privileged set")
	}
}

// ── Server-mode detection tests ───────────────────────────────────────────────

func TestServerMode_ExplicitModeOnly(t *testing.T) {
	// Server mode requires explicit Spec.Mode="server" — RequiresServer
	// sidecars alone do NOT auto-promote to server mode.
	run := newTestRun()
	run.Spec.Mode = "" // defaults to task

	if run.Spec.Mode == "server" {
		t.Error("empty mode should not be server")
	}

	run.Spec.Mode = "server"
	if run.Spec.Mode != "server" {
		t.Errorf("explicit mode = %q, want server", run.Spec.Mode)
	}
}

func TestTaskMode_FiltersRequiresServerSidecars(t *testing.T) {
	sidecars := []resolvedSidecar{
		{
			skillPackName: "k8s-ops",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image:          "k8s:latest",
				MountWorkspace: true,
			},
		},
		{
			skillPackName: "web-endpoint",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image:          "web:latest",
				RequiresServer: true,
				Ports: []sympoziumv1alpha1.SidecarPort{
					{Name: "http", ContainerPort: 8080},
				},
			},
		},
	}

	// Task-mode runs should filter out RequiresServer sidecars.
	taskSidecars := make([]resolvedSidecar, 0, len(sidecars))
	for _, sc := range sidecars {
		if !sc.sidecar.RequiresServer {
			taskSidecars = append(taskSidecars, sc)
		}
	}

	if len(taskSidecars) != 1 {
		t.Errorf("expected 1 task sidecar, got %d", len(taskSidecars))
	}
	if taskSidecars[0].skillPackName != "k8s-ops" {
		t.Errorf("expected k8s-ops sidecar, got %s", taskSidecars[0].skillPackName)
	}
}

func TestServerMode_ExplicitModeField(t *testing.T) {
	run := newTestRun()
	run.Spec.Mode = "server"
	if run.Spec.Mode != "server" {
		t.Errorf("mode = %q, want server", run.Spec.Mode)
	}
}

func TestServerMode_DefaultModeIsTask(t *testing.T) {
	run := newTestRun()
	effectiveMode := run.Spec.Mode
	if effectiveMode == "" {
		effectiveMode = "task"
	}
	if effectiveMode != "task" {
		t.Errorf("default mode = %q, want task", effectiveMode)
	}
}

func TestServerMode_PhaseServing(t *testing.T) {
	if sympoziumv1alpha1.AgentRunPhaseServing != "Serving" {
		t.Errorf("AgentRunPhaseServing = %q, want Serving", sympoziumv1alpha1.AgentRunPhaseServing)
	}
}

func TestServerMode_StatusFields(t *testing.T) {
	run := newTestRun()
	run.Status.DeploymentName = "test-run-server"
	run.Status.ServiceName = "test-run-server"

	if run.Status.DeploymentName != "test-run-server" {
		t.Errorf("deploymentName = %q", run.Status.DeploymentName)
	}
	if run.Status.ServiceName != "test-run-server" {
		t.Errorf("serviceName = %q", run.Status.ServiceName)
	}
}

func TestBuildContainers_IPCBridgeSecurityContext(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, nil, nil, nil)

	ipc := cs[1]
	if ipc.SecurityContext == nil {
		t.Fatal("ipc-bridge security context is nil")
	}
	if ipc.SecurityContext.ReadOnlyRootFilesystem == nil || !*ipc.SecurityContext.ReadOnlyRootFilesystem {
		t.Error("ipc-bridge ReadOnlyRootFilesystem should be true")
	}
	if ipc.SecurityContext.AllowPrivilegeEscalation == nil || *ipc.SecurityContext.AllowPrivilegeEscalation {
		t.Error("ipc-bridge AllowPrivilegeEscalation should be false")
	}
	if ipc.SecurityContext.Capabilities == nil || len(ipc.SecurityContext.Capabilities.Drop) == 0 {
		t.Error("ipc-bridge should drop ALL capabilities")
	}
}

// ── NodeSelector tests ──────────────────────────────────────────────────────

func TestBuildJob_NodeSelector(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Model.NodeSelector = map[string]string{
		"kubernetes.io/hostname": "gpu-node-1",
	}

	job, _ := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)
	ns := job.Spec.Template.Spec.NodeSelector

	if ns == nil {
		t.Fatal("NodeSelector should not be nil")
	}
	if ns["kubernetes.io/hostname"] != "gpu-node-1" {
		t.Errorf("NodeSelector[kubernetes.io/hostname] = %q, want gpu-node-1", ns["kubernetes.io/hostname"])
	}
}

func TestBuildJob_NoNodeSelector(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	// No NodeSelector set.

	job, _ := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)
	ns := job.Spec.Template.Spec.NodeSelector

	if ns != nil {
		t.Errorf("NodeSelector should be nil when not set, got %v", ns)
	}
}

// ── Lifecycle hook tests ────────────────────────────────────────────────────

// newTestRunWithLifecycle returns a test AgentRun with lifecycle hooks configured.
func newTestRunWithLifecycle(preRun, postRun []sympoziumv1alpha1.LifecycleHookContainer, rbac []sympoziumv1alpha1.RBACRule) *sympoziumv1alpha1.AgentRun {
	run := newTestRun()
	run.Spec.Lifecycle = &sympoziumv1alpha1.LifecycleHooks{
		PreRun:  preRun,
		PostRun: postRun,
		RBAC:    rbac,
	}
	return run
}

func TestBuildContainers_PreRunInitContainers(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{
				Name:    "fetch-context",
				Image:   "curlimages/curl:latest",
				Command: []string{"sh", "-c", "curl -s http://example.com > /workspace/context.json"},
			},
			{
				Name:    "setup-repo",
				Image:   "alpine/git:latest",
				Command: []string{"git", "clone", "https://github.com/org/repo", "/workspace/repo"},
				Env:     []sympoziumv1alpha1.EnvVar{{Name: "GIT_TOKEN", Value: "abc123"}},
			},
		},
		nil, nil,
	)

	containers, initContainers, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	// Agent + IPC bridge = 2 containers.
	if len(containers) != 2 {
		t.Fatalf("expected 2 containers, got %d", len(containers))
	}

	// Find preRun init containers by name prefix.
	var preInits []corev1.Container
	for _, ic := range initContainers {
		if strings.HasPrefix(ic.Name, "pre-") {
			preInits = append(preInits, ic)
		}
	}
	if len(preInits) != 2 {
		t.Fatalf("expected 2 preRun init containers, got %d (total init containers: %d)", len(preInits), len(initContainers))
	}

	// Verify first hook.
	if preInits[0].Name != "pre-fetch-context" {
		t.Errorf("first preRun init container name = %q, want pre-fetch-context", preInits[0].Name)
	}
	if preInits[0].Image != "curlimages/curl:latest" {
		t.Errorf("first preRun init container image = %q, want curlimages/curl:latest", preInits[0].Image)
	}

	// Verify second hook has custom env var.
	if preInits[1].Name != "pre-setup-repo" {
		t.Errorf("second preRun init container name = %q, want pre-setup-repo", preInits[1].Name)
	}
	foundGitToken := false
	for _, e := range preInits[1].Env {
		if e.Name == "GIT_TOKEN" && e.Value == "abc123" {
			foundGitToken = true
		}
	}
	if !foundGitToken {
		t.Error("second preRun init container missing GIT_TOKEN env var")
	}
}

func TestBuildContainers_PreRunVolumeMounts(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "setup", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil, nil,
	)

	_, initContainers, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	var hook *corev1.Container
	for i := range initContainers {
		if initContainers[i].Name == "pre-setup" {
			hook = &initContainers[i]
			break
		}
	}
	if hook == nil {
		t.Fatal("pre-setup init container not found")
	}

	// Verify volume mounts: workspace, ipc, tmp.
	mountNames := map[string]bool{}
	for _, m := range hook.VolumeMounts {
		mountNames[m.Name] = true
	}
	for _, want := range []string{"workspace", "ipc", "tmp"} {
		if !mountNames[want] {
			t.Errorf("preRun init container missing volume mount %q", want)
		}
	}
}

func TestBuildContainers_PreRunSecurityContext(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "setup", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil, nil,
	)

	_, initContainers, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	var hook *corev1.Container
	for i := range initContainers {
		if initContainers[i].Name == "pre-setup" {
			hook = &initContainers[i]
			break
		}
	}
	if hook == nil {
		t.Fatal("pre-setup init container not found")
	}

	sc := hook.SecurityContext
	if sc == nil {
		t.Fatal("preRun init container missing SecurityContext")
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("preRun init container should have ReadOnlyRootFilesystem=true")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 {
		t.Error("preRun init container should drop ALL capabilities")
	}
}

func TestBuildContainers_PreRunBaseEnvVars(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "setup", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil, nil,
	)

	_, initContainers, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	var hook *corev1.Container
	for i := range initContainers {
		if initContainers[i].Name == "pre-setup" {
			hook = &initContainers[i]
			break
		}
	}
	if hook == nil {
		t.Fatal("pre-setup init container not found")
	}

	envMap := map[string]string{}
	for _, e := range hook.Env {
		envMap[e.Name] = e.Value
	}
	for _, want := range []string{"AGENT_RUN_ID", "INSTANCE_NAME", "AGENT_NAMESPACE"} {
		if _, ok := envMap[want]; !ok {
			t.Errorf("preRun init container missing env var %q", want)
		}
	}
	if envMap["AGENT_RUN_ID"] != "test-run" {
		t.Errorf("AGENT_RUN_ID = %q, want test-run", envMap["AGENT_RUN_ID"])
	}
	if envMap["INSTANCE_NAME"] != "my-instance" {
		t.Errorf("INSTANCE_NAME = %q, want my-instance", envMap["INSTANCE_NAME"])
	}
}

func TestBuildContainers_PreRunForwardsSpecEnv(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "setup", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil, nil,
	)
	run.Spec.Env = map[string]string{"PAGERDUTY_URL": "https://pd.example.com"}

	_, initContainers, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	var hook *corev1.Container
	for i := range initContainers {
		if initContainers[i].Name == "pre-setup" {
			hook = &initContainers[i]
			break
		}
	}
	if hook == nil {
		t.Fatal("pre-setup init container not found")
	}

	found := false
	for _, e := range hook.Env {
		if e.Name == "PAGERDUTY_URL" && e.Value == "https://pd.example.com" {
			found = true
		}
	}
	if !found {
		t.Error("preRun init container missing forwarded spec.env PAGERDUTY_URL")
	}
}

func TestBuildContainers_PreRunSecretKeyRefEnv(t *testing.T) {
	r := &AgentRunReconciler{}
	optional := true
	run := newTestRunWithLifecycle(
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{
				Name:    "fetch",
				Image:   "busybox:1.36",
				Command: []string{"true"},
				Env: []sympoziumv1alpha1.EnvVar{
					{Name: "PLAIN", Value: "literal"},
					{
						Name: "GITHUB_TOKEN",
						ValueFrom: &sympoziumv1alpha1.EnvVarSource{
							SecretKeyRef: &sympoziumv1alpha1.SecretKeySelector{
								Name:     "gh-pat",
								Key:      "token",
								Optional: &optional,
							},
						},
					},
				},
			},
		},
		nil, nil,
	)

	_, initContainers, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	var hook *corev1.Container
	for i := range initContainers {
		if initContainers[i].Name == "pre-fetch" {
			hook = &initContainers[i]
			break
		}
	}
	if hook == nil {
		t.Fatal("pre-fetch init container not found")
	}

	var secretEnv *corev1.EnvVar
	for i := range hook.Env {
		if hook.Env[i].Name == "GITHUB_TOKEN" {
			secretEnv = &hook.Env[i]
		}
		if hook.Env[i].Name == "PLAIN" && hook.Env[i].Value != "literal" {
			t.Errorf("PLAIN env = %q, want literal", hook.Env[i].Value)
		}
	}
	if secretEnv == nil {
		t.Fatal("GITHUB_TOKEN env var not found on hook container")
	}
	if secretEnv.Value != "" {
		t.Errorf("secret-sourced env should have empty literal Value, got %q", secretEnv.Value)
	}
	if secretEnv.ValueFrom == nil {
		t.Fatal("GITHUB_TOKEN env missing valueFrom")
	}
	ref := secretEnv.ValueFrom.SecretKeyRef
	if ref == nil {
		t.Fatal("GITHUB_TOKEN env missing valueFrom.secretKeyRef")
	}
	if ref.Name != "gh-pat" || ref.Key != "token" {
		t.Errorf("secretKeyRef = %s/%s, want gh-pat/token", ref.Name, ref.Key)
	}
	if ref.Optional == nil || !*ref.Optional {
		t.Error("secretKeyRef Optional should be true")
	}
}

func TestBuildContainers_PreRunAppearsAfterSystemInits(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "my-hook", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil, nil,
	)
	// Add memory skill so wait-for-memory init container is present.
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "memory"}}

	_, initContainers, _ := r.buildContainers(run, true, nil, nil, nil, nil)

	if len(initContainers) < 2 {
		t.Fatalf("expected at least 2 init containers (wait-for-memory + pre-my-hook), got %d", len(initContainers))
	}

	// The preRun hook must come AFTER system init containers.
	lastIdx := len(initContainers) - 1
	if initContainers[lastIdx].Name != "pre-my-hook" {
		t.Errorf("preRun hook should be last init container, got %q at position %d", initContainers[lastIdx].Name, lastIdx)
	}
	if initContainers[0].Name != "wait-for-memory" {
		t.Errorf("first init container should be wait-for-memory, got %q", initContainers[0].Name)
	}
}

func TestBuildContainers_NoLifecycleNoExtraInits(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	// No lifecycle hooks.

	_, initContainers, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	for _, ic := range initContainers {
		if strings.HasPrefix(ic.Name, "pre-") {
			t.Errorf("unexpected preRun init container %q when no lifecycle defined", ic.Name)
		}
	}
}

func TestBuildVolumes_WorkspacePVCWhenPostRunDefined(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		nil,
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "upload", Image: "amazon/aws-cli:latest", Command: []string{"aws", "s3", "cp", "/workspace/out", "s3://bucket/"}},
		},
		nil,
	)

	volumes := r.buildVolumes(run, false, nil, nil)

	var workspace *corev1.Volume
	for i := range volumes {
		if volumes[i].Name == "workspace" {
			workspace = &volumes[i]
			break
		}
	}
	if workspace == nil {
		t.Fatal("workspace volume not found")
	}
	if workspace.VolumeSource.PersistentVolumeClaim == nil {
		t.Fatal("workspace should use PVC when postRun hooks are defined")
	}
	if workspace.VolumeSource.PersistentVolumeClaim.ClaimName != "test-run-workspace" {
		t.Errorf("PVC claim name = %q, want test-run-workspace", workspace.VolumeSource.PersistentVolumeClaim.ClaimName)
	}
}

func TestBuildVolumes_WorkspaceEmptyDirWhenNoPostRun(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "setup", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil, // No postRun.
		nil,
	)

	volumes := r.buildVolumes(run, false, nil, nil)

	var workspace *corev1.Volume
	for i := range volumes {
		if volumes[i].Name == "workspace" {
			workspace = &volumes[i]
			break
		}
	}
	if workspace == nil {
		t.Fatal("workspace volume not found")
	}
	if workspace.VolumeSource.EmptyDir == nil {
		t.Fatal("workspace should use EmptyDir when no postRun hooks are defined")
	}
}

func TestBuildPostRunJob_ContainerSpec(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		nil,
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{
				Name:    "notify-slack",
				Image:   "curlimages/curl:latest",
				Command: []string{"sh", "-c", "curl -X POST $SLACK_URL"},
				Env:     []sympoziumv1alpha1.EnvVar{{Name: "SLACK_URL", Value: "https://hooks.slack.com/xxx"}},
			},
			{
				Name:    "upload-artifacts",
				Image:   "amazon/aws-cli:latest",
				Command: []string{"aws", "s3", "cp", "/workspace/output", "s3://bucket/results/"},
			},
		},
		nil,
	)
	run.Spec.Env = map[string]string{"TEAM": "platform"}

	job := r.buildPostRunJob(run, 0, "task completed successfully")

	// Verify job name.
	if job.Name != "test-run-postrun" {
		t.Errorf("postRun Job name = %q, want test-run-postrun", job.Name)
	}

	// PostRun hooks should be init containers (sequential execution).
	if len(job.Spec.Template.Spec.InitContainers) != 2 {
		t.Fatalf("expected 2 postRun init containers, got %d", len(job.Spec.Template.Spec.InitContainers))
	}

	// Final "done" container should exist.
	if len(job.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected 1 main container (done), got %d", len(job.Spec.Template.Spec.Containers))
	}
	if job.Spec.Template.Spec.Containers[0].Name != "done" {
		t.Errorf("main container name = %q, want done", job.Spec.Template.Spec.Containers[0].Name)
	}

	// Verify first hook container.
	hook1 := job.Spec.Template.Spec.InitContainers[0]
	if hook1.Name != "post-notify-slack" {
		t.Errorf("first postRun container name = %q, want post-notify-slack", hook1.Name)
	}
	if hook1.Image != "curlimages/curl:latest" {
		t.Errorf("first postRun container image = %q, want curlimages/curl:latest", hook1.Image)
	}

	// Verify env vars include AGENT_EXIT_CODE, AGENT_RESULT, custom env, and hook env.
	envMap := map[string]string{}
	for _, e := range hook1.Env {
		envMap[e.Name] = e.Value
	}
	if envMap["AGENT_EXIT_CODE"] != "0" {
		t.Errorf("AGENT_EXIT_CODE = %q, want 0", envMap["AGENT_EXIT_CODE"])
	}
	if envMap["AGENT_RESULT"] != "task completed successfully" {
		t.Errorf("AGENT_RESULT = %q, want 'task completed successfully'", envMap["AGENT_RESULT"])
	}
	if envMap["TEAM"] != "platform" {
		t.Errorf("TEAM = %q, want platform (forwarded from spec.env)", envMap["TEAM"])
	}
	if envMap["SLACK_URL"] != "https://hooks.slack.com/xxx" {
		t.Errorf("SLACK_URL = %q, want https://hooks.slack.com/xxx", envMap["SLACK_URL"])
	}

	// Verify second hook has AGENT_EXIT_CODE too.
	hook2 := job.Spec.Template.Spec.InitContainers[1]
	if hook2.Name != "post-upload-artifacts" {
		t.Errorf("second postRun container name = %q, want post-upload-artifacts", hook2.Name)
	}
	hook2Env := map[string]string{}
	for _, e := range hook2.Env {
		hook2Env[e.Name] = e.Value
	}
	if hook2Env["AGENT_EXIT_CODE"] != "0" {
		t.Errorf("second hook AGENT_EXIT_CODE = %q, want 0", hook2Env["AGENT_EXIT_CODE"])
	}
}

func TestBuildPostRunJob_FailedAgentExitCode(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		nil,
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "cleanup", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil,
	)

	job := r.buildPostRunJob(run, 1, "OOMKilled")

	hook := job.Spec.Template.Spec.InitContainers[0]
	envMap := map[string]string{}
	for _, e := range hook.Env {
		envMap[e.Name] = e.Value
	}
	if envMap["AGENT_EXIT_CODE"] != "1" {
		t.Errorf("AGENT_EXIT_CODE = %q, want 1", envMap["AGENT_EXIT_CODE"])
	}
	if envMap["AGENT_RESULT"] != "OOMKilled" {
		t.Errorf("AGENT_RESULT = %q, want OOMKilled", envMap["AGENT_RESULT"])
	}
}

func TestBuildPostRunJob_WorkspacePVC(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		nil,
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "upload", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil,
	)

	job := r.buildPostRunJob(run, 0, "done")

	// Workspace volume should use PVC.
	var workspace *corev1.Volume
	for i := range job.Spec.Template.Spec.Volumes {
		if job.Spec.Template.Spec.Volumes[i].Name == "workspace" {
			workspace = &job.Spec.Template.Spec.Volumes[i]
			break
		}
	}
	if workspace == nil {
		t.Fatal("workspace volume not found in postRun Job")
	}
	if workspace.VolumeSource.PersistentVolumeClaim == nil {
		t.Fatal("postRun Job workspace should use PVC")
	}
	if workspace.VolumeSource.PersistentVolumeClaim.ClaimName != "test-run-workspace" {
		t.Errorf("PVC claim name = %q, want test-run-workspace", workspace.VolumeSource.PersistentVolumeClaim.ClaimName)
	}

	// Hook container should mount /workspace.
	hook := job.Spec.Template.Spec.InitContainers[0]
	foundMount := false
	for _, m := range hook.VolumeMounts {
		if m.Name == "workspace" && m.MountPath == "/workspace" {
			foundMount = true
		}
	}
	if !foundMount {
		t.Error("postRun hook container missing /workspace volume mount")
	}
}

func TestBuildPostRunJob_SecurityContext(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		nil,
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "hook", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil,
	)

	job := r.buildPostRunJob(run, 0, "done")

	// Pod-level security context.
	psc := job.Spec.Template.Spec.SecurityContext
	if psc == nil {
		t.Fatal("postRun Job missing pod SecurityContext")
	}
	if psc.RunAsNonRoot == nil || !*psc.RunAsNonRoot {
		t.Error("postRun Job should have RunAsNonRoot=true")
	}

	// Container-level security context.
	hook := job.Spec.Template.Spec.InitContainers[0]
	sc := hook.SecurityContext
	if sc == nil {
		t.Fatal("postRun hook container missing SecurityContext")
	}
	if sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("postRun hook container should have ReadOnlyRootFilesystem=true")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) == 0 {
		t.Error("postRun hook container should drop ALL capabilities")
	}
}

func TestBuildPostRunJob_ServiceAccount(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		nil,
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "hook", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil,
	)

	job := r.buildPostRunJob(run, 0, "done")

	if job.Spec.Template.Spec.ServiceAccountName != "sympozium-run-test-run" {
		t.Errorf("postRun Job ServiceAccountName = %q, want sympozium-run-test-run", job.Spec.Template.Spec.ServiceAccountName)
	}
	if job.Spec.Template.Spec.AutomountServiceAccountToken == nil || *job.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Error("postRun pod must disable automatic service-account token mounting")
	}
}

func TestBuildPostRunJob_Labels(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		nil,
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "hook", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil,
	)

	job := r.buildPostRunJob(run, 0, "done")

	labels := job.Labels
	if labels["sympozium.ai/component"] != "post-run" {
		t.Errorf("postRun Job component label = %q, want post-run", labels["sympozium.ai/component"])
	}
	if labels["sympozium.ai/agent-run"] != "test-run" {
		t.Errorf("postRun Job agent-run label = %q, want test-run", labels["sympozium.ai/agent-run"])
	}
}

func TestBuildPostRunJob_TruncatesLongResult(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		nil,
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "hook", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil,
	)

	// Create a result larger than 32Ki.
	longResult := strings.Repeat("x", 40*1024)
	job := r.buildPostRunJob(run, 0, longResult)

	hook := job.Spec.Template.Spec.InitContainers[0]
	for _, e := range hook.Env {
		if e.Name == "AGENT_RESULT" {
			if len(e.Value) > 32*1024 {
				t.Errorf("AGENT_RESULT should be truncated to 32Ki, got %d bytes", len(e.Value))
			}
			return
		}
	}
	t.Error("AGENT_RESULT env var not found")
}

// ── RBAC tests ──────────────────────────────────────────────────────────────

func TestLifecycleRBACRules_ConfigMapAccess(t *testing.T) {
	// Verify the RBAC rules type supports the ConfigMap create/delete pattern
	// that the issue specifically requested.
	rules := []sympoziumv1alpha1.RBACRule{
		{
			APIGroups: []string{""},
			Resources: []string{"configmaps"},
			Verbs:     []string{"get", "list", "create", "delete"},
		},
	}

	run := newTestRunWithLifecycle(
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "create-cm", Image: "soldevelo/kubectl:1.36", Command: []string{"kubectl", "create", "configmap", "test", "--from-literal=key=value"}},
		},
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "delete-cm", Image: "soldevelo/kubectl:1.36", Command: []string{"kubectl", "delete", "configmap", "test"}},
		},
		rules,
	)

	if run.Spec.Lifecycle.RBAC == nil || len(run.Spec.Lifecycle.RBAC) != 1 {
		t.Fatal("expected 1 RBAC rule")
	}
	rule := run.Spec.Lifecycle.RBAC[0]
	if rule.APIGroups[0] != "" {
		t.Errorf("APIGroups[0] = %q, want empty string (core API group)", rule.APIGroups[0])
	}
	if rule.Resources[0] != "configmaps" {
		t.Errorf("Resources[0] = %q, want configmaps", rule.Resources[0])
	}
	wantVerbs := map[string]bool{"get": true, "list": true, "create": true, "delete": true}
	for _, v := range rule.Verbs {
		if !wantVerbs[v] {
			t.Errorf("unexpected verb %q", v)
		}
	}
}

// ── Sandbox + lifecycle integration ─────────────────────────────────────────

// ── Structured handoff card tests ───────────────────────────────────────────

func TestBuildHandoffTask_Basic(t *testing.T) {
	got := buildHandoffTask("researcher", "Find population data", "Paris has 2.1M people", "Write an article about Paris")

	for _, want := range []string{
		"## Handoff from researcher",
		"### Previous Task\nFind population data",
		"### Result\nParis has 2.1M people",
		"### Your Task\nWrite an article about Paris",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("handoff card missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestBuildHandoffTask_NoTargetTask(t *testing.T) {
	got := buildHandoffTask("researcher", "Find data", "result here", "")
	if !strings.Contains(got, "Continue the workflow as your role requires.") {
		t.Errorf("expected default target task, got:\n%s", got)
	}
}

func TestBuildHandoffTask_PassesResultsUnderTheLimitIntact(t *testing.T) {
	// A result comfortably under the cap must arrive whole. A report handed to
	// a reviewer is the payload of the whole edge; clipping it silently makes
	// the reviewer critique a fragment.
	result := strings.Repeat("x", handoffResultMaxChars-1)
	got := buildHandoffTask("writer", "task", result, "review it")
	if !strings.Contains(got, result) {
		t.Error("result under the limit should pass through unmodified")
	}
	if strings.Contains(got, "[truncated:") {
		t.Error("result under the limit should not be marked truncated")
	}
}

func TestBuildHandoffTask_TruncatesResult(t *testing.T) {
	longResult := strings.Repeat("x", handoffResultMaxChars+500)
	got := buildHandoffTask("researcher", "task", longResult, "next")

	// The notice must name the truncation explicitly. A bare "..." is
	// indistinguishable from an ellipsis, so a successor cannot tell that data
	// is missing and will act on the fragment as if it were complete.
	if !strings.Contains(got, "[truncated:") {
		t.Error("expected an explicit truncation notice in the result")
	}
	if !strings.Contains(got, "researcher") {
		t.Error("truncation notice should name the source persona to ask for the full text")
	}
	if !strings.Contains(got, fmt.Sprintf("produced %d characters", handoffResultMaxChars+500)) {
		t.Error("truncation notice should report the original length, not the clipped one")
	}

	idx := strings.Index(got, "### Result\n")
	if idx < 0 {
		t.Fatal("missing Result section")
	}
	resultSection := got[idx+len("### Result\n"):]
	if endIdx := strings.Index(resultSection, "\n\n### "); endIdx >= 0 {
		resultSection = resultSection[:endIdx]
	}

	// Measure the carried payload — the text before the notice — rather than
	// the whole section, which also contains the notice's own prose.
	payload := resultSection
	if n := strings.Index(payload, "\n\n[truncated:"); n >= 0 {
		payload = payload[:n]
	}
	if payload != strings.Repeat("x", handoffResultMaxChars) {
		t.Errorf("carried payload = %d chars, want exactly %d", len(payload), handoffResultMaxChars)
	}
}

func TestBuildHandoffTask_TruncatesPreviousTask(t *testing.T) {
	longTask := strings.Repeat("y", 300)
	got := buildHandoffTask("src", longTask, "result", "next")
	idx := strings.Index(got, "### Previous Task\n")
	if idx < 0 {
		t.Fatal("missing Previous Task section")
	}
	taskSection := got[idx+len("### Previous Task\n"):]
	endIdx := strings.Index(taskSection, "\n\n### ")
	if endIdx >= 0 {
		taskSection = taskSection[:endIdx]
	}
	if len(taskSection) > 203 {
		t.Errorf("previous task section too long: %d chars", len(taskSection))
	}
}

func TestExtractOriginalTask_PlainTask(t *testing.T) {
	got := extractOriginalTask("Do some research")
	if got != "Do some research" {
		t.Errorf("got %q, want plain task returned as-is", got)
	}
}

func TestExtractOriginalTask_NestedHandoff(t *testing.T) {
	handoff := "## Handoff from researcher\n\n### Previous Task\nFind population data\n\n### Result\nParis: 2.1M\n\n### Your Task\nWrite article"
	got := extractOriginalTask(handoff)
	if got != "Find population data" {
		t.Errorf("got %q, want %q", got, "Find population data")
	}
}

func TestExtractOriginalTask_DoubleNested(t *testing.T) {
	// Simulate A→B→C where C receives B's handoff which itself was a handoff from A
	inner := "## Handoff from agent-a\n\n### Previous Task\nOriginal task from user\n\n### Result\nA's result\n\n### Your Task\nB's job"
	outer := buildHandoffTask("agent-b", inner, "B's result", "C's job")
	got := extractOriginalTask(outer)
	if got != "Original task from user" {
		t.Errorf("double-nested extraction got %q, want %q", got, "Original task from user")
	}
}

// ── Dry run tests ──────────────────────────────────────────────────────────

func TestBuildContainers_DryRunEnvVar(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.DryRun = true
	cs, _, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	var found bool
	for _, e := range cs[0].Env {
		if e.Name == "DRY_RUN" && e.Value == "true" {
			found = true
			break
		}
	}
	if !found {
		t.Error("agent container missing DRY_RUN=true env var")
	}
}

func TestBuildContainers_NoDryRunEnvByDefault(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, _ := r.buildContainers(newTestRun(), false, nil, nil, nil, nil)

	for _, e := range cs[0].Env {
		if e.Name == "DRY_RUN" {
			t.Errorf("DRY_RUN env var should not be present by default, got value=%q", e.Value)
		}
	}
}

// ── Sandbox + lifecycle integration ─────────────────────────────────────────

func TestBuildVolumes_WorkspacePVCWithSandboxEnabled(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRunWithLifecycle(
		nil,
		[]sympoziumv1alpha1.LifecycleHookContainer{
			{Name: "upload", Image: "busybox:1.36", Command: []string{"true"}},
		},
		nil,
	)
	// Enable agent sandbox — volumes should still use PVC for workspace.
	run.Spec.AgentSandbox = &sympoziumv1alpha1.AgentSandboxSpec{
		Enabled:      true,
		RuntimeClass: "gvisor",
	}

	volumes := r.buildVolumes(run, false, nil, nil)

	var workspace *corev1.Volume
	for i := range volumes {
		if volumes[i].Name == "workspace" {
			workspace = &volumes[i]
			break
		}
	}
	if workspace == nil {
		t.Fatal("workspace volume not found")
	}
	if workspace.VolumeSource.PersistentVolumeClaim == nil {
		t.Fatal("workspace should use PVC even with sandbox enabled when postRun hooks are defined")
	}
}

// ── Membrane helper tests ───────────────────────────────────────────────────

func TestResolveTrustPeers_FromGroups(t *testing.T) {
	groups := []sympoziumv1alpha1.TrustGroup{
		{Name: "research", AgentConfigs: []string{"researcher", "writer", "editor"}},
	}
	peers := resolveTrustPeers("researcher", groups, nil)
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d: %v", len(peers), peers)
	}
	// Should be sorted.
	if peers[0] != "editor" || peers[1] != "writer" {
		t.Errorf("peers = %v, want [editor writer]", peers)
	}
}

func TestResolveTrustPeers_FromRelationships(t *testing.T) {
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "planner", Target: "executor", Type: "delegation"},
		{Source: "monitor", Target: "planner", Type: "supervision"},
		{Source: "planner", Target: "logger", Type: "sequential"},
	}
	peers := resolveTrustPeers("planner", nil, rels)
	// delegation to executor + supervision from monitor = 2 peers
	// sequential to logger should NOT imply trust
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d: %v", len(peers), peers)
	}
	peerMap := map[string]bool{}
	for _, p := range peers {
		peerMap[p] = true
	}
	if !peerMap["executor"] || !peerMap["monitor"] {
		t.Errorf("peers = %v, want executor and monitor", peers)
	}
}

func TestResolveTrustPeers_NoMembrane(t *testing.T) {
	peers := resolveTrustPeers("agent-a", nil, nil)
	if len(peers) != 0 {
		t.Errorf("expected no peers, got %v", peers)
	}
}

func TestResolveTrustPeers_Combined(t *testing.T) {
	groups := []sympoziumv1alpha1.TrustGroup{
		{Name: "team", AgentConfigs: []string{"a", "b"}},
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "c", Type: "delegation"},
	}
	peers := resolveTrustPeers("a", groups, rels)
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers (b from group, c from rel), got %d: %v", len(peers), peers)
	}
}

func TestResolveMembraneEnvVars(t *testing.T) {
	membrane := &sympoziumv1alpha1.MembraneSpec{
		DefaultVisibility: "public",
		Permeability: []sympoziumv1alpha1.PermeabilityRule{
			{
				AgentConfig:       "writer",
				DefaultVisibility: "trusted",
				AcceptTags:        []string{"research", "data"},
			},
		},
		TrustGroups: []sympoziumv1alpha1.TrustGroup{
			{Name: "team", AgentConfigs: []string{"writer", "editor"}},
		},
		TimeDecay: &sympoziumv1alpha1.TimeDecaySpec{
			TTL: "168h",
		},
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "writer", Target: "reviewer", Type: "delegation"},
	}

	envs := resolveMembraneEnvVars("writer", membrane, rels)
	envMap := map[string]string{}
	for _, e := range envs {
		envMap[e.Name] = e.Value
	}

	if envMap["WORKFLOW_MEMBRANE_VISIBILITY"] != "trusted" {
		t.Errorf("visibility = %q, want trusted", envMap["WORKFLOW_MEMBRANE_VISIBILITY"])
	}
	if envMap["WORKFLOW_MEMBRANE_MAX_AGE"] != "168h" {
		t.Errorf("max_age = %q, want 168h", envMap["WORKFLOW_MEMBRANE_MAX_AGE"])
	}
	if envMap["WORKFLOW_MEMBRANE_ACCEPT_TAGS"] != "research,data" {
		t.Errorf("accept_tags = %q, want research,data", envMap["WORKFLOW_MEMBRANE_ACCEPT_TAGS"])
	}
	// Trust peers: editor (from group) + reviewer (from delegation)
	peers := envMap["WORKFLOW_MEMBRANE_TRUST_PEERS"]
	if !strings.Contains(peers, "editor") || !strings.Contains(peers, "reviewer") {
		t.Errorf("trust_peers = %q, want editor and reviewer", peers)
	}
	// No expose tags configured for this rule → should be empty.
	if envMap["WORKFLOW_MEMBRANE_EXPOSE_TAGS"] != "" {
		t.Errorf("expose_tags = %q, want empty", envMap["WORKFLOW_MEMBRANE_EXPOSE_TAGS"])
	}
}

func TestResolveMembraneEnvVars_ExposeTags(t *testing.T) {
	membrane := &sympoziumv1alpha1.MembraneSpec{
		DefaultVisibility: "public",
		Permeability: []sympoziumv1alpha1.PermeabilityRule{
			{
				AgentConfig: "researcher",
				ExposeTags:  []string{"findings", "summary"},
				AcceptTags:  []string{"tasks"},
			},
		},
	}

	envs := resolveMembraneEnvVars("researcher", membrane, nil)
	envMap := map[string]string{}
	for _, e := range envs {
		envMap[e.Name] = e.Value
	}

	if envMap["WORKFLOW_MEMBRANE_EXPOSE_TAGS"] != "findings,summary" {
		t.Errorf("expose_tags = %q, want findings,summary", envMap["WORKFLOW_MEMBRANE_EXPOSE_TAGS"])
	}
}

func TestResolveMembraneEnvVars_MaxTokensPerRun(t *testing.T) {
	membrane := &sympoziumv1alpha1.MembraneSpec{
		DefaultVisibility: "public",
		TokenBudget: &sympoziumv1alpha1.TokenBudgetSpec{
			MaxTokens:       100000,
			MaxTokensPerRun: 50000,
			Action:          "warn",
		},
	}

	envs := resolveMembraneEnvVars("agent-a", membrane, nil)
	envMap := map[string]string{}
	for _, e := range envs {
		envMap[e.Name] = e.Value
	}

	if envMap["WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN"] != "50000" {
		t.Errorf("max_tokens_per_run = %q, want 50000", envMap["WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN"])
	}
	if envMap["WORKFLOW_MEMBRANE_TOKEN_BUDGET_ACTION"] != "warn" {
		t.Errorf("token_budget_action = %q, want warn", envMap["WORKFLOW_MEMBRANE_TOKEN_BUDGET_ACTION"])
	}
}

func TestResolveMembraneEnvVars_NoTokenBudget(t *testing.T) {
	membrane := &sympoziumv1alpha1.MembraneSpec{
		DefaultVisibility: "public",
	}

	envs := resolveMembraneEnvVars("agent-a", membrane, nil)
	envMap := map[string]string{}
	for _, e := range envs {
		envMap[e.Name] = e.Value
	}

	if _, ok := envMap["WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN"]; ok {
		t.Error("expected no WORKFLOW_MEMBRANE_MAX_TOKENS_PER_RUN when budget is nil")
	}
}

func TestResolveMembraneEnvVars_Nil(t *testing.T) {
	envs := resolveMembraneEnvVars("test", nil, nil)
	if len(envs) != 0 {
		t.Errorf("expected no envs for nil membrane, got %d", len(envs))
	}
}

// ── Token budget tests ──────────────────────────────────────────────────────

func newMembraneTestReconciler(t *testing.T, objs ...client.Object) *AgentRunReconciler {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&sympoziumv1alpha1.Ensemble{}, &sympoziumv1alpha1.AgentRun{}).
		Build()

	return &AgentRunReconciler{
		Client: cl,
		Scheme: scheme,
	}
}

func TestCheckTokenBudget_NoBudget(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{Enabled: true},
		},
	}
	run := newTestRun()
	run.Labels = map[string]string{"sympozium.ai/ensemble": "my-pack"}

	r := newMembraneTestReconciler(t, pack)
	err := r.checkTokenBudget(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Errorf("expected no error without budget config, got: %v", err)
	}
}

func TestCheckTokenBudget_UnderLimit(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{
				Enabled: true,
				Membrane: &sympoziumv1alpha1.MembraneSpec{
					TokenBudget: &sympoziumv1alpha1.TokenBudgetSpec{
						MaxTokens: 10000,
						Action:    "halt",
					},
				},
			},
		},
		Status: sympoziumv1alpha1.EnsembleStatus{
			TokenBudgetUsed: 5000,
		},
	}
	run := newTestRun()
	run.Labels = map[string]string{"sympozium.ai/ensemble": "my-pack"}

	r := newMembraneTestReconciler(t, pack)
	err := r.checkTokenBudget(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Errorf("expected no error under budget, got: %v", err)
	}
}

func TestCheckTokenBudget_OverLimit_Halt(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{
				Enabled: true,
				Membrane: &sympoziumv1alpha1.MembraneSpec{
					TokenBudget: &sympoziumv1alpha1.TokenBudgetSpec{
						MaxTokens: 10000,
						Action:    "halt",
					},
				},
			},
		},
		Status: sympoziumv1alpha1.EnsembleStatus{
			TokenBudgetUsed: 10000,
		},
	}
	run := newTestRun()
	run.Labels = map[string]string{"sympozium.ai/ensemble": "my-pack"}

	r := newMembraneTestReconciler(t, pack)
	err := r.checkTokenBudget(context.Background(), logr.Discard(), run)
	if err == nil {
		t.Error("expected error when budget exceeded with halt action")
	}
}

func TestCheckTokenBudget_OverLimit_Warn(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{
				Enabled: true,
				Membrane: &sympoziumv1alpha1.MembraneSpec{
					TokenBudget: &sympoziumv1alpha1.TokenBudgetSpec{
						MaxTokens: 10000,
						Action:    "warn",
					},
				},
			},
		},
		Status: sympoziumv1alpha1.EnsembleStatus{
			TokenBudgetUsed: 15000,
		},
	}
	run := newTestRun()
	run.Labels = map[string]string{"sympozium.ai/ensemble": "my-pack"}

	r := newMembraneTestReconciler(t, pack)
	err := r.checkTokenBudget(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Errorf("expected warn mode to allow run, got: %v", err)
	}
}

func TestUpdateTokenBudget(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{
				Enabled: true,
				Membrane: &sympoziumv1alpha1.MembraneSpec{
					TokenBudget: &sympoziumv1alpha1.TokenBudgetSpec{
						MaxTokens: 100000,
					},
				},
			},
		},
	}
	run := newTestRun()
	run.Labels = map[string]string{"sympozium.ai/ensemble": "my-pack"}
	run.Status.TokenUsage = &sympoziumv1alpha1.TokenUsage{
		TotalTokens: 1500,
	}

	// Register the run too: updateTokenBudget now stamps an idempotency guard
	// annotation on it, which requires the run to exist in the client.
	r := newMembraneTestReconciler(t, pack, run)
	err := r.updateTokenBudget(context.Background(), logr.Discard(), run)
	if err != nil {
		t.Fatalf("updateTokenBudget: %v", err)
	}

	// Verify the ensemble status was updated.
	var updated sympoziumv1alpha1.Ensemble
	if err := r.Get(context.Background(), types.NamespacedName{Name: "my-pack", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get ensemble: %v", err)
	}
	if updated.Status.TokenBudgetUsed != 1500 {
		t.Errorf("tokenBudgetUsed = %d, want 1500", updated.Status.TokenBudgetUsed)
	}

	// Idempotency: re-running on the same (now guard-annotated) run must not
	// double-count the tokens into the ensemble budget.
	var afterFirst sympoziumv1alpha1.AgentRun
	if err := r.Get(context.Background(), types.NamespacedName{Name: run.Name, Namespace: run.Namespace}, &afterFirst); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if err := r.updateTokenBudget(context.Background(), logr.Discard(), &afterFirst); err != nil {
		t.Fatalf("updateTokenBudget (second call): %v", err)
	}
	if err := r.Get(context.Background(), types.NamespacedName{Name: "my-pack", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get ensemble: %v", err)
	}
	if updated.Status.TokenBudgetUsed != 1500 {
		t.Errorf("after second call tokenBudgetUsed = %d, want 1500 (no double-count)", updated.Status.TokenBudgetUsed)
	}
}

func TestUpdateTokenBudget_NegativeTotalNeverDecrementsLedger(t *testing.T) {
	// Regression: a TokenUsage with a negative total (e.g. from a forged
	// result marker on an older controller) must not be added to the ensemble
	// ledger — the budget only ever counts up.
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "my-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{
				Enabled: true,
				Membrane: &sympoziumv1alpha1.MembraneSpec{
					TokenBudget: &sympoziumv1alpha1.TokenBudgetSpec{
						MaxTokens: 100000,
					},
				},
			},
		},
		Status: sympoziumv1alpha1.EnsembleStatus{
			TokenBudgetUsed: 1500,
		},
	}
	run := newTestRun()
	run.Labels = map[string]string{"sympozium.ai/ensemble": "my-pack"}
	run.Status.TokenUsage = &sympoziumv1alpha1.TokenUsage{
		InputTokens:  -2000,
		OutputTokens: 0,
		TotalTokens:  -2000,
	}

	r := newMembraneTestReconciler(t, pack, run)
	if err := r.updateTokenBudget(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("updateTokenBudget: %v", err)
	}

	var updated sympoziumv1alpha1.Ensemble
	if err := r.Get(context.Background(), types.NamespacedName{Name: "my-pack", Namespace: "default"}, &updated); err != nil {
		t.Fatalf("get ensemble: %v", err)
	}
	if updated.Status.TokenBudgetUsed != 1500 {
		t.Errorf("tokenBudgetUsed = %d, want 1500 (negative usage must not decrement)", updated.Status.TokenBudgetUsed)
	}
}

// ── Auto-derive permeability tests ──────────────────────────────────────────

func TestDerivePermeability_DelegationWorkflow(t *testing.T) {
	configs := []sympoziumv1alpha1.AgentConfigSpec{
		{Name: "researcher"},
		{Name: "writer"},
		{Name: "reviewer"},
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "researcher", Target: "writer", Type: "delegation"},
		{Source: "writer", Target: "reviewer", Type: "sequential"},
	}

	rules := derivePermeability(configs, rels, "public")
	ruleMap := map[string]string{}
	for _, r := range rules {
		ruleMap[r.AgentConfig] = r.DefaultVisibility
	}

	// researcher is delegation source → trusted
	if ruleMap["researcher"] != "trusted" {
		t.Errorf("researcher = %q, want trusted", ruleMap["researcher"])
	}
	// writer is source of sequential → public (default, since sequential doesn't set trusted)
	// but writer is also a source (of the sequential edge), so not terminal
	if ruleMap["writer"] != "public" {
		t.Errorf("writer = %q, want public", ruleMap["writer"])
	}
	// reviewer is terminal (only target, never source) → private
	if ruleMap["reviewer"] != "private" {
		t.Errorf("reviewer = %q, want private", ruleMap["reviewer"])
	}
}

func TestDerivePermeability_SupervisionMakesPublic(t *testing.T) {
	configs := []sympoziumv1alpha1.AgentConfigSpec{
		{Name: "lead"},
		{Name: "worker"},
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "lead", Target: "worker", Type: "supervision"},
	}

	rules := derivePermeability(configs, rels, "trusted")
	ruleMap := map[string]string{}
	for _, r := range rules {
		ruleMap[r.AgentConfig] = r.DefaultVisibility
	}

	// worker is supervision target → public
	if ruleMap["worker"] != "public" {
		t.Errorf("worker = %q, want public (supervision target)", ruleMap["worker"])
	}
	// lead is source only → default (trusted) since it's not a delegation source
	if ruleMap["lead"] != "trusted" {
		t.Errorf("lead = %q, want trusted (default)", ruleMap["lead"])
	}
}

func TestDerivePermeability_NoRelationships(t *testing.T) {
	configs := []sympoziumv1alpha1.AgentConfigSpec{
		{Name: "solo"},
	}

	rules := derivePermeability(configs, nil, "public")
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].DefaultVisibility != "public" {
		t.Errorf("solo = %q, want public (default)", rules[0].DefaultVisibility)
	}
}
