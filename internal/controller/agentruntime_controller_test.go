package controller

import (
	"strings"
	"testing"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

const runtimeTestImage = "ghcr.io/acme/my-harness@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func runtimeSpec(image string, capabilities ...string) *sympoziumv1alpha1.AgentRuntime {
	return &sympoziumv1alpha1.AgentRuntime{
		Spec: sympoziumv1alpha1.AgentRuntimeSpec{
			Image:        image,
			Capabilities: capabilities,
		},
	}
}

func TestAgentRuntime_Validate_AcceptsDigestPinned(t *testing.T) {
	r := &AgentRuntimeReconciler{}
	digest, reason := r.validate(runtimeSpec(runtimeTestImage, "persona", "toolFilter"))
	if reason != "" {
		t.Fatalf("validate returned reason %q, want empty", reason)
	}
	if digest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("validate returned digest %q", digest)
	}
}

func TestAgentRuntime_Validate_RejectsUnpinnedImage(t *testing.T) {
	r := &AgentRuntimeReconciler{}
	for _, image := range []string{"ghcr.io/acme/my-harness:v1", "ghcr.io/acme/my-harness"} {
		if _, reason := r.validate(runtimeSpec(image)); reason == "" {
			t.Fatalf("validate(%q) accepted an unpinned image", image)
		}
	}
}

func TestAgentRuntime_Validate_RejectsUnsupportedCapability(t *testing.T) {
	r := &AgentRuntimeReconciler{}
	for _, capability := range []string{"subagents", "resume", "outputSchema", "nonsense"} {
		if _, reason := r.validate(runtimeSpec(runtimeTestImage, capability)); reason == "" {
			t.Fatalf("validate(capability %q) accepted; want rejection", capability)
		}
	}
}

func TestAgentRuntime_Validate_AllowsSupportedCapabilities(t *testing.T) {
	r := &AgentRuntimeReconciler{}
	if _, reason := r.validate(runtimeSpec(runtimeTestImage, "persona", "toolFilter")); reason != "" {
		t.Fatalf("validate(persona,toolFilter) returned reason %q", reason)
	}
}
