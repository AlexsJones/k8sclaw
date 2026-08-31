package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// ── the harness image allowlist, controller side ────────────────────────────
//
// The webhook checks this too, and earlier. It is a separate, optional
// deployment though, and in harness mode the image is the agent process rather
// than an accessory to it — so a cluster without the webhook would otherwise
// have no bound at all on which external harness runs.

func policyWithRegistries(name string, registries ...string) *sympoziumv1alpha1.SympoziumPolicy {
	return &sympoziumv1alpha1.SympoziumPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: sympoziumv1alpha1.SympoziumPolicySpec{
			ImagePolicy:   &sympoziumv1alpha1.ImagePolicySpec{AllowedRegistries: registries},
			HarnessPolicy: &sympoziumv1alpha1.HarnessPolicySpec{Enabled: true, AllowUnmetered: true},
		},
	}
}

func harnessRunWithImage(image string) *sympoziumv1alpha1.AgentRun {
	run := harnessModeRun(map[string]string{"image": image})
	return run
}

func TestValidatePolicy_RequiresExplicitHarnessOptIn(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy *sympoziumv1alpha1.SympoziumPolicy
	}{
		{name: "no-policy"},
		{name: "policy-disabled", policy: &sympoziumv1alpha1.SympoziumPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: "disabled", Namespace: "default"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := parityAgent()
			objects := []client.Object{}
			if tc.policy != nil {
				agent.Spec.PolicyRef = tc.policy.Name
				objects = append(objects, tc.policy)
			}
			run := harnessRunWithImage("ghcr.io/acme/my-harness:v1")
			objects = append(objects, run, agent)
			r := newAgentRunTestReconciler(t, objects...)

			err := r.validatePolicy(context.Background(), run)
			if err == nil || !strings.Contains(err.Error(), "harnessPolicy.enabled") {
				t.Fatalf("validatePolicy() error = %v, want explicit opt-in denial", err)
			}
		})
	}
}

func TestValidatePolicy_RejectsHarnessImageOutsideAllowedRegistries(t *testing.T) {
	agent := parityAgent()
	agent.Spec.PolicyRef = "locked-down"
	policy := policyWithRegistries("locked-down", "ghcr.io/sympozium-ai/")

	run := harnessRunWithImage("docker.io/someone/unvetted-harness:latest")
	r := newAgentRunTestReconciler(t, run, agent, policy)

	err := r.validatePolicy(context.Background(), run)
	if err == nil {
		t.Fatal("validatePolicy accepted a harness image outside allowedRegistries")
	}
	if !strings.Contains(err.Error(), "unvetted-harness") {
		t.Errorf("error should name the image, got: %v", err)
	}
}

func TestValidatePolicy_AllowsHarnessImageInsideAllowedRegistries(t *testing.T) {
	agent := parityAgent()
	agent.Spec.PolicyRef = "locked-down"
	policy := policyWithRegistries("locked-down", "ghcr.io/acme/")

	run := harnessRunWithImage("ghcr.io/acme/my-harness:v1")
	r := newAgentRunTestReconciler(t, run, agent, policy)

	if err := r.validatePolicy(context.Background(), run); err != nil {
		t.Errorf("validatePolicy rejected an allowed harness image: %v", err)
	}
}

// An empty allowlist means "no restriction", which is what the field
// documentation promises. Getting this backwards would break every cluster that
// binds a policy without an image policy.
func TestValidatePolicy_EmptyAllowlistDoesNotRestrict(t *testing.T) {
	agent := parityAgent()
	agent.Spec.PolicyRef = "open"
	policy := policyWithRegistries("open")

	run := harnessRunWithImage("docker.io/someone/anything:latest")
	r := newAgentRunTestReconciler(t, run, agent, policy)

	if err := r.validatePolicy(context.Background(), run); err != nil {
		t.Errorf("an empty allowedRegistries list must not restrict: %v", err)
	}
}

// The run fails rather than requeueing forever: validatePolicy's caller routes
// its error through failRun, which is what puts the reason on status.error.
func TestReconcilePending_HarnessImageRejectionFailsTheRun(t *testing.T) {
	agent := parityAgent()
	agent.Spec.PolicyRef = "locked-down"
	policy := policyWithRegistries("locked-down", "ghcr.io/sympozium-ai/")

	run := harnessRunWithImage("docker.io/someone/unvetted-harness:latest")
	r := newAgentRunTestReconciler(t, run, agent, policy)

	if _, err := r.reconcilePending(context.Background(), logr.Discard(), run); err != nil {
		t.Fatalf("reconcilePending returned error: %v", err)
	}

	var stored sympoziumv1alpha1.AgentRun
	if err := r.Client.Get(context.Background(), client.ObjectKeyFromObject(run), &stored); err != nil {
		t.Fatalf("get stored run: %v", err)
	}
	if stored.Status.Phase != sympoziumv1alpha1.AgentRunPhaseFailed {
		t.Fatalf("expected phase Failed, got %q", stored.Status.Phase)
	}
	if !strings.Contains(stored.Status.Error, "unvetted-harness") {
		t.Errorf("status.error should name the image, got %q", stored.Status.Error)
	}
	if stored.Status.JobName != "" {
		t.Errorf("a Job was created for a disallowed harness image: %q", stored.Status.JobName)
	}
}
