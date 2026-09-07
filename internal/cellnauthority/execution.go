package cellnauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

// ExecutionArtifacts are actual packaging outputs, not the runtime catalogue's
// template mote. Their bytes, source provenance and admission must still be
// verified by the host issuer; syntactically valid hashes are not readiness.
type ExecutionArtifacts struct {
	Mote    api.CellnImmutableRef `json:"mote"`
	Closure api.CellnImmutableRef `json:"closure"`
}

// ExecutionCandidate is deliberately not dispatchable: the model grant hash
// is a placeholder. Only the trusted host issuer may replace it. No caller-
// supplied task, tool list, persona, model or resource ceiling is copied in.
type ExecutionCandidate struct {
	APIVersion string          `json:"apiVersion"`
	Approval   ModelApproval   `json:"approval"`
	Request    json.RawMessage `json:"request"`
}

func (l ModelLoader) BuildExecution(ctx context.Context, frozen FrozenSelection, approval ModelApproval, artifacts ExecutionArtifacts) (*ExecutionCandidate, error) {
	if err := l.Revalidate(ctx, frozen, approval); err != nil {
		return nil, err
	}
	if !hashPattern.MatchString(artifacts.Mote.Hash) || !hashPattern.MatchString(artifacts.Closure.Hash) {
		return nil, fmt.Errorf("exact materialized mote and composed closure required")
	}
	run, id, err := l.Selection.readRun(ctx, types.NamespacedName{Namespace: frozen.Run.Namespace, Name: frozen.Run.Name})
	if err != nil {
		return nil, err
	}
	if id != frozen.Run {
		return nil, fmt.Errorf("run changed before execution construction")
	}
	s := run.Spec
	if s.Parent != nil || len(s.Env) != 0 || s.Sandbox != nil || s.AgentSandbox != nil || len(s.Skills) != 0 || s.ToolPolicy != nil || s.Celln != nil || s.CanaryMode || s.DryRun || len(s.ImagePullSecrets) != 0 || s.Lifecycle != nil || len(s.Volumes) != 0 || len(s.VolumeMounts) != 0 || (s.Mode != "" && s.Mode != "task") || (s.UseContext != nil && !*s.UseContext) || (s.Cleanup != "" && s.Cleanup != "delete") {
		return nil, fmt.Errorf("run requests unsupported pod, execution, lifecycle or context semantics")
	}
	task := s.Task.GetPrompt()
	limits := frozen.Prepared.Limits
	if len(task) > int(limits.TaskBytes) || strings.TrimSpace(task) == "" || strings.ContainsRune(task, '\x00') || len(s.SystemPrompt) > 2048 || strings.ContainsRune(s.SystemPrompt, '\x00') {
		return nil, fmt.Errorf("task or persona exceeds the bounded JSON Harness contract")
	}
	timeout := limits.TimeoutMillis
	if s.Timeout != nil {
		requested := s.Timeout.Duration.Milliseconds()
		if requested < 1 {
			return nil, fmt.Errorf("positive millisecond timeout required")
		}
		timeout = min(timeout, requested)
	}
	// The identity is independent of the model grant's self-reference but binds
	// run UID/spec, every approval revision and actual packaging output. Retries
	// reuse it; callers must persist this candidate before any execution effect.
	identity, err := json.Marshal(struct {
		Approval  ModelApproval
		Artifacts ExecutionArtifacts
	}{approval, artifacts})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(identity)
	executionID := "celln-" + hex.EncodeToString(digest[:])
	borrowed := frozen.Prepared.BorrowedTools
	if borrowed == nil {
		borrowed = []api.CellnBorrowedTool{}
	}
	request := map[string]any{
		"apiVersion": "celln.dev/v1alpha3", "id": executionID,
		"workload":   map[string]any{"id": string(frozen.Run.UID), "caller": approval.Caller},
		"mote":       artifacts.Mote,
		"tools":      []api.CellnToolRef{{Alias: frozen.Prepared.RuntimeEntryPoint, Hash: frozen.Prepared.RuntimeExecutable.Hash, Closure: &artifacts.Closure}},
		"invocation": api.CellnInvocation{Alias: frozen.Prepared.RuntimeEntryPoint},
		"harness": map[string]any{"contractVersion": "celln.json-tools/v1", "modelGrant": api.CellnImmutableRef{Hash: "blake3:" + strings.Repeat("0", 64)}, "model": approval.Policy.Model, "task": task, "borrowedTools": borrowed,
			"json": map[string]any{"system": s.SystemPrompt, "maxTurns": frozen.Prepared.JSON.MaxTurns, "maxCalls": frozen.Prepared.JSON.MaxCalls}},
		"capabilities": map[string]any{"workspace": "none", "egress": []string{"https://api.deepseek.com"}, "timeoutMs": timeout, "memoryBytes": limits.MemoryBytes, "outputBytes": limits.OutputBytes},
		"execution":    map[string]any{"lane": "agent", "requireHardwareIsolation": true},
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(raw) > 65536 {
		return nil, fmt.Errorf("execution request exceeds 64 KiB")
	}
	if err := l.Revalidate(ctx, frozen, approval); err != nil {
		return nil, err
	}
	return &ExecutionCandidate{APIVersion: "sympozium.ai/celln-execution-candidate-v1", Approval: approval, Request: raw}, nil
}
