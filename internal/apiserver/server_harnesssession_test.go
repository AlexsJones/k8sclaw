package apiserver

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

type harnessRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f harnessRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestHarnessSessionAPI_CreateAndList(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	agent := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: "analyst", Namespace: "team-a", UID: "agent-uid"}}
	server := NewServer(fake.NewClientBuilder().WithScheme(scheme).WithObjects(agent).Build(), nil, nil, logr.Discard())
	handler := server.Handler(nil)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/harness-sessions?namespace=team-a", bytes.NewBufferString(`{"name":"orphan","agentRef":"missing","runtimeRef":"pi-session"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("create for a missing Agent status = %d, want %d", response.Code, http.StatusNotFound)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/harness-sessions?namespace=team-a", bytes.NewBufferString(`{"name":"analyst-session","agentRef":"analyst","runtimeRef":"pi-session","idleTimeout":"1h"}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", response.Code, response.Body.String())
	}
	var created sympoziumv1alpha1.HarnessSession
	if err := server.client.Get(context.Background(), client.ObjectKey{Name: "analyst-session", Namespace: "team-a"}, &created); err != nil {
		t.Fatal(err)
	}
	if len(created.OwnerReferences) != 1 || created.OwnerReferences[0].Kind != "Agent" || created.OwnerReferences[0].UID != "agent-uid" {
		t.Fatalf("session is not garbage-collected with its Agent: %#v", created.OwnerReferences)
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

func TestHarnessSessionAPI_PatchLifecycleOnly(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := &sympoziumv1alpha1.HarnessSession{ObjectMeta: metav1.ObjectMeta{Name: "analyst", Namespace: "team-a"}, Spec: sympoziumv1alpha1.HarnessSessionSpec{AgentRef: "agent", RuntimeRef: "pi", DesiredState: "running"}}
	server := NewServer(fake.NewClientBuilder().WithScheme(scheme).WithObjects(session).Build(), nil, nil, logr.Discard())
	response := httptest.NewRecorder()
	server.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/api/v1/harness-sessions/analyst?namespace=team-a", bytes.NewBufferString(`{"desiredState":"stopped"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("patch status = %d: %s", response.Code, response.Body.String())
	}
	var updated sympoziumv1alpha1.HarnessSession
	if err := server.client.Get(context.Background(), client.ObjectKey{Name: "analyst", Namespace: "team-a"}, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Spec.DesiredState != "stopped" {
		t.Fatalf("desiredState = %q, want stopped", updated.Spec.DesiredState)
	}
}

func TestHarnessSessionRequestAuditTracksLifecycle(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := &sympoziumv1alpha1.HarnessSession{ObjectMeta: metav1.ObjectMeta{Name: "analyst", Namespace: "team-a"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(session).WithObjects(session).Build()
	server := NewServer(cl, nil, nil, logr.Discard())
	started := metav1.Now()
	server.recordHarnessSessionRequest(context.Background(), "team-a", "analyst", "request-1", "started", started, true)
	server.recordHarnessSessionRequest(context.Background(), "team-a", "analyst", "request-1", "succeeded", metav1.Now(), false)
	var got sympoziumv1alpha1.HarnessSession
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "analyst", Namespace: "team-a"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.RequestCount != 1 || got.Status.ActiveRequests != 0 || got.Status.ErrorCount != 0 {
		t.Fatalf("unexpected request counters: %#v", got.Status)
	}
	if got.Status.LastRequestID != "request-1" || got.Status.LastRequestState != "succeeded" || got.Status.LastActivityTime == nil || got.Status.LastRequestCompletedAt == nil {
		t.Fatalf("request audit was not recorded: %#v", got.Status)
	}
	if got.Status.UsageAccounting != "unavailable" {
		t.Fatalf("usage accounting = %q, want unavailable", got.Status.UsageAccounting)
	}
}

func TestHarnessSessionRequestAuditCountsCancellation(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := &sympoziumv1alpha1.HarnessSession{ObjectMeta: metav1.ObjectMeta{Name: "analyst", Namespace: "team-a"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(session).WithObjects(session).Build()
	server := NewServer(cl, nil, nil, logr.Discard())
	server.recordHarnessSessionRequest(context.Background(), "team-a", "analyst", "request-2", "started", metav1.Now(), true)
	server.recordHarnessSessionRequest(context.Background(), "team-a", "analyst", "request-2", "cancelled", metav1.Now(), false)
	var got sympoziumv1alpha1.HarnessSession
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "analyst", Namespace: "team-a"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.ActiveRequests != 0 || got.Status.ErrorCount != 1 || got.Status.LastRequestState != "cancelled" {
		t.Fatalf("cancellation audit was not recorded: %#v", got.Status)
	}
}

func TestHarnessSessionChatReturnsRequestIDAndAudit(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := &sympoziumv1alpha1.HarnessSession{
		ObjectMeta: metav1.ObjectMeta{Name: "analyst", Namespace: "team-a"},
		Spec:       sympoziumv1alpha1.HarnessSessionSpec{RuntimeRef: "pi", DesiredState: "running"},
		Status:     sympoziumv1alpha1.HarnessSessionStatus{Phase: "Ready", ServiceName: "analyst"},
	}
	runtime := &sympoziumv1alpha1.AgentRuntime{
		ObjectMeta: metav1.ObjectMeta{Name: "pi", Namespace: "team-a"},
		Spec:       sympoziumv1alpha1.AgentRuntimeSpec{ContractVersion: "v1alpha2", Session: &sympoziumv1alpha1.AgentRuntimeSession{Protocol: "openai-chat", Port: 8080}},
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(session).WithObjects(session, runtime).Build()
	server := NewServer(cl, nil, nil, logr.Discard())
	previousClient := harnessSessionProxyClient
	harnessSessionProxyClient = &http.Client{Transport: harnessRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(`{"choices":[{"message":{"content":"ok"}}]}`))}, nil
	})}
	t.Cleanup(func() { harnessSessionProxyClient = previousClient })

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/harness-sessions/analyst/chat?namespace=team-a", bytes.NewBufferString(`{"messages":[{"role":"user","content":"hello"}]}`))
	server.Handler(nil).ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Sympozium-Request-ID") == "" {
		t.Fatalf("chat response did not expose its request ID: %d %#v", response.Code, response.Header())
	}
	var got sympoziumv1alpha1.HarnessSession
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "analyst", Namespace: "team-a"}, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status.RequestCount != 1 || got.Status.ActiveRequests != 0 || got.Status.LastRequestState != "succeeded" || got.Status.LastRequestID != response.Header().Get("X-Sympozium-Request-ID") {
		t.Fatalf("chat audit does not match the response: %#v", got.Status)
	}
}

func TestHarnessSessionAPI_RetryOfFailedSessionTriggersReconcile(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := &sympoziumv1alpha1.HarnessSession{
		ObjectMeta: metav1.ObjectMeta{Name: "analyst", Namespace: "team-a"},
		Spec:       sympoziumv1alpha1.HarnessSessionSpec{AgentRef: "agent", RuntimeRef: "pi", DesiredState: "running"},
		Status:     sympoziumv1alpha1.HarnessSessionStatus{Phase: "Failed"},
	}
	server := NewServer(fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(session).WithObjects(session).Build(), nil, nil, logr.Discard())
	response := httptest.NewRecorder()
	server.Handler(nil).ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/api/v1/harness-sessions/analyst?namespace=team-a", bytes.NewBufferString(`{"desiredState":"running"}`)))
	if response.Code != http.StatusOK {
		t.Fatalf("patch status = %d: %s", response.Code, response.Body.String())
	}
	var updated sympoziumv1alpha1.HarnessSession
	if err := server.client.Get(context.Background(), client.ObjectKey{Name: "analyst", Namespace: "team-a"}, &updated); err != nil {
		t.Fatal(err)
	}
	// An unchanged spec produces no watch event; the retry must still reach the controller.
	if updated.Annotations[harnessSessionRetryAnnotation] == "" {
		t.Fatalf("retry of a Failed session did not record a change for the controller: %#v", updated.ObjectMeta)
	}
}

func TestHarnessSessionRequestAuditSurvivesConcurrentWriters(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	session := &sympoziumv1alpha1.HarnessSession{ObjectMeta: metav1.ObjectMeta{Name: "analyst", Namespace: "team-a"}}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(session).WithObjects(session).Build()
	server := NewServer(cl, nil, nil, logr.Discard())
	const writers = 8
	done := make(chan struct{})
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			id := fmt.Sprintf("request-%d", i)
			server.recordHarnessSessionRequest(context.Background(), "team-a", "analyst", id, "started", metav1.Now(), true)
			server.recordHarnessSessionRequest(context.Background(), "team-a", "analyst", id, "succeeded", metav1.Now(), false)
		}(i)
	}
	for i := 0; i < writers; i++ {
		<-done
	}
	var got sympoziumv1alpha1.HarnessSession
	if err := cl.Get(context.Background(), client.ObjectKey{Name: "analyst", Namespace: "team-a"}, &got); err != nil {
		t.Fatal(err)
	}
	// Lost increments would either undercount requests or strand ActiveRequests
	// above zero, which permanently blocks idle timeout.
	if got.Status.RequestCount != writers || got.Status.ActiveRequests != 0 {
		t.Fatalf("concurrent audit lost updates: requests=%d active=%d", got.Status.RequestCount, got.Status.ActiveRequests)
	}
}
