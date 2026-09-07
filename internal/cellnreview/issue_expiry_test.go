package cellnreview

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func clockRunner(next runner, clock *profileClock, calls *int) runner {
	return func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		if args[0] == "harness-profile-clock" {
			*calls++
			return json.Marshal(clock)
		}
		return next(ctx, binary, args...)
	}
}

func testProfileClock() profileClock {
	return profileClock{APIVersion: "celln.dev/model-profile-clock-v1", BootID: "01234567-89ab-cdef-0123-456789abcdef", Now: 1000, MaxLifetime: 300000}
}

func TestBoundedIssuanceRetryNeverRenewsOrRestoresAuthority(t *testing.T) {
	for _, mode := range []string{"expiry", "reboot", "withdrawn", "profile-removed", "pending", "changed-lifetime", "corrupt-window"} {
		t.Run(mode, func(t *testing.T) {
			f := provisionFixture(t)
			f.o.ProfileLifetime = time.Minute
			clock, calls := testProfileClock(), 0
			runner := clockRunner(f.run, &clock, &calls)
			ctx := context.Background()
			issued, err := issue(ctx, f.l, *f.f, *f.a, f.artifacts, f.o, runner)
			if err != nil {
				t.Fatal(err)
			}
			profilePath := filepath.Join(f.o.PolicyRoot, "trusted-model-profiles", issued.Profile+".json")
			original, err := os.ReadFile(profilePath)
			if err != nil {
				t.Fatal(err)
			}
			clock.Now += 10000
			again, err := issue(ctx, f.l, *f.f, *f.a, f.artifacts, f.o, runner)
			if err != nil || again.Profile != issued.Profile || again.Grant != issued.Grant {
				t.Fatalf("retry changed identity: %+v %v", again, err)
			}
			if current, err := os.ReadFile(profilePath); err != nil || string(current) != string(original) {
				t.Fatal("retry renewed profile")
			}
			switch mode {
			case "expiry":
				clock.Now = 61000
			case "reboot":
				clock.BootID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
			case "withdrawn":
				if err := Withdraw(f.o.PolicyRoot, *issued); err != nil {
					t.Fatal(err)
				}
			case "profile-removed":
				if err := os.Remove(profilePath); err != nil {
					t.Fatal(err)
				}
			case "pending":
				r, err := ReadIssuance(f.o.PolicyRoot, issued.Profile, issued.ProfileSHA256)
				if err != nil {
					t.Fatal(err)
				}
				r.State = "pending"
				if err := writeIssuerRecord(f.o.PolicyRoot, r); err != nil {
					t.Fatal(err)
				}
			case "changed-lifetime":
				f.o.ProfileLifetime = 2 * time.Minute
			case "corrupt-window":
				entries, err := os.ReadDir(filepath.Join(f.o.PolicyRoot, "sympozium-issuer-windows"))
				if err != nil || len(entries) != 1 {
					t.Fatalf("missing window: %v %v", entries, err)
				}
				if err := os.WriteFile(filepath.Join(f.o.PolicyRoot, "sympozium-issuer-windows", entries[0].Name()), []byte("broken"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := issue(ctx, f.l, *f.f, *f.a, f.artifacts, f.o, runner); err == nil {
				t.Fatal("retry restored or extended authority")
			}
			if mode == "withdrawn" || mode == "pending" || mode == "profile-removed" {
				assertNoProfiles(t, f.o.PolicyRoot)
			}
			if calls < 4 {
				t.Fatal("missing initial/final host clock checks")
			}
		})
	}
}

func TestBoundedIssuanceRefusesInvalidClockOrWindow(t *testing.T) {
	for _, mode := range []string{"invalid-clock", "overflow", "negative", "too-long", "fractional", "expiry-during-issuance"} {
		t.Run(mode, func(t *testing.T) {
			f := provisionFixture(t)
			f.o.ProfileLifetime = time.Minute
			clock, calls := testProfileClock(), 0
			switch mode {
			case "invalid-clock":
				clock.ExecutionAuthorized = true
			case "overflow":
				clock.Now = ^uint64(0)
			case "negative":
				f.o.ProfileLifetime = -time.Second
			case "too-long":
				f.o.ProfileLifetime = 6 * time.Minute
			case "fractional":
				f.o.ProfileLifetime = time.Millisecond + time.Nanosecond
			}
			next := clockRunner(f.run, &clock, &calls)
			runner := func(ctx context.Context, binary string, args ...string) ([]byte, error) {
				out, err := next(ctx, binary, args...)
				if mode == "expiry-during-issuance" && args[0] == "--root" && args[2] == "harness-grant" {
					clock.Now = 61000
				}
				return out, err
			}
			if _, err := issue(context.Background(), f.l, *f.f, *f.a, f.artifacts, f.o, runner); err == nil {
				t.Fatal("invalid bounded issuance accepted")
			}
			assertNoProfiles(t, f.o.PolicyRoot)
		})
	}
}
