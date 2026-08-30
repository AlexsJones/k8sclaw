package taskmodes

import (
	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// AgentContainerOverride replaces parts of the agent container outright. Most
// modes never need it — ConfigureAgentContainer's append-only env is enough
// when agent-runner still drives the loop. A mode needs this only when
// something other than agent-runner is the primary process (see harness.go).
//
// The controller applies it last, after every central env assignment, so an
// override always wins over the defaults it is there to displace.
type AgentContainerOverride struct {
	// Image is the fully-qualified image reference that becomes the pod's
	// primary process. Subject to SympoziumPolicy.imagePolicy.allowedRegistries
	// at admission, which is the control an operator uses to bound what may
	// run here.
	Image string

	// Command replaces the image's ENTRYPOINT. Leave nil to keep it.
	Command []string

	// Args replaces the image's CMD. Leave nil to keep it.
	Args []string

	// WorkingDir replaces the image's WORKDIR. Leave empty to keep it.
	WorkingDir string

	// SetEnv entries replace any same-named entry already on the container
	// and are appended when there is none. This is the only way to displace
	// a central assignment (TASK, MODEL_*); ConfigureAgentContainer appends
	// and so cannot.
	SetEnv []corev1.EnvVar

	// VolumeMounts are appended to the agent container. Names must match
	// entries in Volumes or an existing pod volume.
	VolumeMounts []corev1.VolumeMount

	// Volumes are appended to the pod. Reserved names are dropped by
	// buildVolumes, so a mode must claim its own name (see
	// reservedVolumeNames in agentrun_controller.go).
	Volumes []corev1.Volume
}

// AgentContainerOverrider is the optional TaskModeHandler extension for modes
// that replace the agent container. Kept off TaskModeHandler so the four-method
// contract stays valid for every mode that does not need it.
type AgentContainerOverrider interface {
	// OverrideAgentContainer returns the replacement for this task, or
	// (nil, nil) when the mode does not want one. It is called on tasks that
	// have already passed Validate.
	OverrideAgentContainer(task *sympoziumv1alpha1.TaskSpec) (*AgentContainerOverride, error)
}

// OverrideFor returns the agent-container override the task's mode requests.
// Returns (nil, nil) for string-form tasks, unregistered modes, and modes that
// do not implement AgentContainerOverrider.
func OverrideFor(task *sympoziumv1alpha1.TaskSpec) (*AgentContainerOverride, error) {
	if task == nil || task.IsString() {
		return nil, nil
	}
	handler, ok := Get(task.GetMode())
	if !ok {
		return nil, nil
	}
	overrider, ok := handler.(AgentContainerOverrider)
	if !ok {
		return nil, nil
	}
	return overrider.OverrideAgentContainer(task)
}

// ReplacesAgentContainer reports whether the task's mode swaps out the agent
// container. Exported because the answer has to be the same in two binaries:
// the controller uses it for the central decisions that differ when
// agent-runner is not the process being configured, and the webhook uses it to
// reject a run whose execution backend never builds a pod at all (see
// spec.backend: celln, which dispatches instead of scheduling containers — the
// override would be silently dropped).
//
// A malformed task answers false. Validate reports that failure with a usable
// message; this predicate is not the place for it.
func ReplacesAgentContainer(task *sympoziumv1alpha1.TaskSpec) bool {
	override, err := OverrideFor(task)
	return err == nil && override != nil
}
