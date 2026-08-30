package taskmodes

import (
	"encoding/json"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// Harness is the mode identifier for running an external agent harness as the
// pod's primary process instead of agent-runner. See docs/modes/harness.md.
//
// Sympozium's own strengths — policy CRDs and the admission webhook, the
// synthetic membrane, ensembles and relationships, gVisor/Kata isolation,
// response gates, cost estimation, channels, schedules — do not care what
// binary drove the agent loop. This mode makes that explicit: which harness
// runs inside the Pod becomes the operator's choice, supplied through the
// mechanisms that already exist (TASK, per-key SecretKeyRef credentials, the
// MCP ConfigMap, /skills, TOOL_POLICY_*), and returning its answer through
// the unchanged result contract so gates, cost estimation, retries, memory
// extraction and the run-detail UI keep working untouched.
//
// Sympozium ships the seam, not the harnesses. An adapter image tracks its
// upstream harness's release cadence, which is work this repo deliberately
// does not take on — the same division as the celln backend, where the
// execution runtime lives in its own repository. Writing one is
// docs/modes/harness-adapters.md.
const (
	Harness = "harness"

	// HarnessContractVersion identifies the adapter contract exposed by this
	// implementation. Adapters should fail closed on versions they do not
	// understand rather than guessing at filesystem or result semantics.
	HarnessContractVersion = "v1alpha1"
)

// harnessHomeVolume gives the harness a writable HOME on a pod that keeps
// readOnlyRootFilesystem. Several harnesses assume they can write to
// $HOME/.config and $HOME/.cache; the answer is an emptyDir, never a relaxed
// security context.
const (
	harnessHomeVolume = "harness-home"
	harnessHomePath   = "/home/agent"
)

// The two /ipc paths a harness gets, and the only two it needs: read the task,
// return the result.
//
// The agent container is normally given the whole /ipc volume, and the bridge
// watches eight directories under it — spawn, tools, messages, schedules,
// prompts, context, input, output. Each one turns a dropped JSON file into a
// control-plane action: a sub-agent run, a command executed in a skill
// sidecar, an outbound channel message, a schedule.
//
// That is safe when agent-runner is the agent process, because it is
// Sympozium's own code and writes those files only for tools it chose to
// register — policy and writer are the same trusted process. A harness
// separates them. Handing it the volume root would let any harness with a
// file-write tool spawn children and message a Slack workspace, whatever its
// capability descriptor claims.
//
// So the mount is narrowed to two subPaths instead. The other six directories
// are not in the container's mount namespace at all: not filtered, not
// checked, absent. Anything a harness should be able to reach has to be given
// to it deliberately — see the skill tool server in the controller.
const (
	harnessIPCVolume    = "ipc"
	harnessIPCInputDir  = "input"
	harnessIPCOutputDir = "output"
	harnessIPCInputPath = "/ipc/input"
	harnessIPCOutPath   = "/ipc/output"
)

// Task parameter keys recognised by harness mode.
const (
	// harnessParamPrompt carries the task text. Object-form tasks have no
	// prompt field (adding one would make TASK non-empty for every mode and
	// break sidecar-driven's prompt-server entry), so harness mode takes it
	// from parameters and sets TASK from there.
	harnessParamPrompt = "prompt"
	// harnessParamImage is the adapter image that becomes the pod's primary
	// process. Required: Sympozium builds no harness images.
	harnessParamImage = "image"
	// harnessParamArgs is a JSON array of strings appended to the harness
	// argv. TaskSpec.Parameters is map[string]string, so it travels as a
	// JSON-encoded string.
	harnessParamArgs = "args"
	// harnessParamCapabilities is the comma-separated capability list the
	// operator asserts their image honours. Nothing is assumed: an image
	// Sympozium did not build gets no claims by default.
	harnessParamCapabilities = "capabilities"
	// harnessParamRuntime references an administrator-approved AgentRuntime in
	// the run's namespace by name. When set, the runtime supplies the image and
	// capabilities; it is mutually exclusive with the inline harnessParamImage.
	harnessParamRuntime = "runtime"
)

// HarnessHandler is the TaskModeHandler for harness mode. It leaves the
// sidecars alone entirely — its whole job is to replace the agent container.
type HarnessHandler struct{}

// NewHarnessHandler returns a new HarnessHandler.
func NewHarnessHandler() *HarnessHandler {
	return &HarnessHandler{}
}

// Mode returns "harness". See Harness.
func (h *HarnessHandler) Mode() string { return Harness }

// Capabilities is the zero value: harness mode itself vouches for nothing.
//
// Every harness-mode run names an image Sympozium did not build and cannot
// inspect, so there is no mode-wide claim to make. What a run may ask for
// comes from the operator's own declaration on the task, which TaskCapabilities
// reads. Callers holding a TaskSpec must use CapabilitiesFor.
func (h *HarnessHandler) Capabilities() Capabilities {
	return Capabilities{}
}

// TaskCapabilities returns exactly what the operator declared in
// parameters.capabilities — nothing by default, because Sympozium cannot
// vouch for an image it did not build.
func (h *HarnessHandler) TaskCapabilities(task *sympoziumv1alpha1.TaskSpec) Capabilities {
	if task == nil {
		return Capabilities{}
	}
	// Validate has already rejected unparseable declarations; an error here
	// can only mean this was called on an unvalidated task, and claiming
	// nothing is the safe answer.
	caps, err := ParseCapabilities(splitCommaList(task.Parameters[harnessParamCapabilities]))
	if err != nil {
		return Capabilities{}
	}
	return caps
}

// Validate enforces the required fields for harness mode. It is strict on
// purpose: the agent container is being replaced wholesale, so a
// misconfiguration here produces a pod that runs the wrong binary rather than
// one that merely misbehaves.
func (h *HarnessHandler) Validate(task *sympoziumv1alpha1.TaskSpec) error {
	if task == nil {
		return fmt.Errorf("harness: task is nil")
	}

	image := strings.TrimSpace(task.Parameters[harnessParamImage])
	runtimeName := strings.TrimSpace(task.Parameters[harnessParamRuntime])

	if image == "" && runtimeName == "" {
		return fmt.Errorf("harness: task.parameters requires either %q (an adapter image) or %q (an AgentRuntime reference); Sympozium ships no default",
			harnessParamImage, harnessParamRuntime)
	}
	if image != "" && runtimeName != "" {
		return fmt.Errorf("harness: set exactly one of task.parameters.%s or task.parameters.%s, not both",
			harnessParamImage, harnessParamRuntime)
	}

	// The image becomes the pod's primary process and receives the run's model
	// and MCP credentials, so a mutable tag is not an acceptable trust anchor:
	// "v1" can be retagged under the operator. Require a digest-pinned reference
	// so the exact artifact is fixed and can be recorded on the run. A runtime
	// reference skips this here: its image is checked after NormalizeHarnessTask
	// substitutes it (and the AgentRuntime controller already vets it).
	if image != "" {
		if _, ok := sympoziumv1alpha1.ParseImageDigest(image); !ok {
			return fmt.Errorf("harness: task.parameters.%s must be a digest-pinned OCI reference (e.g. \"ghcr.io/acme/my-harness@sha256:<64-hex>\"); tag-only or unpinned references are rejected", harnessParamImage)
		}
	}

	if strings.TrimSpace(task.Parameters[harnessParamPrompt]) == "" {
		return fmt.Errorf("harness: task.parameters.%s is required (the task text handed to the harness)",
			harnessParamPrompt)
	}

	if raw, present := task.Parameters[harnessParamCapabilities]; present {
		caps, err := ParseCapabilities(splitCommaList(raw))
		if err != nil {
			return fmt.Errorf("harness: task.parameters.%s: %w", harnessParamCapabilities, err)
		}
		// These claims describe platform-mediated behavior that does not exist
		// for an external process yet. Accepting a self-assertion would make the
		// descriptor say more than Sympozium can enforce or verify.
		if caps.OutputSchema || caps.Subagents || caps.Resume {
			unsupported := Capabilities{
				OutputSchema: caps.OutputSchema,
				Subagents:    caps.Subagents,
				Resume:       caps.Resume,
			}.Names()
			return fmt.Errorf("harness: task.parameters.%s claims unsupported platform-mediated capabilities %v; currently supported declarations: [%s %s]",
				harnessParamCapabilities, unsupported, CapabilityPersona, CapabilityToolFilter)
		}
	}

	if _, err := harnessArgs(task); err != nil {
		return err
	}

	return nil
}

// ConfigureAgentContainer is deliberately a no-op. Every value the harness
// adapter reads is set in OverrideAgentContainer's SetEnv instead, because
// only SetEnv replaces:
//
//   - TASK is already assigned centrally (empty, for object-form tasks), so
//     appending a second entry would leave two contradictory values behind.
//   - HOME and the rest must survive spec.env, which the central build appends
//     *after* this method runs. A user-set HOME would otherwise point the
//     harness at a path it cannot write and fail the run confusingly.
func (h *HarnessHandler) ConfigureAgentContainer(task *sympoziumv1alpha1.TaskSpec, _ *[]corev1.EnvVar) error {
	if task == nil {
		return fmt.Errorf("harness: task is nil")
	}
	return nil
}

// AdjustSidecars returns no adjustments. Harness mode changes the primary
// process, not the SkillPack sidecars: they keep serving exec requests over
// /ipc as they do for any other run.
func (h *HarnessHandler) AdjustSidecars(task *sympoziumv1alpha1.TaskSpec, _ []SidecarContext) ([]SidecarAdjustment, error) {
	if task == nil {
		return nil, fmt.Errorf("harness: task is nil")
	}
	return nil, nil
}

// OverrideAgentContainer replaces the agent container with the harness image.
//
// The image keeps its own ENTRYPOINT: the adapter is the image's business, and
// Sympozium has no argv to impose on a binary it did not build. Only
// parameters.args is passed through.
func (h *HarnessHandler) OverrideAgentContainer(task *sympoziumv1alpha1.TaskSpec) (*AgentContainerOverride, error) {
	if task == nil {
		return nil, fmt.Errorf("harness: task is nil")
	}

	image := strings.TrimSpace(task.Parameters[harnessParamImage])
	if image == "" {
		return nil, fmt.Errorf("harness: task.parameters.%s is required", harnessParamImage)
	}

	args, err := harnessArgs(task)
	if err != nil {
		return nil, err
	}

	homeSizeLimit := resource.MustParse("256Mi")
	return &AgentContainerOverride{
		Image:      image,
		Args:       args,
		WorkingDir: "/workspace",
		SetEnv: []corev1.EnvVar{
			// The task text. Object-form tasks leave the central TASK
			// assignment empty, so this replaces it.
			{Name: "TASK", Value: task.Parameters[harnessParamPrompt]},
			// A writable HOME on a read-only rootfs, backed by the emptyDir
			// below.
			{Name: "HOME", Value: harnessHomePath},
			{Name: "XDG_CONFIG_HOME", Value: harnessHomePath + "/.config"},
			{Name: "XDG_CACHE_HOME", Value: harnessHomePath + "/.cache"},
			// Where the adapter writes the result contract. In env so the
			// path lives in one place rather than in every adapter.
			{Name: "SYMPOZIUM_RESULT_PATH", Value: "/ipc/output/result.json"},
			{Name: "SYMPOZIUM_HARNESS_CONTRACT_VERSION", Value: HarnessContractVersion},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: harnessHomeVolume, MountPath: harnessHomePath},
			// Replaces the central {ipc -> /ipc} mount; see the constants above.
			{
				Name:      harnessIPCVolume,
				MountPath: harnessIPCInputPath,
				SubPath:   harnessIPCInputDir,
				ReadOnly:  true,
			},
			{
				Name:      harnessIPCVolume,
				MountPath: harnessIPCOutPath,
				SubPath:   harnessIPCOutputDir,
			},
		},
		Volumes: []corev1.Volume{{
			Name: harnessHomeVolume,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &homeSizeLimit},
			},
		}},
	}, nil
}

// HarnessImage returns the image a harness-mode task asks for, or "" when the
// task is not harness mode. Admission uses it to run that image through
// SympoziumPolicy.imagePolicy.allowedRegistries — the control an operator uses
// to bound which harnesses may run in the cluster.
func HarnessImage(task *sympoziumv1alpha1.TaskSpec) string {
	if task == nil || task.IsString() || task.GetMode() != Harness {
		return ""
	}
	return strings.TrimSpace(task.Parameters[harnessParamImage])
}

// HarnessImageDigest returns the digest of a digest-pinned harness image, or ""
// when the task is not harness mode or the image carries no digest. The
// controller records this on AgentRun.status.harnessImageDigest so operators can
// see exactly which artifact executed.
func HarnessImageDigest(task *sympoziumv1alpha1.TaskSpec) string {
	if task == nil || task.IsString() || task.GetMode() != Harness {
		return ""
	}
	digest, _ := sympoziumv1alpha1.ParseImageDigest(strings.TrimSpace(task.Parameters[harnessParamImage]))
	return digest
}

// ApplyAgentRuntime fills in a harness task's runtime reference from the
// Agent's spec.runtimeRef when the task names neither an inline image nor a
// runtime. It returns the (possibly unchanged) task. This is what lets a
// channel, schedule, ensemble, API, or UI run inherit the Agent's runtime
// without authoring task.parameters.runtime per run.
func ApplyAgentRuntime(task *sympoziumv1alpha1.TaskSpec, agentRuntimeRef string) *sympoziumv1alpha1.TaskSpec {
	if task == nil || task.IsString() || task.GetMode() != Harness {
		return task
	}
	if strings.TrimSpace(task.Parameters[harnessParamImage]) != "" || strings.TrimSpace(task.Parameters[harnessParamRuntime]) != "" {
		return task
	}
	if strings.TrimSpace(agentRuntimeRef) == "" {
		return task
	}

	cp := *task
	params := make(map[string]string, len(task.Parameters)+1)
	for k, v := range task.Parameters {
		params[k] = v
	}
	params[harnessParamRuntime] = agentRuntimeRef
	cp.Parameters = params
	return &cp
}

// NormalizeHarnessTask resolves a harness task's runtime reference into its
// canonical inline form, so the rest of the pipeline (Validate, image policy,
// OverrideAgentContainer, digest recording) sees only resolved values. When the
// task names parameters.runtime, getRuntime fetches the AgentRuntime, its image
// and capabilities are substituted, and the runtime reference is dropped. A
// runtime that does not exist or is not Ready fails closed here. Non-harness
// tasks, string-form tasks, and inline-image harness tasks are returned
// unchanged.
func NormalizeHarnessTask(namespace string, task *sympoziumv1alpha1.TaskSpec, getRuntime func(namespace, name string) (*sympoziumv1alpha1.AgentRuntime, error)) (*sympoziumv1alpha1.TaskSpec, error) {
	if task == nil || task.IsString() || task.GetMode() != Harness {
		return task, nil
	}
	runtimeName := strings.TrimSpace(task.Parameters[harnessParamRuntime])
	if runtimeName == "" {
		return task, nil
	}

	rt, err := getRuntime(namespace, runtimeName)
	if err != nil {
		return nil, fmt.Errorf("harness: resolving runtime %q: %w", runtimeName, err)
	}
	if !meta.IsStatusConditionTrue(rt.Status.Conditions, sympoziumv1alpha1.AgentRuntimeReadyCondition) {
		return nil, fmt.Errorf("harness: runtime %q is not Ready; fix the AgentRuntime or choose another", runtimeName)
	}

	normalized := *task
	params := make(map[string]string, len(task.Parameters)+2)
	for k, v := range task.Parameters {
		if k == harnessParamRuntime {
			continue
		}
		params[k] = v
	}
	params[harnessParamImage] = rt.Spec.Image
	if len(rt.Spec.Capabilities) > 0 {
		params[harnessParamCapabilities] = strings.Join(rt.Spec.Capabilities, ",")
	}
	normalized.Parameters = params
	return &normalized, nil
}

// harnessArgs parses task.parameters.args, a JSON array of strings appended
// to the harness argv.
func harnessArgs(task *sympoziumv1alpha1.TaskSpec) ([]string, error) {
	raw := strings.TrimSpace(task.Parameters[harnessParamArgs])
	if raw == "" {
		return nil, nil
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, fmt.Errorf("harness: task.parameters.%s must be a JSON array of strings (e.g. '[\"--profile\",\"headless\"]'): %w",
			harnessParamArgs, err)
	}
	return args, nil
}

// splitCommaList splits a comma-separated list, trimming space and dropping
// empty entries.
func splitCommaList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
