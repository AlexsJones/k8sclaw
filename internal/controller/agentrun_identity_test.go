package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestHarnessReceivesNoKubernetesToken(t *testing.T) {
	r := &AgentRunReconciler{}
	job, err := r.buildJob(context.Background(), harnessModeRun(nil), false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("build harness job: %v", err)
	}

	spec := job.Spec.Template.Spec
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatal("harness pod must set automountServiceAccountToken=false")
	}
	if hasVolume(spec.Volumes, runAPIAccessVolumeName) {
		t.Fatal("harness-only pod must not contain a projected Kubernetes API token")
	}
	for _, container := range spec.Containers {
		if hasMount(container.VolumeMounts, runAPIAccessVolumeName) {
			t.Errorf("container %q unexpectedly receives Kubernetes credentials", container.Name)
		}
	}
}

func TestHarnessRejectsUnisolatedPrivilegeCombinations(t *testing.T) {
	t.Run("lifecycle RBAC", func(t *testing.T) {
		run := harnessModeRun(nil)
		run.Spec.Lifecycle = &sympoziumv1alpha1.LifecycleHooks{RBAC: []sympoziumv1alpha1.RBACRule{{
			APIGroups: []string{""}, Resources: []string{"secrets"}, Verbs: []string{"get"},
		}}}
		if err := validateHarnessIsolation(run, nil); err == nil {
			t.Fatal("expected harness plus lifecycle RBAC to be rejected")
		}
	})

	t.Run("host access SkillPack", func(t *testing.T) {
		run := harnessModeRun(nil)
		sidecars := []resolvedSidecar{{
			skillPackName: "node-admin",
			sidecar: sympoziumv1alpha1.SkillSidecar{HostAccess: &sympoziumv1alpha1.HostAccessSpec{
				Enabled: true, HostPID: true,
			}},
		}}
		if err := validateHarnessIsolation(run, sidecars); err == nil {
			t.Fatal("expected harness plus host-access SkillPack to be rejected")
		}
	})
}

func TestRunAPIAccessMountedOnlyIntoRBACSidecars(t *testing.T) {
	r := &AgentRunReconciler{}
	run := newTestRun()
	sidecars := []resolvedSidecar{
		{
			skillPackName: "k8s-reader",
			sidecar: sympoziumv1alpha1.SkillSidecar{
				Image: "reader:latest",
				RBAC: []sympoziumv1alpha1.RBACRule{{
					APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get"},
				}},
			},
		},
		{
			skillPackName: "calculator",
			sidecar:       sympoziumv1alpha1.SkillSidecar{Image: "calculator:latest"},
		},
	}

	job, err := r.buildJob(context.Background(), run, false, nil, sidecars, nil, nil)
	if err != nil {
		t.Fatalf("build job: %v", err)
	}
	spec := job.Spec.Template.Spec
	if !hasVolume(spec.Volumes, runAPIAccessVolumeName) {
		t.Fatal("RBAC sidecar requires a projected Kubernetes API token volume")
	}

	for _, name := range []string{"agent", "ipc-bridge", "skill-calculator"} {
		container := containerByName(spec.Containers, name)
		if container == nil {
			t.Fatalf("missing container %q", name)
		}
		if hasMount(container.VolumeMounts, runAPIAccessVolumeName) {
			t.Errorf("untrusted container %q receives Kubernetes credentials", name)
		}
	}
	reader := containerByName(spec.Containers, "skill-k8s-reader")
	if reader == nil || !hasMount(reader.VolumeMounts, runAPIAccessVolumeName) {
		t.Fatal("RBAC-declaring SkillPack sidecar does not receive its projected token")
	}

	for _, volume := range spec.Volumes {
		if volume.Name != runAPIAccessVolumeName {
			continue
		}
		projection := volume.Projected.Sources[0].ServiceAccountToken
		if projection == nil || projection.ExpirationSeconds == nil || *projection.ExpirationSeconds != 600 {
			t.Fatalf("token projection expiration = %#v, want 600 seconds", projection)
		}
	}
}

func TestLifecycleTokenProjectionExcludesAgentAndDoneContainers(t *testing.T) {
	r := &AgentRunReconciler{}
	rules := []sympoziumv1alpha1.RBACRule{{
		APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"},
	}}
	run := newTestRunWithLifecycle(
		[]sympoziumv1alpha1.LifecycleHookContainer{{Name: "prepare", Image: "kubectl:latest"}},
		[]sympoziumv1alpha1.LifecycleHookContainer{{Name: "finish", Image: "kubectl:latest"}},
		rules,
	)

	job, err := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("build main job: %v", err)
	}
	if !hasMount(job.Spec.Template.Spec.InitContainers[0].VolumeMounts, runAPIAccessVolumeName) {
		t.Fatal("preRun RBAC hook lacks projected token")
	}
	if hasMount(job.Spec.Template.Spec.Containers[0].VolumeMounts, runAPIAccessVolumeName) {
		t.Fatal("primary agent received lifecycle hook credentials")
	}

	post := r.buildPostRunJob(run, 0, "done")
	if !hasMount(post.Spec.Template.Spec.InitContainers[0].VolumeMounts, runAPIAccessVolumeName) {
		t.Fatal("postRun RBAC hook lacks projected token")
	}
	if hasMount(post.Spec.Template.Spec.Containers[0].VolumeMounts, runAPIAccessVolumeName) {
		t.Fatal("postRun sentinel container received lifecycle credentials")
	}
}

func TestCanaryUsesItsOwnIdentityAndFixedRBAC(t *testing.T) {
	run := newTestRun()
	run.Name = "canary-run"
	run.UID = types.UID("canary-uid")
	run.Spec.CanaryMode = true
	r := newAgentRunTestReconciler(t, run)

	if err := r.ensureAgentServiceAccount(context.Background(), run); err != nil {
		t.Fatalf("ensure service account: %v", err)
	}
	if err := r.ensureCanaryRBAC(context.Background(), run); err != nil {
		t.Fatalf("ensure canary RBAC: %v", err)
	}

	var binding rbacv1.ClusterRoleBinding
	if err := r.Get(context.Background(), types.NamespacedName{Name: "sympozium-canary-canary-run"}, &binding); err != nil {
		t.Fatalf("get canary ClusterRoleBinding: %v", err)
	}
	if len(binding.Subjects) != 1 || binding.Subjects[0].Name != "sympozium-run-canary-run" {
		t.Fatalf("canary binding subjects = %#v", binding.Subjects)
	}

	job, err := r.buildJob(context.Background(), run, false, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("build canary job: %v", err)
	}
	agent := containerByName(job.Spec.Template.Spec.Containers, "agent")
	bridge := containerByName(job.Spec.Template.Spec.Containers, "ipc-bridge")
	if agent == nil || !hasMount(agent.VolumeMounts, runAPIAccessVolumeName) {
		t.Fatal("trusted canary runner lacks projected Kubernetes token")
	}
	if bridge == nil || hasMount(bridge.VolumeMounts, runAPIAccessVolumeName) {
		t.Fatal("ipc-bridge received canary Kubernetes credentials")
	}
}

func TestRunServiceAccountsAndBindingsAreIsolated(t *testing.T) {
	runA := newTestRun()
	runA.Name = "run-a"
	runA.UID = types.UID("uid-a")
	runA.Spec.Lifecycle = &sympoziumv1alpha1.LifecycleHooks{RBAC: []sympoziumv1alpha1.RBACRule{{
		APIGroups: []string{""}, Resources: []string{"configmaps"}, Verbs: []string{"get"},
	}}}
	runB := runA.DeepCopy()
	runB.Name = "run-b"
	runB.UID = types.UID("uid-b")

	template := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{
		Name: legacyAgentServiceAccountName, Namespace: "default",
		Annotations: map[string]string{"eks.amazonaws.com/role-arn": "arn:example"},
	}}
	staleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "sympozium-lifecycle-run-a", Namespace: "default"},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io", Kind: "Role", Name: "sympozium-lifecycle-run-a",
		},
		Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: legacyAgentServiceAccountName, Namespace: "default"}},
	}
	r := newAgentRunTestReconciler(t, runA, runB, template, staleBinding)
	for _, run := range []*sympoziumv1alpha1.AgentRun{runA, runB} {
		if err := r.ensureAgentServiceAccount(context.Background(), run); err != nil {
			t.Fatalf("ensure service account for %s: %v", run.Name, err)
		}
		if err := r.ensureLifecycleRBAC(context.Background(), logr.Discard(), run); err != nil {
			t.Fatalf("ensure lifecycle RBAC for %s: %v", run.Name, err)
		}
	}

	for _, run := range []*sympoziumv1alpha1.AgentRun{runA, runB} {
		wantSA := agentRunServiceAccountName(run)
		var sa corev1.ServiceAccount
		if err := r.Get(context.Background(), types.NamespacedName{Name: wantSA, Namespace: run.Namespace}, &sa); err != nil {
			t.Fatalf("get service account %s: %v", wantSA, err)
		}
		if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
			t.Errorf("service account %s does not disable automount", wantSA)
		}
		if sa.Annotations["eks.amazonaws.com/role-arn"] != "arn:example" {
			t.Errorf("service account %s did not inherit workload-identity annotation", wantSA)
		}
		if len(sa.OwnerReferences) != 1 || sa.OwnerReferences[0].UID != run.UID {
			t.Errorf("service account %s owner = %#v, want AgentRun %s", wantSA, sa.OwnerReferences, run.Name)
		}

		var binding rbacv1.RoleBinding
		bindingName := "sympozium-lifecycle-" + run.Name
		if err := r.Get(context.Background(), types.NamespacedName{Name: bindingName, Namespace: run.Namespace}, &binding); err != nil {
			t.Fatalf("get RoleBinding %s: %v", bindingName, err)
		}
		if len(binding.Subjects) != 1 || binding.Subjects[0].Name != wantSA {
			t.Errorf("RoleBinding %s subjects = %#v, want only %s", bindingName, binding.Subjects, wantSA)
		}
	}
}
