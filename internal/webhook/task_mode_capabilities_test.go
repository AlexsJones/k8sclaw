package webhook

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller/taskmodes"
)

// ── task-mode capability admission tests ─────────────────────────────────────
//
// A capability descriptor is only worth having if the mismatch is caught
// before the run exists. These assert the denial happens at admission, and
// that the message names both the mode and the capability so an operator can
// act on it without reading the handler source.

// capabilityEnforcer returns a PolicyEnforcer whose client holds one Agent
// with no policy bound, so the capability check is the only thing under test.
func capabilityEnforcer(t *testing.T) *PolicyEnforcer {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "inst", Namespace: "default"},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build()
	return &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}
}

func capabilityRun(task *sympoziumv1alpha1.TaskSpec) *sympoziumv1alpha1.AgentRun {
	return &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "r", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: "inst",
			Task:     task,
		},
	}
}

func harnessTaskSpec(params map[string]string) *sympoziumv1alpha1.TaskSpec {
	if params == nil {
		params = map[string]string{}
	}
	if _, ok := params["prompt"]; !ok {
		params["prompt"] = "summarise the incident"
	}
	if _, ok := params["image"]; !ok {
		params["image"] = "ghcr.io/acme/my-harness:v1"
	}
	return &sympoziumv1alpha1.TaskSpec{
		Mode:       taskmodes.Harness,
		Parameters: params,
	}
}

// An image that declares nothing gets nothing: a run asking for enforcement
// the image never claimed is denied rather than admitted and degraded.
func TestPolicyEnforcer_DeniesUnsupportedCapability(t *testing.T) {
	pe := capabilityEnforcer(t)

	run := capabilityRun(harnessTaskSpec(nil))
	run.Spec.ToolPolicy = &sympoziumv1alpha1.ToolPolicySpec{Deny: []string{"execute_command"}}

	resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
	if resp.Allowed {
		t.Fatal("expected DENY for a toolPolicy against an image that declares no toolFilter; got allowed")
	}

	msg := resp.Result.Message
	if !strings.Contains(msg, taskmodes.Harness) {
		t.Errorf("denial should name the mode; got: %s", msg)
	}
	if !strings.Contains(msg, taskmodes.CapabilityToolFilter) {
		t.Errorf("denial should name the capability; got: %s", msg)
	}
}

func TestPolicyEnforcer_AllowsSupportedCapability(t *testing.T) {
	pe := capabilityEnforcer(t)

	run := capabilityRun(harnessTaskSpec(map[string]string{
		"capabilities": "persona,toolFilter",
	}))
	run.Spec.ToolPolicy = &sympoziumv1alpha1.ToolPolicySpec{Deny: []string{"execute_command"}}
	run.Spec.SystemPrompt = "You are a careful SRE."

	resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
	if !resp.Allowed {
		t.Fatalf("expected ALLOW for capabilities the image declares; denied: %s", resp.Result.Message)
	}
}

// A partial declaration is the interesting case: the two halves of one
// AgentRun land on opposite sides of the descriptor.
func TestPolicyEnforcer_PartialDeclaration(t *testing.T) {
	pe := capabilityEnforcer(t)

	run := capabilityRun(harnessTaskSpec(map[string]string{
		"capabilities": "persona",
	}))
	run.Spec.SystemPrompt = "You are a careful SRE."

	resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
	if !resp.Allowed {
		t.Fatalf("expected ALLOW: persona is declared; denied: %s", resp.Result.Message)
	}

	run.Spec.ToolPolicy = &sympoziumv1alpha1.ToolPolicySpec{Deny: []string{"execute_command"}}
	resp = pe.Handle(context.Background(), admissionRequestFor(t, run))
	if resp.Allowed {
		t.Fatal("expected DENY: toolFilter was never declared")
	}
	if !strings.Contains(resp.Result.Message, taskmodes.CapabilityToolFilter) {
		t.Errorf("denial should name the capability; got: %s", resp.Result.Message)
	}
}

// backend: celln dispatches the task string to the celln router and never
// builds a pod, so a mode that replaces the agent container has nothing to
// replace. Without this the run is admitted and the harness image is silently
// dropped — the same failure the controller's celln/agentSandbox guard exists
// to prevent.
func TestPolicyEnforcer_DeniesAgentContainerOverrideOnCelln(t *testing.T) {
	pe := capabilityEnforcer(t)

	run := capabilityRun(harnessTaskSpec(nil))
	run.Spec.Backend = "celln"

	resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
	if resp.Allowed {
		t.Fatal("expected DENY: backend celln never creates the container harness mode replaces")
	}
	msg := resp.Result.Message
	if !strings.Contains(msg, "celln") || !strings.Contains(msg, taskmodes.Harness) {
		t.Errorf("denial should name both the backend and the mode; got: %s", msg)
	}
}

// The neighbouring case must keep working: agentSandbox builds its pod through
// buildAgentPodTemplate, which wraps buildContainers, so task-mode dispatch
// applies there normally.
func TestPolicyEnforcer_AllowsAgentContainerOverrideOnAgentSandbox(t *testing.T) {
	pe := capabilityEnforcer(t)

	run := capabilityRun(harnessTaskSpec(nil))
	run.Spec.AgentSandbox = &sympoziumv1alpha1.AgentSandboxSpec{Enabled: true}

	resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
	if !resp.Allowed {
		t.Fatalf("expected ALLOW: agentSandbox reaches buildContainers; denied: %s", resp.Result.Message)
	}
}

// Applying the descriptor retroactively to sidecar-driven is part of the
// value: prompt-server mode runs the LLM with no tools at all, so a
// toolPolicy there was always silently dropped.
func TestPolicyEnforcer_DeniesToolPolicyOnSidecarDriven(t *testing.T) {
	pe := capabilityEnforcer(t)

	run := capabilityRun(&sympoziumv1alpha1.TaskSpec{
		Mode: taskmodes.SidecarDriven,
		Tool: "collector_run",
	})
	run.Spec.ToolPolicy = &sympoziumv1alpha1.ToolPolicySpec{Allow: []string{"read_file"}}

	resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
	if resp.Allowed {
		t.Fatal("expected DENY: sidecar-driven has no tool surface to filter")
	}
	if !strings.Contains(resp.Result.Message, taskmodes.SidecarDriven) {
		t.Errorf("denial should name the mode; got: %s", resp.Result.Message)
	}
}

// systemPrompt is honoured by sidecar-driven, so it must still be allowed.
func TestPolicyEnforcer_AllowsSystemPromptOnSidecarDriven(t *testing.T) {
	pe := capabilityEnforcer(t)

	run := capabilityRun(&sympoziumv1alpha1.TaskSpec{
		Mode: taskmodes.SidecarDriven,
		Tool: "collector_run",
	})
	run.Spec.SystemPrompt = "You are a careful SRE."

	resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
	if !resp.Allowed {
		t.Fatalf("expected ALLOW: sidecar-driven passes SYSTEM_PROMPT through; denied: %s", resp.Result.Message)
	}
}

// Path A carries no mode, so there is nothing to check.
func TestPolicyEnforcer_StringFormTaskSkipsCapabilityCheck(t *testing.T) {
	pe := capabilityEnforcer(t)

	run := capabilityRun(sympoziumv1alpha1.NewStringTask("do the thing"))
	run.Spec.ToolPolicy = &sympoziumv1alpha1.ToolPolicySpec{Deny: []string{"execute_command"}}
	run.Spec.SystemPrompt = "You are a careful SRE."

	resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
	if !resp.Allowed {
		t.Fatalf("expected ALLOW for a string-form task; denied: %s", resp.Result.Message)
	}
}

// The webhook is a separate binary from the controller, so a downstream mode
// registered only in the controller's main() is invisible here. Denying it
// would make that documented path unusable.
func TestPolicyEnforcer_UnregisteredModePassesCapabilityCheck(t *testing.T) {
	pe := capabilityEnforcer(t)

	run := capabilityRun(&sympoziumv1alpha1.TaskSpec{Mode: "acme-batch-runner"})
	run.Spec.ToolPolicy = &sympoziumv1alpha1.ToolPolicySpec{Deny: []string{"execute_command"}}

	resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
	if !resp.Allowed {
		t.Fatalf("expected ALLOW for an unregistered mode; denied: %s", resp.Result.Message)
	}
}

// The custom backend's image becomes the pod's primary process, so
// allowedRegistries is the control that bounds which harnesses may run.
func TestPolicyEnforcer_DeniesHarnessImageOutsideAllowedRegistries(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "locked-down", Namespace: "default"},
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{
			ImagePolicy: &sympoziumv1alpha1.ImagePolicySpec{
				AllowedRegistries: []string{"ghcr.io/sympozium-ai/"},
			},
		},
	}
	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "inst", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentSpec{PolicyRef: "locked-down"},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	run := capabilityRun(harnessTaskSpec(map[string]string{
		"image": "docker.io/someone/unvetted-harness:latest",
	}))

	resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
	if resp.Allowed {
		t.Fatal("expected DENY for a harness image outside allowedRegistries; got allowed")
	}
	if !strings.Contains(resp.Result.Message, "unvetted-harness") {
		t.Errorf("denial should name the image; got: %s", resp.Result.Message)
	}
}

func TestPolicyEnforcer_AllowsHarnessImageInsideAllowedRegistries(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	policy := &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "locked-down", Namespace: "default"},
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{
			ImagePolicy: &sympoziumv1alpha1.ImagePolicySpec{
				AllowedRegistries: []string{"ghcr.io/acme/"},
			},
		},
	}
	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "inst", Namespace: "default"},
		Spec:       sympoziumv1alpha1.AgentSpec{PolicyRef: "locked-down"},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent, policy).Build()
	pe := &PolicyEnforcer{Client: cl, Log: logr.Discard(), Decoder: decoderFor(t, scheme)}

	run := capabilityRun(harnessTaskSpec(map[string]string{
		"image": "ghcr.io/acme/my-harness:v1",
	}))

	resp := pe.Handle(context.Background(), admissionRequestFor(t, run))
	if !resp.Allowed {
		t.Fatalf("expected ALLOW for an image inside allowedRegistries; denied: %s", resp.Result.Message)
	}
}
