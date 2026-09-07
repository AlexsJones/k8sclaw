package cellnauthority

import (
	"fmt"
	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"regexp"
)

var jsonToolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// CompositionPlan is the exact celln closure compose input wire contract.
// Its source identities are descriptor-byte BLAKE3 hashes, not metadata hashes.
type CompositionPlan struct {
	APIVersion string   `json:"apiVersion"`
	Sources    []string `json:"sources"`
	ImageBytes int64    `json:"imageBytes"`
}

// PreparedSelection is an operator packaging input, not a runnable request.
// The actual composed image/mote, host model grant, conformance and readiness
// are deliberately absent: no selection metadata can manufacture those proofs.
type PreparedSelection struct {
	Composition       CompositionPlan             `json:"composition"`
	RuntimeExecutable api.CellnImmutableRef       `json:"runtimeExecutable"`
	RuntimeEntryPoint string                      `json:"runtimeEntryPoint"`
	BorrowedTools     []api.CellnBorrowedTool     `json:"borrowedTools"`
	Limits            api.AgentRuntimeCellnLimits `json:"limits"`
	JSON              api.CellnHarnessJSONLimits  `json:"json"`
}

func Prepare(snapshot SelectionSnapshot, imageBytes int64) (*PreparedSelection, error) {
	subject := snapshot.Runtime
	id, err := IdentifySubject("AgentRuntime", metav1.ObjectMeta{Namespace: subject.Namespace, Name: subject.Name, UID: subject.UID, Generation: subject.Generation}, snapshot.RuntimeSpec)
	if err != nil || id != subject || subject.Namespace != snapshot.Agent.Namespace {
		return nil, fmt.Errorf("runtime metadata changed or crossed namespace")
	}
	p := snapshot.RuntimeSpec.Celln
	if p == nil {
		return nil, fmt.Errorf("Celln runtime profile required")
	}
	if p.ContractVersion != "celln.json-tools/v1" || p.Platform != "linux/amd64" || p.Lane != "agent" || p.Lifecycle != "disposable-one-shot" || len(p.RuntimeData) != 0 || p.JSON == nil || p.JSON.MaxTurns < 1 || p.JSON.MaxTurns > 6 || p.JSON.MaxCalls < 0 || p.JSON.MaxCalls > 16 || !hashPattern.MatchString(p.Executable.Hash) || !hashPattern.MatchString(p.Closure.Hash) || !hashPattern.MatchString(p.Mote.Hash) || !publisherPattern.MatchString(p.PublisherKey) || !pathPattern.MatchString(p.EntryPoint) || len(p.EntryPoint) > 256 || p.EntryPoint == "/pilot-fetch" || len(p.Revision) < 1 || len(p.Revision) > 64 {
		return nil, fmt.Errorf("invalid or unsupported JSON runtime profile")
	}
	l := p.Limits
	if l.TimeoutMillis < 1 || l.TimeoutMillis > 300000 || l.MemoryBytes < 1 || l.MemoryBytes > 268435456 || l.TaskBytes < 1 || l.TaskBytes > 2048 || l.OutputBytes < 1 || l.OutputBytes > 65536 || l.Workspace != "none" || len(snapshot.Tools) > 16 {
		return nil, fmt.Errorf("invalid runtime ceilings")
	}
	if imageBytes < 33554432 || imageBytes > 536870912 || imageBytes%2097152 != 0 {
		return nil, fmt.Errorf("image must be 32..512 MiB, 2 MiB aligned")
	}
	result := &PreparedSelection{Composition: CompositionPlan{APIVersion: "celln.dev/composition-plan-v1", Sources: []string{p.Closure.Hash}, ImageBytes: imageBytes}, RuntimeExecutable: p.Executable, RuntimeEntryPoint: p.EntryPoint, Limits: l, JSON: *p.JSON, BorrowedTools: []api.CellnBorrowedTool{}}
	names := map[string]bool{}
	paths := map[string]bool{p.EntryPoint: true, "/pilot-fetch": true}
	sources := map[string]bool{p.Closure.Hash: true}
	for _, tool := range snapshot.Tools {
		s := tool.Spec
		if tool.Identity.Namespace != snapshot.Agent.Namespace || !jsonToolName.MatchString(tool.Identity.Name) || names[tool.Identity.Name] || paths[s.EntryPoint] || sources[s.Closure.Hash] || s.InvocationABI != "celln.json-stdio/v1" || s.Lane != "tool" {
			return nil, fmt.Errorf("unsupported or colliding selected tool")
		}
		// Recheck metadata identity so a mutated copy cannot substitute artifact
		// bytes between authorization and preparation.
		object := api.CellnTool{Spec: s}
		object.Namespace = tool.Identity.Namespace
		object.Name = tool.Identity.Name
		object.UID = tool.Identity.UID
		object.Generation = tool.Identity.Generation
		id, err := Identify(object)
		if err != nil || id != tool.Identity {
			return nil, fmt.Errorf("selected tool metadata changed")
		}
		if err := validateLimits(tool.Limits); err != nil {
			return nil, err
		}
		if tool.Limits.TimeoutMillis > s.Limits.TimeoutMillis || tool.Limits.MemoryBytes > s.Limits.MemoryBytes || tool.Limits.ArgumentBytes > s.Limits.ArgumentBytes || tool.Limits.OutputBytes > s.Limits.OutputBytes || tool.Limits.Effects != s.Limits.Effects {
			return nil, fmt.Errorf("selected limits exceed tool declaration")
		}
		names[tool.Identity.Name] = true
		paths[s.EntryPoint] = true
		sources[s.Closure.Hash] = true
		result.Composition.Sources = append(result.Composition.Sources, s.Closure.Hash)
		result.BorrowedTools = append(result.BorrowedTools, api.CellnBorrowedTool{Name: tool.Identity.Name, Path: s.EntryPoint, Hash: s.Executable.Hash, Description: s.Description, JSONStdio: &api.CellnJSONToolIO{ABI: s.InvocationABI, InputSchema: s.ArgumentsSchema.Hash, OutputSchema: s.ResultSchema.Hash, InputBytes: tool.Limits.ArgumentBytes, OutputBytes: tool.Limits.OutputBytes, TimeoutMs: tool.Limits.TimeoutMillis}})
		// Tools share the cell memory budget. Until a separate per-process memory
		// contract exists, cap the entire cell at every selected tool's ceiling.
		result.Limits.MemoryBytes = min(result.Limits.MemoryBytes, tool.Limits.MemoryBytes)
	}
	return result, nil
}
