package taskmodes

import (
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// SidecarDriven is the mode identifier for the sidecar-driven orchestrator
// pattern. The named SkillPack sidecar runs as the pod's primary process and
// drives the workflow end-to-end; the agent-runner runs in prompt-server
// mode and answers action decisions via /ipc/prompts/. See
// docs/modes/sidecar-driven.md for the full lifecycle.
const SidecarDriven = "sidecar-driven"

// SidecarDrivenHandler is the TaskModeHandler implementation for sidecar-driven
// mode. It configures the agent container (AGENT_MODE=prompt-server) and
// returns per-sidecar adjustments to apply during container build: the named
// initiator sidecar gets its command overridden from the SkillPack tool and
// SYMPOZIUM_RUN_CONFIG_JSON env var set from task.parameters.
//
// Required task fields for sidecar-driven mode:
//   - task.mode    == "sidecar-driven" (matched by the registry)
//   - task.tool    non-empty (SkillPack tool name on the initiator sidecar)
//
// Note: the spec.task object form does not carry a `sidecar` field by default
// (the schema only requires `mode`). The controller's resolvedSidecar lookup
// uses the SkillPack that declares the named tool to disambiguate. We may add
// a top-level sidecar field to TaskSpec later if multiple SkillPacks declare
// the same tool name; for now the controller picks the unique match or errors.
type SidecarDrivenHandler struct{}

// NewSidecarDrivenHandler returns a new SidecarDrivenHandler.
func NewSidecarDrivenHandler() *SidecarDrivenHandler {
	return &SidecarDrivenHandler{}
}

// Mode returns "sidecar-driven". See SidecarDriven.
func (h *SidecarDrivenHandler) Mode() string { return SidecarDriven }

// Capabilities describes what sidecar-driven actually honours. Each value is
// pinned to a line in cmd/agent-runner/main.go's runPromptServer:
//
//   - OutputSchema: yes. The sidecar sets ipc.PromptRequest.Schema per call
//     and the prompt service returns the validated payload in
//     PromptResult.Parsed.
//   - Persona: yes. runPromptServer reads SYSTEM_PROMPT and passes it as
//     promptServiceDeps.SystemPrompt.
//   - ToolFilter: no. runPromptServer passes Tools: nil — the LLM answers
//     individual prompts and has no tool surface, so spec.toolPolicy has
//     nothing to filter. A run that sets one is asking for enforcement that
//     cannot happen, which is what this descriptor exists to catch.
//   - Subagents: no. The orchestrator is the sidecar, not the LLM, and no
//     spawn path runs in prompt-server mode.
//   - Resume: no. A driving sidecar cannot be parked mid-workflow, so a
//     gated run falls back to the successor-clone retry path.
func (h *SidecarDrivenHandler) Capabilities() Capabilities {
	return Capabilities{
		OutputSchema: true,
		Persona:      true,
		ToolFilter:   false,
		Subagents:    false,
		Resume:       false,
	}
}

// Validate enforces required fields for sidecar-driven mode. Called by the
// controller after registry lookup and before any container configuration.
func (h *SidecarDrivenHandler) Validate(task *sympoziumv1alpha1.TaskSpec) error {
	if task == nil {
		return fmt.Errorf("sidecar-driven: task is nil")
	}
	if task.Tool == "" {
		return fmt.Errorf("sidecar-driven: task.tool is required (SkillPack tool name on the initiator sidecar)")
	}
	return nil
}

// ConfigureAgentContainer sets AGENT_MODE=prompt-server on the agent
// container. This is what makes the agent-runner skip the main agent loop
// and just listen on /ipc/prompts/ for the sidecar to drive individual
// LLM calls. Object-mode handlers don't set the TASK env var at all.
func (h *SidecarDrivenHandler) ConfigureAgentContainer(task *sympoziumv1alpha1.TaskSpec, agentEnv *[]corev1.EnvVar) error {
	if task == nil {
		return fmt.Errorf("sidecar-driven: task is nil")
	}
	*agentEnv = append(*agentEnv, corev1.EnvVar{
		Name:  "AGENT_MODE",
		Value: "prompt-server",
	})
	return nil
}

// AdjustSidecars returns the per-sidecar mutations for sidecar-driven mode.
// The controller iterates resolved SkillPack sidecars and finds the one that
// declares task.tool in its Sidecar.Tools list. That sidecar gets its command
// overridden from the tool's exec + subcommand, and gets
// SYMPOZIUM_RUN_CONFIG_JSON env appended (JSON-marshalled task.parameters).
//
// If no sidecar declares task.tool, the handler returns an error — that's
// the cleanest failure mode (caller asked for a tool that doesn't exist on
// any declared sidecar). We deliberately do not silently fall back to the
// sidecar's default CMD, because in prompt-server mode the agent would
// simply sit idle waiting for /ipc/prompts/ requests that never come.
func (h *SidecarDrivenHandler) AdjustSidecars(task *sympoziumv1alpha1.TaskSpec, sidecars []SidecarContext) ([]SidecarAdjustment, error) {
	if task == nil {
		return nil, fmt.Errorf("sidecar-driven: task is nil")
	}
	if task.Tool == "" {
		return nil, fmt.Errorf("sidecar-driven: task.tool is required")
	}

	var matching *SidecarContext
	for i := range sidecars {
		sc := &sidecars[i]
		for _, tool := range sc.Sidecar.Tools {
			if tool.Name == task.Tool {
				matching = sc
				break
			}
		}
		if matching != nil {
			break
		}
	}
	if matching == nil {
		declared := make([]string, 0, len(sidecars))
		for _, sc := range sidecars {
			for _, t := range sc.Sidecar.Tools {
				declared = append(declared, fmt.Sprintf("%s.%s", sc.SkillPackName, t.Name))
			}
		}
		return nil, fmt.Errorf("sidecar-driven: no sidecar declares tool %q (declared tools: %v)", task.Tool, declared)
	}

	// Build the new command: tool.Exec + (tool.Subcommand if set).
	var newCmd []string
	for _, t := range matching.Sidecar.Tools {
		if t.Name != task.Tool {
			continue
		}
		newCmd = append([]string{}, t.Exec...)
		if t.Subcommand != "" {
			newCmd = append(newCmd, t.Subcommand)
		}
		break
	}

	// Marshal task.parameters. Nil maps must serialise to "{}" (not "null")
	// because the sidecar's CLI does `JSON.parse(SYMPOZIUM_RUN_CONFIG_JSON)`
	// and expects an object — JSON.parse("null") returns null which fails
	// the sidecar's input schema. Empty-but-non-nil maps serialise to "{}"
	// naturally.
	params := map[string]string(task.Parameters)
	if params == nil {
		params = map[string]string{}
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("sidecar-driven: failed to marshal task.parameters: %w", err)
	}

	return []SidecarAdjustment{
		{
			SkillPackName:   matching.SkillPackName,
			OverrideCommand: newCmd,
			AddEnv: []corev1.EnvVar{
				{
					Name:  "SYMPOZIUM_RUN_CONFIG_JSON",
					Value: string(paramsJSON),
				},
			},
		},
	}, nil
}
