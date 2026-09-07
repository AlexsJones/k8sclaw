package cellnreview

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type unavailableApprovalReader struct{ client.Reader }

func (r unavailableApprovalReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return fmt.Errorf("API unavailable")
}

type stalledApprovalReader struct {
	client.Reader
	t *testing.T
}

func (r stalledApprovalReader) Get(ctx context.Context, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > 5*time.Second {
		r.t.Fatal("approval read has no bounded deadline")
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestIssuedReconciliationWithdrawsOnDeadline(t *testing.T) {
	f := provisionFixture(t)
	issued, err := issue(context.Background(), f.l, *f.f, *f.a, f.artifacts, f.o, f.run)
	if err != nil {
		t.Fatal(err)
	}
	f.l.Selection.Reader = stalledApprovalReader{f.c, t}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	result, err := ReconcileIssued(ctx, f.o.PolicyRoot, issued.Profile, issued.ProfileSHA256, f.l)
	if err != nil || result.State != "withdrawn" || result.Reason != "approval-changed-or-unavailable" {
		t.Fatalf("deadline retained stale authority: %+v %v", result, err)
	}
	assertNoProfiles(t, f.o.PolicyRoot)
	// Restored API availability must not silently recreate authority.
	f.l.Selection.Reader = f.c
	result, err = ReconcileIssued(context.Background(), f.o.PolicyRoot, issued.Profile, issued.ProfileSHA256, f.l)
	if err != nil || result.State != "withdrawn" || result.Reason != "already-withdrawn" {
		t.Fatalf("reconciliation recreated authority: %+v %v", result, err)
	}
	assertNoProfiles(t, f.o.PolicyRoot)
}

func TestIssuedApprovalReconciliationOnlyShrinksAuthority(t *testing.T) {
	for _, mode := range []string{"unchanged", "tool-policy-withdrawn", "model-policy-withdrawn", "API-unavailable", "mapping-retargeted", "mapping-unavailable", "profile-absent"} {
		t.Run(mode, func(t *testing.T) {
			f := provisionFixture(t)
			ctx := context.Background()
			issued, err := issue(ctx, f.l, *f.f, *f.a, f.artifacts, f.o, f.run)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.o.PolicyRoot, "trusted-model-profiles", issued.Profile+".json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "tool-policy-withdrawn", "model-policy-withdrawn":
				ref := f.l.Source
				if mode == "tool-policy-withdrawn" {
					ref = f.l.Selection.OperatorSource
				}
				var cm corev1.ConfigMap
				if err := f.c.Get(ctx, ref, &cm); err != nil {
					t.Fatal(err)
				}
				if err := f.c.Delete(ctx, &cm); err != nil {
					t.Fatal(err)
				}
			case "API-unavailable":
				f.l.Selection.Reader = unavailableApprovalReader{f.c}
			case "mapping-retargeted":
				if err := os.WriteFile(filepath.Join(f.o.PolicyRoot, "model-credentials.json"), []byte(`{"apiVersion":"sympozium.ai/celln-host-credentials-v1","profiles":{"host-deepseek":"/different-host-credential"}}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "mapping-unavailable":
				if err := os.Remove(filepath.Join(f.o.PolicyRoot, "model-credentials.json")); err != nil {
					t.Fatal(err)
				}
			case "profile-absent":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			result, err := ReconcileIssued(ctx, f.o.PolicyRoot, issued.Profile, issued.ProfileSHA256, f.l)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unchanged" {
				if result.State != "issued" || result.Reason != "approval-observed-current" {
					t.Fatalf("wrong observation: %+v", result)
				}
				after, err := os.ReadFile(path)
				if err != nil || string(after) != string(before) {
					t.Fatal("observation changed profile authority")
				}
			} else {
				if result.State != "withdrawn" {
					t.Fatalf("stale authority remains: %+v", result)
				}
				assertNoProfiles(t, f.o.PolicyRoot)
				again, err := ReconcileIssued(ctx, f.o.PolicyRoot, issued.Profile, issued.ProfileSHA256, f.l)
				if err != nil || again.Reason != "already-withdrawn" {
					t.Fatalf("reconciliation not idempotent: %+v %v", again, err)
				}
			}
			if data, err := os.ReadFile(filepath.Join(f.o.PolicyRoot, "grant-audit-sentinel")); err != nil || string(data) != "retain" {
				t.Fatal("reconciliation modified grant/audit data")
			}
		})
	}
}

func TestIssuedReconciliationRefusesChangedProfile(t *testing.T) {
	f := provisionFixture(t)
	issued, err := issue(context.Background(), f.l, *f.f, *f.a, f.artifacts, f.o, f.run)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.o.PolicyRoot, "trusted-model-profiles", issued.Profile+".json")
	if err := os.WriteFile(path, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileIssued(context.Background(), f.o.PolicyRoot, issued.Profile, issued.ProfileSHA256, f.l); err == nil {
		t.Fatal("accepted changed profile")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "unrelated" {
		t.Fatal("deleted unrelated state")
	}
}
