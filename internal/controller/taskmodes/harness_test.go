package taskmodes

import (
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// ── HarnessHandler registration and identity ─────────────────────────────────

func TestRegistry_HarnessRegistered(t *testing.T) {
	h, ok := Get(Harness)
	if !ok {
		t.Fatalf("registry missing built-in mode %q; SupportedModes=%v", Harness, SupportedModes())
	}
	if h.Mode() != Harness {
		t.Errorf("Mode() = %q, want %q", h.Mode(), Harness)
	}
}

// harnessTestImage is a digest-pinned harness reference for fixtures. Harness
// images must be digest-pinned — see HarnessHandler.Validate — so the default
// here carries a well-formed sha256 digest.
const harnessTestImage = "ghcr.io/acme/my-harness@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// harnessTask builds an object-form harness task. Every harness task names an
// image — Sympozium builds none — so the helper defaults one.
func harnessTask(params map[string]string) *sympoziumv1alpha1.TaskSpec {
	if params == nil {
		params = map[string]string{}
	}
	if _, ok := params[harnessParamPrompt]; !ok {
		params[harnessParamPrompt] = "summarise the incident"
	}
	if _, ok := params[harnessParamImage]; !ok {
		params[harnessParamImage] = harnessTestImage
	}
	return &sympoziumv1alpha1.TaskSpec{
		Mode:       Harness,
		Parameters: params,
	}
}

// ── Validate ────────────────────────────────────────────────────────────────

func TestHarnessHandler_Validate_Accepts(t *testing.T) {
	h := NewHarnessHandler()
	if err := h.Validate(harnessTask(nil)); err != nil {
		t.Errorf("Validate(image + prompt) returned error: %v", err)
	}
}

// The image is the whole mode: without one there is nothing to run in place of
// agent-runner, and Sympozium ships no default to fall back to.
func TestHarnessHandler_Validate_RequiresImage(t *testing.T) {
	h := NewHarnessHandler()
	err := h.Validate(&sympoziumv1alpha1.TaskSpec{
		Mode:       Harness,
		Parameters: map[string]string{harnessParamPrompt: "do it"},
	})
	if err == nil {
		t.Fatal("Validate(no image) returned nil; expected error")
	}
	if !strings.Contains(err.Error(), harnessParamImage) {
		t.Errorf("error should name the missing parameter, got: %v", err)
	}
}

func TestHarnessHandler_Validate_RequiresPrompt(t *testing.T) {
	h := NewHarnessHandler()
	err := h.Validate(&sympoziumv1alpha1.TaskSpec{
		Mode:       Harness,
		Parameters: map[string]string{harnessParamImage: harnessTestImage},
	})
	if err == nil {
		t.Fatal("Validate(no prompt) returned nil; expected error")
	}
	if !strings.Contains(err.Error(), harnessParamPrompt) {
		t.Errorf("error should name the missing parameter, got: %v", err)
	}
}

// The image becomes the pod's primary process and receives the run's model and
// MCP credentials, so a mutable tag is not an acceptable trust anchor. Every
// tag-only or otherwise unpinned reference must be rejected, not silently
// pinned to whatever the tag resolves to today.
func TestHarnessHandler_Validate_RejectsUnpinnedImage(t *testing.T) {
	h := NewHarnessHandler()
	for _, tc := range []struct {
		name  string
		image string
	}{
		{name: "tag-only", image: "ghcr.io/acme/my-harness:v1"},
		{name: "bare-name", image: "ghcr.io/acme/my-harness"},
		{name: "truncated-digest", image: "ghcr.io/acme/my-harness@sha256:deadbeef"},
		{name: "uppercase-hex", image: "ghcr.io/acme/my-harness@sha256:" + strings.Repeat("A", 64)},
		{name: "unknown-algorithm", image: "ghcr.io/acme/my-harness@md5:" + strings.Repeat("0", 32)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := h.Validate(harnessTask(map[string]string{harnessParamImage: tc.image}))
			if err == nil {
				t.Fatalf("Validate(%q) returned nil; expected digest-pinning rejection", tc.image)
			}
			if !strings.Contains(err.Error(), "digest-pinned") {
				t.Errorf("error should mention the digest-pinning requirement, got: %v", err)
			}
		})
	}
}

// A well-formed digest-pinned reference is accepted, including the legitimate
// name:tag@digest form where the digest remains authoritative.
func TestHarnessHandler_Validate_AcceptsDigestPinnedImage(t *testing.T) {
	h := NewHarnessHandler()
	for _, image := range []string{
		harnessTestImage,
		"ghcr.io/acme/my-harness:v1@sha256:" + strings.Repeat("b", 64),
		"registry.local/adapter@sha512:" + strings.Repeat("c", 128),
	} {
		if err := h.Validate(harnessTask(map[string]string{harnessParamImage: image})); err != nil {
			t.Errorf("Validate(%q) returned error: %v", image, err)
		}
	}
}

// A typo in a capability name must fail, not silently read as "unsupported" —
// the operator would then get a denial for something they meant to declare.
func TestHarnessHandler_Validate_RejectsUnknownCapability(t *testing.T) {
	h := NewHarnessHandler()
	err := h.Validate(harnessTask(map[string]string{
		harnessParamCapabilities: "persona,toolFiltr",
	}))
	if err == nil {
		t.Fatal("Validate(unknown capability) returned nil; expected error")
	}
	if !strings.Contains(err.Error(), "toolFiltr") {
		t.Errorf("error should name the offending value, got: %v", err)
	}
}

func TestHarnessHandler_Validate_RejectsUnmediatedCapabilities(t *testing.T) {
	h := NewHarnessHandler()
	for _, capability := range []string{CapabilityOutputSchema, CapabilitySubagents, CapabilityResume} {
		t.Run(capability, func(t *testing.T) {
			err := h.Validate(harnessTask(map[string]string{harnessParamCapabilities: capability}))
			if err == nil {
				t.Fatalf("Validate(%s) returned nil; expected unsupported-capability error", capability)
			}
			if !strings.Contains(err.Error(), capability) {
				t.Errorf("error should name %q, got: %v", capability, err)
			}
		})
	}
}

func TestHarnessHandler_Validate_RejectsMalformedArgs(t *testing.T) {
	h := NewHarnessHandler()
	err := h.Validate(harnessTask(map[string]string{
		harnessParamArgs: "--not-json",
	}))
	if err == nil {
		t.Fatal("Validate(malformed args) returned nil; expected error")
	}
	if !strings.Contains(err.Error(), harnessParamArgs) {
		t.Errorf("error should name the parameter, got: %v", err)
	}
}

func TestHarnessHandler_Validate_NilTask(t *testing.T) {
	h := NewHarnessHandler()
	if err := h.Validate(nil); err == nil {
		t.Error("Validate(nil) returned nil; expected error")
	}
}

// ── Capabilities ────────────────────────────────────────────────────────────

// Harness mode vouches for nothing on its own: every run names an image
// Sympozium did not build and cannot inspect.
func TestHarnessHandler_Capabilities_ClaimsNothing(t *testing.T) {
	h := NewHarnessHandler()
	if got := h.Capabilities(); (got != Capabilities{}) {
		t.Errorf("Capabilities() = %+v, want the zero value", got)
	}
}

func TestHarnessHandler_TaskCapabilities_ClaimsNothingByDefault(t *testing.T) {
	h := NewHarnessHandler()
	if got := h.TaskCapabilities(harnessTask(nil)); (got != Capabilities{}) {
		t.Errorf("TaskCapabilities(undeclared) = %+v, want the zero value", got)
	}
	if got := h.TaskCapabilities(nil); (got != Capabilities{}) {
		t.Errorf("TaskCapabilities(nil) = %+v, want the zero value", got)
	}
}

func TestHarnessHandler_TaskCapabilities_HonoursDeclaration(t *testing.T) {
	h := NewHarnessHandler()
	got := h.TaskCapabilities(harnessTask(map[string]string{
		harnessParamCapabilities: "persona, toolFilter",
	}))
	want := Capabilities{Persona: true, ToolFilter: true}
	if got != want {
		t.Errorf("TaskCapabilities(declared) = %+v, want %+v", got, want)
	}
}

// The mode descriptor claims nothing while a task may declare something, so
// CapabilitiesFor must route through the per-task reporter, not Capabilities().
func TestCapabilitiesFor_RoutesThroughTaskReporter(t *testing.T) {
	h := NewHarnessHandler()
	got := CapabilitiesFor(h, harnessTask(map[string]string{
		harnessParamCapabilities: "persona",
	}))
	if !got.Persona {
		t.Errorf("CapabilitiesFor = %+v, want the task's declaration, not the mode descriptor", got)
	}
}

func TestCapabilitiesFor_FallsBackToModeDescriptor(t *testing.T) {
	// SidecarDrivenHandler does not implement TaskCapabilityReporter.
	h := NewSidecarDrivenHandler()
	task := &sympoziumv1alpha1.TaskSpec{Mode: SidecarDriven, Tool: "primary"}
	if CapabilitiesFor(h, task) != h.Capabilities() {
		t.Error("CapabilitiesFor should return Capabilities() for a handler without TaskCapabilityReporter")
	}
}

// ── ConfigureAgentContainer / AdjustSidecars ────────────────────────────────

// Harness env is set through the override's SetEnv, not appended here — see
// the method comment. Appending would leave a second TASK behind and would
// let spec.env, which the central build appends later, shadow HOME.
func TestHarnessHandler_ConfigureAgentContainer_AppendsNothing(t *testing.T) {
	h := NewHarnessHandler()
	env := []corev1.EnvVar{{Name: "EXISTING", Value: "keep"}}
	if err := h.ConfigureAgentContainer(harnessTask(nil), &env); err != nil {
		t.Fatalf("ConfigureAgentContainer returned error: %v", err)
	}
	if len(env) != 1 || env[0].Name != "EXISTING" {
		t.Errorf("env = %v, want it untouched; harness env belongs in the override's SetEnv", env)
	}
	if err := h.ConfigureAgentContainer(nil, &env); err == nil {
		t.Error("ConfigureAgentContainer(nil) returned nil; expected error")
	}
}

func TestHarnessHandler_AdjustSidecars_LeavesSidecarsAlone(t *testing.T) {
	h := NewHarnessHandler()
	adjustments, err := h.AdjustSidecars(harnessTask(nil), []SidecarContext{
		{SkillPackName: "k8s-ops"},
	})
	if err != nil {
		t.Fatalf("AdjustSidecars returned error: %v", err)
	}
	if len(adjustments) != 0 {
		t.Errorf("AdjustSidecars = %v, want none — harness mode changes the primary process, not the sidecars", adjustments)
	}
}

// ── OverrideAgentContainer ──────────────────────────────────────────────────

func TestHarnessHandler_Override_UsesSuppliedImageAndItsOwnEntrypoint(t *testing.T) {
	h := NewHarnessHandler()
	override, err := h.OverrideAgentContainer(harnessTask(nil))
	if err != nil {
		t.Fatalf("OverrideAgentContainer returned error: %v", err)
	}
	if override.Image != harnessTestImage {
		t.Errorf("Image = %q, want the operator's reference", override.Image)
	}
	// Sympozium has no argv to impose on a binary it did not build.
	if len(override.Command) != 0 {
		t.Errorf("Command = %v, want empty so the image's own ENTRYPOINT runs", override.Command)
	}
	if override.WorkingDir != "/workspace" {
		t.Errorf("WorkingDir = %q, want /workspace regardless of image WORKDIR", override.WorkingDir)
	}
}

func TestHarnessHandler_Override_SetEnvCarriesEveryHarnessValue(t *testing.T) {
	h := NewHarnessHandler()
	override, err := h.OverrideAgentContainer(harnessTask(nil))
	if err != nil {
		t.Fatalf("OverrideAgentContainer returned error: %v", err)
	}

	want := map[string]string{
		"TASK":                               "summarise the incident",
		"HOME":                               harnessHomePath,
		"XDG_CONFIG_HOME":                    harnessHomePath + "/.config",
		"XDG_CACHE_HOME":                     harnessHomePath + "/.cache",
		"SYMPOZIUM_RESULT_PATH":              "/ipc/output/result.json",
		"SYMPOZIUM_HARNESS_CONTRACT_VERSION": HarnessContractVersion,
	}
	for name, value := range want {
		if got, ok := envValue(override.SetEnv, name); !ok || got != value {
			t.Errorf("SetEnv %s = %q (present=%v), want %q", name, got, ok, value)
		}
	}
}

// The writable HOME is an emptyDir, never a relaxed security context — the pod
// keeps readOnlyRootFilesystem like every other Sympozium agent pod.
func TestHarnessHandler_Override_WritableHomeIsAnEmptyDir(t *testing.T) {
	h := NewHarnessHandler()
	override, err := h.OverrideAgentContainer(harnessTask(nil))
	if err != nil {
		t.Fatalf("OverrideAgentContainer returned error: %v", err)
	}
	if len(override.Volumes) != 1 || override.Volumes[0].Name != harnessHomeVolume {
		t.Fatalf("Volumes = %v, want a single %q volume", override.Volumes, harnessHomeVolume)
	}
	if override.Volumes[0].EmptyDir == nil {
		t.Error("the writable HOME must be an emptyDir, not a relaxed security context")
	}
	var home *corev1.VolumeMount
	for i := range override.VolumeMounts {
		if override.VolumeMounts[i].Name == harnessHomeVolume {
			home = &override.VolumeMounts[i]
		}
	}
	if home == nil || home.MountPath != harnessHomePath {
		t.Errorf("VolumeMounts = %v, want %q at %q", override.VolumeMounts, harnessHomeVolume, harnessHomePath)
	}
}

// The harness gets /ipc/input and /ipc/output and nothing else. The other six
// directories the bridge watches — spawn, tools, messages, schedules, prompts,
// context — each turn a dropped JSON file into a control-plane action, so a
// harness holding the volume root could spawn children or message a channel
// whatever its capability descriptor claims. Pinned because the failure is
// silent: the pod still runs, and nothing reports the extra reach.
func TestHarnessHandler_Override_NarrowsIPCToInputAndOutput(t *testing.T) {
	h := NewHarnessHandler()
	override, err := h.OverrideAgentContainer(harnessTask(nil))
	if err != nil {
		t.Fatalf("OverrideAgentContainer returned error: %v", err)
	}

	var ipc []corev1.VolumeMount
	for _, m := range override.VolumeMounts {
		if m.Name == harnessIPCVolume {
			ipc = append(ipc, m)
		}
	}
	if len(ipc) != 2 {
		t.Fatalf("ipc mounts = %v, want exactly two (input, output)", ipc)
	}

	bySubPath := map[string]corev1.VolumeMount{}
	for _, m := range ipc {
		if m.SubPath == "" {
			t.Fatalf("ipc mount %+v has no subPath; that is the whole volume, including spawn/ and tools/", m)
		}
		bySubPath[m.SubPath] = m
	}

	in, ok := bySubPath[harnessIPCInputDir]
	if !ok {
		t.Fatalf("no %q subPath mount; the harness cannot read its task", harnessIPCInputDir)
	}
	if in.MountPath != harnessIPCInputPath {
		t.Errorf("input mounted at %q, want %q", in.MountPath, harnessIPCInputPath)
	}
	if !in.ReadOnly {
		t.Error("the task input is read-only for the harness; it is an input, not a channel")
	}

	out, ok := bySubPath[harnessIPCOutputDir]
	if !ok {
		t.Fatalf("no %q subPath mount; the harness cannot return its result", harnessIPCOutputDir)
	}
	if out.MountPath != harnessIPCOutPath {
		t.Errorf("output mounted at %q, want %q", out.MountPath, harnessIPCOutPath)
	}
	if out.ReadOnly {
		t.Error("the result path must be writable; it is how the run reports back")
	}
}

func TestHarnessHandler_Override_PassesThroughArgs(t *testing.T) {
	h := NewHarnessHandler()
	override, err := h.OverrideAgentContainer(harnessTask(map[string]string{
		harnessParamArgs: `["--profile","headless","--max-steps","20"]`,
	}))
	if err != nil {
		t.Fatalf("OverrideAgentContainer returned error: %v", err)
	}
	want := []string{"--profile", "headless", "--max-steps", "20"}
	if len(override.Args) != len(want) {
		t.Fatalf("Args = %v, want %v", override.Args, want)
	}
	for i := range want {
		if override.Args[i] != want[i] {
			t.Errorf("Args[%d] = %q, want %q", i, override.Args[i], want[i])
		}
	}
}

func TestValidateRunCompatibility_RejectsAgentRunnerOnlyControls(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*sympoziumv1alpha1.AgentRun)
		want   string
	}{
		{name: "server", mutate: func(r *sympoziumv1alpha1.AgentRun) { r.Spec.Mode = "server" }, want: "mode=server"},
		{name: "dry-run", mutate: func(r *sympoziumv1alpha1.AgentRun) { r.Spec.DryRun = true }, want: "dryRun"},
		{name: "canary", mutate: func(r *sympoziumv1alpha1.AgentRun) { r.Spec.CanaryMode = true }, want: "canaryMode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &sympoziumv1alpha1.AgentRun{Spec: sympoziumv1alpha1.AgentRunSpec{Task: harnessTask(nil)}}
			tc.mutate(run)
			err := ValidateRunCompatibility(run)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateRunCompatibility() error = %v, want error naming %q", err, tc.want)
			}
		})
	}
}

func TestValidateRunCompatibility_AllowsOrdinaryHarnessRun(t *testing.T) {
	run := &sympoziumv1alpha1.AgentRun{Spec: sympoziumv1alpha1.AgentRunSpec{Task: harnessTask(nil)}}
	if err := ValidateRunCompatibility(run); err != nil {
		t.Fatalf("ordinary harness run rejected: %v", err)
	}
}

func TestOverrideFor_NilForStringAndOtherModes(t *testing.T) {
	stringTask := sympoziumv1alpha1.NewStringTask("do the thing")
	if override, err := OverrideFor(stringTask); err != nil || override != nil {
		t.Errorf("OverrideFor(string form) = (%v, %v), want (nil, nil)", override, err)
	}

	sidecarTask := &sympoziumv1alpha1.TaskSpec{Mode: SidecarDriven, Tool: "primary"}
	if override, err := OverrideFor(sidecarTask); err != nil || override != nil {
		t.Errorf("OverrideFor(sidecar-driven) = (%v, %v), want (nil, nil) — that mode keeps agent-runner", override, err)
	}
}

// The predicate both binaries share. A malformed task answers false rather
// than erroring: Validate reports that failure with a usable message.
func TestReplacesAgentContainer(t *testing.T) {
	if !ReplacesAgentContainer(harnessTask(nil)) {
		t.Error("ReplacesAgentContainer(harness) = false, want true")
	}
	if ReplacesAgentContainer(&sympoziumv1alpha1.TaskSpec{Mode: SidecarDriven, Tool: "primary"}) {
		t.Error("ReplacesAgentContainer(sidecar-driven) = true; that mode keeps agent-runner")
	}
	if ReplacesAgentContainer(sympoziumv1alpha1.NewStringTask("do it")) {
		t.Error("ReplacesAgentContainer(string form) = true, want false")
	}
	if ReplacesAgentContainer(nil) {
		t.Error("ReplacesAgentContainer(nil) = true, want false")
	}
	// No image means OverrideAgentContainer errors; the predicate must not
	// panic or claim a replacement.
	if ReplacesAgentContainer(&sympoziumv1alpha1.TaskSpec{Mode: Harness}) {
		t.Error("ReplacesAgentContainer(harness with no image) = true, want false")
	}
}

// ── HarnessImage (admission's image-policy hook) ────────────────────────────

func TestHarnessImage(t *testing.T) {
	if got := HarnessImage(harnessTask(nil)); got != harnessTestImage {
		t.Errorf("HarnessImage = %q, want the operator's reference for imagePolicy to check", got)
	}
	if got := HarnessImage(&sympoziumv1alpha1.TaskSpec{Mode: SidecarDriven, Tool: "primary"}); got != "" {
		t.Errorf("HarnessImage(other mode) = %q, want empty", got)
	}
	if got := HarnessImage(sympoziumv1alpha1.NewStringTask("do the thing")); got != "" {
		t.Errorf("HarnessImage(string form) = %q, want empty", got)
	}
	if got := HarnessImage(nil); got != "" {
		t.Errorf("HarnessImage(nil) = %q, want empty", got)
	}
}

func TestHarnessImageDigest(t *testing.T) {
	if got := HarnessImageDigest(harnessTask(nil)); got != "sha256:"+strings.Repeat("a", 64) {
		t.Errorf("HarnessImageDigest = %q, want the pinned sha256 digest", got)
	}
	if got := HarnessImageDigest(&sympoziumv1alpha1.TaskSpec{Mode: SidecarDriven, Tool: "primary"}); got != "" {
		t.Errorf("HarnessImageDigest(other mode) = %q, want empty", got)
	}
	if got := HarnessImageDigest(sympoziumv1alpha1.NewStringTask("do it")); got != "" {
		t.Errorf("HarnessImageDigest(string form) = %q, want empty", got)
	}
	if got := HarnessImageDigest(nil); got != "" {
		t.Errorf("HarnessImageDigest(nil) = %q, want empty", got)
	}
	// A tag-only reference has no digest to record.
	unpinned := harnessTask(map[string]string{harnessParamImage: "ghcr.io/acme/my-harness:v1"})
	if got := HarnessImageDigest(unpinned); got != "" {
		t.Errorf("HarnessImageDigest(tag-only) = %q, want empty", got)
	}
}

// ── Capability gating against an AgentRun ───────────────────────────────────

func harnessRun(task *sympoziumv1alpha1.TaskSpec) *sympoziumv1alpha1.AgentRun {
	return &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "test-run", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "my-agent",
			AgentID:  "default",
			Task:     task,
		},
	}
}

func TestValidateCapabilities_RejectsUndeclaredRequest(t *testing.T) {
	run := harnessRun(harnessTask(nil))
	run.Spec.SystemPrompt = "You are a careful SRE."

	err := ValidateCapabilities(run)
	if err == nil {
		t.Fatal("ValidateCapabilities returned nil; an undeclared image claims nothing")
	}
	if !strings.Contains(err.Error(), CapabilityPersona) {
		t.Errorf("error should name the capability, got: %v", err)
	}
	if !strings.Contains(err.Error(), Harness) {
		t.Errorf("error should name the mode, got: %v", err)
	}
}

func TestValidateCapabilities_AllowsDeclaredRequest(t *testing.T) {
	run := harnessRun(harnessTask(map[string]string{
		harnessParamCapabilities: "persona",
	}))
	run.Spec.SystemPrompt = "You are a careful SRE."

	if err := ValidateCapabilities(run); err != nil {
		t.Errorf("ValidateCapabilities returned error for a declared capability: %v", err)
	}
}

// Declaring persona does not buy toolFilter: each capability is checked on its
// own, so a partial declaration still catches the rest.
func TestValidateCapabilities_PartialDeclarationStillRejectsTheRest(t *testing.T) {
	run := harnessRun(harnessTask(map[string]string{
		harnessParamCapabilities: "persona",
	}))
	run.Spec.SystemPrompt = "You are a careful SRE."
	run.Spec.ToolPolicy = &sympoziumv1alpha1.ToolPolicySpec{Deny: []string{"execute_command"}}

	err := ValidateCapabilities(run)
	if err == nil {
		t.Fatal("ValidateCapabilities returned nil; toolFilter was never declared")
	}
	if !strings.Contains(err.Error(), CapabilityToolFilter) {
		t.Errorf("error should name the undeclared capability, got: %v", err)
	}
}

// The retroactive case the descriptor exists for: prompt-server mode runs the
// LLM with no tools at all, so a toolPolicy there was always a no-op.
func TestValidateCapabilities_RejectsToolPolicyOnSidecarDriven(t *testing.T) {
	run := harnessRun(&sympoziumv1alpha1.TaskSpec{Mode: SidecarDriven, Tool: "primary"})
	run.Spec.ToolPolicy = &sympoziumv1alpha1.ToolPolicySpec{Allow: []string{"read_file"}}

	err := ValidateCapabilities(run)
	if err == nil {
		t.Fatal("ValidateCapabilities returned nil; sidecar-driven has no tool surface to filter")
	}
	if !strings.Contains(err.Error(), SidecarDriven) {
		t.Errorf("error should name the mode, got: %v", err)
	}
}

func TestValidateCapabilities_StringFormAndUnknownModePass(t *testing.T) {
	if err := ValidateCapabilities(harnessRun(sympoziumv1alpha1.NewStringTask("do it"))); err != nil {
		t.Errorf("string-form task should not be capability-checked: %v", err)
	}
	if err := ValidateCapabilities(harnessRun(nil)); err != nil {
		t.Errorf("nil task should not be capability-checked: %v", err)
	}
	// An unregistered mode is the controller's error to raise, with the
	// supported-mode list. Denying it here would break the documented
	// downstream-registration path.
	unknown := harnessRun(&sympoziumv1alpha1.TaskSpec{Mode: "acme-batch-runner"})
	if err := ValidateCapabilities(unknown); err != nil {
		t.Errorf("unregistered mode should pass the capability check: %v", err)
	}
}

func TestRequestedCapabilities(t *testing.T) {
	run := harnessRun(harnessTask(nil))
	if got := RequestedCapabilities(run); (got != Capabilities{}) {
		t.Errorf("RequestedCapabilities(bare run) = %+v, want the zero value", got)
	}

	run.Spec.SystemPrompt = "be careful"
	run.Spec.ToolPolicy = &sympoziumv1alpha1.ToolPolicySpec{Allow: []string{"read_file", "spawn_subagents"}}
	want := Capabilities{Persona: true, ToolFilter: true, Subagents: true}
	if got := RequestedCapabilities(run); got != want {
		t.Errorf("RequestedCapabilities = %+v, want %+v", got, want)
	}

	// An empty toolPolicy states no intent, so it requests nothing.
	run.Spec.SystemPrompt = ""
	run.Spec.ToolPolicy = &sympoziumv1alpha1.ToolPolicySpec{}
	if got := RequestedCapabilities(run); (got != Capabilities{}) {
		t.Errorf("RequestedCapabilities(empty toolPolicy) = %+v, want the zero value", got)
	}
}

// ── Capabilities helpers ────────────────────────────────────────────────────

func TestCapabilities_MissingAndNames(t *testing.T) {
	have := Capabilities{Persona: true}
	want := Capabilities{Persona: true, ToolFilter: true, Subagents: true}

	missing := have.Missing(want)
	if len(missing) != 2 || missing[0] != CapabilitySubagents || missing[1] != CapabilityToolFilter {
		t.Errorf("Missing = %v, want sorted [%s %s]", missing, CapabilitySubagents, CapabilityToolFilter)
	}
	if len(have.Missing(Capabilities{Persona: true})) != 0 {
		t.Error("Missing should be empty when the descriptor satisfies the request")
	}
	if !sort.StringsAreSorted(KnownCapabilities()) {
		t.Errorf("KnownCapabilities not sorted: %v", KnownCapabilities())
	}
}

func TestParseCapabilities(t *testing.T) {
	caps, err := ParseCapabilities([]string{"persona", "resume"})
	if err != nil {
		t.Fatalf("ParseCapabilities returned error: %v", err)
	}
	if !caps.Persona || !caps.Resume || caps.ToolFilter {
		t.Errorf("ParseCapabilities = %+v, want persona + resume only", caps)
	}
	if _, err := ParseCapabilities([]string{"nope"}); err == nil {
		t.Error("ParseCapabilities(unknown) returned nil; a typo must not read as unsupported")
	}
}

// envValue returns (value, true) when envs has an entry with the given name.
func envValue(envs []corev1.EnvVar, name string) (string, bool) {
	for _, e := range envs {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}
