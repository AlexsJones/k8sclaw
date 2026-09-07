package cellnreview

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"

	api "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type issuanceStatusFault struct {
	client.Client
	phase       string
	afterCommit bool
}
type issuanceStatusWriter struct {
	client.SubResourceWriter
	owner issuanceStatusFault
}

func (f issuanceStatusFault) Status() client.SubResourceWriter {
	return issuanceStatusWriter{f.Client.Status(), f}
}
func (w issuanceStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	run := obj.(*api.AgentRun)
	fail := run.Status.CellnIssuance != nil && run.Status.CellnIssuance.Phase == w.owner.phase
	if fail && !w.owner.afterCommit {
		return fmt.Errorf("injected status failure before commit")
	}
	if err := w.SubResourceWriter.Update(ctx, obj, opts...); err != nil {
		return err
	}
	if fail {
		return fmt.Errorf("injected lost status response after commit")
	}
	return nil
}

func TestRunIssuancePersistsBeforeRemoteAndRecoversLostStatusResponses(t *testing.T) {
	for _, mode := range []string{"prepared-before", "prepared-after", "issued-before", "issued-after"} {
		t.Run(mode, func(t *testing.T) {
			f, m, _ := managedFixture(t)
			key := types.NamespacedName{Namespace: f.f.Run.Namespace, Name: f.f.Run.Name}
			var calls atomic.Int32
			next := m.execute
			m.execute = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
				if args[0] == "--root" && args[2] == "harness-grant" {
					var run api.AgentRun
					if err := f.c.Get(ctx, key, &run); err != nil {
						t.Error(err)
					}
					if run.Status.CellnIssuance == nil || run.Status.CellnIssuance.Phase != "Prepared" {
						t.Error("remote side effect preceded durable preparation")
					}
					calls.Add(1)
				}
				return next(ctx, binary, args...)
			}
			endpoint, _, tokenPath := serveTestIssuer(t, m)
			issuer := testIssuerClient(t, endpoint, tokenPath, filepath.Join(filepath.Dir(tokenPath), "cert.pem"))
			seed := &IssuerRequest{APIVersion: "sympozium.ai/celln-issuer-request-v1", Frozen: *f.f, Approval: *f.a, Artifacts: f.artifacts}
			fault := issuanceStatusFault{Client: f.c, phase: "Prepared"}
			if mode == "issued-before" || mode == "issued-after" {
				fault.phase = "Issued"
			}
			fault.afterCommit = mode == "prepared-after" || mode == "issued-after"
			ctx := context.Background()
			if _, err := issuer.IssueForRun(ctx, fault, f.c, key, f.l, seed); err == nil {
				t.Fatal("injected status failure disappeared")
			}
			var current api.AgentRun
			if err := f.c.Get(ctx, key, &current); err != nil {
				t.Fatal(err)
			}
			resumeSeed := (*IssuerRequest)(nil)
			if mode == "prepared-before" {
				if current.Status.CellnIssuance != nil || calls.Load() != 0 {
					t.Fatal("failed preparation leaked remote authority")
				}
				resumeSeed = seed
			} else if current.Status.CellnIssuance == nil {
				t.Fatal("lost durable preparation")
			}
			result, err := issuer.IssueForRun(ctx, f.c, f.c, key, f.l, resumeSeed)
			if err != nil {
				t.Fatal(err)
			}
			count := calls.Load()
			wantCalls := int32(1)
			if mode == "issued-before" {
				wantCalls = 2
			}
			if count != wantCalls {
				t.Fatalf("unexpected remote attempts: %d", count)
			}
			again, err := issuer.IssueForRun(ctx, f.c, f.c, key, f.l, nil)
			if err != nil || again.Grant != result.Grant || calls.Load() != count {
				t.Fatalf("saved outcome caused another remote call: %v", err)
			}
			if err := f.c.Get(ctx, key, &current); err != nil {
				t.Fatal(err)
			}
			if current.Status.CellnIssuance.Phase != "Issued" || current.Status.CellnActionID != "" || current.Status.CellnRequest != "" {
				t.Fatal("provisioning changed dispatch state")
			}
		})
	}
}

func TestRunIssuanceRefusesRetargetedOrChangedSavedState(t *testing.T) {
	for _, mode := range []string{"target", "seed", "spec", "payload", "candidate", "terminal"} {
		t.Run(mode, func(t *testing.T) {
			f, m, _ := managedFixture(t)
			endpoint, _, tokenPath := serveTestIssuer(t, m)
			issuer := testIssuerClient(t, endpoint, tokenPath, filepath.Join(filepath.Dir(tokenPath), "cert.pem"))
			key := types.NamespacedName{Namespace: f.f.Run.Namespace, Name: f.f.Run.Name}
			seed := &IssuerRequest{APIVersion: "sympozium.ai/celln-issuer-request-v1", Frozen: *f.f, Approval: *f.a, Artifacts: f.artifacts}
			ctx := context.Background()
			if _, err := issuer.IssueForRun(ctx, issuanceStatusFault{Client: f.c, phase: "Prepared", afterCommit: true}, f.c, key, f.l, seed); err == nil {
				t.Fatal("lost write should refuse")
			}
			var run api.AgentRun
			if err := f.c.Get(ctx, key, &run); err != nil {
				t.Fatal(err)
			}
			var resume *IssuerRequest
			switch mode {
			case "target":
				issuer.endpoint = "https://other-host/v1/issuances"
			case "seed":
				changed := *seed
				changed.Artifacts.Mote.Hash = "blake3:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				resume = &changed
			case "spec":
				run.Spec.SystemPrompt = "changed"
				if err := f.c.Update(ctx, &run); err != nil {
					t.Fatal(err)
				}
			case "payload":
				run.Status.CellnIssuance.Payload = "{}"
			case "candidate":
				payload, _, err := decodeRunIssuance(run.Status.CellnIssuance, issuer.endpoint)
				if err != nil {
					t.Fatal(err)
				}
				payload.Candidate.APIVersion = "wrong"
				data, err := json.Marshal(payload)
				if err != nil {
					t.Fatal(err)
				}
				run.Status.CellnIssuance.Payload = string(data)
				run.Status.CellnIssuance.PayloadSHA256 = payloadHash(string(data))
			case "terminal":
				run.Status.Phase = api.AgentRunPhaseFailed
			}
			if mode == "payload" || mode == "candidate" || mode == "terminal" {
				if err := f.c.Status().Update(ctx, &run); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := issuer.IssueForRun(ctx, f.c, f.c, key, f.l, resume); err == nil {
				t.Fatal("retargeted saved state accepted")
			}
			assertNoProfiles(t, f.o.PolicyRoot)
		})
	}
}
