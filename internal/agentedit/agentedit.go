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
	"reflect"
	"time"

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
//
// AutoStore is a double pointer because the setting is tri-state: true, false,
// or unset. Unset is not "off" — it means inherit, and for an ensemble-managed
// agent that resolves to the Ensemble's spec.autoStoreMemory. A plain *bool
// could not express a return to inheriting, which would leave a user who once
// toggled the value unable to hand it back to the ensemble default. Same idiom
// as Edit.Lifecycle: nil leaves the field alone, a non-nil pointer to nil clears
// it.
type MemoryEdit struct {
	Enabled      *bool
	MaxSizeKB    *int
	AutoStore    **bool
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
// the change landed and whether it has taken effect yet.
type Target struct {
	// Kind is "Agent" or "Ensemble".
	Kind string
	// Name is the object written.
	Name string
	// AgentConfig is the agentConfigs[] entry updated, when Kind is "Ensemble".
	AgentConfig string
	// Changed is false when the edit matched what was already stored and nothing
	// was written.
	Changed bool
	// Observed reports whether the Agent was seen to pick the change up before
	// Apply returned. Always true for a direct Agent write. False for an Ensemble
	// write that did not reconcile within reconcileWait — the caller's next read
	// may still show the previous values.
	Observed bool
}

func (t Target) String() string {
	if t.Kind != "Ensemble" {
		return fmt.Sprintf("Agent %s", t.Name)
	}
	switch {
	case !t.Changed:
		return fmt.Sprintf("Ensemble %s (agent config %s) — no change", t.Name, t.AgentConfig)
	case !t.Observed:
		return fmt.Sprintf("Ensemble %s (agent config %s) — agent still updating", t.Name, t.AgentConfig)
	default:
		return fmt.Sprintf("Ensemble %s (agent config %s)", t.Name, t.AgentConfig)
	}
}

// reconcileWait bounds how long Apply waits for the Ensemble controller to project
// an edit onto the Agent.
//
// Callers re-read the Agent as soon as Apply returns — the TUI refreshes on the
// resulting message, the web UI invalidates its agents query — so returning before
// the controller has acted hands them the pre-edit values. Worse, the TUI seeds its
// edit form from that cache, so a user reopening the form would see stale values and
// could save them back over their own change.
//
// On expiry Apply returns anyway with Observed false: the caller is no worse off
// than it was before this wait existed, and both clients converge on their own
// background poll.
//
// A var rather than a const so tests can shorten it: a fake client has no
// controller, so every managed-agent test would otherwise pay the full wait.
var reconcileWait = 3 * time.Second

// agentPollInterval is how often the wait re-reads the Agent.
var agentPollInterval = 50 * time.Millisecond

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

	target := Target{Kind: "Ensemble", Name: ensembleName, AgentConfig: configName}

	before := pack.Spec.AgentConfigs[idx].DeepCopy()
	applyToAgentConfig(&pack.Spec.AgentConfigs[idx], e)

	// A form re-saved unchanged should not write. Beyond saving a pointless API
	// call, it keeps the wait below honest: with nothing to reconcile there would
	// be no Agent change to observe, and every such save would block for the full
	// timeout.
	if reflect.DeepEqual(before, &pack.Spec.AgentConfigs[idx]) {
		target.Observed = true
		return target, nil
	}
	target.Changed = true

	// Capture before the write: the controller may already be reconciling.
	rvBefore := agent.ResourceVersion

	if err := c.Update(ctx, &pack); err != nil {
		return Target{}, fmt.Errorf("updating ensemble %s: %w", ensembleName, err)
	}

	target.Observed = waitForAgentChange(ctx, c, client.ObjectKeyFromObject(agent), rvBefore)
	return target, nil
}

// waitForAgentChange blocks until the Agent's resourceVersion moves off rvBefore,
// the context is done, or reconcileWait elapses. It reports whether the change was
// observed.
//
// resourceVersion rather than generation: the controller may only reconcile labels,
// which leaves generation untouched. It is a coarse signal — any write to the Agent
// satisfies it — but the only writer of an ensemble-managed Agent is the controller
// projecting this edit.
func waitForAgentChange(ctx context.Context, c client.Client, key client.ObjectKey, rvBefore string) bool {
	ctx, cancel := context.WithTimeout(ctx, reconcileWait)
	defer cancel()

	ticker := time.NewTicker(agentPollInterval)
	defer ticker.Stop()

	for {
		var current sympoziumv1alpha1.Agent
		if err := c.Get(ctx, key, &current); err == nil && current.ResourceVersion != rvBefore {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
	}
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
		if m.AutoStore != nil {
			agent.Spec.Memory.AutoStore = *m.AutoStore
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
	return Target{Kind: "Agent", Name: agent.Name, Changed: true, Observed: true}, nil
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
		// Clearing AutoStore here hands the agent config back to the ensemble's
		// spec.autoStoreMemory rather than to the standalone-Agent default, so
		// the generated Agent comes back carrying the ensemble's value, not nil.
		// resolveAutoStoreMemory (ensemble_controller.go) is the resolution.
		if m.AutoStore != nil {
			cfg.Memory.AutoStore = *m.AutoStore
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
