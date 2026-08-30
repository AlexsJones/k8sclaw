package webhook

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestHarnessIsolationRejectsLifecycleRBAC(t *testing.T) {
	run := capabilityRun(harnessTaskSpec(nil))
	run.Spec.Lifecycle = &sympoziumv1alpha1.LifecycleHooks{RBAC: []sympoziumv1alpha1.RBACRule{{
		APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"},
	}}}
	err := (&PolicyEnforcer{}).validateHarnessIsolation(context.Background(), run)
	if err == nil || !strings.Contains(err.Error(), "lifecycle RBAC") {
		t.Fatalf("error = %v, want lifecycle RBAC rejection", err)
	}
}

func TestHarnessIsolationRejectsHostAccessSkillPack(t *testing.T) {
	pack := &sympoziumv1alpha1.SkillPack{
		ObjectMeta: metav1.ObjectMeta{Name: "node-admin", Namespace: "default"},
		Spec: sympoziumv1alpha1.SkillPackSpec{Sidecar: &sympoziumv1alpha1.SkillSidecar{
			Image: "node-admin:latest",
			HostAccess: &sympoziumv1alpha1.HostAccessSpec{
				Enabled: true, HostNetwork: true,
			},
		}},
	}
	pe := enforcerWithSkillPack(t, pack)
	run := capabilityRun(harnessTaskSpec(nil))
	run.Spec.Skills = []sympoziumv1alpha1.SkillRef{{SkillPackRef: "node-admin"}}
	err := pe.validateHarnessIsolation(context.Background(), run)
	if err == nil || !strings.Contains(err.Error(), "host access") {
		t.Fatalf("error = %v, want host-access rejection", err)
	}
}
