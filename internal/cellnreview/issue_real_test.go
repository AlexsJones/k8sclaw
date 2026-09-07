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
	"k8s.io/apimachinery/pkg/types"
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
	managed, err := NewManagedIssuer(options, map[types.NamespacedName]cellnauthority.ModelLoader{{Namespace: frozen.Snapshot.Agent.Namespace, Name: frozen.Snapshot.Agent.Name}: ml}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	url, _, tokenPath := serveTestIssuer(t, managed)
	issuerClient := testIssuerClient(t, url, tokenPath, filepath.Join(filepath.Dir(tokenPath), "cert.pem"))
	seed := &IssuerRequest{APIVersion: "sympozium.ai/celln-issuer-request-v1", Frozen: frozen, Approval: *approval, Artifacts: artifacts}
	remoteIssue := func(seed *IssuerRequest) *IssuedSelection {
		issued, err := issuerClient.IssueForRun(ctx, c, c, types.NamespacedName{Namespace: frozen.Run.Namespace, Name: frozen.Run.Name}, ml, seed)
		if err != nil {
			t.Fatalf("verified remote client refused real issuance: %v", err)
		}
		return issued
	}
	issued, again := remoteIssue(seed), remoteIssue(nil)
	if again.Grant != issued.Grant || again.Profile != issued.Profile {
		t.Fatal("actual issuance retry changed identity")
	}
	runKey := types.NamespacedName{Namespace: frozen.Run.Namespace, Name: frozen.Run.Name}
	dispatchBytes, err := issuerClient.FreezeIssuedDispatch(ctx, c, c, runKey, ml)
	if err != nil || string(dispatchBytes) != string(issued.Request) {
		t.Fatalf("durable dispatch hand-off changed real issued request: %v", err)
	}
	grantPath := filepath.Join(o.PolicyRoot, "trusted-harness", issued.Grant[7:]+".json")
	before, err := os.ReadFile(grantPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, cm); err != nil {
		t.Fatal(err)
	}
	if bytes, err := issuerClient.FreezeIssuedDispatch(ctx, c, c, runKey, ml); err == nil || len(bytes) != 0 {
		t.Fatal("withdrawn approval still returned dispatch bytes")
	}
	eventuallyManaged(t, func() bool {
		_, err := os.Stat(filepath.Join(o.PolicyRoot, "trusted-model-profiles", issued.Profile+".json"))
		return os.IsNotExist(err)
	})
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
	t.Logf("PASS real catalogue composition -> durable AgentRun preparation -> verified issuer client -> authenticated TLS issuer service -> managed startup recovery gate -> durable boot-bound expiring profile -> real-KVM sealed verification -> committed AgentRun outcome -> saved outcome resume without renewal -> exact dispatch journal hand-off -> approval deletion -> hand-off refusal -> periodic managed withdrawal -> host refusal; grant=%s; Kubernetes=fake, modelCalls=0, executionSubmissions=0", issued.Grant)
}
