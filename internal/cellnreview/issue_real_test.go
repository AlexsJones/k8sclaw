package cellnreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Invoked only by the explicit CELLN_ISSUANCE_KVM=1 composition test.
// This uses real artifacts/CLI/KVM, but fake Kubernetes and no model calls.
func proveCatalogueIssuance(t *testing.T, ctx context.Context, l cellnauthority.Loader, frozen cellnauthority.FrozenSelection, o ComposeOptions, publisher string) {
	t.Helper()
	materializer := os.Getenv("CELLN_ISSUANCE_MATERIALIZER")
	packagePath := os.Getenv("CELLN_HARNESS_PACKAGE")
	if !filepath.IsAbs(materializer) || !filepath.IsAbs(packagePath) {
		t.Fatal("explicit absolute fixture materializer and Harness package required")
	}
	var artifacts cellnauthority.ExecutionArtifacts
	if err := verify(ctx, run, materializer, []string{o.PolicyRoot, o.OutputDir, packagePath}, &artifacts); err != nil {
		t.Fatal(err)
	}
	c := l.Reader.(client.Client)
	doc := cellnauthority.ModelPolicyDocument{APIVersion: "sympozium.ai/celln-model-policy-v1", Agent: frozen.Snapshot.Agent, Runtime: frozen.Snapshot.Runtime, Provider: "deepseek", Model: "deepseek-chat", URL: "https://api.deepseek.com/chat/completions", CredentialProfile: "public-test-only", MaxRequests: 3, MaxOutputTokens: 512, MaxTotalOutputTokens: 1536}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "operators", Name: "model", UID: "model-policy"}, Data: map[string]string{"model-policy.json": string(raw)}}
	if err := c.Create(ctx, cm); err != nil {
		t.Fatal(err)
	}
	ml := cellnauthority.ModelLoader{Selection: l, Source: client.ObjectKeyFromObject(cm)}
	approval, err := ml.Resolve(ctx, frozen)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(o.PolicyRoot, "model-credentials.json"), []byte(`{"apiVersion":"sympozium.ai/celln-host-credentials-v1","profiles":{"public-test-only":"/never-read-catalogue-issuance-credential"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	options := IssueOptions{Binary: o.Binary, PolicyRoot: o.PolicyRoot, ComposerPublisher: publisher, ProfileLifetime: 5 * time.Minute}
	issued, err := Issue(ctx, ml, frozen, *approval, artifacts, options)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Issue(ctx, ml, frozen, *approval, artifacts, options)
	if err != nil {
		t.Fatal(err)
	}
	if again.Grant != issued.Grant || again.Profile != issued.Profile {
		t.Fatal("actual issuance retry changed identity")
	}
	grantPath := filepath.Join(o.PolicyRoot, "trusted-harness", issued.Grant[7:]+".json")
	before, err := os.ReadFile(grantPath)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := ReconcileIssued(ctx, o.PolicyRoot, issued.Profile, issued.ProfileSHA256, ml)
	if err != nil || observed.State != "issued" {
		t.Fatalf("current approval refused: %+v %v", observed, err)
	}
	if err := c.Delete(ctx, cm); err != nil {
		t.Fatal(err)
	}
	observed, err = ReconcileIssued(ctx, o.PolicyRoot, issued.Profile, issued.ProfileSHA256, ml)
	if err != nil || observed.State != "withdrawn" || observed.Reason != "approval-changed-or-unavailable" {
		t.Fatalf("withdrawn approval retained authority: %+v %v", observed, err)
	}
	requestFile := filepath.Join(o.PolicyRoot, "withdrawn-request.json")
	if err := os.WriteFile(requestFile, issued.Request, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := run(ctx, o.Binary, "--root", o.PolicyRoot, "harness-grant", requestFile, "--profile", issued.Profile); err == nil {
		t.Fatal("withdrawn profile still issued")
	}
	after, err := os.ReadFile(grantPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("withdrawal changed retained grant bytes")
	}
	assertNoProfiles(t, o.PolicyRoot)
	t.Logf("PASS real catalogue composition -> durable boot-bound expiring profile -> real-KVM sealed verification -> identical v3 issuance without renewal -> approval deletion -> reconciliation -> host withdrawal refusal; grant=%s; Kubernetes=fake, modelCalls=0", issued.Grant)
}
