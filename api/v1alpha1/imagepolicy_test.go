package v1alpha1

import (
	"strings"
	"testing"
)

func TestParseImageDigest(t *testing.T) {
	for _, tc := range []struct {
		name   string
		image  string
		digest string
		ok     bool
	}{
		{name: "pinned sha256", image: "ghcr.io/acme/harness@sha256:" + strings.Repeat("a", 64), digest: "sha256:" + strings.Repeat("a", 64), ok: true},
		{name: "tag plus digest", image: "ghcr.io/acme/harness:v1@sha256:" + strings.Repeat("b", 64), digest: "sha256:" + strings.Repeat("b", 64), ok: true},
		{name: "sha512", image: "registry.local/adapter@sha512:" + strings.Repeat("c", 128), digest: "sha512:" + strings.Repeat("c", 128), ok: true},
		{name: "tag-only", image: "ghcr.io/acme/harness:v1", ok: false},
		{name: "bare-name", image: "ghcr.io/acme/harness", ok: false},
		{name: "truncated", image: "ghcr.io/acme/harness@sha256:deadbeef", ok: false},
		{name: "uppercase-hex", image: "ghcr.io/acme/harness@sha256:" + strings.Repeat("A", 64), ok: false},
		{name: "unknown-algo", image: "ghcr.io/acme/harness@md5:" + strings.Repeat("0", 32), ok: false},
		{name: "trailing-at", image: "ghcr.io/acme/harness@", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest, ok := ParseImageDigest(tc.image)
			if ok != tc.ok || digest != tc.digest {
				t.Fatalf("ParseImageDigest(%q) = (%q, %v), want (%q, %v)", tc.image, digest, ok, tc.digest, tc.ok)
			}
		})
	}
}
