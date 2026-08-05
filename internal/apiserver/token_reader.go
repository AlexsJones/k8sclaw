// Package apiserver provides the HTTP + WebSocket API server for Sympozium.
package apiserver

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
)

// tokenReader reads a bearer token from a file that is mounted from a
// Kubernetes Secret. The file is re-read on every Current() call so that
// Secret rotations take effect without a pod restart — kubelet rewrites the
// projected file in place when the underlying Secret is updated, and a
// fresh read picks up the new value on the next request.
//
// The cost of a re-read is one open(2) + read(2) per request (a few µs),
// which is negligible against the auth middleware's constant-time compare.
// We intentionally do not cache by file mtime: kubelet does not always
// bump the projected file's mtime on rapid Secret patches
// (https://github.com/kubernetes/kubernetes/issues/59845), so an mtime
// cache could serve a stale token for the duration of the mtime-drift
// window. The simple "always re-read" policy is correct under all
// observed kubelet behaviors.
type tokenReader struct {
	path string

	mu       sync.Mutex
	now      string
	hasValue bool

	// warnMu guards lastWarn. It is separate from mu so warn() can be
	// called from Current() while the read lock is held without deadlocking.
	warnMu   sync.Mutex
	lastWarn time.Time
	logger   logr.Logger
}

// NewTokenReader constructs a reader for the file at path. The initial cache
// is populated from a best-effort read of the file; if the file is missing
// or unreadable the reader is still usable and Current() will return ""
// until the file appears or becomes readable.
func NewTokenReader(path string, logger logr.Logger) *tokenReader {
	r := &tokenReader{path: path, logger: logger}
	r.refresh()
	return r
}

// refresh re-reads the file and updates the cache. Caller must hold r.mu.
func (r *tokenReader) refresh() {
	if _, err := os.Stat(r.path); err != nil {
		r.warn(err)
		return
	}
	raw, err := os.ReadFile(r.path)
	if err != nil {
		r.warn(err)
		return
	}
	r.now = strings.TrimRight(string(raw), "\n")
	r.hasValue = true
}

// Current returns the current bearer token. Every call re-reads the file
// so a Secret rotation is visible to the very next request. The read is
// a single stat + read syscall pair (a few µs) and is dwarfed by the auth
// middleware's constant-time compare.
func (r *tokenReader) Current() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.path == "" {
		// Test path: no file backing. Return the seeded value if any.
		if r.hasValue {
			return r.now
		}
		return ""
	}
	r.refresh()
	if r.hasValue {
		return r.now
	}
	return ""
}

// Seed installs the literal --token fallback when no Secret is mounted
// and the token file is absent. Does not clobber a value already read
// from the file, so a real Secret-backed token always wins over the
// literal fallback. Once the file appears, the next Current() call
// overrides the seed.
func (r *tokenReader) Seed(value string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hasValue {
		return // don't clobber a real file read
	}
	r.now = value
	r.hasValue = value != ""
}

// warn logs an error, throttled to once per minute. Pass logr.Discard() to
// silence the warning; the apiserver's zap logger is passed in main.
func (r *tokenReader) warn(err error) {
	r.warnMu.Lock()
	now := time.Now()
	if !r.lastWarn.IsZero() && now.Sub(r.lastWarn) < time.Minute {
		r.warnMu.Unlock()
		return
	}
	r.lastWarn = now
	r.warnMu.Unlock()
	r.logger.Error(err, "token file unreadable; serving with last known value", "path", r.path)
}
