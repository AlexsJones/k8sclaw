package controller

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/agentedit"
)

// ── Ensemble create/update convergence ────────────────────────────────────────
//
// reconcileAgentConfig forks on errors.IsNotFound: a create path that calls
// buildAgent, and an update path for an Agent that already exists. The Ensemble is
// the source of truth for its Agents, so both paths must land on the same spec —
// whichever one ran.
//
// The update path used to propagate field by field, which meant every field added
// to buildAgent needed a matching clause. It fell behind (#264: authRefs, model,
// volumes, agentSandbox, lifecycle). It now assigns the whole spec, and these tests
// hold it there.
//
// The properties are stated against buildAgent's output as the oracle: whatever
// create produces for the current Ensemble is by definition correct, so update is
// correct exactly when it agrees.

// agentFieldsNotExpressibleByEnsemble lists AgentSpec fields buildAgent never
// sets, because AgentConfigSpec and EnsembleSpec have no field to express them.
// Key: Go field path. Value: reason.
//
// Consequence: an Ensemble-managed Agent cannot carry these. The update path
// assigns the whole spec, so a value set out of band is cleared on the next
// reconcile. To make one configurable, add it to AgentConfigSpec and have
// buildAgent plumb it, rather than adding an exception here.
var agentFieldsNotExpressibleByEnsemble = map[string]string{
	"Agents.Default.Thinking":     "no AgentConfigSpec field; per-Agent thinking mode is not an ensemble concept",
	"Agents.Default.Sandbox":      "no AgentConfigSpec field; ensembles configure agentSandbox instead",
	"Agents.Default.NodeSelector": "no AgentConfigSpec field; placement is not expressible per persona",
	"WebEndpoint":                 "superseded by the web-endpoint skill, which buildDesiredSkills adds from persona.webEndpoint",
	"ImagePullSecrets":            "no EnsembleSpec field; cluster-level registry credentials are not an ensemble concept",
}

// agentFieldsPreservedOutOfBand are intentionally not derived from an Ensemble
// but survive reconciliation because a different owner is authoritative for
// them. Keep this list small: every entry is an exception to whole-spec
// convergence and needs explicit reconciliation code plus a preservation test.
var agentFieldsPreservedOutOfBand = map[string]string{
	"RuntimeRef": "runtime selection is administrator-owned rather than persona-owned",
}

// ── the properties ────────────────────────────────────────────────────────────

// TestAgentUpdateConvergesToCreate perturbs every field of a persisted Agent and
// requires reconcileAgentConfig to restore the spec buildAgent produces.
//
// fillStruct sets every field to a non-zero sentinel, so any field the update path
// fails to overwrite shows up as a difference naming that field.
func TestAgentUpdateConvergesToCreate(t *testing.T) {
	pack, persona := convergenceFixture()

	// The oracle: what create would produce right now.
	wantAgent := (&EnsembleReconciler{}).buildAgent(pack, persona, agentInstanceName(pack, persona), "")

	// A persisted Agent whose spec is entirely wrong.
	drifted := wantAgent.DeepCopy()
	fillStruct(t, reflect.ValueOf(&drifted.Spec).Elem(), 0)
	wantAgent.Spec.RuntimeRef = drifted.Spec.RuntimeRef

	r := newEnsembleTestReconciler(t, drifted)
	if _, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), pack, persona, 0, ""); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}

	got := getAgent(t, r, drifted.Name, drifted.Namespace)

	for _, d := range diffStructs(t, "spec", &wantAgent.Spec, &got.Spec) {
		t.Errorf("update path did not converge at %s\n  buildAgent (create): %s\n  after update:        %s\n\n"+
			"reconcileAgentConfig assigns the whole spec from buildAgent; a difference here means "+
			"something is mutating the spec after that assignment.", d.path, d.a, d.b)
	}
}

func TestAgentRuntimeRefSurvivesEnsembleReconcile(t *testing.T) {
	pack, persona := convergenceFixture()
	instanceName := agentInstanceName(pack, persona)
	existing := (&EnsembleReconciler{}).buildAgent(pack, persona, instanceName, "")
	existing.Spec.RuntimeRef = "codex-v1"

	r := newEnsembleTestReconciler(t, existing)
	if _, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), pack, persona, 0, ""); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}

	got := getAgent(t, r, instanceName, existing.Namespace)
	if got.Spec.RuntimeRef != "codex-v1" {
		t.Fatalf("runtimeRef = %q after Ensemble reconcile, want administrator-owned value preserved", got.Spec.RuntimeRef)
	}
}

// TestAgentUpdateStaleEnsembleConverges covers the realistic case: Agents exist,
// then the Ensemble is edited. Every persona field differs between the two packs,
// so a field the update path skipped keeps its first-pack value.
func TestAgentUpdateStaleEnsembleConverges(t *testing.T) {
	oldPack, oldPersona := convergenceFixture()

	newPack, newPersona := convergenceFixture()
	newPack.Spec.PolicyRef = "tightened-policy"
	newPack.Spec.Volumes = nil
	newPack.Spec.VolumeMounts = nil
	newPack.Spec.AgentSandbox = &sympoziumv1alpha1.AgentSandboxDefaults{Enabled: true, RuntimeClass: "gvisor"}
	newPack.Spec.ProviderHeaders = map[string]string{"x-tenant": "b"}
	newPersona.DisplayName = "Renamed Persona"
	newPersona.SystemPrompt = "you are different now"
	newPersona.Model = "gpt-4o-mini"
	newPersona.RunTimeout = "45m"
	newPersona.Env = map[string]string{"MODE": "b"}
	newPersona.Skills = []string{"k8s-ops"}
	newPersona.Subagents = &sympoziumv1alpha1.SubagentsSpec{MaxDepth: 5}
	newPersona.MCPServers = nil
	newPersona.Channels = nil

	instanceName := agentInstanceName(oldPack, oldPersona)

	// Stamp the Agent from the old pack, as the create path would have.
	existing := (&EnsembleReconciler{}).buildAgent(oldPack, oldPersona, instanceName, "")
	r := newEnsembleTestReconciler(t, existing)

	if _, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), newPack, newPersona, 0, ""); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}

	got := getAgent(t, r, instanceName, existing.Namespace)
	want := (&EnsembleReconciler{}).buildAgent(newPack, newPersona, instanceName, "")

	for _, d := range diffStructs(t, "spec", &want.Spec, &got.Spec) {
		t.Errorf("edited Ensemble did not reach the existing Agent at %s\n  want: %s\n  got:  %s",
			d.path, d.a, d.b)
	}
}

// TestAgentLabelsConverge covers the two labels three features resolve Ensemble
// state through: toolpolicy.ForAgent returns nil without them (dropping the
// persona's toolPolicy), injectSharedMemory falls back to read-write access, and
// injectRelationshipContext returns early. All three fail open, so a stale label is
// not a cosmetic problem.
func TestAgentLabelsConverge(t *testing.T) {
	pack, persona := convergenceFixture()
	instanceName := agentInstanceName(pack, persona)

	existing := (&EnsembleReconciler{}).buildAgent(pack, persona, instanceName, "")
	// Simulate an Agent whose resolver labels were lost or never set.
	delete(existing.Labels, "sympozium.ai/ensemble")
	existing.Labels["sympozium.ai/agent-config"] = "stale-name"

	r := newEnsembleTestReconciler(t, existing)
	if _, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), pack, persona, 0, ""); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}

	got := getAgent(t, r, instanceName, existing.Namespace)
	for label, want := range map[string]string{
		"sympozium.ai/ensemble":     pack.Name,
		"sympozium.ai/agent-config": persona.Name,
	} {
		if got.Labels[label] != want {
			t.Errorf("label %s = %q, want %q — toolpolicy.ForAgent and the sharedMemory/"+
				"relationshipContext pod mutators resolve their configuration through this label "+
				"and fail open without it", label, got.Labels[label], want)
		}
	}

	// A provider label that no longer applies must be removed, not left stale.
	noProvider, noProviderPersona := convergenceFixture()
	noProviderPersona.Provider = ""
	name2 := agentInstanceName(noProvider, noProviderPersona)
	stale := (&EnsembleReconciler{}).buildAgent(noProvider, noProviderPersona, name2, "")
	stale.Labels["sympozium.ai/provider"] = "openai"

	r2 := newEnsembleTestReconciler(t, stale)
	if _, err := r2.reconcileAgentConfig(context.Background(), logr.Discard(), noProvider, noProviderPersona, 0, ""); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}
	got2 := getAgent(t, r2, name2, stale.Namespace)
	if v, ok := got2.Labels["sympozium.ai/provider"]; ok {
		t.Errorf("stale provider label survived as %q; persona.Provider is empty so it should be removed", v)
	}
}

// TestAgentSpecFieldsAreEnsembleExpressible requires every AgentSpec field to be
// either set by buildAgent or declared inexpressible.
//
// This is the anti-recurrence guard. Adding a field to AgentSpec now forces a
// decision: plumb it through buildAgent, or record why an Ensemble cannot express
// it. Without this, a new field silently joins the set that Ensemble-managed Agents
// can never carry.
func TestAgentSpecFieldsAreEnsembleExpressible(t *testing.T) {
	pack, persona := convergenceFixture()
	built := (&EnsembleReconciler{}).buildAgent(pack, persona, agentInstanceName(pack, persona), "")

	specType := reflect.TypeOf(built.Spec)
	specVal := reflect.ValueOf(built.Spec)

	var checked int
	for _, path := range enumerateFieldPaths(specType, "") {
		v, ok := fieldByPath(specVal, path)
		if !ok {
			continue
		}
		checked++
		if !v.IsZero() {
			if reason, declared := agentFieldsNotExpressibleByEnsemble[path]; declared {
				t.Errorf("agentFieldsNotExpressibleByEnsemble[%q] says %q, but buildAgent does set it — delete the entry",
					path, reason)
			}
			if reason, declared := agentFieldsPreservedOutOfBand[path]; declared {
				t.Errorf("agentFieldsPreservedOutOfBand[%q] says %q, but buildAgent does set it — delete the entry",
					path, reason)
			}
			continue
		}
		if _, declared := agentFieldsNotExpressibleByEnsemble[path]; declared {
			continue
		}
		if _, declared := agentFieldsPreservedOutOfBand[path]; declared {
			continue
		}
		t.Errorf("buildAgent leaves AgentSpec.%s unset.\n"+
			"Either derive it from the Ensemble/persona, or add it to "+
			"agentFieldsNotExpressibleByEnsemble with a reason. reconcileAgentConfig assigns the "+
			"whole spec, so an undeclared field is cleared on every reconcile of a managed Agent.",
			path)
	}
	if checked == 0 {
		t.Fatal("enumerated no AgentSpec fields — the reflection walk is broken, so this guard " +
			"would pass without checking anything")
	}
	t.Logf("checked %d AgentSpec field paths", checked)
}

// TestInexpressibleFieldsHaveReasons keeps the declaration list honest.
func TestInexpressibleFieldsHaveReasons(t *testing.T) {
	specType := reflect.TypeOf(sympoziumv1alpha1.AgentSpec{})
	valid := map[string]bool{}
	for _, p := range enumerateFieldPaths(specType, "") {
		valid[p] = true
	}
	for listName, fields := range map[string]map[string]string{
		"agentFieldsNotExpressibleByEnsemble": agentFieldsNotExpressibleByEnsemble,
		"agentFieldsPreservedOutOfBand":       agentFieldsPreservedOutOfBand,
	} {
		for path, reason := range fields {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s[%q] has an empty reason", listName, path)
			}
			if !valid[path] {
				t.Errorf("%s[%q]: no such AgentSpec field path — delete the entry", listName, path)
			}
		}
	}
}

// TestScheduleUpdateConvergesToCreate pins the schedule fork's whole-spec pattern,
// which the Agent fork now follows. Suspend is the one field carried over from the
// existing object: it is set out of band by an operator or an agent's schedule_task
// tool, and overwriting it would resume a paused schedule.
func TestScheduleUpdateConvergesToCreate(t *testing.T) {
	pack, persona := convergenceFixture()
	persona.Schedule = &sympoziumv1alpha1.AgentConfigSchedule{Cron: "*/5 * * * *"}
	instanceName := agentInstanceName(pack, persona)
	schedName := instanceName + "-schedule"

	r0 := &EnsembleReconciler{}
	want := r0.buildSchedule(pack, persona, instanceName, schedName, 0)

	drifted := want.DeepCopy()
	fillStruct(t, reflect.ValueOf(&drifted.Spec).Elem(), 0)
	drifted.Spec.Suspend = true // out-of-band pause that must survive

	agent := r0.buildAgent(pack, persona, instanceName, "")
	r := newEnsembleTestReconciler(t, agent, drifted)
	if _, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), pack, persona, 0, ""); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}

	var got sympoziumv1alpha1.SympoziumSchedule
	if err := r.Get(context.Background(), client.ObjectKey{Name: schedName, Namespace: pack.Namespace}, &got); err != nil {
		t.Fatalf("get schedule: %v", err)
	}

	if !got.Spec.Suspend {
		t.Error("suspend was cleared; it is out-of-band state and must be carried over")
	}

	// Compare everything else against the oracle.
	wantWithSuspend := want.DeepCopy()
	wantWithSuspend.Spec.Suspend = true
	for _, d := range diffStructs(t, "spec", &wantWithSuspend.Spec, &got.Spec) {
		t.Errorf("schedule update did not converge at %s\n  want: %s\n  got:  %s", d.path, d.a, d.b)
	}
}

// ── fixtures and helpers ──────────────────────────────────────────────────────

// newEnsembleTestReconciler builds an EnsembleReconciler over a fake client.
func newEnsembleTestReconciler(t *testing.T, objs ...client.Object) *EnsembleReconciler {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}
	// reconcileAgentConfig writes the memory-seed ConfigMap for personas that
	// declare seeds.
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}

	return &EnsembleReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(),
		Scheme: scheme,
	}
}

// convergenceFixture returns an Ensemble and persona that populate every source
// buildAgent reads.
//
// Completeness matters twice over: convergence is asserted across a fully-populated
// Agent rather than a mostly-empty one, and TestAgentSpecFieldsAreEnsembleExpressible
// treats "buildAgent left this zero" as "the Ensemble cannot express it" — which is
// only true if the fixture supplied every available source.
func convergenceFixture() (*sympoziumv1alpha1.Ensemble, *sympoziumv1alpha1.AgentConfigSpec) {
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			PolicyRef:       "baseline-policy",
			ProviderHeaders: map[string]string{"x-tenant": "a"},
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "openai", Secret: "openai-key"},
				{Provider: "anthropic", Secret: "anthropic-key"},
			},
			ChannelConfigs: map[string]string{"slack": "slack-config"},
			SkillParams:    map[string]map[string]string{"github-gitops": {"repo": "acme/infra"}},
			AgentSandbox:   &sympoziumv1alpha1.AgentSandboxDefaults{Enabled: false},
			Volumes: []corev1.Volume{{
				Name:         "shared-cache",
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			}},
			VolumeMounts: []corev1.VolumeMount{{Name: "shared-cache", MountPath: "/cache"}},
		},
	}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Name:                     "researcher",
		DisplayName:              "Researcher",
		SystemPrompt:             "you research things",
		Provider:                 "openai",
		Model:                    "gpt-4o",
		Skills:                   []string{"github-gitops", "mcp-bridge"},
		Channels:                 []string{"slack"},
		MCPServers:               []sympoziumv1alpha1.MCPServerRef{{Name: "files"}},
		Subagents:                &sympoziumv1alpha1.SubagentsSpec{MaxDepth: 3},
		Env:                      map[string]string{"MODE": "a"},
		RunTimeout:               "30m",
		BaseURL:                  "https://api.example.test/v1",
		ProviderHeadersSecretRef: "headers-secret",
		Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
			PreRun: []sympoziumv1alpha1.LifecycleHookContainer{
				{Name: "warm", Image: "busybox:1.36", Command: []string{"sh", "-c", "true"}},
			},
		},
		Workspace: &sympoziumv1alpha1.WorkspaceSpec{PerSessionPVC: true, Size: "2Gi"},
	}
	return pack, persona
}

func agentInstanceName(pack *sympoziumv1alpha1.Ensemble, persona *sympoziumv1alpha1.AgentConfigSpec) string {
	return pack.Name + "-" + persona.Name
}

func getAgent(t *testing.T, r *EnsembleReconciler, name, namespace string) *sympoziumv1alpha1.Agent {
	t.Helper()
	var got sympoziumv1alpha1.Agent
	if err := r.Get(context.Background(), client.ObjectKey{Name: name, Namespace: namespace}, &got); err != nil {
		t.Fatalf("get agent %s: %v", name, err)
	}
	return &got
}

// enumerateFieldPaths returns dotted Go field paths for a struct type, descending
// into named structs (Agents → Agents.Default → Agents.Default.Model) but stopping
// at pointers, slices, and maps — those are leaves for ownership purposes.
func enumerateFieldPaths(t reflect.Type, prefix string) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		path := f.Name
		if prefix != "" {
			path = prefix + "." + f.Name
		}
		if f.Type.Kind() == reflect.Struct && f.Type.PkgPath() != "" &&
			strings.Contains(f.Type.PkgPath(), "sympozium") {
			out = append(out, enumerateFieldPaths(f.Type, path)...)
			continue
		}
		out = append(out, path)
	}
	return out
}

// fieldByPath resolves a dotted field path produced by enumerateFieldPaths.
func fieldByPath(v reflect.Value, path string) (reflect.Value, bool) {
	cur := v
	for _, part := range strings.Split(path, ".") {
		if cur.Kind() != reflect.Struct {
			return reflect.Value{}, false
		}
		cur = cur.FieldByName(part)
		if !cur.IsValid() {
			return reflect.Value{}, false
		}
	}
	return cur, true
}

// ── ensemble expressibility of previously agent-only settings ─────────────────
//
// These pin the settings that were editable on a generated Agent but had no
// AgentConfigSpec equivalent, so an edit could only be made out of band and was
// then reverted by the whole-spec assign. internal/agentedit redirects such edits
// to the ensemble; that is only safe while every setting has a home here.

func TestBuildAgent_MemorySettingsComeFromAgentConfig(t *testing.T) {
	pack, persona := convergenceFixture()
	persona.Memory = &sympoziumv1alpha1.AgentConfigMemory{Enabled: false, MaxSizeKB: 1024}

	got := (&EnsembleReconciler{}).buildAgent(pack, persona, agentInstanceName(pack, persona), "")

	if got.Spec.Memory == nil {
		t.Fatal("buildAgent produced no memory spec")
	}
	if got.Spec.Memory.Enabled {
		t.Error("memory.enabled = true, want false — the agent config's setting was ignored")
	}
	if got.Spec.Memory.MaxSizeKB != 1024 {
		t.Errorf("memory.maxSizeKB = %d, want 1024", got.Spec.Memory.MaxSizeKB)
	}
}

// An ensemble written before these fields existed must render exactly as before.
func TestBuildAgent_MemoryDefaultsWithoutAgentConfigMemory(t *testing.T) {
	pack, persona := convergenceFixture()
	persona.Memory = nil

	got := (&EnsembleReconciler{}).buildAgent(pack, persona, agentInstanceName(pack, persona), "")

	if got.Spec.Memory == nil || !got.Spec.Memory.Enabled || got.Spec.Memory.MaxSizeKB != 256 {
		t.Errorf("memory = %+v, want enabled with maxSizeKB 256", got.Spec.Memory)
	}
}

func TestBuildDesiredSkills_AgentConfigParamsOverridePack(t *testing.T) {
	pack, persona := convergenceFixture()
	pack.Spec.SkillParams = map[string]map[string]string{
		"github-gitops": {"repo": "acme/infra", "branch": "main"},
	}
	persona.Skills = []string{"github-gitops"}
	persona.SkillParams = map[string]map[string]string{
		"github-gitops": {"repo": "acme/team-a"},
	}

	skills := buildDesiredSkills(pack, persona)

	params := skillParamsFor(t, skills, "github-gitops")
	if params["repo"] != "acme/team-a" {
		t.Errorf("repo = %q, want acme/team-a", params["repo"])
	}
	// Full override, not a merge: the pack's other key must not survive.
	if _, ok := params["branch"]; ok {
		t.Errorf("branch survived from the ensemble-level map; the agent config's params "+
			"replace it outright. got %v", params)
	}
}

func TestBuildDesiredSkills_PackParamsUsedWithoutOverride(t *testing.T) {
	pack, persona := convergenceFixture()
	pack.Spec.SkillParams = map[string]map[string]string{"github-gitops": {"repo": "acme/infra"}}
	persona.Skills = []string{"github-gitops"}
	persona.SkillParams = nil

	if got := skillParamsFor(t, buildDesiredSkills(pack, persona), "github-gitops")["repo"]; got != "acme/infra" {
		t.Errorf("repo = %q, want acme/infra", got)
	}
}

func TestBuildDesiredSkills_WebEndpointParamsAreDerived(t *testing.T) {
	pack, persona := convergenceFixture()
	persona.WebEndpoint = &sympoziumv1alpha1.AgentConfigWebEndpoint{
		Enabled:   true,
		Hostname:  "agent.example.test",
		RateLimit: &sympoziumv1alpha1.RateLimitSpec{RequestsPerMinute: 120, BurstSize: 20},
	}
	// A skillParams entry for web-endpoint must not clobber the derived params:
	// the skill is appended separately, outside the persona.Skills loop.
	persona.SkillParams = map[string]map[string]string{
		"web-endpoint": {"hostname": "attacker.example.test"},
	}

	params := skillParamsFor(t, buildDesiredSkills(pack, persona), "web-endpoint")
	if params["hostname"] != "agent.example.test" {
		t.Errorf("hostname = %q, want agent.example.test — derived params must win", params["hostname"])
	}
	if params["rate_limit_rpm"] != "120" {
		t.Errorf("rate_limit_rpm = %q, want 120", params["rate_limit_rpm"])
	}
	if params["rate_limit_burst"] != "20" {
		t.Errorf("rate_limit_burst = %q, want 20", params["rate_limit_burst"])
	}
}

func skillParamsFor(t *testing.T, skills []sympoziumv1alpha1.SkillRef, ref string) map[string]string {
	t.Helper()
	for _, s := range skills {
		if s.SkillPackRef == ref {
			return s.Params
		}
	}
	t.Fatalf("skill %q not in %v", ref, skills)
	return nil
}

// TestAgentEditRoundTripsToAgent closes the loop the redirect depends on: an edit
// routed to the Ensemble by internal/agentedit must survive reconcileAgentConfig
// and land on the generated Agent.
//
// agentedit.applyToAgentConfig and buildAgent/buildDesiredSkills are inverses of
// each other, maintained in different packages. This is the test that fails if one
// moves without the other.
func TestAgentEditRoundTripsToAgent(t *testing.T) {
	pack, persona := convergenceFixture()
	// convergenceFixture keeps the agent config separate; agentedit resolves it
	// through the ensemble, so it has to be present on the pack.
	pack.Spec.AgentConfigs = []sympoziumv1alpha1.AgentConfigSpec{*persona}
	// An ensemble-level default that disagrees with the edit below, so the
	// assertion proves the per-agent-config override wins rather than merely
	// proving some value arrived.
	autoStoreDefault := true
	pack.Spec.AutoStoreMemory = &autoStoreDefault
	instanceName := agentInstanceName(pack, persona)

	// The Agent as the create path would have stamped it, with the labels
	// agentedit routes on.
	agent := (&EnsembleReconciler{}).buildAgent(pack, persona, instanceName, "")
	r := newEnsembleTestReconciler(t, agent, pack)

	disabled := false
	maxKB := 1024
	noAutoStore := &disabled
	edit := agentedit.Edit{
		Memory: &agentedit.MemoryEdit{
			Enabled: &disabled, MaxSizeKB: &maxKB, AutoStore: &noAutoStore,
		},
		WebEndpoint: &agentedit.WebEndpointEdit{
			Enabled: true, Hostname: "analyst.example.test", RequestsPerMinute: 90,
		},
	}

	// Apply waits for the Agent to pick the edit up, which cannot happen here: this
	// test drives the reconcile itself, below. A short deadline caps that wait —
	// the Ensemble write happens before it, so the translation under test is
	// unaffected.
	applyCtx, cancelApply := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelApply()

	target, err := agentedit.Apply(applyCtx, r.Client, agent, edit)
	if err != nil {
		t.Fatalf("agentedit.Apply: %v", err)
	}
	if target.Kind != "Ensemble" {
		t.Fatalf("edit went to %s, want the Ensemble", target)
	}

	// Reconcile the (now edited) Ensemble and read the Agent back.
	var edited sympoziumv1alpha1.Ensemble
	if err := r.Get(context.Background(), client.ObjectKey{Name: pack.Name, Namespace: pack.Namespace}, &edited); err != nil {
		t.Fatalf("get ensemble: %v", err)
	}
	cfg := &edited.Spec.AgentConfigs[0]
	if _, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), &edited, cfg, 0, ""); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}

	got := getAgent(t, r, instanceName, pack.Namespace)

	if got.Spec.Memory == nil || got.Spec.Memory.Enabled {
		t.Errorf("memory.enabled = %+v, want disabled — the redirected edit did not reach the Agent",
			got.Spec.Memory)
	}
	if got.Spec.Memory != nil && got.Spec.Memory.MaxSizeKB != 1024 {
		t.Errorf("memory.maxSizeKB = %d, want 1024", got.Spec.Memory.MaxSizeKB)
	}
	// autoStore travels a longer road than the others: agentedit writes it to the
	// agent config, then resolveAutoStoreMemory has to prefer that over the
	// ensemble-level default for it to reach the Agent.
	if m := got.Spec.Memory; m != nil && (m.AutoStore == nil || *m.AutoStore) {
		actual := "unset — it inherited the ensemble default"
		if m.AutoStore != nil {
			actual = "true"
		}
		t.Errorf("memory.autoStore = %s, want an explicit false from the per-agent-config override", actual)
	}

	params := skillParamsFor(t, got.Spec.Skills, "web-endpoint")
	if params["hostname"] != "analyst.example.test" {
		t.Errorf("web-endpoint hostname = %q, want analyst.example.test", params["hostname"])
	}
	if params["rate_limit_rpm"] != "90" {
		t.Errorf("web-endpoint rate_limit_rpm = %q, want 90 — the rate limit was lost in translation",
			params["rate_limit_rpm"])
	}
}
