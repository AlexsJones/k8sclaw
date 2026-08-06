package apiserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
)

// okHandler is a stub downstream handler that always returns 200 and a
// marker body so the tests can confirm the chain was actually invoked.
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Marker", "ok")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
})

func authRequest(t *testing.T, h http.Handler, method, path, header string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthMiddleware_AcceptsValidToken(t *testing.T) {
	reader := newTestTokenReader("secret")
	mw := authMiddleware(reader, okHandler)

	rec := authRequest(t, mw, "GET", "/api/v1/runs", "Bearer secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Marker") != "ok" {
		t.Errorf("expected downstream handler to fire, got marker %q", rec.Header().Get("X-Marker"))
	}
}

func TestAuthMiddleware_RejectsInvalidToken(t *testing.T) {
	reader := newTestTokenReader("secret")
	mw := authMiddleware(reader, okHandler)

	rec := authRequest(t, mw, "GET", "/api/v1/runs", "Bearer wrong")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_RejectsRotatedToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	reader := &tokenReader{path: path}
	mw := authMiddleware(reader, okHandler)

	// Initial: "old" works.
	rec := authRequest(t, mw, "GET", "/api/v1/runs", "Bearer old")
	if rec.Code != http.StatusOK {
		t.Fatalf("first call: status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}

	// Rotate the file.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("rotate token: %v", err)
	}

	// Old token must be rejected.
	rec = authRequest(t, mw, "GET", "/api/v1/runs", "Bearer old")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("old token after rotation: status = %d, want 401", rec.Code)
	}

	// New token must be accepted.
	rec = authRequest(t, mw, "GET", "/api/v1/runs", "Bearer new")
	if rec.Code != http.StatusOK {
		t.Fatalf("new token after rotation: status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAuthMiddleware_KeepsServingDuringPermissionDenied(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("token-a"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	reader := &tokenReader{path: path}
	mw := authMiddleware(reader, okHandler)

	// Seed the cache.
	if got := reader.Current(); got != "token-a" {
		t.Fatalf("seed Current() = %q, want token-a", got)
	}

	// Make the file unreadable (chmod 000) so subsequent reads fail. The
	// reader must fall back to the cached value and keep serving.
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	// Ensure the mtime changed so the reader attempts to re-read.
	time.Sleep(10 * time.Millisecond)
	if err := os.Chtimes(path, time.Now(), time.Now()); err != nil {
		// Some environments disallow Chtimes on owner-only files; chmod
		// already advances the mtime in most filesystems, so this is best
		// effort — if Chtimes fails we proceed regardless.
		t.Logf("chtimes: %v", err)
	}
	// Restore the file mode immediately so the reader can re-read for the
	// second phase of the test.
	defer func() { _ = os.Chmod(path, 0o600) }()

	rec := authRequest(t, mw, "GET", "/api/v1/runs", "Bearer token-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (cached fallback), body = %s", rec.Code, rec.Body.String())
	}

	// Restore permissions and rotate to a new value.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod 600: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(path, []byte("token-b"), 0o600); err != nil {
		t.Fatalf("write token-b: %v", err)
	}

	rec = authRequest(t, mw, "GET", "/api/v1/runs", "Bearer token-b")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (post-recovery rotation), body = %s", rec.Code, rec.Body.String())
	}
}

func TestAuthMiddleware_WebSocketFallsBackToQueryToken(t *testing.T) {
	reader := newTestTokenReader("secret")
	mw := authMiddleware(reader, okHandler)

	// No Authorization header, but ?token=secret on a /ws/ path. Should
	// be accepted by the auth middleware and forwarded to the downstream
	// handler.
	req := httptest.NewRequest("GET", "/ws/stream?token=secret", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Marker") != "ok" {
		t.Errorf("expected downstream handler to fire, got marker %q", rec.Header().Get("X-Marker"))
	}
}

func TestAuthMiddleware_HealthAndMetricsBypassAuth(t *testing.T) {
	reader := newTestTokenReader("secret")
	mw := authMiddleware(reader, okHandler)

	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		rec := authRequest(t, mw, "GET", path, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
		}
		if rec.Header().Get("X-Marker") != "ok" {
			t.Errorf("%s: expected downstream handler to fire", path)
		}
	}
}

func TestAuthMiddleware_LengthMismatchShortCircuits(t *testing.T) {
	reader := newTestTokenReader("short")
	mw := authMiddleware(reader, okHandler)

	// Off-by-one length mismatch must still 401.
	rec := authRequest(t, mw, "GET", "/api/v1/runs", "Bearer longer")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
	}
}

func TestAuthMiddleware_NilReaderDisablesAuth(t *testing.T) {
	// buildMux must skip authMiddleware when the reader is nil or when
	// its current value is empty. We exercise both branches.
	srv := NewServer(nil, nil, nil, logr.Discard())

	// nil reader
	h := srv.buildMux(nil, nil)
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("nil reader: /healthz status = %d, want 200", rec.Code)
	}

	// Empty reader (no seed, no file)
	empty := newTestTokenReader("")
	h2 := srv.buildMux(nil, empty)
	req2 := httptest.NewRequest("GET", "/healthz", nil)
	rec2 := httptest.NewRecorder()
	h2.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("empty reader: /healthz status = %d, want 200", rec2.Code)
	}
}

func TestAuthMiddleware_FailsClosedOnEmptyExpectedToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("good-token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	reader := &tokenReader{path: path}
	mw := authMiddleware(reader, okHandler)

	// Sanity: valid token works.
	rec := authRequest(t, mw, "GET", "/api/v1/runs", "Bearer good-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: status = %d, want 200", rec.Code)
	}

	// Rotate the file to an empty value.
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("write empty token: %v", err)
	}

	// No Authorization header — must 401, not pass through.
	rec = authRequest(t, mw, "GET", "/api/v1/runs", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("empty expected + no header: status = %d, want 401", rec.Code)
	}

	// Empty Bearer header — must 401.
	rec = authRequest(t, mw, "GET", "/api/v1/runs", "Bearer ")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("empty expected + empty bearer: status = %d, want 401", rec.Code)
	}

	// Non-empty Bearer — must still 401.
	rec = authRequest(t, mw, "GET", "/api/v1/runs", "Bearer any-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("empty expected + any bearer: status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_FailsClosedOnEmptyExpectedToken_WebSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("good-token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}
	reader := &tokenReader{path: path}
	mw := authMiddleware(reader, okHandler)

	// Rotate the file to empty.
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatalf("write empty token: %v", err)
	}

	// WebSocket path with ?token= query param — must still 401.
	req := httptest.NewRequest("GET", "/ws/stream?token=anything", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("empty expected + ws query token: status = %d, want 401", rec.Code)
	}
}
