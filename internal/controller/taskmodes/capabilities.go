package taskmodes

import (
	"fmt"
	"sort"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// Capability names, used in admission error messages and in the mode docs.
// The string form is the field name in lowerCamelCase so an operator reading
// a denial can map it straight back to the descriptor.
const (
	CapabilityOutputSchema = "outputSchema"
	CapabilityToolFilter   = "toolFilter"
	CapabilityPersona      = "persona"
	CapabilitySubagents    = "subagents"
	CapabilityResume       = "resume"
)

// Capabilities is a task mode's self-description: what it can honour from an
// AgentRun spec. A mode advertises this before any run exists, so a request
// for something the mode lacks fails loud at admission instead of being
// accepted and then quietly ignored at runtime.
//
// Every field is opt-in: the zero value claims nothing, which is the correct
// default for a mode (or an operator-supplied harness image) whose behaviour
// Sympozium cannot vouch for.
type Capabilities struct {
	// OutputSchema: the mode can constrain a model call with a JSON Schema
	// and return the validated payload.
	OutputSchema bool

	// ToolFilter: the mode honours spec.toolPolicy (allow/deny).
	ToolFilter bool

	// Persona: the mode honours spec.systemPrompt.
	Persona bool

	// Subagents: the mode can spawn child runs.
	Subagents bool

	// Resume: the mode can be parked mid-run and resumed in place, rather
	// than restarted as a successor clone.
	Resume bool
}

// Names returns the sorted names of the capabilities this descriptor sets.
// Sorted so log lines and admission messages are stable.
func (c Capabilities) Names() []string {
	var names []string
	if c.OutputSchema {
		names = append(names, CapabilityOutputSchema)
	}
	if c.Persona {
		names = append(names, CapabilityPersona)
	}
	if c.Resume {
		names = append(names, CapabilityResume)
	}
	if c.Subagents {
		names = append(names, CapabilitySubagents)
	}
	if c.ToolFilter {
		names = append(names, CapabilityToolFilter)
	}
	sort.Strings(names)
	return names
}

// Missing returns the sorted names of the capabilities set on want that this
// descriptor does not have. An empty result means c satisfies want.
func (c Capabilities) Missing(want Capabilities) []string {
	gap := Capabilities{
		OutputSchema: want.OutputSchema && !c.OutputSchema,
		ToolFilter:   want.ToolFilter && !c.ToolFilter,
		Persona:      want.Persona && !c.Persona,
		Subagents:    want.Subagents && !c.Subagents,
		Resume:       want.Resume && !c.Resume,
	}
	return gap.Names()
}

// Union returns a descriptor supporting everything either input supports.
// Used by a mode whose support varies by backend to report its ceiling.
func (c Capabilities) Union(other Capabilities) Capabilities {
	return Capabilities{
		OutputSchema: c.OutputSchema || other.OutputSchema,
		ToolFilter:   c.ToolFilter || other.ToolFilter,
		Persona:      c.Persona || other.Persona,
		Subagents:    c.Subagents || other.Subagents,
		Resume:       c.Resume || other.Resume,
	}
}

// ParseCapabilities builds a descriptor from capability names. Used where an
// operator declares, out of band, what an image they supplied honours (see
// the harness mode's `custom` backend). An unknown name is an error rather
// than a silent no-op — a typo would otherwise read as "unsupported".
func ParseCapabilities(names []string) (Capabilities, error) {
	var caps Capabilities
	for _, raw := range names {
		switch raw {
		case CapabilityOutputSchema:
			caps.OutputSchema = true
		case CapabilityToolFilter:
			caps.ToolFilter = true
		case CapabilityPersona:
			caps.Persona = true
		case CapabilitySubagents:
			caps.Subagents = true
		case CapabilityResume:
			caps.Resume = true
		case "":
			continue
		default:
			return Capabilities{}, fmt.Errorf("unknown capability %q; known: %v", raw, KnownCapabilities())
		}
	}
	return caps, nil
}

// KnownCapabilities returns every capability name, sorted.
func KnownCapabilities() []string {
	all := Capabilities{
		OutputSchema: true,
		ToolFilter:   true,
		Persona:      true,
		Subagents:    true,
		Resume:       true,
	}
	return all.Names()
}

// TaskCapabilityReporter is an optional TaskModeHandler extension for modes
// whose support depends on the task rather than being fixed — harness mode
// reads a declaration off the task, because the image an operator names is
// the only thing that knows what it honours. Handlers that do not implement
// it are assumed uniform, and CapabilitiesFor falls back to
// TaskModeHandler.Capabilities.
type TaskCapabilityReporter interface {
	// TaskCapabilities narrows Capabilities() to one concrete task. It is
	// called on tasks that have already passed Validate.
	TaskCapabilities(task *sympoziumv1alpha1.TaskSpec) Capabilities
}

// CapabilitiesFor returns the capabilities a handler offers for one specific
// task. Prefer this over calling Capabilities() directly whenever a TaskSpec
// is in hand: for a multi-backend mode the two differ, and only this one is
// safe to gate admission on.
func CapabilitiesFor(h TaskModeHandler, task *sympoziumv1alpha1.TaskSpec) Capabilities {
	if h == nil {
		return Capabilities{}
	}
	if tc, ok := h.(TaskCapabilityReporter); ok {
		return tc.TaskCapabilities(task)
	}
	return h.Capabilities()
}

// RequestedCapabilities derives from an AgentRun spec which mode capabilities
// the run actually needs. Only what the CR states unambiguously is counted —
// an inferred "maybe" would deny working runs:
//
//   - Persona   ← spec.systemPrompt is set.
//   - ToolFilter ← spec.toolPolicy declares any allow or deny entry.
//   - Subagents ← spec.toolPolicy explicitly allows a spawn/delegate tool.
//     A nil toolPolicy says nothing about intent, so it requests nothing.
//
// OutputSchema and Resume are deliberately absent: neither is expressible on
// AgentRun today. OutputSchema is requested per-call over /ipc/prompts/
// (ipc.PromptRequest.Schema) once the run is live, and Resume is decided by
// the gate machinery, not the submitter. Both stay in the descriptor so modes
// can be told apart, and both start being checked here the day a spec field
// exists to check.
func RequestedCapabilities(run *sympoziumv1alpha1.AgentRun) Capabilities {
	if run == nil {
		return Capabilities{}
	}
	var want Capabilities
	if run.Spec.SystemPrompt != "" {
		want.Persona = true
	}
	if tp := run.Spec.ToolPolicy; tp != nil {
		if len(tp.Allow) > 0 || len(tp.Deny) > 0 {
			want.ToolFilter = true
		}
		for _, tool := range tp.Allow {
			if tool == "spawn_subagents" || tool == "delegate_to_persona" {
				want.Subagents = true
				break
			}
		}
	}
	return want
}

// ValidateCapabilities checks an AgentRun's object-form task against the
// capabilities its mode offers for that task, and returns a readable error
// naming the mode and every capability it cannot honour.
//
// Returns nil for string-form or unset tasks (Path A has no mode) and for a
// mode with no registered handler. Unknown modes are the caller's business:
// the controller already fails those with the supported-mode list, and the
// webhook must not deny them, or a mode registered only in the controller
// binary would become unschedulable.
func ValidateCapabilities(run *sympoziumv1alpha1.AgentRun) error {
	if run == nil {
		return nil
	}
	task := run.Spec.Task
	if task == nil || task.IsString() {
		return nil
	}
	mode := task.GetMode()
	handler, ok := Get(mode)
	if !ok {
		return nil
	}

	have := CapabilitiesFor(handler, task)
	missing := have.Missing(RequestedCapabilities(run))
	if len(missing) == 0 {
		return nil
	}

	supported := have.Names()
	if len(supported) == 0 {
		supported = []string{"none"}
	}
	return fmt.Errorf(
		"task.mode %q does not support %v requested by this AgentRun (mode supports: %v)",
		mode, missing, supported)
}

// ValidateRunCompatibility rejects AgentRun controls whose implementation
// lives inside agent-runner and therefore cannot be honoured when an external
// harness replaces that process.
func ValidateRunCompatibility(run *sympoziumv1alpha1.AgentRun) error {
	if run == nil || HarnessImage(run.Spec.Task) == "" {
		return nil
	}
	var unsupported []string
	if run.Spec.Mode == "server" {
		unsupported = append(unsupported, "mode=server")
	}
	if run.Spec.DryRun {
		unsupported = append(unsupported, "dryRun")
	}
	if run.Spec.CanaryMode {
		unsupported = append(unsupported, "canaryMode")
	}
	if run.Spec.AgentSandbox != nil && run.Spec.AgentSandbox.WarmPoolRef != "" {
		unsupported = append(unsupported, "agentSandbox.warmPoolRef")
	}
	if len(unsupported) > 0 {
		return fmt.Errorf("task.mode %q does not support AgentRun controls %v because they are implemented by agent-runner", Harness, unsupported)
	}
	return nil
}
