package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/sympozium-ai/sympozium/internal/cellnreview"
)

func TestWithdrawGrantWorksWithoutKubernetesAndRetainsUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	data := []byte(`{"fixture":"profile"}`)
	digest := sha256.Sum256(data)
	name := "symp-" + hex.EncodeToString(digest[:])[:58]
	dir := filepath.Join(root, "trusted-model-profiles")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	retained := filepath.Join(root, "audit-retained")
	if err := os.WriteFile(retained, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"apiVersion": "sympozium.ai/celln-issuance-report-v1", "issued": cellnreview.IssuedSelection{APIVersion: "sympozium.ai/celln-issued-selection-v1", Profile: name, ProfileSHA256: "sha256:" + hex.EncodeToString(digest[:])}})
	if err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(root, "issuance.json")
	if err := os.WriteFile(report, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		parent := &cobra.Command{Use: "root", PersistentPreRunE: func(*cobra.Command, []string) error { return fmt.Errorf("Kubernetes unavailable") }}
		parent.AddCommand(newCellnWithdrawGrantCmd())
		parent.SetArgs([]string{"withdraw-grant", report, "--policy-root", root})
		var out bytes.Buffer
		parent.SetOut(&out)
		parent.SetErr(&out)
		if err := parent.Execute(); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(out.Bytes(), []byte(`"grantAndAuditRetained":true`)) {
			t.Fatalf("unexpected report: %s", out.Bytes())
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("profile remains")
	}
	if data, err := os.ReadFile(retained); err != nil || string(data) != "keep" {
		t.Fatal("audit changed")
	}
}
