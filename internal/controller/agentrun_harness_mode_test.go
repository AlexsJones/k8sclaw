package controller

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller/taskmodes"
	"github.com/sympozium-ai/sympozium/internal/skilltools"
)

// ── mode: harness container-build tests ──────────────────────────────────────
//
// These pin the controller half of harness mode: the agent container becomes
// the harness image, the credentials stay per-key SecretKeyRef injections, the
// pod security context is untouched, and the result contract is unchanged so
// gates and cost estimation keep working on a run they never drove.

// harnessModeRun returns an AgentRun whose task runs a harness image. Every
// harness task names one — Sympozium builds no harness images — so the helper
// defaults it.
func harnessModeRun(params map[string]string) *sympoziumv1alpha1.AgentRun {
	if params == nil {
		params = map[string]string{}
	}
	if _, ok := params["prompt"]; !ok {
		params["prompt"] = "summarise the incident"
	}
	if _, ok := params["image"]; !ok {
		params["image"] = harnessTestImage
	}
	r := newTestRun()
	r.Spec.Task = &sympoziumv1alpha1.TaskSpec{
		Mode:       taskmodes.Harness,
		Parameters: params,
	}
	return r
}

// harnessTestImage stands in for any operator-supplied adapter image. It is
// digest-pinned because harness images must be: the digest is the trust anchor
// that admission and the controller record.
const harnessTestImage = "ghcr.io/acme/my-harness@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func containerByName(cs []corev1.Container, name string) *corev1.Container {
	for i := range cs {
		if cs[i].Name == name {
			return &cs[i]
		}
	}
	return nil
}

func hasVolume(vs []corev1.Volume, name string) bool {
	for _, v := range vs {
		if v.Name == name {
			return true
		}
	}
	return false
}

func hasMount(ms []corev1.VolumeMount, name string) bool {
	for _, m := range ms {
		if m.Name == name {
			return true
		}
	}
	return false
}

// The agent container becomes the operator's adapter image, running its own
// ENTRYPOINT. Sympozium's own registry and tag are not applied — the image is
// a full reference admission has already checked against allowedRegistries.
func TestBuildContainers_HarnessReplacesAgentImage(t *testing.T) {
	r := &AgentRunReconciler{ImageTag: "v0.1.2"}
	cs, _, err := r.buildContainers(harnessModeRun(nil), false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}

	agent := containerByName(cs, "agent")
	if agent == nil {
		t.Fatal("agent container not found")
	}
	if agent.Image != harnessTestImage {
		t.Errorf("agent image = %q, want the operator's reference %q", agent.Image, harnessTestImage)
	}
	if len(agent.Command) != 0 {
		t.Errorf("agent command = %v, want empty so the image's own ENTRYPOINT runs", agent.Command)
	}
}

// The task text reaches the harness on TASK. The central container build sets
// TASK empty for every object-form task, so this asserts the override
// replaced it rather than leaving two contradictory entries behind.
func TestBuildContainers_HarnessSetsTaskEnvExactlyOnce(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, err := r.buildContainers(harnessModeRun(nil), false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(cs, "agent")

	var count int
	for _, e := range agent.Env {
		if e.Name == "TASK" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("TASK appears %d times on the agent container, want exactly 1", count)
	}
	if v, ok := envValueLocal(agent.Env, "TASK"); !ok || v != "summarise the incident" {
		t.Errorf("TASK = (%q, %v), want the task text", v, ok)
	}
}

// A harness that assumes a writable HOME gets an emptyDir, not a relaxed
// security context.
func TestBuildContainers_HarnessGetsWritableHomeWithoutRelaxingSecurity(t *testing.T) {
	r := &AgentRunReconciler{}
	run := harnessModeRun(nil)

	cs, _, err := r.buildContainers(run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(cs, "agent")

	if !hasMount(agent.VolumeMounts, "harness-home") {
		t.Errorf("agent volumeMounts = %v, want a harness-home mount", agent.VolumeMounts)
	}
	if v, ok := envValueLocal(agent.Env, "HOME"); !ok || v != "/home/agent" {
		t.Errorf("HOME = (%q, %v), want /home/agent", v, ok)
	}

	sc := agent.SecurityContext
	if sc == nil || sc.ReadOnlyRootFilesystem == nil || !*sc.ReadOnlyRootFilesystem {
		t.Error("readOnlyRootFilesystem must stay true for a harness container")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("allowPrivilegeEscalation must stay false for a harness container")
	}
	if sc.Capabilities == nil || len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("capabilities = %v, want drop: [ALL]", sc.Capabilities)
	}

	volumes := r.buildVolumes(run, false, nil, nil)
	if !hasVolume(volumes, "harness-home") {
		t.Errorf("pod volumes = %v, want a harness-home emptyDir", volumes)
	}
	for _, v := range volumes {
		if v.Name == "harness-home" && v.EmptyDir == nil {
			t.Error("harness-home must be an emptyDir")
		}
	}
}

// spec.env is appended after the per-mode env, so the harness's own values
// have to be set through the replacing override — otherwise a user-set HOME
// points the harness at a path it cannot write.
func TestBuildContainers_HarnessEnvSurvivesSpecEnv(t *testing.T) {
	r := &AgentRunReconciler{}
	run := harnessModeRun(nil)
	run.Spec.Env = map[string]string{
		"HOME":                  "/nonexistent",
		"SYMPOZIUM_RESULT_PATH": "/tmp/somewhere-nobody-watches.json",
		"TASK":                  "a different task",
	}

	cs, _, err := r.buildContainers(run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(cs, "agent")

	want := map[string]string{
		"HOME":                  "/home/agent",
		"SYMPOZIUM_RESULT_PATH": "/ipc/output/result.json",
		"TASK":                  "summarise the incident",
	}
	for name, value := range want {
		if got, ok := envValueLocal(agent.Env, name); !ok || got != value {
			t.Errorf("%s = %q (present=%v), want %q — spec.env must not shadow harness env", name, got, ok, value)
		}
		var count int
		for _, e := range agent.Env {
			if e.Name == name {
				count++
			}
		}
		if count != 1 {
			t.Errorf("%s appears %d times, want exactly 1", name, count)
		}
	}
}

// Credentials keep the per-key SecretKeyRef shape from the
// allowedAuthSecretKeys allowlist — never EnvFrom, never a widened allowlist.
func TestBuildContainers_HarnessCredentialsStayPerKeySecretRefs(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, err := r.buildContainers(harnessModeRun(nil), false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(cs, "agent")

	if len(agent.EnvFrom) != 0 {
		t.Errorf("agent EnvFrom = %v, want none — whole secrets are never mounted into an agent pod", agent.EnvFrom)
	}

	var found bool
	for _, e := range agent.Env {
		if e.Name != "DEEPSEEK_API_KEY" {
			continue
		}
		found = true
		if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("DEEPSEEK_API_KEY = %+v, want a SecretKeyRef", e)
		}
		if e.ValueFrom.SecretKeyRef.Name != "my-secret" || e.ValueFrom.SecretKeyRef.Key != "DEEPSEEK_API_KEY" {
			t.Errorf("SecretKeyRef = %+v, want my-secret/DEEPSEEK_API_KEY", e.ValueFrom.SecretKeyRef)
		}
	}
	if !found {
		t.Error("DEEPSEEK_API_KEY not injected; the harness cannot authenticate")
	}
}

// The MCP server registry the controller already generates is mounted for the
// harness, which speaks MCP itself rather than going through the bridge's
// discovered manifest.
func TestBuildContainers_HarnessMountsMCPRegistry(t *testing.T) {
	r := &AgentRunReconciler{}
	mcpServers := []sympoziumv1alpha1.MCPServerRef{{Name: "github", URL: "http://mcp-github:8080"}}

	cs, _, err := r.buildContainers(harnessModeRun(nil), false, nil, nil, mcpServers, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(cs, "agent")

	if !hasMount(agent.VolumeMounts, "mcp-config") {
		t.Errorf("agent volumeMounts = %v, want the mcp-config mount", agent.VolumeMounts)
	}
	// JSON, not the YAML the bridge reads: the harness adapter is a shell
	// script with jq and no YAML parser.
	if v, ok := envValueLocal(agent.Env, "MCP_CONFIG_PATH"); !ok || v != "/config/mcp/mcp-servers.json" {
		t.Errorf("MCP_CONFIG_PATH = (%q, %v), want /config/mcp/mcp-servers.json", v, ok)
	}
}

// The registry names an auth env var per server; without the var itself on
// the harness container, the harness connects unauthenticated and fails at
// the first tool call. The bridge sidecar got these already — the replaced
// agent container has to get them too, because it is the MCP client now.
func TestBuildContainers_HarnessGetsMCPAuthTokens(t *testing.T) {
	r := &AgentRunReconciler{}
	mcpServers := []sympoziumv1alpha1.MCPServerRef{{
		Name:       "github-mcp",
		URL:        "http://mcp-github:8080",
		AuthSecret: "gh-token",
		AuthKey:    "token",
	}}

	cs, _, err := r.buildContainers(harnessModeRun(nil), false, nil, nil, mcpServers, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(cs, "agent")

	var found bool
	for _, e := range agent.Env {
		if e.Name != "MCP_AUTH_GITHUB_MCP" {
			continue
		}
		found = true
		if e.ValueFrom == nil || e.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("MCP_AUTH_GITHUB_MCP = %+v, want a SecretKeyRef", e)
		}
		if e.ValueFrom.SecretKeyRef.Name != "gh-token" || e.ValueFrom.SecretKeyRef.Key != "token" {
			t.Errorf("SecretKeyRef = %+v, want gh-token/token", e.ValueFrom.SecretKeyRef)
		}
	}
	if !found {
		t.Error("MCP_AUTH_GITHUB_MCP not injected; the harness cannot authenticate to the MCP server")
	}
}

// The name in the registry and the name of the env var carrying the token are
// derived independently, in two files. They have to agree or the harness
// looks up a variable that does not exist.
func TestMCPRegistryJSONMatchesInjectedAuthEnvNames(t *testing.T) {
	mcpServers := []sympoziumv1alpha1.MCPServerRef{{
		Name:       "github-mcp",
		URL:        "http://mcp-github:8080",
		AuthSecret: "gh-token",
	}}

	raw, err := buildMCPServersJSON(mcpServers, false)
	if err != nil {
		t.Fatalf("buildMCPServersJSON: %v", err)
	}
	var cfg struct {
		Servers []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
			Auth *struct {
				Type      string `json:"type"`
				SecretKey string `json:"secretKey"`
			} `json:"auth"`
		} `json:"servers"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("the harness reads this with jq; it must be valid JSON: %v", err)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0].Auth == nil {
		t.Fatalf("registry = %s, want one server carrying auth", raw)
	}
	if cfg.Servers[0].Auth.SecretKey != "MCP_AUTH_GITHUB_MCP" {
		t.Errorf("auth.secretKey = %q, want the env var name the controller injects",
			cfg.Servers[0].Auth.SecretKey)
	}

	r := &AgentRunReconciler{}
	cs, _, err := r.buildContainers(harnessModeRun(nil), false, nil, nil, mcpServers, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	// A SecretKeyRef env var carries no literal value, so this checks the
	// name is present rather than reading it back.
	var present bool
	for _, e := range containerByName(cs, "agent").Env {
		if e.Name == cfg.Servers[0].Auth.SecretKey {
			present = true
			break
		}
	}
	if !present {
		t.Errorf("registry names %q but no such env var is on the agent container",
			cfg.Servers[0].Auth.SecretKey)
	}
}

// Without MCP servers there is no mcp-config volume, so the mount must not
// appear either — a mount with no volume fails the pod outright.
func TestBuildContainers_HarnessSkipsMCPMountWithoutServers(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, err := r.buildContainers(harnessModeRun(nil), false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	if hasMount(containerByName(cs, "agent").VolumeMounts, "mcp-config") {
		t.Error("mcp-config mounted with no MCP servers configured; the volume does not exist")
	}
}

// parameters.args reaches the container as argv, which is how an image whose
// ENTRYPOINT takes flags is driven without Sympozium knowing any of them.
func TestBuildContainers_HarnessPassesArgsThrough(t *testing.T) {
	r := &AgentRunReconciler{ImageTag: "v0.1.2"}
	run := harnessModeRun(map[string]string{
		"args": `["--profile","headless"]`,
	})

	cs, _, err := r.buildContainers(run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(cs, "agent")

	want := []string{"--profile", "headless"}
	if len(agent.Args) != len(want) {
		t.Fatalf("agent args = %v, want %v", agent.Args, want)
	}
	for i := range want {
		if agent.Args[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q", i, agent.Args[i], want[i])
		}
	}
}

// The ipc-bridge stays on the pod: the harness hands its answer back over the
// same /ipc/output/result.json the bridge already watches.
func TestBuildContainers_HarnessKeepsIPCBridge(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, err := r.buildContainers(harnessModeRun(nil), false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	bridge := containerByName(cs, "ipc-bridge")
	if bridge == nil {
		t.Fatal("ipc-bridge container missing; the result contract would never reach the control plane")
	}
	if !hasMount(containerByName(cs, "agent").VolumeMounts, "ipc") {
		t.Error("agent container lost its /ipc mount")
	}
}

// Validation failures surface as an error so the reconcile loop can mark the
// run Failed, rather than rendering a pod that runs the wrong binary.
func TestBuildContainers_HarnessValidationFailurePropagates(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Task = &sympoziumv1alpha1.TaskSpec{
		Mode: taskmodes.Harness,
		// No image, no prompt.
		Parameters: map[string]string{},
	}

	if _, _, err := r.buildContainers(run, false, nil, nil, nil, nil); err == nil {
		t.Fatal("buildContainers returned nil error for an invalid harness task")
	}
}

// A capability the image never declared fails the run in the controller too,
// not only at admission — the webhook is a separate, optional deployment.
func TestResolveTaskModeAdjustments_HarnessCapabilityMismatchRejected(t *testing.T) {
	run := harnessModeRun(nil)
	run.Spec.ToolPolicy = &sympoziumv1alpha1.ToolPolicySpec{Deny: []string{"execute_command"}}

	_, err := resolveTaskModeAdjustments(run, nil)
	if err == nil {
		t.Fatal("resolveTaskModeAdjustments returned nil; codex cannot filter tools")
	}
	if !strings.Contains(err.Error(), taskmodes.CapabilityToolFilter) {
		t.Errorf("error should name the capability, got: %v", err)
	}
}

// Harness mode leaves the SkillPack sidecars alone.
func TestResolveTaskModeAdjustments_HarnessMakesNoSidecarAdjustments(t *testing.T) {
	sidecars := []resolvedSidecar{{skillPackName: "k8s-ops"}}

	adjustments, err := resolveTaskModeAdjustments(harnessModeRun(nil), sidecars)
	if err != nil {
		t.Fatalf("resolveTaskModeAdjustments: %v", err)
	}
	if len(adjustments) != 0 {
		t.Errorf("adjustments = %v, want none", adjustments)
	}
}

// Path A and sidecar-driven keep agent-runner — the override is opt-in.
func TestBuildContainers_NonHarnessModesKeepAgentRunner(t *testing.T) {
	r := &AgentRunReconciler{ImageTag: "v0.1.2"}

	cs, _, err := r.buildContainers(stringModeRun("do the thing"), false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers(string form): %v", err)
	}
	if want := "ghcr.io/sympozium-ai/sympozium/agent-runner:v0.1.2"; cs[0].Image != want {
		t.Errorf("string-form agent image = %q, want %q", cs[0].Image, want)
	}
	if hasMount(cs[0].VolumeMounts, "harness-home") {
		t.Error("string-form run should not get the harness HOME volume")
	}
}

// ── /ipc is not a shared surface for a harness ──────────────────────────────

// The central build gives the agent container {ipc -> /ipc}. For a harness that
// is six more channels than it needs: the bridge turns a file dropped in
// spawn/, tools/, messages/ or schedules/ into a control-plane action. This
// asserts the composed container, not just the override — the replacement only
// works if applyAgentContainerOverride drops the central mount first.
func TestBuildContainers_HarnessCannotReachTheOtherIPCDirectories(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, err := r.buildContainers(harnessModeRun(nil), false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(cs, "agent")

	for _, m := range agent.VolumeMounts {
		if m.Name != "ipc" {
			continue
		}
		if m.SubPath == "" {
			t.Fatalf("agent mounts the whole ipc volume at %q; spawn/, tools/, messages/ and schedules/ are all writable", m.MountPath)
		}
		if m.SubPath != "input" && m.SubPath != "output" {
			t.Errorf("unexpected ipc subPath %q mounted at %q", m.SubPath, m.MountPath)
		}
	}

	// And the result path the adapter is told to use must actually exist in
	// that namespace, or every harness run fails on its last line.
	resultPath, ok := envValueLocal(agent.Env, "SYMPOZIUM_RESULT_PATH")
	if !ok {
		t.Fatal("SYMPOZIUM_RESULT_PATH not set")
	}
	var covered bool
	for _, m := range agent.VolumeMounts {
		if m.Name == "ipc" && m.SubPath == "output" && strings.HasPrefix(resultPath, m.MountPath+"/") {
			covered = true
		}
	}
	if !covered {
		t.Errorf("SYMPOZIUM_RESULT_PATH=%q is not inside any mounted ipc subPath", resultPath)
	}
}

// The narrowing is harness-only. agent-runner is Sympozium's own code and the
// writer the bridge's directories expect, so its mount must not change.
func TestBuildContainers_AgentRunnerKeepsTheWholeIPCVolume(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Task = sympoziumv1alpha1.NewStringTask("do the thing")

	cs, _, err := r.buildContainers(run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(cs, "agent")

	var whole bool
	for _, m := range agent.VolumeMounts {
		if m.Name == "ipc" && m.MountPath == "/ipc" && m.SubPath == "" {
			whole = true
		}
	}
	if !whole {
		t.Errorf("agent-runner lost its {ipc -> /ipc} mount; mounts = %v", agent.VolumeMounts)
	}
}

// ── the skill tool server ───────────────────────────────────────────────────

// toolBearingSidecar is a resolved sidecar that declares one native tool.
func toolBearingSidecar() []resolvedSidecar {
	return []resolvedSidecar{{
		skillPackName: "k8s-ops",
		sidecar: sympoziumv1alpha1.SkillSidecar{
			Tools: []sympoziumv1alpha1.SidecarTool{{
				Name:        "kubectl_get",
				Description: "read resources",
				Exec:        []string{"kubectl", "get"},
			}},
		},
	}}
}

// A harness cannot write exec requests any more, so its SkillPack tools have to
// arrive through the gated server. Without the sidecar, attaching a SkillPack to
// a harness run would silently do nothing.
func TestBuildContainers_HarnessGetsTheSkillToolServer(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, initCs, err := r.buildContainers(harnessModeRun(nil), false, nil, toolBearingSidecar(), nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	if containerByName(cs, skillToolsContainerName) != nil {
		t.Error("skill-tools is an ordinary container; it must be a native sidecar so it starts before the harness")
	}

	srv := containerByName(initCs, skillToolsContainerName)
	if srv == nil {
		t.Fatal("no skill-tools container; the harness has no way to reach its SkillPack tools")
	}
	// Native sidecar: an init container that never exits, started and probed
	// ready before the agent container runs.
	if srv.RestartPolicy == nil || *srv.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Error("skill-tools must have restartPolicy Always, or it is a one-shot init container")
	}
	if srv.StartupProbe == nil {
		t.Error("without a startup probe the kubelet does not wait for it, and the harness can race it")
	}
	if srv.StartupProbe == nil || srv.StartupProbe.Exec == nil {
		t.Fatal("skill-tools must use an in-container exec probe so its loopback-only listener is reachable")
	}
	if srv.StartupProbe.HTTPGet != nil {
		t.Error("skill-tools must not use an HTTP probe: kubelet probes from outside the pod network namespace")
	}
	if got := strings.Join(srv.StartupProbe.Exec.Command, " "); !strings.Contains(got, "127.0.0.1:8771/readyz") {
		t.Errorf("startup probe = %q, want loopback readiness endpoint", got)
	}
	if v, ok := envValueLocal(srv.Env, "MCP_SERVE_SKILL_TOOLS"); !ok || v != "true" {
		t.Errorf("MCP_SERVE_SKILL_TOOLS = (%q, %v), want true", v, ok)
	}
	// Loopback only: pod containers share a network namespace, so binding
	// anywhere else would expose the tools beyond the pod.
	addr, ok := envValueLocal(srv.Env, "SKILL_TOOLS_ADDR")
	if !ok || !strings.HasPrefix(addr, "127.0.0.1:") {
		t.Errorf("SKILL_TOOLS_ADDR = %q, want a loopback address", addr)
	}
	// It is the writer the skill sidecars expect, so it keeps the whole volume.
	var whole bool
	for _, m := range srv.VolumeMounts {
		if m.Name == "ipc" && m.MountPath == "/ipc" && m.SubPath == "" {
			whole = true
		}
	}
	if !whole {
		t.Error("skill-tools cannot write exec requests; it needs the whole ipc volume")
	}
}

// The policy the server enforces must come from the CR through the controller.
// If it arrived any other way the process being policed could edit it.
//
// The image declares toolFilter because spec.toolPolicy also covers the
// harness's own tools, which only the adapter can filter — the skill tool
// server makes the claim true for SkillPack tools, it does not replace it.
func TestBuildContainers_SkillToolServerCarriesTheToolPolicy(t *testing.T) {
	r := &AgentRunReconciler{}
	run := harnessModeRun(map[string]string{"capabilities": "toolFilter"})
	run.Spec.ToolPolicy = &sympoziumv1alpha1.ToolPolicySpec{Deny: []string{"kubectl_delete"}}

	_, initCs, err := r.buildContainers(run, false, nil, toolBearingSidecar(), nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	srv := containerByName(initCs, skillToolsContainerName)
	if srv == nil {
		t.Fatal("no skill-tools container")
	}
	if v, ok := envValueLocal(srv.Env, "TOOL_POLICY_DENY"); !ok || v != "kubectl_delete" {
		t.Errorf("TOOL_POLICY_DENY = (%q, %v), want the AgentRun's deny list", v, ok)
	}
}

// A run with sidecars but no operator MCP servers still needs the registry,
// because the skill tool server is in it.
func TestBuildContainers_HarnessGetsTheRegistryForSkillToolsAlone(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, _, err := r.buildContainers(harnessModeRun(nil), false, nil, toolBearingSidecar(), nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(cs, "agent")
	if v, ok := envValueLocal(agent.Env, "MCP_CONFIG_PATH"); !ok || v != "/config/mcp/mcp-servers.json" {
		t.Errorf("MCP_CONFIG_PATH = (%q, %v), want the JSON registry", v, ok)
	}
	if !hasMount(agent.VolumeMounts, "mcp-config") {
		t.Error("agent has no mcp-config mount; it cannot find the skill tool server")
	}
}

// agent-runner enforces the policy itself and writes its own exec requests, so
// it must not get the sidecar — it would be a second, redundant tool surface.
func TestBuildContainers_AgentRunnerDoesNotGetTheSkillToolServer(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Task = sympoziumv1alpha1.NewStringTask("do the thing")

	cs, initCs, err := r.buildContainers(run, false, nil, toolBearingSidecar(), nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	if containerByName(cs, skillToolsContainerName) != nil || containerByName(initCs, skillToolsContainerName) != nil {
		t.Error("agent-runner got the skill-tools sidecar; it already dispatches these tools itself")
	}
}

// No tools, no server.
func TestBuildContainers_NoSkillToolServerWithoutTools(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, initCs, err := r.buildContainers(harnessModeRun(nil), false, nil, []resolvedSidecar{{skillPackName: "plain"}}, nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	if containerByName(cs, skillToolsContainerName) != nil || containerByName(initCs, skillToolsContainerName) != nil {
		t.Error("skill-tools attached for a sidecar that declares no tools")
	}
}

// The skills entry belongs in the JSON registry the harness reads, and must
// stay out of the YAML one: the mcp-bridge reads that, and would try to connect
// to a peer in its own pod for tools it already dispatches itself.
func TestMCPRegistry_SkillToolsEntryIsJSONOnly(t *testing.T) {
	jsonOut, err := buildMCPServersJSON(nil, true)
	if err != nil {
		t.Fatalf("buildMCPServersJSON: %v", err)
	}
	if !strings.Contains(jsonOut, skilltools.ServerName) {
		t.Errorf("JSON registry = %s, want the %s entry", jsonOut, skilltools.ServerName)
	}
	if !strings.Contains(jsonOut, "127.0.0.1") {
		t.Errorf("JSON registry = %s, want a loopback URL", jsonOut)
	}

	yamlOut, err := buildMCPServersYAML(nil)
	if err != nil {
		t.Fatalf("buildMCPServersYAML: %v", err)
	}
	if strings.Contains(yamlOut, skilltools.ServerName) {
		t.Errorf("YAML registry = %s, must not carry the skill tool server", yamlOut)
	}

	// And it is opt-in: a run that does not need it gets a clean registry.
	plain, err := buildMCPServersJSON(nil, false)
	if err != nil {
		t.Fatalf("buildMCPServersJSON: %v", err)
	}
	if strings.Contains(plain, skilltools.ServerName) {
		t.Errorf("JSON registry = %s, want no skill tool server when not requested", plain)
	}
}

// The harness has no dispatch path for SkillPack tools — it cannot write exec
// requests — so mounting the manifest would only disclose the full tool list,
// policy-denied names included, and invite an adapter to act on it. The skill
// tool server reads it instead, and filters before anything reaches the harness.
func TestBuildContainers_HarnessDoesNotGetTheRawToolManifest(t *testing.T) {
	r := &AgentRunReconciler{}
	cs, initCs, err := r.buildContainers(harnessModeRun(nil), false, nil, toolBearingSidecar(), nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(cs, "agent")

	if hasMount(agent.VolumeMounts, "sidecar-tools") {
		t.Error("harness has the raw sidecar-tools manifest mounted; it lists tools the policy denies")
	}
	if _, ok := envValueLocal(agent.Env, "SIDECAR_TOOLS_MANIFEST_PATH"); ok {
		t.Error("SIDECAR_TOOLS_MANIFEST_PATH points a harness at a manifest it must not act on")
	}
	// SYMPOZIUM_SKILL_TARGETS advises agent-runner's execute_command, which a
	// harness does not have.
	if _, ok := envValueLocal(agent.Env, "SYMPOZIUM_SKILL_TARGETS"); ok {
		t.Error("SYMPOZIUM_SKILL_TARGETS is agent-runner's; it means nothing to a harness")
	}

	// The server that does read it still must.
	srv := containerByName(initCs, skillToolsContainerName)
	if srv == nil {
		t.Fatal("no skill-tools container")
	}
	if !hasMount(srv.VolumeMounts, "sidecar-tools") {
		t.Error("skill-tools lost the manifest it serves from")
	}
}

// agent-runner reads both, and must keep them.
func TestBuildContainers_AgentRunnerKeepsTheToolManifest(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Task = sympoziumv1alpha1.NewStringTask("do the thing")

	cs, _, err := r.buildContainers(run, false, nil, toolBearingSidecar(), nil, nil)
	if err != nil {
		t.Fatalf("buildContainers: %v", err)
	}
	agent := containerByName(cs, "agent")
	if !hasMount(agent.VolumeMounts, "sidecar-tools") {
		t.Error("agent-runner lost the sidecar-tools mount it dispatches from")
	}
	if _, ok := envValueLocal(agent.Env, "SYMPOZIUM_SKILL_TARGETS"); !ok {
		t.Error("agent-runner lost SYMPOZIUM_SKILL_TARGETS")
	}
}

// ── the reserved name namespace ─────────────────────────────────────────────

// Sympozium adds its own server to the registry a harness reads, and a harness
// namespaces tools by server name. An operator server called sympozium-skills
// would shadow the internal one, so the agent's SkillPack tool calls would go
// somewhere with no policy check between. That is a spoofing vector, so the run
// is refused rather than built.
func TestBuildContainers_RejectsReservedMCPServerName(t *testing.T) {
	r := &AgentRunReconciler{}
	mcp := []sympoziumv1alpha1.MCPServerRef{{Name: skilltools.ServerName, URL: "http://attacker:8080"}}

	_, _, err := r.buildContainers(harnessModeRun(nil), false, nil, toolBearingSidecar(), mcp, nil)
	if err == nil {
		t.Fatal("buildContainers accepted an MCP server shadowing the internal one")
	}
	if !strings.Contains(err.Error(), skilltools.ServerName) {
		t.Errorf("error should name the offending server, got: %v", err)
	}
	if !strings.Contains(err.Error(), sympoziumv1alpha1.ReservedNamePrefix) {
		t.Errorf("error should name the reserved prefix so the fix is obvious, got: %v", err)
	}
}

// The reservation is a prefix, not one name — a future internal server needs no
// new rule. It applies to agent-runner runs too: the registry is shared.
func TestBuildContainers_RejectsAnyReservedPrefixName(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	run.Spec.Task = sympoziumv1alpha1.NewStringTask("do the thing")
	mcp := []sympoziumv1alpha1.MCPServerRef{{Name: "Sympozium-Memory", URL: "http://x:8080"}}

	if _, _, err := r.buildContainers(run, false, nil, nil, mcp, nil); err == nil {
		t.Error("the reservation must be case-insensitive and cover every sympozium* name")
	}
}

// An ordinary name is untouched — the reservation must not cost operators the
// obvious names for their own servers.
func TestBuildContainers_AllowsOrdinaryMCPServerNames(t *testing.T) {
	r := &AgentRunReconciler{}
	mcp := []sympoziumv1alpha1.MCPServerRef{
		{Name: "github", URL: "http://gh:8080"},
		{Name: "my-sympozium-proxy", URL: "http://p:8080"},
	}
	if _, _, err := r.buildContainers(harnessModeRun(nil), false, nil, nil, mcp, nil); err != nil {
		t.Errorf("buildContainers rejected ordinary server names: %v", err)
	}
}
