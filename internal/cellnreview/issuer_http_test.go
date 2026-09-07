package cellnreview

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testIssuerToken = "public-test-only-controller-token-0001"

func serveTestIssuer(t *testing.T, m *ManagedIssuer) (string, *http.Client, string) {
	t.Helper()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "controller-token")
	if err := os.WriteFile(tokenPath, []byte(testIssuerToken), 0600); err != nil {
		t.Fatal(err)
	}
	// Reuse Go's public test certificate, not a deployment credential.
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	cert := fixture.TLS.Certificates[0]
	fixture.Close()
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	parsed, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots.AddCert(parsed)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeIssuer(ctx, listener, m, tokenPath, certPath, keyPath) }()
	t.Cleanup(func() {
		cancel()
		transport.CloseIdleConnections()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("issuer shutdown failed: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("issuer server did not stop")
		}
	})
	eventuallyManaged(t, func() bool { ready, _ := m.Status(); return ready })
	return "https://" + listener.Addr().String(), client, tokenPath
}

func issuerHTTP(t *testing.T, client *http.Client, method, url, token string, data []byte) (int, []byte) {
	t.Helper()
	r, err := http.NewRequest(method, url, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Content-Type", "application/json")
	response, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func TestIssuerTLSServiceAuthenticationIssuanceAndRotation(t *testing.T) {
	f, m, _ := managedFixture(t)
	url, client, tokenPath := serveTestIssuer(t, m)
	for _, token := range []string{"", "wrong"} {
		if code, _ := issuerHTTP(t, client, "GET", url+"/v1/issuer/status", token, nil); code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated status: %d", code)
		}
	}
	request := IssuerRequest{APIVersion: "sympozium.ai/celln-issuer-request-v1", Frozen: *f.f, Approval: *f.a, Artifacts: f.artifacts}
	data, _ := json.Marshal(request)
	var identity string
	for i := 0; i < 2; i++ {
		code, body := issuerHTTP(t, client, "POST", url+"/v1/issuances", testIssuerToken, data)
		if code != http.StatusOK {
			t.Fatalf("issuance refused: %d", code)
		}
		var report IssuerResponse
		if err := json.Unmarshal(body, &report); err != nil {
			t.Fatal(err)
		}
		if report.APIVersion != "sympozium.ai/celln-issuer-response-v1" || report.Issued == nil || report.Executed || report.ArtifactReadiness != "not_checked" {
			t.Fatal("invalid remote issuance report")
		}
		if i == 1 && report.Issued.Profile != identity {
			t.Fatal("remote retry renewed authority")
		}
		identity = report.Issued.Profile
		if bytes.Contains(body, []byte("never-read-host-credential")) || bytes.Contains(body, []byte(testIssuerToken)) {
			t.Fatal("host credentials leaked")
		}
	}
	newToken := "public-test-only-controller-token-0002"
	if err := os.WriteFile(tokenPath, []byte(newToken), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _ := issuerHTTP(t, client, "GET", url+"/v1/issuer/status", testIssuerToken, nil); code != http.StatusUnauthorized {
		t.Fatal("old credential survived rotation")
	}
	if code, _ := issuerHTTP(t, client, "GET", url+"/v1/issuer/status", newToken, nil); code != http.StatusOK {
		t.Fatal("new credential refused")
	}
	if err := os.Remove(tokenPath); err != nil {
		t.Fatal(err)
	}
	if code, _ := issuerHTTP(t, client, "GET", url+"/v1/issuer/status", newToken, nil); code != http.StatusUnauthorized {
		t.Fatal("missing credential failed open")
	}
}

func TestIssuerTLSServiceRefusesMalformedOrSubstitutedAuthority(t *testing.T) {
	f, m, _ := managedFixture(t)
	url, client, tokenPath := serveTestIssuer(t, m)
	for _, body := range []string{`{}`, `{"apiVersion":"sympozium.ai/celln-issuer-request-v1","policyRoot":"/tenant"}`, `{} {}`, `{"padding":"` + strings.Repeat("x", 1<<20) + `"}`} {
		if code, _ := issuerHTTP(t, client, "POST", url+"/v1/issuances", testIssuerToken, []byte(body)); code != http.StatusBadRequest {
			t.Fatalf("malformed request accepted: %d", code)
		}
	}
	r := IssuerRequest{APIVersion: "sympozium.ai/celln-issuer-request-v1", Frozen: *f.f, Approval: *f.a, Artifacts: f.artifacts}
	r.Frozen.Snapshot.Agent.Namespace = "other-tenant"
	data, _ := json.Marshal(r)
	if code, _ := issuerHTTP(t, client, "POST", url+"/v1/issuances", testIssuerToken, data); code != http.StatusConflict {
		t.Fatal("unconfigured Agent accepted")
	}
	if code, _ := issuerHTTP(t, client, "GET", url+"/v1/issuer/status?token=ignored", testIssuerToken, nil); code != http.StatusBadRequest {
		t.Fatal("query accepted")
	}
	handler, err := NewIssuerHandler(m, tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	plain := httptest.NewRequest("GET", "/v1/issuer/status", nil)
	plain.Header.Set("Authorization", "Bearer "+testIssuerToken)
	handler.ServeHTTP(recorder, plain)
	if recorder.Code != http.StatusUpgradeRequired {
		t.Fatal("plaintext accepted")
	}
	duplicate := httptest.NewRequest("GET", "/v1/issuer/status", nil)
	duplicate.TLS = &tls.ConnectionState{}
	duplicate.Header.Add("Authorization", "Bearer "+testIssuerToken)
	duplicate.Header.Add("Authorization", "Bearer "+testIssuerToken)
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, duplicate)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatal("duplicate credentials accepted")
	}
	assertNoProfiles(t, f.o.PolicyRoot)
}

func TestIssuerHandlerBoundsConcurrentAuthenticatedWork(t *testing.T) {
	_, m, _ := managedFixture(t)
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(testIssuerToken), 0600); err != nil {
		t.Fatal(err)
	}
	handler, err := NewIssuerHandler(m, path)
	if err != nil {
		t.Fatal(err)
	}
	request := func() int {
		r := httptest.NewRequest("GET", "/v1/issuer/status", nil)
		r.TLS = &tls.ConnectionState{}
		r.Header.Set("Authorization", "Bearer "+testIssuerToken)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	// Hold the lifecycle mutex so the two admitted status requests cannot exit.
	m.mu.Lock()
	done := make(chan int, 3)
	for i := 0; i < 3; i++ {
		go func() { done <- request() }()
	}
	var code int
	select {
	case code = <-done:
	case <-time.After(5 * time.Second):
		m.mu.Unlock()
		t.Fatal("concurrency limit did not refuse excess work")
	}
	m.mu.Unlock()
	if code != http.StatusTooManyRequests {
		t.Fatalf("unexpected excess request result: %d", code)
	}
	for i := 0; i < 2; i++ {
		if code := <-done; code != http.StatusServiceUnavailable {
			t.Fatalf("closed gate reported ready: %d", code)
		}
	}
}
