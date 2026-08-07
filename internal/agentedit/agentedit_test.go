package agentedit

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

// managed returns an Agent labelled as belonging to an Ensemble, plus that Ensemble.
func managed() (*sympoziumv1alpha1.Agent, *sympoziumv1alpha1.Ensemble) {
	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "team-analyst",
			Namespace: "default",
			Labels: map[string]string{
				ensembleLabel:    "team",
				agentConfigLabel: "analyst",
			},
		},
	}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "team", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "analyst", SystemPrompt: "original"},
				{Name: "writer"},
			},
		},
	}
	return agent, pack
}

func standalone() *sympoziumv1alpha1.Agent {
	return &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "solo", Namespace: "default"},
	}
}

func boolPtr(b bool) *bool    { return &b }
func intPtr(i int) *int       { return &i }
func strPtr(s string) *string { return &s }

// setBool builds the "set to this value" form of a tri-state edit field;
// clearBool builds the "hand it back to the inherited default" form. Both are
// non-nil, so both are applied — the difference is what they write.
func setBool(b bool) **bool { p := &b; return &p }
func clearBool() **bool     { var p *bool; return &p }

// ── routing ───────────────────────────────────────────────────────────────────

func TestApply_ManagedAgentWritesEnsemble(t *testing.T) {
	shortWait(t)

	agent, pack := managed()
	c := newClient(t, agent, pack)

	target, err := Apply(context.Background(), c, agent, Edit{
		Memory: &MemoryEdit{
			Enabled:      boolPtr(false),
			MaxSizeKB:    intPtr(1024),
			AutoStore:    setBool(false),
			SystemPrompt: strPtr("updated"),
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if target.Kind != "Ensemble" || target.Name != "team" || target.AgentConfig != "analyst" {
		t.Errorf("target = %+v, want Ensemble team/analyst", target)
	}

	var got sympoziumv1alpha1.Ensemble
	if err := c.Get(context.Background(), types.NamespacedName{Name: "team", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get ensemble: %v", err)
	}
	cfg := got.Spec.AgentConfigs[0]
	if cfg.Name != "analyst" {
		t.Fatalf("edited the wrong agent config: %s", cfg.Name)
	}
	if cfg.Memory == nil || cfg.Memory.Enabled || cfg.Memory.MaxSizeKB != 1024 {
		t.Errorf("memory = %+v, want disabled with maxSizeKB 1024", cfg.Memory)
	}
	if cfg.Memory.AutoStore == nil || *cfg.Memory.AutoStore {
		t.Errorf("autoStore = %v, want an explicit false on the agent config", cfg.Memory.AutoStore)
	}
	if cfg.SystemPrompt != "updated" {
		t.Errorf("systemPrompt = %q, want updated", cfg.SystemPrompt)
	}

	// The sibling agent config must be untouched.
	if got.Spec.AgentConfigs[1].Memory != nil {
		t.Error("editing one agent config changed another")
	}

	// And the Agent itself must not have been written — the controller owns it.
	var agentAfter sympoziumv1alpha1.Agent
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(agent), &agentAfter); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if agentAfter.Spec.Memory != nil {
		t.Error("Apply wrote the managed Agent directly; the edit belongs on the Ensemble")
	}
}

// TestApply_MemoryAutoStoreTriState pins the three things a caller can mean when
// it touches autoStore. The middle case is the one a plain *bool cannot express:
// clearing the value hands the setting back to the inherited default instead of
// pinning it to false.
func TestApply_MemoryAutoStoreTriState(t *testing.T) {
	cases := []struct {
		name string
		edit **bool
		want *bool
	}{
		{"set false", setBool(false), boolPtr(false)},
		{"set true", setBool(true), boolPtr(true)},
		{"clear back to inherit", clearBool(), nil},
		{"leave alone", nil, boolPtr(true)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agent := standalone()
			// Start from an explicit true so "leave alone" and "clear" are
			// distinguishable: both would look like nil from a zero value.
			agent.Spec.Memory = &sympoziumv1alpha1.MemorySpec{
				Enabled: true, MaxSizeKB: 256, AutoStore: boolPtr(true),
			}
			c := newClient(t, agent)

			if _, err := Apply(context.Background(), c, agent, Edit{
				Memory: &MemoryEdit{AutoStore: tc.edit},
			}); err != nil {
				t.Fatalf("Apply: %v", err)
			}

			var got sympoziumv1alpha1.Agent
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(agent), &got); err != nil {
				t.Fatalf("get agent: %v", err)
			}
			switch {
			case tc.want == nil && got.Spec.Memory.AutoStore != nil:
				t.Errorf("autoStore = %v, want nil (inherit)", *got.Spec.Memory.AutoStore)
			case tc.want != nil && got.Spec.Memory.AutoStore == nil:
				t.Errorf("autoStore = nil, want %v", *tc.want)
			case tc.want != nil && *got.Spec.Memory.AutoStore != *tc.want:
				t.Errorf("autoStore = %v, want %v", *got.Spec.Memory.AutoStore, *tc.want)
			}

			// The neighbouring memory fields must survive an autoStore-only edit.
			if !got.Spec.Memory.Enabled || got.Spec.Memory.MaxSizeKB != 256 {
				t.Errorf("autoStore edit disturbed the rest of memory: %+v", got.Spec.Memory)
			}
		})
	}
}

func TestApply_StandaloneAgentWritesAgent(t *testing.T) {
	agent := standalone()
	c := newClient(t, agent)

	target, err := Apply(context.Background(), c, agent, Edit{
		Memory: &MemoryEdit{Enabled: boolPtr(false)},
		Model:  strPtr("gpt-4o-mini"),
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if target.Kind != "Agent" || target.Name != "solo" {
		t.Errorf("target = %+v, want Agent solo", target)
	}

	var got sympoziumv1alpha1.Agent
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(agent), &got); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if got.Spec.Memory == nil || got.Spec.Memory.Enabled {
		t.Errorf("memory = %+v, want disabled", got.Spec.Memory)
	}
	if got.Spec.Agents.Default.Model != "gpt-4o-mini" {
		t.Errorf("model = %q, want gpt-4o-mini", got.Spec.Agents.Default.Model)
	}
}

// An Agent labelled for an Ensemble that no longer exists is edited directly:
// nothing is reconciling it, so there is nothing to revert the edit.
func TestApply_OrphanedLabelsWriteAgent(t *testing.T) {
	agent, _ := managed()
	c := newClient(t, agent) // ensemble deliberately absent

	target, err := Apply(context.Background(), c, agent, Edit{Model: strPtr("gpt-4o")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if target.Kind != "Agent" {
		t.Errorf("target = %+v, want Agent (owning ensemble is gone)", target)
	}
}

// A label pointing at an agent config the Ensemble does not define is a genuine
// inconsistency: writing either object would lose the edit, so refuse.
func TestApply_UnknownAgentConfigErrors(t *testing.T) {
	agent, pack := managed()
	agent.Labels[agentConfigLabel] = "ghost"
	c := newClient(t, agent, pack)

	if _, err := Apply(context.Background(), c, agent, Edit{Model: strPtr("gpt-4o")}); err == nil {
		t.Fatal("expected an error when the agent config does not exist on the ensemble")
	}
}

// ── translation into the ensemble's vocabulary ────────────────────────────────

func TestApplyToAgentConfig_SkillsSplitIntoNamesAndParams(t *testing.T) {
	cfg := &sympoziumv1alpha1.AgentConfigSpec{Name: "analyst"}

	skills := []sympoziumv1alpha1.SkillRef{
		{SkillPackRef: "memory"},
		{SkillPackRef: "github-gitops", Params: map[string]string{"repo": "acme/infra"}},
		// Derived from cfg.WebEndpoint downstream, so it must not enter the list.
		{SkillPackRef: "web-endpoint", Params: map[string]string{"hostname": "x"}},
	}
	applyToAgentConfig(cfg, Edit{Skills: &skills})

	if len(cfg.Skills) != 2 || cfg.Skills[0] != "memory" || cfg.Skills[1] != "github-gitops" {
		t.Errorf("skills = %v, want [memory github-gitops] with web-endpoint dropped", cfg.Skills)
	}
	if cfg.SkillParams["github-gitops"]["repo"] != "acme/infra" {
		t.Errorf("skillParams = %v, want repo acme/infra", cfg.SkillParams)
	}
	if _, ok := cfg.SkillParams["web-endpoint"]; ok {
		t.Error("web-endpoint params leaked into skillParams; they are derived from webEndpoint")
	}
}

func TestApplyToAgentConfig_ChannelsSplitIntoTypesAndConfigs(t *testing.T) {
	cfg := &sympoziumv1alpha1.AgentConfigSpec{Name: "analyst"}

	channels := []sympoziumv1alpha1.ChannelSpec{
		{Type: "slack", ConfigRef: sympoziumv1alpha1.SecretRef{Secret: "slack-token"}},
		{Type: "telegram"},
	}
	applyToAgentConfig(cfg, Edit{Channels: &channels})

	if len(cfg.Channels) != 2 || cfg.Channels[0] != "slack" {
		t.Errorf("channels = %v, want [slack telegram]", cfg.Channels)
	}
	if cfg.ChannelConfigs["slack"] != "slack-token" {
		t.Errorf("channelConfigs = %v, want slack -> slack-token", cfg.ChannelConfigs)
	}
	if _, ok := cfg.ChannelConfigs["telegram"]; ok {
		t.Error("telegram has no secret; it should not appear in channelConfigs")
	}
}

func TestApplyToAgentConfig_WebEndpointCarriesRateLimit(t *testing.T) {
	cfg := &sympoziumv1alpha1.AgentConfigSpec{Name: "analyst"}

	applyToAgentConfig(cfg, Edit{WebEndpoint: &WebEndpointEdit{
		Enabled: true, Hostname: "a.example.test", RequestsPerMinute: 120, BurstSize: 20,
	}})

	if cfg.WebEndpoint == nil || !cfg.WebEndpoint.Enabled {
		t.Fatalf("webEndpoint = %+v, want enabled", cfg.WebEndpoint)
	}
	if cfg.WebEndpoint.RateLimit == nil || cfg.WebEndpoint.RateLimit.RequestsPerMinute != 120 {
		t.Errorf("rateLimit = %+v, want 120 rpm", cfg.WebEndpoint.RateLimit)
	}

	applyToAgentConfig(cfg, Edit{WebEndpoint: &WebEndpointEdit{Enabled: false}})
	if cfg.WebEndpoint != nil {
		t.Errorf("webEndpoint = %+v, want nil after disabling", cfg.WebEndpoint)
	}
}

// A nil field means "leave alone" — a partial edit must not blank the rest.
func TestApply_NilFieldsAreLeftAlone(t *testing.T) {
	shortWait(t)

	agent, pack := managed()
	pack.Spec.AgentConfigs[0].Model = "gpt-4o"
	pack.Spec.AgentConfigs[0].Skills = []string{"memory"}
	c := newClient(t, agent, pack)

	if _, err := Apply(context.Background(), c, agent, Edit{BaseURL: strPtr("http://local:8080/v1")}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var got sympoziumv1alpha1.Ensemble
	if err := c.Get(context.Background(), types.NamespacedName{Name: "team", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get ensemble: %v", err)
	}
	cfg := got.Spec.AgentConfigs[0]
	if cfg.BaseURL != "http://local:8080/v1" {
		t.Errorf("baseURL = %q, want the edited value", cfg.BaseURL)
	}
	if cfg.Model != "gpt-4o" || len(cfg.Skills) != 1 {
		t.Errorf("untouched fields were cleared: model=%q skills=%v", cfg.Model, cfg.Skills)
	}
}

// ── no-op detection and the reconcile wait ────────────────────────────────────

// shortWait shrinks the reconcile wait for tests. A fake client has no controller,
// so any test that writes the Ensemble would otherwise block for the full timeout.
func shortWait(t *testing.T) {
	t.Helper()
	prevWait, prevPoll := reconcileWait, agentPollInterval
	reconcileWait, agentPollInterval = 150*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { reconcileWait, agentPollInterval = prevWait, prevPoll })
}

// Re-saving a form unchanged must not write. Beyond the wasted API call, a write
// with nothing to reconcile would block for the full wait every time.
func TestApply_NoOpEditDoesNotWriteEnsemble(t *testing.T) {
	shortWait(t)

	agent, pack := managed()
	pack.Spec.AgentConfigs[0].Model = "gpt-4o"
	c := newClient(t, agent, pack)

	var before sympoziumv1alpha1.Ensemble
	if err := c.Get(context.Background(), types.NamespacedName{Name: "team", Namespace: "default"}, &before); err != nil {
		t.Fatalf("get ensemble: %v", err)
	}

	// The same value that is already stored.
	target, err := Apply(context.Background(), c, agent, Edit{Model: strPtr("gpt-4o")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if target.Changed {
		t.Error("Target.Changed = true for an edit that matched what was stored")
	}
	if !target.Observed {
		t.Error("Target.Observed = false; nothing was written, so there is nothing to wait for")
	}

	var after sympoziumv1alpha1.Ensemble
	if err := c.Get(context.Background(), types.NamespacedName{Name: "team", Namespace: "default"}, &after); err != nil {
		t.Fatalf("get ensemble: %v", err)
	}
	if after.ResourceVersion != before.ResourceVersion {
		t.Errorf("ensemble was written for a no-op edit (resourceVersion %s → %s)",
			before.ResourceVersion, after.ResourceVersion)
	}
}

// A real edit writes, and reports itself as changed.
func TestApply_RealEditReportsChanged(t *testing.T) {
	shortWait(t)

	agent, pack := managed()
	c := newClient(t, agent, pack)

	target, err := Apply(context.Background(), c, agent, Edit{Model: strPtr("gpt-4o-mini")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !target.Changed {
		t.Error("Target.Changed = false for an edit that altered the agent config")
	}
}

// With no controller to project the edit, the wait expires and says so rather than
// claiming the Agent is up to date.
func TestApply_WaitExpiresWhenNothingReconciles(t *testing.T) {
	shortWait(t)

	agent, pack := managed()
	c := newClient(t, agent, pack)

	start := time.Now()
	target, err := Apply(context.Background(), c, agent, Edit{Model: strPtr("gpt-4o-mini")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if target.Observed {
		t.Error("Target.Observed = true, but no controller updated the Agent")
	}
	if elapsed := time.Since(start); elapsed < reconcileWait {
		t.Errorf("returned after %v, before the %v deadline — the wait is not running", elapsed, reconcileWait)
	}
	if !strings.Contains(target.String(), "still updating") {
		t.Errorf("Target.String() = %q, want it to say the agent is still updating", target)
	}
}

// Once something updates the Agent — as the controller would — the wait returns
// promptly rather than sitting out the deadline.
func TestApply_WaitReturnsOnceAgentChanges(t *testing.T) {
	shortWait(t)
	reconcileWait = 5 * time.Second // long enough that returning early is meaningful

	agent, pack := managed()
	c := newClient(t, agent, pack)

	// Stand in for the controller projecting the edit onto the Agent.
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(30 * time.Millisecond)
		var current sympoziumv1alpha1.Agent
		if err := c.Get(context.Background(), client.ObjectKeyFromObject(agent), &current); err != nil {
			return
		}
		current.Spec.Agents.Default.Model = "gpt-4o-mini"
		_ = c.Update(context.Background(), &current)
	}()

	start := time.Now()
	target, err := Apply(context.Background(), c, agent, Edit{Model: strPtr("gpt-4o-mini")})
	<-done
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !target.Observed {
		t.Error("Target.Observed = false, but the Agent was updated during the wait")
	}
	if elapsed := time.Since(start); elapsed >= reconcileWait {
		t.Errorf("waited %v — should have returned as soon as the Agent changed", elapsed)
	}
}

// A cancelled request must not keep polling.
func TestApply_WaitHonoursCancelledContext(t *testing.T) {
	shortWait(t)
	reconcileWait = 10 * time.Second // so only cancellation can end the wait quickly

	agent, pack := managed()
	c := newClient(t, agent, pack)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	target, err := Apply(ctx, c, agent, Edit{Model: strPtr("gpt-4o-mini")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if target.Observed {
		t.Error("Target.Observed = true after the context was cancelled")
	}
	if elapsed := time.Since(start); elapsed >= reconcileWait {
		t.Errorf("waited %v despite cancellation", elapsed)
	}
}

// A standalone Agent is written directly, so it is current the moment Apply returns.
func TestApply_StandaloneIsImmediatelyObserved(t *testing.T) {
	shortWait(t)

	agent := standalone()
	c := newClient(t, agent)

	target, err := Apply(context.Background(), c, agent, Edit{Model: strPtr("gpt-4o")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !target.Changed || !target.Observed {
		t.Errorf("target = %+v, want changed and observed for a direct Agent write", target)
	}
}
