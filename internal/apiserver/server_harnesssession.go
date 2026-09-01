package apiserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// The proxy is deliberately short-lived, bounded, and does not follow
// redirects. A session adapter is untrusted code; redirects must not turn its
// private Service connection into a request to another internal endpoint.
var harnessSessionProxyClient = &http.Client{
	Timeout: 2 * time.Minute,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type CreateHarnessSessionRequest struct {
	Name        string `json:"name"`
	AgentRef    string `json:"agentRef"`
	RuntimeRef  string `json:"runtimeRef"`
	IdleTimeout string `json:"idleTimeout,omitempty"`
}

func requestNamespace(r *http.Request) string {
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		return ns
	}
	return "default"
}

func (s *Server) listHarnessSessions(w http.ResponseWriter, r *http.Request) {
	var list sympoziumv1alpha1.HarnessSessionList
	if err := s.client.List(r.Context(), &list, client.InNamespace(requestNamespace(r))); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, list.Items)
}

func (s *Server) createHarnessSession(w http.ResponseWriter, r *http.Request) {
	var request CreateHarnessSessionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024)).Decode(&request); err != nil {
		http.Error(w, "invalid session request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(request.Name) == "" || strings.TrimSpace(request.AgentRef) == "" || strings.TrimSpace(request.RuntimeRef) == "" {
		http.Error(w, "name, agentRef, and runtimeRef are required", http.StatusBadRequest)
		return
	}
	session := &sympoziumv1alpha1.HarnessSession{ObjectMeta: metav1.ObjectMeta{Name: request.Name, Namespace: requestNamespace(r)}, Spec: sympoziumv1alpha1.HarnessSessionSpec{AgentRef: request.AgentRef, RuntimeRef: request.RuntimeRef, DesiredState: "running"}}
	if request.IdleTimeout != "" {
		duration, err := time.ParseDuration(request.IdleTimeout)
		if err != nil || duration <= 0 {
			http.Error(w, "idleTimeout must be a positive Go duration", http.StatusBadRequest)
			return
		}
		session.Spec.IdleTimeout = &metav1.Duration{Duration: duration}
	}
	if err := s.client.Create(r.Context(), session); err != nil {
		if k8serrors.IsAlreadyExists(err) {
			http.Error(w, "HarnessSession already exists", http.StatusConflict)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, session)
}

func (s *Server) deleteHarnessSession(w http.ResponseWriter, r *http.Request) {
	session := &sympoziumv1alpha1.HarnessSession{ObjectMeta: metav1.ObjectMeta{Name: r.PathValue("name"), Namespace: requestNamespace(r)}}
	if err := s.client.Delete(r.Context(), session); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "HarnessSession not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) chatHarnessSession(w http.ResponseWriter, r *http.Request) {
	ns, name := requestNamespace(r), r.PathValue("name")
	var session sympoziumv1alpha1.HarnessSession
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: name, Namespace: ns}, &session); err != nil {
		if k8serrors.IsNotFound(err) {
			http.Error(w, "HarnessSession not found", http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	if session.Status.Phase != "Ready" || session.Status.ServiceName != name {
		http.Error(w, "HarnessSession is not ready", http.StatusConflict)
		return
	}
	var runtime sympoziumv1alpha1.AgentRuntime
	if err := s.client.Get(r.Context(), types.NamespacedName{Name: session.Spec.RuntimeRef, Namespace: ns}, &runtime); err != nil {
		http.Error(w, "session runtime is unavailable", http.StatusConflict)
		return
	}
	if runtime.Spec.ContractVersion != "v1alpha2" || runtime.Spec.Session == nil || runtime.Spec.Session.Protocol != "openai-chat" {
		http.Error(w, "session runtime does not support proxied chat", http.StatusConflict)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1_048_576))
	if err != nil {
		http.Error(w, "invalid chat request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if !json.Valid(body) {
		http.Error(w, "chat request must be valid JSON", http.StatusBadRequest)
		return
	}
	// The URL is constructed from CR fields that were checked above rather than
	// accepting a host/path from the caller. It therefore cannot be used as an
	// internal-network request primitive.
	target := fmt.Sprintf("http://%s.%s.svc:%d/v1/chat/completions", name, ns, runtime.Spec.Session.Port)
	proxyRequest, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		http.Error(w, "could not construct session request", http.StatusInternalServerError)
		return
	}
	proxyRequest.Header.Set("Content-Type", "application/json")
	response, err := harnessSessionProxyClient.Do(proxyRequest)
	if err != nil {
		http.Error(w, "session adapter is unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 2_000_001))
	if err != nil {
		http.Error(w, "could not read session response", http.StatusBadGateway)
		return
	}
	if len(responseBody) > 2_000_000 {
		http.Error(w, "session response exceeded 2 MB", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}
