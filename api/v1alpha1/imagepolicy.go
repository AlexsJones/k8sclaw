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

// imageDigestAlgorithms maps the digest algorithms Sympozium accepts to the
// expected lowercase-hex length of that digest. Only well-known OCI algorithms
// are accepted; anything else is treated as unpinned and rejected rather than
// guessed at.
var imageDigestAlgorithms = map[string]int{
	"sha256": 64,
	"sha384": 96,
	"sha512": 128,
}

// ParseImageDigest returns the digest (algorithm:hex) of a digest-pinned OCI
// image reference, or ("", false) when the reference carries no valid digest.
//
// A reference may name a tag as well as a digest ("name:tag@sha256:…"); the
// digest is the part after the last "@", and is the authoritative identifier
// for a pull. The strict form check matters: a runtime image becomes the pod's
// primary process, so a typo'd or truncated digest must not be read as
// "pinned".
func ParseImageDigest(image string) (string, bool) {
	at := strings.LastIndex(image, "@")
	if at < 0 || at == len(image)-1 {
		return "", false
	}
	digest := image[at+1:]
	algo, hex, ok := strings.Cut(digest, ":")
	if !ok {
		return "", false
	}
	want, known := imageDigestAlgorithms[algo]
	if !known || len(hex) != want {
		return "", false
	}
	for _, c := range hex {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", false
		}
	}
	return digest, true
}
