// Package taskmodes is the TaskModeHandler registry and per-mode implementations
// for the polymorphic AgentRun spec.task field.
//
// The AgentRun CR exposes a single `task` field that accepts either a string
// (legacy Path A: the prompt passed to the LLM via TASK env) or an object
// describing an orchestration mode (Path B). For object form, the controller
// looks up a TaskModeHandler by Mode() and dispatches the per-mode container
// configuration.
//
// Adding a new mode is a self-contained change:
//  1. Implement TaskModeHandler (Mode, Capabilities, Validate,
//     ConfigureAgentContainer, AdjustSidecars).
//  2. Register it in the package init() below (or from the controller's
//     main.go if it lives in a downstream repo).
//  3. Document it under docs/modes/<mode-name>.md.
//
// The controller never branches on Mode() directly; it always goes through
// the registry. This keeps the central reconcile loop stable as modes are
// added.
package taskmodes

import (
	"fmt"
	"sort"
	"sync"

	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// SidecarContext is the read-only view of a resolved SkillPack sidecar that
// a handler may inspect when computing its AdjustSidecars output. The handler
// never mutates this; it returns SidecarAdjustment values instead and the
// controller applies them during container build.
type SidecarContext struct {
	// SkillPackName is the SkillPack CR name this sidecar was declared in.
	// Unique within an AgentRun.
	SkillPackName string

	// Sidecar is the resolved SkillSidecar spec.
	Sidecar sympoziumv1alpha1.SkillSidecar

	// Params holds per-instance SKILL_* env vars (from SkillRef.Params on
	// the AgentRun). Handlers generally ignore these; included for
	// completeness so future modes can read them.
	Params map[string]string
}

// SidecarAdjustment is the per-sidecar mutation a handler requests. The
// controller applies these during container build (replacing command,
// appending env, etc.). Multiple adjustments for the same SkillPackName are
// disallowed — the handler should accumulate them into a single adjustment.
type SidecarAdjustment struct {
	SkillPackName string

	// OverrideCommand, when non-empty, replaces the sidecar's default
	// command (i.e. the SkillSidecar.Command slice becomes this value).
	OverrideCommand []string

	// AddEnv is appended to the sidecar's env (after the SkillSidecar.Env
	// entries have been copied). Order is preserved.
	AddEnv []corev1.EnvVar
}

// TaskModeHandler is the per-mode contract. The controller invokes these
// methods in order: Validate, ConfigureAgentContainer, AdjustSidecars.
// Any error short-circuits the rest.
type TaskModeHandler interface {
	// Mode is the identifier matched against spec.task.mode in object form.
	// Must be unique across registered handlers. Examples: "sidecar-driven".
	Mode() string

	// Capabilities reports what this mode supports. Checked before the run
	// exists, so an AgentRun asking for something the mode cannot honour is
	// rejected at admission rather than accepted and silently degraded.
	//
	// A mode whose support varies from task to task reports the ceiling
	// here (what the mode can do at its best) and additionally implements
	// TaskCapabilityReporter to narrow it per task. Callers that hold a
	// TaskSpec should use CapabilitiesFor, never this method directly.
	Capabilities() Capabilities

	// Validate runs after the handler is resolved and before any container
	// configuration. It should check that the task object carries the
	// required fields for this mode (e.g. sidecar-driven requires Tool).
	// Returns a human-readable error to surface on AgentRun.status.error.
	Validate(task *sympoziumv1alpha1.TaskSpec) error

	// ConfigureAgentContainer mutates the agent container's env in place.
	// Handlers typically set AGENT_MODE and any other per-mode env vars.
	// Implementations should append (not replace) unless they specifically
	// intend to clobber an existing entry.
	ConfigureAgentContainer(task *sympoziumv1alpha1.TaskSpec, agentEnv *[]corev1.EnvVar) error

	// AdjustSidecars returns per-sidecar mutations to apply during
	// container build. Returning a nil/empty slice is fine — a handler may
	// only need to configure the agent container. The handler should not
	// mutate the input sidecars slice; it returns its desired state.
	AdjustSidecars(task *sympoziumv1alpha1.TaskSpec, sidecars []SidecarContext) ([]SidecarAdjustment, error)
}

// Registry maps Mode() → TaskModeHandler. It is process-global; handlers
// register themselves in init() so the controller can look them up by mode
// without importing them directly.
//
// The registry is read-only after startup. Use Register at package init
// time; Get from the controller.
var (
	registryMu sync.RWMutex
	registry   = map[string]TaskModeHandler{}
)

// Register adds a handler to the global registry. Panics on duplicate
// registration because that indicates a programming error (two handlers
// competing for the same mode) and should be caught at startup.
func Register(h TaskModeHandler) {
	if h == nil {
		panic("taskmodes: Register called with nil handler")
	}
	mode := h.Mode()
	if mode == "" {
		panic(fmt.Sprintf("taskmodes: handler %T has empty Mode()", h))
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[mode]; exists {
		panic(fmt.Sprintf("taskmodes: mode %q already registered", mode))
	}
	registry[mode] = h
}

// Get returns the handler for the given mode, or (nil, false) if no handler
// is registered. The controller logs the supported modes (sorted) when the
// AgentRun requests an unknown mode.
func Get(mode string) (TaskModeHandler, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	h, ok := registry[mode]
	return h, ok
}

// ValidateTask runs the registered handler's validation for an object-form
// task. It intentionally allows unregistered modes: downstream controller
// binaries may register modes that the separately deployed webhook does not
// know about.
func ValidateTask(task *sympoziumv1alpha1.TaskSpec) error {
	if task == nil || task.IsString() {
		return nil
	}
	handler, ok := Get(task.GetMode())
	if !ok {
		return nil
	}
	if err := handler.Validate(task); err != nil {
		return fmt.Errorf("task.mode %q validation failed: %w", task.GetMode(), err)
	}
	return nil
}

// SupportedModes returns the registered mode names sorted alphabetically.
// Used by the controller to render a clear error message when an AgentRun
// requests an unregistered mode.
func SupportedModes() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	modes := make([]string, 0, len(registry))
	for m := range registry {
		modes = append(modes, m)
	}
	sort.Strings(modes)
	return modes
}
