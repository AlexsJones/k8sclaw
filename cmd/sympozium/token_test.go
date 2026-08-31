package main

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func tokenTestClient(t *testing.T, objects ...runtime.Object) *fake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...)
}

func TestUIToken(t *testing.T) {
	reader := tokenTestClient(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sympozium-ui-token", Namespace: "sympozium-system"},
		Data:       map[string][]byte{"token": []byte(" dashboard-token ")},
	}).Build()

	token, err := uiToken(context.Background(), reader, "sympozium-system")
	if err != nil {
		t.Fatalf("uiToken() error = %v", err)
	}
	if token != "dashboard-token" {
		t.Errorf("uiToken() = %q, want trimmed token", token)
	}
}

func TestUITokenMissingSecret(t *testing.T) {
	_, err := uiToken(context.Background(), tokenTestClient(t).Build(), "sympozium-system")
	if err == nil || !strings.Contains(err.Error(), "was not found") {
		t.Errorf("uiToken() error = %v, want missing Secret guidance", err)
	}
}

func TestUITokenMissingValue(t *testing.T) {
	reader := tokenTestClient(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "sympozium-ui-token", Namespace: "sympozium-system"},
	}).Build()

	_, err := uiToken(context.Background(), reader, "sympozium-system")
	if err == nil || !strings.Contains(err.Error(), "no non-empty") {
		t.Errorf("uiToken() error = %v, want missing token-key guidance", err)
	}
}
