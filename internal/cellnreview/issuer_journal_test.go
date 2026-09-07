package cellnreview

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestProvisioningCrashHelper(t *testing.T) {
	root := os.Getenv("CELLN_TEST_ISSUER_CRASH_ROOT")
	if root == "" {
		t.Skip("subprocess helper only")
	}
	f := provisionFixtureAt(t, root)
	stage := os.Getenv("CELLN_TEST_ISSUER_CRASH_STAGE")
	run := func(ctx context.Context, binary string, args ...string) ([]byte, error) {
		isIssue := args[0] == "--root" && args[2] == "harness-grant"
		if isIssue && stage == "before-host" {
			os.Exit(47)
		}
		out, err := f.run(ctx, binary, args...)
		if isIssue && stage == "after-host" {
			os.Exit(47)
		}
		return out, err
	}
	issued, err := issue(context.Background(), f.l, *f.f, *f.a, f.artifacts, f.o, run)
	if err != nil {
		t.Fatal(err)
	}
	if stage == "withdrawing" {
		record, err := ReadIssuance(root, issued.Profile, issued.ProfileSHA256)
		if err != nil {
			t.Fatal(err)
		}
		record.State = "withdrawing"
		unlock, err := lockIssuer(root)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeIssuerRecord(root, record); err != nil {
			t.Fatal(err)
		}
		_ = unlock // deliberately exit with lock held, as in interrupted withdrawal.
	}
	os.Exit(47) // bypass all Go cleanup; issued outcome is already durable.
}

func TestIssuerRecoveryAfterActualProcessExit(t *testing.T) {
	for _, stage := range []string{"before-host", "after-host", "committed", "withdrawing"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestProvisioningCrashHelper$")
			cmd.Env = append(os.Environ(), "CELLN_TEST_ISSUER_CRASH_ROOT="+root, "CELLN_TEST_ISSUER_CRASH_STAGE="+stage)
			out, err := cmd.CombinedOutput()
			var status *exec.ExitError
			if !errors.As(err, &status) || status.ExitCode() != 47 {
				t.Fatalf("expected abrupt exit: %v %s", err, out)
			}
			entries, err := os.ReadDir(filepath.Join(root, "sympozium-issuer-journal"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 {
				t.Fatalf("missing durable record: %v", entries)
			}
			path := filepath.Join(root, "sympozium-issuer-journal", entries[0].Name())
			record, err := readIssuerRecord(path)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := RecoverPending(root)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "committed" {
				if len(recovered) != 0 || record.State != "issued" || record.Issued == nil {
					t.Fatal("lost committed outcome")
				}
				if _, err := os.Stat(filepath.Join(root, "trusted-model-profiles", record.Profile+".json")); err != nil {
					t.Fatal(err)
				}
				if err := Withdraw(root, *record.Issued); err != nil {
					t.Fatal(err)
				}
			} else {
				if len(recovered) != 1 || recovered[0] != record.Profile {
					t.Fatal("pending authority not recovered")
				}
			}
			assertNoProfiles(t, root)
			after, err := readIssuerRecord(path)
			if err != nil || after.State != "withdrawn" {
				t.Fatalf("withdrawal not durable: %+v %v", after, err)
			}
			if stage != "before-host" {
				if data, err := os.ReadFile(filepath.Join(root, "grant-audit-sentinel")); err != nil || string(data) != "retain" {
					t.Fatal("recovery changed audit/grant bytes")
				}
			}
			if again, err := RecoverPending(root); err != nil || len(again) != 0 {
				t.Fatalf("recovery not idempotent: %v %v", again, err)
			}
		})
	}
}

func TestJournalRecoveryRefusesChangedProfileAndCorruptRecord(t *testing.T) {
	f := provisionFixture(t)
	issued, err := issue(context.Background(), f.l, *f.f, *f.a, f.artifacts, f.o, f.run)
	if err != nil {
		t.Fatal(err)
	}
	record, err := ReadIssuance(f.o.PolicyRoot, issued.Profile, issued.ProfileSHA256)
	if err != nil {
		t.Fatal(err)
	}
	record.State = "pending"
	if err := writeIssuerRecord(f.o.PolicyRoot, record); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(f.o.PolicyRoot, "trusted-model-profiles", issued.Profile+".json")
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverPending(f.o.PolicyRoot); err == nil {
		t.Fatal("removed unrelated profile")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "replacement" {
		t.Fatal("changed profile was deleted")
	}
	journal := filepath.Join(f.o.PolicyRoot, "sympozium-issuer-journal", issued.Profile+".json")
	if err := os.WriteFile(journal, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := issue(context.Background(), f.l, *f.f, *f.a, f.artifacts, f.o, f.run); err == nil {
		t.Fatal("issuance ignored broken recovery record")
	}
}
