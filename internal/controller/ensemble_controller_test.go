package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBuildInstance_ChannelAccessControlPrecedence(t *testing.T) {
	tests := []struct {
		name           string
		packAC         map[string]*sympoziumv1alpha1.ChannelAccessControl
		personaAC      map[string]*sympoziumv1alpha1.ChannelAccessControl
		wantAllowChats []string
	}{
		{
			name: "persona override wins over ensemble-level",
			packAC: map[string]*sympoziumv1alpha1.ChannelAccessControl{
				"discord": {AllowedChats: []string{"ensemble-channel"}},
			},
			personaAC: map[string]*sympoziumv1alpha1.ChannelAccessControl{
				"discord": {AllowedChats: []string{"persona-channel"}},
			},
			wantAllowChats: []string{"persona-channel"},
		},
		{
			name: "fallback to ensemble-level when persona has none",
			packAC: map[string]*sympoziumv1alpha1.ChannelAccessControl{
				"discord": {AllowedChats: []string{"ensemble-channel"}},
			},
			personaAC:      nil,
			wantAllowChats: []string{"ensemble-channel"},
		},
		{
			name:           "no access control at either level",
			packAC:         nil,
			personaAC:      nil,
			wantAllowChats: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &EnsembleReconciler{}
			pack := &sympoziumv1alpha1.Ensemble{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pack",
					Namespace: "default",
				},
				Spec: sympoziumv1alpha1.EnsembleSpec{
					ChannelConfigs:       map[string]string{"discord": "my-discord-secret"},
					ChannelAccessControl: tt.packAC,
				},
			}
			persona := &sympoziumv1alpha1.AgentConfigSpec{
				Name:                 "tech-lead",
				SystemPrompt:         "You are a tech lead.",
				Channels:             []string{"discord"},
				ChannelAccessControl: tt.personaAC,
			}

			inst := r.buildAgent(pack, persona, "test-pack-tech-lead", "")

			if len(inst.Spec.Channels) != 1 {
				t.Fatalf("expected 1 channel, got %d", len(inst.Spec.Channels))
			}
			ch := inst.Spec.Channels[0]
			if ch.Type != "discord" {
				t.Fatalf("expected channel type discord, got %s", ch.Type)
			}

			if tt.wantAllowChats == nil {
				if ch.AccessControl != nil {
					t.Errorf("expected nil AccessControl, got %+v", ch.AccessControl)
				}
				return
			}

			if ch.AccessControl == nil {
				t.Fatal("expected non-nil AccessControl")
			}
			if len(ch.AccessControl.AllowedChats) != len(tt.wantAllowChats) {
				t.Fatalf("AllowedChats = %v, want %v", ch.AccessControl.AllowedChats, tt.wantAllowChats)
			}
			for i, want := range tt.wantAllowChats {
				if ch.AccessControl.AllowedChats[i] != want {
					t.Errorf("AllowedChats[%d] = %q, want %q", i, ch.AccessControl.AllowedChats[i], want)
				}
			}
		})
	}
}

func TestIsSequentialSuccessor(t *testing.T) {
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "ops-check", Target: "incident-responder", Type: "stimulus"},
		{Source: "incident-responder", Target: "cost-analyzer", Type: "sequential"},
	}
	tests := []struct {
		name         string
		workflowType string
		persona      string
		want         bool
	}{
		{"sequential target in pipeline is suppressed", "pipeline", "cost-analyzer", true},
		{"sequential source (pipeline head) keeps its schedule", "pipeline", "incident-responder", false},
		{"stimulus target is not a sequential successor", "pipeline", "incident-responder", false},
		// A sequential edge means the same thing in every workflow type: the
		// target runs when its predecessor finishes. Suppressing only under
		// "pipeline" left delegation ensembles stamping a schedule for a
		// sequential target, which then fired at t=0 — before the predecessor
		// it exists to react to had produced anything.
		{"sequential target in delegation mode is suppressed", "delegation", "cost-analyzer", true},
		{"sequential target in autonomous mode is suppressed", "autonomous", "cost-analyzer", true},
		{"empty workflowType still suppresses a sequential target", "", "cost-analyzer", true},
		{"unrelated persona is not suppressed", "pipeline", "some-other-agent", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pack := &sympoziumv1alpha1.Ensemble{
				Spec: sympoziumv1alpha1.EnsembleSpec{
					WorkflowType:  tt.workflowType,
					Relationships: rels,
				},
			}
			if got := isSequentialSuccessor(pack, tt.persona); got != tt.want {
				t.Errorf("isSequentialSuccessor(%q, %q) = %v, want %v", tt.workflowType, tt.persona, got, tt.want)
			}
		})
	}
}

func TestBuildInstance_SubagentsPropagated(t *testing.T) {
	r := &EnsembleReconciler{}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pack",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.EnsembleSpec{},
	}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Name:         "lead-analyst",
		DisplayName:  "Lead Analyst",
		SystemPrompt: "You are the lead analyst.",
		Subagents: &sympoziumv1alpha1.SubagentsSpec{
			MaxDepth:            3,
			MaxConcurrent:       8,
			MaxChildrenPerAgent: 5,
		},
	}

	inst := r.buildAgent(pack, persona, "test-pack-lead-analyst", "")

	if inst.Spec.DisplayName != "Lead Analyst" {
		t.Errorf("Agent DisplayName = %q, want %q (should carry the agentConfig displayName)", inst.Spec.DisplayName, "Lead Analyst")
	}

	sub := inst.Spec.Agents.Default.Subagents
	if sub == nil {
		t.Fatal("expected Subagents to be propagated, got nil")
	}
	if sub.MaxDepth != 3 {
		t.Errorf("MaxDepth = %d, want 3", sub.MaxDepth)
	}
	if sub.MaxConcurrent != 8 {
		t.Errorf("MaxConcurrent = %d, want 8", sub.MaxConcurrent)
	}
	if sub.MaxChildrenPerAgent != 5 {
		t.Errorf("MaxChildrenPerAgent = %d, want 5", sub.MaxChildrenPerAgent)
	}
}

func TestBuildInstance_SubagentsNilWhenUnset(t *testing.T) {
	r := &EnsembleReconciler{}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pack",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.EnsembleSpec{},
	}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Name:         "worker",
		SystemPrompt: "You are a worker.",
	}

	inst := r.buildAgent(pack, persona, "test-pack-worker", "")

	if inst.Spec.Agents.Default.Subagents != nil {
		t.Errorf("expected Subagents to be nil for persona without subagents config, got %+v",
			inst.Spec.Agents.Default.Subagents)
	}
}

func TestReconcileAgentConfig_UpdatesSubagentsOnExistingAgent(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	existing := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pack-coordinator",
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/agent-config": "coordinator",
			},
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{},
			},
		},
	}

	r := &EnsembleReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build(),
		Scheme: scheme,
	}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pack", Namespace: "default"},
	}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Name: "coordinator",
		Subagents: &sympoziumv1alpha1.SubagentsSpec{
			MaxDepth:            1,
			MaxConcurrent:       8,
			MaxChildrenPerAgent: 8,
		},
	}

	_, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), pack, persona, 0, "")
	if err != nil {
		t.Fatalf("reconcileAgentConfig returned error: %v", err)
	}

	var updated sympoziumv1alpha1.Agent
	if err := r.Get(context.Background(), client.ObjectKey{Name: existing.Name, Namespace: existing.Namespace}, &updated); err != nil {
		t.Fatalf("get updated agent: %v", err)
	}
	sub := updated.Spec.Agents.Default.Subagents
	if sub == nil {
		t.Fatal("expected existing Agent to receive subagents config")
	}
	if sub.MaxDepth != 1 || sub.MaxConcurrent != 8 || sub.MaxChildrenPerAgent != 8 {
		t.Fatalf("subagents = %+v, want maxDepth=1 maxConcurrent=8 maxChildrenPerAgent=8", sub)
	}
}

// ── resolveAuthRefs / resolveModel unit tests ────────────────────────────────

func TestResolveAuthRefs_FiltersOnProvider(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		Spec: sympoziumv1alpha1.EnsembleSpec{
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "openai", Secret: "openai-key"},
				{Provider: "anthropic", Secret: "anthropic-key"},
			},
		},
	}

	t.Run("filters to matching provider", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{Provider: "anthropic"}
		got := resolveAuthRefs(pack, persona, "")
		if len(got) != 1 || got[0].Secret != "anthropic-key" {
			t.Fatalf("got %+v, want [anthropic-key]", got)
		}
	})

	t.Run("returns all when no provider set", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{}
		got := resolveAuthRefs(pack, persona, "")
		if len(got) != 2 {
			t.Fatalf("got %d refs, want 2", len(got))
		}
	})

	t.Run("returns nil for local model endpoint with no provider", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{}
		got := resolveAuthRefs(pack, persona, "http://local:8080")
		if got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
	})

	t.Run("mixed ensemble: provider-pinned persona keeps its key under a local endpoint", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{Provider: "openai"}
		got := resolveAuthRefs(pack, persona, "http://local:8080")
		if len(got) != 1 || got[0].Secret != "openai-key" {
			t.Fatalf("got %+v, want [openai-key] (mixed ensemble)", got)
		}
	})

	t.Run("falls back to all refs when provider has no match", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{Provider: "azure-openai"}
		got := resolveAuthRefs(pack, persona, "")
		if len(got) != 2 {
			t.Fatalf("got %d refs, want 2 (fallback to all)", len(got))
		}
	})
}

func TestResolveModel_Precedence(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		Spec: sympoziumv1alpha1.EnsembleSpec{
			ModelRef: "local-llm",
		},
	}

	t.Run("persona model wins", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{Model: "gpt-5-mini"}
		if got := resolveModel(pack, persona, ""); got != "gpt-5-mini" {
			t.Fatalf("got %q, want gpt-5-mini", got)
		}
	})

	t.Run("defaults to gpt-4o when empty", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{}
		if got := resolveModel(pack, persona, ""); got != "gpt-4o" {
			t.Fatalf("got %q, want gpt-4o", got)
		}
	})

	t.Run("local endpoint overrides to modelRef", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{Model: "gpt-5-mini"}
		if got := resolveModel(pack, persona, "http://local:8080"); got != "local-llm" {
			t.Fatalf("got %q, want local-llm", got)
		}
	})

	t.Run("preserves default when modelRef is empty with local endpoint", func(t *testing.T) {
		emptyRefPack := &sympoziumv1alpha1.Ensemble{Spec: sympoziumv1alpha1.EnsembleSpec{}}
		persona := &sympoziumv1alpha1.AgentConfigSpec{}
		if got := resolveModel(emptyRefPack, persona, "http://local:8080"); got != "gpt-4o" {
			t.Fatalf("got %q, want gpt-4o (should not overwrite with empty modelRef)", got)
		}
	})

	t.Run("mixed ensemble: provider-pinned persona keeps its model under a local endpoint", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{Provider: "openai", Model: "gpt-5-mini"}
		if got := resolveModel(pack, persona, "http://local:8080"); got != "gpt-5-mini" {
			t.Fatalf("got %q, want gpt-5-mini (mixed ensemble keeps persona model)", got)
		}
	})
}

func TestResolveAutoStoreMemory_Precedence(t *testing.T) {
	tr := func() *bool { b := true; return &b }
	fa := func() *bool { b := false; return &b }

	t.Run("persona override wins over ensemble default", func(t *testing.T) {
		pack := &sympoziumv1alpha1.Ensemble{Spec: sympoziumv1alpha1.EnsembleSpec{AutoStoreMemory: tr()}}
		persona := &sympoziumv1alpha1.AgentConfigSpec{Memory: &sympoziumv1alpha1.AgentConfigMemory{AutoStore: fa()}}
		got := resolveAutoStoreMemory(pack, persona)
		if got == nil || *got != false {
			t.Fatalf("got %v, want false", got)
		}
	})

	t.Run("ensemble default applies when persona unset", func(t *testing.T) {
		pack := &sympoziumv1alpha1.Ensemble{Spec: sympoziumv1alpha1.EnsembleSpec{AutoStoreMemory: fa()}}
		persona := &sympoziumv1alpha1.AgentConfigSpec{}
		got := resolveAutoStoreMemory(pack, persona)
		if got == nil || *got != false {
			t.Fatalf("got %v, want false", got)
		}
	})

	t.Run("persona memory present but AutoStore nil falls through to ensemble", func(t *testing.T) {
		pack := &sympoziumv1alpha1.Ensemble{Spec: sympoziumv1alpha1.EnsembleSpec{AutoStoreMemory: fa()}}
		persona := &sympoziumv1alpha1.AgentConfigSpec{Memory: &sympoziumv1alpha1.AgentConfigMemory{Enabled: true}}
		got := resolveAutoStoreMemory(pack, persona)
		if got == nil || *got != false {
			t.Fatalf("got %v, want false", got)
		}
	})

	t.Run("nil at both levels leaves field unset (Agent default)", func(t *testing.T) {
		pack := &sympoziumv1alpha1.Ensemble{Spec: sympoziumv1alpha1.EnsembleSpec{}}
		persona := &sympoziumv1alpha1.AgentConfigSpec{}
		if got := resolveAutoStoreMemory(pack, persona); got != nil {
			t.Fatalf("got %v, want nil", got)
		}
	})
}

func TestResolveBaseURL_Precedence(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		Spec: sympoziumv1alpha1.EnsembleSpec{BaseURL: "https://pack.example/v1"},
	}

	t.Run("ensemble base url by default", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{}
		if got := resolveBaseURL(pack, persona, ""); got != "https://pack.example/v1" {
			t.Fatalf("got %q, want ensemble base url", got)
		}
	})

	t.Run("local endpoint applies when no provider pinned", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{}
		if got := resolveBaseURL(pack, persona, "http://local:8080"); got != "http://local:8080" {
			t.Fatalf("got %q, want local endpoint", got)
		}
	})

	t.Run("persona base url always wins", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{BaseURL: "https://persona.example/v1"}
		if got := resolveBaseURL(pack, persona, "http://local:8080"); got != "https://persona.example/v1" {
			t.Fatalf("got %q, want persona base url", got)
		}
	})

	t.Run("mixed ensemble: provider-pinned persona ignores the local endpoint", func(t *testing.T) {
		persona := &sympoziumv1alpha1.AgentConfigSpec{Provider: "openai"}
		if got := resolveBaseURL(pack, persona, "http://local:8080"); got != "https://pack.example/v1" {
			t.Fatalf("got %q, want ensemble base url (not the local endpoint)", got)
		}
	})
}

// ── Update path propagation tests ────────────────────────────────────────────

func TestReconcileAgentConfig_UpdatePropagatesAuthRefsFiltered(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	existing := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pack-enrichment",
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/agent-config": "enrichment",
				"sympozium.ai/provider":     "openai",
			},
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "openai", Secret: "openai-key"},
				{Provider: "anthropic", Secret: "anthropic-key"},
			},
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{Model: "gpt-5-mini"},
			},
		},
	}

	r := &EnsembleReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build(),
		Scheme: scheme,
	}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "openai", Secret: "openai-key"},
				{Provider: "anthropic", Secret: "anthropic-key"},
			},
		},
	}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Name:     "enrichment",
		Provider: "openai",
		Model:    "gpt-5-mini",
	}

	if _, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), pack, persona, 0, ""); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}

	var updated sympoziumv1alpha1.Agent
	if err := r.Get(context.Background(), client.ObjectKey{Name: existing.Name, Namespace: existing.Namespace}, &updated); err != nil {
		t.Fatalf("get updated agent: %v", err)
	}
	if len(updated.Spec.AuthRefs) != 1 {
		t.Fatalf("expected 1 authRef (filtered to openai), got %d: %+v", len(updated.Spec.AuthRefs), updated.Spec.AuthRefs)
	}
	if updated.Spec.AuthRefs[0].Secret != "openai-key" {
		t.Fatalf("expected openai-key, got %s", updated.Spec.AuthRefs[0].Secret)
	}
}

func TestReconcileAgentConfig_UpdatePropagatesVolumesAndMounts(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	existing := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pack-worker",
			Namespace: "default",
			Labels:    map[string]string{"sympozium.ai/agent-config": "worker"},
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{Default: sympoziumv1alpha1.AgentConfig{}},
		},
	}

	wantVolumes := []corev1.Volume{{Name: "logs", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}}
	wantMounts := []corev1.VolumeMount{{Name: "logs", MountPath: "/var/log"}}

	r := &EnsembleReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build(),
		Scheme: scheme,
	}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Volumes:      wantVolumes,
			VolumeMounts: wantMounts,
		},
	}
	persona := &sympoziumv1alpha1.AgentConfigSpec{Name: "worker"}

	if _, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), pack, persona, 0, ""); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}

	var updated sympoziumv1alpha1.Agent
	if err := r.Get(context.Background(), client.ObjectKey{Name: existing.Name, Namespace: existing.Namespace}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(updated.Spec.Volumes) != 1 || updated.Spec.Volumes[0].Name != "logs" {
		t.Fatalf("volumes not propagated: %+v", updated.Spec.Volumes)
	}
	if len(updated.Spec.VolumeMounts) != 1 || updated.Spec.VolumeMounts[0].MountPath != "/var/log" {
		t.Fatalf("volumeMounts not propagated: %+v", updated.Spec.VolumeMounts)
	}
}

func TestReconcileAgentConfig_UpdatePropagatesSandboxLifecyclePolicy(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	existing := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pack-worker",
			Namespace: "default",
			Labels:    map[string]string{"sympozium.ai/agent-config": "worker"},
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{Default: sympoziumv1alpha1.AgentConfig{}},
		},
	}

	r := &EnsembleReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build(),
		Scheme: scheme,
	}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			AgentSandbox: &sympoziumv1alpha1.AgentSandboxDefaults{
				Enabled:      true,
				RuntimeClass: "gvisor",
			},
			PolicyRef: "compliance-policy",
		},
	}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Name: "worker",
		Lifecycle: &sympoziumv1alpha1.LifecycleHooks{
			PreRun: []sympoziumv1alpha1.LifecycleHookContainer{
				{Name: "setup"},
			},
		},
	}

	if _, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), pack, persona, 0, ""); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}

	var updated sympoziumv1alpha1.Agent
	if err := r.Get(context.Background(), client.ObjectKey{Name: existing.Name, Namespace: existing.Namespace}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Spec.Agents.Default.AgentSandbox == nil || !updated.Spec.Agents.Default.AgentSandbox.Enabled {
		t.Fatal("agentSandbox not propagated")
	}
	if updated.Spec.Agents.Default.AgentSandbox.RuntimeClass != "gvisor" {
		t.Fatalf("runtimeClass = %q, want gvisor", updated.Spec.Agents.Default.AgentSandbox.RuntimeClass)
	}
	if updated.Spec.Agents.Default.Lifecycle == nil || len(updated.Spec.Agents.Default.Lifecycle.PreRun) != 1 {
		t.Fatal("lifecycle not propagated")
	}
	if updated.Spec.PolicyRef != "compliance-policy" {
		t.Fatalf("policyRef = %q, want compliance-policy", updated.Spec.PolicyRef)
	}
}

func TestReconcileAgentConfig_UpdatePropagatesModelForLocalEndpoint(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	existing := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pack-worker",
			Namespace: "default",
			Labels:    map[string]string{"sympozium.ai/agent-config": "worker"},
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			AuthRefs: []sympoziumv1alpha1.SecretRef{{Provider: "openai", Secret: "openai-key"}},
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{Model: "gpt-4o"},
			},
		},
	}

	r := &EnsembleReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build(),
		Scheme: scheme,
	}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			ModelRef: "local-llama",
			AuthRefs: []sympoziumv1alpha1.SecretRef{{Provider: "openai", Secret: "openai-key"}},
		},
	}
	persona := &sympoziumv1alpha1.AgentConfigSpec{Name: "worker"}

	if _, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), pack, persona, 0, "http://local:8080"); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}

	var updated sympoziumv1alpha1.Agent
	if err := r.Get(context.Background(), client.ObjectKey{Name: existing.Name, Namespace: existing.Namespace}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Spec.Agents.Default.Model != "local-llama" {
		t.Fatalf("model = %q, want local-llama", updated.Spec.Agents.Default.Model)
	}
	if len(updated.Spec.AuthRefs) != 0 {
		t.Fatalf("expected nil authRefs for local model, got %+v", updated.Spec.AuthRefs)
	}
}

// TestReconcileAgentConfig_MixedEnsemble verifies that when an ensemble defaults
// to a cluster-local model, a persona that pins its own cloud provider fully
// opts out: it keeps its own model, its matching cloud auth key, and a non-local
// base URL — end to end through the update path.
func TestReconcileAgentConfig_MixedEnsemble(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add scheme: %v", err)
	}

	existing := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pack-cloud",
			Namespace: "default",
			Labels:    map[string]string{"sympozium.ai/agent-config": "cloud"},
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{Default: sympoziumv1alpha1.AgentConfig{}},
		},
	}

	r := &EnsembleReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build(),
		Scheme: scheme,
	}
	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "test-pack", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			ModelRef: "local-llama",
			BaseURL:  "https://pack.example/v1",
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "openai", Secret: "openai-key"},
				{Provider: "anthropic", Secret: "anthropic-key"},
			},
		},
	}
	// This persona runs on Anthropic even though the ensemble default is local.
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Name:     "cloud",
		Provider: "anthropic",
		Model:    "claude-sonnet-5",
	}

	if _, err := r.reconcileAgentConfig(context.Background(), logr.Discard(), pack, persona, 0, "http://local:8080"); err != nil {
		t.Fatalf("reconcileAgentConfig: %v", err)
	}

	var updated sympoziumv1alpha1.Agent
	if err := r.Get(context.Background(), client.ObjectKey{Name: existing.Name, Namespace: existing.Namespace}, &updated); err != nil {
		t.Fatalf("get: %v", err)
	}
	if updated.Spec.Agents.Default.Model != "claude-sonnet-5" {
		t.Fatalf("model = %q, want claude-sonnet-5 (persona model, not modelRef)", updated.Spec.Agents.Default.Model)
	}
	if len(updated.Spec.AuthRefs) != 1 || updated.Spec.AuthRefs[0].Secret != "anthropic-key" {
		t.Fatalf("authRefs = %+v, want [anthropic-key]", updated.Spec.AuthRefs)
	}
	if updated.Spec.Agents.Default.BaseURL != "https://pack.example/v1" {
		t.Fatalf("baseURL = %q, want the ensemble base url (not the local endpoint)", updated.Spec.Agents.Default.BaseURL)
	}
}

// ── Relationship graph validation tests ────────────────────────────────────

func testPersonas(names ...string) []sympoziumv1alpha1.AgentConfigSpec {
	out := make([]sympoziumv1alpha1.AgentConfigSpec, len(names))
	for i, n := range names {
		out[i] = sympoziumv1alpha1.AgentConfigSpec{Name: n}
	}
	return out
}

func TestValidateRelationshipGraph_NoCycle(t *testing.T) {
	personas := testPersonas("a", "b", "c")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "b", Type: "sequential"},
		{Source: "b", Target: "c", Type: "sequential"},
	}
	if err := validateRelationshipGraph(personas, rels, nil, ""); err != nil {
		t.Errorf("expected no error for linear pipeline, got: %v", err)
	}
}

func TestValidateRelationshipGraph_Cycle(t *testing.T) {
	personas := testPersonas("a", "b", "c")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "b", Type: "sequential"},
		{Source: "b", Target: "c", Type: "sequential"},
		{Source: "c", Target: "a", Type: "sequential"},
	}
	err := validateRelationshipGraph(personas, rels, nil, "")
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle detected") {
		t.Errorf("error should mention cycle, got: %v", err)
	}
}

func TestValidateRelationshipGraph_SelfLoop(t *testing.T) {
	personas := testPersonas("a")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "a", Type: "sequential"},
	}
	err := validateRelationshipGraph(personas, rels, nil, "")
	if err == nil {
		t.Fatal("expected cycle error for self-loop")
	}
}

func TestValidateRelationshipGraph_DanglingRef(t *testing.T) {
	personas := testPersonas("a", "b")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "nonexistent", Type: "sequential"},
	}
	err := validateRelationshipGraph(personas, rels, nil, "")
	if err == nil {
		t.Fatal("expected error for dangling reference")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention missing persona, got: %v", err)
	}
}

func TestValidateRelationshipGraph_IgnoresSupervision(t *testing.T) {
	// Supervision edges are observability-only and do not drive invocation, so
	// they never participate in cycle detection. Here a→b delegation plus b→a
	// supervision is not a cycle (only the delegation edge is a graph edge).
	personas := testPersonas("a", "b")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "b", Type: "delegation"},
		{Source: "b", Target: "a", Type: "supervision"},
	}
	if err := validateRelationshipGraph(personas, rels, nil, ""); err != nil {
		t.Errorf("supervision edges should not trigger cycle detection, got: %v", err)
	}
}

func TestValidateRelationshipGraph_DelegationDAGAllowed(t *testing.T) {
	// A coordinator delegating to two workers is a DAG — legal.
	personas := testPersonas("coord", "w1", "w2")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "coord", Target: "w1", Type: "delegation"},
		{Source: "coord", Target: "w2", Type: "delegation"},
	}
	if err := validateRelationshipGraph(personas, rels, nil, ""); err != nil {
		t.Errorf("delegation DAG should be allowed, got: %v", err)
	}
}

func TestValidateRelationshipGraph_DelegationCycleRejected(t *testing.T) {
	// Regression: a delegation cycle a→b→a is a fork-bomb topology and must be
	// rejected at config time (previously only sequential edges were checked).
	personas := testPersonas("a", "b")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "b", Type: "delegation"},
		{Source: "b", Target: "a", Type: "delegation"},
	}
	if err := validateRelationshipGraph(personas, rels, nil, ""); err == nil {
		t.Fatal("expected a delegation cycle to be rejected")
	}
}

func TestValidateRelationshipGraph_MixedSequentialDelegationCycleRejected(t *testing.T) {
	// A cycle formed across a sequential and a delegation edge is still a cycle.
	personas := testPersonas("a", "b")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "b", Type: "sequential"},
		{Source: "b", Target: "a", Type: "delegation"},
	}
	if err := validateRelationshipGraph(personas, rels, nil, ""); err == nil {
		t.Fatal("expected a mixed sequential/delegation cycle to be rejected")
	}
}

func TestValidateRelationshipGraph_EmptyRelationships(t *testing.T) {
	personas := testPersonas("a", "b")
	if err := validateRelationshipGraph(personas, nil, nil, ""); err != nil {
		t.Errorf("empty relationships should pass, got: %v", err)
	}
}

// ── Stimulus validation tests ────────────────────────────────────────────────

func TestValidateRelationshipGraph_StimulusValid(t *testing.T) {
	personas := testPersonas("lead", "worker")
	stimulus := &sympoziumv1alpha1.StimulusSpec{
		Name:   "kickoff",
		Prompt: "Begin the research workflow",
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "kickoff", Target: "lead", Type: "stimulus"},
		{Source: "lead", Target: "worker", Type: "sequential"},
	}
	if err := validateRelationshipGraph(personas, rels, stimulus, ""); err != nil {
		t.Errorf("expected valid stimulus config, got: %v", err)
	}
}

func TestValidateRelationshipGraph_StimulusNoSpec(t *testing.T) {
	personas := testPersonas("lead")
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "kickoff", Target: "lead", Type: "stimulus"},
	}
	err := validateRelationshipGraph(personas, rels, nil, "")
	if err == nil {
		t.Fatal("expected error when stimulus relationship exists without spec")
	}
	if !strings.Contains(err.Error(), "no stimulus spec") {
		t.Errorf("error should mention missing spec, got: %v", err)
	}
}

func TestValidateRelationshipGraph_StimulusNoRelationship(t *testing.T) {
	personas := testPersonas("lead")
	stimulus := &sympoziumv1alpha1.StimulusSpec{
		Name:   "kickoff",
		Prompt: "Start",
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "lead", Target: "lead", Type: "delegation"},
	}
	err := validateRelationshipGraph(personas, rels, stimulus, "")
	if err == nil {
		t.Fatal("expected error when stimulus spec exists without relationship")
	}
	if !strings.Contains(err.Error(), "no stimulus relationship") {
		t.Errorf("error should mention missing relationship, got: %v", err)
	}
}

func TestValidateRelationshipGraph_StimulusEmptyPrompt(t *testing.T) {
	personas := testPersonas("lead")
	stimulus := &sympoziumv1alpha1.StimulusSpec{
		Name:   "kickoff",
		Prompt: "   ",
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "kickoff", Target: "lead", Type: "stimulus"},
	}
	err := validateRelationshipGraph(personas, rels, stimulus, "")
	if err == nil {
		t.Fatal("expected error for empty stimulus prompt")
	}
	if !strings.Contains(err.Error(), "prompt must not be empty") {
		t.Errorf("error should mention empty prompt, got: %v", err)
	}
}

func TestValidateRelationshipGraph_StimulusSourceMismatch(t *testing.T) {
	personas := testPersonas("lead")
	stimulus := &sympoziumv1alpha1.StimulusSpec{
		Name:   "kickoff",
		Prompt: "Begin",
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "wrong-name", Target: "lead", Type: "stimulus"},
	}
	err := validateRelationshipGraph(personas, rels, stimulus, "")
	if err == nil {
		t.Fatal("expected error when stimulus source doesn't match name")
	}
	if !strings.Contains(err.Error(), "must match stimulus name") {
		t.Errorf("error should mention name mismatch, got: %v", err)
	}
}

func TestValidateRelationshipGraph_StimulusDanglingTarget(t *testing.T) {
	personas := testPersonas("lead")
	stimulus := &sympoziumv1alpha1.StimulusSpec{
		Name:   "kickoff",
		Prompt: "Begin",
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "kickoff", Target: "nonexistent", Type: "stimulus"},
	}
	err := validateRelationshipGraph(personas, rels, stimulus, "")
	if err == nil {
		t.Fatal("expected error for dangling stimulus target")
	}
	if !strings.Contains(err.Error(), "unknown persona") {
		t.Errorf("error should mention unknown target, got: %v", err)
	}
}

func TestValidateRelationshipGraph_MultipleStimulusRelationships(t *testing.T) {
	personas := testPersonas("lead", "worker")
	stimulus := &sympoziumv1alpha1.StimulusSpec{
		Name:   "kickoff",
		Prompt: "Begin",
	}
	rels := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "kickoff", Target: "lead", Type: "stimulus"},
		{Source: "kickoff", Target: "worker", Type: "stimulus"},
	}
	err := validateRelationshipGraph(personas, rels, stimulus, "")
	if err == nil {
		t.Fatal("expected error for multiple stimulus relationships")
	}
	if !strings.Contains(err.Error(), "at most one stimulus") {
		t.Errorf("error should mention multiple stimulus, got: %v", err)
	}
}

// ── Skill reconciliation tests ────────────────────────────────────────────────

func TestBuildDesiredSkills_Basic(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SkillParams: map[string]map[string]string{
				"github-gitops": {"repo": "my-org/my-repo"},
			},
		},
	}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Skills: []string{"k8s-ops", "github-gitops"},
	}

	got := buildDesiredSkills(pack, persona)

	// Should have k8s-ops, github-gitops (with params), and memory (auto-added).
	if len(got) != 3 {
		t.Fatalf("expected 3 skills, got %d: %+v", len(got), got)
	}
	if got[0].SkillPackRef != "k8s-ops" {
		t.Errorf("expected first skill k8s-ops, got %s", got[0].SkillPackRef)
	}
	if got[1].SkillPackRef != "github-gitops" {
		t.Errorf("expected second skill github-gitops, got %s", got[1].SkillPackRef)
	}
	if got[1].Params["repo"] != "my-org/my-repo" {
		t.Errorf("expected github-gitops repo param, got %v", got[1].Params)
	}
	if got[2].SkillPackRef != "memory" {
		t.Errorf("expected memory skill auto-added, got %s", got[2].SkillPackRef)
	}
}

func TestBuildDesiredSkills_SkipsMCPBridge(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Skills: []string{"mcp-bridge", "k8s-ops"},
	}

	got := buildDesiredSkills(pack, persona)

	for _, s := range got {
		if s.SkillPackRef == "mcp-bridge" {
			t.Error("mcp-bridge should be filtered out")
		}
	}
}

func TestBuildDesiredSkills_MemoryNotDuplicated(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Skills: []string{"memory", "k8s-ops"},
	}

	got := buildDesiredSkills(pack, persona)

	memoryCount := 0
	for _, s := range got {
		if s.SkillPackRef == "memory" {
			memoryCount++
		}
	}
	if memoryCount != 1 {
		t.Errorf("expected exactly 1 memory skill, got %d", memoryCount)
	}
}

func TestBuildDesiredSkills_WebEndpoint(t *testing.T) {
	pack := &sympoziumv1alpha1.Ensemble{}
	persona := &sympoziumv1alpha1.AgentConfigSpec{
		Skills: []string{"k8s-ops"},
		WebEndpoint: &sympoziumv1alpha1.AgentConfigWebEndpoint{
			Enabled:  true,
			Hostname: "my-agent.example.com",
		},
	}

	got := buildDesiredSkills(pack, persona)

	var found bool
	for _, s := range got {
		if s.SkillPackRef == "web-endpoint" {
			found = true
			if s.Params["hostname"] != "my-agent.example.com" {
				t.Errorf("expected hostname param, got %v", s.Params)
			}
		}
	}
	if !found {
		t.Error("expected web-endpoint skill to be included")
	}
}

func TestSkillRefsEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []sympoziumv1alpha1.SkillRef
		want bool
	}{
		{
			name: "both nil",
			a:    nil,
			b:    nil,
			want: true,
		},
		{
			name: "equal with params",
			a: []sympoziumv1alpha1.SkillRef{
				{SkillPackRef: "k8s-ops"},
				{SkillPackRef: "github-gitops", Params: map[string]string{"repo": "org/repo"}},
			},
			b: []sympoziumv1alpha1.SkillRef{
				{SkillPackRef: "k8s-ops"},
				{SkillPackRef: "github-gitops", Params: map[string]string{"repo": "org/repo"}},
			},
			want: true,
		},
		{
			name: "different length",
			a:    []sympoziumv1alpha1.SkillRef{{SkillPackRef: "k8s-ops"}},
			b:    []sympoziumv1alpha1.SkillRef{{SkillPackRef: "k8s-ops"}, {SkillPackRef: "memory"}},
			want: false,
		},
		{
			name: "different skill",
			a:    []sympoziumv1alpha1.SkillRef{{SkillPackRef: "k8s-ops"}},
			b:    []sympoziumv1alpha1.SkillRef{{SkillPackRef: "github-gitops"}},
			want: false,
		},
		{
			name: "different params",
			a:    []sympoziumv1alpha1.SkillRef{{SkillPackRef: "x", Params: map[string]string{"a": "1"}}},
			b:    []sympoziumv1alpha1.SkillRef{{SkillPackRef: "x", Params: map[string]string{"a": "2"}}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := skillRefsEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("skillRefsEqual() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBuildChannelSpec_SlackOptionsPrecedence(t *testing.T) {
	packOpts := &sympoziumv1alpha1.SlackChannelOptions{
		Threading:        true,
		ThreadStickiness: false,
		AllowedTriggers:  []string{"mention"},
	}
	personaOpts := &sympoziumv1alpha1.SlackChannelOptions{
		Threading:        true,
		ThreadStickiness: true,
		AllowedTriggers:  []string{"mention", "dm"},
	}

	tests := []struct {
		name        string
		channelType string
		packOpts    *sympoziumv1alpha1.SlackChannelOptions
		personaOpts *sympoziumv1alpha1.SlackChannelOptions
		want        *sympoziumv1alpha1.SlackChannelOptions
	}{
		{"persona overrides pack", "slack", packOpts, personaOpts, personaOpts},
		{"pack falls through when persona absent", "slack", packOpts, nil, packOpts},
		{"nothing set", "slack", nil, nil, nil},
		{"slack options ignored on non-slack channels", "discord", packOpts, personaOpts, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pack := &sympoziumv1alpha1.Ensemble{
				ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
				Spec: sympoziumv1alpha1.EnsembleSpec{
					SlackOptions: tt.packOpts,
				},
			}
			persona := &sympoziumv1alpha1.AgentConfigSpec{
				Name:         "alice",
				SystemPrompt: "x",
				SlackOptions: tt.personaOpts,
			}
			cs := buildChannelSpec(pack, persona, tt.channelType)
			if tt.want == nil {
				if cs.Slack != nil {
					t.Fatalf("expected nil Slack, got %+v", cs.Slack)
				}
				return
			}
			if cs.Slack != tt.want {
				t.Fatalf("Slack = %+v, want %+v", cs.Slack, tt.want)
			}
		})
	}
}

// ── workflowType ⇄ relationship-edge consistency ───────────────────────────

func TestValidateRelationshipGraph_WorkflowTypeConsistency(t *testing.T) {
	personas := testPersonas("a", "b")
	seqEdge := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "b", Type: "sequential"},
	}
	delEdge := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "b", Type: "delegation"},
	}
	supEdge := []sympoziumv1alpha1.AgentConfigRelationship{
		{Source: "a", Target: "b", Type: "supervision"},
	}

	tests := []struct {
		name         string
		workflowType string
		rels         []sympoziumv1alpha1.AgentConfigRelationship
		wantErr      string // substring; "" means expect success
	}{
		{"pipeline with sequential edge ok", "pipeline", seqEdge, ""},
		{"pipeline without sequential edge errors", "pipeline", delEdge, "requires at least one sequential"},
		{"pipeline with no edges errors", "pipeline", nil, "requires at least one sequential"},
		{"delegation with delegation edge ok", "delegation", delEdge, ""},
		{"delegation without delegation edge errors", "delegation", supEdge, "requires at least one delegation"},
		{"delegation with no edges errors", "delegation", nil, "requires at least one delegation"},
		{"autonomous needs no edges", "autonomous", nil, ""},
		{"empty workflowType needs no edges", "", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRelationshipGraph(personas, tt.rels, nil, tt.workflowType)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErr)
			}
		})
	}
}
