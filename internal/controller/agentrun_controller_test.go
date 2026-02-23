package controller

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	k8sclawv1alpha1 "github.com/k8sclaw/k8sclaw/api/v1alpha1"
)

// helper builds a minimal AgentRun for testing.
func newTestRun() *k8sclawv1alpha1.AgentRun {
	return &k8sclawv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-run",
			Namespace: "default",
		},
		Spec: k8sclawv1alpha1.AgentRunSpec{
			InstanceRef: "my-instance",
			AgentID:     "default",
			SessionKey:  "sess-1",
			Task:        "do stuff",
			Model: k8sclawv1alpha1.ModelSpec{
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
	job := r.buildJob(run, false)

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
	job := r.buildJob(run, false)

	labels := job.Spec.Template.Labels
	if labels["k8sclaw.io/instance"] != "my-instance" {
		t.Errorf("instance label = %q", labels["k8sclaw.io/instance"])
	}
	if labels["k8sclaw.io/agent-run"] != "test-run" {
		t.Errorf("agent-run label = %q", labels["k8sclaw.io/agent-run"])
	}
	if labels["k8sclaw.io/component"] != "agent-run" {
		t.Errorf("component label = %q", labels["k8sclaw.io/component"])
	}
}

func TestBuildJob_TTLAndBackoff(t *testing.T) {
	r := &AgentRunReconciler{}
	job := r.buildJob(newTestRun(), false)

	if job.Spec.TTLSecondsAfterFinished == nil || *job.Spec.TTLSecondsAfterFinished != 300 {
		t.Error("TTL should be 300")
	}
	if job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 {
		t.Error("BackoffLimit should be 0")
	}
}

func TestBuildJob_DeadlineDefault(t *testing.T) {
	r := &AgentRunReconciler{}
	job := r.buildJob(newTestRun(), false)

	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 600 {
		t.Errorf("deadline = %v, want 600", job.Spec.ActiveDeadlineSeconds)
	}
}

func TestBuildJob_DeadlineWithTimeout(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Timeout = &metav1.Duration{Duration: 5 * time.Minute}
	job := r.buildJob(run, false)

	// 5min = 300s + 60 = 360
	if job.Spec.ActiveDeadlineSeconds == nil || *job.Spec.ActiveDeadlineSeconds != 360 {
		t.Errorf("deadline = %v, want 360", job.Spec.ActiveDeadlineSeconds)
	}
}

func TestBuildJob_ServiceAccount(t *testing.T) {
	r := &AgentRunReconciler{}
	job := r.buildJob(newTestRun(), false)

	if job.Spec.Template.Spec.ServiceAccountName != "k8sclaw-agent" {
		t.Errorf("SA = %q, want k8sclaw-agent", job.Spec.Template.Spec.ServiceAccountName)
	}
}

func TestBuildJob_PodSecurityContext(t *testing.T) {
	r := &AgentRunReconciler{}
	job := r.buildJob(newTestRun(), false)

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
	job := r.buildJob(newTestRun(), false)

	if job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restart = %q, want Never", job.Spec.Template.Spec.RestartPolicy)
	}
}

// ── buildContainers tests ────────────────────────────────────────────────────

func TestBuildContainers_BasicCount(t *testing.T) {
	r := &AgentRunReconciler{}
	cs := r.buildContainers(newTestRun(), false)
	// agent + ipc-bridge = 2
	if len(cs) != 2 {
		t.Fatalf("container count = %d, want 2", len(cs))
	}
}

func TestBuildContainers_AgentImage(t *testing.T) {
	r := &AgentRunReconciler{}
	cs := r.buildContainers(newTestRun(), false)
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
	cs := r.buildContainers(newTestRun(), false)
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
	cs := r.buildContainers(run, false)

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
	cs := r.buildContainers(run, false)

	if len(cs[0].EnvFrom) == 0 {
		t.Fatal("expected envFrom for auth secret")
	}
	if cs[0].EnvFrom[0].SecretRef.Name != "my-secret" {
		t.Errorf("secret = %q, want my-secret", cs[0].EnvFrom[0].SecretRef.Name)
	}
}

func TestBuildContainers_NoAuthSecretRef(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Model.AuthSecretRef = ""
	cs := r.buildContainers(run, false)

	if len(cs[0].EnvFrom) != 0 {
		t.Errorf("envFrom should be empty for no-auth providers, got %d", len(cs[0].EnvFrom))
	}
}

func TestBuildContainers_AgentSecurityContext(t *testing.T) {
	r := &AgentRunReconciler{}
	cs := r.buildContainers(newTestRun(), false)

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
	cs := r.buildContainers(newTestRun(), false)

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
	cs := r.buildContainers(newTestRun(), false)

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
	cs := r.buildContainers(run, false)

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
	run.Spec.Sandbox = &k8sclawv1alpha1.AgentRunSandboxSpec{Enabled: true}
	cs := r.buildContainers(run, false)
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
	run.Spec.Sandbox = &k8sclawv1alpha1.AgentRunSandboxSpec{
		Enabled: true,
		Image:   "my-sandbox:v1",
	}
	cs := r.buildContainers(run, false)
	if cs[2].Image != "my-sandbox:v1" {
		t.Errorf("sandbox image = %q, want my-sandbox:v1", cs[2].Image)
	}
}

func TestBuildContainers_SandboxDisabled(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Sandbox = &k8sclawv1alpha1.AgentRunSandboxSpec{Enabled: false}
	cs := r.buildContainers(run, false)
	if len(cs) != 2 {
		t.Errorf("container count = %d, want 2 (sandbox disabled)", len(cs))
	}
}

// ── buildVolumes tests ───────────────────────────────────────────────────────

func TestBuildVolumes_DefaultVolumes(t *testing.T) {
	r := &AgentRunReconciler{}
	vols := r.buildVolumes(newTestRun(), false)

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
	vols := r.buildVolumes(newTestRun(), false)

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

func TestBuildVolumes_SkillsWithRefs(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Skills = []k8sclawv1alpha1.SkillRef{
		{ConfigMapRef: "my-skills"},
	}
	vols := r.buildVolumes(run, false)

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
	vols := r.buildVolumes(run, false)

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
	vols := r.buildVolumes(run, true)

	for _, v := range vols {
		if v.Name == "memory" {
			if v.ConfigMap == nil {
				t.Fatal("memory volume should be a ConfigMap volume")
			}
			expected := run.Spec.InstanceRef + "-memory"
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
	vols := r.buildVolumes(run, false)

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
	cs := r.buildContainers(run, true)

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
