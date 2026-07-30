// Package agentedit routes a user-initiated Agent edit to whichever object owns
// the setting.
//
// An Agent stamped out by an Ensemble is a derived artifact: the Ensemble
// controller assigns its whole spec from buildAgent on every reconcile, so an edit
// written straight to the Agent is reverted. Rather than losing the edit, Apply
// writes it to the Ensemble's matching agentConfigs[] entry, from which it flows
// back down to the Agent. Standalone Agents are edited in place as before.
//
// Both the TUI and the agents API go through here, so the routing rule has one
// implementation rather than one per caller.
package agentedit

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// Labels the Ensemble controller stamps on every Agent it generates. Both are
// required to resolve the owning agentConfigs[] entry.
const (
	ensembleLabel    = "sympozium.ai/ensemble"
	agentConfigLabel = "sympozium.ai/agent-config"
)

// Edit is the union of settings the TUI and the agents API can change. A nil
// field means "leave alone"; a non-nil field is applied.
//
// It is deliberately expressed in Agent terms — the shape a user edits — and
// translated to the ensemble's vocabulary by applyToAgentConfig.
type Edit struct {
	Memory      *MemoryEdit
	Skills      *[]sympoziumv1alpha1.SkillRef
	Channels    *[]sympoziumv1alpha1.ChannelSpec
	WebEndpoint *WebEndpointEdit
	Lifecycle   **sympoziumv1alpha1.LifecycleHooks
	Model       *string
	BaseURL     *string
}

// MemoryEdit changes the agent's persistent memory settings.
type MemoryEdit struct {
	Enabled      *bool
	MaxSizeKB    *int
	SystemPrompt *string
}

// WebEndpointEdit changes the agent's web endpoint. Disabled removes it.
type WebEndpointEdit struct {
	Enabled           bool
	Hostname          string
	RequestsPerMinute int
	BurstSize         int
}

// Target reports which object Apply wrote, so a caller can tell the user where
// the change landed.
type Target struct {
	// Kind is "Agent" or "Ensemble".
	Kind string
	// Name is the object written.
	Name string
	// AgentConfig is the agentConfigs[] entry updated, when Kind is "Ensemble".
	AgentConfig string
}

func (t Target) String() string {
	if t.Kind == "Ensemble" {
		return fmt.Sprintf("Ensemble %s (agent config %s)", t.Name, t.AgentConfig)
	}
	return fmt.Sprintf("Agent %s", t.Name)
}

// Apply writes e to the object that owns the setting and reports which that was.
//
// For an Ensemble-managed Agent the edit lands on the Ensemble; the Agent updates
// on the next reconcile. For a standalone Agent it lands on the Agent directly.
//
// An Agent whose ensemble labels point at an Ensemble that no longer exists is
// treated as standalone: the Ensemble controller is not reconciling it, so there
// is nothing to revert the edit.
func Apply(ctx context.Context, c client.Client, agent *sympoziumv1alpha1.Agent, e Edit) (Target, error) {
	ensembleName := agent.Labels[ensembleLabel]
	configName := agent.Labels[agentConfigLabel]

	if ensembleName == "" || configName == "" {
		return applyToAgent(ctx, c, agent, e)
	}

	var pack sympoziumv1alpha1.Ensemble
	if err := c.Get(ctx, types.NamespacedName{Name: ensembleName, Namespace: agent.Namespace}, &pack); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return Target{}, fmt.Errorf("resolving owning ensemble %q: %w", ensembleName, err)
		}
		// Labelled but orphaned — nothing will revert a direct edit.
		return applyToAgent(ctx, c, agent, e)
	}

	idx := -1
	for i := range pack.Spec.AgentConfigs {
		if pack.Spec.AgentConfigs[i].Name == configName {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Target{}, fmt.Errorf(
			"agent %s is labelled as agent config %q of ensemble %s, but that ensemble has no such entry; "+
				"fix the label or add the agent config before editing",
			agent.Name, configName, ensembleName)
	}

	applyToAgentConfig(&pack.Spec.AgentConfigs[idx], e)

	if err := c.Update(ctx, &pack); err != nil {
		return Target{}, fmt.Errorf("updating ensemble %s: %w", ensembleName, err)
	}
	return Target{Kind: "Ensemble", Name: ensembleName, AgentConfig: configName}, nil
}

// applyToAgent writes the edit straight to a standalone Agent.
func applyToAgent(ctx context.Context, c client.Client, agent *sympoziumv1alpha1.Agent, e Edit) (Target, error) {
	if m := e.Memory; m != nil {
		if agent.Spec.Memory == nil {
			agent.Spec.Memory = &sympoziumv1alpha1.MemorySpec{Enabled: true, MaxSizeKB: 256}
		}
		if m.Enabled != nil {
			agent.Spec.Memory.Enabled = *m.Enabled
		}
		if m.MaxSizeKB != nil {
			agent.Spec.Memory.MaxSizeKB = *m.MaxSizeKB
		}
		if m.SystemPrompt != nil {
			agent.Spec.Memory.SystemPrompt = *m.SystemPrompt
		}
	}
	if e.Skills != nil {
		agent.Spec.Skills = *e.Skills
	}
	if e.Channels != nil {
		agent.Spec.Channels = *e.Channels
	}
	if w := e.WebEndpoint; w != nil {
		if !w.Enabled {
			agent.Spec.WebEndpoint = nil
		} else {
			spec := &sympoziumv1alpha1.WebEndpointSpec{Enabled: true, Hostname: w.Hostname}
			if w.RequestsPerMinute > 0 || w.BurstSize > 0 {
				spec.RateLimit = &sympoziumv1alpha1.RateLimitSpec{
					RequestsPerMinute: w.RequestsPerMinute,
					BurstSize:         w.BurstSize,
				}
			}
			agent.Spec.WebEndpoint = spec
		}
	}
	if e.Lifecycle != nil {
		agent.Spec.Agents.Default.Lifecycle = *e.Lifecycle
	}
	if e.Model != nil {
		agent.Spec.Agents.Default.Model = *e.Model
	}
	if e.BaseURL != nil {
		agent.Spec.Agents.Default.BaseURL = *e.BaseURL
	}

	if err := c.Update(ctx, agent); err != nil {
		return Target{}, fmt.Errorf("updating agent %s: %w", agent.Name, err)
	}
	return Target{Kind: "Agent", Name: agent.Name}, nil
}

// applyToAgentConfig translates the edit into the ensemble's vocabulary.
//
// The mapping is not field-for-field: an agent's memory system prompt is the agent
// config's systemPrompt, its skill list is a name list plus a params map, and its
// web endpoint becomes a skill downstream. buildAgent and buildDesiredSkills
// (internal/controller/ensemble_controller.go) are the inverse of this function —
// change one and the other has to follow, which TestAgentUpdateConvergesToCreate
// and the agentedit round-trip tests both cover.
func applyToAgentConfig(cfg *sympoziumv1alpha1.AgentConfigSpec, e Edit) {
	if m := e.Memory; m != nil {
		if cfg.Memory == nil {
			cfg.Memory = &sympoziumv1alpha1.AgentConfigMemory{Enabled: true, MaxSizeKB: 256}
		}
		if m.Enabled != nil {
			cfg.Memory.Enabled = *m.Enabled
		}
		if m.MaxSizeKB != nil {
			cfg.Memory.MaxSizeKB = *m.MaxSizeKB
		}
		// The agent's memory.systemPrompt is stamped from the agent config's
		// systemPrompt, so that is where the edit belongs.
		if m.SystemPrompt != nil {
			cfg.SystemPrompt = *m.SystemPrompt
		}
	}

	if e.Skills != nil {
		names := make([]string, 0, len(*e.Skills))
		var params map[string]map[string]string
		for _, s := range *e.Skills {
			// web-endpoint is derived from cfg.WebEndpoint by buildDesiredSkills,
			// so it is not carried in the skill list.
			if s.SkillPackRef == "web-endpoint" || s.SkillPackRef == "skillpack-web-endpoint" {
				continue
			}
			names = append(names, s.SkillPackRef)
			if len(s.Params) > 0 {
				if params == nil {
					params = map[string]map[string]string{}
				}
				params[s.SkillPackRef] = s.Params
			}
		}
		cfg.Skills = names
		cfg.SkillParams = params
	}

	if e.Channels != nil {
		types := make([]string, 0, len(*e.Channels))
		var configs map[string]string
		for _, ch := range *e.Channels {
			types = append(types, ch.Type)
			if ch.ConfigRef.Secret != "" {
				if configs == nil {
					configs = map[string]string{}
				}
				configs[ch.Type] = ch.ConfigRef.Secret
			}
		}
		cfg.Channels = types
		cfg.ChannelConfigs = configs
	}

	if w := e.WebEndpoint; w != nil {
		if !w.Enabled {
			cfg.WebEndpoint = nil
		} else {
			we := &sympoziumv1alpha1.AgentConfigWebEndpoint{Enabled: true, Hostname: w.Hostname}
			if w.RequestsPerMinute > 0 || w.BurstSize > 0 {
				we.RateLimit = &sympoziumv1alpha1.RateLimitSpec{
					RequestsPerMinute: w.RequestsPerMinute,
					BurstSize:         w.BurstSize,
				}
			}
			cfg.WebEndpoint = we
		}
	}

	if e.Lifecycle != nil {
		cfg.Lifecycle = *e.Lifecycle
	}
	if e.Model != nil {
		cfg.Model = *e.Model
	}
	if e.BaseURL != nil {
		cfg.BaseURL = *e.BaseURL
	}
}
