package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/sympozium-ai/sympozium/internal/cellnreview"
)

func newCellnWithdrawGrantCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{Use: "withdraw-grant ISSUANCE_REPORT", Args: cobra.ExactArgs(1), SilenceUsage: true,
		Short: "Withdraw the exact local host profile from a saved issuance report",
		// Recovery must remain available when Kubernetes is unreachable.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(args[0])
			if err != nil {
				return err
			}
			defer f.Close()
			raw, err := io.ReadAll(io.LimitReader(f, 1048577))
			if err != nil {
				return err
			}
			if len(raw) > 1048576 {
				return fmt.Errorf("issuance report exceeds 1 MiB")
			}
			var report struct {
				APIVersion string                      `json:"apiVersion"`
				Issued     cellnreview.IssuedSelection `json:"issued"`
			}
			if err := json.Unmarshal(raw, &report); err != nil {
				return err
			}
			if report.APIVersion != "sympozium.ai/celln-issuance-report-v1" || report.Issued.APIVersion != "sympozium.ai/celln-issued-selection-v1" {
				return fmt.Errorf("invalid issuance report")
			}
			if err := cellnreview.Withdraw(root, report.Issued); err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"apiVersion": "sympozium.ai/celln-withdrawal-report-v1", "profile": report.Issued.Profile, "scope": "local-profile-only", "grantAndAuditRetained": true})
		}}
	cmd.Flags().StringVar(&root, "policy-root", "", "Absolute trusted local host policy root")
	_ = cmd.MarkFlagRequired("policy-root")
	return cmd
}
