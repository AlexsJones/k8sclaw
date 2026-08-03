package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// newInstanceTestReconciler builds a AgentReconciler backed by a
// fake client pre-loaded with the supplied objects.
func newInstanceTestReconciler(t *testing.T, objs ...client.Object) (*AgentReconciler, client.Client) {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 scheme: %v", err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("add appsv1 scheme: %v", err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add sympozium scheme: %v", err)
	}

	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&sympoziumv1alpha1.Agent{}).
		Build()

	return &AgentReconciler{
		Client: cl,
		Scheme: scheme,
		Log:    logr.Discard(),
	}, cl
}

// simulateMemoryUpdate mimics what extractAndPersistMemory does: it reads the
// memory ConfigMap for an instance and patches it with new content, exactly as
// the AgentRun controller would after parsing __SYMPOZIUM_MEMORY__ markers
// from the agent's stdout.
func simulateMemoryUpdate(t *testing.T, cl client.Client, namespace, instanceName, newContent string) {
	t.Helper()
	ctx := context.Background()

	cmName := fmt.Sprintf("%s-memory", instanceName)
	var cm corev1.ConfigMap
	if err := cl.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, &cm); err != nil {
		t.Fatalf("get memory ConfigMap %q: %v", cmName, err)
	}
	cm.Data["MEMORY.md"] = newContent
	if err := cl.Update(ctx, &cm); err != nil {
		t.Fatalf("update memory ConfigMap %q: %v", cmName, err)
	}
}

// getMemoryContent reads the MEMORY.md value from the instance's ConfigMap.
func getMemoryContent(t *testing.T, cl client.Client, namespace, instanceName string) string {
	t.Helper()
	ctx := context.Background()

	cmName := fmt.Sprintf("%s-memory", instanceName)
	var cm corev1.ConfigMap
	if err := cl.Get(ctx, types.NamespacedName{Name: cmName, Namespace: namespace}, &cm); err != nil {
		t.Fatalf("get memory ConfigMap %q: %v", cmName, err)
	}
	return cm.Data["MEMORY.md"]
}

// ---------------------------------------------------------------------------
// Test: adhoc instance memory grows across successive prompts
// ---------------------------------------------------------------------------

func TestInstanceMemory_AdhocInstance_GrowsAcrossPrompts(t *testing.T) {
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "adhoc-test",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: "claude-sonnet-4-20250514",
				},
			},
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "anthropic", Secret: "CLAUDE_TOKEN"},
			},
			Memory: &sympoziumv1alpha1.MemorySpec{
				Enabled:   true,
				MaxSizeKB: 256,
			},
		},
	}

	r, cl := newInstanceTestReconciler(t, instance)

	// --- Reconcile 1: should create the memory ConfigMap with initial content ---
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "adhoc-test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile 1: %v", err)
	}

	initial := getMemoryContent(t, cl, "default", "adhoc-test")
	if !strings.Contains(initial, "No memories recorded yet") {
		t.Fatalf("initial memory should contain default content, got: %q", initial)
	}
	initialLen := len(initial)

	// --- Prompt 1: agent responds with a memory update ---
	prompt1Memory := "# Agent Memory\n\n## User Preferences\n- Prefers concise answers\n- Uses Go professionally\n"
	simulateMemoryUpdate(t, cl, "default", "adhoc-test", prompt1Memory)

	content1 := getMemoryContent(t, cl, "default", "adhoc-test")
	if content1 != prompt1Memory {
		t.Fatalf("after prompt 1, memory = %q, want %q", content1, prompt1Memory)
	}
	if len(content1) <= initialLen {
		t.Fatalf("memory should have grown: initial=%d, after prompt 1=%d", initialLen, len(content1))
	}

	// --- Reconcile 2: controller reconciles again; ConfigMap already exists, no overwrite ---
	_, err = r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "adhoc-test", Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile 2: %v", err)
	}

	afterReconcile2 := getMemoryContent(t, cl, "default", "adhoc-test")
	if afterReconcile2 != prompt1Memory {
		t.Fatalf("reconcile should not overwrite existing memory; got %q, want %q", afterReconcile2, prompt1Memory)
	}

	// --- Prompt 2: agent appends more context ---
	prompt2Memory := prompt1Memory +
		"\n## Project Context\n- Working on sympozium Kubernetes operator\n- Stack: Go, controller-runtime, Helm\n"
	simulateMemoryUpdate(t, cl, "default", "adhoc-test", prompt2Memory)

	content2 := getMemoryContent(t, cl, "default", "adhoc-test")
	if content2 != prompt2Memory {
		t.Fatalf("after prompt 2, memory = %q, want %q", content2, prompt2Memory)
	}
	if len(content2) <= len(content1) {
		t.Fatalf("memory should have grown: after prompt 1=%d, after prompt 2=%d", len(content1), len(content2))
	}

	// --- Prompt 3: agent appends even more ---
	prompt3Memory := prompt2Memory +
		"\n## Session Notes\n- Discussed testing strategies for memory persistence\n- User wants realistic integration-style tests\n"
	simulateMemoryUpdate(t, cl, "default", "adhoc-test", prompt3Memory)

	content3 := getMemoryContent(t, cl, "default", "adhoc-test")
	if !strings.Contains(content3, "User Preferences") {
		t.Error("memory should still contain User Preferences section")
	}
	if !strings.Contains(content3, "Project Context") {
		t.Error("memory should still contain Project Context section")
	}
	if !strings.Contains(content3, "Session Notes") {
		t.Error("memory should contain new Session Notes section")
	}
	if len(content3) <= len(content2) {
		t.Fatalf("memory should have grown: after prompt 2=%d, after prompt 3=%d", len(content2), len(content3))
	}

	t.Logf("memory growth: initial=%d → prompt1=%d → prompt2=%d → prompt3=%d",
		initialLen, len(content1), len(content2), len(content3))
}

// ---------------------------------------------------------------------------
// Test: Ensemble-created instance memory grows across successive prompts
// ---------------------------------------------------------------------------

func TestInstanceMemory_EnsembleInstance_GrowsAcrossPrompts(t *testing.T) {
	// A Ensemble creates instances named "<pack>-<persona>".
	// This test simulates the instance that would be created by a Ensemble
	// with a "devops-assistant" persona.
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "mypack-devops-assistant",
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/personapack":  "mypack",
				"sympozium.ai/agent-config": "devops-assistant",
			},
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: "claude-sonnet-4-20250514",
				},
			},
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "anthropic", Secret: "CLAUDE_TOKEN"},
			},
			Memory: &sympoziumv1alpha1.MemorySpec{
				Enabled:      true,
				MaxSizeKB:    128,
				SystemPrompt: "You are a DevOps assistant. Track infrastructure changes and deployment patterns.",
			},
		},
	}

	r, cl := newInstanceTestReconciler(t, instance)

	// Reconcile creates the memory ConfigMap.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: instance.Name, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	initial := getMemoryContent(t, cl, "default", instance.Name)
	if !strings.Contains(initial, "No memories recorded yet") {
		t.Fatalf("initial memory should contain default content, got: %q", initial)
	}

	// Feed 1: scheduled heartbeat produces memory about cluster state.
	feed1 := "# Agent Memory\n\n## Cluster State\n- 3 nodes healthy\n- Deployed sympozium v0.0.25\n"
	simulateMemoryUpdate(t, cl, "default", instance.Name, feed1)
	if got := getMemoryContent(t, cl, "default", instance.Name); got != feed1 {
		t.Fatalf("feed 1 memory mismatch: got %q", got)
	}

	// Feed 2: user asks about deployments via channel; memory accumulates.
	feed2 := feed1 + "\n## Deployment History\n- 2024-03-10: rolled out v0.0.25 to prod\n- User asked about rollback procedures\n"
	simulateMemoryUpdate(t, cl, "default", instance.Name, feed2)
	if got := getMemoryContent(t, cl, "default", instance.Name); got != feed2 {
		t.Fatalf("feed 2 memory mismatch: got %q", got)
	}

	// Feed 3: another heartbeat adds more observations.
	feed3 := feed2 + "\n## Alerts\n- Pod restarts detected in staging namespace\n- Memory usage trending up on node-2\n"
	simulateMemoryUpdate(t, cl, "default", instance.Name, feed3)

	final := getMemoryContent(t, cl, "default", instance.Name)
	if !strings.Contains(final, "Cluster State") {
		t.Error("final memory should contain Cluster State")
	}
	if !strings.Contains(final, "Deployment History") {
		t.Error("final memory should contain Deployment History")
	}
	if !strings.Contains(final, "Alerts") {
		t.Error("final memory should contain Alerts")
	}

	// Verify monotonic growth.
	if len(feed1) >= len(feed2) || len(feed2) >= len(feed3) {
		t.Fatalf("memory did not grow monotonically: feed1=%d, feed2=%d, feed3=%d",
			len(feed1), len(feed2), len(feed3))
	}

	t.Logf("persona memory growth: initial=%d → feed1=%d → feed2=%d → feed3=%d",
		len(initial), len(feed1), len(feed2), len(feed3))
}

// ---------------------------------------------------------------------------
// Test: memory ConfigMap is not created when memory is disabled
// ---------------------------------------------------------------------------

func TestInstanceMemory_DisabledDoesNotCreateConfigMap(t *testing.T) {
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "no-memory-instance",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: "gpt-4o",
				},
			},
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "openai", Secret: "CLAUDE_TOKEN"},
			},
			// Memory is nil → disabled.
		},
	}

	r, cl := newInstanceTestReconciler(t, instance)

	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: instance.Name, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// The memory ConfigMap should NOT exist.
	cmName := fmt.Sprintf("%s-memory", instance.Name)
	var cm corev1.ConfigMap
	err = cl.Get(context.Background(), types.NamespacedName{Name: cmName, Namespace: "default"}, &cm)
	if err == nil {
		t.Fatalf("memory ConfigMap %q should not exist when memory is disabled", cmName)
	}
}

// ---------------------------------------------------------------------------
// Test: reconcile does not overwrite agent-updated memory
// ---------------------------------------------------------------------------

func TestInstanceMemory_ReconcilePreservesExistingContent(t *testing.T) {
	instance := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "preserve-test",
			Namespace: "default",
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model: "claude-sonnet-4-20250514",
				},
			},
			AuthRefs: []sympoziumv1alpha1.SecretRef{
				{Provider: "anthropic", Secret: "CLAUDE_TOKEN"},
			},
			Memory: &sympoziumv1alpha1.MemorySpec{
				Enabled: true,
			},
		},
	}

	// Pre-create the memory ConfigMap with agent-written content (simulates
	// an agent that already ran and updated memory before this reconcile).
	existingContent := "# Agent Memory\n\n## Important\n- User's production cluster is in us-east-1\n- Always use blue-green deployments\n"
	memoryCM := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "preserve-test-memory",
			Namespace: "default",
			Labels: map[string]string{
				"sympozium.ai/instance":  "preserve-test",
				"sympozium.ai/component": "memory",
			},
		},
		Data: map[string]string{
			"MEMORY.md": existingContent,
		},
	}

	r, cl := newInstanceTestReconciler(t, instance, memoryCM)

	// Reconcile should see the ConfigMap already exists and NOT overwrite it.
	_, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: instance.Name, Namespace: "default"},
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := getMemoryContent(t, cl, "default", instance.Name)
	if got != existingContent {
		t.Fatalf("reconcile overwrote existing memory!\ngot:  %q\nwant: %q", got, existingContent)
	}
}
