package v1alpha1

import "strings"

// Allows reports whether an image reference satisfies this image policy.
//
// The match is a plain **string prefix** against each entry in
// AllowedRegistries. There is no parsing of the reference into registry,
// repository and tag, which makes the granularity the operator's to choose:
//
//	"docker.io/"                    every image on Docker Hub
//	"ghcr.io/acme/"                 one organisation
//	"ghcr.io/acme/harness:v1"       one image and tag
//	"ghcr.io/acme/harness@sha256:…" one exact digest
//
// Two consequences of prefix matching are worth knowing, because the field
// looks stricter than it is:
//
//   - There is no component boundary. "ghcr.io/acme" (no trailing slash) also
//     matches "ghcr.io/acmecorp-evil/…", and ":v1" also matches ":v1-evil".
//     End an entry at a "/" — or at a full tag or digest — to mean what it
//     looks like it means.
//   - A short name carries no registry, so "docker.io/library/" does not match
//     "alpine:3.20". That direction fails closed.
//
// An empty policy allows everything: callers treat "no list" as "no
// restriction", which is what the field documentation promises.
//
// Shared rather than duplicated: the admission webhook and the controller both
// enforce this, and an image the two disagreed about would be admitted and then
// fail — or worse, the reverse.
func (p *ImagePolicySpec) Allows(image string) bool {
	if p == nil || len(p.AllowedRegistries) == 0 {
		return true
	}
	for _, prefix := range p.AllowedRegistries {
		if strings.HasPrefix(image, prefix) {
			return true
		}
	}
	return false
}
