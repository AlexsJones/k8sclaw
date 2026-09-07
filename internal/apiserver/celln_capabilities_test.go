package apiserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const discoveryTestToken = "public-readonly-test-token-at-least-24"
const eligibleCapabilityFixture = `{"apiVersion":"celln.dev/capabilities-v1alpha1","preflightOnly":true,"artifactReadiness":"not_checked","eligibleNodes":1,"nodes":[{"preflightEligible":true,"report":{"apiVersion":"celln.dev/capabilities-v1alpha1","preflightOnly":true,"artifactReadiness":"not_checked","requestVersions":["celln.dev/v1alpha1"],"node":{"kvm":true,"cpu_virtualization":true,"guest_kernel":true,"mote_store":true,"tool_store":true,"live_cells":0,"max_cells":1,"memory_bytes":268435456}}}]}`

func configureCapabilityTest(t *testing.T, origin string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(discoveryTestToken), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CELLN_ENABLED", "true")
	t.Setenv("CELLN_ROUTER_URL", origin)
	t.Setenv("CELLN_ALLOW_INSECURE_HTTP", "true")
	t.Setenv("CELLN_CAPABILITY_TOKEN_FILE", path)
	return path
}

func TestCellnCapabilityContract(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		available  bool
	}{
		{"eligible", eligibleCapabilityFixture, 200, true},
		{"old-public-health", `{"ok":true,"kvm":true}`, 200, false},
		{"no-nodes", `{"apiVersion":"celln.dev/capabilities-v1alpha1","preflightOnly":true,"artifactReadiness":"not_checked","eligibleNodes":0,"nodes":[]}`, 200, false},
		{"incompatible", strings.ReplaceAll(eligibleCapabilityFixture, "capabilities-v1alpha1", "capabilities-v99"), 200, false},
		{"missing-kvm", strings.ReplaceAll(eligibleCapabilityFixture, `"kvm":true`, `"kvm":false`), 200, false},
		{"full", strings.ReplaceAll(eligibleCapabilityFixture, `"live_cells":0`, `"live_cells":1`), 200, false},
		{"missing-live-count", strings.ReplaceAll(eligibleCapabilityFixture, `"live_cells":0,`, ``), 200, false},
		{"no-memory", strings.ReplaceAll(eligibleCapabilityFixture, `268435456`, `0`), 200, false},
		{"inconsistent-count", strings.ReplaceAll(eligibleCapabilityFixture, `"eligibleNodes":1`, `"eligibleNodes":2`), 200, false},
		{"missing-stores", strings.ReplaceAll(eligibleCapabilityFixture, `"tool_store":true`, `"tool_store":false`), 200, false},
		{"malformed", `{`, 200, false},
		{"oversized", strings.Repeat("x", 2*1024*1024+1), 200, false},
		{"unauthorized", discoveryTestToken, 401, false},
		{"busy", discoveryTestToken, 503, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.URL.Path != "/v1/capabilities" || r.Header.Get("Authorization") != "Bearer "+discoveryTestToken {
					t.Error("wrong authenticated capability request")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			configureCapabilityTest(t, server.URL)
			result := cellnCapabilityStatus()
			if result.Available != tc.available {
				t.Fatalf("status=%+v", result)
			}
			if result.Reason == "" || strings.Contains(result.Reason, discoveryTestToken) {
				t.Fatalf("missing or sensitive reason: %+v", result)
			}
			if result.Available && !strings.Contains(result.Reason, "preflight") {
				t.Fatal("readiness overstated")
			}
		})
	}
}

func TestCellnCapabilityCredentialRotationAndTransport(t *testing.T) {
	var calls atomic.Int32
	var authorization atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		authorization.Store(r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(eligibleCapabilityFixture))
	}))
	defer server.Close()
	path := configureCapabilityTest(t, server.URL)
	if !cellnCapabilityStatus().Available {
		t.Fatal("baseline")
	}
	rotated := "rotated-public-readonly-token-at-least-24"
	if err := os.WriteFile(path, []byte(rotated), 0600); err != nil {
		t.Fatal(err)
	}
	if !cellnCapabilityStatus().Available || authorization.Load() != "Bearer "+rotated {
		t.Fatal("credential not reloaded")
	}
	before := calls.Load()
	for _, bad := range []string{"short", strings.Repeat("x", 4097), discoveryTestToken + " injected"} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if cellnCapabilityStatus().Available {
			t.Fatal("bad token accepted")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if cellnCapabilityStatus().Available {
		t.Fatal("missing token accepted")
	}
	if err := os.WriteFile(path, []byte(discoveryTestToken), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CELLN_ALLOW_INSECURE_HTTP", "false")
	if cellnCapabilityStatus().Available {
		t.Fatal("plaintext accepted without acknowledgement")
	}
	t.Setenv("CELLN_ENABLED", "false")
	if cellnCapabilityStatus().Available {
		t.Fatal("disabled backend advertised")
	}
	if calls.Load() != before {
		t.Fatal("invalid configuration contacted router")
	}
}

func TestCellnCapabilityRedirectNeverForwardsCredential(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	configureCapabilityTest(t, redirect.URL)
	if cellnCapabilityStatus().Available || leaked.Load() {
		t.Fatal("redirect followed")
	}
}
