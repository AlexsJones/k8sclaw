package cellnreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Real binary/artifacts, fake Kubernetes metadata. Opt in explicitly; this is
// not a live API-server or model-call proof. CELLN_ISSUANCE_KVM=1 adds an
// explicit real-KVM sealed verification and host issuance/withdrawal proof.
func TestComposeRealCellnArtifacts(t *testing.T) {
	fixture := os.Getenv("CELLN_COMPOSITION_FIXTURE")
	binary := os.Getenv("CELLN_COMPOSITION_BINARY")
	if fixture == "" || binary == "" {
		t.Skip("explicit real Celln fixture and binary required")
	}
	if !filepath.IsAbs(fixture) || !filepath.IsAbs(binary) {
		t.Fatal("absolute fixture and binary paths required")
	}
	l, _, _, _ := compositionFixture(t)
	c := l.Reader.(client.Client)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "store")
	if err := os.CopyFS(root, os.DirFS(fixture)); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(filepath.Join(root, "catalogue.json"))
	if err != nil {
		t.Fatal(err)
	}
	var catalogue struct {
		RuntimeSpec api.AgentRuntimeSpec `json:"runtimeSpec"`
		Tools       []struct {
			Name string            `json:"name"`
			Spec api.CellnToolSpec `json:"spec"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(bytes, &catalogue); err != nil {
		t.Fatal(err)
	}
	var runtime api.AgentRuntime
	if err := c.Get(ctx, types.NamespacedName{Namespace: "tenant", Name: "runtime"}, &runtime); err != nil {
		t.Fatal(err)
	}
	runtime.Spec = catalogue.RuntimeSpec
	if err := c.Update(ctx, &runtime); err != nil {
		t.Fatal(err)
	}
	var agent api.Agent
	if err := c.Get(ctx, types.NamespacedName{Namespace: "tenant", Name: "agent"}, &agent); err != nil {
		t.Fatal(err)
	}
	aid, _ := cellnauthority.IdentifySubject("Agent", agent.ObjectMeta, agent.Spec)
	rid, _ := cellnauthority.IdentifySubject("AgentRuntime", runtime.ObjectMeta, runtime.Spec)
	var grants []cellnauthority.Grant
	var selections []cellnauthority.Selection
	for _, tool := range catalogue.Tools {
		object := &api.CellnTool{ObjectMeta: metav1.ObjectMeta{Namespace: "tenant", Name: tool.Name, UID: types.UID(tool.Name), Generation: 1}, Spec: tool.Spec}
		if err := c.Create(ctx, object); err != nil {
			t.Fatal(err)
		}
		id, err := cellnauthority.Identify(*object)
		if err != nil {
			t.Fatal(err)
		}
		grants = append(grants, cellnauthority.Grant{Tool: id, Limits: tool.Spec.Limits})
		selections = append(selections, cellnauthority.Selection{Name: tool.Name, Revision: tool.Spec.Revision})
	}
	for _, ref := range []types.NamespacedName{l.OperatorSource, l.RuntimeSource, l.AgentSource} {
		var cm corev1.ConfigMap
		if err := c.Get(ctx, ref, &cm); err != nil {
			t.Fatal(err)
		}
		doc := cellnauthority.GrantDocument{APIVersion: "sympozium.ai/celln-grants-v1", Layer: ref.Name, Agent: aid, Runtime: rid, Grants: grants}
		bytes, _ := json.Marshal(doc)
		cm.Data["grants.json"] = string(bytes)
		if err := c.Update(ctx, &cm); err != nil {
			t.Fatal(err)
		}
	}
	if os.Getenv("CELLN_ISSUANCE_KVM") == "1" {
		var agentRun api.AgentRun
		if err := c.Get(ctx, types.NamespacedName{Namespace: "tenant", Name: "run"}, &agentRun); err != nil {
			t.Fatal(err)
		}
		agentRun.Spec.Model = api.ModelSpec{Provider: "deepseek", Model: "deepseek-chat"}
		agentRun.Spec.SystemPrompt = "Use the explicitly lent tools."
		if err := c.Update(ctx, &agentRun); err != nil {
			t.Fatal(err)
		}
	}
	frozen, err := l.FreezeRun(ctx, types.NamespacedName{Namespace: "tenant", Name: "run"}, selections, 33554432)
	if err != nil {
		t.Fatal(err)
	}
	options := ComposeOptions{Binary: binary, PolicyRoot: root, KeyFile: filepath.Join(root, "public-fixture-seed"), OutputDir: filepath.Join(t.TempDir(), "composed")}
	report, err := Compose(ctx, l, *frozen, options)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Sources) != 3 {
		t.Fatal("runtime plus two tools not composed")
	}
	descriptor := filepath.Join(options.OutputDir, "signed-closure.json")
	bytes, err = os.ReadFile(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	var signed struct {
		Publisher string `json:"publisher"`
	}
	if err := json.Unmarshal(bytes, &signed); err != nil {
		t.Fatal(err)
	}
	var verified closureReport
	if err := verify(ctx, run, binary, []string{"--root", root, "closure", "verify", descriptor, "--expected-hash", report.Closure, "--publisher", signed.Publisher, "--entry-point", "/harness", "--executable", catalogue.RuntimeSpec.Celln.Executable.Hash, "--toolfs", filepath.Join(options.OutputDir, "toolfs.ext2")}, &verified); err != nil {
		t.Fatal(err)
	}
	if !verified.LocalToolfsVerified || verified.Toolfs != report.Toolfs {
		t.Fatal("actual image identity mismatch")
	}
	if os.Getenv("CELLN_ISSUANCE_KVM") == "1" {
		proveCatalogueIssuance(t, ctx, l, *frozen, options, signed.Publisher)
	}
	// Withdraw a real signed source in the Celln policy: the composed artifact
	// must refuse even while all Kubernetes grant objects still exist unchanged.
	policy := filepath.Join(root, "trusted-closures.json")
	bytes, err = os.ReadFile(policy)
	if err != nil {
		t.Fatal(err)
	}
	var changed map[string]any
	if err := json.Unmarshal(bytes, &changed); err != nil {
		t.Fatal(err)
	}
	changed["revoked"] = []string{report.Sources[1]}
	bytes, _ = json.Marshal(changed)
	if err := os.WriteFile(policy, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verify(ctx, run, binary, []string{"--root", root, "closure", "verify", descriptor, "--expected-hash", report.Closure, "--publisher", signed.Publisher, "--entry-point", "/harness", "--executable", catalogue.RuntimeSpec.Celln.Executable.Hash}, &verified); err == nil {
		t.Fatal("source withdrawal did not refuse composed closure")
	}
	t.Logf("PASS real signed runtime + two tools -> actual compositor -> verified 32 MiB image -> source withdrawal refused; closure=%s toolfs=%s; Kubernetes=fake, issuanceKVM=%t, modelCalls=0", report.Closure, report.Toolfs, os.Getenv("CELLN_ISSUANCE_KVM") == "1")
}
