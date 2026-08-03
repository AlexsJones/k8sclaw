package controller

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// ── syncMemoryAdminTokenEnv ──────────────────────────────────────────────────
//
// reconcileMemoryDeployment is create-only for the bulk of the Deployment spec,
// so an existing memory Deployment would never pick up the admin-delete token
// after the operator enabled it. These pin that the env var alone is reconciled.

// memoryDeploy builds a minimal memory Deployment with the given memory-server
// env, shaped like the one reconcileMemoryDeployment creates.
func memoryDeploy(name, namespace string, env []corev1.EnvVar, extraContainers ...corev1.Container) *appsv1.Deployment {
	memory := corev1.Container{Name: "memory-server", Image: "skill-memory:latest", Env: env}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: append(extraContainers, memory)},
			},
		},
	}
}

// tokenEnvOf returns the MEMORY_ADMIN_TOKEN env var on the memory-server
// container, if present.
func tokenEnvOf(t *testing.T, cl client.Client, name, namespace string) (corev1.EnvVar, bool) {
	t.Helper()
	var got appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: name, Namespace: namespace}, &got); err != nil {
		t.Fatalf("get deployment %q: %v", name, err)
	}
	c := memoryServerContainer(&got.Spec.Template.Spec)
	if c == nil {
		t.Fatal("memory-server container not found")
	}
	for _, e := range c.Env {
		if e.Name == "MEMORY_ADMIN_TOKEN" {
			return e, true
		}
	}
	return corev1.EnvVar{}, false
}

func TestSyncMemoryAdminTokenEnv_AddsWhenEnabled(t *testing.T) {
	t.Setenv("MEMORY_ADMIN_TOKEN_SECRET", "sympozium-memory-admin-token")

	deploy := memoryDeploy("agent-memory", "default", []corev1.EnvVar{
		{Name: "MEMORY_DB_PATH", Value: "/data/memory.db"},
	})
	_, cl := newInstanceTestReconciler(t, deploy)

	if err := syncMemoryAdminTokenEnv(context.Background(), cl, logr.Discard(), deploy); err != nil {
		t.Fatalf("syncMemoryAdminTokenEnv: %v", err)
	}

	env, ok := tokenEnvOf(t, cl, "agent-memory", "default")
	if !ok {
		t.Fatal("MEMORY_ADMIN_TOKEN not added to existing memory Deployment")
	}
	ref := env.ValueFrom.SecretKeyRef
	if ref.Name != "sympozium-memory-admin-token" {
		t.Errorf("secret name = %q, want sympozium-memory-admin-token", ref.Name)
	}
	if ref.Key != "token" {
		t.Errorf("secret key = %q, want token", ref.Key)
	}
	if ref.Optional == nil || !*ref.Optional {
		t.Error("SecretKeyRef must stay optional so a missing Secret cannot block pod startup")
	}
	if env.Value != "" {
		t.Errorf("token must come from the Secret, not a literal value (got %q)", env.Value)
	}
}

func TestSyncMemoryAdminTokenEnv_RemovesWhenDisabled(t *testing.T) {
	t.Setenv("MEMORY_ADMIN_TOKEN_SECRET", "")

	deploy := memoryDeploy("agent-memory", "default", []corev1.EnvVar{
		{Name: "MEMORY_DB_PATH", Value: "/data/memory.db"},
		{Name: "MEMORY_ADMIN_TOKEN", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "old-secret"},
				Key:                  "token",
			},
		}},
	})
	_, cl := newInstanceTestReconciler(t, deploy)

	if err := syncMemoryAdminTokenEnv(context.Background(), cl, logr.Discard(), deploy); err != nil {
		t.Fatalf("syncMemoryAdminTokenEnv: %v", err)
	}

	if _, ok := tokenEnvOf(t, cl, "agent-memory", "default"); ok {
		t.Error("MEMORY_ADMIN_TOKEN should be removed when adminDelete is switched off")
	}

	// The unrelated env must survive.
	var got appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "agent-memory", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	c := memoryServerContainer(&got.Spec.Template.Spec)
	if len(c.Env) != 1 || c.Env[0].Name != "MEMORY_DB_PATH" {
		t.Errorf("removal clobbered unrelated env: %+v", c.Env)
	}
}

func TestSyncMemoryAdminTokenEnv_RepointsToNewSecret(t *testing.T) {
	t.Setenv("MEMORY_ADMIN_TOKEN_SECRET", "new-secret")

	deploy := memoryDeploy("agent-memory", "default", []corev1.EnvVar{
		{Name: "MEMORY_ADMIN_TOKEN", ValueFrom: &corev1.EnvVarSource{
			SecretKeyRef: &corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "old-secret"},
				Key:                  "token",
			},
		}},
	})
	_, cl := newInstanceTestReconciler(t, deploy)

	if err := syncMemoryAdminTokenEnv(context.Background(), cl, logr.Discard(), deploy); err != nil {
		t.Fatalf("syncMemoryAdminTokenEnv: %v", err)
	}

	env, ok := tokenEnvOf(t, cl, "agent-memory", "default")
	if !ok {
		t.Fatal("MEMORY_ADMIN_TOKEN missing after repoint")
	}
	if got := env.ValueFrom.SecretKeyRef.Name; got != "new-secret" {
		t.Errorf("secret name = %q, want new-secret", got)
	}
}

func TestSyncMemoryAdminTokenEnv_NoWriteWhenAlreadyCorrect(t *testing.T) {
	t.Setenv("MEMORY_ADMIN_TOKEN_SECRET", "sympozium-memory-admin-token")

	deploy := memoryDeploy("agent-memory", "default", memoryAdminTokenEnv())
	_, cl := newInstanceTestReconciler(t, deploy)

	var before appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "agent-memory", Namespace: "default"}, &before); err != nil {
		t.Fatalf("get deployment: %v", err)
	}

	if err := syncMemoryAdminTokenEnv(context.Background(), cl, logr.Discard(), deploy); err != nil {
		t.Fatalf("syncMemoryAdminTokenEnv: %v", err)
	}

	var after appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "agent-memory", Namespace: "default"}, &after); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	// A steady-state reconcile must not bump resourceVersion — an Update on every
	// pass would churn the Deployment and restart the memory pod.
	if before.ResourceVersion != after.ResourceVersion {
		t.Errorf("steady-state reconcile issued a write: resourceVersion %s -> %s",
			before.ResourceVersion, after.ResourceVersion)
	}
}

func TestSyncMemoryAdminTokenEnv_MatchesContainerByName(t *testing.T) {
	t.Setenv("MEMORY_ADMIN_TOKEN_SECRET", "sympozium-memory-admin-token")

	// A sidecar injected ahead of memory-server must not receive the token.
	sidecar := corev1.Container{Name: "istio-proxy", Image: "proxy:latest"}
	deploy := memoryDeploy("agent-memory", "default", nil, sidecar)
	_, cl := newInstanceTestReconciler(t, deploy)

	if err := syncMemoryAdminTokenEnv(context.Background(), cl, logr.Discard(), deploy); err != nil {
		t.Fatalf("syncMemoryAdminTokenEnv: %v", err)
	}

	var got appsv1.Deployment
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "agent-memory", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	for _, c := range got.Spec.Template.Spec.Containers {
		for _, e := range c.Env {
			if e.Name == "MEMORY_ADMIN_TOKEN" && c.Name != "memory-server" {
				t.Errorf("admin token leaked into container %q", c.Name)
			}
		}
	}
	if _, ok := tokenEnvOf(t, cl, "agent-memory", "default"); !ok {
		t.Error("memory-server container did not receive the token")
	}
}

func TestSyncMemoryAdminTokenEnv_NoMemoryServerContainer(t *testing.T) {
	t.Setenv("MEMORY_ADMIN_TOKEN_SECRET", "sympozium-memory-admin-token")

	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-memory", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "something-else"}}},
			},
		},
	}
	_, cl := newInstanceTestReconciler(t, deploy)

	if err := syncMemoryAdminTokenEnv(context.Background(), cl, logr.Discard(), deploy); err != nil {
		t.Fatalf("expected a no-op, got error: %v", err)
	}
}

// ── reconcileMemoryDeployment entry point ────────────────────────────────────

// TestReconcileMemoryDeployment_UpdatesTokenOnExisting pins the actual bug: the
// early "already exists" return used to skip the token entirely, so enabling
// adminDelete required deleting the Deployment by hand.
func TestReconcileMemoryDeployment_UpdatesTokenOnExisting(t *testing.T) {
	t.Setenv("MEMORY_ADMIN_TOKEN_SECRET", "sympozium-memory-admin-token")

	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentSpec{
			Skills: []sympoziumv1alpha1.SkillRef{{SkillPackRef: "memory"}},
		},
	}
	deploy := memoryDeploy("agent-memory", "default", []corev1.EnvVar{
		{Name: "MEMORY_DB_PATH", Value: "/data/memory.db"},
	})
	r, cl := newInstanceTestReconciler(t, instance, deploy)

	if err := r.reconcileMemoryDeployment(context.Background(), logr.Discard(), instance); err != nil {
		t.Fatalf("reconcileMemoryDeployment: %v", err)
	}

	if _, ok := tokenEnvOf(t, cl, "agent-memory", "default"); !ok {
		t.Error("existing memory Deployment did not pick up MEMORY_ADMIN_TOKEN on reconcile")
	}
}

// TestReconcileSharedMemory_UpdatesTokenOnExisting is the Ensemble-side mirror:
// shared workflow memory is created by a different reconciler with the same
// create-only guard, so workflow_memory_* entries would otherwise stay
// undeletable after enabling adminDelete.
func TestReconcileSharedMemory_UpdatesTokenOnExisting(t *testing.T) {
	t.Setenv("MEMORY_ADMIN_TOKEN_SECRET", "sympozium-memory-admin-token")

	pack := &sympoziumv1alpha1.Ensemble{
		ObjectMeta: metav1.ObjectMeta{Name: "crew", Namespace: "default"},
		Spec: sympoziumv1alpha1.EnsembleSpec{
			SharedMemory: &sympoziumv1alpha1.SharedMemorySpec{Enabled: true},
		},
	}
	// Pre-create the PVC and Deployment so the reconcile takes the "already
	// exists" path for both.
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "crew-shared-memory-db", Namespace: "default"},
	}
	deploy := memoryDeploy("crew-shared-memory", "default", []corev1.EnvVar{
		{Name: "MEMORY_DB_PATH", Value: "/data/memory.db"},
	})

	agentR, cl := newInstanceTestReconciler(t, pack, pvc, deploy)
	r := &EnsembleReconciler{Client: cl, Scheme: agentR.Scheme, Log: logr.Discard()}

	if err := r.reconcileSharedMemory(context.Background(), logr.Discard(), pack); err != nil {
		t.Fatalf("reconcileSharedMemory: %v", err)
	}

	if _, ok := tokenEnvOf(t, cl, "crew-shared-memory", "default"); !ok {
		t.Error("existing shared memory Deployment did not pick up MEMORY_ADMIN_TOKEN on reconcile")
	}
}
