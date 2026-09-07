package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestAgentRuntime_CellnReadinessIsIndependent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		image      string
		profile    bool
		wantOCI    bool
		wantReason string
	}{
		{"oci only", runtimeTestImage, false, true, "NotConfigured"},
		{"dual profile", runtimeTestImage, true, true, "VerificationUnavailable"},
		{"celln cannot rescue invalid OCI", "mutable:latest", true, false, "VerificationUnavailable"},
		{"celln only remains unsupported", "", true, false, "VerificationUnavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			obj := runtimeSpec(tc.image)
			obj.Name, obj.Namespace, obj.Generation = "runtime", "tenant", 7
			if tc.profile {
				obj.Spec.Celln = &sympoziumv1alpha1.AgentRuntimeCellnProfile{Revision: "v1"}
			}
			// Informational conformance and stale status are not Celln approval.
			obj.Spec.Conformance = &sympoziumv1alpha1.AgentRuntimeConformance{Status: "conformant"}
			obj.Status.Conditions = []metav1.Condition{{Type: "CellnReady", Status: metav1.ConditionTrue, Reason: "Forged", ObservedGeneration: 6, LastTransitionTime: metav1.Now()}}
			c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(obj).WithObjects(obj).Build()
			r := &AgentRuntimeReconciler{Client: c, Scheme: scheme, Log: logr.Discard()}
			key := types.NamespacedName{Name: obj.Name, Namespace: obj.Namespace}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
				t.Fatal(err)
			}
			var got sympoziumv1alpha1.AgentRuntime
			if err := c.Get(context.Background(), key, &got); err != nil {
				t.Fatal(err)
			}
			if meta.IsStatusConditionTrue(got.Status.Conditions, "Ready") != tc.wantOCI {
				t.Fatalf("OCI readiness changed: %+v", got.Status)
			}
			cond := meta.FindStatusCondition(got.Status.Conditions, "CellnReady")
			if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != tc.wantReason || cond.ObservedGeneration != 7 {
				t.Fatalf("Celln condition did not fail closed at current generation: %+v", cond)
			}
		})
	}
}

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

func TestAgentRuntime_Validate_RequiresSessionDescriptorForV1Alpha2(t *testing.T) {
	runtime := runtimeSpec(runtimeTestImage)
	runtime.Spec.ContractVersion = "v1alpha2"
	if _, reason := (&AgentRuntimeReconciler{}).validate(runtime); reason == "" {
		t.Fatal("v1alpha2 without session descriptor was accepted")
	}
	runtime.Spec.Session = &sympoziumv1alpha1.AgentRuntimeSession{Protocol: "openai-chat", Port: 8080}
	if _, reason := (&AgentRuntimeReconciler{}).validate(runtime); reason != "" {
		t.Fatalf("v1alpha2 session descriptor rejected: %s", reason)
	}
}
