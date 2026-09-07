package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/sympozium-ai/sympozium/internal/cellnauthority"
	"github.com/sympozium-ai/sympozium/internal/cellnreview"
	"k8s.io/apimachinery/pkg/types"
)

// These are operator review/provisioning commands, not tenant-facing endpoints.
// Source flags are trusted operator configuration, never proof of ownership.
func newCellnSelectionPlanCmd() *cobra.Command {
	return newCellnSelectionCmd("plan")
}

func newCellnSelectionComposeCmd() *cobra.Command {
	return newCellnSelectionCmd("compose")
}

func newCellnSelectionIssueCmd() *cobra.Command {
	return newCellnSelectionCmd("issue")
}

func newCellnSelectionCmd(mode string) *cobra.Command {
	compose, issue := mode == "compose", mode == "issue"
	var sourceNamespace, operatorSource, runtimeSource, agentSource string
	var selected []string
	var imageBytes int64
	var runName string
	var modelPolicy string
	var executionMote, executionClosure string
	var options cellnreview.ComposeOptions
	var issueOptions cellnreview.IssueOptions
	cmd := &cobra.Command{Use: "plan AGENT", Args: cobra.ExactArgs(1), SilenceUsage: true,
		Short: "Resolve live grants and emit a Celln composition plan without executing",
		Long:  "Operator-only planning input. Read three independently configured grant ConfigMaps and live Agent/runtime/tool identities. Prints the resolved authority snapshot and exact compositor input; does not grant authority, certify readiness, write resources, compose images or execute. Tenant-facing callers must not accept these source flags from run requests.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if executionMote != "" || executionClosure != "" {
				if compose || executionMote == "" || executionClosure == "" || modelPolicy == "" || runName == "" {
					return fmt.Errorf("execution candidate requires plan, --run, --model-policy, --execution-mote and --execution-closure")
				}
			}
			if modelPolicy != "" && runName == "" {
				return fmt.Errorf("model policy review requires --run")
			}
			selection := make([]cellnauthority.Selection, 0, len(selected))
			for _, ref := range selected {
				name, revision, ok := strings.Cut(ref, "@")
				if !ok || name == "" || revision == "" || strings.Contains(revision, "@") {
					return fmt.Errorf("tool must be NAME@REVISION")
				}
				selection = append(selection, cellnauthority.Selection{Name: name, Revision: revision})
			}
			loader := cellnauthority.Loader{Reader: k8sClient,
				OperatorSource: types.NamespacedName{Namespace: sourceNamespace, Name: operatorSource},
				RuntimeSource:  types.NamespacedName{Namespace: sourceNamespace, Name: runtimeSource},
				AgentSource:    types.NamespacedName{Namespace: sourceNamespace, Name: agentSource}}
			if runName != "" {
				frozen, err := loader.FreezeRun(cmd.Context(), types.NamespacedName{Namespace: namespace, Name: runName}, selection, imageBytes)
				if err != nil {
					return err
				}
				if frozen.Snapshot.Agent.Name != args[0] {
					return fmt.Errorf("run belongs to a different Agent")
				}
				if err := loader.Revalidate(cmd.Context(), *frozen); err != nil {
					return err
				}
				var modelApproval *cellnauthority.ModelApproval
				modelLoader := cellnauthority.ModelLoader{Selection: loader, Source: types.NamespacedName{Namespace: sourceNamespace, Name: modelPolicy}}
				if modelPolicy != "" {
					modelApproval, err = modelLoader.Resolve(cmd.Context(), *frozen)
					if err != nil {
						return err
					}
				}
				if executionMote != "" {
					artifacts := cellnauthority.ExecutionArtifacts{}
					artifacts.Mote.Hash, artifacts.Closure.Hash = executionMote, executionClosure
					if issue {
						issued, err := cellnreview.Issue(cmd.Context(), modelLoader, *frozen, *modelApproval, artifacts, issueOptions)
						if err != nil {
							return err
						}
						if err := json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"apiVersion": "sympozium.ai/celln-issuance-report-v1", "frozen": frozen, "issued": issued, "executed": false, "artifactReadiness": "not_checked"}); err != nil {
							return errors.Join(err, cellnreview.Withdraw(issueOptions.PolicyRoot, *issued))
						}
						return nil
					}
					candidate, err := modelLoader.BuildExecution(cmd.Context(), *frozen, *modelApproval, artifacts)
					if err != nil {
						return err
					}
					return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"apiVersion": "sympozium.ai/celln-selection-report-v1", "frozen": frozen, "candidate": candidate, "executionAuthorized": false, "artifactReadiness": "not_checked", "conformance": "not_checked"})
				}
				if compose {
					report, err := cellnreview.Compose(cmd.Context(), loader, *frozen, options)
					if err != nil {
						return err
					}
					if modelApproval != nil {
						if err := modelLoader.Revalidate(cmd.Context(), *frozen, *modelApproval); err != nil {
							return err
						}
					}
					return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"apiVersion": "sympozium.ai/celln-composed-selection-v1", "frozen": frozen, "composition": report, "modelApproval": modelApproval, "executionAuthorized": false})
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"apiVersion": "sympozium.ai/celln-selection-report-v1", "frozen": frozen, "modelApproval": modelApproval, "artifactReadiness": "not_checked", "conformance": "not_checked", "executionAuthorized": false})
			}
			snapshot, err := loader.Resolve(cmd.Context(), types.NamespacedName{Namespace: namespace, Name: args[0]}, selection)
			if err != nil {
				return err
			}
			plan, err := cellnauthority.Prepare(*snapshot, imageBytes)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"apiVersion": "sympozium.ai/celln-selection-report-v1", "snapshot": snapshot, "prepared": plan, "artifactReadiness": "not_checked", "conformance": "not_checked", "executionAuthorized": false})
		}}
	cmd.Flags().StringVar(&sourceNamespace, "grant-namespace", "", "Operator-controlled namespace containing grant sources")
	cmd.Flags().StringVar(&operatorSource, "operator-grants", "", "Operator grant ConfigMap name")
	cmd.Flags().StringVar(&runtimeSource, "runtime-grants", "", "Runtime grant ConfigMap name")
	cmd.Flags().StringVar(&agentSource, "agent-grants", "", "Agent grant ConfigMap name")
	cmd.Flags().StringArrayVar(&selected, "tool", nil, "Explicit NAME@REVISION; repeat in desired order; omission selects no tools")
	cmd.Flags().Int64Var(&imageBytes, "image-bytes", 33554432, "Composed image size, 32..512 MiB and 2 MiB aligned")
	cmd.Flags().StringVar(&runName, "run", "", "Bind and revalidate the plan against an existing same-namespace AgentRun (no dispatch)")
	cmd.Flags().StringVar(&modelPolicy, "model-policy", "", "Independent operator model-policy ConfigMap in grant namespace; requires --run")
	cmd.Flags().StringVar(&executionMote, "execution-mote", "", "Actual materialized mote hash for plan/issue (requires run, model policy and closure)")
	cmd.Flags().StringVar(&executionClosure, "execution-closure", "", "Actual composed closure hash for an unissued execution candidate; host verification remains required")
	for _, name := range []string{"grant-namespace", "operator-grants", "runtime-grants", "agent-grants"} {
		_ = cmd.MarkFlagRequired(name)
	}
	if compose {
		cmd.Use = "compose AGENT"
		cmd.Short = "Build a local signed Harness/tool composition from live reviewed grants"
		cmd.Long = "Operator packaging only: verifies exact source publishers, executables and schemas, invokes the trusted Celln compositor, then revalidates the frozen run and approvals. Creates a new output directory; never admits, distributes, prewarms, grants model access or executes a cell. Failed post-build checks may leave diagnostic artifacts."
		cmd.Flags().StringVar(&options.Binary, "celln-binary", "", "Absolute operator-selected Celln binary")
		cmd.Flags().StringVar(&options.PolicyRoot, "policy-root", "", "Absolute trusted Celln policy/store root")
		cmd.Flags().StringVar(&options.KeyFile, "key-file", "", "Absolute operator composer seed path (never read into Kubernetes)")
		cmd.Flags().StringVar(&options.OutputDir, "output-dir", "", "Absolute new output directory; must not exist")
		for _, name := range []string{"run", "celln-binary", "policy-root", "key-file", "output-dir"} {
			_ = cmd.MarkFlagRequired(name)
		}
	}
	if issue {
		cmd.Use = "issue AGENT"
		cmd.Short = "Provision a local request-bound host grant from live independent approvals"
		cmd.Long = "Operator-only local issuance. Verifies exact signed composition sources, resolves an independently configured host credential mapping, performs real sealed member verification, and publishes a request-bound grant. No dispatch or positive readiness. Persist the output and withdraw the profile when approval changes; this is not an autonomous revocation controller."
		cmd.Flags().StringVar(&issueOptions.Binary, "celln-binary", "", "Absolute operator-selected Celln binary")
		cmd.Flags().StringVar(&issueOptions.PolicyRoot, "policy-root", "", "Absolute trusted local host policy/store root")
		cmd.Flags().StringVar(&issueOptions.ComposerPublisher, "composer-publisher", "", "Exact operator-approved composition publisher key")
		cmd.Flags().DurationVar(&issueOptions.ProfileLifetime, "profile-lifetime", 5*time.Minute, "Host admission lifetime (1ms..5m); retries reuse original expiry; 0 is legacy explicit-operator mode only")
		for _, name := range []string{"run", "model-policy", "execution-mote", "execution-closure", "celln-binary", "policy-root", "composer-publisher"} {
			_ = cmd.MarkFlagRequired(name)
		}
	}
	return cmd
}
