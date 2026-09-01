package controller

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func harnessSessionTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func readySessionRuntime() *sympoziumv1alpha1.AgentRuntime {
	return &sympoziumv1alpha1.AgentRuntime{
		ObjectMeta: metav1.ObjectMeta{Name: "pi-session", Namespace: "default"},
		Spec: sympoziumv1alpha1.AgentRuntimeSpec{
			Image: "ghcr.io/sympozium-ai/pi-session@sha256:" + strings.Repeat("a", 64), ContractVersion: "v1alpha2",
			Session: &sympoziumv1alpha1.AgentRuntimeSession{Protocol: "openai-chat", Port: 8080},
			Model:   &sympoziumv1alpha1.AgentRuntimeModel{Provider: "openai-compatible", Model: "qwen", BaseURL: "http://model/v1", AuthSecretRef: "model-key"},
		},
		Status: sympoziumv1alpha1.AgentRuntimeStatus{ResolvedImageDigest: "sha256:" + strings.Repeat("a", 64), Conditions: []metav1.Condition{{Type: sympoziumv1alpha1.AgentRuntimeReadyCondition, Status: metav1.ConditionTrue}}},
	}
}

func TestHarnessSessionCreatesPrivateHardenedWorkload(t *testing.T) {
	scheme := harnessSessionTestScheme(t)
	agent := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "analyst", Namespace: "default"}, Spec: sympoziumv1alpha1.AgentSpec{AuthRefs: []sympoziumv1alpha1.SecretRef{{Provider: "openai-compatible", Secret: "model-key"}}}}
	session := &sympoziumv1alpha1.HarnessSession{ObjectMeta: metav1.ObjectMeta{Name: "analyst-session", Namespace: "default"}, Spec: sympoziumv1alpha1.HarnessSessionSpec{AgentRef: "analyst", RuntimeRef: "pi-session"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(session).WithObjects(agent, readySessionRuntime(), session).Build()
	r := &HarnessSessionReconciler{Client: cl, Scheme: scheme}
	key := types.NamespacedName{Name: session.Name, Namespace: session.Namespace}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}

	var deployment appsv1.Deployment
	if err := cl.Get(context.Background(), key, &deployment); err != nil {
		t.Fatal(err)
	}
	pod := deployment.Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatal("session pod must disable service-account token automount")
	}
	if len(pod.Containers) != 1 || pod.Containers[0].SecurityContext == nil || pod.Containers[0].SecurityContext.AllowPrivilegeEscalation == nil || *pod.Containers[0].SecurityContext.AllowPrivilegeEscalation {
		t.Fatal("session container must prohibit privilege escalation")
	}
	if pod.Containers[0].Image != readySessionRuntime().Spec.Image {
		t.Fatalf("image = %q", pod.Containers[0].Image)
	}
	if pod.Containers[0].ReadinessProbe == nil || pod.Containers[0].ReadinessProbe.HTTPGet == nil || pod.Containers[0].ReadinessProbe.HTTPGet.Path != "/healthz" {
		t.Fatal("session container must wait for the adapter health endpoint before becoming Ready")
	}
	if got := len(pod.Containers[0].Env); got < len(allowedAuthSecretKeys) {
		t.Fatalf("got %d env vars, want allowlisted credential env vars", got)
	}
	var service corev1.Service
	if err := cl.Get(context.Background(), key, &service); err != nil {
		t.Fatal(err)
	}
	if service.Spec.Ports[0].Port != 8080 {
		t.Fatalf("service port = %d, want 8080", service.Spec.Ports[0].Port)
	}
}

func TestHarnessSessionFailsClosedForOneShotRuntime(t *testing.T) {
	scheme := harnessSessionTestScheme(t)
	agent := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "analyst", Namespace: "default"}}
	runtime := readySessionRuntime()
	runtime.Spec.ContractVersion = "v1alpha1"
	runtime.Spec.Session = nil
	session := &sympoziumv1alpha1.HarnessSession{ObjectMeta: metav1.ObjectMeta{Name: "analyst-session", Namespace: "default"}, Spec: sympoziumv1alpha1.HarnessSessionSpec{AgentRef: "analyst", RuntimeRef: "pi-session"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(session).WithObjects(agent, runtime, session).Build()
	r := &HarnessSessionReconciler{Client: cl, Scheme: scheme}
	key := types.NamespacedName{Name: session.Name, Namespace: session.Namespace}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
	var got sympoziumv1alpha1.HarnessSession
	if err := cl.Get(context.Background(), key, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.Phase != "Failed" || !strings.Contains(got.Status.Conditions[0].Message, "v1alpha2") {
		t.Fatalf("unexpected failed status: %#v", got.Status)
	}
}
