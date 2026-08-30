package webhook

import (
	"testing"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestAgentAllowsModelCredential(t *testing.T) {
	agent := &sympoziumv1alpha1.Agent{Spec: sympoziumv1alpha1.AgentSpec{AuthRefs: []sympoziumv1alpha1.SecretRef{
		{Provider: "openai", Secret: "openai-key"},
	}}}
	if !agentAllowsModelCredential(agent, "openai", "openai-key") {
		t.Fatal("expected Agent-declared credential to be allowed")
	}
	if agentAllowsModelCredential(agent, "anthropic", "openai-key") {
		t.Fatal("credential must not cross a provider boundary")
	}
	if agentAllowsModelCredential(agent, "", "openai-key") {
		t.Fatal("missing provider must not match a provider-specific credential")
	}
	if agentAllowsModelCredential(agent, "openai", "other-key") {
		t.Fatal("undeclared credential must be denied")
	}
}
