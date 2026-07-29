// Package controller holds the pod-spec mutators shared by every AgentRun
// execution backend.
//
// An AgentRun runs as either a batchv1.Job (reconcilePending) or an agent-sandbox
// Sandbox CR (reconcilePendingAgentSandbox). Both build their pod via
// buildAgentPodTemplate, which applies the podMutators registry below, so a
// registered mutator applies to both.
package controller

import (
	"context"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// agentContainerName is the name buildContainers gives the agent-runner container.
const agentContainerName = "agent"

// agentContainer returns a pointer to the agent-runner container in podSpec, or
// nil when the pod has none.
//
// Mutators match on container name rather than index: task-mode handlers and
// skill sidecars both append to Containers.
func agentContainer(podSpec *corev1.PodSpec) *corev1.Container {
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == agentContainerName {
			return &podSpec.Containers[i]
		}
	}
	return nil
}

// podMutator is a named pod-spec transformation applied to every agent pod,
// whichever backend ships it.
//
// buildAgentPodTemplate applies the registry below the backend fork, so one entry
// covers both the Job and the Sandbox CR path. TestNoOrphanPodMutators fails if an
// inject* method on *AgentRunReconciler is not listed here.
type podMutator struct {
	name  string
	apply func(*AgentRunReconciler, context.Context, *sympoziumv1alpha1.AgentRun, *corev1.PodSpec)
}

var podMutators = []podMutator{
	{"sharedMemory", (*AgentRunReconciler).injectSharedMemory},
	{"relationshipContext", (*AgentRunReconciler).injectRelationshipContext},
	{"subagentsConfig", (*AgentRunReconciler).injectSubagentsConfig},
}

// applyPodMutators runs every registered mutator against podSpec in registration
// order. Each mutator no-ops when its feature is not configured, and none can
// fail the build.
func (r *AgentRunReconciler) applyPodMutators(
	ctx context.Context,
	agentRun *sympoziumv1alpha1.AgentRun,
	podSpec *corev1.PodSpec,
) {
	for _, m := range podMutators {
		m.apply(r, ctx, agentRun, podSpec)
	}
}

// injectSharedMemory adds WORKFLOW_MEMORY_SERVER_URL, WORKFLOW_MEMORY_ACCESS env vars
// and a wait-for-shared-memory init container to the pod spec if the AgentRun
// belongs to a Ensemble with shared memory enabled.
func (r *AgentRunReconciler) injectSharedMemory(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, podSpec *corev1.PodSpec) {
	packName := agentRun.Labels["sympozium.ai/ensemble"]
	if packName == "" {
		return
	}

	var pack sympoziumv1alpha1.Ensemble
	if err := r.Get(ctx, types.NamespacedName{Name: packName, Namespace: agentRun.Namespace}, &pack); err != nil {
		return
	}
	if pack.Spec.SharedMemory == nil || !pack.Spec.SharedMemory.Enabled {
		return
	}

	sharedMemoryURL := fmt.Sprintf("http://%s-shared-memory.%s.svc:8080", packName, agentRun.Namespace)

	// Resolve access mode for this persona from the instance's label.
	accessMode := "read-write"
	if agentRun.Spec.AgentRef != "" {
		var inst sympoziumv1alpha1.Agent
		if err := r.Get(ctx, types.NamespacedName{Name: agentRun.Spec.AgentRef, Namespace: agentRun.Namespace}, &inst); err == nil {
			personaName := inst.Labels["sympozium.ai/agent-config"]
			for _, rule := range pack.Spec.SharedMemory.AccessRules {
				if rule.AgentConfig == personaName {
					accessMode = rule.Access
					break
				}
			}
		}
	}

	// Inject env vars into the agent container.
	if agent := agentContainer(podSpec); agent != nil {
		agent.Env = append(agent.Env,
			corev1.EnvVar{Name: "WORKFLOW_MEMORY_SERVER_URL", Value: sharedMemoryURL},
			corev1.EnvVar{Name: "WORKFLOW_MEMORY_ACCESS", Value: accessMode},
		)

		// Inject membrane env vars if configured.
		if pack.Spec.SharedMemory.Membrane != nil {
			personaName := ""
			if agentRun.Spec.AgentRef != "" {
				var inst sympoziumv1alpha1.Agent
				if err := r.Get(ctx, types.NamespacedName{Name: agentRun.Spec.AgentRef, Namespace: agentRun.Namespace}, &inst); err == nil {
					personaName = inst.Labels["sympozium.ai/agent-config"]
				}
			}

			// Auto-derive permeability from relationships if not explicitly set.
			membrane := pack.Spec.SharedMemory.Membrane
			if len(membrane.Permeability) == 0 && len(pack.Spec.Relationships) > 0 {
				membrane = membrane.DeepCopy()
				membrane.Permeability = derivePermeability(pack.Spec.AgentConfigs, pack.Spec.Relationships, membrane.DefaultVisibility)
			}

			membraneEnvs := resolveMembraneEnvVars(personaName, membrane, pack.Spec.Relationships)
			agent.Env = append(agent.Env, membraneEnvs...)

			// Inject evidence policy env var if configured.
			if membrane.EvidencePolicy != nil && membrane.EvidencePolicy.MinKind != "" {
				agent.Env = append(agent.Env,
					corev1.EnvVar{Name: "WORKFLOW_MEMBRANE_MIN_EVIDENCE_KIND", Value: membrane.EvidencePolicy.MinKind},
				)
			}
		}
	}

	// Add wait-for-shared-memory init container.
	readOnly := true
	noPrivEsc := false
	podSpec.InitContainers = append(podSpec.InitContainers, corev1.Container{
		Name:            "wait-for-shared-memory",
		Image:           "busybox:1.36",
		ImagePullPolicy: corev1.PullIfNotPresent,
		SecurityContext: &corev1.SecurityContext{
			ReadOnlyRootFilesystem:   &readOnly,
			AllowPrivilegeEscalation: &noPrivEsc,
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"},
			},
		},
		Command: []string{"sh", "-c",
			fmt.Sprintf("elapsed=0; until wget -q --spider --timeout=2 %s/health; do echo 'waiting for shared memory server...'; sleep 2; elapsed=$((elapsed+2)); if [ $elapsed -ge 120 ]; then echo 'ERROR: shared memory server not ready after 120s'; exit 1; fi; done", sharedMemoryURL),
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
		},
	})
}

// injectRelationshipContext serialises the ensemble's relationships and persona
// display names into env vars on the agent container so the agent-runner can
// auto-generate delegation/supervision instructions in the system prompt.
// This ensures user-created dynamic ensembles get correct routing guidance
// without requiring manual system prompt edits.
func (r *AgentRunReconciler) injectRelationshipContext(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, podSpec *corev1.PodSpec) {
	packName := agentRun.Labels["sympozium.ai/ensemble"]
	if packName == "" {
		return
	}

	var pack sympoziumv1alpha1.Ensemble
	if err := r.Get(ctx, types.NamespacedName{Name: packName, Namespace: agentRun.Namespace}, &pack); err != nil {
		return
	}
	if len(pack.Spec.Relationships) == 0 {
		return
	}

	// Resolve the persona name for this agent instance.
	personaName := ""
	if agentRun.Spec.AgentRef != "" {
		var inst sympoziumv1alpha1.Agent
		if err := r.Get(ctx, types.NamespacedName{Name: agentRun.Spec.AgentRef, Namespace: agentRun.Namespace}, &inst); err == nil {
			personaName = inst.Labels["sympozium.ai/agent-config"]
		}
	}
	if personaName == "" {
		return
	}

	// Build a map of persona name → display name for human-readable context.
	displayNames := make(map[string]string, len(pack.Spec.AgentConfigs))
	for _, ac := range pack.Spec.AgentConfigs {
		if ac.DisplayName != "" {
			displayNames[ac.Name] = ac.DisplayName
		}
	}

	// Filter relationships relevant to this persona (as source).
	type relJSON struct {
		Target      string `json:"target"`
		DisplayName string `json:"displayName,omitempty"`
		Type        string `json:"type"`
		Condition   string `json:"condition,omitempty"`
	}
	var rels []relJSON
	for _, rel := range pack.Spec.Relationships {
		if rel.Source != personaName {
			continue
		}
		rels = append(rels, relJSON{
			Target:      rel.Target,
			DisplayName: displayNames[rel.Target],
			Type:        rel.Type,
			Condition:   rel.Condition,
		})
	}
	if len(rels) == 0 {
		return
	}

	data, err := json.Marshal(rels)
	if err != nil {
		return
	}

	if agent := agentContainer(podSpec); agent != nil {
		agent.Env = append(agent.Env,
			corev1.EnvVar{Name: "PERSONA_NAME", Value: personaName},
			corev1.EnvVar{Name: "ENSEMBLE_RELATIONSHIPS", Value: string(data)},
		)
	}
}

// injectSubagentsConfig adds SUBAGENTS_ENABLED, SUBAGENTS_MAX_CHILDREN,
// SUBAGENTS_MAX_CONCURRENT, and SUBAGENTS_MAX_DEPTH env vars to the agent
// container when the "subagents" SkillPack is attached. Limits are taken from
// SubagentsSpec if set, otherwise sensible defaults are used.
func (r *AgentRunReconciler) injectSubagentsConfig(ctx context.Context, agentRun *sympoziumv1alpha1.AgentRun, podSpec *corev1.PodSpec) {
	if agentRun.Spec.AgentRef == "" {
		return
	}

	// Check whether the "subagents" skill is attached to the AgentRun or
	// the backing Agent. The skill attachment is the gate — users control
	// access by adding/removing the SkillPack.
	hasSkill := false
	for _, s := range agentRun.Spec.Skills {
		if s.SkillPackRef == "subagents" {
			hasSkill = true
			break
		}
	}
	if !hasSkill {
		return
	}

	// Look up the Agent for optional limit overrides.
	var inst sympoziumv1alpha1.Agent
	maxChildren, maxConcurrent, maxDepth := 3, 5, 2
	if err := r.Get(ctx, types.NamespacedName{Name: agentRun.Spec.AgentRef, Namespace: agentRun.Namespace}, &inst); err == nil {
		if sub := inst.Spec.Agents.Default.Subagents; sub != nil {
			if sub.MaxChildrenPerAgent > 0 {
				maxChildren = sub.MaxChildrenPerAgent
			}
			if sub.MaxConcurrent > 0 {
				maxConcurrent = sub.MaxConcurrent
			}
			if sub.MaxDepth > 0 {
				maxDepth = sub.MaxDepth
			}
		}
	}

	if agent := agentContainer(podSpec); agent != nil {
		agent.Env = append(agent.Env,
			corev1.EnvVar{Name: "SUBAGENTS_ENABLED", Value: "true"},
			corev1.EnvVar{Name: "SUBAGENTS_MAX_CHILDREN", Value: fmt.Sprintf("%d", maxChildren)},
			corev1.EnvVar{Name: "SUBAGENTS_MAX_CONCURRENT", Value: fmt.Sprintf("%d", maxConcurrent)},
			corev1.EnvVar{Name: "SUBAGENTS_MAX_DEPTH", Value: fmt.Sprintf("%d", maxDepth)},
		)
	}
}
