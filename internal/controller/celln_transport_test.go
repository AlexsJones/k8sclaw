package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testCellnToken = "isolated-test-credential-at-least-24-bytes"

func configureCellnToken(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(testCellnToken), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CELLN_TOKEN_FILE", path)
	return path
}

func TestCellnTransportAuthenticationAndRotation(t *testing.T) {
	path := configureCellnToken(t)
	t.Setenv("CELLN_ROUTER_URL", "http://127.0.0.1:8787")
	for _, token := range []string{testCellnToken, testCellnToken + "-rotated"} {
		if err := os.WriteFile(path, []byte(token+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			req, err := cellnRequest(context.Background(), method, "/v1/executions", nil)
			if err != nil || req.Header.Get("Authorization") != "Bearer "+token {
				t.Fatalf("credential missing or stale: %v", err)
			}
		}
	}
}

func TestCellnTransportRefusesUnsafeConfiguration(t *testing.T) {
	path := configureCellnToken(t)
	t.Setenv("CELLN_ALLOW_INSECURE_HTTP", "")
	for _, u := range []string{"http://example.com", "https://user:password@example.com", "file:///tmp/socket", "https://example.com?token=secret"} {
		t.Setenv("CELLN_ROUTER_URL", u)
		if _, err := cellnRequest(context.Background(), "GET", "/v1/node", nil); err == nil {
			t.Fatalf("accepted unsafe URL %s", u)
		}
	}
	t.Setenv("CELLN_ROUTER_URL", "https://example.com")
	for _, token := range []string{"", "short", testCellnToken + "\r\nInjected: yes", strings.Repeat("x", 4097)} {
		if err := os.WriteFile(path, []byte(token), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := cellnRequest(context.Background(), "GET", "/v1/node", nil); err == nil {
			t.Fatal("accepted invalid credential")
		}
	}
	t.Setenv("CELLN_TOKEN_FILE", "")
	if _, err := cellnRequest(context.Background(), "GET", "/v1/node", nil); err == nil {
		t.Fatal("accepted missing credential configuration")
	}
}

func TestCellnTransportDoesNotFollowRedirects(t *testing.T) {
	configureCellnToken(t)
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testCellnToken {
			t.Error("missing authentication")
		}
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	t.Setenv("CELLN_ROUTER_URL", source.URL)
	req, err := cellnRequest(context.Background(), "POST", "/v1/executions", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cellnHTTPClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if called || resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatal("followed redirect")
	}
}
