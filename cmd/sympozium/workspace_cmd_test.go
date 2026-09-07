package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// --- harness ----------------------------------------------------------------

// withTestClient installs a fake client + namespace on the package-level
// globals the workspace command reads, and returns a restore function.
func withTestClient(t *testing.T, ns string, objs ...client.Object) func() {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium: %v", err)
	}
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(
			&sympoziumv1alpha1.WorkspaceSession{},
			&sympoziumv1alpha1.AgentRun{},
		).
		Build()

	prevClient := k8sClient
	prevNS := namespace
	k8sClient = cl
	namespace = ns
	return func() {
		k8sClient = prevClient
		namespace = prevNS
	}
}

// captureStdout runs fn while redirecting os.Stdout to a buffer and
// returns whatever was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	prev := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = prev }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()
	_ = w.Close()
	return <-done
}

// --- builders ---------------------------------------------------------------

func ws(name, agent, sessKey, hash, pvc string, phase sympoziumv1alpha1.WorkspaceSessionPhase) *sympoziumv1alpha1.WorkspaceSession {
	w := &sympoziumv1alpha1.WorkspaceSession{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/agent":            agent,
				"sympozium.ai/session-key-hash": hash,
			},
			CreationTimestamp: metav1.Time{Time: time.Now().Add(-5 * time.Minute)},
		},
		Spec: sympoziumv1alpha1.WorkspaceSessionSpec{
			AgentRef:   agent,
			SessionKey: sessKey,
			Size:       "1Gi",
		},
		Status: sympoziumv1alpha1.WorkspaceSessionStatus{
			Phase:   phase,
			PVCName: pvc,
		},
	}
	return w
}

func agent(name, ensemble string) *sympoziumv1alpha1.Agent {
	labels := map[string]string{}
	if ensemble != "" {
		labels["sympozium.ai/ensemble"] = ensemble
	}
	return &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    labels,
		},
	}
}

func run(name, agentRef, sessKey, hash string, phase sympoziumv1alpha1.AgentRunPhase) *sympoziumv1alpha1.AgentRun {
	r := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/instance":         agentRef,
				"sympozium.ai/session-key-hash": hash,
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef:   agentRef,
			SessionKey: sessKey,
		},
		Status: sympoziumv1alpha1.AgentRunStatus{Phase: phase},
	}
	return r
}

func pvc(name, size string) *corev1.PersistentVolumeClaim {
	q := resource.MustParse(size)
	return &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: q},
			},
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase:    corev1.ClaimBound,
			Capacity: corev1.ResourceList{corev1.ResourceStorage: q},
		},
	}
}

// --- helper unit tests ------------------------------------------------------

func TestIsTerminalRunPhase(t *testing.T) {
	cases := map[sympoziumv1alpha1.AgentRunPhase]bool{
		sympoziumv1alpha1.AgentRunPhasePending:          false,
		sympoziumv1alpha1.AgentRunPhaseRunning:          false,
		sympoziumv1alpha1.AgentRunPhaseServing:          false,
		sympoziumv1alpha1.AgentRunPhasePostRunning:      false,
		sympoziumv1alpha1.AgentRunPhaseAwaitingDelegate: false,
		sympoziumv1alpha1.AgentRunPhaseSucceeded:        true,
		sympoziumv1alpha1.AgentRunPhaseFailed:           true,
	}
	for phase, want := range cases {
		if got := isTerminalRunPhase(phase); got != want {
			t.Errorf("isTerminalRunPhase(%q)=%v want %v", phase, got, want)
		}
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := orDash(""); got != "-" {
		t.Errorf("orDash(empty)=%q want -", got)
	}
	if got := orDash("hi"); got != "hi" {
		t.Errorf("orDash(hi)=%q want hi", got)
	}
	if got := formatTouched(nil); got != "-" {
		t.Errorf("formatTouched(nil)=%q want -", got)
	}
	now := metav1.Now()
	if got := formatTouched(&now); !strings.HasSuffix(got, "ago") {
		t.Errorf("formatTouched=%q want ...ago", got)
	}
	if got := formatTime(nil); got != "-" {
		t.Errorf("formatTime(nil)=%q want -", got)
	}
	if got := formatTime(&now); got == "-" {
		t.Errorf("formatTime(now) should not be -")
	}
}

// --- liveRunsForWorkspace ---------------------------------------------------

func TestLiveRunsForWorkspace_HashLabel_FiltersTerminalAndOthers(t *testing.T) {
	const hash = "0123456789abcdef"
	w := ws("ws-alice-h", "alice", "sess-A", hash, "pvc-alice-h-g0", sympoziumv1alpha1.WorkspaceSessionPhaseBound)

	live1 := run("run-1", "alice", "sess-A", hash, sympoziumv1alpha1.AgentRunPhaseRunning)
	live2 := run("run-2", "alice", "sess-A", hash, sympoziumv1alpha1.AgentRunPhaseServing)
	terminal := run("run-3", "alice", "sess-A", hash, sympoziumv1alpha1.AgentRunPhaseSucceeded)
	otherSess := run("run-4", "alice", "sess-B", "ffffffffffffffff", sympoziumv1alpha1.AgentRunPhaseRunning)
	otherAgent := run("run-5", "bob", "sess-A", hash, sympoziumv1alpha1.AgentRunPhaseRunning)

	cleanup := withTestClient(t, "default", w, live1, live2, terminal, otherSess, otherAgent)
	defer cleanup()

	got, err := liveRunsForWorkspace(context.Background(), w)
	if err != nil {
		t.Fatalf("liveRunsForWorkspace: %v", err)
	}
	names := map[string]bool{}
	for _, r := range got {
		names[r.Name] = true
	}
	if len(names) != 2 || !names["run-1"] || !names["run-2"] {
		t.Errorf("expected only run-1 and run-2, got %v", names)
	}
}

func TestLiveRunsForWorkspace_NoHashLabel_FallsBackToSessionKey(t *testing.T) {
	w := ws("ws-alice-old", "alice", "sess-old", "", "pvc-alice-old", sympoziumv1alpha1.WorkspaceSessionPhaseBound)
	// Strip the hash label to exercise the fallback branch.
	delete(w.Labels, "sympozium.ai/session-key-hash")

	live := run("run-live", "alice", "sess-old", "anyhash", sympoziumv1alpha1.AgentRunPhaseRunning)
	otherSess := run("run-other", "alice", "sess-different", "anyhash", sympoziumv1alpha1.AgentRunPhaseRunning)
	terminal := run("run-done", "alice", "sess-old", "anyhash", sympoziumv1alpha1.AgentRunPhaseFailed)

	cleanup := withTestClient(t, "default", w, live, otherSess, terminal)
	defer cleanup()

	got, err := liveRunsForWorkspace(context.Background(), w)
	if err != nil {
		t.Fatalf("liveRunsForWorkspace: %v", err)
	}
	if len(got) != 1 || got[0].Name != "run-live" {
		t.Errorf("expected only run-live, got %+v", runNames(got))
	}
}

func runNames(rs []sympoziumv1alpha1.AgentRun) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

// --- list -------------------------------------------------------------------

func TestWorkspaceListCmd_FiltersByAgentAndEnsemble(t *testing.T) {
	a := ws("ws-alice", "alice", "sA", "h1", "pvc-a", sympoziumv1alpha1.WorkspaceSessionPhaseBound)
	b := ws("ws-bob", "bob", "sB", "h2", "pvc-b", sympoziumv1alpha1.WorkspaceSessionPhaseBound)
	c := ws("ws-carol", "carol", "sC", "h3", "pvc-c", sympoziumv1alpha1.WorkspaceSessionPhaseBound)
	alice := agent("alice", "ensemble-x")
	bob := agent("bob", "ensemble-x")
	carol := agent("carol", "ensemble-y")

	cleanup := withTestClient(t, "default", a, b, c, alice, bob, carol)
	defer cleanup()

	// Plain list: all three.
	out := captureStdout(t, func() {
		cmd := newWorkspaceListCmd()
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("list: %v", err)
		}
	})
	for _, want := range []string{"ws-alice", "ws-bob", "ws-carol"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain list missing %q\noutput:\n%s", want, out)
		}
	}

	// --agent=alice → only alice.
	out = captureStdout(t, func() {
		cmd := newWorkspaceListCmd()
		_ = cmd.Flags().Set("agent", "alice")
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("list --agent: %v", err)
		}
	})
	if !strings.Contains(out, "ws-alice") || strings.Contains(out, "ws-bob") || strings.Contains(out, "ws-carol") {
		t.Errorf("--agent=alice filtering wrong:\n%s", out)
	}

	// --ensemble=ensemble-y → only carol.
	out = captureStdout(t, func() {
		cmd := newWorkspaceListCmd()
		_ = cmd.Flags().Set("ensemble", "ensemble-y")
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("list --ensemble: %v", err)
		}
	})
	if !strings.Contains(out, "ws-carol") || strings.Contains(out, "ws-alice") || strings.Contains(out, "ws-bob") {
		t.Errorf("--ensemble=ensemble-y filtering wrong:\n%s", out)
	}
}

// --- delete -----------------------------------------------------------------

func TestWorkspaceDeleteCmd_RefusesWhenLiveRunsExist(t *testing.T) {
	const hash = "0123456789abcdef"
	w := ws("ws-alice", "alice", "sA", hash, "pvc-a", sympoziumv1alpha1.WorkspaceSessionPhaseBound)
	live := run("run-live", "alice", "sA", hash, sympoziumv1alpha1.AgentRunPhaseRunning)

	cleanup := withTestClient(t, "default", w, live)
	defer cleanup()

	cmd := newWorkspaceDeleteCmd()
	err := cmd.RunE(cmd, []string{"ws-alice"})
	if err == nil {
		t.Fatal("expected error refusing to delete live workspace")
	}
	if !strings.Contains(err.Error(), "run-live") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("error message should name the live run and mention --force, got: %v", err)
	}

	// WS should still exist.
	got := &sympoziumv1alpha1.WorkspaceSession{}
	if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ws-alice"}, got); err != nil {
		t.Errorf("workspace should still exist after refused delete: %v", err)
	}
}

func TestWorkspaceDeleteCmd_DeletesWhenIdle(t *testing.T) {
	const hash = "0123456789abcdef"
	w := ws("ws-alice", "alice", "sA", hash, "pvc-a", sympoziumv1alpha1.WorkspaceSessionPhaseBound)
	done := run("run-done", "alice", "sA", hash, sympoziumv1alpha1.AgentRunPhaseSucceeded)

	cleanup := withTestClient(t, "default", w, done)
	defer cleanup()

	cmd := newWorkspaceDeleteCmd()
	_ = captureStdout(t, func() {
		if err := cmd.RunE(cmd, []string{"ws-alice"}); err != nil {
			t.Fatalf("delete: %v", err)
		}
	})

	got := &sympoziumv1alpha1.WorkspaceSession{}
	err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ws-alice"}, got)
	if err == nil {
		t.Errorf("workspace should be gone, but Get returned object")
	}
}

func TestWorkspaceDeleteCmd_ForceBypassesLiveCheck(t *testing.T) {
	const hash = "0123456789abcdef"
	w := ws("ws-alice", "alice", "sA", hash, "pvc-a", sympoziumv1alpha1.WorkspaceSessionPhaseBound)
	live := run("run-live", "alice", "sA", hash, sympoziumv1alpha1.AgentRunPhaseRunning)

	cleanup := withTestClient(t, "default", w, live)
	defer cleanup()

	cmd := newWorkspaceDeleteCmd()
	_ = cmd.Flags().Set("force", "true")
	_ = captureStdout(t, func() {
		if err := cmd.RunE(cmd, []string{"ws-alice"}); err != nil {
			t.Fatalf("delete --force: %v", err)
		}
	})

	got := &sympoziumv1alpha1.WorkspaceSession{}
	err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: "default", Name: "ws-alice"}, got)
	if err == nil {
		t.Errorf("workspace should be gone after --force delete")
	}
}

// --- exec -------------------------------------------------------------------

func TestWorkspaceExecCmd_RefusesWhenLiveRunAttached(t *testing.T) {
	const hash = "0123456789abcdef"
	w := ws("ws-alice", "alice", "sA", hash, "pvc-a", sympoziumv1alpha1.WorkspaceSessionPhaseBound)
	live := run("run-live", "alice", "sA", hash, sympoziumv1alpha1.AgentRunPhaseRunning)

	cleanup := withTestClient(t, "default", w, live, pvc("pvc-a", "1Gi"))
	defer cleanup()

	cmd := newWorkspaceExecCmd()
	err := cmd.RunE(cmd, []string{"ws-alice"})
	if err == nil {
		t.Fatal("expected error: PVC is RWO and a live run is attached")
	}
	if !strings.Contains(err.Error(), "mounted by AgentRun") {
		t.Errorf("error should mention the live attacher, got: %v", err)
	}

	// No debug pod should have been created.
	pods := &corev1.PodList{}
	if err := k8sClient.List(context.Background(), pods, client.InNamespace("default")); err != nil {
		t.Fatalf("list pods: %v", err)
	}
	for _, p := range pods.Items {
		if strings.HasPrefix(p.GenerateName, "ws-debug-") || strings.HasPrefix(p.Name, "ws-debug-") {
			t.Errorf("debug pod should not have been created, got %q", p.Name)
		}
	}
}

func TestWorkspaceExecCmd_RefusesWhenPVCNotYetBound(t *testing.T) {
	w := ws("ws-alice", "alice", "sA", "h1", "", sympoziumv1alpha1.WorkspaceSessionPhasePending)

	cleanup := withTestClient(t, "default", w)
	defer cleanup()

	cmd := newWorkspaceExecCmd()
	err := cmd.RunE(cmd, []string{"ws-alice"})
	if err == nil {
		t.Fatal("expected error when WorkspaceSession has no PVCName yet")
	}
	if !strings.Contains(err.Error(), "no PVC yet") {
		t.Errorf("error should mention missing PVC, got: %v", err)
	}
}

func TestWorkspaceExecCmd_CreatesDebugPodWhenIdle(t *testing.T) {
	const hash = "0123456789abcdef"
	w := ws("ws-alice", "alice", "sA", hash, "pvc-a", sympoziumv1alpha1.WorkspaceSessionPhaseBound)
	done := run("run-done", "alice", "sA", hash, sympoziumv1alpha1.AgentRunPhaseSucceeded)

	cleanup := withTestClient(t, "default", w, done, pvc("pvc-a", "1Gi"))
	defer cleanup()

	cmd := newWorkspaceExecCmd()
	// Avoid waiting 30s for a fake Pod that never reaches Ready.
	_ = cmd.Flags().Set("wait", "10ms")
	out := captureStdout(t, func() {
		// Error here is the "not Ready" warning printed to stderr, not
		// returned — RunE itself should succeed.
		if err := cmd.RunE(cmd, []string{"ws-alice"}); err != nil {
			t.Fatalf("exec: %v", err)
		}
	})
	if !strings.Contains(out, "Debug pod created") {
		t.Errorf("expected creation message, got:\n%s", out)
	}
	if !strings.Contains(out, "kubectl exec -it") {
		t.Errorf("expected kubectl attach hint, got:\n%s", out)
	}

	pods := &corev1.PodList{}
	if err := k8sClient.List(context.Background(), pods, client.InNamespace("default")); err != nil {
		t.Fatalf("list pods: %v", err)
	}
	found := false
	for _, p := range pods.Items {
		if p.Labels["sympozium.ai/workspacesession"] != "ws-alice" {
			continue
		}
		found = true
		if p.Labels["sympozium.ai/component"] != "workspace-debug" {
			t.Errorf("missing component label: %v", p.Labels)
		}
		if p.Spec.RestartPolicy != corev1.RestartPolicyNever {
			t.Errorf("restart policy = %v, want Never", p.Spec.RestartPolicy)
		}
		if p.Spec.ActiveDeadlineSeconds == nil || *p.Spec.ActiveDeadlineSeconds <= 0 {
			t.Errorf("ActiveDeadlineSeconds not set: %v", p.Spec.ActiveDeadlineSeconds)
		}
		if len(p.Spec.Volumes) != 1 || p.Spec.Volumes[0].PersistentVolumeClaim == nil ||
			p.Spec.Volumes[0].PersistentVolumeClaim.ClaimName != "pvc-a" {
			t.Errorf("PVC volume not wired: %+v", p.Spec.Volumes)
		}
		if len(p.Spec.Containers) != 1 || p.Spec.Containers[0].WorkingDir != "/workspace" {
			t.Errorf("container should chdir to /workspace, got %+v", p.Spec.Containers)
		}
		if len(p.Spec.Containers[0].VolumeMounts) != 1 || p.Spec.Containers[0].VolumeMounts[0].MountPath != "/workspace" {
			t.Errorf("workspace mount missing: %+v", p.Spec.Containers[0].VolumeMounts)
		}
	}
	if !found {
		t.Errorf("debug pod not created for ws-alice; pods=%+v", podNames(pods.Items))
	}
}

func podNames(ps []corev1.Pod) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		if p.Name != "" {
			out[i] = p.Name
		} else {
			out[i] = p.GenerateName + "<gen>"
		}
	}
	return out
}

// --- show -------------------------------------------------------------------

func TestWorkspaceShowCmd_PrintsWorkspacePVCAndLastRun(t *testing.T) {
	const hash = "0123456789abcdef"
	w := ws("ws-alice", "alice", "sA", hash, "pvc-a", sympoziumv1alpha1.WorkspaceSessionPhaseBound)
	w.Status.LastRunName = "run-prev"
	now := metav1.Now()
	w.Status.LastTouchedAt = &now
	last := run("run-prev", "alice", "sA", hash, sympoziumv1alpha1.AgentRunPhaseSucceeded)
	last.Status.StartedAt = &now
	last.Status.CompletedAt = &now

	cleanup := withTestClient(t, "default", w, last, pvc("pvc-a", "1Gi"))
	defer cleanup()

	cmd := newWorkspaceShowCmd()
	out := captureStdout(t, func() {
		if err := cmd.RunE(cmd, []string{"ws-alice"}); err != nil {
			t.Fatalf("show: %v", err)
		}
	})
	for _, want := range []string{
		"WorkspaceSession",
		"ws-alice",
		"alice",
		"Bound",
		"PVC",
		"pvc-a",
		"Last AgentRun",
		"run-prev",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("show output missing %q\noutput:\n%s", want, out)
		}
	}
}

func TestWorkspaceShowCmd_HandlesMissingPVCGracefully(t *testing.T) {
	w := ws("ws-alice", "alice", "sA", "h1", "pvc-gone", sympoziumv1alpha1.WorkspaceSessionPhaseBound)

	cleanup := withTestClient(t, "default", w)
	defer cleanup()

	cmd := newWorkspaceShowCmd()
	out := captureStdout(t, func() {
		if err := cmd.RunE(cmd, []string{"ws-alice"}); err != nil {
			t.Fatalf("show: %v", err)
		}
	})
	if !strings.Contains(out, "NOT FOUND") {
		t.Errorf("show should flag missing PVC, got:\n%s", out)
	}
}

// Ensure os.Stdout is restored even when individual tests run in any
// order under -shuffle.
func TestMain(m *testing.M) {
	orig := os.Stdout
	code := m.Run()
	os.Stdout = orig
	os.Exit(code)
}
