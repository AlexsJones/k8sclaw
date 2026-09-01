package apiserver

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

func TestHarnessSessionAPI_CreateAndList(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	server := NewServer(fake.NewClientBuilder().WithScheme(scheme).Build(), nil, nil, logr.Discard())
	handler := server.Handler(nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/harness-sessions?namespace=team-a", bytes.NewBufferString(`{"name":"analyst-session","agentRef":"analyst","runtimeRef":"pi-session","idleTimeout":"1h"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/harness-sessions?namespace=team-a", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte("analyst-session")) {
		t.Fatalf("list failed: %d %s", response.Code, response.Body.String())
	}
}

func TestHarnessSessionAPI_ChatRequiresReadyControllerOwnedService(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := &sympoziumv1alpha1.HarnessSession{ObjectMeta: metav1.ObjectMeta{Name: "not-ready", Namespace: "default"}}
	server := NewServer(fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build(), nil, nil, logr.Discard())
	response := httptest.NewRecorder()
	server.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/v1/harness-sessions/not-ready/chat", bytes.NewBufferString(`{}`)))
	if response.Code != http.StatusConflict {
		t.Fatalf("chat status = %d, want %d", response.Code, http.StatusConflict)
	}
}
