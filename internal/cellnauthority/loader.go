package cellnauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Subject binds approvals to a live object and its complete spec, not status.
type Subject struct {
	Kind       string    `json:"kind"`
	Namespace  string    `json:"namespace"`
	Name       string    `json:"name"`
	UID        types.UID `json:"uid"`
	Generation int64     `json:"generation"`
	SpecSHA256 string    `json:"specSHA256"`
}

func IdentifySubject(kind string, meta metav1.ObjectMeta, spec any) (Subject, error) {
	if (kind != "Agent" && kind != "AgentRuntime" && kind != "AgentRun") || meta.Name == "" || meta.Namespace == "" || meta.UID == "" || meta.Generation < 1 || meta.DeletionTimestamp != nil {
		return Subject{}, fmt.Errorf("subject must be a live namespaced Agent, AgentRun or AgentRuntime")
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return Subject{}, err
	}
	digest := sha256.Sum256(b)
	return Subject{kind, meta.Namespace, meta.Name, meta.UID, meta.Generation, "sha256:" + hex.EncodeToString(digest[:])}, nil
}

// GrantDocument is stored under grants.json in an operator-owned ConfigMap.
// All three layers bind both subjects so approvals cannot cross runtime/Agent
// combinations. Source locations are deployment configuration, never run input.
type GrantDocument struct {
	APIVersion string  `json:"apiVersion"`
	Layer      string  `json:"layer"`
	Agent      Subject `json:"agent"`
	Runtime    Subject `json:"runtime"`
	Grants     []Grant `json:"grants"`
}

type SourceRevision struct {
	Namespace       string    `json:"namespace"`
	Name            string    `json:"name"`
	UID             types.UID `json:"uid"`
	ResourceVersion string    `json:"resourceVersion"`
	SHA256          string    `json:"sha256"`
}

type Selection struct {
	Name     string
	Revision string
	Limits   *api.CellnToolLimits
}

// Loader requires an uncached APIReader and source refs selected by trusted
// controller configuration. RBAC must deny tenant writes to these ConfigMaps.
// The loader cannot infer write ownership from a ConfigMap label or status.
type Loader struct {
	Reader         client.Reader
	OperatorSource types.NamespacedName
	RuntimeSource  types.NamespacedName
	AgentSource    types.NamespacedName
}

type SelectionSnapshot struct {
	Agent       Subject              `json:"agent"`
	Runtime     Subject              `json:"runtime"`
	Sources     []SourceRevision     `json:"sources"`
	Tools       []ResolvedTool       `json:"tools"`
	RuntimeSpec api.AgentRuntimeSpec `json:"runtimeSpec"`
}

func (l Loader) Resolve(ctx context.Context, agentKey types.NamespacedName, selection []Selection) (*SelectionSnapshot, error) {
	if l.Reader == nil || agentKey.Namespace == "" || agentKey.Name == "" || len(selection) > 16 {
		return nil, fmt.Errorf("reader, Agent identity and bounded selection required")
	}
	var agent api.Agent
	if err := l.Reader.Get(ctx, agentKey, &agent); err != nil {
		return nil, err
	}
	if agent.Spec.RuntimeRef == "" {
		return nil, fmt.Errorf("Agent has no approved runtime reference")
	}
	var runtime api.AgentRuntime
	runtimeKey := types.NamespacedName{Namespace: agentKey.Namespace, Name: agent.Spec.RuntimeRef}
	if err := l.Reader.Get(ctx, runtimeKey, &runtime); err != nil {
		return nil, err
	}
	agentID, err := IdentifySubject("Agent", agent.ObjectMeta, agent.Spec)
	if err != nil {
		return nil, err
	}
	runtimeID, err := IdentifySubject("AgentRuntime", runtime.ObjectMeta, runtime.Spec)
	if err != nil {
		return nil, err
	}
	if runtime.Spec.Celln == nil || runtime.Spec.Celln.ContractVersion != "celln.json-tools/v1" {
		return nil, fmt.Errorf("runtime does not declare the JSON Celln contract")
	}
	result := &SelectionSnapshot{Agent: agentID, Runtime: runtimeID, RuntimeSpec: *runtime.Spec.DeepCopy()}
	request := ToolRequest{Namespace: agentKey.Namespace}
	refs := []types.NamespacedName{l.OperatorSource, l.RuntimeSource, l.AgentSource}
	seen := map[types.NamespacedName]bool{}
	layers := []string{"operator", "runtime", "agent"}
	grants := make([][]Grant, 3)
	for i, ref := range refs {
		if ref.Namespace == "" || ref.Name == "" || seen[ref] {
			return nil, fmt.Errorf("three distinct configured grant sources required")
		}
		seen[ref] = true
		doc, rev, err := l.read(ctx, ref)
		if err != nil {
			return nil, err
		}
		if doc.APIVersion != "sympozium.ai/celln-grants-v1" || doc.Layer != layers[i] || doc.Agent != agentID || doc.Runtime != runtimeID || doc.Grants == nil || len(doc.Grants) > 16 {
			return nil, fmt.Errorf("%s grant source absent, stale or wrong subject", layers[i])
		}
		result.Sources = append(result.Sources, rev)
		grants[i] = doc.Grants
	}
	request.Operator, request.Runtime, request.Agent = grants[0], grants[1], grants[2]
	for _, s := range selection {
		if s.Name == "" || s.Revision == "" {
			return nil, fmt.Errorf("tool name and revision required")
		}
		var tool api.CellnTool
		if err := l.Reader.Get(ctx, types.NamespacedName{Namespace: agentKey.Namespace, Name: s.Name}, &tool); err != nil {
			return nil, err
		}
		id, err := Identify(tool)
		if err != nil {
			return nil, err
		}
		if tool.Spec.InvocationABI != "celln.json-stdio/v1" || tool.Spec.Lane != "tool" {
			return nil, fmt.Errorf("selection requires an approved JSON tool-lane artifact")
		}
		if id.Revision != s.Revision {
			return nil, fmt.Errorf("selected revision changed")
		}
		limits := tool.Spec.Limits
		if s.Limits != nil {
			limits = *s.Limits.DeepCopy()
		}
		request.Selection = append(request.Selection, Grant{Tool: id, Limits: limits})
		request.Catalogue = append(request.Catalogue, tool)
	}
	result.Tools, err = ResolveTools(request)
	if err != nil {
		return nil, err
	}
	// Detect observed changes during this multi-object read. This is not a
	// Kubernetes transaction or a lease; revalidation is mandatory before work.
	for i, ref := range refs {
		_, rev, err := l.read(ctx, ref)
		if err != nil {
			return nil, err
		}
		if rev != result.Sources[i] {
			return nil, fmt.Errorf("grant source changed during resolution")
		}
	}
	var currentAgent api.Agent
	if err := l.Reader.Get(ctx, agentKey, &currentAgent); err != nil {
		return nil, err
	}
	currentAgentID, err := IdentifySubject("Agent", currentAgent.ObjectMeta, currentAgent.Spec)
	if err != nil || currentAgentID != agentID {
		return nil, fmt.Errorf("Agent changed during resolution")
	}
	var currentRuntime api.AgentRuntime
	if err := l.Reader.Get(ctx, runtimeKey, &currentRuntime); err != nil {
		return nil, err
	}
	currentRuntimeID, err := IdentifySubject("AgentRuntime", currentRuntime.ObjectMeta, currentRuntime.Spec)
	if err != nil || currentRuntimeID != runtimeID {
		return nil, fmt.Errorf("runtime changed during resolution")
	}
	for _, resolved := range result.Tools {
		var current api.CellnTool
		if err := l.Reader.Get(ctx, types.NamespacedName{Namespace: resolved.Identity.Namespace, Name: resolved.Identity.Name}, &current); err != nil {
			return nil, err
		}
		id, err := Identify(current)
		if err != nil || id != resolved.Identity {
			return nil, fmt.Errorf("tool changed during resolution")
		}
	}
	return result, nil
}

func (l Loader) read(ctx context.Context, ref types.NamespacedName) (GrantDocument, SourceRevision, error) {
	var cm corev1.ConfigMap
	if err := l.Reader.Get(ctx, ref, &cm); err != nil {
		return GrantDocument{}, SourceRevision{}, err
	}
	raw, ok := cm.Data["grants.json"]
	if !ok || len(raw) > 262144 || cm.UID == "" || cm.ResourceVersion == "" || cm.DeletionTimestamp != nil {
		return GrantDocument{}, SourceRevision{}, fmt.Errorf("grant source unavailable or invalid")
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var doc GrantDocument
	if err := decoder.Decode(&doc); err != nil {
		return doc, SourceRevision{}, fmt.Errorf("invalid grant document: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return doc, SourceRevision{}, fmt.Errorf("trailing grant data")
	}
	digest := sha256.Sum256([]byte(raw))
	return doc, SourceRevision{cm.Namespace, cm.Name, cm.UID, cm.ResourceVersion, "sha256:" + hex.EncodeToString(digest[:])}, nil
}
