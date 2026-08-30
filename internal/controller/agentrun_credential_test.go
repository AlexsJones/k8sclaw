package controller

import (
	"testing"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestAgentAllowsModelCredential(t *testing.T) {
	agent := &sympoziumv1alpha1.Agent{Spec: sympoziumv1alpha1.AgentSpec{AuthRefs: []sympoziumv1alpha1.SecretRef{
		{Provider: "openai", Secret: "openai-key"},
		{Secret: "provider-agnostic-key"},
	}}}

	for _, tt := range []struct {
		name     string
		provider string
		secret   string
		want     bool
	}{
		{name: "empty local credential", provider: "custom", want: true},
		{name: "matching provider", provider: "openai", secret: "openai-key", want: true},
		{name: "provider case is insensitive", provider: "OpenAI", secret: "openai-key", want: true},
		{name: "missing provider does not match provider-specific grant", secret: "openai-key", want: false},
		{name: "agent provider-agnostic grant", provider: "anthropic", secret: "provider-agnostic-key", want: true},
		{name: "mismatched provider", provider: "anthropic", secret: "openai-key", want: false},
		{name: "undeclared secret", provider: "openai", secret: "other-key", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := agentAllowsModelCredential(agent, tt.provider, tt.secret); got != tt.want {
				t.Fatalf("agentAllowsModelCredential(%q, %q) = %t, want %t", tt.provider, tt.secret, got, tt.want)
			}
		})
	}
}
