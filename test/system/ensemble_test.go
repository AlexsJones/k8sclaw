//go:build system

package system_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/agentedit"
)

func TestEnsembleCreatesAgents(t *testing.T) {
	ns := createTestNamespace(t)
	name := "ens-create"

	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Enabled:     true,
			Description: "test ensemble",
			Category:    "test",
			Version:     "1.0",
			BaseURL:     "http://fake:1234/v1",
			AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "analyst", SystemPrompt: "You are an analyst."},
				{Name: "writer", SystemPrompt: "You are a writer."},
			},
		},
	}
	if err := k8sClient.Create(testCtx, ensemble); err != nil {
		t.Fatalf("create ensemble: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, ensemble) })

	// Wait for the controller to create Agent CRs.
	pollUntil(t, 10*time.Second, 200*time.Millisecond, func() bool {
		var agents sympoziumv1alpha1.AgentList
		if err := k8sClient.List(testCtx, &agents,
			client.InNamespace(ns),
			client.MatchingLabels{"sympozium.ai/ensemble": name},
		); err != nil {
			return false
		}
		return len(agents.Items) == 2
	})

	// Verify agent names and labels.
	for _, persona := range []string{"analyst", "writer"} {
		agentName := fmt.Sprintf("%s-%s", name, persona)
		var agent sympoziumv1alpha1.Agent
		assertExists(t, &agent, ns, agentName)

		labels := agent.Labels
		if labels["sympozium.ai/ensemble"] != name {
			t.Errorf("agent %s: ensemble label = %q, want %q", agentName, labels["sympozium.ai/ensemble"], name)
		}
		if labels["sympozium.ai/agent-config"] != persona {
			t.Errorf("agent %s: agent-config label = %q, want %q", agentName, labels["sympozium.ai/agent-config"], persona)
		}
	}
}

func TestEnsembleUpdatePropagatesBaseURL(t *testing.T) {
	ns := createTestNamespace(t)
	name := "ens-update"

	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Enabled:     true,
			Description: "update test",
			Category:    "test",
			Version:     "1.0",
			BaseURL:     "http://old:1234/v1",
			AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "worker", SystemPrompt: "You work."},
			},
		},
	}
	if err := k8sClient.Create(testCtx, ensemble); err != nil {
		t.Fatalf("create ensemble: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, ensemble) })

	agentName := fmt.Sprintf("%s-worker", name)

	// Wait for agent to be created.
	pollUntil(t, 10*time.Second, 200*time.Millisecond, func() bool {
		var a sympoziumv1alpha1.Agent
		return k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: agentName}, &a) == nil
	})

	// Patch the ensemble baseURL.
	if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: name}, ensemble); err != nil {
		t.Fatalf("get ensemble: %v", err)
	}
	ensemble.Spec.BaseURL = "http://new:5678/v1"
	if err := k8sClient.Update(testCtx, ensemble); err != nil {
		t.Fatalf("update ensemble: %v", err)
	}

	// Wait for the agent to pick up the new baseURL.
	pollUntil(t, 10*time.Second, 200*time.Millisecond, func() bool {
		var a sympoziumv1alpha1.Agent
		if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: agentName}, &a); err != nil {
			return false
		}
		cfg := a.Spec.Agents.Default
		return cfg.BaseURL == "http://new:5678/v1"
	})
}

func TestEnsembleDisableDeletesAgents(t *testing.T) {
	ns := createTestNamespace(t)
	name := "ens-disable"

	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Enabled:     true,
			Description: "disable test",
			Category:    "test",
			Version:     "1.0",
			BaseURL:     "http://fake:1234/v1",
			AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "alpha", SystemPrompt: "Alpha."},
				{Name: "beta", SystemPrompt: "Beta."},
			},
		},
	}
	if err := k8sClient.Create(testCtx, ensemble); err != nil {
		t.Fatalf("create ensemble: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, ensemble) })

	// Wait for both agents to appear.
	pollUntil(t, 10*time.Second, 200*time.Millisecond, func() bool {
		var agents sympoziumv1alpha1.AgentList
		_ = k8sClient.List(testCtx, &agents,
			client.InNamespace(ns),
			client.MatchingLabels{"sympozium.ai/ensemble": name},
		)
		return len(agents.Items) == 2
	})

	// Disable the ensemble.
	if err := k8sClient.Get(testCtx, client.ObjectKey{Namespace: ns, Name: name}, ensemble); err != nil {
		t.Fatalf("get ensemble: %v", err)
	}
	ensemble.Spec.Enabled = false
	if err := k8sClient.Update(testCtx, ensemble); err != nil {
		t.Fatalf("update ensemble: %v", err)
	}

	// Wait for agents to be deleted.
	pollUntil(t, 10*time.Second, 200*time.Millisecond, func() bool {
		var agents sympoziumv1alpha1.AgentList
		_ = k8sClient.List(testCtx, &agents,
			client.InNamespace(ns),
			client.MatchingLabels{"sympozium.ai/ensemble": name},
		)
		return len(agents.Items) == 0
	})
}

func TestEnsembleStimulusConfigViaAPI(t *testing.T) {
	ns := createTestNamespace(t)
	name := "ens-stimulus"

	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Enabled:      true,
			Description:  "stimulus test",
			Category:     "test",
			Version:      "1.0",
			BaseURL:      "http://fake:1234/v1",
			WorkflowType: "pipeline",
			Stimulus: &sympoziumv1alpha1.StimulusSpec{
				Name:   "kickoff",
				Prompt: "Begin the research workflow.",
			},
			AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "lead", SystemPrompt: "You are a lead researcher."},
				{Name: "analyst", SystemPrompt: "You are an analyst."},
			},
			Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
				{Source: "kickoff", Target: "lead", Type: "stimulus"},
				{Source: "lead", Target: "analyst", Type: "sequential"},
			},
		},
	}
	if err := k8sClient.Create(testCtx, ensemble); err != nil {
		t.Fatalf("create ensemble: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, ensemble) })

	// Wait for the ensemble to be visible via API.
	path := fmt.Sprintf("/api/v1/ensembles/%s?%s", name, nsQuery(ns))
	pollUntil(t, 10*time.Second, 200*time.Millisecond, func() bool {
		rec := httpDo(t, "GET", path, nil)
		return rec.Code == 200
	})

	var got sympoziumv1alpha1.Ensemble
	got, _ = httpJSON[sympoziumv1alpha1.Ensemble](t, "GET", path, nil)

	if got.Spec.Stimulus == nil {
		t.Fatal("stimulus is nil")
	}
	if got.Spec.Stimulus.Name != "kickoff" {
		t.Errorf("stimulus.name = %q, want kickoff", got.Spec.Stimulus.Name)
	}
	if got.Spec.Stimulus.Prompt != "Begin the research workflow." {
		t.Errorf("stimulus.prompt mismatch")
	}

	// Verify stimulus relationship exists.
	found := false
	for _, r := range got.Spec.Relationships {
		if r.Type == "stimulus" && r.Source == "kickoff" && r.Target == "lead" {
			found = true
		}
	}
	if !found {
		t.Error("stimulus relationship not found")
	}
}

func TestEnsembleStimulusTriggerRejectsDisabled(t *testing.T) {
	ns := createTestNamespace(t)
	name := "ens-stim-disabled"

	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Enabled:      false,
			Description:  "disabled stimulus test",
			Category:     "test",
			Version:      "1.0",
			WorkflowType: "pipeline",
			Stimulus: &sympoziumv1alpha1.StimulusSpec{
				Name:   "kickoff",
				Prompt: "Begin.",
			},
			AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "lead", SystemPrompt: "Lead."},
			},
			Relationships: []sympoziumv1alpha1.AgentConfigRelationship{
				{Source: "kickoff", Target: "lead", Type: "stimulus"},
			},
		},
	}
	if err := k8sClient.Create(testCtx, ensemble); err != nil {
		t.Fatalf("create ensemble: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, ensemble) })

	// Trigger should fail because ensemble is disabled (agents not stamped out).
	path := fmt.Sprintf("/api/v1/ensembles/%s/stimulus/trigger?%s", name, nsQuery(ns))
	rec := httpDo(t, "POST", path, nil)
	// Expect 404 (target agent doesn't exist because pack is disabled).
	if rec.Code != 404 {
		t.Errorf("trigger status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}

func TestEnsembleStimulusTriggerRejectsNoStimulus(t *testing.T) {
	ns := createTestNamespace(t)
	name := "ens-no-stim"

	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Enabled:     false,
			Description: "no stimulus test",
			Category:    "test",
			Version:     "1.0",
			AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "worker", SystemPrompt: "Worker."},
			},
		},
	}
	if err := k8sClient.Create(testCtx, ensemble); err != nil {
		t.Fatalf("create ensemble: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, ensemble) })

	path := fmt.Sprintf("/api/v1/ensembles/%s/stimulus/trigger?%s", name, nsQuery(ns))
	rec := httpDo(t, "POST", path, nil)
	if rec.Code != 400 {
		t.Errorf("trigger status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

// TestAgentSpecHasNoServerSideDefaults guards the assumption the Ensemble update
// path rests on.
//
// reconcileAgentConfig compares the *stored* Agent spec against buildAgent's
// in-memory output and assigns the whole spec on any difference. That converges only
// while the apiserver returns a spec byte-identical to what was submitted. A
// +kubebuilder:default on an AgentSpec field buildAgent leaves zero would break it:
// stored and desired then differ forever, and every reconcile issues a redundant
// Update.
//
// Note on what this does NOT test, and why: resourceVersion is not a usable signal
// here. The apiserver no-ops an Update that produces no net storage change, so the
// redundant writes are invisible in resourceVersion and generate no watch events.
// Asserting the round-trip directly is what catches the defaulting change, and it
// names the offending field.
//
// The unit tests cannot cover this — the fake client applies no defaults.
func TestAgentSpecHasNoServerSideDefaults(t *testing.T) {
	ns := createTestNamespace(t)

	// Mirrors the shape buildAgent produces: a few fields set, the rest zero. Any
	// zero field the CRD defaults comes back populated.
	submitted := sympoziumv1alpha1.AgentSpec{
		DisplayName: "Analyst",
		Agents: sympoziumv1alpha1.AgentsSpec{
			Default: sympoziumv1alpha1.AgentConfig{
				Model:   "gpt-4o",
				BaseURL: "http://fake:1234/v1",
			},
		},
		Skills: []sympoziumv1alpha1.SkillRef{{SkillPackRef: "memory"}},
		Memory: &sympoziumv1alpha1.MemorySpec{Enabled: true, MaxSizeKB: 256},
	}

	agent := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "defaults-probe", Namespace: ns},
		Spec:       *submitted.DeepCopy(),
	}
	if err := k8sClient.Create(testCtx, agent); err != nil {
		t.Fatalf("create agent: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, agent) })

	// k8sClient is the manager's cached client, so the read has to wait for the
	// informer rather than assuming write-then-read consistency.
	var stored sympoziumv1alpha1.Agent
	pollUntil(t, 10*time.Second, 200*time.Millisecond, func() bool {
		return k8sClient.Get(testCtx, client.ObjectKey{Name: "defaults-probe", Namespace: ns}, &stored) == nil
	})

	want, err := json.MarshalIndent(submitted, "", "  ")
	if err != nil {
		t.Fatalf("marshal submitted: %v", err)
	}
	got, err := json.MarshalIndent(stored.Spec, "", "  ")
	if err != nil {
		t.Fatalf("marshal stored: %v", err)
	}

	if string(want) != string(got) {
		t.Errorf("AgentSpec did not round-trip through the apiserver unchanged.\n"+
			"submitted:\n%s\n\nstored:\n%s\n\n"+
			"A field defaulted server-side but left zero by buildAgent makes "+
			"reconcileAgentConfig's whole-spec comparison never converge, so every reconcile "+
			"issues a redundant Update. Either have buildAgent set the same value, or drop the "+
			"+kubebuilder:default.", want, got)
	}
}

// TestAgentEditIsVisibleImmediately is the end-to-end claim behind the reconcile
// wait: after agentedit.Apply returns, a read through the same client a UI uses
// already shows the edit.
//
// Without the wait this races the controller. Both clients re-read the moment the
// edit returns — the TUI refreshes on the result message, the web UI invalidates its
// agents query — and would land on pre-edit values, then correct themselves seconds
// later. The TUI seeds its edit form from that read, so a user reopening the form
// could save stale values back over their own change.
//
// k8sClient is the manager's cached client, which is what makes this meaningful:
// the assertion is that the informer has caught up too, not merely the apiserver.
func TestAgentEditIsVisibleImmediately(t *testing.T) {
	ns := createTestNamespace(t)
	name := "ens-visible"
	agentName := fmt.Sprintf("%s-analyst", name)

	ensemble := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			Enabled: true,
			BaseURL: "http://fake:1234/v1",
			AgentConfigs: []sympoziumv1alpha1.AgentConfigSpec{
				{Name: "analyst", SystemPrompt: "You are an analyst.", Model: "gpt-4o"},
			},
		},
	}
	if err := k8sClient.Create(testCtx, ensemble); err != nil {
		t.Fatalf("create ensemble: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(testCtx, ensemble) })

	var agent sympoziumv1alpha1.Agent
	pollUntil(t, 15*time.Second, 200*time.Millisecond, func() bool {
		return k8sClient.Get(testCtx, client.ObjectKey{Name: agentName, Namespace: ns}, &agent) == nil
	})

	target, err := agentedit.Apply(testCtx, k8sClient, &agent, agentedit.Edit{
		Model: strPtr("gpt-4o-mini"),
	})
	if err != nil {
		t.Fatalf("agentedit.Apply: %v", err)
	}
	if target.Kind != "Ensemble" {
		t.Fatalf("edit went to %s, want the Ensemble — the agent is ensemble-managed", target)
	}
	if !target.Changed {
		t.Fatal("Target.Changed = false, but the model was altered")
	}
	if !target.Observed {
		t.Fatalf("Target.Observed = false: the agent did not pick the edit up within the wait. "+
			"target = %s", target)
	}

	// The assertion that matters: no polling here. This is the read a UI performs
	// the instant the edit returns.
	var afterEdit sympoziumv1alpha1.Agent
	if err := k8sClient.Get(testCtx, client.ObjectKey{Name: agentName, Namespace: ns}, &afterEdit); err != nil {
		t.Fatalf("get agent: %v", err)
	}
	if got := afterEdit.Spec.Agents.Default.Model; got != "gpt-4o-mini" {
		t.Errorf("model = %q immediately after the edit, want gpt-4o-mini.\n"+
			"The read raced the controller, which is what the reconcile wait exists to prevent.", got)
	}
}

func strPtr(s string) *string { return &s }
