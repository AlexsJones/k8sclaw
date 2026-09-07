package cellnauthority

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func modelFixture(t *testing.T) (ModelLoader, client.Client, *FrozenSelection) {
	t.Helper()
	l, c, old := frozenFixture(t)
	ctx := context.Background()
	var run api.AgentRun
	key := types.NamespacedName{Namespace: old.Run.Namespace, Name: old.Run.Name}
	if err := c.Get(ctx, key, &run); err != nil {
		t.Fatal(err)
	}
	run.Spec.Model = api.ModelSpec{Provider: "deepseek", Model: "deepseek-chat"}
	if err := c.Update(ctx, &run); err != nil {
		t.Fatal(err)
	}
	selection := []Selection{}
	for _, tool := range old.Snapshot.Tools {
		selection = append(selection, Selection{Name: tool.Identity.Name, Revision: tool.Identity.Revision})
	}
	frozen, err := l.FreezeRun(ctx, key, selection, old.Prepared.Composition.ImageBytes)
	if err != nil {
		t.Fatal(err)
	}
	doc := ModelPolicyDocument{APIVersion: "sympozium.ai/celln-model-policy-v1", Agent: frozen.Snapshot.Agent, Runtime: frozen.Snapshot.Runtime, Provider: "deepseek", Model: "deepseek-chat", URL: "https://api.deepseek.com/chat/completions", CredentialProfile: "reviewed-deepseek", MaxRequests: 3, MaxOutputTokens: 512, MaxTotalOutputTokens: 1536}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "operator", Name: "model-policy", UID: "model-policy-uid"}, Data: map[string]string{"model-policy.json": string(raw)}}
	if err := c.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	return ModelLoader{Selection: l, Source: client.ObjectKeyFromObject(cm)}, c, frozen
}

func TestModelApprovalPinsSelectionAndIndependentSource(t *testing.T) {
	l, _, frozen := modelFixture(t)
	approval, err := l.Resolve(context.Background(), *frozen)
	if err != nil {
		t.Fatal(err)
	}
	if approval.Caller != "sympozium:tenant/run" || !strings.HasPrefix(approval.SelectionSHA256, "sha256:") || approval.Source.UID != "model-policy-uid" {
		t.Fatalf("missing identity: %+v", approval)
	}
	raw, err := json.Marshal(approval)
	if err != nil {
		t.Fatal(err)
	}
	var restored ModelApproval
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err := l.Revalidate(context.Background(), *frozen, restored); err != nil {
		t.Fatal(err)
	}
	restored.Policy.MaxTotalOutputTokens++
	if err := l.Revalidate(context.Background(), *frozen, restored); err == nil {
		t.Fatal("accepted changed budget")
	}
}

func TestModelPolicyChecksFreshRunOptionsWithoutFallback(t *testing.T) {
	for _, mode := range []string{"provider", "model", "base-url", "thinking", "secret", "headers", "header-secret", "model-ref", "node-selector"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			l, c, old := modelFixture(t)
			var run api.AgentRun
			key := types.NamespacedName{Namespace: old.Run.Namespace, Name: old.Run.Name}
			if err := c.Get(ctx, key, &run); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "provider":
				run.Spec.Model.Provider = "other"
			case "model":
				run.Spec.Model.Model = "different"
			case "base-url":
				run.Spec.Model.BaseURL = "https://untrusted.example"
			case "thinking":
				run.Spec.Model.Thinking = "high"
			case "secret":
				run.Spec.Model.AuthSecretRef = "tenant-key"
			case "headers":
				run.Spec.Model.ProviderHeaders = map[string]string{"X-Test": "value"}
			case "header-secret":
				run.Spec.Model.ProviderHeadersSecretRef = "tenant-headers"
			case "model-ref":
				run.Spec.Model.ModelRef = "local-model"
			case "node-selector":
				run.Spec.Model.NodeSelector = map[string]string{"node": "selected"}
			}
			if err := c.Update(ctx, &run); err != nil {
				t.Fatal(err)
			}
			var selection []Selection
			for _, tool := range old.Snapshot.Tools {
				selection = append(selection, Selection{Name: tool.Identity.Name, Revision: tool.Identity.Revision})
			}
			fresh, err := l.Selection.FreezeRun(ctx, key, selection, old.Prepared.Composition.ImageBytes)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := l.Resolve(ctx, *fresh); err == nil || !strings.Contains(err.Error(), "run model") {
				t.Fatalf("unsupported options accepted or wrong refusal: %v", err)
			}
		})
	}
}

func TestModelPolicyRefusesWithdrawalSubstitutionAndExpansion(t *testing.T) {
	for _, mode := range []string{"withdrawn", "lookalike", "revision", "agent", "runtime", "provider", "model", "url", "profile-path", "budget", "underfunded", "turns", "tokens", "unknown", "trailing", "oversized", "tool-source", "run-credentials", "run-model"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			l, c, frozen := modelFixture(t)
			approval, err := l.Resolve(ctx, *frozen)
			if err != nil {
				t.Fatal(err)
			}
			var cm corev1.ConfigMap
			if err := c.Get(ctx, l.Source, &cm); err != nil {
				t.Fatal(err)
			}
			doc := approval.Policy
			switch mode {
			case "withdrawn", "lookalike":
				if err := c.Delete(ctx, &cm); err != nil {
					t.Fatal(err)
				}
				if mode == "lookalike" {
					cm.Namespace = "tenant"
					cm.UID = "forged"
					cm.ResourceVersion = ""
					if err := c.Create(ctx, &cm); err != nil {
						t.Fatal(err)
					}
				}
			case "tool-source":
				l.Source = l.Selection.AgentSource
			case "run-credentials", "run-model":
				var run api.AgentRun
				if err := c.Get(ctx, types.NamespacedName{Namespace: frozen.Run.Namespace, Name: frozen.Run.Name}, &run); err != nil {
					t.Fatal(err)
				}
				if mode == "run-credentials" {
					run.Spec.Model.AuthSecretRef = "tenant-key"
				} else {
					run.Spec.Model.Model = "different"
				}
				if err := c.Update(ctx, &run); err != nil {
					t.Fatal(err)
				}
			default:
				switch mode {
				case "revision":
					cm.Labels = map[string]string{"revision": "changed"}
				case "agent":
					doc.Agent.UID = "replacement"
				case "runtime":
					doc.Runtime.SpecSHA256 = "changed"
				case "provider":
					doc.Provider = "other"
				case "model":
					doc.Model = "other"
				case "url":
					doc.URL = "https://untrusted.example/chat/completions"
				case "profile-path":
					doc.CredentialProfile = "/host/secret"
				case "budget":
					doc.MaxTotalOutputTokens = 3073
				case "underfunded":
					doc.MaxTotalOutputTokens = 1535
				case "turns":
					doc.MaxRequests = 1
				case "tokens":
					doc.MaxOutputTokens = 513
				}
				raw, err := json.Marshal(doc)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "unknown" {
					raw = append([]byte(`{"approve":true,`), raw[1:]...)
				}
				if mode == "trailing" {
					raw = append(raw, []byte(" {}")...)
				}
				if mode == "oversized" {
					raw = []byte(strings.Repeat(" ", 65537))
				}
				cm.Data["model-policy.json"] = string(raw)
				if err := c.Update(ctx, &cm); err != nil {
					t.Fatal(err)
				}
			}
			if err := l.Revalidate(ctx, *frozen, *approval); err == nil {
				t.Fatalf("accepted %s", mode)
			}
			if mode != "revision" {
				if _, err := l.Resolve(ctx, *frozen); err == nil {
					t.Fatalf("fresh resolution accepted %s", mode)
				}
			}
		})
	}
}

type changingModelReader struct {
	client.Reader
	source types.NamespacedName
	reads  int
}

func (r *changingModelReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := r.Reader.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if key == r.source {
		r.reads++
		if r.reads == 2 {
			obj.SetResourceVersion("changed-between-reads")
		}
	}
	return nil
}

func TestModelPolicyDetectsChangeDuringResolution(t *testing.T) {
	l, c, frozen := modelFixture(t)
	r := &changingModelReader{Reader: c, source: l.Source}
	l.Selection.Reader = r
	if _, err := l.Resolve(context.Background(), *frozen); err == nil || !strings.Contains(err.Error(), "changed during resolution") {
		t.Fatalf("missed concurrent change: %v", err)
	}
	if r.reads != 2 {
		t.Fatalf("expected two independent source reads, got %d", r.reads)
	}
}
