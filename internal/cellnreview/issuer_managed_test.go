package cellnreview

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

func managedFixture(t *testing.T) (issuerFixture, *ManagedIssuer, *profileClock) {
	t.Helper()
	f := provisionFixture(t)
	f.o.ProfileLifetime = time.Minute
	m, err := NewManagedIssuer(f.o, map[types.NamespacedName]cellnauthority.ModelLoader{{Namespace: f.f.Snapshot.Agent.Namespace, Name: f.f.Snapshot.Agent.Name}: f.l}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clock, calls := testProfileClock(), 0
	m.execute = clockRunner(f.run, &clock, &calls)
	return f, m, &clock
}

func eventuallyManaged(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatal("managed issuer observation timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManagedIssuerStartupIssuePeriodicWithdrawalAndShutdown(t *testing.T) {
	f, m, _ := managedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	if _, err := m.Issue(ctx, *f.f, *f.a, f.artifacts); err == nil {
		t.Fatal("issued before startup")
	}
	go func() { done <- m.Start(ctx) }()
	defer func() { cancel(); <-done }()
	eventuallyManaged(t, func() bool { ready, _ := m.Status(); return ready })
	issued, err := m.Issue(ctx, *f.f, *f.a, f.artifacts)
	if err != nil {
		t.Fatal(err)
	}
	var cm corev1.ConfigMap
	if err := f.c.Get(ctx, f.l.Source, &cm); err != nil {
		t.Fatal(err)
	}
	if err := f.c.Delete(ctx, &cm); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.o.PolicyRoot, "trusted-model-profiles", issued.Profile+".json")
	eventuallyManaged(t, func() bool { _, err := os.Stat(path); return os.IsNotExist(err) })
	if _, err := m.Issue(ctx, *f.f, *f.a, f.artifacts); err == nil {
		t.Fatal("issued after approval withdrawal")
	}
	cancel()
	if ready, _ := m.Status(); ready {
		t.Fatal("gate open after cancellation")
	}
	if _, err := m.Issue(context.Background(), *f.f, *f.a, f.artifacts); err == nil {
		t.Fatal("issued after shutdown")
	}
}

func TestManagedStartupSweepsLegacyExpiredUnknownAndInterruptedAuthority(t *testing.T) {
	for _, mode := range []string{"legacy", "expired", "unconfigured", "pending", "api-unavailable", "clock-unavailable"} {
		t.Run(mode, func(t *testing.T) {
			f, m, clock := managedFixture(t)
			options := f.o
			if mode == "legacy" {
				options.ProfileLifetime = 0
			}
			issued, err := issue(context.Background(), f.l, *f.f, *f.a, f.artifacts, options, m.execute)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "expired":
				clock.Now = 61000
			case "unconfigured":
				m.loaders = nil
			case "clock-unavailable":
				clock.ExecutionAuthorized = true
			case "pending":
				r, err := ReadIssuance(f.o.PolicyRoot, issued.Profile, issued.ProfileSHA256)
				if err != nil {
					t.Fatal(err)
				}
				r.State = "pending"
				if err := writeIssuerRecord(f.o.PolicyRoot, r); err != nil {
					t.Fatal(err)
				}
			case "api-unavailable":
				for key, loader := range m.loaders {
					loader.Selection.Reader = unavailableApprovalReader{f.c}
					m.loaders[key] = loader
				}
			}
			err = m.sweep(context.Background())
			if mode == "clock-unavailable" {
				if err == nil {
					t.Fatal("clock failure did not close gate")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			assertNoProfiles(t, f.o.PolicyRoot)
			r, err := ReadIssuance(f.o.PolicyRoot, issued.Profile, issued.ProfileSHA256)
			if err != nil || r.State != "withdrawn" {
				t.Fatalf("withdrawal not durable: %+v %v", r, err)
			}
		})
	}
}

func TestManagedIssuerRefusesUntrackedOrChangedHostAuthority(t *testing.T) {
	for _, mode := range []string{"untracked", "changed"} {
		t.Run(mode, func(t *testing.T) {
			f, m, _ := managedFixture(t)
			issued, err := issue(context.Background(), f.l, *f.f, *f.a, f.artifacts, f.o, m.execute)
			if err != nil {
				t.Fatal(err)
			}
			name := "unrelated.json"
			if mode == "changed" {
				name = issued.Profile + ".json"
			}
			path := filepath.Join(f.o.PolicyRoot, "trusted-model-profiles", name)
			if err := os.WriteFile(path, []byte("unrelated"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := m.sweep(context.Background()); err == nil {
				t.Fatal("unsafe startup accepted")
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "unrelated" {
				t.Fatal("deleted unrelated authority")
			}
		})
	}
}

func TestManagedGateStaysClosedUntilStartupFaultIsRemoved(t *testing.T) {
	f, m, _ := managedFixture(t)
	dir := filepath.Join(f.o.PolicyRoot, "trusted-model-profiles")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "untracked.json")
	if err := os.WriteFile(path, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Start(ctx) }()
	defer func() { cancel(); <-done }()
	eventuallyManaged(t, func() bool { ready, err := m.Status(); return !ready && err != nil })
	if _, err := m.Issue(ctx, *f.f, *f.a, f.artifacts); err == nil {
		t.Fatal("issued through failed startup gate")
	}
	if err := m.Start(ctx); err == nil {
		t.Fatal("accepted duplicate lifecycle owner")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	eventuallyManaged(t, func() bool { ready, err := m.Status(); return ready && err == nil })
}

func TestManagedShutdownCancelsInflightIssuance(t *testing.T) {
	f, m, _ := managedFixture(t)
	entered := make(chan struct{})
	next := m.execute
	m.execute = func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if args[0] == "--root" && args[2] == "harness-grant" {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return next(ctx, binary, args...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.Start(ctx) }()
	defer func() { cancel(); <-done }()
	eventuallyManaged(t, func() bool { ready, _ := m.Status(); return ready })
	issued := make(chan error, 1)
	go func() { _, err := m.Issue(context.Background(), *f.f, *f.a, f.artifacts); issued <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("issuer did not start")
	}
	cancel()
	select {
	case err := <-issued:
		if err == nil {
			t.Fatal("issuance survived shutdown")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel issuance")
	}
	assertNoProfiles(t, f.o.PolicyRoot)
}
