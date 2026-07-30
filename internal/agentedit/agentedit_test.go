package agentedit

import (
	"context"
	"testing"

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

// ── routing ───────────────────────────────────────────────────────────────────

func TestApply_ManagedAgentWritesEnsemble(t *testing.T) {
	agent, pack := managed()
	c := newClient(t, agent, pack)

	target, err := Apply(context.Background(), c, agent, Edit{
		Memory: &MemoryEdit{Enabled: boolPtr(false), MaxSizeKB: intPtr(1024), SystemPrompt: strPtr("updated")},
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
