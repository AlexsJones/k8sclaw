package controller

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// ── backend parity ────────────────────────────────────────────────────────────
//
// An AgentRun runs as either a batchv1.Job (reconcilePending) or an agent-sandbox
// Sandbox CR (reconcilePendingAgentSandbox). Both are expected to produce the same
// pod.
//
// These tests enter at the reconcile functions rather than the builders. The
// builders are shared, so divergence arises at the call site — in which
// prerequisites and mutators each path applies. The assertions compare what each
// backend persisted: a Job in the fake client, a Sandbox CR in the fake dynamic
// client.
//
// Tier 1 (TestBackendParity) requires identical pod specs from the two task-mode
// backends. Tier 2 (TestBackendInvariants) covers the paths that differ by design
// — postRun Job, sandbox runtimeClassName, SandboxClaim — with per-path
// invariants.

// parityBaseline suppresses accepted divergences between the Job and Sandbox
// backends.
//
// Key: dotted field path as produced by diffStructs, optionally ending in "*".
// Value: reason, prefixed "intentional:" or "known-gap:" with an issue number for
// the latter.
//
// Currently empty: both backends render from buildAgentPodTemplate. An added entry
// means the two ship different pods and needs sign-off in review.
// TestParityBaselineHasNoStaleEntries fails entries that no longer match a
// divergence.
var parityBaseline = map[string]string{}

// ── Tier 1: strict Job ↔ Sandbox parity ───────────────────────────────────────

func TestBackendParity(t *testing.T) {
	for _, sc := range parityScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			if sc.guards != "" {
				t.Logf("guards: %s", sc.guards)
			}

			jobSpec := jobBackendPodSpec(t, sc)
			sandboxSpec := sandboxBackendPodSpec(t, sc)

			// A fixture that stops triggering its feature renders it as absent on
			// both backends, leaving the diff below empty. Assert presence on each
			// side first.
			assertScenarioIsLive(t, "job backend", sc, jobSpec)
			assertScenarioIsLive(t, "sandbox backend", sc, sandboxSpec)

			diffs := diffStructs(t, "spec", &jobSpec, &sandboxSpec)

			for _, d := range diffs {
				if reason, ok := matchBaseline(d.path); ok {
					t.Logf("known divergence at %s: %s", d.path, reason)
					continue
				}
				t.Errorf("backend divergence at %s\n  job backend:     %s\n  sandbox backend: %s\n\n"+
					"Both backends render from buildAgentPodTemplate; apply shared pod changes there "+
					"or register a podMutator.",
					d.path, d.a, d.b)
			}
		})
	}
}

// assertScenarioIsLive checks the scenario's feature landed on the agent
// container, so parity is compared over a pod that carries it.
func assertScenarioIsLive(t *testing.T, side string, sc parityScenario, spec corev1.PodSpec) {
	t.Helper()

	agent := agentContainer(&spec)
	if agent == nil {
		t.Fatalf("%s: scenario %q rendered no %q container", side, sc.name, agentContainerName)
	}

	present := make(map[string]corev1.EnvVar, len(agent.Env))
	for _, e := range agent.Env {
		present[e.Name] = e
	}
	for _, want := range sc.wantAgentEnv {
		e, ok := present[want]
		if !ok {
			t.Fatalf("%s: scenario %q expects env %s on the agent container; it is absent.\n"+
				"Either the fixture no longer triggers the feature, or the feature regressed on "+
				"this backend.", side, sc.name, want)
		}
		if e.Value == "" && e.ValueFrom == nil {
			t.Errorf("%s: scenario %q env %s is present but carries neither value nor valueFrom",
				side, sc.name, want)
		}
	}
	for _, want := range sc.wantInitContainers {
		if !hasContainerNamed(spec.InitContainers, want) {
			t.Fatalf("%s: scenario %q expects init container %q; got %v",
				side, sc.name, want, containerNames(spec.InitContainers))
		}
	}
	for _, want := range sc.wantVolumes {
		if !hasVolumeNamed(spec.Volumes, want) {
			t.Fatalf("%s: scenario %q expects volume %q; got %v",
				side, sc.name, want, volumeNames(spec.Volumes))
		}
	}
}

func hasContainerNamed(list []corev1.Container, name string) bool {
	for _, c := range list {
		if c.Name == name {
			return true
		}
	}
	return false
}

func hasVolumeNamed(list []corev1.Volume, name string) bool {
	for _, v := range list {
		if v.Name == name {
			return true
		}
	}
	return false
}

func containerNames(list []corev1.Container) []string {
	out := make([]string, 0, len(list))
	for _, c := range list {
		out = append(out, c.Name)
	}
	return out
}

func volumeNames(list []corev1.Volume) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, v.Name)
	}
	return out
}

// parityScenario describes one AgentRun plus cluster state, rendered through both
// backends. The only difference between the two runs is
// spec.agentSandbox.enabled.
type parityScenario struct {
	name string
	// guards names what this scenario covers; logged on run.
	guards string
	// objects returns the cluster state (Agent, Ensemble, memory Deployment, …).
	// Called once per backend so the two runs never share pointers.
	objects func() []client.Object
	// mutate customises the AgentRun beyond the shared default.
	mutate func(*sympoziumv1alpha1.AgentRun)

	// wantAgentEnv, wantInitContainers, and wantVolumes are liveness assertions
	// rather than parity assertions: they hold on each backend independently, so a
	// fixture that stops triggering its feature fails rather than producing an
	// empty diff.
	wantAgentEnv       []string
	wantInitContainers []string
	wantVolumes        []string
}

func parityScenarios() []parityScenario {
	return []parityScenario{
		{
			name:         "baseline_no_features",
			guards:       "pod security (seccompProfile), serviceAccount, restartPolicy, nodeSelector, imagePullSecrets",
			objects:      func() []client.Object { return []client.Object{parityAgent()} },
			wantAgentEnv: []string{"TASK", "MODEL_NAME", "MODEL_PROVIDER"},
			wantVolumes:  []string{"workspace", "ipc", "tmp"},
		},
		{
			name:   "auth_secret_ref",
			guards: "env[].valueFrom SecretKeyRef — the provider API key",
			objects: func() []client.Object {
				return []client.Object{parityAgent(), &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "default"},
				}}
			},
			mutate: func(run *sympoziumv1alpha1.AgentRun) {
				run.Spec.Model.AuthSecretRef = "my-secret"
			},
			wantAgentEnv: []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
		},
		{
			name:    "canary_mode",
			guards:  "env[].valueFrom FieldRef — HOST_IP",
			objects: func() []client.Object { return []client.Object{parityAgent()} },
			mutate: func(run *sympoziumv1alpha1.AgentRun) {
				run.Spec.CanaryMode = true
			},
			wantAgentEnv: []string{"CANARY_MODE", "HOST_IP"},
		},
		{
			name:    "node_selector_and_pull_secrets",
			guards:  "pod-level scheduling and registry credentials",
			objects: func() []client.Object { return []client.Object{parityAgent()} },
			mutate: func(run *sympoziumv1alpha1.AgentRun) {
				run.Spec.Model.NodeSelector = map[string]string{"gpu": "true"}
				run.Spec.ImagePullSecrets = []corev1.LocalObjectReference{{Name: "regcred"}}
			},
		},
		{
			name:    "run_timeout",
			guards:  "RUN_TIMEOUT env from spec.timeout",
			objects: func() []client.Object { return []client.Object{parityAgent()} },
			mutate: func(run *sympoziumv1alpha1.AgentRun) {
				run.Spec.Timeout = &metav1.Duration{Duration: 15 * 60 * 1e9}
			},
			wantAgentEnv: []string{"RUN_TIMEOUT"},
		},
		{
			name:   "observability",
			guards: "OTel env vars and resourceAttributes",
			objects: func() []client.Object {
				agent := parityAgent()
				agent.Spec.Observability = &sympoziumv1alpha1.ObservabilitySpec{
					Enabled:            true,
					OTLPEndpoint:       "http://otel-collector.observability.svc:4318",
					ResourceAttributes: map[string]string{"team": "platform"},
				}
				return []client.Object{agent}
			},
			wantAgentEnv: []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_RESOURCE_ATTRIBUTES"},
		},
		{
			name:    "user_supplied_csi_volume",
			guards:  "volumeToMap losslessness — an arbitrary user volume source",
			objects: func() []client.Object { return []client.Object{parityAgent()} },
			mutate: func(run *sympoziumv1alpha1.AgentRun) {
				readOnly := true
				run.Spec.Volumes = []corev1.Volume{{
					Name: "vault-secrets",
					VolumeSource: corev1.VolumeSource{
						CSI: &corev1.CSIVolumeSource{
							Driver:           "secrets-store.csi.k8s.io",
							ReadOnly:         &readOnly,
							VolumeAttributes: map[string]string{"secretProviderClass": "vault-db"},
						},
					},
				}}
			},
			wantVolumes: []string{"vault-secrets"},
		},
		{
			name:   "memory_skill",
			guards: "MEMORY_SERVER_URL env and the wait-for-memory init container behind the readiness gate",
			objects: func() []client.Object {
				agent := parityAgent()
				agent.Spec.Memory = &sympoziumv1alpha1.MemorySpec{Enabled: true}
				return []client.Object{agent, readyMemoryDeployment("my-instance")}
			},
			mutate: func(run *sympoziumv1alpha1.AgentRun) {
				run.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "memory"}}
			},
			wantAgentEnv: []string{"MEMORY_SERVER_URL"},
		},
		{
			name:   "memory_auto_store_disabled",
			guards: "MEMORY_AUTO_STORE from the Agent's memory.autoStore opt-out (#310)",
			objects: func() []client.Object {
				agent := parityAgent()
				autoStore := false
				agent.Spec.Memory = &sympoziumv1alpha1.MemorySpec{Enabled: true, AutoStore: &autoStore}
				return []client.Object{agent, readyMemoryDeployment("my-instance")}
			},
			mutate: func(run *sympoziumv1alpha1.AgentRun) {
				run.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "memory"}}
			},
			wantAgentEnv: []string{"MEMORY_AUTO_STORE"},
		},
		{
			name:    "lifecycle_prerun_hooks",
			guards:  "preRun hook containers rendered as init containers by buildContainers",
			objects: func() []client.Object { return []client.Object{parityAgent()} },
			mutate: func(run *sympoziumv1alpha1.AgentRun) {
				run.Spec.Lifecycle = &sympoziumv1alpha1.LifecycleHooks{
					PreRun: []sympoziumv1alpha1.LifecycleHookContainer{
						{Name: "warm", Image: "busybox:1.36", Command: []string{"sh", "-c", "true"}},
					},
				}
			},
			wantInitContainers: []string{"pre-warm"},
		},
		{
			name:   "provider_headers_from_secret",
			guards: "resolveProviderHeaders + MODEL_PROVIDER_HEADERS env",
			objects: func() []client.Object {
				return []client.Object{parityAgent(), &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "headers-secret", Namespace: "default"},
					Data:       map[string][]byte{"x-tenant": []byte("acme")},
				}}
			},
			mutate: func(run *sympoziumv1alpha1.AgentRun) {
				run.Spec.Model.ProviderHeadersSecretRef = "headers-secret"
			},
			wantAgentEnv: []string{"MODEL_PROVIDER_HEADERS"},
		},
		{
			name:   "ensemble_shared_memory",
			guards: "injectSharedMemory mutator + its wait-for-shared-memory init container",
			objects: func() []client.Object {
				agent := parityAgent()
				agent.Labels = map[string]string{
					"sympozium.ai/ensemble":     "my-pack",
					"sympozium.ai/agent-config": "researcher",
				}
				return []client.Object{agent, &sympoziumv1alpha1.Ensemble{
					ObjectMeta: metav1.ObjectMeta{Name: "my-pack", Namespace: "default"},
					Spec: sympoziumv1alpha1.EnsembleSpec{
						SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{
							Enabled: true,
							AccessRules: []sympoziumv1alpha1.SharedMemoryAccessRule{
								{AgentConfig: "researcher", Access: "read-only"},
							},
						},
					},
				}}
			},
			wantAgentEnv:       []string{"WORKFLOW_MEMORY_SERVER_URL", "WORKFLOW_MEMORY_ACCESS"},
			wantInitContainers: []string{"wait-for-shared-memory"},
		},
		{
			name:   "ensemble_relationships",
			guards: "injectRelationshipContext mutator (PERSONA_NAME, ENSEMBLE_RELATIONSHIPS)",
			objects: func() []client.Object {
				agent := parityAgent()
				agent.Labels = map[string]string{
					"sympozium.ai/ensemble":     "my-pack",
					"sympozium.ai/agent-config": "lead",
				}
				return []client.Object{agent, &sympoziumv1alpha1.Ensemble{
					ObjectMeta: metav1.ObjectMeta{Name: "my-pack", Namespace: "default"},
					Spec: sympoziumv1alpha1.EnsembleSpec{
						AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
							{Name: "lead", DisplayName: "Lead"},
							{Name: "worker", DisplayName: "Worker"},
						},
						Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
							{Source: "lead", Target: "worker", Type: "delegation"},
						},
					},
				}}
			},
			wantAgentEnv: []string{"PERSONA_NAME", "ENSEMBLE_RELATIONSHIPS"},
		},
		{
			name:   "subagents_skill",
			guards: "injectSubagentsConfig mutator",
			objects: func() []client.Object {
				agent := parityAgent()
				agent.Spec.Agents.Default.Subagents = &sympoziumv1alpha1.SubagentsSpec{
					MaxChildrenPerAgent: 7,
					MaxConcurrent:       2,
					MaxDepth:            4,
				}
				return []client.Object{agent}
			},
			mutate: func(run *sympoziumv1alpha1.AgentRun) {
				run.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "subagents"}}
			},
			wantAgentEnv: []string{
				"SUBAGENTS_ENABLED", "SUBAGENTS_MAX_CHILDREN",
				"SUBAGENTS_MAX_CONCURRENT", "SUBAGENTS_MAX_DEPTH",
			},
		},
	}
}

// parityAgent is the Agent both backends resolve: validatePolicy requires it to
// exist, and resolveAgentRunInputs reads its memory, observability, and labels.
func parityAgent() *sympoziumv1alpha1.Agent {
	return &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "my-instance", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentSpec{},
	}
}

// parityRun is the AgentRun shared by both backends before sc.mutate runs.
func parityRun() *sympoziumv1alpha1.AgentRun {
	run := newTestRun()
	// newTestRun sets AuthSecretRef; clear it so only the scenario that cares
	// about valueFrom exercises it.
	run.Spec.Model.AuthSecretRef = ""
	return run
}

// jobBackendPodSpec drives reconcilePending and returns the pod spec of the Job
// it created.
func jobBackendPodSpec(t *testing.T, sc parityScenario) corev1.PodSpec {
	t.Helper()

	run := parityRun()
	if sc.mutate != nil {
		sc.mutate(run)
	}

	objs := append(sc.objects(), run)
	r := newAgentRunTestReconciler(t, objs...)

	if _, err := r.reconcilePending(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("job backend: reconcilePending: %v", err)
	}

	var job batchv1.Job
	if err := r.Get(context.Background(), client.ObjectKey{Name: run.Name, Namespace: run.Namespace}, &job); err != nil {
		t.Fatalf("job backend: Job was not created: %v", err)
	}
	return job.Spec.Template.Spec
}

// sandboxBackendPodSpec drives the same scenario with spec.agentSandbox.enabled
// and returns the pod spec embedded in the Sandbox CR, converted back to a typed
// PodSpec so the two backends are compared in the same representation.
func sandboxBackendPodSpec(t *testing.T, sc parityScenario) corev1.PodSpec {
	t.Helper()

	run := parityRun()
	if sc.mutate != nil {
		sc.mutate(run)
	}
	// RuntimeClass is left empty: it has no Job-backend equivalent and is
	// covered separately by TestBackendInvariants.
	run.Spec.AgentSandbox = &sympoziumv1alpha1.AgentSandboxSpec{Enabled: true}

	objs := append(sc.objects(), run)
	r := newAgentRunTestReconciler(t, objs...)

	// Enter through reconcilePending so the backend fork itself is under test.
	if _, err := r.reconcilePending(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("sandbox backend: reconcilePending: %v", err)
	}

	obj, err := r.DynamicClient.Resource(sandboxGVR).Namespace(run.Namespace).
		Get(context.Background(), "sb-"+run.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("sandbox backend: Sandbox CR was not created: %v", err)
	}

	raw, found, err := unstructured.NestedMap(obj.Object, "spec", "podTemplate", "spec")
	if err != nil || !found {
		t.Fatalf("sandbox backend: spec.podTemplate.spec missing (found=%v, err=%v)", found, err)
	}

	var spec corev1.PodSpec
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &spec); err != nil {
		t.Fatalf("sandbox backend: pod spec does not round-trip to corev1.PodSpec: %v\n"+
			"This usually means a converter emitted a value in the wrong JSON shape.", err)
	}
	return spec
}

// ── Tier 2: per-path invariants for the by-design-different paths ─────────────

// TestBackendInvariants checks the paths where strict parity does not apply.
// They render different pods by design, but each must still hold the invariants
// below, pod security in particular.
func TestBackendInvariants(t *testing.T) {
	t.Run("postRunJob", func(t *testing.T) {
		run := parityRun()
		run.Spec.Lifecycle = &sympoziumv1alpha1.LifecycleHooks{
			PostRun: []sympoziumv1alpha1.LifecycleHookContainer{
				{Name: "publish", Image: "busybox:1.36", Command: []string{"sh", "-c", "true"}},
			},
		}
		r := newAgentRunTestReconciler(t, parityAgent(), run)

		job := r.buildPostRunJob(run, 0, "ok")
		if job == nil {
			t.Fatal("buildPostRunJob returned nil")
		}
		spec := job.Spec.Template.Spec

		// Runs hook containers, not the agent-runner.
		if agentContainer(&spec) != nil {
			t.Error("postRun Job should not carry the agent-runner container")
		}
		// Pod security still applies — a postRun hook is arbitrary user code.
		assertPodSecurityHardened(t, "postRun Job", spec)
	})

	t.Run("sandboxRuntimeClass", func(t *testing.T) {
		run := parityRun()
		run.Spec.AgentSandbox = &sympoziumv1alpha1.AgentSandboxSpec{
			Enabled:      true,
			RuntimeClass: "gvisor",
		}
		r := newAgentRunTestReconciler(t, parityAgent(), run)

		if _, err := r.reconcilePending(context.Background(), logr.Discard(), run); err != nil {
			t.Fatalf("reconcilePending: %v", err)
		}
		obj, err := r.DynamicClient.Resource(sandboxGVR).Namespace(run.Namespace).
			Get(context.Background(), "sb-"+run.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("Sandbox CR not created: %v", err)
		}
		rc, found, _ := unstructured.NestedString(obj.Object, "spec", "podTemplate", "spec", "runtimeClassName")
		if !found || rc != "gvisor" {
			t.Errorf("runtimeClassName = %q (found=%v), want gvisor", rc, found)
		}

		// The sandbox pod must still be hardened, runtimeClass or not.
		raw, _, _ := unstructured.NestedMap(obj.Object, "spec", "podTemplate", "spec")
		var spec corev1.PodSpec
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &spec); err != nil {
			t.Fatalf("pod spec round-trip: %v", err)
		}
		assertPodSecurityHardened(t, "Sandbox CR", spec)
	})

	// The warm-pool path claims a pre-warmed sandbox rather than describing one, so
	// it carries no pod spec: no task, model config, or auth. This pins current
	// behaviour.
	t.Run("sandboxClaimCarriesNoPodSpec", func(t *testing.T) {
		run := parityRun()
		run.Spec.AgentSandbox = &sympoziumv1alpha1.AgentSandboxSpec{
			Enabled:     true,
			WarmPoolRef: "wp-my-instance",
		}
		r := newAgentRunTestReconciler(t, parityAgent(), run)

		if _, err := r.reconcilePending(context.Background(), logr.Discard(), run); err != nil {
			t.Fatalf("reconcilePending: %v", err)
		}
		obj, err := r.DynamicClient.Resource(sandboxClaimGVR).Namespace(run.Namespace).
			Get(context.Background(), "sbc-"+run.Name, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("SandboxClaim not created: %v", err)
		}

		if _, found, _ := unstructured.NestedMap(obj.Object, "spec", "podTemplate"); found {
			t.Error("SandboxClaim now carries a podTemplate — if the warm-pool path delivers the " +
				"agent pod, move it into TestBackendParity and remove this check")
		}
		name, _, _ := unstructured.NestedString(obj.Object, "spec", "warmPoolRef", "name")
		if name != "wp-my-instance" {
			t.Errorf("spec.warmPoolRef.name = %q, want wp-my-instance", name)
		}
	})
}

// assertPodSecurityHardened checks the pod-security floor required of agent pods
// (see the pod security conventions in CLAUDE.md).
func assertPodSecurityHardened(t *testing.T, path string, spec corev1.PodSpec) {
	t.Helper()

	sc := spec.SecurityContext
	if sc == nil {
		t.Errorf("%s: pod securityContext is nil", path)
		return
	}
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Errorf("%s: pod securityContext.runAsNonRoot is not true", path)
	}
	if sc.SeccompProfile == nil {
		t.Errorf("%s: pod securityContext.seccompProfile is unset — agent pods must carry one", path)
	} else if sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("%s: pod seccompProfile.type = %q, want RuntimeDefault", path, sc.SeccompProfile.Type)
	}
}

// ── side-resource parity ──────────────────────────────────────────────────────

// TestBackendParity_SidecarToolsConfigMap compares a resource the pod-spec diff
// cannot see. The sidecar-tools manifest lives in a ConfigMap, so a divergence in
// its contents is invisible to TestBackendParity even though both pods mount it.
//
// The specific hazard: the two paths once built the manifest from different sidecar
// lists — the Job path from the unfiltered list, the sandbox path from the
// RequiresServer-filtered one. A server-only sidecar declaring tools then advertised
// tools to a task-mode agent whose sidecar was not in the pod.
func TestBackendParity_SidecarToolsConfigMap(t *testing.T) {
	sc := parityScenario{
		name:    "sidecar_tools_manifest",
		guards:  "sidecar-tools ConfigMap contents, built from the filtered sidecar list on both paths",
		objects: sidecarToolsObjects,
		mutate: func(run *sympoziumv1alpha1.AgentRun) {
			run.Spec.Skills = []sympoziumv1alpha1.SkillRef{
				{SkillPackRef: "task-tools"},
				{SkillPackRef: "server-tools"},
			}
		},
	}

	jobCM := backendSidecarToolsConfigMap(t, sc, false)
	sandboxCM := backendSidecarToolsConfigMap(t, sc, true)

	if jobCM == nil || sandboxCM == nil {
		t.Fatalf("expected both backends to create a sidecar-tools ConfigMap; job=%v sandbox=%v",
			jobCM != nil, sandboxCM != nil)
	}

	jobManifest := sidecarToolsManifestOf(t, jobCM)
	sandboxManifest := sidecarToolsManifestOf(t, sandboxCM)

	if jobManifest != sandboxManifest {
		t.Errorf("sidecar-tools manifests differ between backends\n  job:     %s\n  sandbox: %s\n\n"+
			"Both paths must build the manifest from the same RequiresServer-filtered sidecar list.",
			jobManifest, sandboxManifest)
	}

	// And the manifest must describe only sidecars the pod actually runs.
	if strings.Contains(jobManifest, "server_only_tool") {
		t.Error("manifest advertises server_only_tool, whose RequiresServer sidecar is filtered " +
			"out of a task-mode pod — the agent could call a tool with no backing container")
	}
	if !strings.Contains(jobManifest, "task_tool") {
		t.Errorf("manifest is missing task_tool; got %s", jobManifest)
	}
}

// sidecarToolsManifestOf returns the ConfigMap's single manifest payload, whatever
// key it is stored under, so the test does not hard-code the filename.
func sidecarToolsManifestOf(t *testing.T, cm *corev1.ConfigMap) string {
	t.Helper()
	if len(cm.Data) != 1 {
		t.Fatalf("expected one key in %s, got %v", cm.Name, mapKeysOf(cm.Data))
	}
	for _, v := range cm.Data {
		return v
	}
	return ""
}

func mapKeysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sidecarToolsObjects supplies two SkillPacks that both declare tools, one of them
// server-only.
func sidecarToolsObjects() []client.Object {
	return []client.Object{
		parityAgent(),
		&sympoziumv1alpha1.SkillPack{
			ObjectMeta: metav1.ObjectMeta{Name: "task-tools", Namespace: "default"},
			Spec: sympoziumv1alpha1.SkillPackSpec{
				Sidecar: &sympoziumv1alpha1.SkillSidecar{
					Image: "example.test/task-tools:v1",
					Tools: []sympoziumv1alpha1.SidecarTool{
						{Name: "task_tool", Exec: []string{"/bin/task-tool"}},
					},
				},
			},
		},
		&sympoziumv1alpha1.SkillPack{
			ObjectMeta: metav1.ObjectMeta{Name: "server-tools", Namespace: "default"},
			Spec: sympoziumv1alpha1.SkillPackSpec{
				Sidecar: &sympoziumv1alpha1.SkillSidecar{
					Image:          "example.test/server-tools:v1",
					RequiresServer: true,
					Tools: []sympoziumv1alpha1.SidecarTool{
						{Name: "server_only_tool", Exec: []string{"/bin/server-tool"}},
					},
				},
			},
		},
	}
}

// backendSidecarToolsConfigMap drives one backend and returns the sidecar-tools
// ConfigMap it created, or nil when it created none.
func backendSidecarToolsConfigMap(t *testing.T, sc parityScenario, sandbox bool) *corev1.ConfigMap {
	t.Helper()

	run := parityRun()
	if sc.mutate != nil {
		sc.mutate(run)
	}
	if sandbox {
		run.Spec.AgentSandbox = &sympoziumv1alpha1.AgentSandboxSpec{Enabled: true}
	}

	objs := append(sc.objects(), run)
	r := newAgentRunTestReconciler(t, objs...)

	if _, err := r.reconcilePending(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("reconcilePending (sandbox=%v): %v", sandbox, err)
	}

	var cm corev1.ConfigMap
	if err := r.Get(context.Background(), client.ObjectKey{
		Name:      run.Name + "-sidecar-tools",
		Namespace: run.Namespace,
	}, &cm); err != nil {
		return nil
	}
	return &cm
}

// TestServerModeLeavesNoSidecarToolsConfigMap pins the ordering constraint that
// keeps prepareRunPrerequisites and prepareTaskPrerequisites split.
//
// The sidecar-tools ConfigMap is only mounted by task-mode pods, so it must be
// written after the server-mode branch. Folding the whole of the prerequisites into
// one helper — the obvious simplification — would leave an orphan ConfigMap behind
// on every server-mode run, owned by the AgentRun and never mounted.
func TestServerModeLeavesNoSidecarToolsConfigMap(t *testing.T) {
	run := parityRun()
	run.Spec.Mode = "server"
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{
		{SkillPackRef: "task-tools"},
		{SkillPackRef: "server-tools"},
	}

	objs := append(sidecarToolsObjects(), run)
	r := newAgentRunTestReconciler(t, objs...)

	// reconcilePendingServer needs cluster plumbing this fixture does not provide,
	// so it may well fail. That is fine: the branch is taken before anything
	// task-mode runs, which is the whole assertion.
	if _, err := r.reconcilePending(context.Background(), logr.Discard(), run); err != nil {
		t.Logf("reconcilePending returned %v (expected for a server-mode fixture)", err)
	}

	var cm corev1.ConfigMap
	err := r.Get(context.Background(), client.ObjectKey{
		Name:      run.Name + "-sidecar-tools",
		Namespace: run.Namespace,
	}, &cm)
	if err == nil {
		t.Error("server-mode run created a sidecar-tools ConfigMap; only task-mode pods mount it, " +
			"so this is an orphan. The server-mode branch must stay between " +
			"prepareRunPrerequisites and prepareTaskPrerequisites.")
	}
}

// ── the mutator registry guard ────────────────────────────────────────────────

// TestNoOrphanPodMutators fails when an inject* method on *AgentRunReconciler is
// absent from podMutators.
//
// buildAgentPodTemplate applies the registry, so a listed mutator applies to both
// backends; an unlisted one applies only where it is called explicitly.
func TestNoOrphanPodMutators(t *testing.T) {
	registered := map[string]bool{}
	for _, m := range podMutators {
		registered[m.name] = true
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse controller package: %v", err)
	}

	var found []string
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
					continue
				}
				if !isAgentRunReconcilerReceiver(fn.Recv.List[0].Type) {
					continue
				}
				name := fn.Name.Name
				if !strings.HasPrefix(name, "inject") || name == "inject" {
					continue
				}
				found = append(found, name)

				// injectSharedMemory → "sharedMemory"
				key := strings.ToLower(name[len("inject"):len("inject")+1]) + name[len("inject")+1:]
				if !registered[key] {
					t.Errorf("%s (%s) is not registered in podMutators as %q.\n"+
						"Unlisted mutators apply only where they are called explicitly, covering one "+
						"execution backend. Add it to podMutators in agentrun_pod_mutators.go; "+
						"buildAgentPodTemplate applies the registry to both.",
						name, path, key)
				}
			}
		}
	}

	if len(found) == 0 {
		t.Fatal("found no inject* methods on *AgentRunReconciler — the AST scan is not matching, " +
			"so this guard would pass without checking anything")
	}
	sort.Strings(found)
	t.Logf("scanned %d inject* methods: %s", len(found), strings.Join(found, ", "))
}

func isAgentRunReconcilerReceiver(expr ast.Expr) bool {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "AgentRunReconciler"
}

// ── baseline hygiene ──────────────────────────────────────────────────────────

// TestParityBaselineEntriesHaveReasons requires each suppression to state a reason
// and whether it is intentional or a tracked gap.
func TestParityBaselineEntriesHaveReasons(t *testing.T) {
	for path, reason := range parityBaseline {
		switch {
		case strings.TrimSpace(reason) == "":
			t.Errorf("parityBaseline[%q]: empty reason", path)
		case !strings.HasPrefix(reason, "intentional:") && !strings.HasPrefix(reason, "known-gap:"):
			t.Errorf("parityBaseline[%q]: reason must start with \"intentional:\" or \"known-gap:\", got %q", path, reason)
		}
	}
}

// TestParityBaselineHasNoStaleEntries fails on baseline entries that no longer
// match a divergence.
func TestParityBaselineHasNoStaleEntries(t *testing.T) {
	if len(parityBaseline) == 0 {
		return
	}

	used := map[string]bool{}
	for _, sc := range parityScenarios() {
		jobSpec := jobBackendPodSpec(t, sc)
		sandboxSpec := sandboxBackendPodSpec(t, sc)
		for _, d := range diffStructs(t, "spec", &jobSpec, &sandboxSpec) {
			if _, ok := matchBaseline(d.path); ok {
				used[baselineKeyFor(d.path)] = true
			}
		}
	}
	for path := range parityBaseline {
		if !used[path] {
			t.Errorf("parityBaseline[%q] matches no actual divergence — delete it", path)
		}
	}
}

func matchBaseline(path string) (string, bool) {
	key := baselineKeyFor(path)
	if key == "" {
		return "", false
	}
	return parityBaseline[key], true
}

// baselineKeyFor returns the baseline key covering path, or "" when none does.
func baselineKeyFor(path string) string {
	if _, ok := parityBaseline[path]; ok {
		return path
	}
	for key := range parityBaseline {
		if strings.HasSuffix(key, "*") && strings.HasPrefix(path, strings.TrimSuffix(key, "*")) {
			return key
		}
	}
	return ""
}

// ── compile-time assertion that the fixtures stay realistic ──────────────────

// TestParityScenariosCoverEveryMutator fails when a registered pod mutator has no
// scenario exercising it. A mutator with no fixture compares absent to absent on
// both backends.
func TestParityScenariosCoverEveryMutator(t *testing.T) {
	// Scenario name → the mutator it exercises.
	covered := map[string]string{
		"ensemble_shared_memory":     "sharedMemory",
		"ensemble_relationships":     "relationshipContext",
		"subagents_skill":            "subagentsConfig",
		"memory_auto_store_disabled": "memoryConfig",
	}

	exercised := map[string]bool{}
	names := map[string]bool{}
	for _, sc := range parityScenarios() {
		names[sc.name] = true
		if m, ok := covered[sc.name]; ok {
			exercised[m] = true
		}
	}
	for scenario := range covered {
		if !names[scenario] {
			t.Errorf("coverage map names scenario %q, which no longer exists — update the map", scenario)
		}
	}
	for _, m := range podMutators {
		if !exercised[m.name] {
			t.Errorf("pod mutator %q has no parity scenario exercising it.\n"+
				"Add one to parityScenarios() and map it in this test; otherwise parity for that "+
				"feature is compared on a pod that does not carry it.", m.name)
		}
	}
}

// readyMemoryDeployment is the memory server both backends require before
// rendering a pod for a run carrying the memory skill; without a ready replica
// each path requeues.
func readyMemoryDeployment(agentRef string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      agentRef + "-memory",
			Namespace: "default",
		},
		Status: appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
}
