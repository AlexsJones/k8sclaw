package controller

import (
	"fmt"
	"log/slog"

	corev1 "k8s.io/api/core/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/controller/taskmodes"
)

// resolveTaskModeAdjustments validates the AgentRun's spec.task field and
// computes the per-sidecar mutations requested by the resolved TaskModeHandler.
//
// Returns (nil, nil) for the trivial cases:
//   - agentRun.Spec.Task == nil (unset; string form by default)
//   - agentRun.Spec.Task is the string form (Path A — the LLM prompt,
//     no orchestration dispatch needed)
//
// For object-form tasks:
//   - Looks up the handler in the taskmodes registry by Mode().
//   - On unknown mode: returns an error naming the supported modes so the
//     reconcile loop can surface it on AgentRun.status.error.
//   - On handler validation failure: returns the handler's error.
//   - On a capability the mode cannot honour: returns an error naming the
//     mode and the capability. The admission webhook checks this first and
//     with a better error surface, but it is a separate, optional
//     deployment — repeating the check here means a cluster without the
//     webhook still fails loud instead of degrading silently.
//   - On success: returns the handler's per-sidecar adjustments.
//
// The function does not mutate any container/env state itself; the caller
// (buildJob → buildContainers) applies the adjustments during render.
//
// Note: ConfigureAgentContainer is intentionally NOT called here — that
// method mutates the agent container's env slice which is built inside
// buildContainers. buildContainers looks up the handler again (a cheap
// map access) and calls ConfigureAgentContainer directly. The two-step
// pattern keeps resolveTaskModeAdjustments pure (no side effects) and
// keeps buildContainers the single owner of container rendering.
func resolveTaskModeAdjustments(
	agentRun *sympoziumv1alpha1.AgentRun,
	sidecars []resolvedSidecar,
) ([]taskmodes.SidecarAdjustment, error) {
	task := agentRun.Spec.Task
	if task == nil || task.IsString() {
		return nil, nil
	}

	mode := task.GetMode()
	handler, ok := taskmodes.Get(mode)
	if !ok {
		return nil, fmt.Errorf("unknown task.mode %q; supported modes: %v", mode, taskmodes.SupportedModes())
	}

	if err := handler.Validate(task); err != nil {
		return nil, fmt.Errorf("task.mode %q validation failed: %w", mode, err)
	}

	if err := taskmodes.ValidateCapabilities(agentRun); err != nil {
		return nil, err
	}
	if err := taskmodes.ValidateRunCompatibility(agentRun); err != nil {
		return nil, err
	}

	contexts := make([]taskmodes.SidecarContext, 0, len(sidecars))
	for _, sc := range sidecars {
		contexts = append(contexts, taskmodes.SidecarContext{
			SkillPackName: sc.skillPackName,
			Sidecar:       sc.sidecar,
			Params:        sc.params,
		})
	}

	adjustments, err := handler.AdjustSidecars(task, contexts)
	if err != nil {
		return nil, fmt.Errorf("task.mode %q sidecar adjustment failed: %w", mode, err)
	}

	return adjustments, nil
}

// applyTaskModeToAgentContainer looks up the TaskModeHandler for the given
// object-form task and calls its ConfigureAgentContainer method to mutate
// the agent container's env in place. For string-form / nil task this is a
// no-op. Returns an error if the mode is unknown or the handler fails —
// buildContainers logs but continues, treating the failure as a soft error
// (the agent-runner will surface the misconfiguration at runtime).
//
// Validation is done by resolveTaskModeAdjustments earlier in the pipeline;
// by the time we get here the handler is known to be valid. The redundant
// lookup is intentional: agentEnv is local to buildContainers so this is
// the simplest place to apply the mutation.
func applyTaskModeToAgentContainer(task *sympoziumv1alpha1.TaskSpec, agentEnv *[]corev1.EnvVar) {
	if task == nil || task.IsString() {
		return
	}
	handler, ok := taskmodes.Get(task.GetMode())
	if !ok {
		// Should not happen — resolveTaskModeAdjustments already rejected
		// this. Log and continue for safety.
		slog.Warn("task-mode handler missing during agent container config",
			"mode", task.GetMode(),
			"supported", taskmodes.SupportedModes(),
		)
		return
	}
	if err := handler.ConfigureAgentContainer(task, agentEnv); err != nil {
		slog.Warn("task-mode ConfigureAgentContainer failed",
			"mode", task.GetMode(),
			"err", err,
		)
	}
}

// applyAgentContainerOverride lets an object-form task mode replace the agent
// container outright — the mechanism behind mode: harness, where an external
// harness image is the pod's primary process instead of agent-runner.
//
// Called last in buildContainers, after every central env assignment, so the
// override's SetEnv wins over the defaults it exists to displace (TASK in
// particular: the central build sets it empty for object-form tasks, and the
// harness needs the task text there).
//
// The image is always a full reference the operator supplied — Sympozium
// builds no harness images — and admission has already run it through
// SympoziumPolicy.imagePolicy.allowedRegistries.
//
// Returns an error rather than warning-and-continuing: unlike
// ConfigureAgentContainer, a failure here means the pod would run the wrong
// binary, so the run must fail with the reason on status.error instead.
func (r *AgentRunReconciler) applyAgentContainerOverride(
	task *sympoziumv1alpha1.TaskSpec,
	agent *corev1.Container,
) error {
	override, err := taskmodes.OverrideFor(task)
	if err != nil {
		return fmt.Errorf("task.mode %q agent container override failed: %w", task.GetMode(), err)
	}
	if override == nil {
		return nil
	}

	if override.Image == "" {
		return fmt.Errorf("task.mode %q returned an agent container override with no image", task.GetMode())
	}
	agent.Image = override.Image

	if len(override.Command) > 0 {
		agent.Command = override.Command
	}
	if len(override.Args) > 0 {
		agent.Args = override.Args
	}
	if override.WorkingDir != "" {
		agent.WorkingDir = override.WorkingDir
	}
	for _, e := range override.SetEnv {
		setEnvVar(&agent.Env, e)
	}
	setVolumeMounts(&agent.VolumeMounts, override.VolumeMounts)

	slog.Info("task-mode: replacing agent container",
		"mode", task.GetMode(),
		"image", agent.Image,
		"argv", append(append([]string{}, agent.Command...), agent.Args...),
	)
	return nil
}

// taskModeReplacesAgentContainer reports whether the task's mode swaps out
// the agent container. Callers use it for the handful of central decisions
// that differ when agent-runner is not the process being configured.
//
// Thin alias: the webhook needs the same answer, so the predicate itself lives
// in taskmodes.
func taskModeReplacesAgentContainer(task *sympoziumv1alpha1.TaskSpec) bool {
	return taskmodes.ReplacesAgentContainer(task)
}

// taskModeAgentVolumes returns the pod volumes an object-form task mode's
// agent container override needs (e.g. the harness's writable HOME). Errors
// are swallowed here and surfaced by applyAgentContainerOverride, which runs
// on the same task in the same reconcile: buildVolumes has no error return,
// and a missing volume only matters when the container that mounts it exists.
func taskModeAgentVolumes(task *sympoziumv1alpha1.TaskSpec) []corev1.Volume {
	override, err := taskmodes.OverrideFor(task)
	if err != nil || override == nil {
		return nil
	}
	return override.Volumes
}

// setEnvVar makes e the container's only entry with that name, appended last.
//
// Every prior entry is dropped rather than the first one overwritten: the
// agent container can already carry two same-named entries by the time this
// runs (the central TASK assignment, plus anything spec.env contributed), and
// replacing only the first would leave the later one winning — Kubernetes
// resolves duplicates by taking the last. Appending last is belt and braces
// on top of that, and safe because no central value references an override's
// name through $(VAR) expansion.
// setVolumeMounts applies an override's mounts, replacing by volume name.
//
// Replacing rather than appending is what lets a mode narrow what its
// container can reach. The central build gives the agent container the whole
// /ipc volume, which is correct for agent-runner — it is Sympozium's own code,
// and the only writer the bridge's eight watched directories expect. A mode
// that runs someone else's binary needs less than that, and the only way to
// give it less is to replace the mount rather than add to it.
//
// All mounts of a replaced volume go, then every override mount is appended:
// one volume may come back as several mounts (different subPaths), which is
// exactly how harness mode narrows /ipc to input and output.
func setVolumeMounts(mounts *[]corev1.VolumeMount, overrides []corev1.VolumeMount) {
	if len(overrides) == 0 {
		return
	}
	replaced := make(map[string]struct{}, len(overrides))
	for _, m := range overrides {
		replaced[m.Name] = struct{}{}
	}
	kept := (*mounts)[:0]
	for _, existing := range *mounts {
		if _, ok := replaced[existing.Name]; !ok {
			kept = append(kept, existing)
		}
	}
	*mounts = append(kept, overrides...)
}

func setEnvVar(env *[]corev1.EnvVar, e corev1.EnvVar) {
	kept := (*env)[:0]
	for _, existing := range *env {
		if existing.Name != e.Name {
			kept = append(kept, existing)
		}
	}
	*env = append(kept, e)
}
