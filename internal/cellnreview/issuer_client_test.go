package cellnreview

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func testIssuerClient(t *testing.T, endpoint, tokenPath, caPath string) *IssuerClient {
	t.Helper()
	c, err := NewIssuerClient(IssuerClientOptions{URL: endpoint, TokenFile: tokenPath, CAFile: caPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func TestIssuerClientUsesVerifiedTLSAndRotatingCredential(t *testing.T) {
	f, m, _ := managedFixture(t)
	endpoint, _, tokenPath := serveTestIssuer(t, m)
	c := testIssuerClient(t, endpoint, tokenPath, filepath.Join(filepath.Dir(tokenPath), "cert.pem"))
	ctx := context.Background()
	first, err := c.Issue(ctx, f.l, *f.f, *f.a, f.artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte("public-test-only-controller-token-rotated"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := c.Issue(ctx, f.l, *f.f, *f.a, f.artifacts)
	if err != nil || second.Profile != first.Profile || second.Grant != first.Grant {
		t.Fatalf("rotation/retry changed identity: %v", err)
	}
	untrusted := testIssuerClient(t, endpoint, tokenPath, "")
	if _, err := untrusted.Issue(ctx, f.l, *f.f, *f.a, f.artifacts); !errors.Is(err, ErrIssuerOutcomeUnknown) {
		t.Fatal("untrusted TLS identity accepted")
	}
}

func TestIssuerClientRefusesResponseSubstitutionAndNeverFollowsRedirects(t *testing.T) {
	for _, mode := range []string{"valid", "task", "caller", "tool", "extra", "grant", "candidate", "profile", "executed", "missing-executed", "ready", "trailing", "oversized", "redirect", "lost-response", "withdrawal"} {
		t.Run(mode, func(t *testing.T) {
			f := provisionFixture(t)
			ctx := context.Background()
			issued, err := issue(ctx, f.l, *f.f, *f.a, f.artifacts, f.o, f.run)
			if err != nil {
				t.Fatal(err)
			}
			value := IssuerResponse{APIVersion: "sympozium.ai/celln-issuer-response-v1", Issued: issued, ArtifactReadiness: "not_checked"}
			var request map[string]any
			if err := json.Unmarshal(issued.Request, &request); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "task":
				request["harness"].(map[string]any)["task"] = "changed"
			case "caller":
				request["workload"].(map[string]any)["caller"] = "other"
			case "tool":
				tool := request["tools"].([]any)[0].(map[string]any)
				original := tool["hash"].(string)
				replacement := "blake3:" + strings.Repeat("f", 64)
				if original == replacement {
					t.Fatal("substitution fixture must change the tool hash")
				}
				tool["hash"] = replacement
			case "extra":
				request["unexpectedAuthority"] = true
			case "grant":
				issued.Grant = "blake3:" + strings.Repeat("a", 64)
			case "candidate":
				issued.Candidate.Approval.Caller = "other"
			case "profile":
				issued.Profile = "../other"
			case "executed":
				value.Executed = true
			case "ready":
				value.ArtifactReadiness = "ready"
			}
			issued.Request, _ = json.Marshal(request)
			body, _ := json.Marshal(value)
			if mode == "missing-executed" {
				var fields map[string]any
				_ = json.Unmarshal(body, &fields)
				delete(fields, "executed")
				body, _ = json.Marshal(fields)
			}
			if mode == "trailing" {
				body = append(body, []byte(" {}")...)
			}
			if mode == "oversized" {
				body = append(body, []byte(strings.Repeat(" ", 1<<20))...)
			}
			var hits atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+testIssuerToken {
					w.WriteHeader(401)
					return
				}
				if mode == "redirect" {
					http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
					return
				}
				if mode == "lost-response" {
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				if mode == "withdrawal" {
					var cm corev1.ConfigMap
					if err := f.c.Get(ctx, f.l.Source, &cm); err != nil {
						t.Error(err)
					}
					if err := f.c.Delete(ctx, &cm); err != nil {
						t.Error(err)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(body)
			}))
			defer server.Close()
			dir := t.TempDir()
			tokenPath, caPath := filepath.Join(dir, "token"), filepath.Join(dir, "ca.pem")
			if err := os.WriteFile(tokenPath, []byte(testIssuerToken), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600); err != nil {
				t.Fatal(err)
			}
			client := testIssuerClient(t, server.URL, tokenPath, caPath)
			_, err = client.Issue(ctx, f.l, *f.f, *f.a, f.artifacts)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("wrong response acceptance: %v", err)
			}
			if mode == "lost-response" && !errors.Is(err, ErrIssuerOutcomeUnknown) {
				t.Fatal("lost response was not classified as ambiguous")
			}
			if hits.Load() != 1 {
				t.Fatal("client automatically retried or followed redirect")
			}
		})
	}
}

func TestIssuerClientRejectsUnsafeOperatorConfiguration(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:8788", "https://user:password@host", "https://host/path", "https://host?", "https://host?token=secret", "https://host#fragment"} {
		if _, err := NewIssuerClient(IssuerClientOptions{URL: endpoint, TokenFile: "/operator/token"}); err == nil {
			t.Fatalf("accepted unsafe endpoint %q", endpoint)
		}
	}
}
