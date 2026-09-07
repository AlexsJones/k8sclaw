// Package cellnauthority resolves explicit tool selections without I/O. It does
// not verify signatures, authorize callers, certify node readiness or dispatch.
// Callers must obtain each grant layer from its independently trusted source.
package cellnauthority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

// ToolIdentity freezes object identity and the entire spec, including schemas,
// publisher, lane and provenance. SpecSHA256 hashes canonical Go JSON metadata,
// not Celln artifact bytes: it must never substitute for a BLAKE3 artifact hash.
type ToolIdentity struct {
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	UID        types.UID `json:"uid"`
	Generation int64     `json:"generation"`
	Revision   string    `json:"revision"`
	SpecSHA256 string    `json:"specSHA256"`
}

type Grant struct {
	Tool   ToolIdentity        `json:"tool"`
	Limits api.CellnToolLimits `json:"limits"`
}

// ToolRequest deliberately requires three named layers. An omitted layer
// grants nothing; callers cannot omit a layer from a variadic intersection.
// These are internal inputs, not tenant-facing approval assertions.
type ToolRequest struct {
	Namespace string
	Selection []Grant
	Catalogue []api.CellnTool
	Operator  []Grant
	Runtime   []Grant
	Agent     []Grant
}

// ResolvedTool owns its spec copy; later cache mutation cannot retarget it.
// This is the tool portion of a future execution plan, not a dispatch request.
type ResolvedTool struct {
	Identity ToolIdentity        `json:"identity"`
	Spec     api.CellnToolSpec   `json:"spec"`
	Limits   api.CellnToolLimits `json:"limits"`
}

var hashPattern = regexp.MustCompile(`^blake3:[0-9a-f]{64}$`)
var publisherPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var pathPattern = regexp.MustCompile(`^/([A-Za-z0-9_-][A-Za-z0-9_.+-]*/)*[A-Za-z0-9_-][A-Za-z0-9_.+-]*$`)
var imagePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*@sha256:[0-9a-f]{64}$`)

// Identify validates metadata even for non-API-server callers. Status is not
// an input: a Ready string cannot supply any missing grant layer.
func Identify(tool api.CellnTool) (ToolIdentity, error) {
	s := tool.Spec
	if tool.Namespace == "" || tool.Name == "" || tool.UID == "" || tool.Generation < 1 || tool.DeletionTimestamp != nil {
		return ToolIdentity{}, fmt.Errorf("tool must have live namespace/name/UID/generation")
	}
	if len(s.Revision) < 1 || len(s.Revision) > 64 || len(s.Description) < 1 || len(s.Description) > 1024 || len(s.SupportOwner) < 1 || len(s.SupportOwner) > 256 || !publisherPattern.MatchString(s.PublisherKey) || len(s.EntryPoint) > 256 || !pathPattern.MatchString(s.EntryPoint) || s.InvocationABI != "celln.argv/v1" || s.Platform != "linux/amd64" || (s.Lane != "tool" && s.Lane != "agent") || (s.SourceImage != "" && (len(s.SourceImage) > 512 || !imagePattern.MatchString(s.SourceImage))) {
		return ToolIdentity{}, fmt.Errorf("invalid or unsupported tool metadata")
	}
	for _, ref := range []api.CellnImmutableRef{s.Executable, s.Closure, s.ArgumentsSchema, s.ResultSchema} {
		if !hashPattern.MatchString(ref.Hash) {
			return ToolIdentity{}, fmt.Errorf("invalid immutable artifact hash")
		}
	}
	if err := validateLimits(s.Limits); err != nil {
		return ToolIdentity{}, err
	}
	b, err := json.Marshal(s)
	if err != nil {
		return ToolIdentity{}, err
	}
	digest := sha256.Sum256(b)
	return ToolIdentity{Namespace: tool.Namespace, Name: tool.Name, UID: tool.UID, Generation: tool.Generation, Revision: s.Revision, SpecSHA256: "sha256:" + hex.EncodeToString(digest[:])}, nil
}

func validateLimits(l api.CellnToolLimits) error {
	if l.TimeoutMillis < 1 || l.TimeoutMillis > 300000 || l.MemoryBytes < 1 || l.MemoryBytes > 268435456 || l.ArgumentBytes < 1 || l.ArgumentBytes > 65536 || l.OutputBytes < 1 || l.OutputBytes > 65536 || l.Workspace != "none" || len(l.Egress) != 0 || len(l.Inputs) != 0 || (l.Effects != "none" && l.Effects != "external-side-effects") {
		return fmt.Errorf("invalid or unsupported tool limits")
	}
	return nil
}

// ResolveTools preserves selection order and refuses the entire selection on
// any denied member. It never silently removes tools or expands empty selection.
// Re-running with current grants is mandatory before new work; frozen output
// is not a revocation exemption and must not be used to mint retry identities.
func ResolveTools(req ToolRequest) ([]ResolvedTool, error) {
	if req.Namespace == "" || len(req.Selection) > 16 {
		return nil, fmt.Errorf("namespace required and at most 16 tools may be selected")
	}
	result := make([]ResolvedTool, 0, len(req.Selection))
	seenNames, seenPaths := map[string]bool{}, map[string]bool{}
	for _, selection := range req.Selection {
		selected := selection.Tool
		if selected.Namespace != req.Namespace || seenNames[selected.Name] {
			return nil, fmt.Errorf("cross-namespace or duplicate tool selection")
		}
		seenNames[selected.Name] = true
		var found *api.CellnTool
		for i := range req.Catalogue {
			t := &req.Catalogue[i]
			if t.Namespace == req.Namespace && t.Name == selected.Name {
				if found != nil {
					return nil, fmt.Errorf("ambiguous catalogue identity")
				}
				found = t
			}
		}
		if found == nil {
			return nil, fmt.Errorf("selected tool is absent")
		}
		identity, err := Identify(*found)
		if err != nil {
			return nil, err
		}
		if identity != selected {
			return nil, fmt.Errorf("selected tool identity or revision changed")
		}
		if seenPaths[found.Spec.EntryPoint] {
			return nil, fmt.Errorf("selected entry point collision")
		}
		seenPaths[found.Spec.EntryPoint] = true
		limits := found.Spec.Limits
		for _, layer := range []struct {
			name   string
			grants []Grant
		}{{"operator", req.Operator}, {"runtime", req.Runtime}, {"agent", req.Agent}, {"selection", []Grant{selection}}} {
			var grant *Grant
			for i := range layer.grants {
				g := &layer.grants[i]
				if g.Tool.Namespace == selected.Namespace && g.Tool.Name == selected.Name {
					if grant != nil {
						return nil, fmt.Errorf("ambiguous %s grant", layer.name)
					}
					grant = g
				}
			}
			if grant == nil || grant.Tool != identity {
				return nil, fmt.Errorf("%s grant absent or stale", layer.name)
			}
			if err := validateLimits(grant.Limits); err != nil {
				return nil, fmt.Errorf("%s: %w", layer.name, err)
			}
			if found.Spec.Limits.Effects == "external-side-effects" && grant.Limits.Effects != "external-side-effects" {
				return nil, fmt.Errorf("%s forbids required external effects", layer.name)
			}
			limits.TimeoutMillis = min(limits.TimeoutMillis, grant.Limits.TimeoutMillis)
			limits.MemoryBytes = min(limits.MemoryBytes, grant.Limits.MemoryBytes)
			limits.ArgumentBytes = min(limits.ArgumentBytes, grant.Limits.ArgumentBytes)
			limits.OutputBytes = min(limits.OutputBytes, grant.Limits.OutputBytes)
		}
		result = append(result, ResolvedTool{Identity: identity, Spec: *found.Spec.DeepCopy(), Limits: limits})
	}
	return result, nil
}
