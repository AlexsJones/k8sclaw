package main

import (
	"encoding/json"
	"errors"

	"github.com/spf13/cobra"
	"github.com/sympozium-ai/sympozium/internal/cellnreview"
)

func newCellnRecoverGrantsCmd() *cobra.Command {
	var root string
	cmd := &cobra.Command{Use: "recover-grants", Args: cobra.NoArgs, SilenceUsage: true,
		Short:             "Withdraw incomplete local issuer profiles from the durable journal",
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			recovered, err := cellnreview.RecoverPending(root)
			writeErr := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"apiVersion": "sympozium.ai/celln-issuer-recovery-v1", "withdrawnProfiles": recovered, "complete": err == nil, "scope": "local-incomplete-issuance-only", "executionAuthorized": false})
			return errors.Join(err, writeErr)
		}}
	cmd.Flags().StringVar(&root, "policy-root", "", "Absolute trusted local host policy root")
	_ = cmd.MarkFlagRequired("policy-root")
	return cmd
}
