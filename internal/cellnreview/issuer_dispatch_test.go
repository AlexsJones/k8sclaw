package cellnreview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

type forbiddenDispatchTransport struct{ t *testing.T }

func (f forbiddenDispatchTransport) RoundTrip(*http.Request) (*http.Response, error) {
	f.t.Error("dispatch hand-off contacted issuer")
	return nil, fmt.Errorf("network forbidden in hand-off")
}

func TestFreezeIssuedDispatchExactOutcomeAndLostCommit(t *testing.T) {
	for _, lostCommit := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "lost-commit"}[lostCommit], func(t *testing.T) {
			f, m, _ := managedFixture(t)
			endpoint, _, tokenPath := serveTestIssuer(t, m)
			issuer := testIssuerClient(t, endpoint, tokenPath, filepath.Join(filepath.Dir(tokenPath), "cert.pem"))
			key := types.NamespacedName{Namespace: f.f.Run.Namespace, Name: f.f.Run.Name}
			ctx := context.Background()
			seed := &IssuerRequest{APIVersion: "sympozium.ai/celln-issuer-request-v1", Frozen: *f.f, Approval: *f.a, Artifacts: f.artifacts}
			issued, err := issuer.IssueForRun(ctx, f.c, f.c, key, f.l, seed)
			if err != nil {
				t.Fatal(err)
			}
			// No hand-off path may provision again.
			issuer.CloseIdleConnections()
			issuer.http = &http.Client{Transport: forbiddenDispatchTransport{t}}
			if lostCommit {
				bytes, err := issuer.FreezeIssuedDispatch(ctx, issuanceStatusFault{Client: f.c, phase: "Issued", afterCommit: true}, f.c, key, f.l)
				if err == nil || len(bytes) != 0 {
					t.Fatal("ambiguous status commit returned dispatch bytes")
				}
			}
			for i := 0; i < 2; i++ {
				bytes, err := issuer.FreezeIssuedDispatch(ctx, f.c, f.c, key, f.l)
				if err != nil || string(bytes) != string(issued.Request) {
					t.Fatalf("hand-off changed bytes: %v", err)
				}
			}
			var current api.AgentRun
			if err := f.c.Get(ctx, key, &current); err != nil {
				t.Fatal(err)
			}
			var request struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(issued.Request, &request); err != nil {
				t.Fatal(err)
			}
			if current.Status.CellnActionID != request.ID || current.Status.Phase == api.AgentRunPhaseRunning || current.Status.StartedAt != nil {
				t.Fatal("hand-off replaced identity or asserted submission")
			}
		})
	}
}

func TestFreezeIssuedDispatchRefusesChangedState(t *testing.T) {
	for _, mode := range []string{"unissued", "target", "task", "withdrawn", "request", "action", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			f, m, _ := managedFixture(t)
			endpoint, _, tokenPath := serveTestIssuer(t, m)
			issuer := testIssuerClient(t, endpoint, tokenPath, filepath.Join(filepath.Dir(tokenPath), "cert.pem"))
			key := types.NamespacedName{Namespace: f.f.Run.Namespace, Name: f.f.Run.Name}
			ctx := context.Background()
			seed := &IssuerRequest{APIVersion: "sympozium.ai/celln-issuer-request-v1", Frozen: *f.f, Approval: *f.a, Artifacts: f.artifacts}
			if _, err := issuer.IssueForRun(ctx, f.c, f.c, key, f.l, seed); err != nil {
				t.Fatal(err)
			}
			var current api.AgentRun
			if err := f.c.Get(ctx, key, &current); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "unissued":
				current.Status.CellnIssuance.Phase = "Prepared"
				current.Status.CellnIssuance.Result = ""
			case "target":
				issuer.endpoint = "https://other-host/v1/issuances"
			case "task":
				current.Spec.Task = api.NewStringTask("substituted")
				if err := f.c.Update(ctx, &current); err != nil {
					t.Fatal(err)
				}
			case "withdrawn":
				var policy corev1.ConfigMap
				if err := f.c.Get(ctx, f.l.Source, &policy); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(ctx, &policy); err != nil {
					t.Fatal(err)
				}
			case "request":
				current.Status.CellnRequest = "{}"
			case "action":
				current.Status.CellnActionID = "legacy-id"
			case "terminal":
				current.Status.Phase = api.AgentRunPhaseFailed
			}
			if err := f.c.Status().Update(ctx, &current); err != nil {
				t.Fatal(err)
			}
			if bytes, err := issuer.FreezeIssuedDispatch(ctx, f.c, f.c, key, f.l); err == nil || len(bytes) != 0 {
				t.Fatal("changed state returned dispatch bytes")
			}
		})
	}
}
