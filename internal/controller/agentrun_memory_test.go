package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// newAgentRunReconcilerWithAgent builds an AgentRunReconciler backed by a fake
// client pre-loaded with an Agent whose memory.autoStore is set to the supplied
// pointer (nil = field unset).
func newAgentRunReconcilerWithAgent(t *testing.T, autoStore *bool) *AgentRunReconciler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}
	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-instance", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentSpec{
			Memory: &sympoziumv1alpha1.MemorySpec{Enabled: true, AutoStore: autoStore},
		},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
	return &AgentRunReconciler{Client: cl, Scheme: scheme, Log: logr.Discard()}
}

func memoryRun() *sympoziumv1alpha1.AgentRun {
	run := newTestRun()
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "memory"}}
	return run
}

func jobEnvValue(job *batchv1.Job, name string) (string, bool) {
	agent := agentContainer(&job.Spec.Template.Spec)
	if agent == nil {
		return "", false
	}
	for _, e := range agent.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// ── injectMemoryConfig: MEMORY_AUTO_STORE opt-out ────────────────────────────
//
// injectMemoryConfig is a registered podMutator, so buildJob applies it via
// buildAgentPodTemplate. Asserting on buildJob's output rather than calling the
// mutator directly is what keeps these tests honest about the wiring: drop the
// registry entry and they fail alongside TestBackendParity.

func TestInjectMemoryConfig_DisablesAutoStore(t *testing.T) {
	r := newAgentRunReconcilerWithAgent(t, boolPtr(false))
	run := memoryRun()
	job, err := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildJob: %v", err)
	}

	v, ok := jobEnvValue(job, "MEMORY_AUTO_STORE")
	if !ok || v != "false" {
		t.Errorf("MEMORY_AUTO_STORE = %q (present=%v), want \"false\"", v, ok)
	}
}

func TestInjectMemoryConfig_EnabledByDefault(t *testing.T) {
	// AutoStore nil (unset) and AutoStore=true must both leave the env var off,
	// preserving the default-enabled behaviour.
	for _, tc := range []struct {
		name string
		val  *bool
	}{
		{"nil", nil},
		{"true", boolPtr(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newAgentRunReconcilerWithAgent(t, tc.val)
			run := memoryRun()
			job, err := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)
			if err != nil {
				t.Fatalf("buildJob: %v", err)
			}

			if _, ok := jobEnvValue(job, "MEMORY_AUTO_STORE"); ok {
				t.Error("MEMORY_AUTO_STORE should not be injected when auto-store is enabled")
			}
		})
	}
}

func TestInjectMemoryConfig_NoopWithoutMemorySkill(t *testing.T) {
	r := newAgentRunReconcilerWithAgent(t, boolPtr(false))
	run := newTestRun() // no memory skill
	job, err := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildJob: %v", err)
	}

	if _, ok := jobEnvValue(job, "MEMORY_AUTO_STORE"); ok {
		t.Error("MEMORY_AUTO_STORE should not be injected when the run has no memory skill")
	}
}

// TestInjectMemoryConfig_SpecEnvWins pins the precedence contract: everything
// buildContainers injects (RUN_TIMEOUT, MEMORY_SERVER_URL, …) is appended ahead
// of spec.env and is therefore user-overridable. The mutator pass runs after
// spec.env, so without an explicit guard this one variable would invert that
// rule — duplicate env names resolve last-wins at the kubelet.
func TestInjectMemoryConfig_SpecEnvWins(t *testing.T) {
	for _, userValue := range []string{"true", "false"} {
		t.Run("spec_env_"+userValue, func(t *testing.T) {
			// Agent says autoStore=false, so the mutator would otherwise inject.
			r := newAgentRunReconcilerWithAgent(t, boolPtr(false))
			run := memoryRun()
			run.Spec.Env = map[string]string{"MEMORY_AUTO_STORE": userValue}

			job, err := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)
			if err != nil {
				t.Fatalf("buildJob: %v", err)
			}

			// Exactly one entry, carrying the user's value: a second entry would
			// win at the kubelet even though the first is the user's.
			var found []string
			for _, e := range agentContainer(&job.Spec.Template.Spec).Env {
				if e.Name == "MEMORY_AUTO_STORE" {
					found = append(found, e.Value)
				}
			}
			if len(found) != 1 || found[0] != userValue {
				t.Errorf("MEMORY_AUTO_STORE entries = %q, want exactly [%q] — spec.env must win", found, userValue)
			}
		})
	}
}

// ── buildContainers: MEMORY_SERVER_URL env injection ─────────────────────────

func TestBuildContainers_MemoryServerURLInjected(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{
		{SkillPackRef: "memory"},
	}

	cs, _, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	// The agent container is always the first one.
	agentContainer := cs[0]

	var found bool
	for _, env := range agentContainer.Env {
		if env.Name == "MEMORY_SERVER_URL" {
			found = true
			expectedURL := "http://my-instance-memory.default.svc:8080"
			if env.Value != expectedURL {
				t.Errorf("MEMORY_SERVER_URL = %q, want %q", env.Value, expectedURL)
			}
			break
		}
	}
	if !found {
		t.Error("MEMORY_SERVER_URL env var not found on agent container when memory skill is attached")
	}
}

func TestBuildContainers_NoMemoryServerURLWithoutSkill(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{
		{SkillPackRef: "k8s-ops"},
	}

	cs, _, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	agentContainer := cs[0]
	for _, env := range agentContainer.Env {
		if env.Name == "MEMORY_SERVER_URL" {
			t.Error("MEMORY_SERVER_URL should not be set without memory skill")
			return
		}
	}
}

// ── buildVolumes: no memory-db volume on agent pods ──────────────────────────

func TestBuildVolumes_NoMemoryDBVolume(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{
		{SkillPackRef: "memory"},
	}

	vols := r.buildVolumes(run, false, nil, nil)

	for _, v := range vols {
		if v.Name == "memory-db" {
			t.Error("memory-db volume should not exist on agent pods (it belongs to the standalone memory Deployment)")
			return
		}
	}
}

func TestBuildVolumes_NoMemoryDBWithoutSkill(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{
		{SkillPackRef: "k8s-ops"},
	}

	vols := r.buildVolumes(run, false, nil, nil)

	for _, v := range vols {
		if v.Name == "memory-db" {
			t.Error("memory-db volume should not exist without memory SkillPack")
			return
		}
	}
}

// ── buildContainers: wait-for-memory init container ─────────────────────────

func TestBuildContainers_WaitForMemoryInitContainer(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{
		{SkillPackRef: "memory"},
	}

	_, initCs, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	var found bool
	for _, ic := range initCs {
		if ic.Name == "wait-for-memory" {
			found = true
			cmd := strings.Join(ic.Command, " ")
			expectedURL := "http://my-instance-memory.default.svc:8080/health"
			if !strings.Contains(cmd, expectedURL) {
				t.Errorf("init container command %q does not contain %q", cmd, expectedURL)
			}
			// Verify security context.
			if ic.SecurityContext == nil {
				t.Fatal("expected SecurityContext to be set")
			}
			if ic.SecurityContext.ReadOnlyRootFilesystem == nil || !*ic.SecurityContext.ReadOnlyRootFilesystem {
				t.Error("expected ReadOnlyRootFilesystem=true")
			}
			break
		}
	}
	if !found {
		t.Error("wait-for-memory init container not found when memory skill is attached")
	}
}

func TestBuildContainers_NoWaitForMemoryWithoutSkill(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{
		{SkillPackRef: "k8s-ops"},
	}

	_, initCs, _ := r.buildContainers(run, false, nil, nil, nil, nil)

	for _, ic := range initCs {
		if ic.Name == "wait-for-memory" {
			t.Error("wait-for-memory init container should not exist without memory skill")
			return
		}
	}
}
