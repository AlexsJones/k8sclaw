package main

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/sympozium-ai/sympozium/internal/cellnreview"
	"k8s.io/apimachinery/pkg/types"
)

func newCellnToolCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "celln-tool", Short: "Inspect submissions and publish operator-reviewed Celln catalogue metadata"}
	cmd.AddCommand(&cobra.Command{
		Use: "inspect NAME", Args: cobra.ExactArgs(1), Short: "Print exact submission identity and requested behavior for operator review",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, identity, err := cellnreview.Inspect(cmd.Context(), k8sClient, namespace, args[0])
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"identity": identity, "spec": s.Spec})
		},
	})
	var o cellnreview.Options
	var uid string
	approve := &cobra.Command{
		SilenceUsage: true,
		Use:          "approve NAME", Args: cobra.ExactArgs(1),
		Short: "Verify a local bundle and publish a reviewed revision (not execution readiness)",
		Long:  "Requires reviewer RBAC and explicit UID/spec acknowledgement after reviewing behavior. The operator supplies a trusted Celln binary, publisher/revocation policy and local closure/toolfs/schema files. Does not pull tenant URLs, write Ready status, distribute, prewarm or execute tools.",
		RunE: func(cmd *cobra.Command, args []string) error {
			o.Namespace, o.Name, o.SubmissionUID = namespace, args[0], types.UID(uid)
			tool, err := cellnreview.Approve(cmd.Context(), k8sClient, o)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Published CellnTool %s/%s; conformance, distribution and execution readiness remain unverified.\n", tool.Namespace, tool.Name)
			return err
		},
	}
	approve.Flags().StringVar(&uid, "reviewed-uid", "", "Exact inspected submission UID")
	approve.Flags().StringVar(&o.ReviewedSpecSHA256, "reviewed-spec-sha256", "", "Exact inspected full-spec hash acknowledging behavior review")
	approve.Flags().StringVar(&o.Binary, "celln-binary", "", "Absolute trusted Celln binary path (requires closure verify --toolfs)")
	approve.Flags().StringVar(&o.PolicyRoot, "policy-root", "", "Absolute operator-controlled root containing trusted-closures.json")
	approve.Flags().StringVar(&o.BundleDir, "bundle-dir", "", "Absolute operator-staged directory with closure.json, toolfs.ext2, arguments.schema.json and result.schema.json")
	for _, flag := range []string{"reviewed-uid", "reviewed-spec-sha256", "celln-binary", "policy-root", "bundle-dir"} {
		_ = approve.MarkFlagRequired(flag)
	}
	cmd.AddCommand(approve)
	cmd.AddCommand(newCellnSelectionPlanCmd())
	cmd.AddCommand(newCellnSelectionComposeCmd())
	cmd.AddCommand(newCellnSelectionIssueCmd())
	cmd.AddCommand(newCellnWithdrawGrantCmd())
	cmd.AddCommand(newCellnRecoverGrantsCmd())
	cmd.AddCommand(newCellnIssuerServiceCmd())
	return cmd
}
