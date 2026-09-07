// Package main provides the sympozium CLI tool for managing Sympozium resources.
package main

import (
	"bufio"
	"context"
	cryptoRand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	helmcli "helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/strvals"

	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/agentedit"
	"github.com/sympozium-ai/sympozium/internal/helmchart"
)

var (
	// version is set via -ldflags at build time.
	version = "dev"

	kubeconfig string
	namespace  string
	k8sClient  client.Client
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "sympozium",
		Short: "Sympozium - Kubernetes-native AI agent management",
		Long: `Sympozium CLI for managing Agents, AgentRuns, SympoziumPolicies,
SkillPacks, and feature gates in your Kubernetes cluster.

Running without a subcommand launches the interactive TUI.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip K8s client init for commands that don't need it.
			switch cmd.Name() {
			case "version", "install", "uninstall", "onboard", "tui", "sympozium", "serve":
				return nil
			}
			return initClient()
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := initClient(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not connect to cluster: %v\n", err)
				fmt.Fprintln(os.Stderr, "TUI will start in disconnected mode.")
			}
			m := newTUIModel(namespace)
			p := tea.NewProgram(m, tea.WithAltScreen())
			_, err := p.Run()
			return err
		},
	}

	rootCmd.PersistentFlags().StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig")
	rootCmd.PersistentFlags().StringVarP(&namespace, "namespace", "n", "default", "Kubernetes namespace")

	rootCmd.AddCommand(
		newInstallCmd(),
		newUninstallCmd(),
		newOnboardCmd(),
		newTokenCmd(),
		newAgentsCmd(),
		newRunsCmd(),
		newWorkspaceCmd(),
		newPoliciesCmd(),
		newSkillsCmd(),
		newMCPServersCmd(),
		newFeaturesCmd(),
		newVersionCmd(),
		newTUICmd(),
		newServeCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// uiToken reads the dashboard bearer token from the control-plane Secret.
// Keep this separate from the Cobra command so the Secret contract is easy to
// test without printing a credential.
func uiToken(ctx context.Context, reader client.Reader, systemNamespace string) (string, error) {
	secret := &corev1.Secret{}
	if err := reader.Get(ctx, types.NamespacedName{Name: "sympozium-ui-token", Namespace: systemNamespace}, secret); err != nil {
		if k8serr.IsNotFound(err) {
			return "", fmt.Errorf("UI token Secret %q was not found in namespace %q; is Sympozium installed there?", "sympozium-ui-token", systemNamespace)
		}
		return "", fmt.Errorf("read UI token Secret in namespace %q: %w", systemNamespace, err)
	}

	token := strings.TrimSpace(string(secret.Data["token"]))
	if token == "" {
		return "", fmt.Errorf("UI token Secret %q in namespace %q has no non-empty %q key", "sympozium-ui-token", systemNamespace, "token")
	}
	return token, nil
}

func newTokenCmd() *cobra.Command {
	var systemNamespace string

	cmd := &cobra.Command{
		Use:   "token",
		Short: "Print the web dashboard authentication token",
		Long: `Print the Sympozium web dashboard bearer token to standard output.

Use this after a manual port-forward, for example:
  sympozium token | pbcopy

The command requires permission to read the sympozium-ui-token Secret. Treat
the output as a credential and do not paste it into logs or shell history.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			token, err := uiToken(cmd.Context(), k8sClient, systemNamespace)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), token)
			return err
		},
	}

	cmd.Flags().StringVar(&systemNamespace, "service-namespace", "sympozium-system", "Namespace containing the sympozium-ui-token Secret")
	return cmd
}

func initClient() error {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	if err := sympoziumv1alpha1.AddToScheme(scheme); err != nil {
		return fmt.Errorf("failed to register scheme: %w", err)
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}

	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules, &clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return fmt.Errorf("failed to load kubeconfig: %w", err)
	}

	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	k8sClient = c
	return nil
}

func newAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "agents",
		Aliases: []string{"agent", "inst", "instances"},
		Short:   "Manage Agents",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List Agents",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var list sympoziumv1alpha1.AgentList
				if err := k8sClient.List(ctx, &list, client.InNamespace(namespace)); err != nil {
					return err
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tPHASE\tCHANNELS\tAGENT PODS\tAGE")
				for _, inst := range list.Items {
					age := time.Since(inst.CreationTimestamp.Time).Round(time.Second)
					channels := make([]string, 0)
					for _, ch := range inst.Status.Channels {
						channels = append(channels, ch.Type)
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n",
						inst.Name, inst.Status.Phase,
						strings.Join(channels, ","),
						inst.Status.ActiveAgentPods, age)
				}
				return w.Flush()
			},
		},
		&cobra.Command{
			Use:   "get [name]",
			Short: "Get a Agent",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var inst sympoziumv1alpha1.Agent
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: args[0], Namespace: namespace}, &inst); err != nil {
					return err
				}
				data, _ := json.MarshalIndent(inst, "", "  ")
				fmt.Println(string(data))
				return nil
			},
		},
		&cobra.Command{
			Use:   "delete [name]",
			Short: "Delete a Agent",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				inst := &sympoziumv1alpha1.Agent{
					ObjectMeta: metav1.ObjectMeta{Name: args[0], Namespace: namespace},
				}
				if err := k8sClient.Delete(ctx, inst); err != nil {
					return err
				}
				fmt.Printf("agent/%s deleted\n", args[0])
				return nil
			},
		},
	)
	return cmd
}

func newRunsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "runs",
		Aliases: []string{"run"},
		Short:   "Manage AgentRuns",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List AgentRuns",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var list sympoziumv1alpha1.AgentRunList
				if err := k8sClient.List(ctx, &list, client.InNamespace(namespace)); err != nil {
					return err
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tAGENT\tPHASE\tPOD\tTOKENS\tAGE")
				for _, run := range list.Items {
					age := time.Since(run.CreationTimestamp.Time).Round(time.Second)
					tokens := "-"
					if run.Status.TokenUsage != nil {
						tokens = fmt.Sprintf("%d/%d", run.Status.TokenUsage.InputTokens, run.Status.TokenUsage.OutputTokens)
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
						run.Name, run.Spec.AgentRef,
						run.Status.Phase, run.Status.PodName, tokens, age)
				}
				return w.Flush()
			},
		},
		&cobra.Command{
			Use:   "get [name]",
			Short: "Get an AgentRun",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var run sympoziumv1alpha1.AgentRun
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: args[0], Namespace: namespace}, &run); err != nil {
					return err
				}
				data, _ := json.MarshalIndent(run, "", "  ")
				fmt.Println(string(data))
				return nil
			},
		},
		&cobra.Command{
			Use:   "logs [name]",
			Short: "Stream logs from an AgentRun pod",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var run sympoziumv1alpha1.AgentRun
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: args[0], Namespace: namespace}, &run); err != nil {
					return err
				}
				if run.Status.PodName == "" {
					return fmt.Errorf("agentrun %s has no pod yet (phase: %s)", args[0], run.Status.Phase)
				}
				fmt.Printf("Use: kubectl logs %s -c agent -f\n", run.Status.PodName)
				return nil
			},
		},
	)
	return cmd
}

func newPoliciesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "policies",
		Aliases: []string{"policy", "pol"},
		Short:   "Manage SympoziumPolicies",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List SympoziumPolicies",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var list sympoziumv1alpha1.SympoziumPolicyList
				if err := k8sClient.List(ctx, &list, client.InNamespace(namespace)); err != nil {
					return err
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tBOUND AGENTS\tAGE")
				for _, pol := range list.Items {
					age := time.Since(pol.CreationTimestamp.Time).Round(time.Second)
					fmt.Fprintf(w, "%s\t%d\t%s\n", pol.Name, pol.Status.BoundInstances, age)
				}
				return w.Flush()
			},
		},
		&cobra.Command{
			Use:   "get [name]",
			Short: "Get a SympoziumPolicy",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var pol sympoziumv1alpha1.SympoziumPolicy
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: args[0], Namespace: namespace}, &pol); err != nil {
					return err
				}
				data, _ := json.MarshalIndent(pol, "", "  ")
				fmt.Println(string(data))
				return nil
			},
		},
	)
	return cmd
}

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "skills",
		Aliases: []string{"skill", "sk"},
		Short:   "Manage SkillPacks",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List SkillPacks",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var list sympoziumv1alpha1.SkillPackList
				if err := k8sClient.List(ctx, &list, client.InNamespace(namespace)); err != nil {
					return err
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tSKILLS\tCONFIGMAP\tAGE")
				for _, sk := range list.Items {
					age := time.Since(sk.CreationTimestamp.Time).Round(time.Second)
					fmt.Fprintf(w, "%s\t%d\t%s\t%s\n",
						sk.Name, len(sk.Spec.Skills), sk.Status.ConfigMapName, age)
				}
				return w.Flush()
			},
		},
	)
	return cmd
}

func newMCPServersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mcp-servers",
		Aliases: []string{"mcp-server", "mcp"},
		Short:   "Manage MCP servers",
	}

	createCmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create an MCP server",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			transport, _ := cmd.Flags().GetString("transport")
			prefix, _ := cmd.Flags().GetString("prefix")
			image, _ := cmd.Flags().GetString("image")
			mcpURL, _ := cmd.Flags().GetString("url")
			timeout, _ := cmd.Flags().GetInt("timeout")

			if prefix == "" {
				return fmt.Errorf("--prefix is required")
			}
			if transport == "" {
				transport = "http"
			}

			mcp := &sympoziumv1alpha1.MCPServer{
				ObjectMeta: metav1.ObjectMeta{Name: args[0], Namespace: namespace},
				Spec: sympoziumv1alpha1.MCPServerSpec{
					TransportType: transport,
					ToolsPrefix:   prefix,
					URL:           mcpURL,
					Timeout:       timeout,
				},
			}
			if image != "" {
				mcp.Spec.Deployment = &sympoziumv1alpha1.MCPServerDeployment{
					Image: image,
				}
			}
			if err := k8sClient.Create(ctx, mcp); err != nil {
				return err
			}
			fmt.Printf("mcpserver/%s created\n", args[0])
			return nil
		},
	}
	createCmd.Flags().String("transport", "http", "Transport type (http or stdio)")
	createCmd.Flags().String("prefix", "", "Tools prefix (required)")
	createCmd.Flags().String("image", "", "Container image for managed deployment")
	createCmd.Flags().String("url", "", "URL for external MCP server")
	createCmd.Flags().Int("timeout", 30, "Per-request timeout in seconds")

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List MCP servers",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var list sympoziumv1alpha1.MCPServerList
				if err := k8sClient.List(ctx, &list, client.InNamespace(namespace)); err != nil {
					return err
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tTRANSPORT\tREADY\tURL\tTOOLS\tAGE")
				for _, mcp := range list.Items {
					age := time.Since(mcp.CreationTimestamp.Time).Round(time.Second)
					url := mcp.Status.URL
					if url == "" {
						url = mcp.Spec.URL
					}
					fmt.Fprintf(w, "%s\t%s\t%v\t%s\t%d\t%s\n",
						mcp.Name, mcp.Spec.TransportType, mcp.Status.Ready,
						url, mcp.Status.ToolCount, age)
				}
				return w.Flush()
			},
		},
		&cobra.Command{
			Use:   "get [name]",
			Short: "Get an MCP server",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var mcp sympoziumv1alpha1.MCPServer
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: args[0], Namespace: namespace}, &mcp); err != nil {
					return err
				}
				data, _ := json.MarshalIndent(mcp, "", "  ")
				fmt.Println(string(data))
				return nil
			},
		},
		createCmd,
		&cobra.Command{
			Use:   "delete [name]",
			Short: "Delete an MCP server",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				mcp := &sympoziumv1alpha1.MCPServer{
					ObjectMeta: metav1.ObjectMeta{Name: args[0], Namespace: namespace},
				}
				if err := k8sClient.Delete(ctx, mcp); err != nil {
					return err
				}
				fmt.Printf("mcpserver/%s deleted\n", args[0])
				return nil
			},
		},
	)
	return cmd
}

func newFeaturesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "features",
		Aliases: []string{"feature", "feat"},
		Short:   "Manage feature gates",
	}

	enableCmd := &cobra.Command{
		Use:   "enable [feature]",
		Short: "Enable a feature gate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return toggleFeature(args[0], true, cmd)
		},
	}
	enableCmd.Flags().String("policy", "", "Target SympoziumPolicy")

	disableCmd := &cobra.Command{
		Use:   "disable [feature]",
		Short: "Disable a feature gate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return toggleFeature(args[0], false, cmd)
		},
	}
	disableCmd.Flags().String("policy", "", "Target SympoziumPolicy")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List feature gates on a policy",
		RunE: func(cmd *cobra.Command, args []string) error {
			policyName, _ := cmd.Flags().GetString("policy")
			if policyName == "" {
				return fmt.Errorf("--policy is required")
			}
			ctx := context.Background()
			var pol sympoziumv1alpha1.SympoziumPolicy
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: policyName, Namespace: namespace}, &pol); err != nil {
				return err
			}
			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "FEATURE\tENABLED")
			if pol.Spec.FeatureGates != nil {
				for feature, enabled := range pol.Spec.FeatureGates {
					fmt.Fprintf(w, "%s\t%v\n", feature, enabled)
				}
			}
			return w.Flush()
		},
	}
	listCmd.Flags().String("policy", "", "Target SympoziumPolicy")

	cmd.AddCommand(enableCmd, disableCmd, listCmd)
	return cmd
}

func toggleFeature(feature string, enabled bool, cmd *cobra.Command) error {
	policyName, _ := cmd.Flags().GetString("policy")
	if policyName == "" {
		return fmt.Errorf("--policy is required")
	}

	ctx := context.Background()
	var pol sympoziumv1alpha1.SympoziumPolicy
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: policyName, Namespace: namespace}, &pol); err != nil {
		return err
	}

	if pol.Spec.FeatureGates == nil {
		pol.Spec.FeatureGates = make(map[string]bool)
	}
	pol.Spec.FeatureGates[feature] = enabled

	if err := k8sClient.Update(ctx, &pol); err != nil {
		return err
	}

	action := "enabled"
	if !enabled {
		action = "disabled"
	}
	fmt.Printf("Feature %q %s on policy %s\n", feature, action, policyName)
	return nil
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Printf("sympozium %s\n", version)
		},
	}
}

const (
	ghRepo            = "sympozium-ai/sympozium"
	gatewayAPICRDsURL = "https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml"
)

// ── Onboard ──────────────────────────────────────────────────────────────────

func newOnboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "onboard",
		Short: "Interactive setup wizard for Sympozium",
		Long: `Walks you through creating your first Agent, connecting a
channel (Telegram, Slack, Discord, or WhatsApp), setting up your AI provider
credentials, and optionally applying a default SympoziumPolicy.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOnboard()
		},
	}
}

func runOnboard() error {
	reader := bufio.NewReader(os.Stdin)

	printBanner()

	// ── Step 1: Check Sympozium is installed ───────────────────────────────
	fmt.Println("\n📋 Step 1/9 — Checking cluster...")
	if err := initClient(); err != nil {
		fmt.Println("\n  ❌ Cannot connect to your cluster.")
		fmt.Println("  Make sure kubectl is configured and run: sympozium install")
		return err
	}

	// Quick health check: can we list CRDs?
	ctx := context.Background()
	var instances sympoziumv1alpha1.AgentList
	if err := k8sClient.List(ctx, &instances, client.InNamespace(namespace)); err != nil {
		fmt.Println("\n  ❌ Sympozium CRDs not found. Run 'sympozium install' first.")
		return fmt.Errorf("CRDs not installed: %w", err)
	}
	fmt.Println("  ✅ Sympozium is installed and CRDs are available.")

	// ── Step 2: Namespace ────────────────────────────────────────────────
	fmt.Println("\n📋 Step 2/9 — Target Namespace")
	fmt.Println("  Which namespace should resources be created in?")
	targetNS := prompt(reader, "  Namespace", namespace)
	if targetNS != "" {
		namespace = targetNS
	}

	// ── Step 3: Agent name ──────────────────────────────────────────────
	fmt.Println("\n📋 Step 3/9 — Create your Agent")
	fmt.Println("  An agent represents you (or a tenant) in the system.")
	instanceName := prompt(reader, "  Agent name", "my-agent")

	// ── Step 3: AI provider ──────────────────────────────────────────────
	fmt.Println("\n📋 Step 4/9 — AI Provider")
	fmt.Println("  Which model provider do you want to use?")
	fmt.Println("    1) OpenAI")
	fmt.Println("    2) Anthropic")
	fmt.Println("    3) Azure OpenAI")
	fmt.Println("    4) Ollama          (local, no API key needed)")
	fmt.Println("    5) LM Studio       (local, optional API key)")
	fmt.Println("    6) llama-server    (local, no API key needed)")
	fmt.Println("    7) AWS Bedrock     (Claude, Nova, etc.)")
	fmt.Println("    8) Other / OpenAI-compatible")
	providerChoice := prompt(reader, "  Choice [1-8]", "1")

	var providerName, secretEnvKey, modelName, baseURL string
	switch providerChoice {
	case "2":
		providerName = "anthropic"
		secretEnvKey = "ANTHROPIC_API_KEY"
		modelName = prompt(reader, "  Model name", "claude-sonnet-4-20250514")
	case "3":
		providerName = "azure-openai"
		secretEnvKey = "AZURE_OPENAI_API_KEY"
		baseURL = prompt(reader, "  Azure OpenAI endpoint URL", "")
		modelName = prompt(reader, "  Deployment name", "gpt-4o")
	case "4":
		providerName = "ollama"
		secretEnvKey = ""
		baseURL = prompt(reader, "  Ollama URL", "http://ollama.default.svc:11434/v1")
		modelName = prompt(reader, "  Model name", "llama3")
		fmt.Println("  💡 No API key needed for Ollama.")
	case "5":
		providerName = "lm-studio"
		secretEnvKey = ""
		baseURL = prompt(reader, "  LM Studio URL", "http://localhost:1234/v1")
		modelName = prompt(reader, "  Model name", "")
		fmt.Println("  💡 No API key needed for LM Studio.")
	case "6":
		providerName = "llama-server"
		secretEnvKey = ""
		baseURL = prompt(reader, "  llama-server URL", "http://localhost:8080/v1")
		modelName = prompt(reader, "  Model name", "")
		fmt.Println("  💡 No API key needed for llama-server.")
	case "7":
		providerName = "bedrock"
		secretEnvKey = "" // Bedrock uses multiple AWS credential keys, handled below.
		awsRegion := prompt(reader, "  AWS Region", "us-east-1")
		awsAccessKeyID := promptSecret(reader, "  AWS Access Key ID (Enter to skip for IRSA)")
		var awsSecretAccessKey, awsSessionToken string
		if awsAccessKeyID != "" {
			awsSecretAccessKey = promptSecret(reader, "  AWS Secret Access Key")
			awsSessionToken = promptSecret(reader, "  AWS Session Token (optional, Enter to skip)")
		}
		modelName = prompt(reader, "  Model ID", "anthropic.claude-sonnet-4-20250514-v1:0")
		// Build the secret data map for Bedrock.
		bedrockSecretData := map[string]string{"AWS_REGION": awsRegion}
		if awsAccessKeyID != "" {
			bedrockSecretData["AWS_ACCESS_KEY_ID"] = awsAccessKeyID
			bedrockSecretData["AWS_SECRET_ACCESS_KEY"] = awsSecretAccessKey
			if awsSessionToken != "" {
				bedrockSecretData["AWS_SESSION_TOKEN"] = awsSessionToken
			}
		}
		// Create the Bedrock secret immediately (uses multiple keys, not
		// the single secretEnvKey/apiKey pattern used by other providers).
		bedrockSecretName := fmt.Sprintf("%s-%s-key", instanceName, providerName)
		fmt.Printf("  Creating secret %s...\n", bedrockSecretName)
		_ = kubectl("delete", "secret", bedrockSecretName, "-n", namespace, "--ignore-not-found")
		args := []string{"create", "secret", "generic", bedrockSecretName, "-n", namespace}
		for k, v := range bedrockSecretData {
			args = append(args, fmt.Sprintf("--from-literal=%s=%s", k, v))
		}
		if err := kubectl(args...); err != nil {
			return fmt.Errorf("create Bedrock provider secret: %w", err)
		}
	case "8":
		providerName = prompt(reader, "  Provider name", "custom")
		secretEnvKey = prompt(reader, "  API key env var name (empty if none)", "API_KEY")
		baseURL = prompt(reader, "  API base URL", "")
		modelName = prompt(reader, "  Model name", "")
	default:
		providerName = "openai"
		secretEnvKey = "OPENAI_API_KEY"
		modelName = prompt(reader, "  Model name", "gpt-4o")
	}

	var apiKey string
	if secretEnvKey != "" {
		apiKey = promptSecret(reader, fmt.Sprintf("  %s", secretEnvKey))
		if apiKey == "" {
			// Fall back to environment variable.
			apiKey = os.Getenv(secretEnvKey)
			if apiKey != "" {
				fmt.Printf("  ✓ Using %s from environment\n", secretEnvKey)
			}
		}
		if apiKey == "" {
			fmt.Println("  ⚠  No API key provided — you can add it later:")
			fmt.Printf("  kubectl create secret generic %s-%s-key --from-literal=%s=<key>\n",
				instanceName, providerName, secretEnvKey)
		}
	}

	providerSecretName := fmt.Sprintf("%s-%s-key", instanceName, providerName)

	// ── Step 4: GitHub Repository ────────────────────────────────────────
	fmt.Println("\n📋 Step 5/9 — GitHub Repository (optional)")
	fmt.Println("  Point your agent at a GitHub repository to enable")
	fmt.Println("  issue triage, PR reviews, and code contributions.")
	githubRepo := prompt(reader, "  GitHub repo owner/repo (Enter to skip)", "")

	// ── Step 5: Team Instructions ────────────────────────────────────────
	fmt.Println("\n📋 Step 6/9 — Instructions (optional)")
	fmt.Println("  Give your agent an objective or task to work on.")
	fmt.Println("  This will be included in every heartbeat run.")
	teamTask := prompt(reader, "  What should the agent work on? (Enter to skip)", "")

	// ── Step 6: Channel ──────────────────────────────────────────────────
	fmt.Println("\n📋 Step 7/9 — Connect a Channel (optional)")
	fmt.Println("  Channels let your agent receive messages from external platforms.")
	fmt.Println("    1) Telegram  — easiest, just talk to @BotFather")
	fmt.Println("    2) Slack")
	fmt.Println("    3) Discord")
	fmt.Println("    4) WhatsApp")
	fmt.Println("    5) Skip — I'll add a channel later")
	channelChoice := prompt(reader, "  Choice [1-5]", "5")

	var channelType, channelTokenKey, channelToken, slackAppToken string
	switch channelChoice {
	case "1":
		channelType = "telegram"
		channelTokenKey = "TELEGRAM_BOT_TOKEN"
		fmt.Println("\n  💡 Get a bot token from https://t.me/BotFather")
		channelToken = promptSecret(reader, "  Bot Token")
	case "2":
		channelType = "slack"
		channelTokenKey = "SLACK_BOT_TOKEN"
		fmt.Println("\n  💡 Create a Slack app at https://api.slack.com/apps")
		fmt.Println("  💡 Socket Mode requires BOTH SLACK_BOT_TOKEN (xoxb-...) and SLACK_APP_TOKEN (xapp-...)")
		channelToken = promptSecret(reader, "  Bot OAuth Token")
		slackAppToken = promptSecret(reader, "  App-Level Token (xapp-..., optional for Events API mode)")
	case "3":
		channelType = "discord"
		channelTokenKey = "DISCORD_BOT_TOKEN"
		fmt.Println("\n  💡 Create a Discord app at https://discord.com/developers/applications")
		channelToken = promptSecret(reader, "  Bot Token")
	case "4":
		channelType = "whatsapp"
		channelTokenKey = "" // WhatsApp uses QR pairing, no token needed
		fmt.Println("\n  📱 WhatsApp uses QR code pairing — no API token needed!")
		fmt.Println("  After setup, a QR code will appear. Scan it with your phone:")
		fmt.Println("  WhatsApp → Settings → Linked Devices → Link a Device")
	default:
		channelType = ""
	}

	channelSecretName := fmt.Sprintf("%s-%s-secret", instanceName, channelType)

	// ── Step 5: Apply default policy? ────────────────────────────────────
	fmt.Println("\n📋 Step 8/9 — Default Policy")
	fmt.Println("  A SympoziumPolicy controls what tools agents can use, sandboxing, etc.")
	applyPolicy := promptYN(reader, "  Apply the default policy?", true)

	// ── Step 6: Heartbeat interval ──────────────────────────────────────
	fmt.Println("\n📋 Step 9/9 — Heartbeat Schedule")
	fmt.Println("  A heartbeat lets your agent wake up periodically to review memory")
	fmt.Println("  and note anything that needs attention.")
	fmt.Println("    1) Every 30 minutes")
	fmt.Println("    2) Every hour          (recommended)")
	fmt.Println("    3) Every 6 hours")
	fmt.Println("    4) Once a day (9am)")
	fmt.Println("    5) Disabled — no heartbeat")
	hbChoice := prompt(reader, "  Choice [1-5]", "2")
	var heartbeatCron, heartbeatLabel string
	switch hbChoice {
	case "1":
		heartbeatCron = "*/30 * * * *"
		heartbeatLabel = "every 30 minutes"
	case "3":
		heartbeatCron = "0 */6 * * *"
		heartbeatLabel = "every 6 hours"
	case "4":
		heartbeatCron = "0 9 * * *"
		heartbeatLabel = "once a day (9am)"
	case "5":
		heartbeatCron = ""
		heartbeatLabel = "disabled"
	default:
		heartbeatCron = "0 * * * *"
		heartbeatLabel = "every hour"
	}

	// ── Summary ──────────────────────────────────────────────────────────
	fmt.Println("\n━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Println("  Summary")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("  Agent:      %s  (namespace: %s)\n", instanceName, namespace)
	fmt.Printf("  Provider:   %s  (model: %s)\n", providerName, modelName)
	if baseURL != "" {
		fmt.Printf("  Base URL:   %s\n", baseURL)
	}
	if githubRepo != "" {
		fmt.Printf("  GitHub:     %s\n", githubRepo)
	}
	if teamTask != "" {
		display := teamTask
		if len(display) > 50 {
			display = display[:47] + "..."
		}
		fmt.Printf("  Task:       %s\n", display)
	}
	if channelType != "" {
		fmt.Printf("  Channel:    %s\n", channelType)
	} else {
		fmt.Println("  Channel:    (none)")
	}
	fmt.Printf("  Policy:     %v\n", applyPolicy)
	fmt.Printf("  Heartbeat:  %s\n", heartbeatLabel)
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

	if !promptYN(reader, "\n  Proceed?", true) {
		fmt.Println("  Aborted.")
		return nil
	}

	// ── Apply resources ──────────────────────────────────────────────────
	fmt.Println()

	// 1. Create AI provider secret.
	if apiKey != "" {
		fmt.Printf("  Creating secret %s...\n", providerSecretName)
		// Delete first to allow re-runs.
		_ = kubectl("delete", "secret", providerSecretName, "-n", namespace, "--ignore-not-found")
		if err := kubectl("create", "secret", "generic", providerSecretName,
			"-n", namespace,
			fmt.Sprintf("--from-literal=%s=%s", secretEnvKey, apiKey)); err != nil {
			return fmt.Errorf("create provider secret: %w", err)
		}
	}

	// 2. Create channel secret.
	if channelType != "" && channelToken != "" {
		fmt.Printf("  Creating secret %s...\n", channelSecretName)
		_ = kubectl("delete", "secret", channelSecretName, "-n", namespace, "--ignore-not-found")
		args := []string{
			"create", "secret", "generic", channelSecretName,
			"-n", namespace,
			fmt.Sprintf("--from-literal=%s=%s", channelTokenKey, channelToken),
		}
		if channelType == "slack" && slackAppToken != "" {
			args = append(args, fmt.Sprintf("--from-literal=SLACK_APP_TOKEN=%s", slackAppToken))
		} else if channelType == "slack" {
			fmt.Println("  ⚠  SLACK_APP_TOKEN not provided — Slack will run in Events API fallback mode (requires public URL).")
		}
		if err := kubectl(args...); err != nil {
			return fmt.Errorf("create channel secret: %w", err)
		}
	}

	// 3. Apply default policy.
	policyName := "default-policy"
	if applyPolicy {
		fmt.Println("  Applying default SympoziumPolicy...")
		policyYAML := buildDefaultPolicyYAML(policyName, namespace)
		if err := kubectlApplyStdin(policyYAML); err != nil {
			return fmt.Errorf("apply policy: %w", err)
		}
	}

	// 4. Create Agent.
	fmt.Printf("  Creating Agent %s...\n", instanceName)
	// Only pass the secret name if an API key was provided.
	instanceSecret := providerSecretName
	if apiKey == "" {
		instanceSecret = ""
	}
	// WhatsApp doesn't need a channel secret (QR pairing)
	chSecret := channelSecretName
	if channelType == "whatsapp" {
		chSecret = ""
	}
	instanceYAML := buildAgentYAML(instanceName, namespace, modelName, baseURL,
		providerName, instanceSecret, channelType, chSecret,
		policyName, applyPolicy, githubRepo)
	if err := kubectlApplyStdin(instanceYAML); err != nil {
		return fmt.Errorf("apply instance: %w", err)
	}

	// 5. Create heartbeat schedule (unless disabled).
	if heartbeatCron != "" {
		heartbeatName := fmt.Sprintf("%s-heartbeat", instanceName)
		fmt.Printf("  Creating heartbeat schedule %s (%s)...\n", heartbeatName, heartbeatLabel)
		hbTask := "Review your memory. Summarise what you know so far and note anything that needs attention."
		if teamTask != "" {
			hbTask = fmt.Sprintf("OBJECTIVE: %s\n\n%s", teamTask, hbTask)
		}
		heartbeatYAML := fmt.Sprintf(`apiVersion: sympozium.ai/v1alpha1
kind: SympoziumSchedule
metadata:
  name: %s
  namespace: %s
spec:
  instanceRef: %s
  schedule: "%s"
  task: %q
  type: heartbeat
  concurrencyPolicy: Forbid
  includeMemory: true
`, heartbeatName, namespace, instanceName, heartbeatCron, hbTask)
		if err := kubectlApplyStdin(heartbeatYAML); err != nil {
			return fmt.Errorf("apply heartbeat schedule: %w", err)
		}
	}

	// ── WhatsApp QR pairing ──────────────────────────────────────────────
	if channelType == "whatsapp" {
		fmt.Println("\n  📱 Waiting for WhatsApp channel pod to start...")
		fmt.Println("  (this may take a moment on first deploy)")
		fmt.Println()

		if err := streamWhatsAppQR(namespace, instanceName); err != nil {
			fmt.Printf("\n  ⚠  Could not stream QR automatically: %s\n", err)
			fmt.Printf("  You can scan later: kubectl logs -l sympozium.ai/channel=whatsapp,sympozium.ai/instance=%s -n %s\n",
				instanceName, namespace)
		}
	}

	// ── Done ─────────────────────────────────────────────────────────────
	fmt.Println("\n  ✅ Onboarding complete!")
	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Println("  ─────────────────────────────────────────────────")
	fmt.Printf("  • Check your agent:      sympozium agents get %s\n", instanceName)
	if channelType == "telegram" {
		fmt.Println("  • Send a message to your Telegram bot — it's live!")
	}
	if channelType == "whatsapp" {
		fmt.Println("  • Send a WhatsApp message to your linked number — it's live!")
	}
	fmt.Printf("  • Run an agent:          kubectl apply -f config/samples/agentrun_sample.yaml\n")
	fmt.Printf("  • View runs:             sympozium runs list\n")
	fmt.Printf("  • Feature gates:         sympozium features list --policy %s\n", policyName)
	fmt.Println()
	return nil
}

// streamWhatsAppQR polls the WhatsApp channel pod until a QR code appears,
// prints it to stdout, and waits for the device to be linked.
func streamWhatsAppQR(ns, instanceName string) error {
	selector := fmt.Sprintf("sympozium.ai/instance=%s,sympozium.ai/channel=whatsapp,sympozium.ai/component=channel", instanceName)
	timeout := 3 * time.Minute
	deadline := time.Now().Add(timeout)

	// Phase 1: Wait for pod to be Running
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-l", selector, "-n", ns,
			"-o", "jsonpath={.items[0].status.phase}")
		out, err := cmd.CombinedOutput()
		cancel()

		phase := strings.TrimSpace(string(out))
		if err == nil && phase == "Running" {
			fmt.Println("  ✓ Pod is running")
			break
		}
		if phase != "" {
			fmt.Printf("\r  ⏳ Pod status: %s...", phase)
		}
		time.Sleep(3 * time.Second)
	}

	// Phase 2: Stream logs looking for QR code
	lastQR := ""
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "kubectl", "logs", "-l", selector, "-n", ns, "--tail=80")
		out, err := cmd.CombinedOutput()
		cancel()

		if err != nil {
			time.Sleep(3 * time.Second)
			continue
		}

		logStr := string(out)

		// Check if already linked
		if strings.Contains(logStr, "linked successfully") || strings.Contains(logStr, "connected with existing session") {
			fmt.Println("\n  ✅ WhatsApp device linked successfully!")
			return nil
		}

		// Extract QR code block
		lines := strings.Split(logStr, "\n")
		var qrBlock []string
		inQR := false
		for _, line := range lines {
			if strings.Contains(line, "Scan this QR code") {
				inQR = true
				qrBlock = append(qrBlock, line)
				continue
			}
			if inQR {
				qrBlock = append(qrBlock, line)
				if strings.TrimSpace(line) == "" && len(qrBlock) > 5 {
					break
				}
			}
		}

		if len(qrBlock) > 0 {
			qrStr := strings.Join(qrBlock, "\n")
			if qrStr != lastQR {
				lastQR = qrStr
				fmt.Println()
				for _, l := range qrBlock {
					fmt.Println("  " + l)
				}
				fmt.Println("\n  Open WhatsApp → Settings → Linked Devices → Link a Device")
				fmt.Println("  Waiting for you to scan...")
			}
		}

		time.Sleep(3 * time.Second)
	}

	return fmt.Errorf("timed out after %s waiting for WhatsApp pairing", timeout)
}

func printBanner() {
	fmt.Println()
	fmt.Println("  ╔═══════════════════════════════════════════╗")
	fmt.Println("  ║         Sympozium · Onboarding Wizard       ║")
	fmt.Println("  ╚═══════════════════════════════════════════╝")
}

// prompt shows a prompt with an optional default and returns the user's input.
func prompt(reader *bufio.Reader, label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

// promptSecret reads input without showing a default.
func promptSecret(reader *bufio.Reader, label string) string {
	fmt.Printf("%s: ", label)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

// promptYN asks a yes/no question.
func promptYN(reader *bufio.Reader, label string, defaultYes bool) bool {
	hint := "Y/n"
	if !defaultYes {
		hint = "y/N"
	}
	fmt.Printf("%s [%s]: ", label, hint)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	if line == "" {
		return defaultYes
	}
	return line == "y" || line == "yes"
}

func buildDefaultPolicyYAML(name, ns string) string {
	return fmt.Sprintf(`apiVersion: sympozium.ai/v1alpha1
kind: SympoziumPolicy
metadata:
  name: %s
  namespace: %s
spec:
  toolGating:
    defaultAction: allow
    rules:
      - tool: exec_command
        action: ask
      - tool: write_file
        action: allow
      - tool: network_request
        action: deny
  subagentPolicy:
    maxDepth: 3
    maxConcurrent: 5
  sandboxPolicy:
    required: false
    defaultImage: ghcr.io/sympozium-ai/sympozium/sandbox:latest
    maxCPU: "4"
    maxMemory: 8Gi
    agentSandboxPolicy:
      required: false
      defaultRuntimeClass: gvisor
      allowedRuntimeClasses: [gvisor, kata]
  featureGates:
    browser-automation: false
    code-execution: true
    file-access: true
`, name, ns)
}

func buildAgentYAML(name, ns, model, baseURL, provider, providerSecret,
	channelType, channelSecret, policyName string, hasPolicy bool, githubRepo string) string {

	var channelsBlock string
	if channelType != "" {
		if channelSecret != "" {
			channelsBlock = fmt.Sprintf(`  channels:
    - type: %s
      configRef:
        secret: %s
`, channelType, channelSecret)
		} else {
			// WhatsApp and other QR-paired channels don't need a secret
			channelsBlock = fmt.Sprintf(`  channels:
    - type: %s
`, channelType)
		}
	}

	var policyBlock string
	if hasPolicy {
		policyBlock = fmt.Sprintf("  policyRef: %s\n", policyName)
	}

	var baseURLLine string
	if baseURL != "" {
		baseURLLine = fmt.Sprintf("      baseURL: %s\n", baseURL)
	}

	var authRefsBlock string
	if providerSecret != "" {
		authRefsBlock = fmt.Sprintf(`  authRefs:
    - provider: %s
      secret: %s
`, provider, providerSecret)
	}

	var githubSkillBlock string
	if githubRepo != "" {
		githubSkillBlock = fmt.Sprintf(`    - skillPackRef: github-gitops
      params:
        repo: %s
`, githubRepo)
	}

	return fmt.Sprintf(`apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: %s
  namespace: %s
spec:
%s  agents:
    default:
      model: %s
%s%s%s  skills:
    - skillPackRef: k8s-ops
    - skillPackRef: llmfit
    - skillPackRef: memory
%s  memory:
    enabled: true
    maxSizeKB: 256
`, name, ns, channelsBlock, model, baseURLLine, authRefsBlock, policyBlock, githubSkillBlock)
}

func kubectlApplyStdin(yaml string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func newInstallCmd() *cobra.Command {
	var imageTag string
	var setValues []string
	var enableHermeticWorkloads bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install Sympozium into the current Kubernetes cluster",
		Long: `Installs Sympozium using the embedded Helm chart. This sets up CRDs,
the controller manager, API server, admission webhook, RBAC rules,
network policies, and default SkillPacks/Policies/Ensembles.

Use --image-tag to override the container image tag, for example when
you have sideloaded images into Kind with a custom tag.

Use --set to override arbitrary Helm values (e.g. --set controller.replicas=2).

Use --enable-hermetic-workloads to opt into the Celln backend (spec.backend:
"celln" on AgentRuns): hardware-isolated, single-task execution in a sealed
KVM cell instead of a Kubernetes Job. This is off by default because it
deploys a DaemonSet that installs a host-level dispatcher on KVM-capable
nodes, running privileged with hostPID and a read-write mount of the host
root filesystem — necessary to set up KVM on the host, but a materially
different trust boundary than the rest of Sympozium's pods. See
docs/concepts/celln-backend.md before enabling it.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if enableHermeticWorkloads {
				setValues = append(setValues, "celln.enabled=true")
			}
			return runInstall(imageTag, setValues)
		},
	}
	cmd.Flags().StringVar(&imageTag, "image-tag", "", "Override image tag (e.g. 'latest')")
	cmd.Flags().StringArrayVar(&setValues, "set", nil, "Set Helm values (key=value, can be repeated)")
	cmd.Flags().BoolVar(&enableHermeticWorkloads, "enable-hermetic-workloads", false,
		"Opt into the Celln backend: hardware-isolated execution via a privileged host-installer DaemonSet on KVM-capable nodes")
	return cmd
}

func newUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Sympozium from the current Kubernetes cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUninstall()
		},
	}
}

const (
	helmReleaseName = "sympozium"
	helmNamespace   = "sympozium-system"
)

// newHelmConfig creates a Helm action.Configuration bound to the given namespace.
func newHelmConfig(ns string) (*action.Configuration, error) {
	cfg := new(action.Configuration)
	// Use the same kubeconfig resolution as the rest of the CLI.
	kubeconfigPath := kubeconfig
	if kubeconfigPath == "" {
		kubeconfigPath = clientcmd.RecommendedHomeFile
	}
	settings := helmcli.New()
	settings.KubeConfig = kubeconfigPath
	settings.SetNamespace(ns)
	if err := cfg.Init(settings.RESTClientGetter(), ns, "secret", func(format string, v ...interface{}) {
		// Silence Helm's debug logging.
	}); err != nil {
		return nil, fmt.Errorf("initializing Helm config: %w", err)
	}
	return cfg, nil
}

// buildHelmValues constructs a Helm values map from CLI flags.
func buildHelmValues(imageTag string, setValues []string) (map[string]interface{}, error) {
	vals := make(map[string]interface{})
	// The CLI manages namespace creation itself via Helm's install.CreateNamespace
	// flag, so disable the chart's own Namespace template to avoid the two
	// racing and producing an "already exists" error on install.
	vals["createNamespace"] = false
	if imageTag != "" {
		vals["image"] = map[string]interface{}{
			"tag": imageTag,
		}
	}
	for _, kv := range setValues {
		parts := strings.SplitN(kv, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid --set value %q (expected key=value)", kv)
		}
		if err := strvals.ParseInto(kv, vals); err != nil {
			return nil, fmt.Errorf("parsing --set %q: %w", kv, err)
		}
	}
	return vals, nil
}

// applyCRDs extracts CRDs from the embedded chart and applies them via kubectl
// with server-side apply. Helm installs CRDs on first install but never upgrades
// them, so we handle CRDs separately to ensure upgrades work correctly.
func applyCRDs(ch *chart.Chart) error {
	if len(ch.CRDObjects()) == 0 {
		return nil
	}
	tmpDir, err := os.MkdirTemp("", "sympozium-crds-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	for _, crd := range ch.CRDObjects() {
		path := filepath.Join(tmpDir, filepath.Base(crd.Name))
		if err := os.WriteFile(path, crd.File.Data, 0644); err != nil {
			return fmt.Errorf("writing CRD %s: %w", crd.Name, err)
		}
	}
	fmt.Println("  Applying CRDs...")
	return kubectl("apply", "--server-side", "--force-conflicts", "-f", tmpDir)
}

func runInstall(imageTag string, setValues []string) error {
	ver := version
	if ver == "" || ver == "dev" {
		ver = "embedded"
	}
	fmt.Printf("  Installing Sympozium %s...\n", ver)
	for _, kv := range setValues {
		if kv == "celln.enabled=true" {
			fmt.Println("  ⚠️  Hermetic workloads (Celln) enabled — this deploys a privileged,")
			fmt.Println("      hostPID DaemonSet with a read-write mount of the host root")
			fmt.Println("      filesystem on any node labeled celln.dev/kvm=true, to set up KVM")
			fmt.Println("      host-side. See docs/concepts/celln-backend.md.")
			break
		}
	}

	// Load the embedded Helm chart.
	ch, err := helmchart.Load()
	if err != nil {
		return fmt.Errorf("loading embedded chart: %w", err)
	}

	// Build Helm values from CLI flags.
	vals, err := buildHelmValues(imageTag, setValues)
	if err != nil {
		return err
	}

	// ── Pre-flight: stuck namespace ─────────────────────────────────────
	// If the namespace exists in Terminating state (e.g. from a prior
	// uninstall whose controller was removed before finalizers were
	// stripped), clean it up so the install can proceed.
	if nsPhase, err := exec.Command("kubectl", "get", "namespace", helmNamespace,
		"-o", "jsonpath={.status.phase}").Output(); err == nil && string(nsPhase) == "Terminating" {
		fmt.Println("  Namespace is stuck terminating, cleaning up finalizers...")
		for _, res := range sympoziumResources {
			stripFinalizers(res)
		}
		// Wait for the namespace to be fully removed.
		fmt.Println("  Waiting for namespace to be deleted...")
		_ = kubectl("wait", "--for=delete", "namespace/"+helmNamespace, "--timeout=60s")
	}

	// ── Pre-flight: CRDs ────────────────────────────────────────────────
	if err := applyCRDs(ch); err != nil {
		return err
	}

	// ── Pre-flight: Gateway API CRDs ────────────────────────────────────
	fmt.Println("  Installing Gateway API CRDs...")
	if err := kubectl("apply", "--server-side", "--force-conflicts", "-f", gatewayAPICRDsURL); err != nil {
		return fmt.Errorf("install Gateway API CRDs: %w", err)
	}

	// ── Pre-flight: cert-manager ────────────────────────────────────────
	fmt.Println("  Checking cert-manager...")
	if err := kubectlQuiet("get", "namespace", "cert-manager"); err != nil {
		fmt.Println("  Installing cert-manager...")
		if err := kubectl("apply", "-f",
			"https://github.com/cert-manager/cert-manager/releases/download/v1.17.1/cert-manager.yaml"); err != nil {
			return fmt.Errorf("install cert-manager: %w", err)
		}
		fmt.Println("  Waiting for cert-manager to be ready...")
		_ = kubectl("wait", "--for=condition=Available", "deployment/cert-manager",
			"-n", "cert-manager", "--timeout=120s")
		_ = kubectl("wait", "--for=condition=Available", "deployment/cert-manager-webhook",
			"-n", "cert-manager", "--timeout=120s")
		_ = kubectl("wait", "--for=condition=Available", "deployment/cert-manager-cainjector",
			"-n", "cert-manager", "--timeout=120s")
		fmt.Println("  Waiting for cert-manager webhook TLS to bootstrap...")
		time.Sleep(10 * time.Second)
	}

	// ── Helm install or upgrade ─────────────────────────────────────────
	cfg, err := newHelmConfig(helmNamespace)
	if err != nil {
		return err
	}

	// Check if a release already exists and in what state.
	histClient := action.NewHistory(cfg)
	histClient.Max = 1
	history, histErr := histClient.Run(helmReleaseName)

	// A release is recoverable-by-install if history is missing, or if the
	// most recent revision is in a non-deployed state (failed, pending-*,
	// uninstalled). In those cases, upgrade will error with "has no deployed
	// releases", so we uninstall and reinstall to recover cleanly.
	needsFreshInstall := histErr != nil
	if !needsFreshInstall && len(history) > 0 {
		switch history[len(history)-1].Info.Status {
		case release.StatusDeployed, release.StatusSuperseded:
			// Healthy — upgrade path.
		default:
			fmt.Printf("  Found previous release in %q state, cleaning up...\n", history[len(history)-1].Info.Status)
			uninstall := action.NewUninstall(cfg)
			uninstall.Wait = true
			uninstall.Timeout = 2 * time.Minute
			if _, err := uninstall.Run(helmReleaseName); err != nil {
				return fmt.Errorf("cleaning up failed release: %w", err)
			}
			needsFreshInstall = true
		}
	}

	if needsFreshInstall {
		fmt.Println("  Running Helm install...")
		install := action.NewInstall(cfg)
		install.ReleaseName = helmReleaseName
		install.Namespace = helmNamespace
		// Safe to always request namespace creation: Helm treats an existing
		// namespace as a no-op, and the chart's own Namespace template is
		// disabled via buildHelmValues (createNamespace=false), so there is
		// no collision.
		install.CreateNamespace = true
		install.SkipCRDs = true // We applied CRDs above.
		install.Wait = false    // Don't block — cert-manager certificate may need time.
		install.Timeout = 5 * time.Minute

		if _, err := install.Run(ch, vals); err != nil {
			return fmt.Errorf("helm install: %w", err)
		}
	} else {
		// Existing deployed release — upgrade.
		fmt.Println("  Running Helm upgrade...")
		upgrade := action.NewUpgrade(cfg)
		upgrade.Namespace = helmNamespace
		upgrade.SkipCRDs = true
		upgrade.Wait = false
		upgrade.Timeout = 5 * time.Minute

		if _, err := upgrade.Run(helmReleaseName, ch, vals); err != nil {
			return fmt.Errorf("helm upgrade: %w", err)
		}
	}

	fmt.Println("\n  Sympozium installed successfully!")
	fmt.Println("  Run: sympozium")
	fmt.Println("\n  To access the web dashboard:")
	fmt.Println("    sympozium serve")
	return nil
}

func runUninstall() error {
	fmt.Println("  Removing Sympozium...")

	// Strip finalizers from all Sympozium CRD instances BEFORE deleting the
	// controller, so resources don't get stuck in Terminating state.
	fmt.Println("  Removing finalizers from Sympozium resources...")
	for _, res := range sympoziumResources {
		stripFinalizers(res)
	}
	fmt.Println("  Deleting Sympozium custom resources...")
	for _, res := range sympoziumResources {
		_ = kubectl("delete", res+".sympozium.ai", "--all", "--all-namespaces", "--ignore-not-found", "--timeout=60s")
	}

	// ── Helm uninstall ──────────────────────────────────────────────────
	cfg, err := newHelmConfig(helmNamespace)
	if err != nil {
		// If we can't init Helm config, fall back to manual cleanup.
		fmt.Printf("  Warning: could not initialize Helm config: %v\n", err)
		fmt.Println("  Falling back to manual resource cleanup...")
		manualUninstall()
	} else {
		histClient := action.NewHistory(cfg)
		histClient.Max = 1
		if _, err := histClient.Run(helmReleaseName); err == nil {
			// Helm release exists — uninstall it.
			fmt.Println("  Running Helm uninstall...")
			uninstall := action.NewUninstall(cfg)
			uninstall.Timeout = 2 * time.Minute
			uninstall.KeepHistory = false
			if _, err := uninstall.Run(helmReleaseName); err != nil {
				fmt.Printf("  Warning: helm uninstall returned error: %v\n", err)
			}
		} else {
			// No Helm release found — this may be a legacy (pre-Helm) install.
			fmt.Println("  No Helm release found, cleaning up legacy resources...")
			manualUninstall()
		}
	}

	// ── CRDs (Helm does not delete CRDs by design) ──────────────────────
	fmt.Println("  Deleting Sympozium CRDs...")
	ch, err := helmchart.Load()
	if err == nil {
		// ch.CRDObjects() includes subchart CRDs (llmfit-dra's ModelClaim).
		// Those are shared cluster infrastructure: keep them when a
		// standalone llmfit-dra release still owns them.
		keepDepCRDs := hasStandaloneLLMFitRelease()
		depCRDs := map[string]bool{}
		for _, dep := range ch.Dependencies() {
			for _, crd := range dep.CRDObjects() {
				depCRDs[crd.Filename] = true
			}
		}
		for _, crd := range ch.CRDObjects() {
			if keepDepCRDs && depCRDs[crd.Filename] {
				fmt.Printf("  Keeping %s: a standalone llmfit-dra release still uses it\n", crd.Name)
				continue
			}
			_ = kubectlApplyDeleteStdin(string(crd.File.Data))
		}
		if !keepDepCRDs {
			// The DRA driver publishes ResourceSlices outside the Helm
			// release; remove any stragglers the kubelet didn't deregister.
			_ = kubectlQuiet("delete", "resourceslices.resource.k8s.io",
				"--field-selector", "spec.driver=llmfit.ai", "--ignore-not-found")
		}
	} else {
		// Fallback: delete by label.
		_ = kubectl("delete", "crd", "-l", "app.kubernetes.io/name=sympozium", "--ignore-not-found")
	}

	// Remove Gateway API CRDs installed by sympozium.
	fmt.Println("  Removing Gateway API CRDs...")
	_ = kubectl("delete", "--ignore-not-found", "-f", gatewayAPICRDsURL)

	// Remove the system namespace.
	fmt.Println("  Deleting namespace sympozium-system...")
	_ = kubectl("delete", "namespace", "sympozium-system", "--ignore-not-found", "--timeout=120s")

	fmt.Println("  Sympozium uninstalled.")
	return nil
}

// sympoziumResources lists the plural names of every sympozium.ai CRD — keep
// in sync with config/crd/bases/. Used to strip finalizers and delete CR
// instances before the controller goes away (install pre-flight + uninstall).
var sympoziumResources = []string{
	"agentruns", "agents", "ensembles", "mcpservers", "models",
	"skillpacks", "sympoziumconfigs", "sympoziumpolicies", "sympoziumschedules",
}

// hasStandaloneLLMFitRelease reports whether an llmfit-dra Helm release exists
// on its own (installed directly from the llmfit-dra repo) rather than as the
// subchart bundled into the sympozium release. Best-effort: any error counts
// as "not found".
func hasStandaloneLLMFitRelease() bool {
	cfg, err := newHelmConfig("")
	if err != nil {
		return false
	}
	list := action.NewList(cfg)
	list.AllNamespaces = true
	list.All = true
	releases, err := list.Run()
	if err != nil {
		return false
	}
	for _, r := range releases {
		if r.Chart != nil && r.Chart.Metadata != nil && r.Chart.Metadata.Name == "llmfit-dra" {
			return true
		}
	}
	return false
}

// manualUninstall deletes Sympozium resources directly via kubectl, used as a
// fallback for clusters where Sympozium was installed before the Helm migration.
func manualUninstall() {
	// Delete known resource types that the old raw-manifest install created.
	kinds := []string{
		"deployment", "service", "daemonset", "configmap",
		"serviceaccount", "clusterrole", "clusterrolebinding",
		"role", "rolebinding", "networkpolicy",
		"certificate", "issuer",
		"validatingwebhookconfiguration",
	}
	for _, k := range kinds {
		_ = kubectl("delete", k, "-n", helmNamespace, "-l", "app.kubernetes.io/part-of=sympozium", "--ignore-not-found")
	}
	// Also try by name for resources that may lack labels.
	_ = kubectl("delete", "deployment", "-n", helmNamespace,
		"sympozium-controller-manager", "sympozium-apiserver",
		"sympozium-webhook", "sympozium-nats", "sympozium-web-proxy",
		"--ignore-not-found")
}

// kubectlApplyDeleteStdin pipes YAML to kubectl delete. The wait is bounded:
// a CRD whose instances hold finalizers with no controller left to clear them
// would otherwise block deletion forever.
func kubectlApplyDeleteStdin(yaml string) error {
	cmd := exec.Command("kubectl", "delete", "--ignore-not-found", "--timeout=60s", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// stripFinalizers patches all instances of a Sympozium CRD to remove finalizers.
func stripFinalizers(resource string) {
	out, err := exec.Command("kubectl", "get", resource+".sympozium.ai",
		"--all-namespaces", "-o", "jsonpath={range .items[*]}{.metadata.namespace}/{.metadata.name}{\"\\n\"}{end}").
		Output()
	if err != nil {
		return // CRD may not exist
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "/", 2)
		if len(parts) != 2 {
			continue
		}
		ns, name := parts[0], parts[1]
		_ = exec.Command("kubectl", "patch", resource+".sympozium.ai", name,
			"-n", ns, "--type=merge",
			"-p", `{"metadata":{"finalizers":[]}}`).Run()
	}
}

func kubectl(args ...string) error {
	cmd := exec.Command("kubectl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// kubectlQuiet runs kubectl but suppresses stderr — used for existence probes
// where a NotFound error is expected and should not be shown to the user.
func kubectlQuiet(args ...string) error {
	cmd := exec.Command("kubectl", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = io.Discard
	return cmd.Run()
}

// generateToken returns a random alphanumeric string of the given length.
func generateToken(n int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	_, _ = io.ReadFull(cryptoRand.Reader, b)
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}

// ═══════════════════════════════════════════════════════════════════════════
//  TUI — Interactive Terminal UI (k9s-style)
// ═══════════════════════════════════════════════════════════════════════════

// ── Views ────────────────────────────────────────────────────────────────────

type tuiViewKind int

const (
	viewEnsembles tuiViewKind = iota
	viewAgents
	viewRuns
	viewPolicies
	viewSkills
	viewChannels
	viewSchedules
	viewGateway
	viewPods
)

var viewNames = []string{"Ensembles", "Agents", "Runs", "Policies", "Skills", "Channels", "Schedules", "Gateway", "Pods"}

// detailPaneState controls the visibility of the right-hand detail pane.
type detailPaneState int

const (
	paneCollapsed  detailPaneState = iota // hidden (default)
	panePanel                             // side panel (~35%)
	paneFullscreen                        // takes over the whole screen
)

// ── Styles ───────────────────────────────────────────────────────────────────

var (
	// ── Neo Industrial TUI palette ──────────────────────────────────
	tuiBannerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#e8562a")).
			Background(lipgloss.Color("#1a1a18"))

	tuiTabStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8a8c82")).
			Background(lipgloss.Color("#1a1a18")).
			Padding(0, 1)

	tuiTabActiveStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#e8562a")).
				Background(lipgloss.Color("#242422")).
				Padding(0, 1)

	tuiColHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#f0ece4")).
				Background(lipgloss.Color("#1a1a18"))

	tuiRowStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f0ece4"))

	tuiRowAltStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#d4cfc6")).
			Background(lipgloss.Color("#1a1a18"))

	tuiRowSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#1a1a18")).
				Background(lipgloss.Color("#e8562a"))

	tuiDimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#8a8c82"))

	tuiErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f87171")).
			Bold(true)

	tuiSuccessStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#34d399")).
			Bold(true)

	tuiRunningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#60a5fa"))

	tuiPendingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#facc15"))

	tuiPostRunningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#fb923c"))

	tuiServingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#facc15"))

	tuiPromptStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#e8562a")).
			Bold(true)

	tuiHeaderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f0ece4")).
			Bold(true)

	tuiStatusBarStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#d4cfc6")).
				Background(lipgloss.Color("#242422"))

	tuiStatusKeyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#e8562a")).
				Background(lipgloss.Color("#242422")).
				Bold(true)

	tuiLogBorderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#333330"))

	tuiSepStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#333330"))

	tuiCountStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#e8562a")).
			Bold(true)

	tuiModalBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#e8562a")).
				Padding(1, 2).
				Background(lipgloss.Color("#242422"))

	tuiModalTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#e8562a")).
				Background(lipgloss.Color("#242422"))

	tuiModalCmdStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#e8562a"))

	tuiModalDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#8a8c82"))

	tuiSuggestStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f0ece4")).
			Background(lipgloss.Color("#242422")).
			Padding(0, 1)

	tuiSuggestSelectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#1a1a18")).
				Background(lipgloss.Color("#e8562a")).
				Bold(true).
				Padding(0, 1)

	tuiSuggestDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#8a8c82")).
				Background(lipgloss.Color("#242422"))

	tuiSuggestDescSelectedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("#1a1a18")).
					Background(lipgloss.Color("#e8562a"))

	// Feed pane styles
	tuiFeedTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#e8562a")).
				Bold(true)

	tuiFeedPromptStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#f0ece4"))

	tuiFeedMetaStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#8a8c82"))
)

func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Interactive terminal UI for managing Sympozium",
		Long:  `Launch an interactive terminal interface with slash commands for managing Agents, AgentRuns, policies, and more.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := initClient(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: could not connect to cluster: %v\n", err)
				fmt.Fprintln(os.Stderr, "TUI will start in disconnected mode.")
			}

			m := newTUIModel(namespace)
			p := tea.NewProgram(m, tea.WithAltScreen())
			_, err := p.Run()
			return err
		},
	}
}

// ── Messages ─────────────────────────────────────────────────────────────────

type tickMsg time.Time
type cmdResultMsg struct {
	output string
	err    error
}
type whatsappQRPollMsg struct {
	qrLines []string // QR code lines to display (empty if not ready)
	linked  bool     // true when pairing succeeded
	status  string   // human-readable status
	err     error
}
type githubAuthDeviceCodeMsg struct {
	deviceCode string
	userCode   string
	verifyURL  string
	interval   int
	err        error
}
type githubAuthPollMsg struct {
	token string
	done  bool
	err   error
}
type githubAuthTokenWrittenMsg struct {
	err         error
	alreadyDone bool
}
type suggestionsMsg struct {
	items []suggestion
}
type dataRefreshMsg struct {
	instances     *[]sympoziumv1alpha1.Agent
	runs          *[]sympoziumv1alpha1.AgentRun
	policies      *[]sympoziumv1alpha1.SympoziumPolicy
	skills        *[]sympoziumv1alpha1.SkillPack
	channels      *[]channelRow
	pods          *[]podRow
	schedules     *[]sympoziumv1alpha1.SympoziumSchedule
	ensembles     *[]sympoziumv1alpha1.Ensemble
	gatewayConfig *sympoziumv1alpha1.SympoziumConfig
	fetchErr      string
}

// ── Suggestion ───────────────────────────────────────────────────────────────

type suggestion struct {
	text string
	desc string
}

var slashCommandSuggestions = []suggestion{
	{"/agents", "List Agents"},
	{"/runs", "List AgentRuns"},
	{"/run", "Create AgentRun: /run <inst> <task>"},
	{"/abort", "Abort run: /abort <run>"},
	{"/result", "Show run result: /result <run>"},
	{"/status", "Cluster or run status"},
	{"/channels", "View channels for agent"},
	{"/channel", "Add channel: /channel <inst> <type> <secret>"},
	{"/pods", "View agent pods: /pods <inst>"},
	{"/provider", "Set provider: /provider <inst> <provider> <model>"},
	{"/policies", "List SympoziumPolicies"},
	{"/skills", "List SkillPacks"},
	{"/features", "Feature gates: /features <policy>"},
	{"/delete", "Delete: /delete <type> <name>"},
	{"/schedule", "Create schedule: /schedule <inst> <cron> <task>"},
	{"/schedules", "View schedules"},
	{"/ensembles", "View Ensembles"},
	{"/ensemble", "Manage ensemble: /ensemble delete <name>"},
	{"/memory", "View memory: /memory <inst>"},
	{"/ns", "Switch namespace: /ns <name>"},
	{"/onboard", "Interactive setup wizard"},
	{"/help", "Show help modal"},
	{"/quit", "Exit the TUI"},
}

var deleteTypeSuggestions = []suggestion{
	{"agent", "Delete an Agent"},
	{"run", "Delete an AgentRun"},
	{"policy", "Delete a SympoziumPolicy"},
	{"schedule", "Delete a SympoziumSchedule"},
	{"ensemble", "Delete an Ensemble"},
	{"channel", "Remove a channel from agent"},
}

var channelTypeSuggestions = []suggestion{
	{"telegram", "Telegram bot channel"},
	{"slack", "Slack integration"},
	{"discord", "Discord bot channel"},
	{"whatsapp", "WhatsApp channel"},
}

var providerSuggestions = []suggestion{
	{"openai", "OpenAI (GPT-4o, etc.)"},
	{"anthropic", "Anthropic (Claude)"},
	{"azure-openai", "Azure OpenAI Service"},
	{"ollama", "Ollama (local)"},
	{"llama-server", "llama-server (llama.cpp local)"},
	{"bedrock", "AWS Bedrock (Claude, Nova, etc.)"},
	{"openai-compatible", "OpenAI-compatible endpoint"},
}

var modelSuggestions = map[string][]suggestion{
	"openai": {
		{"gpt-4o", "Best overall, 128k ctx"},
		{"gpt-4o-mini", "Fast & cheap, 128k ctx"},
		{"gpt-4.1", "Latest GPT-4.1, 1M ctx"},
		{"gpt-4.1-mini", "Fast GPT-4.1, 1M ctx"},
		{"gpt-4.1-nano", "Cheapest GPT-4.1, 1M ctx"},
		{"o3", "Reasoning, 200k ctx"},
		{"o3-mini", "Fast reasoning, 200k ctx"},
		{"o4-mini", "Latest reasoning, 200k ctx"},
	},
	"anthropic": {
		{"claude-sonnet-4-20250514", "Best balanced, 200k ctx"},
		{"claude-opus-4-20250514", "Most capable, 200k ctx"},
		{"claude-haiku-3-5-20241022", "Fast & cheap, 200k ctx"},
	},
	"azure-openai": {
		{"gpt-4o", "GPT-4o deployment"},
		{"gpt-4o-mini", "GPT-4o-mini deployment"},
		{"gpt-4.1", "GPT-4.1 deployment"},
		{"o3-mini", "o3-mini deployment"},
	},
	"google": {
		{"gemini-2.5-pro", "Most capable, 1M ctx"},
		{"gemini-2.5-flash", "Fast & efficient, 1M ctx"},
		{"gemini-2.0-flash", "Previous gen fast, 1M ctx"},
	},
	"bedrock": {
		{"anthropic.claude-sonnet-4-20250514-v1:0", "Claude Sonnet 4"},
		{"anthropic.claude-haiku-4-5-20251001-v1:0", "Claude Haiku 4.5"},
		{"amazon.nova-pro-v1:0", "Amazon Nova Pro"},
		{"amazon.nova-lite-v1:0", "Amazon Nova Lite"},
	},
	"ollama": {
		{"llama3", "Meta Llama 3 8B"},
		{"llama3.3", "Meta Llama 3.3 70B"},
		{"qwen3", "Alibaba Qwen3"},
		{"deepseek-r1", "DeepSeek R1 reasoning"},
		{"mistral", "Mistral 7B"},
		{"codellama", "Code Llama 7B"},
		{"phi4", "Microsoft Phi-4"},
		{"gemma3", "Google Gemma 3"},
	},
}

// fetchProviderModels calls the provider's model-list API and returns model IDs.
// Supports OpenAI-compatible APIs (OpenAI, Azure OpenAI, any /v1/models endpoint).
// Returns nil on any error — the wizard falls back to the static suggestions.
func fetchProviderModels(provider, apiKey, baseURL string) ([]string, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("no API key")
	}

	endpoint := ""
	authHeader := "Bearer " + apiKey
	switch provider {
	case "openai":
		endpoint = "https://api.openai.com/v1/models"
	case "azure-openai":
		if baseURL == "" {
			return nil, fmt.Errorf("no base URL for azure-openai")
		}
		// Azure: GET {endpoint}/openai/models?api-version=2024-06-01
		endpoint = strings.TrimRight(baseURL, "/") + "/openai/models?api-version=2024-06-01"
		authHeader = "" // Azure uses api-key header
	case "anthropic":
		endpoint = "https://api.anthropic.com/v1/models"
		authHeader = "" // Anthropic uses x-api-key
	case "bedrock":
		// Bedrock model listing requires the Bedrock service API and AWS credentials
		// which the CLI may not have. Rely on static model suggestions.
		return nil, fmt.Errorf("dynamic model listing not supported for Bedrock")
	default:
		if baseURL != "" {
			endpoint = strings.TrimRight(baseURL, "/") + "/v1/models"
		} else {
			return nil, fmt.Errorf("unsupported provider for model listing: %s", provider)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if provider == "azure-openai" {
		req.Header.Set("api-key", apiKey)
	}
	if provider == "anthropic" {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// OpenAI/Anthropic response: {"data": [{"id": "gpt-4o", ...}, ...]}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}

	var models []string
	for _, m := range parsed.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	sort.Strings(models)
	return models, nil
}

// filterChatModels keeps only models likely useful for chat/completion tasks.
// Strips embedding, tts, whisper, dall-e, moderation, and other non-chat models.
// Uses targeted prefix/suffix checks to avoid accidentally excluding valid chat
// models that happen to contain common substrings.
func filterChatModels(models []string) []string {
	var filtered []string
	for _, m := range models {
		lower := strings.ToLower(m)

		// Exact-prefix exclusions: models whose name starts with a non-chat family.
		skipPrefixes := []string{
			"text-embedding", "text-search", "text-similarity", "text-davinci",
			"text-curie", "text-babbage", "text-ada", "text-moderation",
			"tts-", "whisper-", "dall-e-", "davinci", "babbage", "curie", "ada",
			"code-davinci", "code-cushman",
			"canary-", "ftjob-", "ft:",
		}
		skip := false
		for _, prefix := range skipPrefixes {
			if strings.HasPrefix(lower, prefix) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}

		// Substring exclusions: non-chat capabilities that appear in model names.
		skipSubstrings := []string{
			"embedding", "moderation",
		}
		for _, sub := range skipSubstrings {
			if strings.Contains(lower, sub) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

// ── GitHub OAuth device-flow helpers ─────────────────────────────────────────

// checkAndStartGithubAuthCmd checks whether the github-gitops-token secret
// already exists. If it does, it signals "already done"; otherwise it starts
// the GitHub OAuth 2.0 device flow.
func checkAndStartGithubAuthCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ex := &corev1.Secret{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "github-gitops-token", Namespace: "sympozium-system"}, ex)
		if err == nil {
			// Token secret already exists — nothing to do.
			return githubAuthTokenWrittenMsg{alreadyDone: true}
		}
		clientID := os.Getenv("GITHUB_OAUTH_CLIENT_ID")
		if clientID == "" {
			return githubAuthDeviceCodeMsg{err: fmt.Errorf("GITHUB_OAUTH_CLIENT_ID env var not set — please configure it on the apiserver deployment")}
		}
		body := "client_id=" + clientID + "&scope=repo"
		req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/device/code", strings.NewReader(body))
		if err != nil {
			return githubAuthDeviceCodeMsg{err: err}
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return githubAuthDeviceCodeMsg{err: err}
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var result struct {
			DeviceCode      string `json:"device_code"`
			UserCode        string `json:"user_code"`
			VerificationURI string `json:"verification_uri"`
			Interval        int    `json:"interval"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return githubAuthDeviceCodeMsg{err: fmt.Errorf("parse response: %w", err)}
		}
		if result.DeviceCode == "" {
			return githubAuthDeviceCodeMsg{err: fmt.Errorf("no device_code returned by GitHub")}
		}
		if result.Interval == 0 {
			result.Interval = 5
		}
		return githubAuthDeviceCodeMsg{
			deviceCode: result.DeviceCode,
			userCode:   result.UserCode,
			verifyURL:  result.VerificationURI,
			interval:   result.Interval,
		}
	}
}

// pollGithubTokenCmd waits `interval` seconds then polls GitHub for the token.
func pollGithubTokenCmd(deviceCode string, interval int) tea.Cmd {
	delay := time.Duration(interval) * time.Second
	if delay < 5*time.Second {
		delay = 5 * time.Second
	}
	return tea.Tick(delay, func(_ time.Time) tea.Msg {
		clientID := os.Getenv("GITHUB_OAUTH_CLIENT_ID")
		body := "client_id=" + clientID +
			"&device_code=" + deviceCode +
			"&grant_type=urn:ietf:params:oauth:grant-type:device_code"
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(body))
		if err != nil {
			return githubAuthPollMsg{err: err}
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return githubAuthPollMsg{err: err}
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		var result struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return githubAuthPollMsg{err: fmt.Errorf("parse poll response: %w", err)}
		}
		if result.AccessToken != "" {
			return githubAuthPollMsg{done: true, token: result.AccessToken}
		}
		if result.Error == "authorization_pending" || result.Error == "slow_down" {
			return githubAuthPollMsg{done: false}
		}
		return githubAuthPollMsg{err: fmt.Errorf("GitHub auth error: %s", result.Error)}
	})
}

// writeGithubTokenCmd writes the access token to the github-gitops-token Secret.
func writeGithubTokenCmd(token string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		sec := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "github-gitops-token", Namespace: "sympozium-system"},
			Type:       corev1.SecretTypeOpaque,
			StringData: map[string]string{"GH_TOKEN": token},
		}
		existing := &corev1.Secret{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: "github-gitops-token", Namespace: "sympozium-system"}, existing)
		if err == nil {
			existing.StringData = map[string]string{"GH_TOKEN": token}
			return githubAuthTokenWrittenMsg{err: k8sClient.Update(ctx, existing)}
		}
		return githubAuthTokenWrittenMsg{err: k8sClient.Create(ctx, sec)}
	}
}

var tuiCommands = []struct{ cmd, desc string }{
	{"/agents", "List Agents"},
	{"/runs", "List AgentRuns"},
	{"/run <inst> <task>", "Create a new AgentRun"},
	{"/abort <run>", "Abort a running AgentRun"},
	{"/result <run>", "Show the LLM response"},
	{"/status [run]", "Cluster / run status"},
	{"/channels [inst]", "View channels (tab 5)"},
	{"/channel <i> <type> <sec>", "Add channel to agent"},
	{"/rmchannel <inst> <type>", "Remove channel"},
	{"/pods [inst]", "Agent pods (tab 6)"},
	{"/provider <i> <prov> <mod>", "Set provider/model"},
	{"/baseurl <inst> <url>", "Set custom base URL"},
	{"/policies", "List SympoziumPolicies"},
	{"/skills", "List SkillPacks"},
	{"/features <pol>", "Feature gates on a policy"},
	{"/delete <type> <name>", "Delete resource"},
	{"/ns <namespace>", "Switch namespace"},
	{"/onboard", "Interactive setup wizard"},
	{"/help  or  ?", "Show this help"},
	{"/quit", "Exit the TUI"},
	{"", ""},
	{"── Keys ──", ""},
	{"l", "Logs (pods) / events (resources)"},
	{"d", "Describe selected resource"},
	{"Esc", "Go back / return to Agents"},
	{"R", "Run task on selected agent"},
	{"O", "Launch onboard wizard"},
	{"x", "Delete selected resource"},
	{"e", "Edit memory / heartbeat config"},
	{"Enter", "Detail / drill in / onboard ensemble"},
	{"r", "Refresh data"},
}

// ── Model ────────────────────────────────────────────────────────────────────

// channelRow is a flattened view of channel config + status across instances.
type channelRow struct {
	InstanceName string
	Type         string
	SecretRef    string
	Status       string
	LastCheck    string
	Message      string
}

// podRow is a flattened view of agent pods across instances.
type podRow struct {
	Name     string
	Instance string
	Phase    string
	Node     string
	IP       string
	Age      string
	Restarts int32
}

// ── Onboard Wizard ───────────────────────────────────────────────────────────

type wizardStep int

const (
	wizStepNone                      wizardStep = iota
	wizStepCheckCluster                         // auto — verify CRDs
	wizStepNamespace                            // text: target namespace
	wizStepInstanceName                         // text: instance name
	wizStepProvider                             // menu 1-6: provider
	wizStepModel                                // text: model name
	wizStepBaseURL                              // text: base URL (some providers)
	wizStepAPIKey                               // text: API key (non-ollama)
	wizStepGithubRepo                           // text: GitHub repo (owner/repo)
	wizStepTeamTask                             // text: team-level task/instructions
	wizStepChannel                              // menu 1-5: channel type
	wizStepChannelToken                         // text: channel bot token
	wizStepPolicy                               // y/n: apply default policy
	wizStepAgentSandbox                         // y/n: enable agent sandbox (CRD) isolation
	wizStepRunTimeout                           // menu: run timeout per agent run
	wizStepHeartbeat                            // menu 1-5: heartbeat interval
	wizStepConfirm                              // y/n: confirm summary
	wizStepApplying                             // auto — create resources
	wizStepWhatsAppQR                           // auto — stream QR from pod logs
	wizStepDone                                 // auto — show result
	wizStepLMStudioAPIKeyRequired               // y/n: LM Studio requires API key?
	wizStepLlamaServerAPIKeyRequired            // y/n: llama-server requires API key?
	wizStepAWSRegion                            // text: AWS region for Bedrock
	wizStepAWSAccessKeyID                       // text: AWS Access Key ID
	wizStepAWSSecretAccessKey                   // text: AWS Secret Access Key
	wizStepAWSSessionToken                      // text: AWS Session Token (optional)

	// Persona wizard steps
	wizStepPersonaPick                      // menu: select a ensemble
	wizStepPersonaProvider                  // menu 1-6: provider
	wizStepPersonaBaseURL                   // text: base URL
	wizStepPersonaLMStudioAPIKeyRequired    // y/n: LM Studio requires API key?
	wizStepPersonaLlamaServerAPIKeyRequired // y/n: llama-server requires API key?
	wizStepPersonaAPIKey                    // text: API key
	wizStepPersonaModel                     // text: model name
	wizStepPersonaGithubRepo                // text: GitHub repo (owner/repo)
	wizStepPersonaTeamTask                  // text: team-level task/instructions
	wizStepPersonaAgentSandbox              // y/n: enable agent sandbox (CRD) isolation
	wizStepPersonaChannels                  // multi-toggle: channels to bind
	wizStepPersonaChannelToken              // text: channel token (per selected channel)
	wizStepPersonaHeartbeat                 // menu 1-5: heartbeat interval override
	wizStepPersonaConfirm                   // y/n: confirm summary
	wizStepPersonaApplying                  // auto — patch pack + create resources
	wizStepPersonaDone                      // auto — show result
)

type wizardState struct {
	active     bool
	step       wizardStep
	err        string // error from last step
	resultMsgs []string

	// Collected values
	targetNamespace     string
	instanceName        string
	providerChoice      string // "1"–"6"
	providerName        string
	modelName           string
	baseURL             string
	secretEnvKey        string
	apiKey              string
	channelChoice       string // "1"–"5"
	channelType         string
	channelTokenKey     string
	channelToken        string
	applyPolicy         bool
	heartbeatCron       string // cron expression for heartbeat schedule
	heartbeatLabel      string // human-readable label (e.g. "every hour")
	githubRepo          string // GitHub repo (owner/repo) for github-gitops skill
	teamTask            string // Team-level instructions/objective
	agentSandboxEnabled bool   // Enable Agent Sandbox (CRD) kernel-level isolation
	runTimeout          string // Max duration per agent run (e.g. "30m", "1h")

	// AWS Bedrock credentials (collected via dedicated wizard steps).
	awsRegion          string
	awsAccessKeyID     string
	awsSecretAccessKey string
	awsSessionToken    string

	// Dynamic model list (fetched from provider API when key is supplied).
	fetchedModels []string // model IDs fetched from the API
	modelFetchErr string   // non-fatal error message if fetch failed

	// WhatsApp QR pairing state
	qrLines  []string // QR code lines from pod logs
	qrStatus string   // "waiting", "scanning", "linked", "error"
	qrErr    string   // error message if QR polling failed

	// Wizard panel scroll offset for long content (e.g. model lists).
	scrollOffset int

	// Persona wizard state
	personaMode       bool                   // true when running persona wizard instead of onboard
	ensembleName      string                 // which pack we're installing
	personaChannels   []personaChannelChoice // channels the user is toggling
	personaChannelIdx int                    // which channel we're collecting a token for
	packDetailScroll  int                    // scroll offset for the pack detail pane
}

func (w *wizardState) reset() {
	*w = wizardState{}
}

// personaChannelChoice tracks a channel toggle during persona onboarding.
type personaChannelChoice struct {
	chType   string // telegram, slack, discord, whatsapp
	enabled  bool
	tokenKey string // env var name (e.g. TELEGRAM_BOT_TOKEN)
	token    string // user-supplied token value
}

var defaultPersonaChannels = []personaChannelChoice{
	{chType: "telegram", tokenKey: "TELEGRAM_BOT_TOKEN"},
	{chType: "slack", tokenKey: "SLACK_BOT_TOKEN"},
	{chType: "discord", tokenKey: "DISCORD_BOT_TOKEN"},
	{chType: "whatsapp", tokenKey: ""}, // QR pairing, no token
}

type logEntry struct {
	time  time.Time
	level string // "info", "warn", "error", "success"
	text  string
}

type tuiModel struct {
	width     int
	height    int
	ready     bool
	quitting  bool
	showModal bool

	// View state
	activeView    tuiViewKind
	selectedRow   int
	tableScroll   int
	drillInstance string // filtered instance for channels/pods views

	// In-view filter (Ctrl+F)
	filterMode  bool
	filterText  string
	filterInput textinput.Model
	filteredIdx []int // maps visible row → original data index

	// Wizard
	wizard wizardState

	// Cached K8s data
	instances     []sympoziumv1alpha1.Agent
	runs          []sympoziumv1alpha1.AgentRun
	policies      []sympoziumv1alpha1.SympoziumPolicy
	skills        []sympoziumv1alpha1.SkillPack
	channels      []channelRow
	pods          []podRow
	schedules     []sympoziumv1alpha1.SympoziumSchedule
	ensembles     []sympoziumv1alpha1.Ensemble
	gatewayConfig *sympoziumv1alpha1.SympoziumConfig

	// Input
	input        textinput.Model
	inputFocused bool

	// Log
	logEntries []logEntry
	logHidden  bool
	logScroll  int

	// Cluster
	namespace string
	connected bool

	// Autocomplete
	suggestions []suggestion
	suggestIdx  int
	lastInput   string

	// Delete confirmation
	confirmDelete      bool
	deleteResourceKind string // e.g. "agent", "run", "pod"
	deleteResourceName string
	deleteFunc         func() (string, error) // the actual delete function

	// Edit modal
	showEditModal         bool
	editTab               int // 0=Memory, 1=Heartbeat
	editInstanceName      string
	editScheduleName      string // non-empty when editing an existing schedule
	editField             int    // which field is selected in the current tab
	editMemory            editMemoryForm
	editHeartbeat         editHeartbeatForm
	editTaskInput         bool            // sub-modal for task text entry
	editTaskTI            textinput.Model // text input for task sub-modal
	editChannelTokenInput bool            // sub-modal for channel token entry
	editChannelTokenTI    textinput.Model // text input for channel token sub-modal
	editChannelTokenIdx   int             // index into editChannels being configured
	editChannelNewTokens  map[int]string  // idx → token for channels needing secret creation
	editSkillGithubInput  bool            // sub-modal for github-gitops repo entry
	editSkillGithubTI     textinput.Model // text input for github repo
	editSkillGithubIdx    int             // index into editSkills being configured

	// GitHub OAuth device-flow auth state (displayed inline in the skills tab)
	githubAuthActive     bool   // device-flow prompt is visible
	githubAuthUserCode   string // e.g. "ABCD-1234" — user must enter at GitHub
	githubAuthVerifyURL  string // URL shown to the user
	githubAuthDeviceCode string // opaque code for polling
	githubAuthInterval   int    // seconds between poll requests
	githubAuthStatus     string // "pending" | "done" | "error"
	githubAuthMessage    string // success note or error detail

	editSkills               []editSkillItem         // toggleable skills list
	editChannels             []editChannelItem       // channel bindings
	editWebEndpoint          editWebEndpointForm     // web endpoint config
	editLifecycle            editLifecycleForm       // lifecycle hooks config
	editLifecycleHookInput   bool                    // sub-modal for hook editing
	editLifecycleHookTI      textinput.Model         // text input for hook sub-modal
	editLifecycleHookField   int                     // 0=name, 1=image, 2=command, 3=env
	editLifecycleHookIdx     int                     // index into preRun/postRun being edited
	editLifecycleHookIsPost  bool                    // true if editing postRun, false for preRun
	editGateway              editGatewayForm         // gateway config
	showGatewayEditModal     bool                    // separate modal for gateway
	editEnsembleName         string                  // non-empty when editing a Ensemble
	editEnsembleAgents       []editEnsembleAgentItem // toggleable agent configs list
	editEnsembleHeartbeatIdx int                     // index into ensembleHeartbeatOptions

	// Detail pane
	detailPane       detailPaneState // collapsed, panel, or fullscreen
	feedInputFocused bool            // typing in the feed chat
	feedInput        textinput.Model
	feedScrollOffset int // 0 = pinned to bottom; >0 = scrolled up N lines
}

// editMemoryForm holds the editable memory fields for a Agent.
type editMemoryForm struct {
	enabled      bool
	maxSizeKB    string // edited as text, parsed to int on apply
	systemPrompt string
}

// editHeartbeatForm holds the editable schedule fields.
type editHeartbeatForm struct {
	schedule          string
	task              string
	schedType         int // index into editScheduleTypes
	concurrencyPolicy int // index into editConcurrencyPolicies
	includeMemory     bool
	suspend           bool
}

// editSkillItem represents a toggleable skill in the edit modal.
type editSkillItem struct {
	name     string            // SkillPack name
	enabled  bool              // whether it's in the agent's Skills list
	category string            // e.g. "kubernetes"
	params   map[string]string // per-skill params (e.g. repo for github-gitops)
	hostReq  bool              // whether this skill requests host access
	hostInfo string            // concise host access summary (pid/net/root/priv/mounts)
}

// editChannelItem represents a channel binding in the edit modal.
type editChannelItem struct {
	chType    string // telegram, slack, discord, whatsapp
	enabled   bool   // whether channel is bound to the agent
	secretRef string // secret name for credentials
	tokenKey  string // env var name for the token (e.g. TELEGRAM_BOT_TOKEN)
}

// editWebEndpointForm holds the editable web endpoint fields.
type editWebEndpointForm struct {
	enabled   bool
	hostname  string
	rateLimit string // requests per minute, edited as text
}

// editWebEndpointFieldCount is the number of fields in the web endpoint tab.
var editWebEndpointFieldCount = 3 // enabled, hostname, rateLimit

// editGatewayForm holds the editable gateway configuration fields.
type editGatewayForm struct {
	enabled                  bool
	baseDomain               string
	gatewayClassName         string
	gatewayName              string
	tlsEnabled               bool
	certManagerClusterIssuer string
	tlsSecretName            string
}

var editGatewayFieldCount = 7

// editEnsembleAgentItem represents a toggleable agent config in the Ensemble edit modal.
type editEnsembleAgentItem struct {
	name        string // agent config name within the ensemble
	displayName string // human-readable name
	enabled     bool   // true = active, false = excluded
}

// editLifecycleHook represents a single preRun or postRun hook in the edit modal.
type editLifecycleHook struct {
	name    string // container name
	image   string // container image
	command string // command as a single string (split by spaces on save)
	envVars string // "KEY=VALUE" per line
}

// editLifecycleForm holds the editable lifecycle hooks for a Agent.
type editLifecycleForm struct {
	preRun  []editLifecycleHook
	postRun []editLifecycleHook
	rbac    string // "resource:verb1,verb2" per line (e.g. "configmaps:get,list,create,delete")
}

// editLifecycleFieldCount returns the dynamic field count for the lifecycle tab.
func (f *editLifecycleForm) fieldCount() int {
	// preRun hooks + "add preRun" button + postRun hooks + "add postRun" button + rbac field
	return len(f.preRun) + 1 + len(f.postRun) + 1 + 1
}

var editScheduleTypes = []string{"heartbeat", "scheduled", "sweep"}
var editConcurrencyPolicies = []string{"Forbid", "Allow", "Replace"}
var editMemoryFieldCount = 3    // enabled, maxSizeKB, systemPrompt
var editHeartbeatFieldCount = 6 // schedule, task, type, concurrencyPolicy, includeMemory, suspend
var editTabNames = []string{"Memory", "Heartbeat", "Skills", "Channels", "Web Endpoint", "Lifecycle"}
var availableChannelTypes = []string{"telegram", "slack", "discord", "whatsapp"}

// ensembleHeartbeatOptions defines the selectable heartbeat intervals for Ensemble editing.
var ensembleHeartbeatOptions = []struct {
	label    string
	interval string // value written to AgentConfigSchedule.Interval
}{
	{"30m", "30m"},
	{"1h", "1h"},
	{"6h", "6h"},
	{"24h", "24h"},
	{"pack default", ""},
}

// channelTokenKeyFor returns the env var name used in the channel secret for the given type.
func channelTokenKeyFor(chType string) string {
	switch chType {
	case "telegram":
		return "TELEGRAM_BOT_TOKEN"
	case "slack":
		return "SLACK_BOT_TOKEN"
	case "discord":
		return "DISCORD_BOT_TOKEN"
	default:
		return "" // whatsapp uses QR pairing, no token
	}
}

// lifecycleEnvToString converts a list of EnvVar to "KEY=VALUE" lines.
func lifecycleEnvToString(envs []sympoziumv1alpha1.EnvVar) string {
	var lines []string
	for _, e := range envs {
		lines = append(lines, e.Name+"="+e.Value)
	}
	return strings.Join(lines, "\n")
}

// lifecycleEnvFromString parses "KEY=VALUE" lines into EnvVar list.
func lifecycleEnvFromString(s string) []sympoziumv1alpha1.EnvVar {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var envs []sympoziumv1alpha1.EnvVar
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			envs = append(envs, sympoziumv1alpha1.EnvVar{Name: parts[0], Value: parts[1]})
		}
	}
	return envs
}

// lifecycleRBACToString converts RBAC rules to "resource:verb1,verb2" lines.
func lifecycleRBACToString(rules []sympoziumv1alpha1.RBACRule) string {
	var lines []string
	for _, r := range rules {
		lines = append(lines, strings.Join(r.Resources, ",")+":"+strings.Join(r.Verbs, ","))
	}
	return strings.Join(lines, "\n")
}

// lifecycleRBACFromString parses "resource:verb1,verb2" lines into RBAC rules.
func lifecycleRBACFromString(s string) []sympoziumv1alpha1.RBACRule {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var rules []sympoziumv1alpha1.RBACRule
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			rules = append(rules, sympoziumv1alpha1.RBACRule{
				APIGroups: []string{""},
				Resources: strings.Split(parts[0], ","),
				Verbs:     strings.Split(parts[1], ","),
			})
		}
	}
	return rules
}

// handleLifecycleEnter opens a sub-modal for editing/adding lifecycle hooks.
func (m *tuiModel) handleLifecycleEnter() {
	lc := &m.editLifecycle
	preLen := len(lc.preRun)
	addPreIdx := preLen        // "add preRun" button
	postStart := addPreIdx + 1 // first postRun hook
	postLen := len(lc.postRun)
	addPostIdx := postStart + postLen // "add postRun" button
	rbacIdx := addPostIdx + 1         // RBAC field

	switch {
	case m.editField < preLen:
		// Edit existing preRun hook.
		h := &lc.preRun[m.editField]
		m.openLifecycleHookEditor(h, m.editField, false)
	case m.editField == addPreIdx:
		// Add new preRun hook.
		lc.preRun = append(lc.preRun, editLifecycleHook{name: fmt.Sprintf("pre-hook-%d", preLen+1), image: "busybox:1.36"})
		h := &lc.preRun[len(lc.preRun)-1]
		m.openLifecycleHookEditor(h, len(lc.preRun)-1, false)
	case m.editField >= postStart && m.editField < addPostIdx:
		// Edit existing postRun hook.
		idx := m.editField - postStart
		h := &lc.postRun[idx]
		m.openLifecycleHookEditor(h, idx, true)
	case m.editField == addPostIdx:
		// Add new postRun hook.
		lc.postRun = append(lc.postRun, editLifecycleHook{name: fmt.Sprintf("post-hook-%d", postLen+1), image: "busybox:1.36"})
		h := &lc.postRun[len(lc.postRun)-1]
		m.openLifecycleHookEditor(h, len(lc.postRun)-1, true)
	case m.editField == rbacIdx:
		// Open RBAC editor sub-modal.
		m.editLifecycleHookInput = true
		m.editLifecycleHookField = 4 // special: RBAC
		ti := textinput.New()
		ti.Placeholder = "configmaps:get,list,create,delete"
		ti.CharLimit = 256
		ti.Width = 50
		ti.SetValue(lc.rbac)
		ti.Focus()
		m.editLifecycleHookTI = ti
	}
}

func (m *tuiModel) openLifecycleHookEditor(h *editLifecycleHook, idx int, isPost bool) {
	m.editLifecycleHookInput = true
	m.editLifecycleHookField = 0 // start with name
	m.editLifecycleHookIdx = idx
	m.editLifecycleHookIsPost = isPost
	ti := textinput.New()
	ti.Placeholder = "hook name"
	ti.CharLimit = 63
	ti.Width = 50
	ti.SetValue(h.name)
	ti.Focus()
	m.editLifecycleHookTI = ti
}

// currentLifecycleHook returns a pointer to the hook being edited in the sub-modal.
func (m *tuiModel) currentLifecycleHook() *editLifecycleHook {
	if m.editLifecycleHookIsPost {
		if m.editLifecycleHookIdx < len(m.editLifecycle.postRun) {
			return &m.editLifecycle.postRun[m.editLifecycleHookIdx]
		}
	} else {
		if m.editLifecycleHookIdx < len(m.editLifecycle.preRun) {
			return &m.editLifecycle.preRun[m.editLifecycleHookIdx]
		}
	}
	return nil
}

// handleLifecycleDelete removes the currently selected lifecycle hook.
func (m *tuiModel) handleLifecycleDelete() {
	lc := &m.editLifecycle
	preLen := len(lc.preRun)
	addPreIdx := preLen
	postStart := addPreIdx + 1
	postLen := len(lc.postRun)
	addPostIdx := postStart + postLen

	switch {
	case m.editField < preLen:
		lc.preRun = append(lc.preRun[:m.editField], lc.preRun[m.editField+1:]...)
		if m.editField >= len(lc.preRun) && m.editField > 0 {
			m.editField--
		}
	case m.editField >= postStart && m.editField < addPostIdx:
		idx := m.editField - postStart
		lc.postRun = append(lc.postRun[:idx], lc.postRun[idx+1:]...)
	}
}

const maxLogLines = 200

func newTUIModel(ns string) tuiModel {
	ti := textinput.New()
	ti.Placeholder = "Type / for commands or press ? for help..."
	ti.CharLimit = 256
	ti.Prompt = "❯ "
	ti.PromptStyle = tuiPromptStyle
	ti.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))

	fi := textinput.New()
	fi.Placeholder = "Type a message..."
	fi.CharLimit = 512
	fi.Prompt = "❯ "
	fi.PromptStyle = tuiPromptStyle
	fi.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))

	flt := textinput.New()
	flt.Placeholder = "Filter..."
	flt.CharLimit = 128
	flt.Prompt = "🔍 "
	flt.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFFFFF"))

	return tuiModel{
		namespace:    ns,
		connected:    k8sClient != nil,
		input:        ti,
		feedInput:    fi,
		filterInput:  flt,
		inputFocused: false,
		activeView:   viewEnsembles,
		logEntries:   []logEntry{{time: time.Now(), level: "info", text: tuiDimStyle.Render("Sympozium TUI ready — press ? for help, / to enter commands")}},
	}
}

// selectedInstanceForFeed returns the agent name that the feed pane should
// display runs for, based on the current view and selected row.
func (m tuiModel) selectedInstanceForFeed() string {
	switch m.activeView {
	case viewAgents:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.instances) {
			return m.instances[idx].Name
		}
	case viewRuns:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.runs) {
			return m.runs[idx].Spec.AgentRef
		}
	case viewChannels:
		idx := m.resolveFilteredRow()
		filtered := m.filteredChannels()
		if idx >= 0 && idx < len(filtered) {
			return filtered[idx].InstanceName
		}
	case viewPods:
		idx := m.resolveFilteredRow()
		filtered := m.filteredPods()
		if idx >= 0 && idx < len(filtered) {
			return filtered[idx].Instance
		}
	}
	// Fallback: first instance
	if len(m.instances) > 0 {
		return m.instances[0].Name
	}
	return ""
}

// runsForInstance returns runs filtered by instance name, oldest-first.
func (m tuiModel) runsForInstance(instName string) []sympoziumv1alpha1.AgentRun {
	if instName == "" {
		return nil
	}
	var filtered []sympoziumv1alpha1.AgentRun
	// m.runs is sorted newest-first; iterate in reverse for oldest-first.
	for i := len(m.runs) - 1; i >= 0; i-- {
		if m.runs[i].Spec.AgentRef == instName {
			filtered = append(filtered, m.runs[i])
		}
	}
	return filtered
}

// buildConversationContext assembles the chat history from prior completed runs
// for the given instance, formatted as a conversation transcript.
func (m tuiModel) buildConversationContext(instName string) string {
	runs := m.runsForInstance(instName)
	if len(runs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("Previous conversation:\n")
	for _, r := range runs {
		phase := string(r.Status.Phase)
		sb.WriteString(fmt.Sprintf("User: %s\n", r.Spec.Task.GetPrompt()))
		if (phase == "Succeeded" || phase == "Completed") && r.Status.Result != "" {
			sb.WriteString(fmt.Sprintf("Assistant: %s\n", r.Status.Result))
		} else if phase == "Failed" {
			sb.WriteString(fmt.Sprintf("Assistant: [error: %s]\n", r.Status.Error))
		} else if phase == "Skipped" {
			reason := r.Status.Result
			if reason == "" {
				reason = "no work to do"
			}
			sb.WriteString(fmt.Sprintf("Assistant: [skipped: %s]\n", reason))
		} else {
			sb.WriteString("Assistant: [pending]\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, refreshDataCmd(m.namespace), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func refreshDataCmd(ns string) tea.Cmd {
	return func() tea.Msg {
		if k8sClient == nil {
			return dataRefreshMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		// Scope every list to the active namespace so the TUI only ever
		// shows resources the user can actually act on. Listing across all
		// namespaces surfaces e.g. Ensembles that don't exist in `ns`, which
		// then fail with a misleading "not found" error when selected.
		inNS := client.InNamespace(ns)

		var (
			inst      sympoziumv1alpha1.AgentList
			runs      sympoziumv1alpha1.AgentRunList
			pols      sympoziumv1alpha1.SympoziumPolicyList
			skls      sympoziumv1alpha1.SkillPackList
			scheds    sympoziumv1alpha1.SympoziumScheduleList
			packs     sympoziumv1alpha1.EnsembleList
			podList   corev1.PodList
			gwConfigs sympoziumv1alpha1.SympoziumConfigList
		)

		var mu sync.Mutex
		var errs []string
		addErr := func(e string) {
			mu.Lock()
			errs = append(errs, e)
			mu.Unlock()
		}

		// Fetch all resources in parallel.
		var wg sync.WaitGroup
		wg.Add(8)

		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &inst, inNS); err != nil {
				addErr(fmt.Sprintf("instances: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &runs, inNS); err != nil {
				addErr(fmt.Sprintf("runs: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &pols, inNS); err != nil {
				addErr(fmt.Sprintf("policies: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &skls, inNS); err != nil {
				addErr(fmt.Sprintf("skills: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &scheds, inNS); err != nil {
				addErr(fmt.Sprintf("schedules: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &podList, inNS, client.MatchingLabels{"app.kubernetes.io/managed-by": "sympozium"}); err != nil {
				addErr(fmt.Sprintf("pods: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &packs, inNS); err != nil {
				addErr(fmt.Sprintf("ensembles: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &gwConfigs, inNS); err != nil {
				addErr(fmt.Sprintf("gatewayconfig: %v", err))
			}
		}()

		wg.Wait()

		// Build the message from fetched data.
		var msg dataRefreshMsg

		if len(inst.Items) > 0 || !containsPrefix(errs, "instances:") {
			msg.instances = &inst.Items
		}
		if !containsPrefix(errs, "runs:") {
			sort.Slice(runs.Items, func(i, j int) bool {
				return runs.Items[i].CreationTimestamp.After(runs.Items[j].CreationTimestamp.Time)
			})
			msg.runs = &runs.Items
		}
		if !containsPrefix(errs, "policies:") {
			msg.policies = &pols.Items
		}
		if !containsPrefix(errs, "skills:") {
			msg.skills = &skls.Items
		}
		if !containsPrefix(errs, "schedules:") {
			msg.schedules = &scheds.Items
		}
		if !containsPrefix(errs, "ensembles:") {
			msg.ensembles = &packs.Items
		}
		if !containsPrefix(errs, "gatewayconfig:") && len(gwConfigs.Items) > 0 {
			msg.gatewayConfig = &gwConfigs.Items[0]
		}

		// Build channel rows from instances.
		var chRows []channelRow
		for _, i := range inst.Items {
			statusMap := make(map[string]sympoziumv1alpha1.ChannelStatus)
			for _, cs := range i.Status.Channels {
				statusMap[cs.Type] = cs
			}
			for _, ch := range i.Spec.Channels {
				row := channelRow{
					InstanceName: i.Name,
					Type:         ch.Type,
					SecretRef:    ch.ConfigRef.Secret,
					Status:       "Unknown",
				}
				if cs, ok := statusMap[ch.Type]; ok {
					row.Status = cs.Status
					row.Message = cs.Message
					if cs.LastHealthCheck != nil {
						row.LastCheck = shortDuration(time.Since(cs.LastHealthCheck.Time))
					}
				}
				chRows = append(chRows, row)
			}
		}

		// Build pod rows from actual pods labelled for sympozium.
		var podRows []podRow
		if !containsPrefix(errs, "pods:") {
			for _, p := range podList.Items {
				instName := p.Labels["sympozium.ai/instance"]
				var restarts int32
				for _, cs := range p.Status.ContainerStatuses {
					restarts += cs.RestartCount
				}
				podRows = append(podRows, podRow{
					Name:     p.Name,
					Instance: instName,
					Phase:    string(p.Status.Phase),
					Node:     p.Spec.NodeName,
					IP:       p.Status.PodIP,
					Age:      shortDuration(time.Since(p.CreationTimestamp.Time)),
					Restarts: restarts,
				})
			}
		}
		// Also include pods from AgentRun status.
		for _, r := range runs.Items {
			if r.Status.PodName == "" {
				continue
			}
			var found bool
			for _, pr := range podRows {
				if pr.Name == r.Status.PodName {
					found = true
					break
				}
			}
			if !found {
				var pod corev1.Pod
				if err := k8sClient.Get(ctx, types.NamespacedName{Name: r.Status.PodName, Namespace: r.Namespace}, &pod); err == nil {
					var restarts int32
					for _, cs := range pod.Status.ContainerStatuses {
						restarts += cs.RestartCount
					}
					podRows = append(podRows, podRow{
						Name:     pod.Name,
						Instance: r.Spec.AgentRef,
						Phase:    string(pod.Status.Phase),
						Node:     pod.Spec.NodeName,
						IP:       pod.Status.PodIP,
						Age:      shortDuration(time.Since(pod.CreationTimestamp.Time)),
						Restarts: restarts,
					})
				}
			}
		}

		msg.channels = &chRows
		msg.pods = &podRows
		if len(errs) > 0 {
			msg.fetchErr = strings.Join(errs, "; ")
		}
		return msg
	}
}

// containsPrefix checks if any string in the slice starts with the given prefix.
func containsPrefix(ss []string, prefix string) bool {
	for _, s := range ss {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// ── Update ───────────────────────────────────────────────────────────────────

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var tiCmd tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		if m.confirmDelete {
			switch msg.String() {
			case "y", "Y":
				fn := m.deleteFunc
				m.confirmDelete = false
				m.deleteFunc = nil
				return m, m.asyncCmd(fn)
			default:
				m.confirmDelete = false
				m.deleteFunc = nil
				m.addLog(tuiDimStyle.Render("Delete cancelled"))
				return m, nil
			}
		}

		if m.showModal {
			m.showModal = false
			return m, nil
		}

		if m.showEditModal {
			// Task sub-modal text input — intercept keys first.
			if m.editTaskInput {
				switch msg.Type {
				case tea.KeyEsc:
					m.editTaskInput = false
					return m, nil
				case tea.KeyEnter:
					m.editHeartbeat.task = m.editTaskTI.Value()
					m.editTaskInput = false
					return m, nil
				default:
					m.editTaskTI, tiCmd = m.editTaskTI.Update(msg)
					return m, tiCmd
				}
			}

			// GitHub-gitops repo sub-modal — intercept keys first.
			if m.editSkillGithubInput {
				switch msg.Type {
				case tea.KeyEsc:
					// Cancel — revert the toggle
					m.editSkills[m.editSkillGithubIdx].enabled = false
					m.editSkillGithubInput = false
					return m, nil
				case tea.KeyEnter:
					repo := m.editSkillGithubTI.Value()
					idx := m.editSkillGithubIdx
					if repo != "" {
						if m.editSkills[idx].params == nil {
							m.editSkills[idx].params = make(map[string]string)
						}
						m.editSkills[idx].params["repo"] = repo
						m.editSkillGithubInput = false
						// Automatically start the GitHub auth flow if the token secret doesn't exist.
						m.githubAuthActive = true
						m.githubAuthStatus = "checking"
						return m, checkAndStartGithubAuthCmd()
					}
					// No repo entered — revert toggle
					m.editSkills[idx].enabled = false
					m.editSkillGithubInput = false
					return m, nil
				default:
					m.editSkillGithubTI, tiCmd = m.editSkillGithubTI.Update(msg)
					return m, tiCmd
				}
			}

			// Lifecycle hook sub-modal — intercept keys first.
			if m.editLifecycleHookInput {
				switch msg.Type {
				case tea.KeyEsc:
					m.editLifecycleHookInput = false
					return m, nil
				case tea.KeyEnter:
					val := m.editLifecycleHookTI.Value()
					if m.editLifecycleHookField == 4 {
						// RBAC editing.
						m.editLifecycle.rbac = val
						m.editLifecycleHookInput = false
						return m, nil
					}
					// Hook field editing — save current field and advance to next.
					hook := m.currentLifecycleHook()
					if hook == nil {
						m.editLifecycleHookInput = false
						return m, nil
					}
					switch m.editLifecycleHookField {
					case 0:
						hook.name = val
						m.editLifecycleHookField = 1
						ti := textinput.New()
						ti.Placeholder = "container image (e.g. curlimages/curl:latest)"
						ti.CharLimit = 256
						ti.Width = 50
						ti.SetValue(hook.image)
						ti.Focus()
						m.editLifecycleHookTI = ti
					case 1:
						hook.image = val
						m.editLifecycleHookField = 2
						ti := textinput.New()
						ti.Placeholder = "command (e.g. sh -c 'curl http://...')"
						ti.CharLimit = 512
						ti.Width = 50
						ti.SetValue(hook.command)
						ti.Focus()
						m.editLifecycleHookTI = ti
					case 2:
						hook.command = val
						m.editLifecycleHookField = 3
						ti := textinput.New()
						ti.Placeholder = "KEY=VALUE (one per line)"
						ti.CharLimit = 512
						ti.Width = 50
						ti.SetValue(hook.envVars)
						ti.Focus()
						m.editLifecycleHookTI = ti
					case 3:
						hook.envVars = val
						m.editLifecycleHookInput = false
					}
					return m, nil
				default:
					m.editLifecycleHookTI, tiCmd = m.editLifecycleHookTI.Update(msg)
					return m, tiCmd
				}
			}

			// Channel token sub-modal text input — intercept keys first.
			if m.editChannelTokenInput {
				switch msg.Type {
				case tea.KeyEsc:
					// Cancel — revert the toggle
					m.editChannels[m.editChannelTokenIdx].enabled = false
					m.editChannelTokenInput = false
					return m, nil
				case tea.KeyEnter:
					token := m.editChannelTokenTI.Value()
					idx := m.editChannelTokenIdx
					if token != "" {
						secretName := fmt.Sprintf("%s-%s-secret", m.editInstanceName, m.editChannels[idx].chType)
						m.editChannels[idx].secretRef = secretName
						if m.editChannelNewTokens == nil {
							m.editChannelNewTokens = make(map[int]string)
						}
						m.editChannelNewTokens[idx] = token
					} else {
						// No token entered — revert toggle
						m.editChannels[idx].enabled = false
					}
					m.editChannelTokenInput = false
					return m, nil
				default:
					m.editChannelTokenTI, tiCmd = m.editChannelTokenTI.Update(msg)
					return m, tiCmd
				}
			}

			switch msg.String() {
			case "esc":
				m.showEditModal = false
				m.editEnsembleName = ""
				m.addLog(tuiDimStyle.Render("Edit cancelled"))
				return m, nil
			case "tab":
				if m.editEnsembleName != "" {
					return m, nil // no tabs in ensemble mode
				}
				m.editTab = (m.editTab + 1) % len(editTabNames)
				m.editField = 0
				return m, nil
			case "shift+tab":
				if m.editEnsembleName != "" {
					return m, nil // no tabs in ensemble mode
				}
				m.editTab = (m.editTab + len(editTabNames) - 1) % len(editTabNames)
				m.editField = 0
				return m, nil
			case "j", "down":
				max := editMemoryFieldCount
				if m.editEnsembleName != "" {
					max = 1 + len(m.editEnsembleAgents) // field 0 = heartbeat, rest = agent toggles
				} else if m.editTab == 1 {
					max = editHeartbeatFieldCount
				} else if m.editTab == 2 {
					max = len(m.editSkills)
				} else if m.editTab == 3 {
					max = len(m.editChannels)
				} else if m.editTab == 4 {
					max = editWebEndpointFieldCount
				} else if m.editTab == 5 {
					max = m.editLifecycle.fieldCount()
				}
				if m.editField < max-1 {
					m.editField++
				}
				return m, nil
			case "k", "up":
				if m.editField > 0 {
					m.editField--
				}
				return m, nil
			case " ":
				// Toggle boolean fields
				if m.editEnsembleName != "" {
					if m.editField == 0 {
						// Cycle heartbeat forward on space
						m.editEnsembleHeartbeatIdx = (m.editEnsembleHeartbeatIdx + 1) % len(ensembleHeartbeatOptions)
					} else {
						idx := m.editField - 1 // agent toggles start at field 1
						if idx >= 0 && idx < len(m.editEnsembleAgents) {
							m.editEnsembleAgents[idx].enabled = !m.editEnsembleAgents[idx].enabled
						}
					}
				} else if m.editTab == 0 {
					if m.editField == 0 {
						m.editMemory.enabled = !m.editMemory.enabled
					}
				} else if m.editTab == 1 {
					switch m.editField {
					case 4:
						m.editHeartbeat.includeMemory = !m.editHeartbeat.includeMemory
					case 5:
						m.editHeartbeat.suspend = !m.editHeartbeat.suspend
					}
				} else if m.editTab == 2 {
					if m.editField >= 0 && m.editField < len(m.editSkills) {
						sk := &m.editSkills[m.editField]
						if sk.name == "memory" {
							// memory is mandatory — cannot be toggled off
						} else {
							sk.enabled = !sk.enabled
							if sk.enabled && sk.name == "github-gitops" {
								m.editSkillGithubInput = true
								m.editSkillGithubIdx = m.editField
								ti := textinput.New()
								ti.Placeholder = "owner/repo (e.g. myorg/platform)"
								ti.CharLimit = 128
								ti.Width = 50
								if repo, ok := sk.params["repo"]; ok {
									ti.SetValue(repo)
								}
								ti.Focus()
								m.editSkillGithubTI = ti
							}
						}
					}
				} else if m.editTab == 3 {
					if m.editField >= 0 && m.editField < len(m.editChannels) {
						ch := &m.editChannels[m.editField]
						if ch.enabled {
							ch.enabled = false
						} else {
							ch.enabled = true
							if ch.secretRef == "" && ch.tokenKey != "" {
								m.editChannelTokenInput = true
								m.editChannelTokenIdx = m.editField
								m.editChannelTokenTI = textinput.New()
								m.editChannelTokenTI.Placeholder = fmt.Sprintf("Enter %s token...", ch.chType)
								m.editChannelTokenTI.CharLimit = 256
								m.editChannelTokenTI.Width = 50
								m.editChannelTokenTI.EchoMode = textinput.EchoPassword
								m.editChannelTokenTI.Focus()
							}
						}
					}
				} else if m.editTab == 4 {
					if m.editField == 0 {
						m.editWebEndpoint.enabled = !m.editWebEndpoint.enabled
					}
				} else if m.editTab == 5 {
					m.handleLifecycleEnter()
				}
				return m, nil
			case "d":
				// Delete lifecycle hook.
				if m.editTab == 5 {
					m.handleLifecycleDelete()
				}
				return m, nil
			case "left", "h":
				// Cycle enum fields backward
				if m.editEnsembleName != "" && m.editField == 0 {
					m.editEnsembleHeartbeatIdx = (m.editEnsembleHeartbeatIdx + len(ensembleHeartbeatOptions) - 1) % len(ensembleHeartbeatOptions)
				} else if m.editTab == 1 {
					switch m.editField {
					case 2:
						m.editHeartbeat.schedType = (m.editHeartbeat.schedType + len(editScheduleTypes) - 1) % len(editScheduleTypes)
					case 3:
						m.editHeartbeat.concurrencyPolicy = (m.editHeartbeat.concurrencyPolicy + len(editConcurrencyPolicies) - 1) % len(editConcurrencyPolicies)
					}
				}
				return m, nil
			case "right", "l":
				// Cycle enum fields forward
				if m.editEnsembleName != "" && m.editField == 0 {
					m.editEnsembleHeartbeatIdx = (m.editEnsembleHeartbeatIdx + 1) % len(ensembleHeartbeatOptions)
				} else if m.editTab == 1 {
					switch m.editField {
					case 2:
						m.editHeartbeat.schedType = (m.editHeartbeat.schedType + 1) % len(editScheduleTypes)
					case 3:
						m.editHeartbeat.concurrencyPolicy = (m.editHeartbeat.concurrencyPolicy + 1) % len(editConcurrencyPolicies)
					}
				}
				return m, nil
			case "backspace":
				// Delete last char from text fields
				if m.editTab == 0 {
					switch m.editField {
					case 1:
						if len(m.editMemory.maxSizeKB) > 0 {
							m.editMemory.maxSizeKB = m.editMemory.maxSizeKB[:len(m.editMemory.maxSizeKB)-1]
						}
					case 2:
						if len(m.editMemory.systemPrompt) > 0 {
							m.editMemory.systemPrompt = m.editMemory.systemPrompt[:len(m.editMemory.systemPrompt)-1]
						}
					}
				} else if m.editTab == 4 {
					switch m.editField {
					case 1:
						if len(m.editWebEndpoint.hostname) > 0 {
							m.editWebEndpoint.hostname = m.editWebEndpoint.hostname[:len(m.editWebEndpoint.hostname)-1]
						}
					case 2:
						if len(m.editWebEndpoint.rateLimit) > 0 {
							m.editWebEndpoint.rateLimit = m.editWebEndpoint.rateLimit[:len(m.editWebEndpoint.rateLimit)-1]
						}
					}
				} else {
					switch m.editField {
					case 0:
						if len(m.editHeartbeat.schedule) > 0 {
							m.editHeartbeat.schedule = m.editHeartbeat.schedule[:len(m.editHeartbeat.schedule)-1]
						}
					}
				}
				return m, nil
			case "enter":
				// Toggle bools, open task sub-modal, or no-op on text fields.
				if m.editEnsembleName != "" {
					if m.editField == 0 {
						// Cycle heartbeat forward on enter
						m.editEnsembleHeartbeatIdx = (m.editEnsembleHeartbeatIdx + 1) % len(ensembleHeartbeatOptions)
					} else {
						idx := m.editField - 1
						if idx >= 0 && idx < len(m.editEnsembleAgents) {
							m.editEnsembleAgents[idx].enabled = !m.editEnsembleAgents[idx].enabled
						}
					}
				} else if m.editTab == 0 {
					if m.editField == 0 {
						m.editMemory.enabled = !m.editMemory.enabled
					}
				} else if m.editTab == 1 {
					switch m.editField {
					case 1:
						// Open task sub-modal
						m.editTaskInput = true
						m.editTaskTI = textinput.New()
						m.editTaskTI.Placeholder = "Enter task description..."
						m.editTaskTI.CharLimit = 512
						m.editTaskTI.Width = 50
						m.editTaskTI.SetValue(m.editHeartbeat.task)
						m.editTaskTI.Focus()
					case 4:
						m.editHeartbeat.includeMemory = !m.editHeartbeat.includeMemory
					case 5:
						m.editHeartbeat.suspend = !m.editHeartbeat.suspend
					}
				} else if m.editTab == 2 {
					if m.editField >= 0 && m.editField < len(m.editSkills) {
						sk := &m.editSkills[m.editField]
						if sk.name == "memory" {
							// memory is mandatory — cannot be toggled off
						} else {
							sk.enabled = !sk.enabled
							if sk.enabled && sk.name == "github-gitops" {
								m.editSkillGithubInput = true
								m.editSkillGithubIdx = m.editField
								ti := textinput.New()
								ti.Placeholder = "owner/repo (e.g. myorg/platform)"
								ti.CharLimit = 128
								ti.Width = 50
								if repo, ok := sk.params["repo"]; ok {
									ti.SetValue(repo)
								}
								ti.Focus()
								m.editSkillGithubTI = ti
							}
						}
					}
				} else if m.editTab == 3 {
					if m.editField >= 0 && m.editField < len(m.editChannels) {
						ch := &m.editChannels[m.editField]
						if ch.enabled {
							// Toggling OFF — just disable
							ch.enabled = false
						} else {
							// Toggling ON — prompt for token if needed
							ch.enabled = true
							if ch.secretRef == "" && ch.tokenKey != "" {
								m.editChannelTokenInput = true
								m.editChannelTokenIdx = m.editField
								m.editChannelTokenTI = textinput.New()
								m.editChannelTokenTI.Placeholder = fmt.Sprintf("Enter %s token...", ch.chType)
								m.editChannelTokenTI.CharLimit = 256
								m.editChannelTokenTI.Width = 50
								m.editChannelTokenTI.EchoMode = textinput.EchoPassword
								m.editChannelTokenTI.Focus()
							}
						}
					}
				} else if m.editTab == 4 {
					if m.editField == 0 {
						m.editWebEndpoint.enabled = !m.editWebEndpoint.enabled
					}
				} else if m.editTab == 5 {
					m.handleLifecycleEnter()
				}
				return m, nil
			case "a":
				// Start (or restart) GitHub OAuth device flow for github-gitops skill
				if m.editTab == 2 {
					for _, sk := range m.editSkills {
						if sk.enabled && sk.name == "github-gitops" {
							m.githubAuthActive = true
							m.githubAuthStatus = "checking"
							return m, checkAndStartGithubAuthCmd()
						}
					}
				}
				return m, nil
			case "ctrl+s":
				// Apply changes
				m.showEditModal = false
				editPackName := m.editEnsembleName
				m.editEnsembleName = ""
				if editPackName != "" {
					return m, m.applyEnsembleEdit(editPackName)
				}
				return m, m.applyEditModal()
			default:
				// Type into text fields
				ch := msg.String()
				if len(ch) == 1 {
					if m.editTab == 0 {
						switch m.editField {
						case 1:
							// Only allow digits for maxSizeKB
							if ch >= "0" && ch <= "9" {
								m.editMemory.maxSizeKB += ch
							}
						case 2:
							m.editMemory.systemPrompt += ch
						}
					} else if m.editTab == 4 {
						switch m.editField {
						case 1:
							m.editWebEndpoint.hostname += ch
						case 2:
							if ch >= "0" && ch <= "9" {
								m.editWebEndpoint.rateLimit += ch
							}
						}
					} else {
						switch m.editField {
						case 0:
							m.editHeartbeat.schedule += ch
						}
					}
				}
				return m, nil
			}
		}

		if m.showGatewayEditModal {
			switch msg.String() {
			case "esc":
				m.showGatewayEditModal = false
				m.addLog(tuiDimStyle.Render("Gateway edit cancelled"))
				return m, nil
			case "j", "down":
				if m.editField < editGatewayFieldCount-1 {
					m.editField++
				}
				return m, nil
			case "k", "up":
				if m.editField > 0 {
					m.editField--
				}
				return m, nil
			case " ", "enter":
				switch m.editField {
				case 0:
					m.editGateway.enabled = !m.editGateway.enabled
				case 4:
					m.editGateway.tlsEnabled = !m.editGateway.tlsEnabled
				}
				return m, nil
			case "backspace":
				switch m.editField {
				case 1:
					if len(m.editGateway.baseDomain) > 0 {
						m.editGateway.baseDomain = m.editGateway.baseDomain[:len(m.editGateway.baseDomain)-1]
					}
				case 2:
					if len(m.editGateway.gatewayClassName) > 0 {
						m.editGateway.gatewayClassName = m.editGateway.gatewayClassName[:len(m.editGateway.gatewayClassName)-1]
					}
				case 3:
					if len(m.editGateway.gatewayName) > 0 {
						m.editGateway.gatewayName = m.editGateway.gatewayName[:len(m.editGateway.gatewayName)-1]
					}
				case 5:
					if len(m.editGateway.certManagerClusterIssuer) > 0 {
						m.editGateway.certManagerClusterIssuer = m.editGateway.certManagerClusterIssuer[:len(m.editGateway.certManagerClusterIssuer)-1]
					}
				case 6:
					if len(m.editGateway.tlsSecretName) > 0 {
						m.editGateway.tlsSecretName = m.editGateway.tlsSecretName[:len(m.editGateway.tlsSecretName)-1]
					}
				}
				return m, nil
			case "ctrl+s":
				m.showGatewayEditModal = false
				return m, m.applyGatewayEdit()
			default:
				ch := msg.String()
				if len(ch) == 1 {
					switch m.editField {
					case 1:
						m.editGateway.baseDomain += ch
					case 2:
						m.editGateway.gatewayClassName += ch
					case 3:
						m.editGateway.gatewayName += ch
					case 5:
						m.editGateway.certManagerClusterIssuer += ch
					case 6:
						m.editGateway.tlsSecretName += ch
					}
				}
				return m, nil
			}
		}

		if m.detailPane == paneFullscreen {
			// Chat input mode inside feed
			if m.feedInputFocused {
				switch msg.Type {
				case tea.KeyCtrlC:
					m.quitting = true
					return m, tea.Quit
				case tea.KeyEsc:
					m.feedInputFocused = false
					m.feedInput.Blur()
					m.feedInput.SetValue("")
					return m, nil
				case tea.KeyEnter:
					text := strings.TrimSpace(m.feedInput.Value())
					m.feedInput.SetValue("")
					if text == "" {
						return m, nil
					}
					inst := m.selectedInstanceForFeed()
					if inst == "" {
						m.addLog(tuiErrorStyle.Render("No agent selected"))
						return m, nil
					}
					// Build context from prior runs and create a new chat run
					context := m.buildConversationContext(inst)
					ns := m.namespace
					m.feedScrollOffset = 0 // pin to bottom for new message
					return m, m.asyncCmd(func() (string, error) {
						return tuiCreateChatRun(ns, inst, text, context)
					})
				}
				var fiCmd tea.Cmd
				m.feedInput, fiCmd = m.feedInput.Update(msg)
				return m, fiCmd
			}
			// Not typing — global feed keys
			switch msg.String() {
			case "esc", "q":
				m.detailPane = panePanel
				m.feedInputFocused = false
				m.feedInput.Blur()
				m.feedInput.SetValue("")
				return m, nil
			case "F":
				m.detailPane = panePanel
				m.feedInputFocused = false
				m.feedInput.Blur()
				m.feedInput.SetValue("")
				return m, nil
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit
			case "up", "k":
				m.feedScrollOffset++
				return m, nil
			case "down", "j":
				if m.feedScrollOffset > 0 {
					m.feedScrollOffset--
				}
				return m, nil
			case "pgup":
				m.feedScrollOffset += 10
				return m, nil
			case "pgdown":
				m.feedScrollOffset -= 10
				if m.feedScrollOffset < 0 {
					m.feedScrollOffset = 0
				}
				return m, nil
			case "G":
				m.feedScrollOffset = 0
				return m, nil
			case "g":
				// scroll to top — set a large offset, clamped during render
				m.feedScrollOffset = 999999
				return m, nil
			case "i", "/", "enter":
				// Enter chat input mode
				inst := m.selectedInstanceForFeed()
				if inst != "" {
					m.feedInputFocused = true
					m.feedInput.Focus()
					m.feedInput.Placeholder = fmt.Sprintf("Chat with %s...", inst)
					return m, textinput.Blink
				}
				return m, nil
			}
			return m, nil
		}

		// When input is focused, handle input-specific keys first.
		if m.inputFocused {
			// Wizard mode: route input to wizard.
			if m.wizard.active {
				switch msg.Type {
				case tea.KeyCtrlC:
					m.quitting = true
					return m, tea.Quit
				case tea.KeyEsc:
					// During WhatsApp QR step, Esc skips pairing but keeps results
					if m.wizard.step == wizStepWhatsAppQR {
						m.wizard.step = wizStepDone
						m.wizard.resultMsgs = append(m.wizard.resultMsgs,
							tuiDimStyle.Render("⚠ WhatsApp QR pairing skipped — scan later via: kubectl logs -l sympozium.ai/channel=whatsapp,sympozium.ai/instance="+m.wizard.instanceName+" -n "+m.wizard.targetNamespace))
						m.input.Placeholder = "Press Enter to return"
						return m, nil
					}
					m.wizard.reset()
					m.inputFocused = false
					m.input.Blur()
					m.input.SetValue("")
					m.input.Placeholder = "Type / for commands or press ? for help..."
					m.suggestions = nil
					m.addLog(tuiDimStyle.Render("Wizard cancelled"))
					return m, nil
				case tea.KeyUp:
					if m.wizard.step == wizStepModel && m.wizard.scrollOffset > 0 {
						m.wizard.scrollOffset--
						return m, nil
					}
					if m.wizard.personaMode && m.wizard.packDetailScroll > 0 {
						m.wizard.packDetailScroll--
						return m, nil
					}
				case tea.KeyDown:
					if m.wizard.step == wizStepModel {
						m.wizard.scrollOffset++
						return m, nil
					}
					if m.wizard.personaMode {
						m.wizard.packDetailScroll++
						return m, nil
					}
				case tea.KeyPgUp:
					if m.wizard.step == wizStepModel && m.wizard.scrollOffset > 0 {
						m.wizard.scrollOffset -= 5
						if m.wizard.scrollOffset < 0 {
							m.wizard.scrollOffset = 0
						}
						return m, nil
					}
					if m.wizard.personaMode && m.wizard.packDetailScroll > 0 {
						m.wizard.packDetailScroll -= 5
						if m.wizard.packDetailScroll < 0 {
							m.wizard.packDetailScroll = 0
						}
						return m, nil
					}
				case tea.KeyPgDown:
					if m.wizard.step == wizStepModel {
						m.wizard.scrollOffset += 5
						return m, nil
					}
					if m.wizard.personaMode {
						m.wizard.packDetailScroll += 5
						return m, nil
					}
				case tea.KeyEnter:
					val := strings.TrimSpace(m.input.Value())
					m.input.SetValue("")
					return m.advanceWizard(val)
				}
				m.input, tiCmd = m.input.Update(msg)
				return m, tiCmd
			}

			switch msg.Type {
			case tea.KeyCtrlC:
				m.quitting = true
				return m, tea.Quit
			case tea.KeyEsc:
				if len(m.suggestions) > 0 {
					m.suggestions = nil
					m.suggestIdx = 0
					return m, nil
				}
				m.inputFocused = false
				m.input.Blur()
				m.input.SetValue("")
				m.suggestions = nil
				return m, nil
			case tea.KeyTab:
				if len(m.suggestions) > 0 {
					m.acceptSuggestion()
					return m, nil
				}
			case tea.KeyUp:
				if len(m.suggestions) > 0 {
					m.suggestIdx--
					if m.suggestIdx < 0 {
						m.suggestIdx = len(m.suggestions) - 1
					}
					return m, nil
				}
			case tea.KeyDown:
				if len(m.suggestions) > 0 {
					m.suggestIdx++
					if m.suggestIdx >= len(m.suggestions) {
						m.suggestIdx = 0
					}
					return m, nil
				}
			case tea.KeyEnter:
				if len(m.suggestions) > 0 {
					m.acceptSuggestion()
					return m, nil
				}
				input := strings.TrimSpace(m.input.Value())
				if input == "" {
					break
				}
				m.input.SetValue("")
				m.suggestions = nil
				m.suggestIdx = 0
				if strings.HasPrefix(input, "/") {
					return m.handleCommand(input)
				}
				m.addLog(tuiDimStyle.Render("Hint: type /help or press ?"))
				return m, nil
			}

			m.input, tiCmd = m.input.Update(msg)
			currentInput := m.input.Value()
			if currentInput != m.lastInput {
				m.lastInput = currentInput
				cmd := m.updateSuggestions(currentInput)
				if cmd != nil {
					return m, tea.Batch(tiCmd, cmd)
				}
			}
			return m, tiCmd
		}

		// Filter mode: keystrokes go to filter input.
		if m.filterMode {
			switch msg.Type {
			case tea.KeyEsc:
				m.filterMode = false
				m.filterText = ""
				m.filterInput.Blur()
				m.filterInput.SetValue("")
				m.filteredIdx = nil
				m.selectedRow = 0
				m.tableScroll = 0
				return m, nil
			case tea.KeyEnter:
				// Confirm filter, return focus to table.
				m.filterMode = false
				m.filterInput.Blur()
				return m, nil
			case tea.KeyCtrlC:
				m.quitting = true
				return m, tea.Quit
			}
			var fiCmd tea.Cmd
			m.filterInput, fiCmd = m.filterInput.Update(msg)
			newVal := m.filterInput.Value()
			if newVal != m.filterText {
				m.filterText = newVal
				m.selectedRow = 0
				m.tableScroll = 0
				m.rebuildFilteredIdx()
			}
			return m, fiCmd
		}

		// Table / global key handling (input not focused).
		// Handle arrow keys via Type first (more reliable across terminals).
		switch msg.Type {
		case tea.KeyDown:
			maxRow := m.activeViewCount() - 1
			if maxRow < 0 {
				maxRow = 0
			}
			if m.selectedRow < maxRow {
				m.selectedRow++
			}
			return m, nil
		case tea.KeyUp:
			if m.selectedRow > 0 {
				m.selectedRow--
			}
			return m, nil
		}
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "ctrl+f":
			m.filterMode = true
			m.filterInput.SetValue(m.filterText)
			m.filterInput.Focus()
			m.filterInput.CursorEnd()
			return m, textinput.Blink
		case "esc":
			// Clear active filter first.
			if m.filterText != "" {
				m.filterText = ""
				m.filteredIdx = nil
				m.selectedRow = 0
				m.tableScroll = 0
				return m, nil
			}
			// Go back: clear drill-in filter or return to Agents view.
			if m.drillInstance != "" {
				m.drillInstance = ""
				m.activeView = viewAgents
				m.selectedRow = 0
				m.tableScroll = 0
				return m, nil
			}
			if m.activeView != viewAgents {
				m.activeView = viewAgents
				m.selectedRow = 0
				m.tableScroll = 0
				return m, nil
			}
			return m, nil
		case "?":
			m.showModal = true
			return m, nil
		case "/":
			m.inputFocused = true
			m.input.Focus()
			m.input.SetValue("/")
			m.input.CursorEnd()
			m.lastInput = "/"
			m.updateSuggestions("/")
			return m, textinput.Blink
		case "1":
			m.activeView = viewEnsembles
			m.selectedRow = 0
			m.tableScroll = 0
			return m, nil
		case "2":
			m.activeView = viewAgents
			m.selectedRow = 0
			m.tableScroll = 0
			return m, nil
		case "3":
			m.activeView = viewRuns
			m.selectedRow = 0
			m.tableScroll = 0
			return m, nil
		case "4":
			m.activeView = viewPolicies
			m.selectedRow = 0
			m.tableScroll = 0
			return m, nil
		case "5":
			m.activeView = viewSkills
			m.selectedRow = 0
			m.tableScroll = 0
			return m, nil
		case "6":
			m.activeView = viewChannels
			m.selectedRow = 0
			m.tableScroll = 0
			m.drillInstance = ""
			return m, nil
		case "7":
			m.activeView = viewSchedules
			m.selectedRow = 0
			m.tableScroll = 0
			return m, nil
		case "8":
			m.activeView = viewGateway
			m.selectedRow = 0
			m.tableScroll = 0
			return m, nil
		case "9":
			m.activeView = viewPods
			m.selectedRow = 0
			m.tableScroll = 0
			m.drillInstance = ""
			return m, nil
		case "tab":
			// Cycle forward through views.
			next := int(m.activeView) + 1
			if next >= len(viewNames) {
				next = 0
			}
			m.activeView = tuiViewKind(next)
			m.selectedRow = 0
			m.tableScroll = 0
			m.clearFilter()
			if m.activeView != viewChannels && m.activeView != viewPods {
				m.drillInstance = ""
			}
			return m, nil
		case "shift+tab":
			// Cycle backward through views.
			prev := int(m.activeView) - 1
			if prev < 0 {
				prev = len(viewNames) - 1
			}
			m.activeView = tuiViewKind(prev)
			m.selectedRow = 0
			m.tableScroll = 0
			m.clearFilter()
			if m.activeView != viewChannels && m.activeView != viewPods {
				m.drillInstance = ""
			}
			return m, nil
		case "right":
			// Cycle forward through views (arrow key).
			next := int(m.activeView) + 1
			if next >= len(viewNames) {
				next = 0
			}
			m.activeView = tuiViewKind(next)
			m.selectedRow = 0
			m.tableScroll = 0
			m.clearFilter()
			if m.activeView != viewChannels && m.activeView != viewPods {
				m.drillInstance = ""
			}
			return m, nil
		case "left":
			// Cycle backward through views (arrow key).
			prev := int(m.activeView) - 1
			if prev < 0 {
				prev = len(viewNames) - 1
			}
			m.activeView = tuiViewKind(prev)
			m.selectedRow = 0
			m.tableScroll = 0
			m.clearFilter()
			if m.activeView != viewChannels && m.activeView != viewPods {
				m.drillInstance = ""
			}
			return m, nil
		case "j", "down":
			maxRow := m.activeViewCount() - 1
			if maxRow < 0 {
				maxRow = 0
			}
			if m.selectedRow < maxRow {
				m.selectedRow++
			}
			return m, nil
		case "k", "up":
			if m.selectedRow > 0 {
				m.selectedRow--
			}
			return m, nil
		case "enter":
			// Show detail for selected row.
			return m.handleRowAction()
		case "l":
			// Show logs for selected pod/resource (like kubectl logs).
			return m.handleRowLogs()
		case "d":
			// Describe selected resource (like kubectl describe).
			return m.handleRowDescribe()
		case "x":
			// Delete selected resource (with confirmation).
			return m.handleRowDelete()
		case "e":
			// Edit selected resource (memory/heartbeat config).
			return m.handleRowEdit()
		case "R":
			// Create a new run on the selected instance.
			return m.handleRunPrompt()
		case "O":
			// Launch onboard wizard (instances view or anytime).
			return m.startOnboardWizard()
		case "r":
			return m, refreshDataCmd(m.namespace)
		case "f":
			// Toggle detail pane: collapsed ↔ panel
			if m.detailPane == paneCollapsed {
				m.detailPane = panePanel
			} else if m.detailPane == panePanel {
				m.detailPane = paneCollapsed
			} else {
				// From fullscreen, go to panel
				m.detailPane = panePanel
			}
			return m, nil
		case "F":
			// Toggle fullscreen detail pane
			if m.detailPane == paneFullscreen {
				m.detailPane = panePanel
			} else {
				m.detailPane = paneFullscreen
			}
			return m, nil
		case "L":
			m.logHidden = !m.logHidden
			return m, nil
		case "{":
			if m.logScroll < len(m.logEntries) {
				m.logScroll += 5
				max := len(m.logEntries)
				if m.logScroll > max {
					m.logScroll = max
				}
			}
			return m, nil
		case "}":
			m.logScroll -= 5
			if m.logScroll < 0 {
				m.logScroll = 0
			}
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.ready = true
		m.input.Width = m.width - 6
		return m, nil

	case dataRefreshMsg:
		// Only overwrite data that was successfully fetched.
		if msg.instances != nil {
			m.instances = *msg.instances
		}
		if msg.runs != nil {
			m.runs = *msg.runs
		}
		if msg.policies != nil {
			m.policies = *msg.policies
		}
		if msg.skills != nil {
			m.skills = *msg.skills
		}
		if msg.channels != nil {
			m.channels = *msg.channels
		}
		if msg.pods != nil {
			m.pods = *msg.pods
		}
		if msg.schedules != nil {
			m.schedules = *msg.schedules
		}
		if msg.ensembles != nil {
			m.ensembles = *msg.ensembles
		}
		m.gatewayConfig = msg.gatewayConfig
		if msg.fetchErr != "" {
			m.addLog(tuiErrorStyle.Render("✗ Fetch error: " + msg.fetchErr))
			m.connected = false
		} else {
			m.connected = true
		}
		// Rebuild filter indices after data refresh.
		if m.filterText != "" {
			m.rebuildFilteredIdx()
		}
		// Clamp selection.
		maxRow := m.activeViewCount() - 1
		if maxRow < 0 {
			maxRow = 0
		}
		if m.selectedRow > maxRow {
			m.selectedRow = maxRow
		}
		return m, nil

	case cmdResultMsg:
		if m.wizard.active && m.wizard.step == wizStepApplying {
			if msg.err != nil {
				m.wizard.step = wizStepDone
				m.wizard.err = msg.err.Error()
				m.wizard.resultMsgs = []string{tuiErrorStyle.Render("✗ " + msg.err.Error())}
				m.input.Placeholder = "Press Enter to return"
				return m, nil
			}
			// Parse result messages from output (newline-separated).
			m.wizard.resultMsgs = strings.Split(msg.output, "\n")

			// If WhatsApp channel, transition to QR pairing step
			if m.wizard.channelType == "whatsapp" {
				m.wizard.step = wizStepWhatsAppQR
				m.wizard.qrStatus = "waiting"
				m.input.Placeholder = "Waiting for WhatsApp pod... (press Esc to skip)"
				return m, pollWhatsAppQRCmd(m.wizard.targetNamespace, m.wizard.instanceName)
			}

			m.wizard.step = wizStepDone
			m.input.Placeholder = "Press Enter to return"
			return m, nil
		}
		if m.wizard.active && m.wizard.step == wizStepPersonaApplying {
			if msg.err != nil {
				m.wizard.step = wizStepPersonaDone
				m.wizard.err = msg.err.Error()
				m.wizard.resultMsgs = []string{tuiErrorStyle.Render("✗ " + msg.err.Error())}
				m.input.Placeholder = "Press Enter to return"
				return m, nil
			}
			// tuiPersonaApply already set resultMsgs and step on the wizardState.
			// But the step mutation happened in the goroutine — re-apply here.
			m.wizard.resultMsgs = strings.Split(msg.output, "\n")
			m.wizard.step = wizStepPersonaDone
			m.input.Placeholder = "Press Enter to switch to Agents"
			return m, nil
		}
		if msg.err != nil {
			m.addLog(tuiErrorStyle.Render("✗ " + msg.err.Error()))
		} else if msg.output != "" {
			m.addLog(msg.output)
		}
		return m, refreshDataCmd(m.namespace)

	case whatsappQRPollMsg:
		if m.wizard.active && m.wizard.step == wizStepWhatsAppQR {
			if msg.err != nil {
				m.wizard.qrErr = msg.err.Error()
				m.wizard.qrStatus = "error"
				// Retry despite error
				return m, pollWhatsAppQRCmd(m.wizard.targetNamespace, m.wizard.instanceName)
			}
			m.wizard.qrStatus = msg.status
			if len(msg.qrLines) > 0 {
				m.wizard.qrLines = msg.qrLines
			}
			if msg.linked {
				// Pairing complete — move to done
				m.wizard.step = wizStepDone
				m.wizard.resultMsgs = append(m.wizard.resultMsgs,
					tuiSuccessStyle.Render("✓ WhatsApp device linked successfully!"))
				m.input.Placeholder = "Press Enter to return"
				return m, nil
			}
			// Keep polling
			return m, pollWhatsAppQRCmd(m.wizard.targetNamespace, m.wizard.instanceName)
		}
		return m, nil

	case suggestionsMsg:
		m.suggestions = msg.items
		m.suggestIdx = 0
		return m, nil

	case githubAuthDeviceCodeMsg:
		if msg.err != nil {
			m.githubAuthStatus = "error"
			m.githubAuthMessage = msg.err.Error()
			return m, nil
		}
		m.githubAuthDeviceCode = msg.deviceCode
		m.githubAuthUserCode = msg.userCode
		m.githubAuthVerifyURL = msg.verifyURL
		m.githubAuthInterval = msg.interval
		m.githubAuthStatus = "pending"
		m.githubAuthMessage = ""
		return m, pollGithubTokenCmd(msg.deviceCode, msg.interval)

	case githubAuthPollMsg:
		if msg.err != nil {
			m.githubAuthStatus = "error"
			m.githubAuthMessage = msg.err.Error()
			return m, nil
		}
		if msg.done {
			return m, writeGithubTokenCmd(msg.token)
		}
		// Still pending — keep polling
		return m, pollGithubTokenCmd(m.githubAuthDeviceCode, m.githubAuthInterval)

	case githubAuthTokenWrittenMsg:
		if msg.err != nil {
			m.githubAuthStatus = "error"
			m.githubAuthMessage = "failed to save token: " + msg.err.Error()
		} else if msg.alreadyDone {
			m.githubAuthStatus = "done"
			m.githubAuthMessage = "GitHub already authenticated"
		} else {
			m.githubAuthStatus = "done"
			m.githubAuthMessage = "GitHub authenticated — token saved to cluster"
		}
		return m, nil

	case tickMsg:
		return m, tea.Batch(refreshDataCmd(m.namespace), tickCmd())
	}

	if m.inputFocused {
		m.input, tiCmd = m.input.Update(msg)
		return m, tiCmd
	}
	return m, nil
}

func (m tuiModel) activeViewCount() int {
	if m.filterText != "" && len(m.filteredIdx) > 0 {
		return len(m.filteredIdx)
	}
	switch m.activeView {
	case viewAgents:
		return len(m.instances)
	case viewRuns:
		return len(m.runs)
	case viewPolicies:
		return len(m.policies)
	case viewSkills:
		return len(m.skills)
	case viewChannels:
		return len(m.filteredChannels())
	case viewPods:
		return len(m.filteredPods())
	case viewSchedules:
		return len(m.schedules)
	case viewGateway:
		if m.gatewayConfig == nil {
			return 0
		}
		return 1 + len(m.gatewayRoutes())
	case viewEnsembles:
		return len(m.ensembles)
	}
	return 0
}

// activeViewTotalCount returns the unfiltered count for the current view.
func (m tuiModel) activeViewTotalCount() int {
	switch m.activeView {
	case viewAgents:
		return len(m.instances)
	case viewRuns:
		return len(m.runs)
	case viewPolicies:
		return len(m.policies)
	case viewSkills:
		return len(m.skills)
	case viewChannels:
		return len(m.filteredChannels())
	case viewPods:
		return len(m.filteredPods())
	case viewSchedules:
		return len(m.schedules)
	case viewGateway:
		if m.gatewayConfig == nil {
			return 0
		}
		return 1 + len(m.gatewayRoutes())
	case viewEnsembles:
		return len(m.ensembles)
	}
	return 0
}

func (m *tuiModel) clearFilter() {
	m.filterText = ""
	m.filterMode = false
	m.filterInput.SetValue("")
	m.filterInput.Blur()
	m.filteredIdx = nil
}

func (m *tuiModel) rebuildFilteredIdx() {
	if m.filterText == "" {
		m.filteredIdx = nil
		return
	}
	m.filteredIdx = nil
	f := m.filterText
	switch m.activeView {
	case viewAgents:
		for i, inst := range m.instances {
			if matchesFilter(f, inst.Name, string(inst.Status.Phase), resolveInstanceProvider(inst)) {
				m.filteredIdx = append(m.filteredIdx, i)
			}
		}
	case viewRuns:
		for i, run := range m.runs {
			phase := string(run.Status.Phase)
			if phase == "" {
				phase = "Pending"
			}
			trigger := run.Labels["sympozium.ai/type"]
			if matchesFilter(f, run.Name, run.Spec.AgentRef, phase, trigger) {
				m.filteredIdx = append(m.filteredIdx, i)
			}
		}
	case viewPolicies:
		for i, pol := range m.policies {
			if matchesFilter(f, pol.Name) {
				m.filteredIdx = append(m.filteredIdx, i)
			}
		}
	case viewSkills:
		for i, sk := range m.skills {
			if matchesFilter(f, sk.Name) {
				m.filteredIdx = append(m.filteredIdx, i)
			}
		}
	case viewChannels:
		filtered := m.filteredChannels()
		for i, ch := range filtered {
			if matchesFilter(f, ch.InstanceName, ch.Type) {
				m.filteredIdx = append(m.filteredIdx, i)
			}
		}
	case viewPods:
		filtered := m.filteredPods()
		for i, p := range filtered {
			if matchesFilter(f, p.Name, p.Phase, p.Instance) {
				m.filteredIdx = append(m.filteredIdx, i)
			}
		}
	case viewSchedules:
		for i, s := range m.schedules {
			if matchesFilter(f, s.Name, s.Spec.AgentRef, s.Spec.Schedule) {
				m.filteredIdx = append(m.filteredIdx, i)
			}
		}
	case viewEnsembles:
		for i, e := range m.ensembles {
			if matchesFilter(f, e.Name) {
				m.filteredIdx = append(m.filteredIdx, i)
			}
		}
	}
}

func matchesFilter(filter string, fields ...string) bool {
	lower := strings.ToLower(filter)
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), lower) {
			return true
		}
	}
	return false
}

// resolveFilteredRow maps a visible row index to the original data index.
// Returns the original index, or -1 if out of range.
func (m tuiModel) resolveFilteredRow() int {
	if m.filterText == "" || len(m.filteredIdx) == 0 {
		return m.selectedRow
	}
	if m.selectedRow < len(m.filteredIdx) {
		return m.filteredIdx[m.selectedRow]
	}
	return -1
}

func (m tuiModel) filteredChannels() []channelRow {
	if m.drillInstance == "" {
		return m.channels
	}
	var out []channelRow
	for _, ch := range m.channels {
		if ch.InstanceName == m.drillInstance {
			out = append(out, ch)
		}
	}
	return out
}

func (m tuiModel) filteredPods() []podRow {
	if m.drillInstance == "" {
		return m.pods
	}
	var out []podRow
	for _, p := range m.pods {
		if p.Instance == m.drillInstance {
			out = append(out, p)
		}
	}
	return out
}

func (m *tuiModel) addLogEntry(level, s string) {
	now := time.Now()
	for _, line := range strings.Split(s, "\n") {
		m.logEntries = append(m.logEntries, logEntry{time: now, level: level, text: line})
	}
	if len(m.logEntries) > maxLogLines {
		m.logEntries = m.logEntries[len(m.logEntries)-maxLogLines:]
	}
	// Reset scroll to bottom when new entries arrive.
	m.logScroll = 0
}

func (m *tuiModel) addLog(s string) {
	m.addLogEntry("info", s)
}

func (m tuiModel) handleRowAction() (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewRuns:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.runs) {
			name := m.runs[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiRunStatus(m.namespace, name)
			})
		}
	case viewAgents:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.instances) {
			inst := m.instances[idx]
			// Show instance detail: provider config + drill into channels.
			model := inst.Spec.Agents.Default.Model
			baseURL := inst.Spec.Agents.Default.BaseURL
			if baseURL == "" {
				baseURL = "(default)"
			}
			chCount := len(inst.Spec.Channels)
			m.addLog(fmt.Sprintf("%s │ model:%s baseURL:%s channels:%d pods:%d",
				inst.Name, model, baseURL, chCount, inst.Status.ActiveAgentPods))
			// Drill into channels view for this instance.
			m.drillInstance = inst.Name
			m.activeView = viewChannels
			m.selectedRow = 0
			m.tableScroll = 0
		}
	case viewPolicies:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.policies) {
			name := m.policies[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiListFeatures(m.namespace, name)
			})
		}
	case viewChannels:
		idx := m.resolveFilteredRow()
		filtered := m.filteredChannels()
		if idx >= 0 && idx < len(filtered) {
			ch := filtered[idx]
			detail := fmt.Sprintf("%s/%s │ secret:%s status:%s", ch.InstanceName, ch.Type, ch.SecretRef, ch.Status)
			if ch.Message != "" {
				detail += " msg:" + ch.Message
			}
			if ch.LastCheck != "" {
				detail += " checked:" + ch.LastCheck + " ago"
			}
			m.addLog(detail)
		}
	case viewPods:
		idx := m.resolveFilteredRow()
		filtered := m.filteredPods()
		if idx >= 0 && idx < len(filtered) {
			p := filtered[idx]
			m.addLog(fmt.Sprintf("%s │ inst:%s phase:%s node:%s ip:%s restarts:%d",
				p.Name, p.Instance, p.Phase, p.Node, p.IP, p.Restarts))
		}
	case viewSchedules:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.schedules) {
			s := m.schedules[idx]
			nextRun := "?"
			if s.Status.NextRunTime != nil {
				nextRun = shortDuration(time.Until(s.Status.NextRunTime.Time))
			}
			m.addLog(fmt.Sprintf("%s │ inst:%s cron:%s type:%s phase:%s runs:%d next:%s",
				s.Name, s.Spec.AgentRef, s.Spec.Schedule, s.Spec.Type, s.Status.Phase, s.Status.TotalRuns, nextRun))
		}
	case viewEnsembles:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.ensembles) {
			pp := m.ensembles[idx]
			// Start the ensemble onboarding wizard with this pack pre-selected.
			return m.startPersonaWizard(pp.Name)
		}
	}
	return m, nil
}

func (m tuiModel) handleRowLogs() (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewPods:
		idx := m.resolveFilteredRow()
		filtered := m.filteredPods()
		if idx >= 0 && idx < len(filtered) {
			podName := filtered[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiPodLogs(m.namespace, podName)
			})
		}
	case viewRuns:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.runs) {
			run := m.runs[idx]
			if run.Status.PodName != "" {
				return m, m.asyncCmd(func() (string, error) {
					return tuiPodLogs(m.namespace, run.Status.PodName)
				})
			}
			m.addLog(tuiDimStyle.Render("No pod yet for run: " + run.Name))
		}
	case viewAgents:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.instances) {
			inst := m.instances[idx]
			// Show events for the instance.
			return m, m.asyncCmd(func() (string, error) {
				return tuiResourceEvents(m.namespace, "Agent", inst.Name)
			})
		}
	case viewPolicies:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.policies) {
			name := m.policies[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiResourceEvents(m.namespace, "SympoziumPolicy", name)
			})
		}
	case viewSkills:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.skills) {
			name := m.skills[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiResourceEvents(m.namespace, "SkillPack", name)
			})
		}
	case viewChannels:
		idx := m.resolveFilteredRow()
		filtered := m.filteredChannels()
		if idx >= 0 && idx < len(filtered) {
			ch := filtered[idx]
			return m, m.asyncCmd(func() (string, error) {
				return tuiResourceEvents(m.namespace, "Agent", ch.InstanceName)
			})
		}
	case viewSchedules:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.schedules) {
			name := m.schedules[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiResourceEvents(m.namespace, "SympoziumSchedule", name)
			})
		}
	default:
		m.addLog(tuiDimStyle.Render("Logs not available for this view"))
	}
	return m, nil
}

func (m tuiModel) handleRowDescribe() (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewAgents:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.instances) {
			name := m.instances[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "agent", name)
			})
		}
	case viewRuns:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.runs) {
			name := m.runs[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "agentrun", name)
			})
		}
	case viewPolicies:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.policies) {
			name := m.policies[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "sympoziumpolicy", name)
			})
		}
	case viewSkills:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.skills) {
			name := m.skills[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "skillpack", name)
			})
		}
	case viewPods:
		idx := m.resolveFilteredRow()
		filtered := m.filteredPods()
		if idx >= 0 && idx < len(filtered) {
			podName := filtered[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "pod", podName)
			})
		}
	case viewChannels:
		idx := m.resolveFilteredRow()
		filtered := m.filteredChannels()
		if idx >= 0 && idx < len(filtered) {
			ch := filtered[idx]
			// Describe the parent instance for the channel.
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "agent", ch.InstanceName)
			})
		}
	case viewSchedules:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.schedules) {
			name := m.schedules[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "sympoziumschedule", name)
			})
		}
	case viewEnsembles:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.ensembles) {
			name := m.ensembles[idx].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "ensemble", name)
			})
		}
	}
	return m, nil
}

func (m tuiModel) handleRowDelete() (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewAgents:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.instances) {
			inst := m.instances[idx]
			name := inst.Name
			ns := m.namespace
			// Check if this instance belongs to a Ensemble.
			packName := inst.Labels["sympozium.ai/ensemble"]
			personaName := inst.Labels["sympozium.ai/agent-config"]
			if packName != "" && personaName != "" {
				m.confirmDelete = true
				m.deleteResourceKind = "agent in ensemble " + packName
				m.deleteResourceName = personaName
				m.deleteFunc = func() (string, error) {
					return tuiDisablePackPersona(ns, packName, personaName)
				}
			} else {
				m.confirmDelete = true
				m.deleteResourceKind = "agent"
				m.deleteResourceName = name
				m.deleteFunc = func() (string, error) { return tuiDelete(ns, "agent", name) }
			}
		}
	case viewRuns:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.runs) {
			name := m.runs[idx].Name
			m.confirmDelete = true
			m.deleteResourceKind = "run"
			m.deleteResourceName = name
			ns := m.namespace
			m.deleteFunc = func() (string, error) { return tuiDelete(ns, "run", name) }
		}
	case viewPolicies:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.policies) {
			name := m.policies[idx].Name
			m.confirmDelete = true
			m.deleteResourceKind = "policy"
			m.deleteResourceName = name
			ns := m.namespace
			m.deleteFunc = func() (string, error) { return tuiDelete(ns, "policy", name) }
		}
	case viewChannels:
		idx := m.resolveFilteredRow()
		filtered := m.filteredChannels()
		if idx >= 0 && idx < len(filtered) {
			ch := filtered[idx]
			m.confirmDelete = true
			m.deleteResourceKind = "channel"
			m.deleteResourceName = ch.InstanceName + "/" + ch.Type
			instName := ch.InstanceName
			chType := ch.Type
			ns := m.namespace
			m.deleteFunc = func() (string, error) { return tuiRemoveChannel(ns, instName, chType) }
		}
	case viewPods:
		idx := m.resolveFilteredRow()
		filtered := m.filteredPods()
		if idx >= 0 && idx < len(filtered) {
			podName := filtered[idx].Name
			m.confirmDelete = true
			m.deleteResourceKind = "pod"
			m.deleteResourceName = podName
			ns := m.namespace
			m.deleteFunc = func() (string, error) { return tuiDeletePod(ns, podName) }
		}
	case viewSchedules:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.schedules) {
			name := m.schedules[idx].Name
			m.confirmDelete = true
			m.deleteResourceKind = "schedule"
			m.deleteResourceName = name
			ns := m.namespace
			m.deleteFunc = func() (string, error) { return tuiDelete(ns, "schedule", name) }
		}
	case viewEnsembles:
		idx := m.resolveFilteredRow()
		if idx >= 0 && idx < len(m.ensembles) {
			pack := m.ensembles[idx]
			name := pack.Name
			ns := m.namespace
			// Collect all agent config names to disable.
			var allNames []string
			for _, p := range pack.Spec.AgentConfigs {
				allNames = append(allNames, p.Name)
			}
			m.confirmDelete = true
			m.deleteResourceKind = "all agents in ensemble"
			m.deleteResourceName = name
			m.deleteFunc = func() (string, error) {
				return tuiDisableAllEnsembleAgents(ns, name, allNames)
			}
		}
	}
	return m, nil
}

func (m tuiModel) handleRowEdit() (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewAgents:
		idx := m.resolveFilteredRow()
		if idx < 0 || idx >= len(m.instances) {
			return m, nil
		}
		inst := m.instances[idx]
		m.editInstanceName = inst.Name
		m.editScheduleName = ""
		m.editTab = 0
		m.editField = 0
		// Populate memory form from instance spec.
		if inst.Spec.Memory != nil {
			m.editMemory = editMemoryForm{
				enabled:      inst.Spec.Memory.Enabled,
				maxSizeKB:    fmt.Sprintf("%d", inst.Spec.Memory.MaxSizeKB),
				systemPrompt: inst.Spec.Memory.SystemPrompt,
			}
		} else {
			m.editMemory = editMemoryForm{
				enabled:   true,
				maxSizeKB: "256",
			}
		}
		// Find first schedule for this instance to pre-populate heartbeat tab.
		m.editHeartbeat = editHeartbeatForm{
			schedule:          "0 * * * *",
			task:              "Review your memory. Summarise what you know so far and note anything that needs attention.",
			schedType:         0,
			concurrencyPolicy: 0,
			includeMemory:     true,
			suspend:           false,
		}
		for i, sched := range m.schedules {
			if sched.Spec.AgentRef == inst.Name {
				m.editScheduleName = sched.Name
				m.editHeartbeat.schedule = sched.Spec.Schedule
				m.editHeartbeat.task = sched.Spec.Task
				for j, t := range editScheduleTypes {
					if t == sched.Spec.Type {
						m.editHeartbeat.schedType = j
						break
					}
				}
				for j, p := range editConcurrencyPolicies {
					if p == sched.Spec.ConcurrencyPolicy {
						m.editHeartbeat.concurrencyPolicy = j
						break
					}
				}
				m.editHeartbeat.includeMemory = sched.Spec.IncludeMemory
				m.editHeartbeat.suspend = sched.Spec.Suspend
				_ = i
				break
			}
		}
		// Populate skills tab: list all available SkillPacks, mark those enabled on this instance.
		enabledSkills := make(map[string]bool)
		for _, sr := range inst.Spec.Skills {
			if sr.SkillPackRef != "" {
				enabledSkills[sr.SkillPackRef] = true
			}
		}
		m.editSkills = nil
		// Build map of existing params per skill for pre-population.
		skillParams := make(map[string]map[string]string)
		for _, sr := range inst.Spec.Skills {
			if sr.SkillPackRef != "" && len(sr.Params) > 0 {
				skillParams[sr.SkillPackRef] = sr.Params
			}
		}
		for _, sp := range m.skills {
			hostReq := false
			hostInfo := ""
			if sp.Spec.Sidecar != nil && sp.Spec.Sidecar.HostAccess != nil && sp.Spec.Sidecar.HostAccess.Enabled {
				hostReq = true
				hostInfo = summarizeSkillHostAccess(sp.Spec.Sidecar.HostAccess)
			}
			m.editSkills = append(m.editSkills, editSkillItem{
				name:     sp.Name,
				enabled:  enabledSkills[sp.Name] || sp.Name == "memory",
				category: sp.Spec.Category,
				params:   skillParams[sp.Name],
				hostReq:  hostReq,
				hostInfo: hostInfo,
			})
		}
		// Populate channels tab: list all available channel types, mark those bound.
		boundChannels := make(map[string]string) // type → secret
		for _, ch := range inst.Spec.Channels {
			boundChannels[ch.Type] = ch.ConfigRef.Secret
		}
		m.editChannels = nil
		m.editChannelNewTokens = nil
		for _, ct := range availableChannelTypes {
			m.editChannels = append(m.editChannels, editChannelItem{
				chType:    ct,
				enabled:   boundChannels[ct] != "",
				secretRef: boundChannels[ct],
				tokenKey:  channelTokenKeyFor(ct),
			})
		}
		// Populate web endpoint tab
		m.editWebEndpoint = editWebEndpointForm{
			enabled:   false,
			hostname:  "",
			rateLimit: "60",
		}
		if inst.Spec.WebEndpoint != nil {
			m.editWebEndpoint.enabled = inst.Spec.WebEndpoint.Enabled
			m.editWebEndpoint.hostname = inst.Spec.WebEndpoint.Hostname
			if inst.Spec.WebEndpoint.RateLimit != nil && inst.Spec.WebEndpoint.RateLimit.RequestsPerMinute > 0 {
				m.editWebEndpoint.rateLimit = fmt.Sprintf("%d", inst.Spec.WebEndpoint.RateLimit.RequestsPerMinute)
			}
		}
		// Populate lifecycle tab.
		m.editLifecycle = editLifecycleForm{}
		if lc := inst.Spec.Agents.Default.Lifecycle; lc != nil {
			for _, h := range lc.PreRun {
				m.editLifecycle.preRun = append(m.editLifecycle.preRun, editLifecycleHook{
					name:    h.Name,
					image:   h.Image,
					command: strings.Join(h.Command, " "),
					envVars: lifecycleEnvToString(h.Env),
				})
			}
			for _, h := range lc.PostRun {
				m.editLifecycle.postRun = append(m.editLifecycle.postRun, editLifecycleHook{
					name:    h.Name,
					image:   h.Image,
					command: strings.Join(h.Command, " "),
					envVars: lifecycleEnvToString(h.Env),
				})
			}
			m.editLifecycle.rbac = lifecycleRBACToString(lc.RBAC)
		}
		m.showEditModal = true
	case viewSchedules:
		idx := m.resolveFilteredRow()
		if idx < 0 || idx >= len(m.schedules) {
			return m, nil
		}
		sched := m.schedules[idx]
		m.editScheduleName = sched.Name
		m.editInstanceName = sched.Spec.AgentRef
		m.editTab = 1
		m.editField = 0
		m.editHeartbeat = editHeartbeatForm{
			schedule:      sched.Spec.Schedule,
			task:          sched.Spec.Task,
			includeMemory: sched.Spec.IncludeMemory,
			suspend:       sched.Spec.Suspend,
		}
		for j, t := range editScheduleTypes {
			if t == sched.Spec.Type {
				m.editHeartbeat.schedType = j
				break
			}
		}
		for j, p := range editConcurrencyPolicies {
			if p == sched.Spec.ConcurrencyPolicy {
				m.editHeartbeat.concurrencyPolicy = j
				break
			}
		}
		// Also populate memory, skills, and channels from instance if found.
		m.editMemory = editMemoryForm{maxSizeKB: "256"}
		m.editSkills = nil
		m.editChannels = nil
		for _, inst := range m.instances {
			if inst.Name == sched.Spec.AgentRef {
				if inst.Spec.Memory != nil {
					m.editMemory = editMemoryForm{
						enabled:      inst.Spec.Memory.Enabled,
						maxSizeKB:    fmt.Sprintf("%d", inst.Spec.Memory.MaxSizeKB),
						systemPrompt: inst.Spec.Memory.SystemPrompt,
					}
				}
				enabledSkills := make(map[string]bool)
				skillParams := make(map[string]map[string]string)
				for _, sr := range inst.Spec.Skills {
					if sr.SkillPackRef != "" {
						enabledSkills[sr.SkillPackRef] = true
						if len(sr.Params) > 0 {
							skillParams[sr.SkillPackRef] = sr.Params
						}
					}
				}
				for _, sp := range m.skills {
					hostReq := false
					hostInfo := ""
					if sp.Spec.Sidecar != nil && sp.Spec.Sidecar.HostAccess != nil && sp.Spec.Sidecar.HostAccess.Enabled {
						hostReq = true
						hostInfo = summarizeSkillHostAccess(sp.Spec.Sidecar.HostAccess)
					}
					m.editSkills = append(m.editSkills, editSkillItem{
						name:     sp.Name,
						enabled:  enabledSkills[sp.Name] || sp.Name == "memory",
						category: sp.Spec.Category,
						params:   skillParams[sp.Name],
						hostReq:  hostReq,
						hostInfo: hostInfo,
					})
				}
				boundChannels := make(map[string]string)
				for _, ch := range inst.Spec.Channels {
					boundChannels[ch.Type] = ch.ConfigRef.Secret
				}
				for _, ct := range availableChannelTypes {
					m.editChannels = append(m.editChannels, editChannelItem{
						chType:    ct,
						enabled:   boundChannels[ct] != "",
						secretRef: boundChannels[ct],
						tokenKey:  channelTokenKeyFor(ct),
					})
				}
				break
			}
		}
		m.showEditModal = true
	case viewEnsembles:
		idx := m.resolveFilteredRow()
		if idx < 0 || idx >= len(m.ensembles) {
			return m, nil
		}
		pp := m.ensembles[idx]
		m.editEnsembleName = pp.Name
		m.editInstanceName = ""
		m.editScheduleName = ""
		m.editTab = 0
		m.editField = 0

		// Detect current heartbeat interval from first agent config with a schedule.
		m.editEnsembleHeartbeatIdx = len(ensembleHeartbeatOptions) - 1 // default: "pack default"
		for _, p := range pp.Spec.AgentConfigs {
			if p.Schedule != nil && p.Schedule.Interval != "" {
				for i, opt := range ensembleHeartbeatOptions {
					if opt.interval == p.Schedule.Interval {
						m.editEnsembleHeartbeatIdx = i
						break
					}
				}
				break
			}
		}

		// Build agent toggle list from the ensemble spec.
		excluded := make(map[string]bool)
		for _, e := range pp.Spec.ExcludeAgentConfigs {
			excluded[e] = true
		}
		m.editEnsembleAgents = nil
		for _, p := range pp.Spec.AgentConfigs {
			dn := p.DisplayName
			if dn == "" {
				dn = p.Name
			}
			m.editEnsembleAgents = append(m.editEnsembleAgents, editEnsembleAgentItem{
				name:        p.Name,
				displayName: dn,
				enabled:     !excluded[p.Name],
			})
		}
		m.showEditModal = true
	case viewGateway:
		m.editGateway = editGatewayForm{
			gatewayClassName: "sympozium",
			gatewayName:      "sympozium-gateway",
			tlsSecretName:    "sympozium-wildcard-cert",
		}
		if m.gatewayConfig != nil && m.gatewayConfig.Spec.Gateway != nil {
			gw := m.gatewayConfig.Spec.Gateway
			m.editGateway.enabled = gw.Enabled
			m.editGateway.baseDomain = gw.BaseDomain
			if gw.GatewayClassName != "" {
				m.editGateway.gatewayClassName = gw.GatewayClassName
			}
			if gw.Name != "" {
				m.editGateway.gatewayName = gw.Name
			}
			if gw.TLS != nil {
				m.editGateway.tlsEnabled = gw.TLS.Enabled
				m.editGateway.certManagerClusterIssuer = gw.TLS.CertManagerClusterIssuer
				if gw.TLS.SecretName != "" {
					m.editGateway.tlsSecretName = gw.TLS.SecretName
				}
			}
		}
		m.editField = 0
		m.showGatewayEditModal = true
	default:
		m.addLog(tuiDimStyle.Render("Edit not available for this view"))
	}
	return m, nil
}

func (m tuiModel) applyEditModal() tea.Cmd {
	ns := m.namespace
	instName := m.editInstanceName
	schedName := m.editScheduleName
	mem := m.editMemory
	hb := m.editHeartbeat
	webEP := m.editWebEndpoint
	lifecycle := m.editLifecycle
	lifecycle.preRun = append([]editLifecycleHook(nil), m.editLifecycle.preRun...)
	lifecycle.postRun = append([]editLifecycleHook(nil), m.editLifecycle.postRun...)
	skills := make([]editSkillItem, len(m.editSkills))
	copy(skills, m.editSkills)
	channels := make([]editChannelItem, len(m.editChannels))
	copy(channels, m.editChannels)
	newTokens := make(map[int]string)
	for k, v := range m.editChannelNewTokens {
		newTokens[k] = v
	}
	return func() tea.Msg {
		ctx := context.Background()
		var msgs []string

		// Create K8s secrets for channels that were newly enabled with tokens.
		for idx, token := range newTokens {
			if idx < 0 || idx >= len(channels) {
				continue
			}
			ch := channels[idx]
			if !ch.enabled || ch.secretRef == "" || ch.tokenKey == "" {
				continue
			}
			existing := &corev1.Secret{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: ch.secretRef, Namespace: ns}, existing); err == nil {
				_ = k8sClient.Delete(ctx, existing)
			}
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: ch.secretRef, Namespace: ns},
				StringData: map[string]string{ch.tokenKey: token},
			}
			if err := k8sClient.Create(ctx, secret); err != nil {
				return cmdResultMsg{err: fmt.Errorf("create channel secret %q: %w", ch.secretRef, err)}
			}
			msgs = append(msgs, fmt.Sprintf("Created secret: %s", ch.secretRef))
		}

		// Apply memory, skills, and channel changes to Agent.
		if instName != "" {
			var inst sympoziumv1alpha1.Agent
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: instName, Namespace: ns}, &inst); err != nil {
				return cmdResultMsg{err: fmt.Errorf("get instance %q: %w", instName, err)}
			}
			maxKB := 256
			if v, err := strconv.Atoi(mem.maxSizeKB); err == nil && v > 0 {
				maxKB = v
			}
			edit := agentedit.Edit{
				Memory: &agentedit.MemoryEdit{
					Enabled:      &mem.enabled,
					MaxSizeKB:    &maxKB,
					SystemPrompt: &mem.systemPrompt,
				},
			}

			// Apply skill toggles to instance (include per-skill Params).
			var skillRefs []sympoziumv1alpha1.SkillRef
			for _, sk := range skills {
				if sk.enabled {
					ref := sympoziumv1alpha1.SkillRef{
						SkillPackRef: sk.name,
					}
					if len(sk.params) > 0 {
						ref.Params = sk.params
					}
					skillRefs = append(skillRefs, ref)
				}
			}
			edit.Skills = &skillRefs

			// Apply channel toggles to instance.
			var channelSpecs []sympoziumv1alpha1.ChannelSpec
			for _, ch := range channels {
				if ch.enabled {
					channelSpecs = append(channelSpecs, sympoziumv1alpha1.ChannelSpec{
						Type: ch.chType,
						ConfigRef: sympoziumv1alpha1.SecretRef{
							Secret: ch.secretRef,
						},
					})
				}
			}
			edit.Channels = &channelSpecs

			// Apply web endpoint toggle
			we := &agentedit.WebEndpointEdit{Enabled: webEP.enabled}
			if webEP.enabled {
				we.Hostname = webEP.hostname
				we.RequestsPerMinute = 60
				if v, err := strconv.Atoi(webEP.rateLimit); err == nil && v > 0 {
					we.RequestsPerMinute = v
				}
			}
			edit.WebEndpoint = we

			// Apply lifecycle hooks.
			lcForm := lifecycle
			if len(lcForm.preRun) > 0 || len(lcForm.postRun) > 0 || lcForm.rbac != "" {
				lh := &sympoziumv1alpha1.LifecycleHooks{}
				for _, h := range lcForm.preRun {
					var cmd []string
					if h.command != "" {
						cmd = strings.Fields(h.command)
					}
					lh.PreRun = append(lh.PreRun, sympoziumv1alpha1.LifecycleHookContainer{
						Name:    h.name,
						Image:   h.image,
						Command: cmd,
						Env:     lifecycleEnvFromString(h.envVars),
					})
				}
				for _, h := range lcForm.postRun {
					var cmd []string
					if h.command != "" {
						cmd = strings.Fields(h.command)
					}
					lh.PostRun = append(lh.PostRun, sympoziumv1alpha1.LifecycleHookContainer{
						Name:    h.name,
						Image:   h.image,
						Command: cmd,
						Env:     lifecycleEnvFromString(h.envVars),
					})
				}
				lh.RBAC = lifecycleRBACFromString(lcForm.rbac)
				edit.Lifecycle = &lh
			} else {
				var none *sympoziumv1alpha1.LifecycleHooks
				edit.Lifecycle = &none
			}

			// Route the edit: for an ensemble-managed Agent it lands on the
			// Ensemble's agent config and flows back down, rather than being
			// written to the Agent and reverted on the next reconcile.
			target, err := agentedit.Apply(ctx, k8sClient, &inst, edit)
			if err != nil {
				return cmdResultMsg{err: fmt.Errorf("update instance %q: %w", instName, err)}
			}
			if target.Kind == "Ensemble" {
				// Target.String() reports whether anything was written and whether
				// the agent has picked it up yet.
				msgs = append(msgs, fmt.Sprintf("Applied via %s", target))
			}
			updateParts := []string{"memory"}
			if len(skills) > 0 {
				enabled := 0
				for _, sk := range skills {
					if sk.enabled {
						enabled++
					}
				}
				updateParts = append(updateParts, fmt.Sprintf("%d skill(s)", enabled))
			}
			chEnabled := 0
			for _, ch := range channels {
				if ch.enabled {
					chEnabled++
				}
			}
			if chEnabled > 0 {
				updateParts = append(updateParts, fmt.Sprintf("%d channel(s)", chEnabled))
			}
			msgs = append(msgs, fmt.Sprintf("%s updated on %s", strings.Join(updateParts, " + "), instName))

			// If WhatsApp was enabled, wait for the channel pod and report its name.
			for _, ch := range channels {
				if ch.chType == "whatsapp" && ch.enabled {
					podName := waitForWhatsAppPod(ns, instName)
					if podName != "" {
						msgs = append(msgs, fmt.Sprintf("WhatsApp pod ready: %s", podName))
						msgs = append(msgs, fmt.Sprintf("Link your device: kubectl logs -f %s -n %s", podName, ns))
					} else {
						deployName := fmt.Sprintf("%s-channel-whatsapp", instName)
						msgs = append(msgs, fmt.Sprintf("WhatsApp deployment created: %s (pod starting...)", deployName))
						msgs = append(msgs, fmt.Sprintf("Watch for the pod: kubectl get pods -l sympozium.ai/instance=%s,sympozium.ai/channel=whatsapp -n %s -w", instName, ns))
					}
					break
				}
			}
		}

		// Apply heartbeat/schedule changes.
		schedType := editScheduleTypes[hb.schedType]
		concPolicy := editConcurrencyPolicies[hb.concurrencyPolicy]
		if schedName != "" {
			// Update existing schedule.
			var sched sympoziumv1alpha1.SympoziumSchedule
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: schedName, Namespace: ns}, &sched); err != nil {
				return cmdResultMsg{err: fmt.Errorf("get schedule %q: %w", schedName, err)}
			}
			sched.Spec.Schedule = hb.schedule
			sched.Spec.Task = hb.task
			sched.Spec.Type = schedType
			sched.Spec.ConcurrencyPolicy = concPolicy
			sched.Spec.IncludeMemory = hb.includeMemory
			sched.Spec.Suspend = hb.suspend
			if err := k8sClient.Update(ctx, &sched); err != nil {
				return cmdResultMsg{err: fmt.Errorf("update schedule %q: %w", schedName, err)}
			}
			msgs = append(msgs, fmt.Sprintf("schedule %s updated", schedName))
		} else if instName != "" && hb.schedule != "" && hb.task != "" {
			// Create new schedule for instance.
			newName := instName + "-schedule"
			sched := sympoziumv1alpha1.SympoziumSchedule{
				ObjectMeta: metav1.ObjectMeta{
					Name:      newName,
					Namespace: ns,
				},
				Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
					AgentRef:          instName,
					Schedule:          hb.schedule,
					Task:              hb.task,
					Type:              schedType,
					ConcurrencyPolicy: concPolicy,
					IncludeMemory:     hb.includeMemory,
					Suspend:           hb.suspend,
				},
			}
			if err := k8sClient.Create(ctx, &sched); err != nil {
				return cmdResultMsg{err: fmt.Errorf("create schedule: %w", err)}
			}
			msgs = append(msgs, fmt.Sprintf("schedule %s created", newName))
		}

		result := tuiSuccessStyle.Render("✓ " + strings.Join(msgs, ", "))
		return cmdResultMsg{output: result}
	}
}

// applyGatewayEdit saves the gateway configuration to a SympoziumConfig CR.
func (m tuiModel) applyGatewayEdit() tea.Cmd {
	ns := m.namespace
	gw := m.editGateway
	return func() tea.Msg {
		if k8sClient == nil {
			return cmdResultMsg{err: fmt.Errorf("not connected to cluster")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		var config sympoziumv1alpha1.SympoziumConfig
		err := k8sClient.Get(ctx, client.ObjectKey{Name: "default", Namespace: ns}, &config)
		if err != nil {
			if !k8serr.IsNotFound(err) {
				return cmdResultMsg{err: fmt.Errorf("get SympoziumConfig: %w", err)}
			}
			// Create new
			config = sympoziumv1alpha1.SympoziumConfig{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default",
					Namespace: ns,
				},
			}
			config.Spec.Gateway = &sympoziumv1alpha1.GatewaySpec{
				Enabled:          gw.enabled,
				GatewayClassName: gw.gatewayClassName,
				Name:             gw.gatewayName,
				BaseDomain:       gw.baseDomain,
			}
			if gw.tlsEnabled {
				config.Spec.Gateway.TLS = &sympoziumv1alpha1.GatewayTLSSpec{
					Enabled:                  gw.tlsEnabled,
					CertManagerClusterIssuer: gw.certManagerClusterIssuer,
					SecretName:               gw.tlsSecretName,
				}
			}
			if err := k8sClient.Create(ctx, &config); err != nil {
				return cmdResultMsg{err: fmt.Errorf("create SympoziumConfig: %w", err)}
			}
			return cmdResultMsg{output: "Gateway config created"}
		}
		// Update existing
		if config.Spec.Gateway == nil {
			config.Spec.Gateway = &sympoziumv1alpha1.GatewaySpec{}
		}
		config.Spec.Gateway.Enabled = gw.enabled
		config.Spec.Gateway.GatewayClassName = gw.gatewayClassName
		config.Spec.Gateway.Name = gw.gatewayName
		config.Spec.Gateway.BaseDomain = gw.baseDomain
		if gw.tlsEnabled {
			if config.Spec.Gateway.TLS == nil {
				config.Spec.Gateway.TLS = &sympoziumv1alpha1.GatewayTLSSpec{}
			}
			config.Spec.Gateway.TLS.Enabled = gw.tlsEnabled
			config.Spec.Gateway.TLS.CertManagerClusterIssuer = gw.certManagerClusterIssuer
			config.Spec.Gateway.TLS.SecretName = gw.tlsSecretName
		} else {
			config.Spec.Gateway.TLS = nil
		}
		if err := k8sClient.Update(ctx, &config); err != nil {
			return cmdResultMsg{err: fmt.Errorf("update SympoziumConfig: %w", err)}
		}
		return cmdResultMsg{output: "Gateway config saved"}
	}
}

// applyEnsembleEdit saves the agent enable/disable toggles and heartbeat back to the Ensemble.
func (m tuiModel) applyEnsembleEdit(packName string) tea.Cmd {
	ns := m.namespace
	personas := make([]editEnsembleAgentItem, len(m.editEnsembleAgents))
	copy(personas, m.editEnsembleAgents)
	heartbeatInterval := ensembleHeartbeatOptions[m.editEnsembleHeartbeatIdx].interval
	return func() tea.Msg {
		ctx := context.Background()

		var pack sympoziumv1alpha1.Ensemble
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: packName, Namespace: ns}, &pack); err != nil {
			return cmdResultMsg{err: fmt.Errorf("get Ensemble %q: %w", packName, err)}
		}

		// Build new ExcludeAgentConfigs list from disabled toggles.
		var excludes []string
		for _, p := range personas {
			if !p.enabled {
				excludes = append(excludes, p.name)
			}
		}
		pack.Spec.ExcludeAgentConfigs = excludes

		// Apply heartbeat interval to all agent configs with a schedule.
		if heartbeatInterval != "" {
			for i := range pack.Spec.AgentConfigs {
				if pack.Spec.AgentConfigs[i].Schedule != nil {
					pack.Spec.AgentConfigs[i].Schedule.Interval = heartbeatInterval
					pack.Spec.AgentConfigs[i].Schedule.Cron = "" // clear cron so interval takes precedence
				}
			}
		}

		if err := k8sClient.Update(ctx, &pack); err != nil {
			return cmdResultMsg{err: fmt.Errorf("update Ensemble %q: %w", packName, err)}
		}

		enabled := 0
		for _, p := range personas {
			if p.enabled {
				enabled++
			}
		}
		result := tuiSuccessStyle.Render(fmt.Sprintf("✓ Ensemble %s updated: %d/%d agents enabled", packName, enabled, len(personas)))
		return cmdResultMsg{output: result}
	}
}

func (m tuiModel) handleRunPrompt() (tea.Model, tea.Cmd) {
	var instName string
	idx := m.resolveFilteredRow()
	switch m.activeView {
	case viewAgents:
		if idx >= 0 && idx < len(m.instances) {
			instName = m.instances[idx].Name
		}
	case viewRuns:
		if idx >= 0 && idx < len(m.runs) {
			instName = m.runs[idx].Spec.AgentRef
		}
	case viewChannels:
		filtered := m.filteredChannels()
		if idx >= 0 && idx < len(filtered) {
			instName = filtered[idx].InstanceName
		}
	case viewPods:
		filtered := m.filteredPods()
		if idx >= 0 && idx < len(filtered) {
			instName = filtered[idx].Instance
		}
	}
	if instName == "" {
		if len(m.instances) > 0 {
			instName = m.instances[0].Name
		} else {
			m.addLog(tuiErrorStyle.Render("No agents available to run against"))
			return m, nil
		}
	}
	m.inputFocused = true
	m.input.Focus()
	m.input.SetValue("/run " + instName + " ")
	m.input.CursorEnd()
	m.lastInput = m.input.Value()
	m.suggestions = nil
	return m, textinput.Blink
}

// ── acceptSuggestion / updateSuggestions ─────────────────────────────────────

func (m *tuiModel) acceptSuggestion() {
	if m.suggestIdx < 0 || m.suggestIdx >= len(m.suggestions) {
		return
	}
	sel := m.suggestions[m.suggestIdx]
	current := m.input.Value()
	parts := strings.Fields(current)
	hasTrailingSpace := strings.HasSuffix(current, " ")

	if len(parts) <= 1 && !hasTrailingSpace {
		m.input.SetValue(sel.text + " ")
	} else {
		if hasTrailingSpace {
			m.input.SetValue(strings.Join(parts, " ") + " " + sel.text + " ")
		} else {
			parts[len(parts)-1] = sel.text
			m.input.SetValue(strings.Join(parts, " ") + " ")
		}
	}
	m.input.CursorEnd()
	m.suggestions = nil
	m.suggestIdx = 0
}

func (m *tuiModel) updateSuggestions(input string) tea.Cmd {
	m.suggestions = nil
	m.suggestIdx = 0

	if input == "" || !strings.HasPrefix(input, "/") {
		return nil
	}

	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])

	if len(parts) == 1 && !strings.HasSuffix(input, " ") {
		var matches []suggestion
		for _, s := range slashCommandSuggestions {
			if strings.HasPrefix(s.text, cmd) && s.text != cmd {
				matches = append(matches, s)
			}
		}
		m.suggestions = matches
		return nil
	}

	argIdx := len(parts) - 1
	if strings.HasSuffix(input, " ") {
		argIdx = len(parts)
	}
	prefix := ""
	if argIdx < len(parts) {
		prefix = strings.ToLower(parts[argIdx])
	}
	ns := m.namespace

	switch cmd {
	case "/ns", "/namespace":
		if argIdx == 1 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchNamespaceSuggestions(prefix) })
		}
	case "/run":
		if argIdx == 1 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchInstanceSuggestions(ns, prefix) })
		}
	case "/abort":
		if argIdx == 1 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchRunSuggestions(ns, prefix, true) })
		}
	case "/result":
		if argIdx == 1 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchRunSuggestions(ns, prefix, false) })
		}
	case "/status":
		if argIdx == 1 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchRunSuggestions(ns, prefix, false) })
		}
	case "/features":
		if argIdx == 1 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchPolicySuggestions(ns, prefix) })
		}
	case "/channels", "/pods":
		if argIdx == 1 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchInstanceSuggestions(ns, prefix) })
		}
	case "/channel":
		if argIdx == 1 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchInstanceSuggestions(ns, prefix) })
		}
		if argIdx == 2 {
			var matches []suggestion
			for _, s := range channelTypeSuggestions {
				if prefix == "" || strings.HasPrefix(s.text, prefix) {
					matches = append(matches, s)
				}
			}
			m.suggestions = matches
			return nil
		}
	case "/rmchannel":
		if argIdx == 1 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchInstanceSuggestions(ns, prefix) })
		}
		if argIdx == 2 {
			var matches []suggestion
			for _, s := range channelTypeSuggestions {
				if prefix == "" || strings.HasPrefix(s.text, prefix) {
					matches = append(matches, s)
				}
			}
			m.suggestions = matches
			return nil
		}
	case "/provider":
		if argIdx == 1 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchInstanceSuggestions(ns, prefix) })
		}
		if argIdx == 2 {
			var matches []suggestion
			for _, s := range providerSuggestions {
				if prefix == "" || strings.HasPrefix(s.text, prefix) {
					matches = append(matches, s)
				}
			}
			m.suggestions = matches
			return nil
		}
		if argIdx == 3 && len(parts) >= 3 {
			prov := strings.ToLower(parts[2])
			if models, ok := modelSuggestions[prov]; ok {
				var matches []suggestion
				for _, s := range models {
					if prefix == "" || strings.HasPrefix(s.text, prefix) {
						matches = append(matches, s)
					}
				}
				m.suggestions = matches
				return nil
			}
		}
	case "/baseurl":
		if argIdx == 1 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchInstanceSuggestions(ns, prefix) })
		}
	case "/persona":
		if argIdx == 1 {
			var matches []suggestion
			for _, s := range []suggestion{
				{"install", "Install a Ensemble"},
				{"delete", "Delete a Ensemble"},
			} {
				if prefix == "" || strings.HasPrefix(s.text, prefix) {
					matches = append(matches, s)
				}
			}
			m.suggestions = matches
			return nil
		}
		if argIdx == 2 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchEnsembleSuggestions(ns, prefix) })
		}
	case "/delete":
		if argIdx == 1 {
			var matches []suggestion
			for _, s := range deleteTypeSuggestions {
				if prefix == "" || strings.HasPrefix(s.text, prefix) {
					matches = append(matches, s)
				}
			}
			m.suggestions = matches
			return nil
		}
		if argIdx == 2 && len(parts) >= 2 {
			rt := strings.ToLower(parts[1])
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchDeleteTargetSuggestions(ns, rt, prefix) })
		}
	}
	return nil
}

func (m *tuiModel) fetchSuggestionsAsync(fn func() []suggestion) tea.Cmd {
	return func() tea.Msg { return suggestionsMsg{items: fn()} }
}

// ── K8s suggestion fetchers ──────────────────────────────────────────────────

func fetchNamespaceSuggestions(prefix string) []suggestion {
	if k8sClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var nsList corev1.NamespaceList
	if err := k8sClient.List(ctx, &nsList); err != nil {
		return nil
	}
	var out []suggestion
	for _, ns := range nsList.Items {
		if prefix == "" || strings.HasPrefix(strings.ToLower(ns.Name), prefix) {
			out = append(out, suggestion{text: ns.Name, desc: string(ns.Status.Phase)})
		}
	}
	return out
}

func fetchInstanceSuggestions(ns, prefix string) []suggestion {
	if k8sClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var list sympoziumv1alpha1.AgentList
	if err := k8sClient.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil
	}
	var out []suggestion
	for _, inst := range list.Items {
		if prefix == "" || strings.HasPrefix(strings.ToLower(inst.Name), prefix) {
			phase := string(inst.Status.Phase)
			if phase == "" {
				phase = "-"
			}
			out = append(out, suggestion{text: inst.Name, desc: phase})
		}
	}
	return out
}

func fetchRunSuggestions(ns, prefix string, activeOnly bool) []suggestion {
	if k8sClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var list sympoziumv1alpha1.AgentRunList
	if err := k8sClient.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil
	}
	var out []suggestion
	for _, run := range list.Items {
		phase := string(run.Status.Phase)
		if phase == "" {
			phase = "Pending"
		}
		if activeOnly && (phase == "Completed" || phase == "Failed" || phase == "Skipped") {
			continue
		}
		if prefix == "" || strings.HasPrefix(strings.ToLower(run.Name), prefix) {
			out = append(out, suggestion{text: run.Name, desc: phase})
		}
	}
	return out
}

func fetchPolicySuggestions(ns, prefix string) []suggestion {
	if k8sClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var list sympoziumv1alpha1.SympoziumPolicyList
	if err := k8sClient.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil
	}
	var out []suggestion
	for _, pol := range list.Items {
		desc := fmt.Sprintf("%d bindings", pol.Status.BoundInstances)
		if prefix == "" || strings.HasPrefix(strings.ToLower(pol.Name), prefix) {
			out = append(out, suggestion{text: pol.Name, desc: desc})
		}
	}
	return out
}

func fetchEnsembleSuggestions(ns, prefix string) []suggestion {
	if k8sClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var list sympoziumv1alpha1.EnsembleList
	if err := k8sClient.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil
	}
	var out []suggestion
	for _, pp := range list.Items {
		if prefix == "" || strings.HasPrefix(strings.ToLower(pp.Name), prefix) {
			desc := pp.Spec.Category
			if desc == "" {
				desc = fmt.Sprintf("%d agents", len(pp.Spec.AgentConfigs))
			}
			out = append(out, suggestion{text: pp.Name, desc: desc})
		}
	}
	return out
}

func fetchDeleteTargetSuggestions(ns, resourceType, prefix string) []suggestion {
	switch resourceType {
	case "agent", "instance", "inst":
		return fetchInstanceSuggestions(ns, prefix)
	case "run":
		return fetchRunSuggestions(ns, prefix, false)
	case "policy", "pol":
		return fetchPolicySuggestions(ns, prefix)
	case "ensemble", "persona":
		return fetchEnsembleSuggestions(ns, prefix)
	}
	return nil
}

// ── Command handler ──────────────────────────────────────────────────────────

func (m tuiModel) handleCommand(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	cmd := strings.ToLower(parts[0])
	args := parts[1:]

	// Return to table mode after command.
	m.inputFocused = false
	m.input.Blur()

	switch cmd {
	case "/quit", "/q", "/exit":
		m.quitting = true
		return m, tea.Quit

	case "/help", "/h", "/", "/?":
		m.showModal = true
		return m, nil

	case "/onboard":
		return m.startOnboardWizard()

	case "/agents", "/inst", "/instances":
		m.activeView = viewAgents
		m.selectedRow = 0
		m.addLog("Switched to Agents view")
		return m, nil

	case "/runs":
		m.activeView = viewRuns
		m.selectedRow = 0
		m.addLog("Switched to Runs view")
		return m, nil

	case "/policies", "/pol":
		m.activeView = viewPolicies
		m.selectedRow = 0
		m.addLog("Switched to Policies view")
		return m, nil

	case "/skills":
		m.activeView = viewSkills
		m.selectedRow = 0
		m.addLog("Switched to Skills view")
		return m, nil

	case "/channels", "/ch":
		m.activeView = viewChannels
		m.selectedRow = 0
		m.tableScroll = 0
		if len(args) > 0 {
			m.drillInstance = args[0]
			m.addLog(fmt.Sprintf("Channels for agent: %s", args[0]))
		} else {
			m.drillInstance = ""
			m.addLog("Switched to Channels view (all instances)")
		}
		return m, nil

	case "/pods":
		m.activeView = viewPods
		m.selectedRow = 0
		m.tableScroll = 0
		if len(args) > 0 {
			m.drillInstance = args[0]
			m.addLog(fmt.Sprintf("Pods for agent: %s", args[0]))
		} else {
			m.drillInstance = ""
			m.addLog("Switched to Pods view (all instances)")
		}
		return m, nil

	case "/schedules", "/sched":
		m.activeView = viewSchedules
		m.selectedRow = 0
		m.tableScroll = 0
		m.addLog("Switched to Schedules view")
		return m, nil

	case "/ensembles", "/personas":
		m.activeView = viewEnsembles
		m.selectedRow = 0
		m.tableScroll = 0
		m.addLog("Switched to Ensembles view")
		return m, nil

	case "/ensemble", "/persona":
		if len(args) < 1 {
			m.addLog(tuiErrorStyle.Render("Usage: /ensemble delete <pack-name>"))
			m.addLog(tuiDimStyle.Render("  Tip: go to the Ensembles tab and press Enter on a pack to onboard."))
			return m, nil
		}
		subCmd := strings.ToLower(args[0])
		switch subCmd {
		case "delete":
			if len(args) < 2 {
				m.addLog(tuiErrorStyle.Render("Usage: /ensemble delete <pack-name>"))
				return m, nil
			}
			packName := args[1]
			ns := m.namespace
			return m, m.asyncCmd(func() (string, error) { return tuiDeleteEnsemble(ns, packName) })
		default:
			m.addLog(tuiErrorStyle.Render("Unknown sub-command. Usage: /ensemble delete <pack-name>"))
			m.addLog(tuiDimStyle.Render("  Tip: go to the Ensembles tab and press Enter on a pack to onboard."))
		}
		return m, nil

	case "/schedule":
		if len(args) < 3 {
			m.addLog(tuiErrorStyle.Render("Usage: /schedule <agent> <cron> <task>"))
			return m, nil
		}
		inst := args[0]
		cronExpr := args[1]
		task := strings.Join(args[2:], " ")
		return m, m.asyncCmd(func() (string, error) { return tuiCreateSchedule(m.namespace, inst, cronExpr, task) })

	case "/memory":
		if len(args) < 1 {
			m.addLog(tuiErrorStyle.Render("Usage: /memory <agent>"))
			return m, nil
		}
		inst := args[0]
		return m, m.asyncCmd(func() (string, error) { return tuiShowMemory(m.namespace, inst) })

	case "/channel":
		if len(args) < 3 {
			m.addLog(tuiErrorStyle.Render("Usage: /channel <agent> <type> <secret-name>"))
			return m, nil
		}
		inst, chType, secret := args[0], args[1], args[2]
		return m, m.asyncCmd(func() (string, error) { return tuiAddChannel(m.namespace, inst, chType, secret) })

	case "/rmchannel":
		if len(args) < 2 {
			m.addLog(tuiErrorStyle.Render("Usage: /rmchannel <agent> <channel-type>"))
			return m, nil
		}
		return m, m.asyncCmd(func() (string, error) { return tuiRemoveChannel(m.namespace, args[0], args[1]) })

	case "/provider":
		if len(args) < 3 {
			m.addLog(tuiErrorStyle.Render("Usage: /provider <agent> <provider> <model>"))
			return m, nil
		}
		inst, prov, model := args[0], args[1], args[2]
		return m, m.asyncCmd(func() (string, error) { return tuiSetProvider(m.namespace, inst, prov, model) })

	case "/baseurl":
		if len(args) < 2 {
			m.addLog(tuiErrorStyle.Render("Usage: /baseurl <agent> <url>"))
			return m, nil
		}
		return m, m.asyncCmd(func() (string, error) { return tuiSetBaseURL(m.namespace, args[0], args[1]) })

	case "/run":
		if len(args) < 2 {
			m.addLog(tuiErrorStyle.Render("Usage: /run <agent> <task>  (or press R to quick-run)"))
			return m, nil
		}
		instance := args[0]
		task := strings.Join(args[1:], " ")
		return m, m.asyncCmd(func() (string, error) { return tuiCreateRun(m.namespace, instance, task) })

	case "/abort":
		if len(args) < 1 {
			m.addLog(tuiErrorStyle.Render("Usage: /abort <run-name>"))
			return m, nil
		}
		return m, m.asyncCmd(func() (string, error) { return tuiAbortRun(m.namespace, args[0]) })

	case "/result":
		if len(args) < 1 {
			m.addLog(tuiErrorStyle.Render("Usage: /result <run-name>  (or press Enter on a run)"))
			return m, nil
		}
		return m, m.asyncCmd(func() (string, error) { return tuiRunStatus(m.namespace, args[0]) })

	case "/status":
		if len(args) < 1 {
			return m, m.asyncCmd(func() (string, error) { return tuiClusterStatus(m.namespace) })
		}
		return m, m.asyncCmd(func() (string, error) { return tuiRunStatus(m.namespace, args[0]) })

	case "/features":
		if len(args) < 1 {
			m.addLog(tuiErrorStyle.Render("Usage: /features <policy-name>"))
			return m, nil
		}
		return m, m.asyncCmd(func() (string, error) { return tuiListFeatures(m.namespace, args[0]) })

	case "/delete":
		if len(args) < 2 {
			m.addLog(tuiErrorStyle.Render("Usage: /delete <type> <name>"))
			return m, nil
		}
		return m, m.asyncCmd(func() (string, error) { return tuiDelete(m.namespace, args[0], args[1]) })

	case "/namespace", "/ns":
		if len(args) < 1 {
			m.addLog(fmt.Sprintf("Namespace: %s", m.namespace))
			return m, nil
		}
		m.namespace = args[0]
		m.addLog(tuiSuccessStyle.Render(fmt.Sprintf("✓ Switched to namespace: %s", m.namespace)))
		return m, refreshDataCmd(m.namespace)

	default:
		m.addLog(tuiErrorStyle.Render(fmt.Sprintf("Unknown command: %s — press ? for help", cmd)))
	}

	return m, nil
}

func (m *tuiModel) asyncCmd(fn func() (string, error)) tea.Cmd {
	return func() tea.Msg {
		out, err := fn()
		return cmdResultMsg{output: out, err: err}
	}
}

func (m tuiModel) startOnboardWizard() (tea.Model, tea.Cmd) {
	if !m.connected {
		m.addLog(tuiErrorStyle.Render("✗ Not connected to cluster — cannot onboard"))
		return m, nil
	}
	m.wizard.reset()
	m.wizard.active = true
	m.wizard.step = wizStepCheckCluster
	m.inputFocused = true
	m.input.Focus()
	m.input.SetValue("")
	m.input.Placeholder = ""
	m.suggestions = nil
	return m.advanceWizard("")
}

func (m tuiModel) startPersonaWizard(packName string) (tea.Model, tea.Cmd) {
	if !m.connected {
		m.addLog(tuiErrorStyle.Render("✗ Not connected to cluster"))
		return m, nil
	}
	m.wizard.reset()
	m.wizard.active = true
	m.wizard.personaMode = true
	m.wizard.ensembleName = packName
	// Pre-populate channels toggle list.
	m.wizard.personaChannels = make([]personaChannelChoice, len(defaultPersonaChannels))
	copy(m.wizard.personaChannels, defaultPersonaChannels)
	m.inputFocused = true
	m.input.Focus()
	m.input.SetValue("")
	m.input.Placeholder = ""
	m.suggestions = nil

	if packName == "" {
		// No pack specified — start at pack selection.
		m.wizard.step = wizStepPersonaPick
		return m, nil
	}
	// Pack specified — verify it exists and jump to provider.
	m.wizard.step = wizStepPersonaPick
	return m.advanceWizard(packName)
}

// ── View ─────────────────────────────────────────────────────────────────────

func (m tuiModel) View() string {
	if m.quitting {
		return ""
	}
	if !m.ready {
		return "\n  Loading..."
	}

	// Layout:
	//  1. Header bar          (1 line)
	//  2. Tab bar / wizard    (dynamic)
	//  3. Input               (1 line)
	//  4. Status bar          (1 line)

	// Wizard mode: full-screen wizard panel.
	if m.wizard.active {
		inputH := 1
		fixedH := 1 + 1 + inputH + 1 // header+sep+input+statusbar
		wizH := m.height - fixedH
		if wizH < 3 {
			wizH = 3
		}

		// When running the persona wizard and the terminal is wide enough,
		// split the view: wizard on the left, pack details on the right.
		showPackPane := m.wizard.personaMode && m.width >= 90 && m.wizard.ensembleName != ""
		fullWidth := m.width
		leftW := m.width
		if showPackPane {
			leftW = fullWidth * 55 / 100
			if leftW > fullWidth-30 {
				leftW = fullWidth - 30 // ensure right pane gets at least 30 cols
			}
			m.width = leftW
		}

		var view strings.Builder
		view.WriteString(m.renderHeader())
		view.WriteString("\n")
		view.WriteString(m.renderWizardPanel(wizH))
		view.WriteString(tuiSepStyle.Render(strings.Repeat("─", m.width)))
		view.WriteString("\n")
		view.WriteString(" " + m.input.View())
		view.WriteString("\n")
		view.WriteString(m.renderStatusBar())
		base := view.String()

		if showPackPane {
			rightW := fullWidth - leftW - 1 // 1 for separator
			paneH := strings.Count(base, "\n")
			rightPane := m.renderEnsembleDetailPane(rightW, paneH)
			base = joinPanesHorizontally(base, rightPane, leftW, rightW)
			m.width = fullWidth
		}

		return base
	}

	// Split pane: show a detail pane on the right when the pane is open,
	// the terminal is wide enough, and the active view supports it.
	// Channels tab hides the detail pane.
	showDetailPane := m.detailPane == panePanel && m.width >= 100 && m.activeView != viewChannels
	fullWidth := m.width
	if showDetailPane {
		// Left pane gets 65%, detail pane gets 35% (minus 1 for separator).
		leftW := fullWidth * 65 / 100
		if leftW > fullWidth-25 {
			leftW = fullWidth - 25 // ensure detail pane gets at least 25 cols
		}
		m.width = leftW
	}

	// Normal layout:
	//  1. Header bar          (1 line)
	//  2. Tab bar             (1 line)
	//  3. Column headers      (1 line)
	//  4. Table rows          (dynamic)
	//  5. Separator           (1 line)
	//  6. Log pane            (logH lines)
	//  7. Separator           (1 line)
	//  8. Input + suggestions (1-N lines)
	//  9. Status bar          (1 line)

	// Dynamically split available space: ~half for table, ~half for log pane.
	inputH := 1
	suggestH := 0
	if len(m.suggestions) > 0 {
		suggestH = min(len(m.suggestions), 6) + 1
	}
	chrome := 1 + 1 + 1 + 1 + inputH + suggestH + 1 // header+tabs+colhdr+sep(below log)+input+suggest+statusbar
	if !m.logHidden {
		chrome += 1 // sep(above log)
	}
	available := m.height - chrome
	if available < 4 {
		available = 4
	}
	var tableH, logH int
	if m.logHidden {
		tableH = available
		logH = 0
	} else {
		tableH = available / 2
		logH = available - tableH
		if tableH < 2 {
			tableH = 2
		}
		if logH < 3 {
			logH = 3
		}
	}

	// Derive the vertical scroll offset from the current selection so the
	// highlighted row is always within the visible window. Every table renderer
	// draws tableH-1 rows starting at m.tableScroll; without this, tableScroll
	// stayed pinned at 0 and any selection past the first screenful became
	// invisible and unreachable.
	visibleRows := tableH - 1
	if visibleRows < 1 {
		visibleRows = 1
	}
	if m.selectedRow < m.tableScroll {
		m.tableScroll = m.selectedRow
	} else if m.selectedRow >= m.tableScroll+visibleRows {
		m.tableScroll = m.selectedRow - visibleRows + 1
	}
	if m.tableScroll < 0 {
		m.tableScroll = 0
	}

	var view strings.Builder

	// 1. Header bar
	view.WriteString(m.renderHeader())
	view.WriteString("\n")

	// 2. Tab bar
	view.WriteString(m.renderTabBar())
	view.WriteString("\n")

	// 3-4. Table
	view.WriteString(m.renderTable(tableH))

	if !m.logHidden {
		// 5. Separator
		view.WriteString(tuiSepStyle.Render(strings.Repeat("─", m.width)))
		view.WriteString("\n")

		// 6. Log pane
		view.WriteString(m.renderLog(logH))
	}

	// 7. Separator
	view.WriteString(tuiSepStyle.Render(strings.Repeat("─", m.width)))
	view.WriteString("\n")

	// 8. Suggestions + Input
	if len(m.suggestions) > 0 {
		view.WriteString(m.renderSuggestions())
	}
	if m.filterMode {
		m.filterInput.Width = m.width - 6
		view.WriteString(" " + m.filterInput.View())
	} else if m.inputFocused {
		m.input.Width = m.width - 4 // reserve space for prompt and padding
		view.WriteString(" " + m.input.View())
	} else if m.filterText != "" {
		badge := tuiDimStyle.Render(" [Filter: ") + lipgloss.NewStyle().Foreground(lipgloss.Color("#e8562a")).Render(m.filterText) + tuiDimStyle.Render("] Press / to enter a command")
		view.WriteString(badge)
	} else {
		view.WriteString(tuiDimStyle.Render(" Press / to enter a command"))
	}
	view.WriteString("\n")

	// 9. Status bar
	view.WriteString(m.renderStatusBar())

	base := view.String()

	if showDetailPane {
		rightW := fullWidth - m.width - 1 // 1 for vertical separator
		// Derive pane height from the actual left-pane line count so the
		// right pane never exceeds it (which would push the header off-screen).
		paneH := strings.Count(base, "\n")
		paneStr := m.renderDetailPane(rightW, paneH)
		base = joinPanesHorizontally(base, paneStr, m.width, rightW)
		m.width = fullWidth // restore for overlay centering
	}

	if m.detailPane == paneFullscreen {
		return m.renderDetailPaneFullscreen()
	}
	if m.confirmDelete {
		return m.renderDeleteConfirm(base)
	}
	if m.showEditModal {
		return m.renderEditModal(base)
	}
	if m.showGatewayEditModal {
		return m.renderGatewayEditModal(base)
	}
	if m.showModal {
		return m.renderModalOverlay(base)
	}
	return base
}

func (m tuiModel) renderHeader() string {
	logo := tuiBannerStyle.Render(" Sympozium ")
	connIcon := tuiSuccessStyle.Render(" ●")
	if !m.connected {
		connIcon = tuiErrorStyle.Render(" ●")
	}

	ns := tuiDimStyle.Render(" ns:") + lipgloss.NewStyle().Foreground(lipgloss.Color("#e8562a")).Render(m.namespace)

	counts := tuiDimStyle.Render(" │ ") +
		tuiCountStyle.Render(fmt.Sprintf("%d", len(m.instances))) + tuiDimStyle.Render(" agents ") +
		tuiCountStyle.Render(fmt.Sprintf("%d", len(m.runs))) + tuiDimStyle.Render(" runs ") +
		tuiCountStyle.Render(fmt.Sprintf("%d", len(m.policies))) + tuiDimStyle.Render(" pol ") +
		tuiCountStyle.Render(fmt.Sprintf("%d", len(m.channels))) + tuiDimStyle.Render(" ch ") +
		tuiCountStyle.Render(fmt.Sprintf("%d", len(m.pods))) + tuiDimStyle.Render(" pods")

	// Pad to full width.
	left := logo + connIcon + ns + counts
	w := lipgloss.Width(left)
	pad := ""
	if m.width > w {
		pad = strings.Repeat(" ", m.width-w)
	}
	return lipgloss.NewStyle().Background(lipgloss.Color("#1a1a18")).Render(left + pad)
}

func (m tuiModel) renderTabBar() string {
	var tabs strings.Builder
	for i, name := range viewNames {
		label := fmt.Sprintf(" %d:%s ", i+1, name)
		if tuiViewKind(i) == m.activeView && m.filterText != "" && m.filteredIdx != nil {
			label = fmt.Sprintf(" %d:%s (%d/%d) ", i+1, name, len(m.filteredIdx), m.activeViewTotalCount())
		}
		if tuiViewKind(i) == m.activeView {
			tabs.WriteString(tuiTabActiveStyle.Render(label))
		} else {
			tabs.WriteString(tuiTabStyle.Render(label))
		}
	}
	// Show drill-down filter.
	if m.drillInstance != "" && (m.activeView == viewChannels || m.activeView == viewPods) {
		tabs.WriteString(tuiDimStyle.Render(" "))
		tabs.WriteString(lipgloss.NewStyle().
			Foreground(lipgloss.Color("#e8562a")).
			Background(lipgloss.Color("#1a1a18")).
			Render("⊳ " + m.drillInstance))
	}
	left := tabs.String()
	w := lipgloss.Width(left)
	pad := ""
	if m.width > w {
		pad = strings.Repeat(" ", m.width-w)
	}
	return lipgloss.NewStyle().Background(lipgloss.Color("#1a1a18")).Render(left + pad)
}

func (m tuiModel) renderTable(tableH int) string {
	var b strings.Builder

	switch m.activeView {
	case viewAgents:
		b.WriteString(m.renderAgentsTable(tableH))
	case viewRuns:
		b.WriteString(m.renderRunsTable(tableH))
	case viewPolicies:
		b.WriteString(m.renderPoliciesTable(tableH))
	case viewSkills:
		b.WriteString(m.renderSkillsTable(tableH))
	case viewChannels:
		b.WriteString(m.renderChannelsTable(tableH))
	case viewPods:
		b.WriteString(m.renderPodsTable(tableH))
	case viewSchedules:
		b.WriteString(m.renderSchedulesTable(tableH))
	case viewGateway:
		b.WriteString(m.renderGatewayTable(tableH))
	case viewEnsembles:
		b.WriteString(m.renderEnsemblesTable(tableH))
	}

	return b.String()
}

// resolveInstanceProvider returns the display provider name for a Agent.
func resolveInstanceProvider(inst sympoziumv1alpha1.Agent) string {
	if len(inst.Spec.AuthRefs) > 0 && inst.Spec.AuthRefs[0].Provider != "" {
		return inst.Spec.AuthRefs[0].Provider
	}
	if inst.Spec.Agents.Default.BaseURL != "" {
		u := inst.Spec.Agents.Default.BaseURL
		if strings.Contains(u, "ollama") || strings.Contains(u, ":11434") {
			return "ollama"
		}
		if strings.Contains(u, "lm-studio") || strings.Contains(u, ":1234") {
			return "lm-studio"
		}
		if strings.Contains(u, "llama-server") {
			return "llama-server"
		}
		return "custom"
	}
	return "-"
}

func (m tuiModel) renderAgentsTable(tableH int) string {
	var b strings.Builder

	header := fmt.Sprintf(" %-22s %-12s %-12s %-16s %-8s %-10s %-8s", "NAME", "PHASE", "PROVIDER", "SKILLS", "PODS", "TOKENS", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if len(m.instances) == 0 {
		b.WriteString(m.renderEmptyTable(tableH-1, "No agents — press O to onboard or type /onboard"))
		return b.String()
	}

	// Pre-compute total token usage per instance from completed runs.
	instanceTokens := make(map[string]int)
	for _, run := range m.runs {
		if run.Status.TokenUsage != nil {
			instanceTokens[run.Spec.AgentRef] += run.Status.TokenUsage.TotalTokens
		}
	}

	dataLen := len(m.instances)
	if m.filterText != "" && m.filteredIdx != nil {
		dataLen = len(m.filteredIdx)
	}

	for i := 0; i < tableH-1; i++ {
		visIdx := i + m.tableScroll
		if visIdx >= dataLen {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		idx := visIdx
		if m.filterText != "" && m.filteredIdx != nil {
			idx = m.filteredIdx[visIdx]
		}
		inst := m.instances[idx]
		age := shortDuration(time.Since(inst.CreationTimestamp.Time))

		provider := resolveInstanceProvider(inst)

		// Build skills column from SkillRef list.
		skillNames := make([]string, 0, len(inst.Spec.Skills))
		for _, sk := range inst.Spec.Skills {
			if sk.SkillPackRef != "" {
				skillNames = append(skillNames, sk.SkillPackRef)
			} else if sk.ConfigMapRef != "" {
				skillNames = append(skillNames, sk.ConfigMapRef)
			}
		}
		skillStr := strings.Join(skillNames, ",")
		if skillStr == "" {
			skillStr = "-"
		}

		tokStr := "-"
		if total, ok := instanceTokens[inst.Name]; ok && total > 0 {
			tokStr = formatTokenCount(total)
		}

		// Append sandbox indicator to phase.
		phase := inst.Status.Phase
		if inst.Spec.Agents.Default.AgentSandbox != nil && inst.Spec.Agents.Default.AgentSandbox.Enabled {
			phase = phase + " ▣"
		}

		row := fmt.Sprintf(" %-22s %-12s %-12s %-16s %-8d %-10s %-8s",
			truncate(inst.Name, 22), phase, truncate(provider, 12), truncate(skillStr, 16), inst.Status.ActiveAgentPods, tokStr, age)

		b.WriteString(m.styleRow(visIdx, row))
		b.WriteString("\n")
	}
	return b.String()
}

// formatTokenCount formats a token count into a human-readable string
// (e.g. 1234 → "1.2k", 56789 → "56.8k", 1234567 → "1.2M").
func formatTokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func (m tuiModel) renderRunsTable(tableH int) string {
	var b strings.Builder

	header := fmt.Sprintf(" %-26s %-18s %-12s %-8s %-8s %-14s %-8s", "NAME", "AGENT", "PHASE", "DUR", "TOKENS", "TRIGGER", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if len(m.runs) == 0 {
		b.WriteString(m.renderEmptyTable(tableH-1, "No runs — try: /run <agent> <task>"))
		return b.String()
	}

	// Styles for trigger badges.
	triggerHeartbeat := lipgloss.NewStyle().Foreground(lipgloss.Color("#e8562a")).Bold(true)
	triggerScheduled := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0ece4"))
	triggerSweep := lipgloss.NewStyle().Foreground(lipgloss.Color("#d4cfc6"))

	runsDataLen := len(m.runs)
	if m.filterText != "" && m.filteredIdx != nil {
		runsDataLen = len(m.filteredIdx)
	}

	for i := 0; i < tableH-1; i++ {
		visIdx := i + m.tableScroll
		if visIdx >= runsDataLen {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		idx := visIdx
		if m.filterText != "" && m.filteredIdx != nil {
			idx = m.filteredIdx[visIdx]
		}
		run := m.runs[idx]
		age := shortDuration(time.Since(run.CreationTimestamp.Time))
		displayName := run.Name
		if run.Spec.Parent != nil {
			displayName = "  └ " + displayName
		}
		phase := string(run.Status.Phase)
		if phase == "" {
			phase = "Pending"
		}
		// Add delegate count badge before sandbox indicator.
		if len(run.Status.Delegates) > 0 {
			phase = phase + fmt.Sprintf(" [%d]", len(run.Status.Delegates))
		}
		// Add agent-sandbox indicator to phase.
		if run.Status.SandboxName != "" || run.Status.SandboxClaimName != "" {
			phase = phase + " ▣"
		}
		// Add lifecycle postRun indicator.
		if run.Status.PostRunJobName != "" && run.Status.Phase == sympoziumv1alpha1.AgentRunPhasePostRunning {
			phase = "PostRunning ⇢"
		}

		// Compute duration.
		dur := "-"
		if run.Status.StartedAt != nil {
			end := time.Now()
			if run.Status.CompletedAt != nil {
				end = run.Status.CompletedAt.Time
			}
			dur = shortDuration(end.Sub(run.Status.StartedAt.Time))
		}

		// Compute tokens.
		tok := "-"
		if run.Status.TokenUsage != nil && run.Status.TokenUsage.TotalTokens > 0 {
			tok = formatTokenCount(run.Status.TokenUsage.TotalTokens)
		}

		// Determine trigger source from labels.
		triggerType := run.Labels["sympozium.ai/type"]
		triggerSched := run.Labels["sympozium.ai/schedule"]
		triggerText := "-"
		if triggerType != "" {
			switch triggerType {
			case "heartbeat":
				triggerText = "♥ " + triggerType
			case "sweep":
				triggerText = "⟳ " + triggerType
			default:
				triggerText = "⏱ " + triggerType
			}
			if triggerSched != "" {
				triggerText += " (" + truncate(triggerSched, 8) + ")"
			}
		}

		// Build row without phase/trigger (we'll colorize them separately).
		nameCol := fmt.Sprintf(" %-26s %-18s ", truncate(displayName, 26), truncate(run.Spec.AgentRef, 18))
		phaseCol := fmt.Sprintf("%-12s ", phase)
		durCol := fmt.Sprintf("%-8s ", dur)
		tokCol := fmt.Sprintf("%-8s ", tok)
		trigCol := fmt.Sprintf("%-14s ", truncate(triggerText, 14))
		restCol := fmt.Sprintf("%-8s", age)

		if visIdx == m.selectedRow {
			b.WriteString(tuiRowSelectedStyle.Render(padRight(nameCol+phaseCol+durCol+tokCol+trigCol+restCol, m.width)))
		} else {
			style := tuiRowStyle
			if visIdx%2 == 1 {
				style = tuiRowAltStyle
			}
			// Colorize phase.
			switch {
			case phase == "Running":
				phaseCol = tuiRunningStyle.Render(fmt.Sprintf("%-12s ", phase))
			case strings.HasPrefix(phase, "PostRunning"):
				phaseCol = tuiPostRunningStyle.Render(fmt.Sprintf("%-12s ", phase))
			case phase == "Completed" || phase == "Succeeded":
				phaseCol = tuiSuccessStyle.Render(fmt.Sprintf("%-12s ", phase))
			case phase == "Failed" || phase == "Timeout":
				phaseCol = tuiErrorStyle.Render(fmt.Sprintf("%-12s ", phase))
			case phase == "Skipped":
				phaseCol = tuiDimStyle.Render(fmt.Sprintf("%-12s ", phase))
			case phase == "Pending":
				phaseCol = tuiPendingStyle.Render(fmt.Sprintf("%-12s ", phase))
			case phase == "Serving":
				phaseCol = tuiServingStyle.Render(fmt.Sprintf("%-12s ", phase))
			default:
				phaseCol = tuiDimStyle.Render(fmt.Sprintf("%-12s ", phase))
			}
			// Colorize trigger.
			switch triggerType {
			case "heartbeat":
				trigCol = triggerHeartbeat.Render(fmt.Sprintf("%-14s ", truncate(triggerText, 14)))
			case "scheduled":
				trigCol = triggerScheduled.Render(fmt.Sprintf("%-14s ", truncate(triggerText, 14)))
			case "sweep":
				trigCol = triggerSweep.Render(fmt.Sprintf("%-14s ", truncate(triggerText, 14)))
			default:
				trigCol = tuiDimStyle.Render(fmt.Sprintf("%-14s ", truncate(triggerText, 14)))
			}
			b.WriteString(style.Render(nameCol) + phaseCol + style.Render(durCol+tokCol) + trigCol + style.Render(restCol))
			// Pad remaining.
			renderedW := lipgloss.Width(style.Render(nameCol)) + lipgloss.Width(phaseCol) + lipgloss.Width(style.Render(durCol+tokCol)) + lipgloss.Width(trigCol) + lipgloss.Width(style.Render(restCol))
			if m.width > renderedW {
				b.WriteString(style.Render(strings.Repeat(" ", m.width-renderedW)))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) renderPoliciesTable(tableH int) string {
	var b strings.Builder

	header := fmt.Sprintf(" %-26s %-18s %-8s", "NAME", "BOUND AGENTS", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if len(m.policies) == 0 {
		b.WriteString(m.renderEmptyTable(tableH-1, "No policies found"))
		return b.String()
	}

	policiesDataLen := len(m.policies)
	if m.filterText != "" && m.filteredIdx != nil {
		policiesDataLen = len(m.filteredIdx)
	}

	for i := 0; i < tableH-1; i++ {
		visIdx := i + m.tableScroll
		if visIdx >= policiesDataLen {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		idx := visIdx
		if m.filterText != "" && m.filteredIdx != nil {
			idx = m.filteredIdx[visIdx]
		}
		pol := m.policies[idx]
		age := shortDuration(time.Since(pol.CreationTimestamp.Time))
		row := fmt.Sprintf(" %-26s %-18d %-8s", truncate(pol.Name, 26), pol.Status.BoundInstances, age)
		b.WriteString(m.styleRow(visIdx, row))
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) renderSkillsTable(tableH int) string {
	var b strings.Builder

	header := fmt.Sprintf(" %-24s %-8s %-22s %-8s %-8s", "NAME", "SKILLS", "CONFIGMAP", "HOST", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if len(m.skills) == 0 {
		b.WriteString(m.renderEmptyTable(tableH-1, "No skill packs found"))
		return b.String()
	}

	skillsDataLen := len(m.skills)
	if m.filterText != "" && m.filteredIdx != nil {
		skillsDataLen = len(m.filteredIdx)
	}

	for i := 0; i < tableH-1; i++ {
		visIdx := i + m.tableScroll
		if visIdx >= skillsDataLen {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		idx := visIdx
		if m.filterText != "" && m.filteredIdx != nil {
			idx = m.filteredIdx[visIdx]
		}
		sk := m.skills[idx]
		age := shortDuration(time.Since(sk.CreationTimestamp.Time))
		cm := sk.Status.ConfigMapName
		host := "-"
		if sk.Spec.Sidecar != nil && sk.Spec.Sidecar.HostAccess != nil && sk.Spec.Sidecar.HostAccess.Enabled {
			host = "required"
		}
		if cm == "" {
			cm = "-"
		}
		row := fmt.Sprintf(" %-24s %-8d %-22s %-8s %-8s", truncate(sk.Name, 24), len(sk.Spec.Skills), truncate(cm, 22), host, age)
		b.WriteString(m.styleRow(visIdx, row))
		b.WriteString("\n")
	}
	return b.String()
}

func summarizeSkillHostAccess(ha *sympoziumv1alpha1.HostAccessSpec) string {
	if ha == nil || !ha.Enabled {
		return ""
	}
	parts := make([]string, 0, 5)
	if ha.HostPID {
		parts = append(parts, "pid")
	}
	if ha.HostNetwork {
		parts = append(parts, "net")
	}
	if ha.RunAsRoot {
		parts = append(parts, "root")
	}
	if ha.Privileged {
		parts = append(parts, "priv")
	}
	if len(ha.Mounts) > 0 {
		parts = append(parts, fmt.Sprintf("mounts:%d", len(ha.Mounts)))
	}
	return strings.Join(parts, ",")
}

func (m tuiModel) renderChannelsTable(tableH int) string {
	var b strings.Builder

	filterLabel := ""
	if m.drillInstance != "" {
		filterLabel = " [" + m.drillInstance + "]"
	}
	header := fmt.Sprintf(" %-20s %-12s %-22s %-14s %-10s %-20s", "AGENT"+filterLabel, "TYPE", "SECRET", "STATUS", "CHECKED", "MESSAGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	filtered := m.filteredChannels()
	if len(filtered) == 0 {
		msg := "No channels — try: /channel <agent> <type> <secret>"
		if m.drillInstance != "" {
			msg = fmt.Sprintf("No channels on %s — try: /channel %s telegram my-secret", m.drillInstance, m.drillInstance)
		}
		b.WriteString(m.renderEmptyTable(tableH-1, msg))
		return b.String()
	}

	channelsDataLen := len(filtered)
	if m.filterText != "" && m.filteredIdx != nil {
		channelsDataLen = len(m.filteredIdx)
	}

	for i := 0; i < tableH-1; i++ {
		visIdx := i + m.tableScroll
		if visIdx >= channelsDataLen {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		chIdx := visIdx
		if m.filterText != "" && m.filteredIdx != nil {
			chIdx = m.filteredIdx[visIdx]
		}
		ch := filtered[chIdx]
		checked := ch.LastCheck
		if checked == "" {
			checked = "-"
		}
		msg := ch.Message
		if msg == "" {
			msg = "-"
		}

		statusCol := fmt.Sprintf("%-14s ", ch.Status)
		nameCol := fmt.Sprintf(" %-20s %-12s %-22s ", truncate(ch.InstanceName, 20), ch.Type, truncate(ch.SecretRef, 22))
		restCol := fmt.Sprintf("%-10s %-20s", checked, truncate(msg, 20))

		if visIdx == m.selectedRow {
			b.WriteString(tuiRowSelectedStyle.Render(padRight(nameCol+statusCol+restCol, m.width)))
		} else {
			style := tuiRowStyle
			if visIdx%2 == 1 {
				style = tuiRowAltStyle
			}
			switch ch.Status {
			case "Connected":
				statusCol = tuiSuccessStyle.Render(fmt.Sprintf("%-14s ", ch.Status))
			case "Error", "Disconnected":
				statusCol = tuiErrorStyle.Render(fmt.Sprintf("%-14s ", ch.Status))
			default:
				statusCol = tuiDimStyle.Render(fmt.Sprintf("%-14s ", ch.Status))
			}
			b.WriteString(style.Render(nameCol) + statusCol + style.Render(restCol))
			renderedW := lipgloss.Width(style.Render(nameCol)) + lipgloss.Width(statusCol) + lipgloss.Width(style.Render(restCol))
			if m.width > renderedW {
				b.WriteString(style.Render(strings.Repeat(" ", m.width-renderedW)))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) renderPodsTable(tableH int) string {
	var b strings.Builder

	filterLabel := ""
	if m.drillInstance != "" {
		filterLabel = " [" + m.drillInstance + "]"
	}
	header := fmt.Sprintf(" %-30s %-20s %-12s %-16s %-16s %-10s %-8s", "NAME"+filterLabel, "AGENT", "PHASE", "NODE", "IP", "RESTARTS", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	filtered := m.filteredPods()
	if len(filtered) == 0 {
		msg := "No agent pods running"
		if m.drillInstance != "" {
			msg = fmt.Sprintf("No pods for %s", m.drillInstance)
		}
		b.WriteString(m.renderEmptyTable(tableH-1, msg))
		return b.String()
	}

	podsDataLen := len(filtered)
	if m.filterText != "" && m.filteredIdx != nil {
		podsDataLen = len(m.filteredIdx)
	}

	for i := 0; i < tableH-1; i++ {
		visIdx := i + m.tableScroll
		if visIdx >= podsDataLen {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		pIdx := visIdx
		if m.filterText != "" && m.filteredIdx != nil {
			pIdx = m.filteredIdx[visIdx]
		}
		p := filtered[pIdx]
		node := p.Node
		if node == "" {
			node = "-"
		}
		ip := p.IP
		if ip == "" {
			ip = "-"
		}

		phaseCol := fmt.Sprintf("%-12s ", p.Phase)
		nameCol := fmt.Sprintf(" %-30s %-20s ", truncate(p.Name, 30), truncate(p.Instance, 20))
		restCol := fmt.Sprintf("%-16s %-16s %-10d %-8s", truncate(node, 16), ip, p.Restarts, p.Age)

		if visIdx == m.selectedRow {
			b.WriteString(tuiRowSelectedStyle.Render(padRight(nameCol+phaseCol+restCol, m.width)))
		} else {
			style := tuiRowStyle
			if visIdx%2 == 1 {
				style = tuiRowAltStyle
			}
			switch p.Phase {
			case "Running":
				phaseCol = tuiRunningStyle.Render(fmt.Sprintf("%-12s ", p.Phase))
			case "Succeeded":
				phaseCol = tuiSuccessStyle.Render(fmt.Sprintf("%-12s ", p.Phase))
			case "Failed":
				phaseCol = tuiErrorStyle.Render(fmt.Sprintf("%-12s ", p.Phase))
			case "Skipped":
				phaseCol = tuiDimStyle.Render(fmt.Sprintf("%-12s ", p.Phase))
			case "Pending":
				phaseCol = tuiPendingStyle.Render(fmt.Sprintf("%-12s ", p.Phase))
			default:
				phaseCol = tuiDimStyle.Render(fmt.Sprintf("%-12s ", p.Phase))
			}
			b.WriteString(style.Render(nameCol) + phaseCol + style.Render(restCol))
			renderedW := lipgloss.Width(style.Render(nameCol)) + lipgloss.Width(phaseCol) + lipgloss.Width(style.Render(restCol))
			if m.width > renderedW {
				b.WriteString(style.Render(strings.Repeat(" ", m.width-renderedW)))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) renderSchedulesTable(tableH int) string {
	var b strings.Builder

	header := fmt.Sprintf(" %-24s %-18s %-18s %-12s %-10s %-10s %-8s", "NAME", "AGENT", "SCHEDULE", "TYPE", "PHASE", "RUNS", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if len(m.schedules) == 0 {
		b.WriteString(m.renderEmptyTable(tableH-1, "No schedules — try: /schedule <agent> <cron> <task>"))
		return b.String()
	}

	schedulesDataLen := len(m.schedules)
	if m.filterText != "" && m.filteredIdx != nil {
		schedulesDataLen = len(m.filteredIdx)
	}

	for i := 0; i < tableH-1; i++ {
		visIdx := i + m.tableScroll
		if visIdx >= schedulesDataLen {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		idx := visIdx
		if m.filterText != "" && m.filteredIdx != nil {
			idx = m.filteredIdx[visIdx]
		}
		s := m.schedules[idx]
		age := shortDuration(time.Since(s.CreationTimestamp.Time))
		phase := s.Status.Phase
		if phase == "" {
			phase = "Pending"
		}
		schedType := s.Spec.Type
		if schedType == "" {
			schedType = "scheduled"
		}

		nameCol := fmt.Sprintf(" %-24s %-18s %-18s ", truncate(s.Name, 24), truncate(s.Spec.AgentRef, 18), truncate(s.Spec.Schedule, 18))
		typeCol := fmt.Sprintf("%-12s ", schedType)
		phaseCol := fmt.Sprintf("%-10s ", phase)
		restCol := fmt.Sprintf("%-10d %-8s", s.Status.TotalRuns, age)

		if visIdx == m.selectedRow {
			b.WriteString(tuiRowSelectedStyle.Render(padRight(nameCol+typeCol+phaseCol+restCol, m.width)))
		} else {
			style := tuiRowStyle
			if visIdx%2 == 1 {
				style = tuiRowAltStyle
			}
			switch phase {
			case "Active":
				phaseCol = tuiRunningStyle.Render(fmt.Sprintf("%-10s ", phase))
			case "Suspended":
				phaseCol = tuiDimStyle.Render(fmt.Sprintf("%-10s ", phase))
			case "Error":
				phaseCol = tuiErrorStyle.Render(fmt.Sprintf("%-10s ", phase))
			default:
				phaseCol = tuiDimStyle.Render(fmt.Sprintf("%-10s ", phase))
			}
			b.WriteString(style.Render(nameCol+typeCol) + phaseCol + style.Render(restCol))
			renderedW := lipgloss.Width(style.Render(nameCol+typeCol)) + lipgloss.Width(phaseCol) + lipgloss.Width(style.Render(restCol))
			if m.width > renderedW {
				b.WriteString(style.Render(strings.Repeat(" ", m.width-renderedW)))
			}
		}
		b.WriteString("\n")
	}
	return b.String()
}

// gatewayRoutes returns instances that have web endpoints enabled.
func (m tuiModel) gatewayRoutes() []sympoziumv1alpha1.Agent {
	var routes []sympoziumv1alpha1.Agent
	for _, inst := range m.instances {
		if inst.Spec.WebEndpoint != nil && inst.Spec.WebEndpoint.Enabled {
			routes = append(routes, inst)
		}
	}
	return routes
}

func (m tuiModel) renderGatewayTable(tableH int) string {
	var b strings.Builder

	header := fmt.Sprintf(" %-12s %-30s %-12s %-10s %-20s %-8s", "ENABLED", "BASE DOMAIN", "PHASE", "TLS", "ADDRESS", "LISTENERS")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if m.gatewayConfig == nil {
		b.WriteString(m.renderEmptyTable(tableH-1, "No gateway configured — create a SympoziumConfig to manage the gateway"))
		return b.String()
	}

	gw := m.gatewayConfig.Spec.Gateway
	enabled := "No"
	baseDomain := "-"
	tls := "Off"
	if gw != nil {
		if gw.Enabled {
			enabled = "Yes"
		}
		if gw.BaseDomain != "" {
			baseDomain = gw.BaseDomain
		}
		if gw.TLS != nil && gw.TLS.Enabled {
			tls = "On"
		}
	}

	phase := m.gatewayConfig.Status.Phase
	if phase == "" {
		phase = "-"
	}
	address := "-"
	listeners := "-"
	if m.gatewayConfig.Status.Gateway != nil {
		if m.gatewayConfig.Status.Gateway.Address != "" {
			address = m.gatewayConfig.Status.Gateway.Address
		}
		listeners = fmt.Sprintf("%d", m.gatewayConfig.Status.Gateway.ListenerCount)
	}

	row := fmt.Sprintf(" %-12s %-30s %-12s %-10s %-20s %-8s",
		enabled, truncate(baseDomain, 30), phase, tls, truncate(address, 20), listeners)
	b.WriteString(m.styleRow(0, row))
	b.WriteString("\n")

	rowsUsed := 1

	// Routes section
	routes := m.gatewayRoutes()
	if rowsUsed < tableH-1 {
		routeHeader := fmt.Sprintf(" %-22s %-30s %-12s %-40s", "AGENT", "HOSTNAME", "STATUS", "URL")
		b.WriteString(tuiColHeaderStyle.Render(padRight(routeHeader, m.width)))
		b.WriteString("\n")
		rowsUsed++
	}

	if len(routes) == 0 {
		if rowsUsed < tableH-1 {
			b.WriteString(m.renderEmptyTable(tableH-1-rowsUsed, "No routes — enable web endpoints on agents"))
			return b.String()
		}
	} else {
		for i, inst := range routes {
			if rowsUsed >= tableH-1 {
				break
			}
			hostname := "-"
			url := "-"
			status := "-"
			if inst.Spec.WebEndpoint.Hostname != "" {
				hostname = inst.Spec.WebEndpoint.Hostname
			} else if gw != nil && gw.BaseDomain != "" {
				hostname = inst.Name + "." + gw.BaseDomain
			}
			if inst.Status.WebEndpoint != nil {
				if inst.Status.WebEndpoint.Status != "" {
					status = inst.Status.WebEndpoint.Status
				}
				if inst.Status.WebEndpoint.URL != "" {
					url = inst.Status.WebEndpoint.URL
				}
			}
			routeRow := fmt.Sprintf(" %-22s %-30s %-12s %-40s",
				truncate(inst.Name, 22), truncate(hostname, 30), status, truncate(url, 40))
			b.WriteString(m.styleRow(1+i, routeRow))
			b.WriteString("\n")
			rowsUsed++
		}
	}

	// Fill remaining rows
	for i := rowsUsed; i < tableH-1; i++ {
		b.WriteString(strings.Repeat(" ", m.width) + "\n")
	}
	return b.String()
}

func (m tuiModel) renderEnsemblesTable(tableH int) string {
	var b strings.Builder

	header := fmt.Sprintf(" %-24s %-14s %-10s %-10s %-12s %-8s", "NAME", "CATEGORY", "AGENTS", "INSTALLED", "PHASE", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if len(m.ensembles) == 0 {
		b.WriteString(m.renderEmptyTable(tableH-1, "No Ensembles found — run 'sympozium install' to add built-in packs"))
		return b.String()
	}

	ensemblesDataLen := len(m.ensembles)
	if m.filterText != "" && m.filteredIdx != nil {
		ensemblesDataLen = len(m.filteredIdx)
	}

	for i := 0; i < tableH-1; i++ {
		visIdx := i + m.tableScroll
		if visIdx >= ensemblesDataLen {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		idx := visIdx
		if m.filterText != "" && m.filteredIdx != nil {
			idx = m.filteredIdx[visIdx]
		}
		pp := m.ensembles[idx]
		age := shortDuration(time.Since(pp.CreationTimestamp.Time))
		phase := pp.Status.Phase
		if phase == "" {
			phase = "Pending"
		}
		cat := pp.Spec.Category
		if cat == "" {
			cat = "-"
		}

		agentCount := len(pp.Spec.AgentConfigs)
		name := pp.Name
		if strings.HasSuffix(name, "-example") {
			name = "📖 " + name
		}
		row := fmt.Sprintf(" %-24s %-14s %-10d %-10d %-12s %-8s",
			truncate(name, 24), truncate(cat, 14), agentCount, pp.Status.InstalledCount, phase, age)

		if visIdx == m.selectedRow {
			b.WriteString(tuiRowSelectedStyle.Render(padRight(row, m.width)))
		} else {
			style := tuiRowStyle
			if visIdx%2 == 1 {
				style = tuiRowAltStyle
			}
			b.WriteString(style.Render(padRight(row, m.width)))
		}
		b.WriteString("\n")
	}

	// Hint line below the table.
	hint := tuiDimStyle.Render(" Press Enter on a pack to onboard and create agents")
	b.WriteString(padRight(hint, m.width) + "\n")

	return b.String()
}

func (m tuiModel) renderEmptyTable(rows int, msg string) string {
	var b strings.Builder
	mid := rows / 2
	for i := 0; i < rows; i++ {
		if i == mid {
			centered := tuiDimStyle.Render(msg)
			pad := (m.width - lipgloss.Width(centered)) / 2
			if pad < 0 {
				pad = 0
			}
			b.WriteString(strings.Repeat(" ", pad) + centered)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) styleRow(idx int, content string) string {
	padded := padRight(content, m.width)
	if idx == m.selectedRow {
		return tuiRowSelectedStyle.Render(padded)
	}
	if idx%2 == 1 {
		return tuiRowAltStyle.Render(padded)
	}
	return tuiRowStyle.Render(padded)
}

func (m tuiModel) renderLog(logH int) string {
	var b strings.Builder
	scrollIndicator := ""
	if m.logScroll > 0 {
		scrollIndicator = fmt.Sprintf(" [+%d] ", m.logScroll)
	}
	title := tuiLogBorderStyle.Render("─── Log " + scrollIndicator)
	titleW := lipgloss.Width(title)
	if m.width > titleW {
		title += tuiSepStyle.Render(strings.Repeat("─", m.width-titleW))
	}
	b.WriteString(title + "\n")

	visibleRows := logH - 1
	end := len(m.logEntries) - m.logScroll
	if end < 0 {
		end = 0
	}
	start := end - visibleRows
	if start < 0 {
		start = 0
	}
	visible := m.logEntries[start:end]
	maxW := m.width - 1
	if maxW < 10 {
		maxW = 10
	}
	tsStyle := tuiDimStyle
	for i := 0; i < visibleRows; i++ {
		if i < len(visible) {
			entry := visible[i]
			ts := tsStyle.Render("[" + entry.time.Format("15:04:05") + "] ")
			line := entry.text
			// Apply level-based coloring for entries without existing ANSI.
			if !strings.Contains(line, "\x1b[") {
				switch entry.level {
				case "error":
					line = tuiErrorStyle.Render(line)
				case "success":
					line = tuiSuccessStyle.Render(line)
				case "warn":
					line = lipgloss.NewStyle().Foreground(lipgloss.Color("#facc15")).Render(line)
				}
			}
			fullLine := ts + line
			if lipgloss.Width(fullLine) > maxW {
				fullLine = ansiTruncate(fullLine, maxW)
			}
			b.WriteString(" " + fullLine)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderDetailPane dispatches to the correct detail pane content based on the
// active tab. Channels tab never shows a detail pane (handled by caller).
func (m tuiModel) renderDetailPane(width, height int) string {
	switch m.activeView {
	case viewAgents:
		return m.renderDetailFeed(width, height)
	case viewRuns:
		return m.renderDetailFeed(width, height)
	case viewSkills:
		return m.renderDetailSkillRuns(width, height)
	case viewPods:
		return m.renderDetailPodLogs(width, height)
	default:
		return m.renderDetailFeed(width, height)
	}
}

// renderDetailInstanceChannels shows channels bound to the selected instance.
func (m tuiModel) renderDetailInstanceChannels(width, height int) string {
	var allLines []string

	inst := m.selectedInstanceForFeed()
	titleLabel := "─── Channels "
	if inst != "" {
		titleLabel = fmt.Sprintf("─── Channels: %s ", inst)
	}
	title := " " + tuiFeedTitleStyle.Render(titleLabel)
	titleW := lipgloss.Width(title)
	if width > titleW {
		title += tuiSepStyle.Render(strings.Repeat("─", width-titleW))
	}
	allLines = append(allLines, title)

	if inst == "" {
		allLines = append(allLines, "")
		allLines = append(allLines, tuiDimStyle.Render("  Select an agent"))
		for len(allLines) < height {
			allLines = append(allLines, "")
		}
		return padAndJoinLines(allLines, width)
	}

	// Find channels for this instance.
	var instChannels []channelRow
	for _, ch := range m.channels {
		if ch.InstanceName == inst {
			instChannels = append(instChannels, ch)
		}
	}

	if len(instChannels) == 0 {
		allLines = append(allLines, "")
		allLines = append(allLines, tuiDimStyle.Render("  No channels"))
		allLines = append(allLines, tuiDimStyle.Render("  /channel "+inst+" telegram <secret>"))
		for len(allLines) < height {
			allLines = append(allLines, "")
		}
		return padAndJoinLines(allLines, width)
	}

	contentW := width - 4
	if contentW < 10 {
		contentW = 10
	}

	for _, ch := range instChannels {
		statusIcon := "●"
		statusStyle := tuiDimStyle
		switch ch.Status {
		case "Connected":
			statusStyle = tuiSuccessStyle
		case "Error", "Disconnected":
			statusStyle = tuiErrorStyle
		}
		line := statusStyle.Render(" "+statusIcon+" ") + lipgloss.NewStyle().Foreground(lipgloss.Color("#f0ece4")).Render(ch.Type)
		allLines = append(allLines, line)

		secretLine := tuiDimStyle.Render("   secret: " + truncate(ch.SecretRef, contentW-10))
		allLines = append(allLines, secretLine)

		statusLine := tuiDimStyle.Render("   status: ") + statusStyle.Render(ch.Status)
		allLines = append(allLines, statusLine)

		if ch.Message != "" {
			for _, wl := range wrapText(ch.Message, contentW) {
				allLines = append(allLines, tuiDimStyle.Render("   "+wl))
			}
		}
		allLines = append(allLines, "")
	}

	for len(allLines) < height {
		allLines = append(allLines, "")
	}
	if len(allLines) > height {
		allLines = allLines[:height]
	}
	return padAndJoinLines(allLines, width)
}

// renderDetailSkillRuns shows which runs have used the selected skill.
func (m tuiModel) renderDetailSkillRuns(width, height int) string {
	var allLines []string

	var skillName string
	if m.selectedRow < len(m.skills) {
		skillName = m.skills[m.selectedRow].Name
	}

	titleLabel := "─── Skill Runs "
	if skillName != "" {
		titleLabel = fmt.Sprintf("─── Runs using: %s ", skillName)
	}
	title := " " + tuiFeedTitleStyle.Render(titleLabel)
	titleW := lipgloss.Width(title)
	if width > titleW {
		title += tuiSepStyle.Render(strings.Repeat("─", width-titleW))
	}
	allLines = append(allLines, title)

	if skillName == "" {
		allLines = append(allLines, "")
		allLines = append(allLines, tuiDimStyle.Render("  Select a skill"))
		for len(allLines) < height {
			allLines = append(allLines, "")
		}
		return padAndJoinLines(allLines, width)
	}

	// Find agents that use this skill, then find their runs.
	usingAgents := make(map[string]bool)
	for _, inst := range m.instances {
		for _, sk := range inst.Spec.Skills {
			if sk.SkillPackRef == skillName || sk.ConfigMapRef == skillName {
				usingAgents[inst.Name] = true
			}
		}
	}

	var matchedRuns []sympoziumv1alpha1.AgentRun
	for _, run := range m.runs {
		if usingAgents[run.Spec.AgentRef] {
			matchedRuns = append(matchedRuns, run)
		}
	}

	if len(matchedRuns) == 0 {
		allLines = append(allLines, "")
		allLines = append(allLines, tuiDimStyle.Render("  No runs for this skill"))
		for len(allLines) < height {
			allLines = append(allLines, "")
		}
		return padAndJoinLines(allLines, width)
	}

	contentW := width - 4
	if contentW < 10 {
		contentW = 10
	}

	for _, run := range matchedRuns {
		age := shortDuration(time.Since(run.CreationTimestamp.Time))
		phase := string(run.Status.Phase)
		if phase == "" {
			phase = "Pending"
		}
		phaseStyle := tuiDimStyle
		switch phase {
		case "Succeeded", "Completed":
			phaseStyle = tuiSuccessStyle
		case "Skipped":
			phaseStyle = tuiDimStyle
		case "Running":
			phaseStyle = tuiRunningStyle
		case "Failed", "Timeout":
			phaseStyle = tuiErrorStyle
		case "Pending":
			phaseStyle = tuiPendingStyle
		case "PostRunning":
			phaseStyle = tuiPostRunningStyle
		case "Serving":
			phaseStyle = tuiServingStyle
		}
		nameLine := " " + lipgloss.NewStyle().Foreground(lipgloss.Color("#f0ece4")).Render(truncate(run.Name, contentW))
		allLines = append(allLines, nameLine)
		metaLine := tuiDimStyle.Render("   "+run.Spec.AgentRef+" • ") + phaseStyle.Render(phase) + tuiDimStyle.Render(" • "+age)
		allLines = append(allLines, metaLine)

		task := extractUserMessageFromTaskSpec(run.Spec.Task)
		if len(task) > contentW {
			task = task[:contentW-3] + "..."
		}
		allLines = append(allLines, tuiDimStyle.Render("   "+task))
		allLines = append(allLines, "")
	}

	for len(allLines) < height {
		allLines = append(allLines, "")
	}
	if len(allLines) > height {
		allLines = allLines[:height]
	}
	return padAndJoinLines(allLines, width)
}

// renderDetailPodLogs shows logs for the selected pod.
func (m tuiModel) renderDetailPodLogs(width, height int) string {
	var allLines []string

	filtered := m.filteredPods()
	var podName string
	if m.selectedRow < len(filtered) {
		podName = filtered[m.selectedRow].Name
	}

	titleLabel := "─── Pod Logs "
	if podName != "" {
		titleLabel = fmt.Sprintf("─── Logs: %s ", truncate(podName, width-16))
	}
	title := " " + tuiFeedTitleStyle.Render(titleLabel)
	titleW := lipgloss.Width(title)
	if width > titleW {
		title += tuiSepStyle.Render(strings.Repeat("─", width-titleW))
	}
	allLines = append(allLines, title)

	if podName == "" {
		allLines = append(allLines, "")
		allLines = append(allLines, tuiDimStyle.Render("  Select a pod"))
		for len(allLines) < height {
			allLines = append(allLines, "")
		}
		return padAndJoinLines(allLines, width)
	}

	// Filter log lines for this pod.
	podPrefix := podName
	var podLogs []string
	for _, entry := range m.logEntries {
		if strings.Contains(entry.text, podPrefix) {
			podLogs = append(podLogs, entry.text)
		}
	}

	if len(podLogs) == 0 {
		allLines = append(allLines, "")
		allLines = append(allLines, tuiDimStyle.Render("  No log entries for this pod"))
		allLines = append(allLines, tuiDimStyle.Render("  Press l to fetch logs"))
		for len(allLines) < height {
			allLines = append(allLines, "")
		}
		return padAndJoinLines(allLines, width)
	}

	contentW := width - 2
	if contentW < 10 {
		contentW = 10
	}

	// Show tail of pod logs.
	start := len(podLogs) - (height - 1)
	if start < 0 {
		start = 0
	}
	for _, line := range podLogs[start:] {
		if lipgloss.Width(line) > contentW {
			line = ansiTruncate(line, contentW)
		}
		allLines = append(allLines, " "+line)
	}

	for len(allLines) < height {
		allLines = append(allLines, "")
	}
	if len(allLines) > height {
		allLines = allLines[:height]
	}
	return padAndJoinLines(allLines, width)
}

// renderDetailFeed shows the conversation feed for the selected agent (used
// by Agents and Runs tabs).
func (m tuiModel) renderDetailFeed(width, height int) string {
	var allLines []string

	inst := m.selectedInstanceForFeed()

	// Title bar — show which instance the feed is for
	titleLabel := "─── Feed "
	if inst != "" {
		titleLabel = fmt.Sprintf("─── %s ", inst)
	}
	title := " " + tuiFeedTitleStyle.Render(titleLabel)
	titleW := lipgloss.Width(title)
	if width > titleW {
		title += tuiSepStyle.Render(strings.Repeat("─", width-titleW))
	}
	allLines = append(allLines, title)

	// Instance metadata section — show provider, model, sandbox, policy.
	if inst != "" {
		for _, si := range m.instances {
			if si.Name == inst {
				provider := resolveInstanceProvider(si)
				model := si.Spec.Agents.Default.Model
				if model == "" {
					model = "-"
				}
				allLines = append(allLines, tuiDimStyle.Render(fmt.Sprintf("  provider: %s  model: %s", provider, model)))
				var badges []string
				if si.Spec.Agents.Default.AgentSandbox != nil && si.Spec.Agents.Default.AgentSandbox.Enabled {
					rt := si.Spec.Agents.Default.AgentSandbox.RuntimeClass
					if rt == "" {
						rt = "default"
					}
					badges = append(badges, fmt.Sprintf("▣ agent-sandbox (%s)", rt))
				}
				if si.Spec.PolicyRef != "" {
					badges = append(badges, fmt.Sprintf("policy: %s", si.Spec.PolicyRef))
				}
				if si.Spec.Memory != nil && si.Spec.Memory.Enabled {
					badges = append(badges, "memory: on")
				}
				if len(badges) > 0 {
					allLines = append(allLines, tuiDimStyle.Render("  "+strings.Join(badges, "  ")))
				}
				allLines = append(allLines, "")
				break
			}
		}
	}

	runs := m.runsForInstance(inst)
	if len(runs) == 0 {
		allLines = append(allLines, tuiDimStyle.Render("  No runs yet"))
		allLines = append(allLines, tuiDimStyle.Render("  Press Shift+F to chat"))
		for len(allLines) < height {
			allLines = append(allLines, "")
		}
		return padAndJoinLines(allLines, width)
	}

	// Content width for wrapping (3-char indent + 1 padding).
	contentW := width - 4
	if contentW < 10 {
		contentW = 10
	}

	// Build feed entries — oldest first.
	for _, run := range runs {

		// Prompt (task) line — strip conversation context for display
		task := extractUserMessageFromTaskSpec(run.Spec.Task)
		for _, wl := range wrapText(task, contentW) {
			allLines = append(allLines, tuiFeedPromptStyle.Render(" ▸ "+wl))
		}

		// Meta line (run name + age)
		age := shortDuration(time.Since(run.CreationTimestamp.Time))
		meta := fmt.Sprintf("   %s • %s", truncate(run.Name, width-12), age)
		allLines = append(allLines, tuiFeedMetaStyle.Render(meta))

		// Result / status
		phase := string(run.Status.Phase)
		switch phase {
		case "Succeeded", "Completed":
			if run.Status.Result != "" {
				resultLines := strings.Split(run.Status.Result, "\n")
				shown := 0
				for _, rl := range resultLines {
					if shown >= 3 {
						allLines = append(allLines, tuiDimStyle.Render("   ┊ Shift+F to expand"))
						break
					}
					rl = strings.TrimRight(rl, " \t\r")
					for _, wl := range wrapText(rl, contentW) {
						allLines = append(allLines, tuiSuccessStyle.Render("   "+wl))
					}
					shown++
				}
			} else {
				allLines = append(allLines, tuiSuccessStyle.Render("   ✓ Completed"))
			}
			if run.Status.TokenUsage != nil {
				u := run.Status.TokenUsage
				allLines = append(allLines, tuiDimStyle.Render(fmt.Sprintf("   ⟠ %d in / %d out │ %d tools │ %dms",
					u.InputTokens, u.OutputTokens, u.ToolCalls, u.DurationMs)))
			}
		case "Skipped":
			skipMsg := run.Status.Result
			if skipMsg == "" {
				skipMsg = "Skipped"
			}
			for _, wl := range wrapText(skipMsg, contentW) {
				allLines = append(allLines, tuiDimStyle.Render("   ⊘ "+wl))
			}
		case "Running":
			allLines = append(allLines, tuiRunningStyle.Render("   ⏳ Running..."))
		case "PostRunning":
			postMsg := "   ⏳ Running post-hooks..."
			if run.Status.PostRunJobName != "" {
				postMsg += " (" + run.Status.PostRunJobName + ")"
			}
			allLines = append(allLines, tuiPostRunningStyle.Render(postMsg))
		case "Failed", "Timeout":
			errMsg := run.Status.Error
			if errMsg == "" {
				errMsg = phase
			}
			for _, wl := range wrapText(errMsg, contentW) {
				allLines = append(allLines, tuiErrorStyle.Render("   ✗ "+wl))
			}
		case "Pending":
			allLines = append(allLines, tuiPendingStyle.Render("   ⏳ Pending..."))
		case "Serving":
			allLines = append(allLines, tuiServingStyle.Render("   ⏳ Serving..."))
		default:
			allLines = append(allLines, tuiDimStyle.Render("   ⏳ Pending..."))
		}

		allLines = append(allLines, "") // blank separator
	}

	// Scrollable: title stays fixed, content scrolls.
	available := height - 1
	if available < 1 {
		available = 1
	}
	feedContent := allLines[1:] // skip title

	// Apply scroll offset (0 = bottom, >0 = scrolled up).
	end := len(feedContent) - m.feedScrollOffset
	if end < available {
		end = len(feedContent)
	}
	if end < 0 {
		end = 0
	}
	start := end - available
	if start < 0 {
		start = 0
	}
	visible := feedContent[start:end]

	result := []string{allLines[0]}
	result = append(result, visible...)
	if len(result) > height {
		result = result[:height]
	}
	for len(result) < height {
		result = append(result, "")
	}
	return padAndJoinLines(result, width)
}

func (m tuiModel) renderDetailPaneFullscreen() string {
	w := m.width
	h := m.height

	// For non-chat tabs, render the tab-specific detail pane at full size.
	switch m.activeView {
	case viewSkills:
		return m.renderFullscreenDetailStatic(w, h, m.renderDetailSkillRuns)
	case viewPods:
		return m.renderFullscreenDetailStatic(w, h, m.renderDetailPodLogs)
	case viewChannels:
		// Channels tab: nothing to show fullscreen, fall back to chat
	case viewAgents:
		// Fall through to chat fullscreen (same as Runs tab)
	}

	// Agents tab, Runs tab and fallback: show the chat fullscreen with input.
	inst := m.selectedInstanceForFeed()

	var allLines []string

	// Title bar — show instance name + scroll hints
	titleLabel := "─── Chat "
	if inst != "" {
		titleLabel = fmt.Sprintf("─── Chat: %s ", inst)
	}
	title := " " + tuiFeedTitleStyle.Render(titleLabel)
	titleW := lipgloss.Width(title)
	hint := tuiDimStyle.Render("  Esc close  i/Enter type  ↑↓/jk scroll")
	hintW := lipgloss.Width(hint)
	if w > titleW+hintW {
		title += tuiSepStyle.Render(strings.Repeat("─", w-titleW-hintW)) + hint
	} else if w > titleW {
		title += tuiSepStyle.Render(strings.Repeat("─", w-titleW))
	}
	allLines = append(allLines, title)

	// Content width for wrapping (3-char indent + 1 padding).
	contentW := w - 4
	if contentW < 10 {
		contentW = 10
	}

	runs := m.runsForInstance(inst)
	if len(runs) == 0 {
		allLines = append(allLines, "")
		allLines = append(allLines, tuiDimStyle.Render("  No messages yet"))
		allLines = append(allLines, tuiDimStyle.Render("  Press i or Enter to start chatting"))
	} else {
		// Build feed entries — oldest first. In fullscreen, show full results.
		for _, run := range runs {
			// Show only the user's actual message, not context preamble
			task := extractUserMessageFromTaskSpec(run.Spec.Task)
			for _, wl := range wrapText(task, contentW) {
				allLines = append(allLines, tuiFeedPromptStyle.Render(" ▸ "+wl))
			}

			// Meta line
			age := shortDuration(time.Since(run.CreationTimestamp.Time))
			meta := fmt.Sprintf("   %s • %s", truncate(run.Name, w-12), age)
			allLines = append(allLines, tuiFeedMetaStyle.Render(meta))

			// Result / status
			phase := string(run.Status.Phase)
			switch phase {
			case "Succeeded", "Completed":
				if run.Status.Result != "" {
					resultLines := strings.Split(run.Status.Result, "\n")
					for _, rl := range resultLines {
						rl = strings.TrimRight(rl, " \t\r")
						for _, wl := range wrapText(rl, contentW) {
							allLines = append(allLines, tuiSuccessStyle.Render("   "+wl))
						}
					}
				} else {
					allLines = append(allLines, tuiSuccessStyle.Render("   ✓ Completed"))
				}
			case "Skipped":
				skipMsg := run.Status.Result
				if skipMsg == "" {
					skipMsg = "Skipped"
				}
				for _, wl := range wrapText(skipMsg, contentW) {
					allLines = append(allLines, tuiDimStyle.Render("   ⊘ "+wl))
				}
			case "Running":
				allLines = append(allLines, tuiRunningStyle.Render("   ⏳ Running..."))
			case "PostRunning":
				postMsg := "   ⏳ Running post-hooks..."
				if run.Status.PostRunJobName != "" {
					postMsg += " (" + run.Status.PostRunJobName + ")"
				}
				allLines = append(allLines, tuiPostRunningStyle.Render(postMsg))
			case "Failed", "Timeout":
				errMsg := run.Status.Error
				if errMsg == "" {
					errMsg = phase
				}
				for _, wl := range wrapText(errMsg, contentW) {
					allLines = append(allLines, tuiErrorStyle.Render("   ✗ "+wl))
				}
			case "Pending":
				allLines = append(allLines, tuiPendingStyle.Render("   ⏳ Pending..."))
			case "Serving":
				allLines = append(allLines, tuiServingStyle.Render("   ⏳ Serving..."))
			default:
				allLines = append(allLines, tuiDimStyle.Render("   ⏳ Pending..."))
			}

			allLines = append(allLines, "") // blank separator
		}
	}

	// Reserve space: title (1) + separator (1) + input (1) + status (1) = 4 lines of chrome
	inputChrome := 3
	available := h - 1 - inputChrome
	if available < 1 {
		available = 1
	}
	feedContent := allLines[1:]

	// Apply scroll offset (0 = bottom, >0 = scrolled up).
	end := len(feedContent) - m.feedScrollOffset
	if end < available {
		end = len(feedContent)
	}
	if end < 0 {
		end = 0
	}
	start := end - available
	if start < 0 {
		start = 0
	}
	visible := feedContent[start:end]

	out := []string{allLines[0]}
	out = append(out, visible...)
	for len(out) < h-inputChrome {
		out = append(out, "")
	}

	// Separator above input
	out = append(out, tuiSepStyle.Render(strings.Repeat("─", w)))

	// Chat input line
	if m.feedInputFocused {
		m.feedInput.Width = w - 4
		out = append(out, " "+m.feedInput.View())
	} else {
		if inst != "" {
			out = append(out, tuiDimStyle.Render(" Press i or Enter to type a message"))
		} else {
			out = append(out, tuiDimStyle.Render(" Select an agent first"))
		}
	}

	// Status bar
	var statusKeys []string
	if m.feedInputFocused {
		statusKeys = []string{"Esc", "cancel", "Enter", "send"}
	} else {
		statusKeys = []string{"i/Enter", "type", "Esc/F", "close", "q", "quit"}
	}
	var sb strings.Builder
	for i := 0; i < len(statusKeys)-1; i += 2 {
		entry := tuiStatusKeyStyle.Render(" "+statusKeys[i]+" ") + tuiStatusBarStyle.Render(statusKeys[i+1]+" ")
		if lipgloss.Width(sb.String()+entry) > w {
			break
		}
		sb.WriteString(entry)
	}
	left := sb.String()
	lw := lipgloss.Width(left)
	pad := ""
	if w > lw {
		pad = strings.Repeat(" ", w-lw)
	}
	out = append(out, lipgloss.NewStyle().Background(lipgloss.Color("#242422")).Render(left+pad))

	return strings.Join(out, "\n")
}

// renderFullscreenDetailStatic renders a tab-specific detail pane at full
// screen size with a status bar at the bottom.
func (m tuiModel) renderFullscreenDetailStatic(w, h int, renderer func(int, int) string) string {
	// Reserve 1 line for status bar.
	contentH := h - 1
	if contentH < 3 {
		contentH = 3
	}
	content := renderer(w, contentH)

	// Status bar.
	statusKeys := []string{"Esc/F", "close", "f", "panel", "q", "quit"}
	var sb strings.Builder
	for i := 0; i < len(statusKeys)-1; i += 2 {
		entry := tuiStatusKeyStyle.Render(" "+statusKeys[i]+" ") + tuiStatusBarStyle.Render(statusKeys[i+1]+" ")
		if lipgloss.Width(sb.String()+entry) > w {
			break
		}
		sb.WriteString(entry)
	}
	left := sb.String()
	lw := lipgloss.Width(left)
	pad := ""
	if w > lw {
		pad = strings.Repeat(" ", w-lw)
	}
	bar := lipgloss.NewStyle().Background(lipgloss.Color("#242422")).Render(left + pad)

	return content + "\n" + bar
}

func padAndJoinLines(lines []string, width int) string {
	var b strings.Builder
	for i, line := range lines {
		w := lipgloss.Width(line)
		if w > width {
			line = ansiTruncate(line, width)
			w = lipgloss.Width(line)
		}
		if w < width {
			line += strings.Repeat(" ", width-w)
		}
		b.WriteString(line)
		if i < len(lines)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func (m tuiModel) renderSuggestions() string {
	maxShow := 6
	items := m.suggestions
	if len(items) > maxShow {
		items = items[:maxShow]
	}

	var b strings.Builder
	for i, s := range items {
		nameStyle := tuiSuggestStyle
		descStyle := tuiSuggestDescStyle
		if i == m.suggestIdx {
			nameStyle = tuiSuggestSelectedStyle
			descStyle = tuiSuggestDescSelectedStyle
		}
		line := nameStyle.Render(fmt.Sprintf(" %-22s", s.text)) + descStyle.Render(fmt.Sprintf(" %s ", s.desc))
		b.WriteString(" " + line + "\n")
	}
	if len(m.suggestions) > maxShow {
		b.WriteString(tuiDimStyle.Render(fmt.Sprintf("  +%d more", len(m.suggestions)-maxShow)) + "\n")
	}
	return b.String()
}

func (m tuiModel) renderStatusBar() string {
	var keys []string
	if m.wizard.active {
		keys = []string{"Esc", "cancel wizard", "Enter", "submit"}
	} else if m.filterMode {
		keys = []string{"Esc", "clear filter", "Enter", "confirm"}
	} else if m.inputFocused {
		keys = []string{"Esc", "exit input", "Tab", "complete", "Enter", "execute"}
	} else if m.confirmDelete {
		keys = []string{"y", "confirm delete", "any", "cancel"}
	} else {
		keys = []string{
			"←/→", "switch view",
			"1-8", "views",
			"Enter", "detail",
			"Esc", "back",
			"Ctrl+F", "filter",
			"f", "detail pane",
			"F", "fullscreen",
			"L", "toggle logs",
			"l", "logs",
			"d", "describe",
			"R", "run",
			"O", "onboard",
			"x", "delete",
			"e", "edit",
			"r", "refresh",
			"/", "command",
			"?", "help",
			"q", "quit",
		}
	}

	// Build key hints, stopping when we'd exceed pane width.
	var sb strings.Builder
	for i := 0; i < len(keys)-1; i += 2 {
		entry := tuiStatusKeyStyle.Render(" "+keys[i]+" ") + tuiStatusBarStyle.Render(keys[i+1]+" ")
		if lipgloss.Width(sb.String()+entry) > m.width {
			break
		}
		sb.WriteString(entry)
	}

	left := sb.String()
	w := lipgloss.Width(left)
	pad := ""
	if m.width > w {
		pad = strings.Repeat(" ", m.width-w)
	}
	return lipgloss.NewStyle().Background(lipgloss.Color("#242422")).Render(left + pad)
}

func (m tuiModel) renderDeleteConfirm(base string) string {
	var content strings.Builder
	content.WriteString(tuiModalTitleStyle.Render("  ⚠  Confirm Delete"))
	content.WriteString("\n\n")
	action := "Delete"
	if strings.HasPrefix(m.deleteResourceKind, "persona in pack") || strings.HasPrefix(m.deleteResourceKind, "all personas in pack") {
		action = "Disable"
	}
	content.WriteString(fmt.Sprintf("  %s %s %s?\n\n",
		action,
		tuiModalCmdStyle.Render(m.deleteResourceKind),
		tuiModalCmdStyle.Render(m.deleteResourceName)))
	content.WriteString(fmt.Sprintf("  %s to confirm, any other key to cancel",
		tuiStatusKeyStyle.Render(" y ")))

	modal := tuiModalBorderStyle.Render(content.String())
	lines := strings.Split(base, "\n")
	modalLines := strings.Split(modal, "\n")

	startRow := (len(lines) - len(modalLines)) / 2
	if startRow < 1 {
		startRow = 1
	}
	for i, ml := range modalLines {
		row := startRow + i
		if row >= 0 && row < len(lines) {
			mw := lipgloss.Width(ml)
			pad := (m.width - mw) / 2
			if pad < 0 {
				pad = 0
			}
			lines[row] = strings.Repeat(" ", pad) + ml
		}
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderModalOverlay(base string) string {
	var content strings.Builder
	content.WriteString(tuiModalTitleStyle.Render("  ⌨  Commands"))
	content.WriteString("\n\n")

	for _, c := range tuiCommands {
		content.WriteString(fmt.Sprintf("  %-26s %s\n",
			tuiModalCmdStyle.Render(c.cmd),
			tuiModalDescStyle.Render(c.desc)))
	}

	content.WriteString("\n")
	content.WriteString(tuiDimStyle.Render("  Press any key to dismiss"))

	modal := tuiModalBorderStyle.Render(content.String())
	lines := strings.Split(base, "\n")
	modalLines := strings.Split(modal, "\n")

	startRow := (len(lines) - len(modalLines)) / 2
	if startRow < 1 {
		startRow = 1
	}
	for i, ml := range modalLines {
		row := startRow + i
		if row >= 0 && row < len(lines) {
			mw := lipgloss.Width(ml)
			pad := (m.width - mw) / 2
			if pad < 0 {
				pad = 0
			}
			lines[row] = strings.Repeat(" ", pad) + ml
		}
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderGatewayEditModal(base string) string {
	var content strings.Builder

	content.WriteString(tuiModalTitleStyle.Render("  ✎  Edit Gateway Configuration"))
	content.WriteString("\n\n")

	highlight := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#1a1a18")).
		Background(lipgloss.Color("#e8562a")).
		Bold(true)
	label := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f0ece4")).
		Background(lipgloss.Color("#242422")).
		Width(28)
	value := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f0ece4")).
		Background(lipgloss.Color("#242422"))

	renderField := func(idx int, name string, val string) {
		lbl := label.Render("  " + name + ":")
		v := value.Render(val)
		if m.editField == idx {
			content.WriteString(highlight.Render("▸") + " " + lbl + " " + v)
		} else {
			content.WriteString("  " + lbl + " " + v)
		}
		content.WriteString("\n")
	}
	renderBool := func(idx int, name string, val bool) {
		s := "Off"
		if val {
			s = "On"
		}
		renderField(idx, name, "["+s+"]")
	}

	renderBool(0, "Enabled", m.editGateway.enabled)
	renderField(1, "Base Domain", m.editGateway.baseDomain)
	renderField(2, "GatewayClass Name", m.editGateway.gatewayClassName)
	renderField(3, "Gateway Name", m.editGateway.gatewayName)
	renderBool(4, "TLS Enabled", m.editGateway.tlsEnabled)
	renderField(5, "CertManager Issuer", m.editGateway.certManagerClusterIssuer)
	renderField(6, "TLS Secret Name", m.editGateway.tlsSecretName)

	content.WriteString("\n")
	content.WriteString(tuiDimStyle.Render("  Ctrl+S to save • Esc to cancel"))

	modal := tuiModalBorderStyle.Render(content.String())
	lines := strings.Split(base, "\n")
	modalLines := strings.Split(modal, "\n")
	startRow := (len(lines) - len(modalLines)) / 2
	if startRow < 1 {
		startRow = 1
	}
	for i, ml := range modalLines {
		row := startRow + i
		if row >= 0 && row < len(lines) {
			mw := lipgloss.Width(ml)
			pad := (m.width - mw) / 2
			if pad < 0 {
				pad = 0
			}
			lines[row] = strings.Repeat(" ", pad) + ml
		}
	}
	return strings.Join(lines, "\n")
}

func (m tuiModel) renderEditModal(base string) string {
	var content strings.Builder

	// Title
	if m.editEnsembleName != "" {
		content.WriteString(tuiModalTitleStyle.Render("  ✎  Edit Ensemble " + m.editEnsembleName))
	} else {
		title := "Edit " + m.editInstanceName
		if m.editScheduleName != "" {
			title += " / " + m.editScheduleName
		}
		content.WriteString(tuiModalTitleStyle.Render("  ✎  " + title))
	}
	content.WriteString("\n\n")

	// Tab bar (not shown for ensemble edit)
	if m.editEnsembleName == "" {
		for i, name := range editTabNames {
			if i == m.editTab {
				content.WriteString(tuiSuggestSelectedStyle.Render(" " + name + " "))
			} else {
				content.WriteString(tuiSuggestStyle.Render(" " + name + " "))
			}
			content.WriteString(" ")
		}
		content.WriteString("\n")
		content.WriteString(tuiDimStyle.Render("  ─────────────────────────────────"))
		content.WriteString("\n\n")
	}

	highlight := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#1a1a18")).
		Background(lipgloss.Color("#e8562a")).
		Bold(true)

	label := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f0ece4")).
		Background(lipgloss.Color("#242422")).
		Width(20)

	value := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#f0ece4")).
		Background(lipgloss.Color("#242422"))

	renderField := func(idx int, name string, val string) {
		lbl := label.Render("  " + name + ":")
		v := value.Render(val)
		if m.editField == idx {
			lbl = highlight.Render("▸ " + name + ":")
		}
		content.WriteString(fmt.Sprintf("  %s %s\n", lbl, v))
	}

	renderBool := func(idx int, name string, val bool) {
		tog := "○ off"
		if val {
			tog = "● on"
		}
		renderField(idx, name, tog)
	}

	if m.editEnsembleName != "" {
		// Ensemble edit — heartbeat interval + agent toggles

		// Heartbeat interval selector (field 0)
		hbLabel := ensembleHeartbeatOptions[m.editEnsembleHeartbeatIdx].label
		hbVal := fmt.Sprintf("◀ %s ▶", hbLabel)
		renderField(0, "Heartbeat", hbVal)
		content.WriteString("\n")

		content.WriteString(tuiDimStyle.Render("  Toggle personas on/off:") + "\n\n")
		if len(m.editEnsembleAgents) == 0 {
			content.WriteString(tuiDimStyle.Render("  No personas defined in this pack.") + "\n")
		} else {
			for i, p := range m.editEnsembleAgents {
				fieldIdx := i + 1 // agent toggles start at field 1
				tog := "○"
				if p.enabled {
					tog = "●"
				}
				lbl := fmt.Sprintf("  %s %s", tog, p.displayName)
				if p.name != p.displayName {
					lbl += tuiDimStyle.Render(" (" + p.name + ")")
				}
				if m.editField == fieldIdx {
					lbl = highlight.Render(fmt.Sprintf("▸ %s %s", tog, p.displayName))
					if p.name != p.displayName {
						lbl += tuiDimStyle.Render(" (" + p.name + ")")
					}
				} else {
					lbl = value.Render(lbl)
				}
				content.WriteString("  " + lbl + "\n")
			}
		}
	} else if m.editTab == 0 {
		// Memory tab
		renderBool(0, "Enabled", m.editMemory.enabled)
		renderField(1, "MaxSizeKB", m.editMemory.maxSizeKB)
		renderField(2, "SystemPrompt", m.editMemory.systemPrompt)
	} else if m.editTab == 1 {
		// Heartbeat tab
		renderField(0, "Schedule", m.editHeartbeat.schedule)
		taskDisplay := m.editHeartbeat.task
		if taskDisplay == "" {
			taskDisplay = "(press enter to set)"
		} else {
			taskDisplay = truncate(taskDisplay, 40)
		}
		renderField(1, "Task", taskDisplay+" ⏎")
		renderField(2, "Type", "◀ "+editScheduleTypes[m.editHeartbeat.schedType]+" ▶")
		renderField(3, "Concurrency", "◀ "+editConcurrencyPolicies[m.editHeartbeat.concurrencyPolicy]+" ▶")
		renderBool(4, "IncludeMemory", m.editHeartbeat.includeMemory)
		renderBool(5, "Suspend", m.editHeartbeat.suspend)
	} else if m.editTab == 2 {
		// Skills tab
		if len(m.editSkills) == 0 {
			content.WriteString(tuiDimStyle.Render("  No SkillPacks found in the cluster.") + "\n")
			content.WriteString(tuiDimStyle.Render("  Run 'sympozium install' to install built-in skills.") + "\n")
		} else {
			content.WriteString(tuiDimStyle.Render("  Toggle skills on/off with space or enter:") + "\n\n")
			for i, sk := range m.editSkills {
				tog := "○"
				if sk.enabled {
					tog = "●"
				}
				cat := ""
				if sk.category != "" {
					cat = " (" + sk.category + ")"
				}
				host := ""
				if sk.hostReq {
					host = tuiDimStyle.Render(" [host: " + sk.hostInfo + "]")
				}
				// Show configured params inline (e.g. repo for github-gitops).
				extra := ""
				if sk.name == "memory" {
					extra = tuiDimStyle.Render(" (required)")
				} else if sk.enabled && sk.name == "github-gitops" {
					if repo, ok := sk.params["repo"]; ok && repo != "" {
						extra = tuiDimStyle.Render(" → " + repo)
					} else {
						extra = tuiDimStyle.Render(" (no repo set — press enter to configure)")
					}
				}
				lbl := fmt.Sprintf("  %s %s%s", tog, sk.name, cat)
				if m.editField == i {
					lbl = highlight.Render(fmt.Sprintf("▸ %s %s%s", tog, sk.name, cat)) + host + extra
				} else {
					lbl = value.Render(lbl) + host + extra
				}
				content.WriteString("  " + lbl + "\n")
			}
			// GitHub auth status — shown below the skill list when active
			if m.githubAuthActive {
				content.WriteString("\n")
				switch m.githubAuthStatus {
				case "checking":
					content.WriteString(tuiDimStyle.Render("  Checking GitHub auth status…") + "\n")
				case "pending":
					content.WriteString(tuiSuccessStyle.Render("  ┌─ GitHub Authorization Required ──────────────────┐") + "\n")
					content.WriteString(tuiSuccessStyle.Render(fmt.Sprintf("  │  Enter code:  %-35s│", m.githubAuthUserCode)) + "\n")
					content.WriteString(tuiSuccessStyle.Render(fmt.Sprintf("  │  at: %-44s│", m.githubAuthVerifyURL)) + "\n")
					content.WriteString(tuiSuccessStyle.Render("  │  Waiting for authorization…                     │") + "\n")
					content.WriteString(tuiSuccessStyle.Render("  └──────────────────────────────────────────────────┘") + "\n")
				case "done":
					content.WriteString(tuiSuccessStyle.Render("  ✓ "+m.githubAuthMessage) + "\n")
				case "error":
					content.WriteString(tuiErrorStyle.Render("  ✗ "+m.githubAuthMessage) + "\n")
					content.WriteString(tuiDimStyle.Render("  Press 'a' to retry auth") + "\n")
				}
			} else {
				// Nudge user if github-gitops is enabled but no auth attempted yet
				for _, sk := range m.editSkills {
					if sk.enabled && sk.name == "github-gitops" {
						content.WriteString("\n")
						content.WriteString(tuiDimStyle.Render("  ⚠  GitHub not yet authenticated — press 'a' to authorize") + "\n")
						break
					}
				}
			}
		}
	} else if m.editTab == 3 {
		// Channels tab
		if len(m.editChannels) == 0 {
			content.WriteString(tuiDimStyle.Render("  No channel types available.") + "\n")
		} else {
			content.WriteString(tuiDimStyle.Render("  Toggle channels on/off — you'll be prompted for a bot token:") + "\n\n")
			for i, ch := range m.editChannels {
				tog := "○"
				if ch.enabled {
					tog = "●"
				}
				var detail string
				if ch.chType == "whatsapp" {
					detail = tuiDimStyle.Render("QR pairing — link against the pod after saving")
				} else if ch.secretRef != "" {
					detail = ch.secretRef
				} else {
					detail = tuiDimStyle.Render("no secret")
				}
				lbl := fmt.Sprintf("  %s %s  %s", tog, ch.chType, detail)
				if m.editField == i {
					lbl = highlight.Render(fmt.Sprintf("▸ %s %s  %s", tog, ch.chType, detail))
				} else {
					lbl = value.Render(lbl)
				}
				content.WriteString("  " + lbl + "\n")
			}
		}
	} else if m.editTab == 4 {
		// Web Endpoint tab
		renderBool(0, "Enabled", m.editWebEndpoint.enabled)
		renderField(1, "Hostname", m.editWebEndpoint.hostname)
		renderField(2, "Rate Limit (rpm)", m.editWebEndpoint.rateLimit)

		// Show status (read-only) when the instance has a web endpoint status
		if m.editInstanceName != "" {
			for _, inst := range m.instances {
				if inst.Name == m.editInstanceName && inst.Status.WebEndpoint != nil {
					we := inst.Status.WebEndpoint
					content.WriteString("\n")
					content.WriteString(tuiDimStyle.Render("  ── Status ──────────────────────") + "\n")
					content.WriteString(fmt.Sprintf("  %s %s\n", label.Render("  Status:"), value.Render(we.Status)))
					if we.URL != "" {
						content.WriteString(fmt.Sprintf("  %s %s\n", label.Render("  URL:"), value.Render(we.URL)))
					}
					if we.AuthSecretName != "" {
						content.WriteString(fmt.Sprintf("  %s %s\n", label.Render("  API Key Secret:"), value.Render(we.AuthSecretName)))
					}
					break
				}
			}
		}
	} else if m.editTab == 5 {
		// Lifecycle tab
		lc := &m.editLifecycle
		fieldIdx := 0

		content.WriteString(tuiDimStyle.Render("  ── PreRun Hooks ──") + "\n")
		for i, h := range lc.preRun {
			lbl := fmt.Sprintf("  ● %s  %s", h.name, tuiDimStyle.Render(h.image))
			if m.editField == fieldIdx {
				lbl = highlight.Render(fmt.Sprintf("▸ ● %s  %s", h.name, h.image))
			} else {
				lbl = value.Render(lbl)
			}
			_ = i
			content.WriteString("  " + lbl + "\n")
			fieldIdx++
		}
		// "Add preRun hook" button.
		addPreLbl := tuiDimStyle.Render("  + Add preRun hook")
		if m.editField == fieldIdx {
			addPreLbl = highlight.Render("▸ + Add preRun hook")
		}
		content.WriteString("  " + addPreLbl + "\n\n")
		fieldIdx++

		content.WriteString(tuiDimStyle.Render("  ── PostRun Hooks ──") + "\n")
		for i, h := range lc.postRun {
			lbl := fmt.Sprintf("  ● %s  %s", h.name, tuiDimStyle.Render(h.image))
			if m.editField == fieldIdx {
				lbl = highlight.Render(fmt.Sprintf("▸ ● %s  %s", h.name, h.image))
			} else {
				lbl = value.Render(lbl)
			}
			_ = i
			content.WriteString("  " + lbl + "\n")
			fieldIdx++
		}
		// "Add postRun hook" button.
		addPostLbl := tuiDimStyle.Render("  + Add postRun hook")
		if m.editField == fieldIdx {
			addPostLbl = highlight.Render("▸ + Add postRun hook")
		}
		content.WriteString("  " + addPostLbl + "\n\n")
		fieldIdx++

		// RBAC field.
		rbacDisplay := lc.rbac
		if rbacDisplay == "" {
			rbacDisplay = "(none — press enter to configure)"
		}
		rbacLbl := fmt.Sprintf("  RBAC: %s", tuiDimStyle.Render(rbacDisplay))
		if m.editField == fieldIdx {
			rbacLbl = highlight.Render(fmt.Sprintf("▸ RBAC: %s", rbacDisplay))
		} else {
			rbacLbl = value.Render(rbacLbl)
		}
		content.WriteString("  " + rbacLbl + "\n\n")

		// Read-only env var reference.
		content.WriteString(tuiDimStyle.Render("  ── Available Environment Variables ──") + "\n")
		envVars := []struct{ name, desc, scope string }{
			{"AGENT_RUN_ID", "Unique run identifier", "all"},
			{"INSTANCE_NAME", "Agent name", "all"},
			{"AGENT_NAMESPACE", "Kubernetes namespace", "all"},
			{"AGENT_EXIT_CODE", "Exit code (postRun only)", "postRun"},
			{"AGENT_RESULT", "Agent response (postRun only)", "postRun"},
		}
		for _, ev := range envVars {
			scope := ""
			if ev.scope == "postRun" {
				scope = " (postRun)"
			}
			content.WriteString(tuiDimStyle.Render(fmt.Sprintf("  %-20s %s%s", ev.name, ev.desc, scope)) + "\n")
		}
		content.WriteString(tuiDimStyle.Render("  + custom env vars from spec.env") + "\n")
		content.WriteString("\n")
		content.WriteString(tuiDimStyle.Render("  enter=edit  d=delete  space=edit") + "\n")
	}

	// Task sub-modal overlay
	if m.editTaskInput {
		content.WriteString("\n")
		content.WriteString(tuiModalTitleStyle.Render("  Task Description"))
		content.WriteString("\n")
		tiView := m.editTaskTI.View()
		content.WriteString("  " + tiView)
		content.WriteString("\n")
		content.WriteString(tuiDimStyle.Render("  enter confirm · esc cancel"))
	} else if m.editSkillGithubInput {
		content.WriteString("\n")
		content.WriteString(tuiModalTitleStyle.Render("  GitHub Repository"))
		content.WriteString("\n")
		content.WriteString(tuiDimStyle.Render("  Enter the GitHub repository the agent should target:") + "\n")
		tiView := m.editSkillGithubTI.View()
		content.WriteString("  " + tiView)
		content.WriteString("\n")
		content.WriteString(tuiDimStyle.Render("  enter confirm · esc cancel"))
	} else if m.editChannelTokenInput {
		chName := ""
		if m.editChannelTokenIdx >= 0 && m.editChannelTokenIdx < len(m.editChannels) {
			chName = m.editChannels[m.editChannelTokenIdx].chType
		}
		content.WriteString("\n")
		content.WriteString(tuiModalTitleStyle.Render(fmt.Sprintf("  %s Bot Token", strings.ToUpper(chName[:1])+chName[1:])))
		content.WriteString("\n")
		tiView := m.editChannelTokenTI.View()
		content.WriteString("  " + tiView)
		content.WriteString("\n")
		content.WriteString(tuiDimStyle.Render("  enter confirm · esc cancel"))
	} else if m.editLifecycleHookInput {
		fieldNames := []string{"Name", "Image", "Command", "Env Vars", "RBAC Rules"}
		fieldName := fieldNames[m.editLifecycleHookField]
		content.WriteString("\n")
		content.WriteString(tuiModalTitleStyle.Render(fmt.Sprintf("  Lifecycle Hook — %s", fieldName)))
		content.WriteString("\n")
		tiView := m.editLifecycleHookTI.View()
		content.WriteString("  " + tiView)
		content.WriteString("\n")
		content.WriteString(tuiDimStyle.Render("  enter next field · esc cancel"))
	} else if m.editEnsembleName != "" {
		content.WriteString("\n")
		content.WriteString(tuiDimStyle.Render("  ↑↓ navigate · ←→ cycle heartbeat · space/enter toggle · ctrl+s apply · esc cancel"))
	} else {
		content.WriteString("\n")
		content.WriteString(tuiDimStyle.Render("  tab switch tabs · ↑↓ navigate · enter toggle/edit"))
		content.WriteString("\n")
		if m.editTab == 2 {
			content.WriteString(tuiDimStyle.Render("  ←→ cycle enums · type text fields · a auth github · ctrl+s apply · esc cancel"))
		} else {
			content.WriteString(tuiDimStyle.Render("  ←→ cycle enums · type text fields · ctrl+s apply · esc cancel"))
		}
	}

	modal := tuiModalBorderStyle.Render(content.String())
	lines := strings.Split(base, "\n")
	modalLines := strings.Split(modal, "\n")

	startRow := (len(lines) - len(modalLines)) / 2
	if startRow < 1 {
		startRow = 1
	}
	for i, ml := range modalLines {
		row := startRow + i
		if row >= 0 && row < len(lines) {
			mw := lipgloss.Width(ml)
			pad := (m.width - mw) / 2
			if pad < 0 {
				pad = 0
			}
			lines[row] = strings.Repeat(" ", pad) + ml
		}
	}
	return strings.Join(lines, "\n")
}

// ── TUI command implementations ──────────────────────────────────────────────

func tuiCreateRun(ns, instance, task string) (string, error) {
	ctx := context.Background()
	var inst sympoziumv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: instance, Namespace: ns}, &inst); err != nil {
		return "", fmt.Errorf("instance %q not found: %w", instance, err)
	}

	// Resolve auth secret and provider from instance — first AuthRef wins.
	authSecret := ""
	provider := "openai"
	if len(inst.Spec.AuthRefs) > 0 {
		authSecret = inst.Spec.AuthRefs[0].Secret
		if inst.Spec.AuthRefs[0].Provider != "" {
			provider = inst.Spec.AuthRefs[0].Provider
		}
	}
	// Infer provider from baseURL for keyless local providers (e.g., Ollama, LM Studio).
	if len(inst.Spec.AuthRefs) == 0 && inst.Spec.Agents.Default.BaseURL != "" {
		if strings.Contains(inst.Spec.Agents.Default.BaseURL, "ollama") || strings.Contains(inst.Spec.Agents.Default.BaseURL, ":11434") {
			provider = "ollama"
		} else if strings.Contains(inst.Spec.Agents.Default.BaseURL, "lm-studio") || strings.Contains(inst.Spec.Agents.Default.BaseURL, ":1234") {
			provider = "lm-studio"
		} else if strings.Contains(inst.Spec.Agents.Default.BaseURL, "llama-server") {
			provider = "llama-server"
		} else {
			provider = "custom"
		}
	}

	// Cloud providers require an API key; local providers with a baseURL do not.
	if authSecret == "" && inst.Spec.Agents.Default.BaseURL == "" {
		return "", fmt.Errorf("instance %q has no API key configured (authRefs is empty) — "+
			"activate the ensemble through the TUI onboarding wizard or add an authRef manually", instance)
	}

	runName := fmt.Sprintf("%s-run-%d", instance, time.Now().Unix())
	run := &sympoziumv1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName,
			Namespace: ns,
			Labels: map[string]string{
				"sympozium.ai/instance": instance,
			},
		},
		Spec: sympoziumv1alpha1.AgentRunSpec{
			AgentRef: instance,
			Task:     sympoziumv1alpha1.NewStringTask(task),
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:                 provider,
				Model:                    inst.Spec.Agents.Default.Model,
				BaseURL:                  inst.Spec.Agents.Default.BaseURL,
				AuthSecretRef:            authSecret,
				ProviderHeaders:          inst.Spec.Agents.Default.ProviderHeaders,
				ProviderHeadersSecretRef: inst.Spec.Agents.Default.ProviderHeadersSecretRef,
			},
			Skills:           inst.Spec.Skills,
			Timeout:          &metav1.Duration{Duration: 10 * time.Minute},
			ImagePullSecrets: inst.Spec.ImagePullSecrets,
		},
	}
	if err := k8sClient.Create(ctx, run); err != nil {
		return "", fmt.Errorf("create run: %w", err)
	}
	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Created AgentRun: %s", runName)), nil
}

// tuiCreateChatRun creates an AgentRun with conversation context prepended to the task.
func tuiCreateChatRun(ns, instance, message, conversationCtx string) (string, error) {
	// Build the full task: conversation context + current message
	var task string
	if conversationCtx != "" {
		task = conversationCtx + "---\nNow respond to the following new message:\n" + message
	} else {
		task = message
	}
	return tuiCreateRun(ns, instance, task)
}

// extractUserMessage extracts just the user's latest message from a task that
// may have conversation context prepended. This is used in the feed display
// so we show the clean message, not the full context blob.
func extractUserMessage(task string) string {
	marker := "---\nNow respond to the following new message:\n"
	if idx := strings.LastIndex(task, marker); idx >= 0 {
		return task[idx+len(marker):]
	}
	return task
}

func extractUserMessageFromTaskSpec(t *sympoziumv1alpha1.TaskSpec) string {
	if t == nil {
		return ""
	}
	return extractUserMessage(t.GetPrompt())
}

func tuiAbortRun(ns, name string) (string, error) {
	ctx := context.Background()
	var run sympoziumv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &run); err != nil {
		return "", fmt.Errorf("run %q not found: %w", name, err)
	}
	if run.Status.Phase == "Completed" || run.Status.Phase == "Failed" || run.Status.Phase == "Skipped" {
		return tuiDimStyle.Render(fmt.Sprintf("Run %s already %s", name, run.Status.Phase)), nil
	}
	if err := k8sClient.Delete(ctx, &run); err != nil {
		return "", fmt.Errorf("abort run: %w", err)
	}
	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Aborted: %s", name)), nil
}

func tuiRunStatus(ns, name string) (string, error) {
	ctx := context.Background()
	var run sympoziumv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &run); err != nil {
		return "", fmt.Errorf("run %q not found: %w", name, err)
	}
	phase := string(run.Status.Phase)
	if phase == "" {
		phase = "Pending"
	}
	pod := run.Status.PodName
	if pod == "" {
		pod = "-"
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s │ phase:%s pod:%s task:%s",
		run.Name, phase, pod, truncate(run.Spec.Task.GetPrompt(), 40)))

	// Show agent-sandbox info if present.
	if run.Status.SandboxName != "" {
		b.WriteString(fmt.Sprintf("\n  ▣ agent-sandbox: %s", run.Status.SandboxName))
		if run.Spec.AgentSandbox != nil && run.Spec.AgentSandbox.RuntimeClass != "" {
			b.WriteString(fmt.Sprintf(" (runtime: %s)", run.Spec.AgentSandbox.RuntimeClass))
		}
	}
	if run.Status.SandboxClaimName != "" {
		b.WriteString(fmt.Sprintf("\n  ▣ sandbox-claim: %s", run.Status.SandboxClaimName))
	}

	if run.Status.Result != "" {
		// Show result inline — truncate to first 2 lines, max 80 chars each.
		lines := strings.Split(strings.TrimSpace(run.Status.Result), "\n")
		shown := 0
		for _, line := range lines {
			if shown >= 2 {
				b.WriteString("\n" + tuiDimStyle.Render("  ┊ use /result "+name+" for full output"))
				break
			}
			line = strings.TrimRight(line, " \t\r")
			if len(line) > 80 {
				line = line[:77] + "..."
			}
			b.WriteString("\n" + tuiSuccessStyle.Render("  ↳ "+line))
			shown++
		}
	}
	if run.Status.TokenUsage != nil {
		u := run.Status.TokenUsage
		b.WriteString("\n" + tuiDimStyle.Render(fmt.Sprintf("  ⟠ tokens: %d in / %d out (%d total) │ tools: %d │ %dms",
			u.InputTokens, u.OutputTokens, u.TotalTokens, u.ToolCalls, u.DurationMs)))
	}
	if run.Status.PostRunJobName != "" {
		b.WriteString(fmt.Sprintf("\n  ⇢ postRun job: %s", run.Status.PostRunJobName))
	}
	// Show PostRunFailed condition if present.
	for _, cond := range run.Status.Conditions {
		if cond.Type == "PostRunFailed" && cond.Status == "True" {
			b.WriteString("\n" + tuiErrorStyle.Render("  ⚠ post-run hooks failed (agent outcome unchanged)"))
			break
		}
	}
	if run.Status.Error != "" {
		b.WriteString("\n" + tuiErrorStyle.Render("  ✗ "+run.Status.Error))
	}
	return b.String(), nil
}

func tuiClusterStatus(ns string) (string, error) {
	ctx := context.Background()
	var instances sympoziumv1alpha1.AgentList
	var runs sympoziumv1alpha1.AgentRunList
	var policies sympoziumv1alpha1.SympoziumPolicyList
	_ = k8sClient.List(ctx, &instances, client.InNamespace(ns))
	_ = k8sClient.List(ctx, &runs, client.InNamespace(ns))
	_ = k8sClient.List(ctx, &policies, client.InNamespace(ns))

	pending, running, completed, failed := 0, 0, 0, 0
	for _, r := range runs.Items {
		switch r.Status.Phase {
		case "Running":
			running++
		case "Completed", "Skipped":
			completed++
		case "Failed", "Timeout":
			failed++
		default:
			pending++
		}
	}
	return fmt.Sprintf("ns:%s │ %d inst │ %d pol │ runs: %d pending %d running %d done %d failed",
		ns, len(instances.Items), len(policies.Items), pending, running, completed, failed), nil
}

func tuiListFeatures(ns, policyName string) (string, error) {
	ctx := context.Background()
	var pol sympoziumv1alpha1.SympoziumPolicy
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: policyName, Namespace: ns}, &pol); err != nil {
		return "", fmt.Errorf("policy %q not found: %w", policyName, err)
	}
	if len(pol.Spec.FeatureGates) == 0 {
		return tuiDimStyle.Render(fmt.Sprintf("No feature gates on %s", policyName)), nil
	}
	names := make([]string, 0, len(pol.Spec.FeatureGates))
	for name := range pol.Spec.FeatureGates {
		v := "off"
		if pol.Spec.FeatureGates[name] {
			v = "on"
		}
		names = append(names, fmt.Sprintf("%s=%s", name, v))
	}
	sort.Strings(names)
	return fmt.Sprintf("%s features: %s", policyName, strings.Join(names, ", ")), nil
}

func tuiDelete(ns, resourceType, name string) (string, error) {
	ctx := context.Background()
	switch strings.ToLower(resourceType) {
	case "agent", "instance", "inst":
		obj := &sympoziumv1alpha1.Agent{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if err := k8sClient.Delete(ctx, obj); err != nil {
			return "", fmt.Errorf("delete agent: %w", err)
		}
		return tuiSuccessStyle.Render(fmt.Sprintf("✓ Deleted agent: %s", name)), nil
	case "run":
		obj := &sympoziumv1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if err := k8sClient.Delete(ctx, obj); err != nil {
			return "", fmt.Errorf("delete run: %w", err)
		}
		return tuiSuccessStyle.Render(fmt.Sprintf("✓ Deleted run: %s", name)), nil
	case "policy", "pol":
		obj := &sympoziumv1alpha1.SympoziumPolicy{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if err := k8sClient.Delete(ctx, obj); err != nil {
			return "", fmt.Errorf("delete policy: %w", err)
		}
		return tuiSuccessStyle.Render(fmt.Sprintf("✓ Deleted policy: %s", name)), nil
	case "schedule", "sched":
		obj := &sympoziumv1alpha1.SympoziumSchedule{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if err := k8sClient.Delete(ctx, obj); err != nil {
			return "", fmt.Errorf("delete schedule: %w", err)
		}
		return tuiSuccessStyle.Render(fmt.Sprintf("✓ Deleted schedule: %s", name)), nil
	case "ensemble", "persona":
		obj := &sympoziumv1alpha1.Ensemble{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if err := k8sClient.Delete(ctx, obj); err != nil {
			return "", fmt.Errorf("delete ensemble: %w", err)
		}
		return tuiSuccessStyle.Render(fmt.Sprintf("✓ Deleted Ensemble: %s (owned resources will be garbage-collected)", name)), nil
	default:
		return "", fmt.Errorf("unknown type: %s (use: agent, run, policy, schedule, ensemble, channel)", resourceType)
	}
}

func tuiAddChannel(ns, instanceName, chType, secretName string) (string, error) {
	ctx := context.Background()
	var inst sympoziumv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: ns}, &inst); err != nil {
		return "", fmt.Errorf("instance %q not found: %w", instanceName, err)
	}

	// Check if channel type already exists.
	for _, ch := range inst.Spec.Channels {
		if strings.EqualFold(ch.Type, chType) {
			return "", fmt.Errorf("channel %q already exists on %s — use /rmchannel first", chType, instanceName)
		}
	}

	channels := append(inst.Spec.Channels, sympoziumv1alpha1.ChannelSpec{
		Type: strings.ToLower(chType),
		ConfigRef: sympoziumv1alpha1.SecretRef{
			Secret: secretName,
		},
	})
	target, err := agentedit.Apply(ctx, k8sClient, &inst, agentedit.Edit{Channels: &channels})
	if err != nil {
		return "", fmt.Errorf("update instance: %w", err)
	}
	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Added %s channel to %s (secret: %s) via %s", chType, instanceName, secretName, target)), nil
}

func tuiRemoveChannel(ns, instanceName, chType string) (string, error) {
	ctx := context.Background()
	var inst sympoziumv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: ns}, &inst); err != nil {
		return "", fmt.Errorf("instance %q not found: %w", instanceName, err)
	}

	var newChannels []sympoziumv1alpha1.ChannelSpec
	found := false
	for _, ch := range inst.Spec.Channels {
		if strings.EqualFold(ch.Type, chType) {
			found = true
			continue
		}
		newChannels = append(newChannels, ch)
	}
	if !found {
		return "", fmt.Errorf("channel %q not found on instance %s", chType, instanceName)
	}

	target, err := agentedit.Apply(ctx, k8sClient, &inst, agentedit.Edit{Channels: &newChannels})
	if err != nil {
		return "", fmt.Errorf("update instance: %w", err)
	}
	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Removed %s channel from %s via %s", chType, instanceName, target)), nil
}

func tuiSetProvider(ns, instanceName, provider, model string) (string, error) {
	ctx := context.Background()
	var inst sympoziumv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: ns}, &inst); err != nil {
		return "", fmt.Errorf("instance %q not found: %w", instanceName, err)
	}

	old := inst.Spec.Agents.Default.Model
	edit := agentedit.Edit{Model: &model}
	// BaseURL is cleared when switching provider (user can set it separately with /baseurl).
	if provider != "openai-compatible" {
		cleared := ""
		edit.BaseURL = &cleared
	}

	target, err := agentedit.Apply(ctx, k8sClient, &inst, edit)
	if err != nil {
		return "", fmt.Errorf("update instance: %w", err)
	}
	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Set %s provider=%s model=%s (was: %s) via %s", instanceName, provider, model, old, target)), nil
}

func tuiCreateSchedule(ns, instanceName, cronExpr, task string) (string, error) {
	ctx := context.Background()

	// Verify instance exists.
	var inst sympoziumv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: ns}, &inst); err != nil {
		return "", fmt.Errorf("instance %q not found: %w", instanceName, err)
	}

	name := fmt.Sprintf("%s-sched-%d", instanceName, time.Now().Unix())
	sched := &sympoziumv1alpha1.SympoziumSchedule{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
			AgentRef:      instanceName,
			Schedule:      cronExpr,
			Task:          task,
			Type:          "scheduled",
			IncludeMemory: true,
		},
	}
	if err := k8sClient.Create(ctx, sched); err != nil {
		return "", fmt.Errorf("create schedule: %w", err)
	}
	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Created schedule %s (%s)", name, cronExpr)), nil
}

func tuiInstallEnsemble(ns, packName string) (string, error) {
	ctx := context.Background()

	// Check if pack already exists.
	var existing sympoziumv1alpha1.Ensemble
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: packName, Namespace: ns}, &existing); err == nil {
		return "", fmt.Errorf("Ensemble %q already exists (phase: %s, %d/%d personas installed)",
			packName, existing.Status.Phase, existing.Status.InstalledCount, existing.Status.AgentConfigCount)
	}

	// Look for a built-in pack YAML on disk. If not found, create a minimal one.
	// The user is expected to have applied the pack YAML via kubectl or sympozium install.
	return "", fmt.Errorf("Ensemble %q not found in cluster. Apply it first:\n  kubectl apply -f config/personas/%s.yaml", packName, packName)
}

func tuiDeleteEnsemble(ns, packName string) (string, error) {
	ctx := context.Background()

	var pack sympoziumv1alpha1.Ensemble
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: packName, Namespace: ns}, &pack); err != nil {
		return "", fmt.Errorf("Ensemble %q not found: %w", packName, err)
	}

	if err := k8sClient.Delete(ctx, &pack); err != nil {
		return "", fmt.Errorf("delete Ensemble %q: %w", packName, err)
	}

	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Deleted Ensemble %s (owned resources will be garbage-collected)", packName)), nil
}

func tuiDisablePackPersona(ns, packName, personaName string) (string, error) {
	ctx := context.Background()

	var pack sympoziumv1alpha1.Ensemble
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: packName, Namespace: ns}, &pack); err != nil {
		return "", fmt.Errorf("Ensemble %q not found: %w", packName, err)
	}

	// Check if already excluded.
	for _, p := range pack.Spec.ExcludeAgentConfigs {
		if p == personaName {
			return tuiDimStyle.Render(fmt.Sprintf("Agent %q is already disabled in ensemble %s", personaName, packName)), nil
		}
	}

	pack.Spec.ExcludeAgentConfigs = append(pack.Spec.ExcludeAgentConfigs, personaName)
	if err := k8sClient.Update(ctx, &pack); err != nil {
		return "", fmt.Errorf("update Ensemble %q: %w", packName, err)
	}

	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Disabled agent %q in ensemble %s (controller will clean up resources)", personaName, packName)), nil
}

func tuiDisableAllEnsembleAgents(ns, packName string, personaNames []string) (string, error) {
	ctx := context.Background()

	var pack sympoziumv1alpha1.Ensemble
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: packName, Namespace: ns}, &pack); err != nil {
		return "", fmt.Errorf("Ensemble %q not found: %w", packName, err)
	}

	// Build full exclusion list (deduplicated).
	excluded := make(map[string]bool)
	for _, e := range pack.Spec.ExcludeAgentConfigs {
		excluded[e] = true
	}
	for _, name := range personaNames {
		excluded[name] = true
	}
	pack.Spec.ExcludeAgentConfigs = make([]string, 0, len(excluded))
	for name := range excluded {
		pack.Spec.ExcludeAgentConfigs = append(pack.Spec.ExcludeAgentConfigs, name)
	}

	if err := k8sClient.Update(ctx, &pack); err != nil {
		return "", fmt.Errorf("update Ensemble %q: %w", packName, err)
	}

	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Disabled all %d personas in pack %s (controller will clean up resources)", len(personaNames), packName)), nil
}

func tuiShowMemory(ns, instanceName string) (string, error) {
	ctx := context.Background()

	cmName := fmt.Sprintf("%s-memory", instanceName)
	var cm corev1.ConfigMap
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: cmName, Namespace: ns}, &cm); err != nil {
		return "", fmt.Errorf("memory ConfigMap %q not found (is memory enabled?): %w", cmName, err)
	}

	content := cm.Data["MEMORY.md"]
	if content == "" {
		return tuiDimStyle.Render(fmt.Sprintf("Memory for %s: (empty)", instanceName)), nil
	}

	// Show a preview in the log pane.
	lines := strings.Split(content, "\n")
	preview := content
	if len(lines) > 20 {
		preview = strings.Join(lines[:20], "\n") + "\n... (truncated)"
	}
	return fmt.Sprintf("Memory for %s:\n%s", instanceName, preview), nil
}

func tuiSetBaseURL(ns, instanceName, baseURL string) (string, error) {
	ctx := context.Background()
	var inst sympoziumv1alpha1.Agent
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: ns}, &inst); err != nil {
		return "", fmt.Errorf("instance %q not found: %w", instanceName, err)
	}

	old := inst.Spec.Agents.Default.BaseURL
	if old == "" {
		old = "(default)"
	}
	target, err := agentedit.Apply(ctx, k8sClient, &inst, agentedit.Edit{BaseURL: &baseURL})
	if err != nil {
		return "", fmt.Errorf("update instance: %w", err)
	}
	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Set %s baseURL=%s (was: %s) via %s", instanceName, baseURL, old, target)), nil
}

func tuiDeletePod(ns, podName string) (string, error) {
	ctx := context.Background()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: ns}}
	if err := k8sClient.Delete(ctx, pod); err != nil {
		return "", fmt.Errorf("delete pod: %w", err)
	}
	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Deleted pod: %s", podName)), nil
}

func tuiPodLogs(ns, podName string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Use kubectl for streaming-friendly log retrieval.
	cmd := exec.CommandContext(ctx, "kubectl", "logs", podName, "-n", ns, "--tail=50")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("logs %s: %s", podName, strings.TrimSpace(string(out)))
	}
	lines := strings.TrimSpace(string(out))
	if lines == "" {
		return tuiDimStyle.Render(fmt.Sprintf("(no logs for %s)", podName)), nil
	}
	// Show last few lines in the log pane, stripping internal markers.
	parts := strings.Split(lines, "\n")
	var filtered []string
	for _, p := range parts {
		if strings.Contains(p, "__SYMPOZIUM_RESULT__") || strings.Contains(p, "__SYMPOZIUM_END__") {
			continue
		}
		filtered = append(filtered, p)
	}
	if len(filtered) > 15 {
		filtered = filtered[len(filtered)-15:]
	}
	header := tuiHeaderStyle.Render(fmt.Sprintf("── logs: %s ──", podName))
	return header + "\n" + strings.Join(filtered, "\n"), nil
}

// pollWhatsAppQRCmd returns a tea.Cmd that polls for the WhatsApp channel pod
// and extracts the QR code from its logs. It sleeps between polls.
func pollWhatsAppQRCmd(ns, instanceName string) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(3 * time.Second)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		// Find the WhatsApp channel pod by labels
		selector := fmt.Sprintf("sympozium.ai/instance=%s,sympozium.ai/channel=whatsapp,sympozium.ai/component=channel", instanceName)
		cmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-l", selector, "-n", ns,
			"-o", "jsonpath={.items[0].metadata.name},{.items[0].status.phase}")
		out, err := cmd.CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) == "" {
			return whatsappQRPollMsg{status: "waiting", err: nil}
		}

		parts := strings.SplitN(strings.TrimSpace(string(out)), ",", 2)
		podName := parts[0]
		phase := ""
		if len(parts) > 1 {
			phase = parts[1]
		}

		if phase != "Running" {
			return whatsappQRPollMsg{status: fmt.Sprintf("waiting (pod %s)", phase)}
		}

		// Get pod logs
		logCmd := exec.CommandContext(ctx, "kubectl", "logs", podName, "-n", ns, "--tail=80")
		logOut, err := logCmd.CombinedOutput()
		if err != nil {
			return whatsappQRPollMsg{status: "waiting (reading logs...)", err: nil}
		}

		logStr := string(logOut)

		// Check if already linked
		if strings.Contains(logStr, "linked successfully") || strings.Contains(logStr, "connected with existing session") {
			return whatsappQRPollMsg{linked: true, status: "linked"}
		}

		// Extract QR code block — look for the box header and the QR block characters
		lines := strings.Split(logStr, "\n")
		var qrLines []string
		inQR := false
		for _, line := range lines {
			if strings.Contains(line, "Scan this QR code") {
				inQR = true
				qrLines = append(qrLines, line)
				continue
			}
			if inQR {
				qrLines = append(qrLines, line)
				// End of QR block — look for empty line after block chars
				if strings.TrimSpace(line) == "" && len(qrLines) > 5 {
					break
				}
			}
		}

		if len(qrLines) > 0 {
			return whatsappQRPollMsg{qrLines: qrLines, status: "scanning"}
		}

		return whatsappQRPollMsg{status: "waiting (initializing...)"}
	}
}

// waitForWhatsAppPod polls for the WhatsApp channel pod to become available.
// Returns the pod name if found within ~30s, or empty string on timeout.
func waitForWhatsAppPod(ns, instanceName string) string {
	selector := fmt.Sprintf("sympozium.ai/instance=%s,sympozium.ai/channel=whatsapp,sympozium.ai/component=channel", instanceName)
	for i := 0; i < 10; i++ {
		time.Sleep(3 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, "kubectl", "get", "pods", "-l", selector, "-n", ns,
			"-o", "jsonpath={.items[0].metadata.name}")
		out, err := cmd.CombinedOutput()
		cancel()
		podName := strings.TrimSpace(string(out))
		if err == nil && podName != "" && podName != "{}" {
			return podName
		}
	}
	return ""
}

func tuiDescribeResource(ns, kind, name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "describe", kind, name, "-n", ns)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("describe %s/%s: %s", kind, name, strings.TrimSpace(string(out)))
	}
	lines := strings.TrimSpace(string(out))
	if lines == "" {
		return tuiDimStyle.Render(fmt.Sprintf("(empty describe for %s/%s)", kind, name)), nil
	}
	// Show a summary — last 20 lines (events are at the bottom).
	parts := strings.Split(lines, "\n")
	if len(parts) > 20 {
		parts = parts[len(parts)-20:]
	}
	header := tuiHeaderStyle.Render(fmt.Sprintf("── describe: %s/%s ──", kind, name))
	return header + "\n" + strings.Join(parts, "\n"), nil
}

func tuiResourceEvents(ns, kind, name string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "kubectl", "get", "events", "-n", ns,
		"--field-selector", fmt.Sprintf("involvedObject.name=%s,involvedObject.kind=%s", name, kind),
		"--sort-by=.lastTimestamp",
		"-o", "custom-columns=TIME:.lastTimestamp,TYPE:.type,REASON:.reason,MESSAGE:.message",
		"--no-headers")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("events %s/%s: %s", kind, name, strings.TrimSpace(string(out)))
	}
	lines := strings.TrimSpace(string(out))
	if lines == "" {
		return tuiDimStyle.Render(fmt.Sprintf("(no events for %s/%s)", kind, name)), nil
	}
	parts := strings.Split(lines, "\n")
	if len(parts) > 15 {
		parts = parts[len(parts)-15:]
	}
	header := tuiHeaderStyle.Render(fmt.Sprintf("── events: %s/%s ──", kind, name))
	return header + "\n" + strings.Join(parts, "\n"), nil
}

// ── Onboard Wizard Logic ─────────────────────────────────────────────────────

// advanceWizard processes the user's input for the current wizard step and
// moves to the next step. It is the state-machine core of the TUI wizard.
func (m tuiModel) advanceWizard(val string) (tea.Model, tea.Cmd) {
	w := &m.wizard
	w.scrollOffset = 0 // reset scroll when advancing steps

	switch w.step {
	case wizStepCheckCluster:
		// Auto step — verify CRDs are reachable.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var instances sympoziumv1alpha1.AgentList
		if err := k8sClient.List(ctx, &instances, client.InNamespace(m.namespace)); err != nil {
			w.err = "CRDs not found — run 'sympozium install' first"
			w.active = false
			m.inputFocused = false
			m.input.Blur()
			m.input.Placeholder = "Type / for commands or press ? for help..."
			m.addLog(tuiErrorStyle.Render("✗ Onboard: " + w.err))
			return m, nil
		}
		w.err = ""
		w.step = wizStepNamespace
		m.input.Placeholder = fmt.Sprintf("Namespace (default: %s)", m.namespace)
		return m, nil

	case wizStepNamespace:
		if val == "" {
			val = m.namespace
		}
		w.targetNamespace = val
		w.step = wizStepInstanceName
		m.input.Placeholder = "Instance name (default: my-agent)"
		return m, nil

	case wizStepInstanceName:
		if val == "" {
			val = "my-agent"
		}
		w.instanceName = val
		w.step = wizStepProvider
		m.input.Placeholder = "Choice [1-8] (default: 1 — OpenAI)"
		return m, nil

	case wizStepProvider:
		if val == "" {
			val = "1"
		}
		w.providerChoice = val
		switch val {
		case "2":
			w.providerName = "anthropic"
			w.secretEnvKey = "ANTHROPIC_API_KEY"
			// Collect API key before model so we can fetch models.
			w.step = wizStepAPIKey
			m.input.Placeholder = fmt.Sprintf("%s (paste key, Enter to skip)", w.secretEnvKey)
			return m, nil
		case "3":
			w.providerName = "azure-openai"
			w.secretEnvKey = "AZURE_OPENAI_API_KEY"
			w.step = wizStepBaseURL
			m.input.Placeholder = "Azure OpenAI endpoint URL"
			return m, nil
		case "4":
			w.providerName = "ollama"
			w.secretEnvKey = ""
			w.step = wizStepBaseURL
			m.input.Placeholder = "Ollama URL (default: http://ollama.default.svc:11434/v1)"
			return m, nil
		case "5":
			w.providerName = "lm-studio"
			w.secretEnvKey = ""
			w.step = wizStepBaseURL
			m.input.Placeholder = "LM Studio URL (default: http://localhost:1234/v1)"
			return m, nil
		case "6":
			w.providerName = "llama-server"
			w.secretEnvKey = ""
			w.step = wizStepBaseURL
			m.input.Placeholder = "llama-server URL (default: http://localhost:8080/v1)"
			return m, nil
		case "7":
			w.providerName = "bedrock"
			w.secretEnvKey = ""
			w.step = wizStepAWSRegion
			m.input.Placeholder = "AWS Region (default: us-east-1)"
			return m, nil
		case "8":
			w.providerName = "custom"
			w.secretEnvKey = "API_KEY"
			w.step = wizStepBaseURL
			m.input.Placeholder = "API base URL (empty for default)"
			return m, nil
		default:
			w.providerName = "openai"
			w.secretEnvKey = "OPENAI_API_KEY"
			// Collect API key before model so we can fetch models.
			w.step = wizStepAPIKey
			m.input.Placeholder = fmt.Sprintf("%s (paste key, Enter to skip)", w.secretEnvKey)
			return m, nil
		}

	case wizStepBaseURL:
		if val == "" && w.providerName == "ollama" {
			val = "http://ollama.default.svc:11434/v1"
		}
		if val == "" && w.providerName == "lm-studio" {
			val = "http://localhost:1234/v1"
		}
		if val == "" && w.providerName == "llama-server" {
			val = "http://localhost:8080/v1"
		}
		w.baseURL = val
		if w.providerName == "lm-studio" {
			// LM Studio — ask if API key is required.
			w.step = wizStepLMStudioAPIKeyRequired
			m.input.Placeholder = "Does LM Studio require an API key? [Y/n]"
			return m, nil
		}
		if w.providerName == "llama-server" {
			// llama-server — ask if API key is required.
			w.step = wizStepLlamaServerAPIKeyRequired
			m.input.Placeholder = "Does llama-server require an API key? [Y/n]"
			return m, nil
		}
		if w.secretEnvKey == "" {
			// Ollama — no API key, go straight to model.
			w.step = wizStepModel
			m.input.Placeholder = "Model name (default: llama3)"
			return m, nil
		}
		// Providers that need a key after base URL (azure, custom).
		w.step = wizStepAPIKey
		m.input.Placeholder = fmt.Sprintf("%s (paste key, Enter to skip)", w.secretEnvKey)
		return m, nil

	case wizStepLMStudioAPIKeyRequired:
		w.step = wizStepModel // default fallback
		switch strings.ToLower(val) {
		case "y", "yes":
			w.secretEnvKey = "API_KEY"
			w.step = wizStepAPIKey
			m.input.Placeholder = "Please enter the API key for LM Studio:"
		default:
			// User skips API key - show warning
			m.addLog(tuiErrorStyle.Render("⚠  Warning: Ensure your LM Studio server is running without authentication"))
			w.step = wizStepModel
			m.input.Placeholder = "Model name (default: llama3)"
		}
		return m, nil

	case wizStepLlamaServerAPIKeyRequired:
		w.step = wizStepModel // default fallback
		switch strings.ToLower(val) {
		case "y", "yes":
			w.secretEnvKey = "API_KEY"
			w.step = wizStepAPIKey
			m.input.Placeholder = "Please enter the API key for llama-server:"
		default:
			// User skips API key - show warning
			m.addLog(tuiErrorStyle.Render("⚠  Warning: Ensure your llama-server is running without authentication"))
			w.step = wizStepModel
			m.input.Placeholder = "Model name (default: llama3)"
		}
		return m, nil

	case wizStepAWSRegion:
		if val == "" {
			val = "us-east-1"
		}
		w.awsRegion = val
		w.step = wizStepAWSAccessKeyID
		m.input.Placeholder = "AWS Access Key ID (Enter to skip for IRSA/pod identity)"
		return m, nil

	case wizStepAWSAccessKeyID:
		w.awsAccessKeyID = val
		modelStep := wizStepModel
		if w.personaMode {
			modelStep = wizStepPersonaModel
		}
		if val == "" {
			// No static credentials — IRSA or pod identity will be used.
			w.step = modelStep
			m.input.Placeholder = "Model ID (default: anthropic.claude-sonnet-4-20250514-v1:0)"
			return m, nil
		}
		w.step = wizStepAWSSecretAccessKey
		m.input.Placeholder = "AWS Secret Access Key"
		m.input.EchoMode = textinput.EchoPassword
		return m, nil

	case wizStepAWSSecretAccessKey:
		w.awsSecretAccessKey = val
		m.input.EchoMode = textinput.EchoNormal
		w.step = wizStepAWSSessionToken
		m.input.Placeholder = "AWS Session Token (optional, Enter to skip)"
		return m, nil

	case wizStepAWSSessionToken:
		w.awsSessionToken = val
		if w.personaMode {
			w.step = wizStepPersonaModel
		} else {
			w.step = wizStepModel
		}
		m.input.Placeholder = "Model ID (default: anthropic.claude-sonnet-4-20250514-v1:0)"
		return m, nil

	case wizStepAPIKey:
		w.apiKey = val
		// Fall back to environment variable if no key was pasted.
		if w.apiKey == "" && w.secretEnvKey != "" {
			w.apiKey = os.Getenv(w.secretEnvKey)
		}
		// Try to fetch models from the provider API.
		w.fetchedModels = nil
		w.modelFetchErr = ""
		if w.apiKey != "" {
			models, err := fetchProviderModels(w.providerName, w.apiKey, w.baseURL)
			if err != nil {
				w.modelFetchErr = err.Error()
			} else {
				filtered := filterChatModels(models)
				if len(filtered) > 0 {
					w.fetchedModels = filtered
				} else {
					w.fetchedModels = models
				}
			}
		}
		w.step = wizStepModel
		if len(w.fetchedModels) > 0 {
			m.input.Placeholder = "Choose a model [number] or type a name"
		} else {
			switch w.providerName {
			case "anthropic":
				m.input.Placeholder = "Model name (default: claude-sonnet-4-20250514)"
			case "azure-openai":
				m.input.Placeholder = "Deployment name (default: gpt-4o)"
			default:
				m.input.Placeholder = "Model name (default: gpt-4o)"
			}
		}
		return m, nil

	case wizStepModel:
		if val == "" {
			switch w.providerName {
			case "anthropic":
				val = "claude-sonnet-4-20250514"
			case "bedrock":
				val = "anthropic.claude-sonnet-4-20250514-v1:0"
			case "ollama":
				val = "llama3"
			default:
				val = "gpt-4o"
			}
		} else if len(w.fetchedModels) > 0 {
			// If the user entered a number, resolve it from the fetched list.
			if idx, err := strconv.Atoi(val); err == nil && idx >= 1 && idx <= len(w.fetchedModels) {
				val = w.fetchedModels[idx-1]
			}
		} else {
			// No fetched models — try resolving number from static suggestions.
			if suggestions, ok := modelSuggestions[w.providerName]; ok {
				if idx, err := strconv.Atoi(val); err == nil && idx >= 1 && idx <= len(suggestions) {
					val = suggestions[idx-1].text
				}
			}
		}
		w.modelName = val
		w.step = wizStepGithubRepo
		m.input.Placeholder = "GitHub repo owner/repo (Enter to skip)"
		return m, nil

	case wizStepGithubRepo:
		w.githubRepo = strings.TrimSpace(val)
		w.step = wizStepTeamTask
		m.input.Placeholder = "What should the agent work on? (Enter to skip)"
		return m, nil

	case wizStepTeamTask:
		w.teamTask = strings.TrimSpace(val)
		w.step = wizStepChannel
		m.input.Placeholder = "Channel [1-5] (default: 5 — skip)"
		return m, nil

	case wizStepChannel:
		if val == "" {
			val = "5"
		}
		w.channelChoice = val
		switch val {
		case "1":
			w.channelType = "telegram"
			w.channelTokenKey = "TELEGRAM_BOT_TOKEN"
			w.step = wizStepChannelToken
			m.input.Placeholder = "Telegram Bot Token"
			return m, nil
		case "2":
			w.channelType = "slack"
			w.channelTokenKey = "SLACK_BOT_TOKEN"
			w.step = wizStepChannelToken
			m.input.Placeholder = "Slack Bot OAuth Token"
			return m, nil
		case "3":
			w.channelType = "discord"
			w.channelTokenKey = "DISCORD_BOT_TOKEN"
			w.step = wizStepChannelToken
			m.input.Placeholder = "Discord Bot Token"
			return m, nil
		case "4":
			w.channelType = "whatsapp"
			w.channelTokenKey = "" // WhatsApp uses QR pairing, no token needed
			// Skip token step — go straight to policy
			w.step = wizStepPolicy
			m.input.Placeholder = "Apply default policy? [Y/n]"
			return m, nil
		default:
			w.channelType = ""
		}
		w.step = wizStepPolicy
		m.input.Placeholder = "Apply default policy? [Y/n]"
		return m, nil

	case wizStepChannelToken:
		w.channelToken = val
		w.step = wizStepPolicy
		m.input.Placeholder = "Apply default policy? [Y/n]"
		return m, nil

	case wizStepPolicy:
		v := strings.ToLower(val)
		w.applyPolicy = (v == "" || v == "y" || v == "yes")
		w.step = wizStepAgentSandbox
		m.input.Placeholder = "Enable Agent Sandbox? [y/N]"
		return m, nil

	case wizStepAgentSandbox:
		v := strings.ToLower(val)
		w.agentSandboxEnabled = (v == "y" || v == "yes")
		w.step = wizStepRunTimeout
		m.input.Placeholder = "Run timeout [1-4] (default: 1 — provider default)"
		return m, nil

	case wizStepRunTimeout:
		if val == "" {
			val = "1"
		}
		switch val {
		case "2":
			w.runTimeout = "30m"
		case "3":
			w.runTimeout = "1h"
		case "4":
			w.runTimeout = "2h"
		default:
			w.runTimeout = "" // provider default
		}
		w.step = wizStepHeartbeat
		m.input.Placeholder = "Heartbeat interval [1-5] (default: 2 — every hour)"
		return m, nil

	case wizStepHeartbeat:
		if val == "" {
			val = "2"
		}
		switch val {
		case "1":
			w.heartbeatCron = "*/30 * * * *"
			w.heartbeatLabel = "every 30 minutes"
		case "3":
			w.heartbeatCron = "0 */6 * * *"
			w.heartbeatLabel = "every 6 hours"
		case "4":
			w.heartbeatCron = "0 9 * * *"
			w.heartbeatLabel = "once a day (9am)"
		case "5":
			w.heartbeatCron = ""
			w.heartbeatLabel = "disabled"
		default: // "2" or anything else
			w.heartbeatCron = "0 * * * *"
			w.heartbeatLabel = "every hour"
		}
		w.step = wizStepConfirm
		m.input.Placeholder = "Proceed? [Y/n]"
		return m, nil

	case wizStepConfirm:
		v := strings.ToLower(val)
		if v == "n" || v == "no" {
			w.reset()
			m.inputFocused = false
			m.input.Blur()
			m.input.Placeholder = "Type / for commands or press ? for help..."
			m.addLog(tuiDimStyle.Render("Onboard wizard cancelled"))
			return m, nil
		}
		w.step = wizStepApplying
		ns := w.targetNamespace
		return m, m.asyncCmd(func() (string, error) {
			return tuiOnboardApply(ns, w)
		})

	case wizStepDone:
		// User pressed Enter on final screen — close wizard.
		// Switch TUI namespace to match the one used during onboarding.
		if w.targetNamespace != "" && w.targetNamespace != m.namespace {
			m.namespace = w.targetNamespace
		}
		w.reset()
		m.inputFocused = false
		m.input.Blur()
		m.input.Placeholder = "Type / for commands or press ? for help..."
		return m, refreshDataCmd(m.namespace)

	// ── Persona Wizard Steps ─────────────────────────────────────────────
	case wizStepPersonaPick:
		if val == "" {
			// No selection yet — show available packs.
			m.input.Placeholder = "Pack name or number"
			return m, nil
		}
		// Resolve number to name.
		if idx, err := strconv.Atoi(val); err == nil {
			packs := m.ensembles
			if idx >= 1 && idx <= len(packs) {
				val = packs[idx-1].Name
			}
		}
		// Verify pack exists in cluster.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var pack sympoziumv1alpha1.Ensemble
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: val, Namespace: m.namespace}, &pack); err != nil {
			w.err = fmt.Sprintf("Ensemble %q not found in cluster. Have you run 'sympozium install'?", val)
			return m, nil
		}
		w.err = ""
		w.ensembleName = val

		// If already activated, allow re-running the wizard to change auth/model settings.
		if len(pack.Spec.AuthRefs) > 0 && pack.Status.Phase == "Ready" {
			m.addLog(tuiDimStyle.Render(fmt.Sprintf("Ensemble %q is already activated — re-running wizard to update auth/model settings", val)))
		}

		w.step = wizStepPersonaProvider
		m.input.Placeholder = "Choice [1-8] (default: 1 — OpenAI)"
		return m, nil

	case wizStepPersonaProvider:
		if val == "" {
			val = "1"
		}
		w.providerChoice = val
		switch val {
		case "2":
			w.providerName = "anthropic"
			w.secretEnvKey = "ANTHROPIC_API_KEY"
			w.step = wizStepPersonaAPIKey
			m.input.Placeholder = fmt.Sprintf("%s (paste key, Enter to skip)", w.secretEnvKey)
			return m, nil
		case "3":
			w.providerName = "azure-openai"
			w.secretEnvKey = "AZURE_OPENAI_API_KEY"
			w.step = wizStepPersonaBaseURL
			m.input.Placeholder = "Azure OpenAI endpoint URL"
			return m, nil
		case "4":
			w.providerName = "ollama"
			w.secretEnvKey = ""
			w.step = wizStepPersonaBaseURL
			m.input.Placeholder = "Ollama URL (default: http://ollama.default.svc:11434/v1)"
			return m, nil
		case "5":
			w.providerName = "lm-studio"
			w.secretEnvKey = ""
			w.step = wizStepPersonaBaseURL
			m.input.Placeholder = "LM Studio URL (default: http://localhost:1234/v1)"
			return m, nil
		case "6":
			w.providerName = "llama-server"
			w.secretEnvKey = ""
			w.step = wizStepPersonaBaseURL
			m.input.Placeholder = "llama-server URL (default: http://localhost:8080/v1)"
			return m, nil
		case "7":
			w.providerName = "bedrock"
			w.secretEnvKey = ""
			w.step = wizStepAWSRegion
			m.input.Placeholder = "AWS Region (default: us-east-1)"
			return m, nil
		case "8":
			w.providerName = "custom"
			w.secretEnvKey = "API_KEY"
			w.step = wizStepPersonaBaseURL
			m.input.Placeholder = "API base URL"
			return m, nil
		default:
			w.providerName = "openai"
			w.secretEnvKey = "OPENAI_API_KEY"
			w.step = wizStepPersonaAPIKey
			m.input.Placeholder = fmt.Sprintf("%s (paste key, Enter to skip)", w.secretEnvKey)
			return m, nil
		}

	case wizStepPersonaBaseURL:
		if val == "" && w.providerName == "ollama" {
			val = "http://ollama.default.svc:11434/v1"
		}
		if val == "" && w.providerName == "lm-studio" {
			val = "http://localhost:1234/v1"
		}
		if val == "" && w.providerName == "llama-server" {
			val = "http://localhost:8080/v1"
		}
		w.baseURL = val
		if w.providerName == "lm-studio" {
			// LM Studio — ask if API key is required.
			w.step = wizStepPersonaLMStudioAPIKeyRequired
			m.input.Placeholder = "Does LM Studio require an API key? [Y/n]"
			return m, nil
		}
		if w.providerName == "llama-server" {
			// llama-server — ask if API key is required.
			w.step = wizStepPersonaLlamaServerAPIKeyRequired
			m.input.Placeholder = "Does llama-server require an API key? [Y/n]"
			return m, nil
		}
		if w.secretEnvKey == "" {
			// Ollama — no key needed, skip to model.
			w.step = wizStepPersonaModel
			m.input.Placeholder = "Model name (default: llama3)"
			return m, nil
		}
		w.step = wizStepPersonaAPIKey
		m.input.Placeholder = fmt.Sprintf("%s (paste key, Enter to skip)", w.secretEnvKey)
		return m, nil

	case wizStepPersonaLMStudioAPIKeyRequired:
		w.step = wizStepPersonaModel // default fallback
		switch strings.ToLower(val) {
		case "y", "yes":
			w.secretEnvKey = "API_KEY"
			w.step = wizStepPersonaAPIKey
			m.input.Placeholder = "Please enter the API key for LM Studio:"
		default:
			// User skips API key - show warning
			m.addLog(tuiErrorStyle.Render("⚠  Warning: Ensure your LM Studio server is running without authentication"))
			w.step = wizStepPersonaModel
			m.input.Placeholder = "Model name (default: llama3)"
		}
		return m, nil

	case wizStepPersonaLlamaServerAPIKeyRequired:
		w.step = wizStepPersonaModel // default fallback
		switch strings.ToLower(val) {
		case "y", "yes":
			w.secretEnvKey = "API_KEY"
			w.step = wizStepPersonaAPIKey
			m.input.Placeholder = "Please enter the API key for llama-server:"
		default:
			// User skips API key - show warning
			m.addLog(tuiErrorStyle.Render("⚠  Warning: Ensure your llama-server is running without authentication"))
			w.step = wizStepPersonaModel
			m.input.Placeholder = "Model name (default: llama3)"
		}
		return m, nil

	case wizStepPersonaAPIKey:
		w.apiKey = val
		if w.apiKey == "" && w.secretEnvKey != "" {
			w.apiKey = os.Getenv(w.secretEnvKey)
		}
		// Try to fetch models.
		w.fetchedModels = nil
		w.modelFetchErr = ""
		if w.apiKey != "" {
			models, err := fetchProviderModels(w.providerName, w.apiKey, w.baseURL)
			if err != nil {
				w.modelFetchErr = err.Error()
			} else {
				filtered := filterChatModels(models)
				if len(filtered) > 0 {
					w.fetchedModels = filtered
				} else {
					w.fetchedModels = models
				}
			}
		}
		w.step = wizStepPersonaModel
		if len(w.fetchedModels) > 0 {
			m.input.Placeholder = "Choose a model [number] or type a name"
		} else {
			switch w.providerName {
			case "anthropic":
				m.input.Placeholder = "Model name (default: claude-sonnet-4-20250514)"
			case "azure-openai":
				m.input.Placeholder = "Deployment name (default: gpt-4o)"
			default:
				m.input.Placeholder = "Model name (default: gpt-4o)"
			}
		}
		return m, nil

	case wizStepPersonaModel:
		if val == "" {
			switch w.providerName {
			case "anthropic":
				val = "claude-sonnet-4-20250514"
			case "bedrock":
				val = "anthropic.claude-sonnet-4-20250514-v1:0"
			case "ollama":
				val = "llama3"
			default:
				val = "gpt-4o"
			}
		} else if len(w.fetchedModels) > 0 {
			if idx, err := strconv.Atoi(val); err == nil && idx >= 1 && idx <= len(w.fetchedModels) {
				val = w.fetchedModels[idx-1]
			}
		} else {
			if suggestions, ok := modelSuggestions[w.providerName]; ok {
				if idx, err := strconv.Atoi(val); err == nil && idx >= 1 && idx <= len(suggestions) {
					val = suggestions[idx-1].text
				}
			}
		}
		w.modelName = val
		// Check if any persona in the pack uses github-gitops.
		hasGithub := false
		for _, pp := range m.ensembles {
			if pp.Name == w.ensembleName {
				for _, p := range pp.Spec.AgentConfigs {
					for _, sk := range p.Skills {
						if sk == "github-gitops" {
							hasGithub = true
							break
						}
					}
					if hasGithub {
						break
					}
				}
				break
			}
		}
		if hasGithub {
			w.step = wizStepPersonaGithubRepo
			m.input.Placeholder = "GitHub repo owner/repo (e.g. myorg/myapp)"
			return m, nil
		}
		w.step = wizStepPersonaTeamTask
		m.input.Placeholder = "Instructions for the team (Enter to use defaults)"
		return m, nil

	case wizStepPersonaGithubRepo:
		w.githubRepo = strings.TrimSpace(val)
		w.step = wizStepPersonaTeamTask
		m.input.Placeholder = "Instructions for the team (Enter to use defaults)"
		return m, nil

	case wizStepPersonaTeamTask:
		w.teamTask = strings.TrimSpace(val)
		w.step = wizStepPersonaAgentSandbox
		m.input.Placeholder = "Enable Agent Sandbox? [y/N]"
		return m, nil

	case wizStepPersonaAgentSandbox:
		v := strings.ToLower(val)
		w.agentSandboxEnabled = (v == "y" || v == "yes")
		w.step = wizStepPersonaChannels
		m.input.Placeholder = "Toggle channels with number, Enter when done"
		return m, nil

	case wizStepPersonaChannels:
		val = strings.TrimSpace(val)
		if val == "" {
			// Done selecting channels — collect tokens for enabled channels.
			w.personaChannelIdx = 0
			return m.advancePersonaChannelToken()
		}
		// Toggle a channel by number.
		if idx, err := strconv.Atoi(val); err == nil && idx >= 1 && idx <= len(w.personaChannels) {
			w.personaChannels[idx-1].enabled = !w.personaChannels[idx-1].enabled
		}
		m.input.SetValue("")
		m.input.Placeholder = "Toggle channels with number, Enter when done"
		return m, nil

	case wizStepPersonaChannelToken:
		// Store token for current channel.
		if w.personaChannelIdx < len(w.personaChannels) {
			w.personaChannels[w.personaChannelIdx].token = val
		}
		w.personaChannelIdx++
		return m.advancePersonaChannelToken()

	case wizStepPersonaHeartbeat:
		if val == "" {
			val = "2"
		}
		switch val {
		case "1":
			w.heartbeatCron = "*/30 * * * *"
			w.heartbeatLabel = "every 30 minutes"
		case "3":
			w.heartbeatCron = "0 */6 * * *"
			w.heartbeatLabel = "every 6 hours"
		case "4":
			w.heartbeatCron = "0 0 * * *"
			w.heartbeatLabel = "once a day (midnight)"
		case "5":
			w.heartbeatCron = ""
			w.heartbeatLabel = "pack default"
		default: // "2" or anything else
			w.heartbeatCron = "0 * * * *"
			w.heartbeatLabel = "every hour"
		}
		w.step = wizStepPersonaConfirm
		m.input.Placeholder = "Proceed? [Y/n]"
		return m, nil

	case wizStepPersonaConfirm:
		v := strings.ToLower(val)
		if v == "n" || v == "no" {
			w.reset()
			m.inputFocused = false
			m.input.Blur()
			m.input.Placeholder = "Type / for commands or press ? for help..."
			m.addLog(tuiDimStyle.Render("Persona wizard cancelled"))
			return m, nil
		}
		w.step = wizStepPersonaApplying
		ns := m.namespace
		return m, m.asyncCmd(func() (string, error) {
			return tuiPersonaApply(ns, w)
		})

	case wizStepPersonaDone:
		w.reset()
		m.inputFocused = false
		m.input.Blur()
		m.input.Placeholder = "Type / for commands or press ? for help..."
		// Switch to Agents view so user sees the newly created agents.
		m.activeView = viewAgents
		m.selectedRow = 0
		m.tableScroll = 0
		return m, refreshDataCmd(m.namespace)
	}

	return m, nil
}

// advancePersonaChannelToken skips to the next enabled channel that needs
// a token, or advances to confirm once all tokens are collected.
func (m tuiModel) advancePersonaChannelToken() (tea.Model, tea.Cmd) {
	w := &m.wizard
	for w.personaChannelIdx < len(w.personaChannels) {
		ch := w.personaChannels[w.personaChannelIdx]
		if ch.enabled && ch.tokenKey != "" {
			// This channel needs a token.
			w.step = wizStepPersonaChannelToken
			m.input.SetValue("")
			m.input.Placeholder = fmt.Sprintf("%s token (%s)", ch.chType, ch.tokenKey)
			return m, nil
		}
		w.personaChannelIdx++
	}
	// All tokens collected — proceed to heartbeat interval.
	w.step = wizStepPersonaHeartbeat
	m.input.Placeholder = "Heartbeat interval [1-5] (default: 2 — every hour)"
	return m, nil
}

// renderWizardPanel renders the full wizard overlay panel.
func (m tuiModel) renderWizardPanel(h int) string {
	w := &m.wizard

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e8562a"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0ece4")).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))
	menuStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0ece4"))
	menuNumStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#e8562a")).Bold(true)
	hintStyle := tuiDimStyle
	stepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#d4cfc6")).Bold(true)

	// Persona wizard has its own renderer.
	if w.personaMode {
		return m.renderPersonaWizardPanel(h, titleStyle, labelStyle, valueStyle, menuStyle, menuNumStyle, hintStyle, stepStyle)
	}

	var lines []string
	lines = append(lines, "")
	lines = append(lines, titleStyle.Render("  ╔═══════════════════════════════════════════╗"))
	lines = append(lines, titleStyle.Render("  ║         Sympozium · Onboarding Wizard       ║"))
	lines = append(lines, titleStyle.Render("  ╚═══════════════════════════════════════════╝"))
	lines = append(lines, "")

	// Show completed values as a recap.
	stepNum := 1
	if w.step > wizStepCheckCluster {
		lines = append(lines, labelStyle.Render("  ✅ Cluster check passed"))
		lines = append(lines, "")
	}
	if w.targetNamespace != "" && w.step > wizStepNamespace {
		lines = append(lines, hintStyle.Render("  Namespace: ")+valueStyle.Render(w.targetNamespace))
	}

	if w.step > wizStepInstanceName {
		stepNum = 2
		lines = append(lines, hintStyle.Render("  Agent: ")+valueStyle.Render(w.instanceName))
	}
	if w.providerName != "" && w.step > wizStepProvider {
		stepNum = 3
		provLine := hintStyle.Render("  Provider: ") + valueStyle.Render(w.providerName)
		if w.modelName != "" {
			provLine += hintStyle.Render("  Model: ") + valueStyle.Render(w.modelName)
		}
		lines = append(lines, provLine)
		if w.baseURL != "" {
			lines = append(lines, hintStyle.Render("  Base URL: ")+valueStyle.Render(w.baseURL))
		}
		if w.apiKey != "" && w.step > wizStepAPIKey {
			lines = append(lines, hintStyle.Render("  API Key:  ")+valueStyle.Render("••••••••"))
		}
	}
	if w.githubRepo != "" && w.step > wizStepGithubRepo {
		lines = append(lines, hintStyle.Render("  GitHub:   ")+valueStyle.Render(w.githubRepo))
	}
	if w.teamTask != "" && w.step > wizStepTeamTask {
		display := w.teamTask
		if len(display) > 40 {
			display = display[:37] + "..."
		}
		lines = append(lines, hintStyle.Render("  Task:     ")+valueStyle.Render(display))
	}
	if w.step > wizStepChannelToken && w.step > wizStepChannel {
		stepNum = 4
		if w.channelType != "" {
			lines = append(lines, hintStyle.Render("  Channel:  ")+valueStyle.Render(w.channelType))
		} else {
			lines = append(lines, hintStyle.Render("  Channel:  ")+hintStyle.Render("(none)"))
		}
	}
	if w.step > wizStepPolicy {
		stepNum = 5
		pv := "yes"
		if !w.applyPolicy {
			pv = "no"
		}
		lines = append(lines, hintStyle.Render("  Policy:   ")+valueStyle.Render(pv))
	}
	if w.step > wizStepAgentSandbox {
		asv := "no"
		if w.agentSandboxEnabled {
			asv = "yes (gVisor/Kata)"
		}
		lines = append(lines, hintStyle.Render("  Agent Sandbox: ")+valueStyle.Render(asv))
	}
	if w.step > wizStepHeartbeat {
		stepNum = 6
		hbLabel := w.heartbeatLabel
		if hbLabel == "" {
			hbLabel = "every hour"
		}
		lines = append(lines, hintStyle.Render("  Heartbeat: ")+valueStyle.Render(hbLabel))
	}

	if w.step >= wizStepNamespace && w.step <= wizStepHeartbeat {
		lines = append(lines, "")
	}

	// Show current step prompt.
	switch w.step {
	case wizStepCheckCluster:
		lines = append(lines, stepStyle.Render("  📋 Step 1/9 — Checking cluster..."))

	case wizStepNamespace:
		lines = append(lines, stepStyle.Render("  📋 Step 2/9 — Target Namespace"))
		lines = append(lines, menuStyle.Render("  Which namespace should resources be created in?"))
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  Enter namespace:"))
		lines = append(lines, hintStyle.Render(fmt.Sprintf("  Press Enter to use current: %s", m.namespace)))

	case wizStepInstanceName:
		lines = append(lines, stepStyle.Render("  📋 Step 3/9 — Create your Agent"))
		lines = append(lines, menuStyle.Render("  An agent represents you (or a tenant) in the system."))
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  Enter agent name:"))

	case wizStepProvider:
		lines = append(lines, stepStyle.Render("  📋 Step 3/9 — AI Provider"))
		lines = append(lines, menuStyle.Render("  Which model provider do you want to use?"))
		lines = append(lines, "")
		lines = append(lines, menuNumStyle.Render("  1)")+menuStyle.Render(" OpenAI"))
		lines = append(lines, menuNumStyle.Render("  2)")+menuStyle.Render(" Anthropic"))
		lines = append(lines, menuNumStyle.Render("  3)")+menuStyle.Render(" Azure OpenAI"))
		lines = append(lines, menuNumStyle.Render("  4)")+menuStyle.Render(" Ollama          (local, no API key needed)"))
		lines = append(lines, menuNumStyle.Render("  5)")+menuStyle.Render(" LM Studio       (local, optional API key)"))
		lines = append(lines, menuNumStyle.Render("  6)")+menuStyle.Render(" llama-server    (local, no API key needed)"))
		lines = append(lines, menuNumStyle.Render("  7)")+menuStyle.Render(" AWS Bedrock     (Claude, Nova, etc.)"))
		lines = append(lines, menuNumStyle.Render("  8)")+menuStyle.Render(" Other / OpenAI-compatible"))

	case wizStepBaseURL:
		lines = append(lines, stepStyle.Render("  📋 Step 3/9 — AI Provider (continued)"))
		lines = append(lines, labelStyle.Render("  Enter base URL:"))

	case wizStepAWSRegion:
		lines = append(lines, stepStyle.Render("  📋 Step 3/9 — AWS Bedrock"))
		lines = append(lines, labelStyle.Render("  Enter your AWS region:"))
		lines = append(lines, hintStyle.Render("  Press Enter for us-east-1"))

	case wizStepAWSAccessKeyID:
		lines = append(lines, stepStyle.Render("  📋 Step 3/9 — AWS Bedrock (continued)"))
		lines = append(lines, labelStyle.Render("  Enter your AWS Access Key ID:"))
		lines = append(lines, hintStyle.Render("  Press Enter to skip (for IRSA/pod identity)"))

	case wizStepAWSSecretAccessKey:
		lines = append(lines, stepStyle.Render("  📋 Step 3/9 — AWS Bedrock (continued)"))
		lines = append(lines, labelStyle.Render("  Enter your AWS Secret Access Key:"))

	case wizStepAWSSessionToken:
		lines = append(lines, stepStyle.Render("  📋 Step 3/9 — AWS Bedrock (continued)"))
		lines = append(lines, labelStyle.Render("  Enter your AWS Session Token (optional):"))
		lines = append(lines, hintStyle.Render("  Press Enter to skip if using permanent credentials"))

	case wizStepAPIKey:
		lines = append(lines, stepStyle.Render("  📋 Step 3/9 — AI Provider (continued)"))
		lines = append(lines, labelStyle.Render(fmt.Sprintf("  Paste your %s:", w.secretEnvKey)))
		envVal := os.Getenv(w.secretEnvKey)
		if envVal != "" {
			lines = append(lines, hintStyle.Render(fmt.Sprintf("  Press Enter to use %s from environment.", w.secretEnvKey)))
		} else {
			lines = append(lines, hintStyle.Render("  Press Enter to skip — you can add it later."))
		}
		lines = append(lines, hintStyle.Render("  (providing a key lets us fetch your available models)"))

	case wizStepModel:
		lines = append(lines, stepStyle.Render("  📋 Step 3/9 — Select Model"))
		if len(w.fetchedModels) > 0 {
			lines = append(lines, menuStyle.Render(fmt.Sprintf("  Found %d models from your %s account:", len(w.fetchedModels), w.providerName)))
			lines = append(lines, "")

			// Render models in columns to fit the panel.
			models := w.fetchedModels
			colWidth := 30 // characters per column
			// Determine how many columns fit (panel is ~56 chars wide, indent is 4).
			usableWidth := 52
			numCols := usableWidth / colWidth
			if numCols < 2 {
				numCols = 2
			}
			if numCols > 3 {
				numCols = 3
			}
			numRows := (len(models) + numCols - 1) / numCols
			for row := 0; row < numRows; row++ {
				line := "  "
				for col := 0; col < numCols; col++ {
					idx := col*numRows + row
					if idx >= len(models) {
						break
					}
					num := fmt.Sprintf("%2d) ", idx+1)
					name := models[idx]
					if len(name) > colWidth-5 {
						name = name[:colWidth-5]
					}
					cell := num + name
					// Pad to column width.
					for len(cell) < colWidth {
						cell += " "
					}
					line += menuNumStyle.Render(num) + menuStyle.Render(name)
					// Add spacing between columns.
					if col < numCols-1 {
						padding := colWidth - len(num) - len(name)
						if padding > 0 {
							line += strings.Repeat(" ", padding)
						}
					}
				}
				lines = append(lines, line)
			}

			lines = append(lines, "")
			lines = append(lines, labelStyle.Render("  Enter number or model name:"))
		} else {
			if w.modelFetchErr != "" {
				lines = append(lines, hintStyle.Render(fmt.Sprintf("  (could not fetch models: %s)", w.modelFetchErr)))
			}
			// Show static suggestions as fallback.
			if suggestions, ok := modelSuggestions[w.providerName]; ok {
				lines = append(lines, "")
				for i, s := range suggestions {
					lines = append(lines, menuNumStyle.Render(fmt.Sprintf("  %d)", i+1))+menuStyle.Render(fmt.Sprintf(" %s  ", s.text))+hintStyle.Render(s.desc))
				}
				lines = append(lines, "")
			}
			lines = append(lines, labelStyle.Render("  Enter model name:"))
		}

	case wizStepGithubRepo:
		lines = append(lines, stepStyle.Render("  📋 Step 4/9 — GitHub Repository (optional)"))
		lines = append(lines, menuStyle.Render("  Point your agent at a GitHub repository to enable"))
		lines = append(lines, menuStyle.Render("  issue triage, PR reviews, and code contributions."))
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  Enter repo (owner/repo):"))
		lines = append(lines, hintStyle.Render("  Press Enter to skip — you can configure this later."))

	case wizStepTeamTask:
		lines = append(lines, stepStyle.Render("  📋 Step 5/9 — Instructions (optional)"))
		lines = append(lines, menuStyle.Render("  Give your agent an objective or task to work on."))
		lines = append(lines, menuStyle.Render("  This will be included in every heartbeat run."))
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  What should the agent work on?"))
		lines = append(lines, hintStyle.Render("  Press Enter to skip."))

	case wizStepChannel:
		lines = append(lines, stepStyle.Render("  📋 Step 6/9 — Connect a Channel (optional)"))
		lines = append(lines, menuStyle.Render("  Channels let your agent receive messages from external platforms."))
		lines = append(lines, "")
		lines = append(lines, menuNumStyle.Render("  1)")+menuStyle.Render(" Telegram  — easiest, just talk to @BotFather"))
		lines = append(lines, menuNumStyle.Render("  2)")+menuStyle.Render(" Slack"))
		lines = append(lines, menuNumStyle.Render("  3)")+menuStyle.Render(" Discord"))
		lines = append(lines, menuNumStyle.Render("  4)")+menuStyle.Render(" WhatsApp  — scan a QR code to link"))
		lines = append(lines, menuNumStyle.Render("  5)")+menuStyle.Render(" Skip — I'll add a channel later"))

	case wizStepChannelToken:
		lines = append(lines, stepStyle.Render("  📋 Step 6/9 — Connect a Channel (continued)"))
		lines = append(lines, labelStyle.Render(fmt.Sprintf("  Paste your %s token:", w.channelType)))

	case wizStepPolicy:
		lines = append(lines, stepStyle.Render("  📋 Step 7/9 — Default Policy"))
		lines = append(lines, menuStyle.Render("  A SympoziumPolicy controls what tools agents can use, sandboxing, etc."))
		lines = append(lines, labelStyle.Render("  Apply the default policy?"))

	case wizStepAgentSandbox:
		lines = append(lines, stepStyle.Render("  📋 Step 7.5/9 — Agent Sandbox (K8s CRD)"))
		lines = append(lines, menuStyle.Render("  Uses kubernetes-sigs/agent-sandbox for kernel-level isolation (gVisor/Kata)."))
		lines = append(lines, menuStyle.Render("  Runs agents in Sandbox CRs instead of Jobs — provides stronger security,"))
		lines = append(lines, menuStyle.Render("  warm pools for fast cold starts, and suspend/resume lifecycle."))
		lines = append(lines, menuStyle.Render("  Requires: agent-sandbox CRDs installed + gVisor/Kata runtime on nodes."))
		lines = append(lines, labelStyle.Render("  Enable Agent Sandbox isolation?"))

	case wizStepRunTimeout:
		lines = append(lines, stepStyle.Render("  📋 Step 7.6/9 — Run Timeout"))
		lines = append(lines, menuStyle.Render("  Maximum time each agent run is allowed before timing out."))
		lines = append(lines, menuStyle.Render("  Local models (Ollama, LM Studio) default to 30m; cloud providers to 10m."))
		lines = append(lines, "")
		lines = append(lines, menuNumStyle.Render("  1)")+menuStyle.Render(" Provider default (10m cloud / 30m local)"))
		lines = append(lines, menuNumStyle.Render("  2)")+menuStyle.Render(" 30 minutes"))
		lines = append(lines, menuNumStyle.Render("  3)")+menuStyle.Render(" 1 hour"))
		lines = append(lines, menuNumStyle.Render("  4)")+menuStyle.Render(" 2 hours"))

	case wizStepHeartbeat:
		lines = append(lines, stepStyle.Render("  📋 Step 8/9 — Heartbeat Schedule"))
		lines = append(lines, menuStyle.Render("  A heartbeat lets your agent wake up periodically to review memory"))
		lines = append(lines, menuStyle.Render("  and note anything that needs attention."))
		lines = append(lines, "")
		lines = append(lines, menuNumStyle.Render("  1)")+menuStyle.Render(" Every 30 minutes"))
		lines = append(lines, menuNumStyle.Render("  2)")+menuStyle.Render(" Every hour")+hintStyle.Render("  (recommended)"))
		lines = append(lines, menuNumStyle.Render("  3)")+menuStyle.Render(" Every 6 hours"))
		lines = append(lines, menuNumStyle.Render("  4)")+menuStyle.Render(" Once a day (9am)"))
		lines = append(lines, menuNumStyle.Render("  5)")+menuStyle.Render(" Disabled — no heartbeat"))

	case wizStepConfirm:
		lines = append(lines, stepStyle.Render("  📋 Step 9/9 — Confirm"))
		lines = append(lines, "")
		lines = append(lines, tuiSepStyle.Render("  "+strings.Repeat("━", 50)))
		lines = append(lines, labelStyle.Render("  Summary"))
		lines = append(lines, tuiSepStyle.Render("  "+strings.Repeat("━", 50)))
		lines = append(lines, hintStyle.Render("  Agent:     ")+valueStyle.Render(w.instanceName)+
			hintStyle.Render("  (namespace: ")+valueStyle.Render(w.targetNamespace)+hintStyle.Render(")"))
		lines = append(lines, hintStyle.Render("  Provider:  ")+valueStyle.Render(w.providerName)+
			hintStyle.Render("  (model: ")+valueStyle.Render(w.modelName)+hintStyle.Render(")"))
		if w.baseURL != "" {
			lines = append(lines, hintStyle.Render("  Base URL:  ")+valueStyle.Render(w.baseURL))
		}
		if w.githubRepo != "" {
			lines = append(lines, hintStyle.Render("  GitHub:    ")+valueStyle.Render(w.githubRepo))
		}
		if w.teamTask != "" {
			display := w.teamTask
			if len(display) > 50 {
				display = display[:47] + "..."
			}
			lines = append(lines, hintStyle.Render("  Task:      ")+valueStyle.Render(display))
		}
		if w.channelType != "" {
			lines = append(lines, hintStyle.Render("  Channel:   ")+valueStyle.Render(w.channelType))
		} else {
			lines = append(lines, hintStyle.Render("  Channel:   ")+hintStyle.Render("(none)"))
		}
		pv := "yes"
		if !w.applyPolicy {
			pv = "no"
		}
		lines = append(lines, hintStyle.Render("  Policy:    ")+valueStyle.Render(pv))
		hbDisplay := w.heartbeatLabel
		if hbDisplay == "" {
			hbDisplay = "every hour"
		}
		lines = append(lines, hintStyle.Render("  Heartbeat: ")+valueStyle.Render(hbDisplay))
		rtDisplay := "provider default"
		if w.runTimeout != "" {
			rtDisplay = w.runTimeout
		}
		lines = append(lines, hintStyle.Render("  Timeout:   ")+valueStyle.Render(rtDisplay))
		lines = append(lines, tuiSepStyle.Render("  "+strings.Repeat("━", 50)))
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  Proceed?"))

	case wizStepApplying:
		lines = append(lines, stepStyle.Render("  ⏳ Applying resources..."))

	case wizStepWhatsAppQR:
		lines = append(lines, stepStyle.Render("  📱 WhatsApp QR Pairing"))
		lines = append(lines, "")
		// Show apply results first
		for _, msg := range w.resultMsgs {
			lines = append(lines, "  "+msg)
		}
		lines = append(lines, "")
		switch w.qrStatus {
		case "waiting":
			lines = append(lines, menuStyle.Render("  ⏳ Waiting for WhatsApp channel pod to start..."))
			lines = append(lines, hintStyle.Render("  (this may take a moment on first deploy)"))
		case "scanning":
			lines = append(lines, menuStyle.Render("  Open WhatsApp on your phone:"))
			lines = append(lines, menuStyle.Render("  Settings → Linked Devices → Link a Device"))
			lines = append(lines, "")
			for _, qrLine := range w.qrLines {
				lines = append(lines, "  "+strings.TrimRight(qrLine, " "))
			}
		case "error":
			lines = append(lines, menuStyle.Render("  ⏳ Waiting for pod... (retrying)"))
			if w.qrErr != "" {
				lines = append(lines, hintStyle.Render("  "+w.qrErr))
			}
		}
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("  Press Esc to skip — you can scan later via kubectl logs"))

	case wizStepDone:
		lines = append(lines, "")
		for _, msg := range w.resultMsgs {
			lines = append(lines, "  "+msg)
		}
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("  Press Enter to return to the dashboard."))
	}

	_ = stepNum
	if w.err != "" {
		lines = append(lines, "")
		lines = append(lines, tuiErrorStyle.Render("  ✗ "+w.err))
	}

	// Apply scroll offset for long content (e.g. model lists).
	if w.scrollOffset > 0 && len(lines) > h {
		maxOffset := len(lines) - h
		if w.scrollOffset > maxOffset {
			w.scrollOffset = maxOffset
		}
		lines = lines[w.scrollOffset:]
	}

	// Pad to fill available height.
	for len(lines) < h {
		lines = append(lines, "")
	}
	// Trim if too long.
	if len(lines) > h {
		lines = lines[:h]
	}

	return strings.Join(lines, "\n") + "\n"
}

// renderPersonaWizardPanel renders the persona onboarding wizard overlay.
func (m tuiModel) renderPersonaWizardPanel(h int,
	titleStyle, labelStyle, valueStyle, menuStyle, menuNumStyle, hintStyle, stepStyle lipgloss.Style,
) string {
	w := &m.wizard
	var lines []string

	lines = append(lines, "")
	lines = append(lines, titleStyle.Render("  ╔═══════════════════════════════════════════╗"))
	lines = append(lines, titleStyle.Render("  ║       Sympozium · Ensemble Wizard       ║"))
	lines = append(lines, titleStyle.Render("  ╚═══════════════════════════════════════════╝"))
	lines = append(lines, "")

	// Recap completed values.
	if w.ensembleName != "" && w.step > wizStepPersonaPick {
		// Show pack info.
		lines = append(lines, hintStyle.Render("  Pack: ")+valueStyle.Render(w.ensembleName))
		for _, pp := range m.ensembles {
			if pp.Name == w.ensembleName {
				lines = append(lines, hintStyle.Render("  Category: ")+valueStyle.Render(pp.Spec.Category)+
					hintStyle.Render("  Agents: ")+valueStyle.Render(fmt.Sprintf("%d", len(pp.Spec.AgentConfigs))))
				for _, p := range pp.Spec.AgentConfigs {
					name := p.Name
					if p.DisplayName != "" {
						name = p.DisplayName
					}
					sched := "(on-demand)"
					if p.Schedule != nil {
						if p.Schedule.Interval != "" {
							sched = "every " + p.Schedule.Interval
						} else if p.Schedule.Cron != "" {
							sched = p.Schedule.Cron
						}
					}
					lines = append(lines, hintStyle.Render("    • ")+valueStyle.Render(name)+hintStyle.Render(" — "+sched))
				}
				break
			}
		}
		lines = append(lines, "")
	}

	if w.providerName != "" && w.step > wizStepPersonaProvider {
		provLine := hintStyle.Render("  Provider: ") + valueStyle.Render(w.providerName)
		if w.modelName != "" {
			provLine += hintStyle.Render("  Model: ") + valueStyle.Render(w.modelName)
		}
		lines = append(lines, provLine)
	}
	if w.apiKey != "" && w.step > wizStepPersonaAPIKey {
		lines = append(lines, hintStyle.Render("  API Key: ")+valueStyle.Render("••••"+w.apiKey[max(0, len(w.apiKey)-4):]))
	}
	if w.githubRepo != "" && w.step > wizStepPersonaGithubRepo {
		lines = append(lines, hintStyle.Render("  GitHub:  ")+valueStyle.Render(w.githubRepo))
	}
	if w.teamTask != "" && w.step > wizStepPersonaTeamTask {
		display := w.teamTask
		if len(display) > 40 {
			display = display[:37] + "..."
		}
		lines = append(lines, hintStyle.Render("  Task:    ")+valueStyle.Render(display))
	}
	if w.step > wizStepPersonaAgentSandbox {
		asv := "no"
		if w.agentSandboxEnabled {
			asv = "yes (gVisor/Kata)"
		}
		lines = append(lines, hintStyle.Render("  Agent Sandbox: ")+valueStyle.Render(asv))
	}

	// Current step.
	switch w.step {
	case wizStepPersonaPick:
		stepNum := 1
		lines = append(lines, stepStyle.Render(fmt.Sprintf("  Step %d: Select a Ensemble", stepNum)))
		lines = append(lines, "")
		if len(m.ensembles) == 0 {
			lines = append(lines, hintStyle.Render("  No Ensembles found in cluster."))
			lines = append(lines, hintStyle.Render("  Run 'sympozium install' to install built-in packs."))
		} else {
			for i, pp := range m.ensembles {
				activated := ""
				if len(pp.Spec.AuthRefs) > 0 && pp.Status.Phase == "Ready" {
					activated = " ✓ activated"
				}
				lines = append(lines, menuNumStyle.Render(fmt.Sprintf("  [%d]", i+1))+
					menuStyle.Render(fmt.Sprintf(" %s", pp.Name))+
					hintStyle.Render(fmt.Sprintf(" — %s (%d personas)%s",
						pp.Spec.Category, len(pp.Spec.AgentConfigs), activated)))
			}
		}
		lines = append(lines, "")

	case wizStepPersonaProvider:
		lines = append(lines, stepStyle.Render("  Step 2: Select AI Provider"))
		lines = append(lines, "")
		lines = append(lines, menuNumStyle.Render("  [1]")+menuStyle.Render(" OpenAI")+hintStyle.Render(" — GPT-4o, o1, etc."))
		lines = append(lines, menuNumStyle.Render("  [2]")+menuStyle.Render(" Anthropic")+hintStyle.Render(" — Claude Sonnet/Opus"))
		lines = append(lines, menuNumStyle.Render("  [3]")+menuStyle.Render(" Azure OpenAI")+hintStyle.Render(" — Enterprise Azure"))
		lines = append(lines, menuNumStyle.Render("  [4]")+menuStyle.Render(" Ollama")+hintStyle.Render(" — Local models"))
		lines = append(lines, menuNumStyle.Render("  [5]")+menuStyle.Render(" LM Studio")+hintStyle.Render(" — Local models"))
		lines = append(lines, menuNumStyle.Render("  [6]")+menuStyle.Render(" llama-server")+hintStyle.Render(" — Local models (llama.cpp)"))
		lines = append(lines, menuNumStyle.Render("  [7]")+menuStyle.Render(" AWS Bedrock")+hintStyle.Render(" — Claude, Nova, etc."))
		lines = append(lines, menuNumStyle.Render("  [8]")+menuStyle.Render(" Custom")+hintStyle.Render(" — Any OpenAI-compatible API"))
		lines = append(lines, "")

	case wizStepPersonaBaseURL:
		lines = append(lines, stepStyle.Render("  Step 3: API Base URL"))
		lines = append(lines, "")

	case wizStepPersonaAPIKey:
		lines = append(lines, stepStyle.Render("  Step 3: API Key"))
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render(fmt.Sprintf("  Paste your %s or press Enter to read from env.", w.secretEnvKey)))
		lines = append(lines, "")

	case wizStepPersonaModel:
		lines = append(lines, stepStyle.Render("  Step 4: Model"))
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("  All personas in the pack will use this model."))
		lines = append(lines, "")
		if len(w.fetchedModels) > 0 {
			lines = append(lines, labelStyle.Render("  Available models:"))
			for i, model := range w.fetchedModels {
				lines = append(lines, menuNumStyle.Render(fmt.Sprintf("  [%d]", i+1))+menuStyle.Render(" "+model))
			}
			lines = append(lines, "")
		} else if suggestions, ok := modelSuggestions[w.providerName]; ok {
			lines = append(lines, labelStyle.Render("  Suggested models:"))
			for i, s := range suggestions {
				lines = append(lines, menuNumStyle.Render(fmt.Sprintf("  [%d]", i+1))+menuStyle.Render(" "+s.text)+hintStyle.Render(" — "+s.desc))
			}
			lines = append(lines, "")
		}

	case wizStepPersonaGithubRepo:
		lines = append(lines, stepStyle.Render("  Step 5: GitHub Repository"))
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("  This pack uses the github-gitops skill."))
		lines = append(lines, hintStyle.Render("  Point all personas at a GitHub repository for"))
		lines = append(lines, hintStyle.Render("  issue triage, PR reviews, and code contributions."))
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  Enter repo (owner/repo):"))

	case wizStepPersonaTeamTask:
		lines = append(lines, stepStyle.Render("  Step 6: Team Instructions"))
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("  Give the team an objective or set of instructions."))
		lines = append(lines, hintStyle.Render("  This is prepended to each persona's scheduled task"))
		lines = append(lines, hintStyle.Render("  so every agent works toward the same goal."))
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  What should the team work on?"))
		lines = append(lines, hintStyle.Render("  Press Enter to use each persona's default task."))

	case wizStepPersonaAgentSandbox:
		lines = append(lines, stepStyle.Render("  Step 6.5: Agent Sandbox (K8s CRD)"))
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("  Uses kubernetes-sigs/agent-sandbox for kernel-level isolation (gVisor/Kata)."))
		lines = append(lines, hintStyle.Render("  Runs agents in Sandbox CRs instead of Jobs — provides stronger security,"))
		lines = append(lines, hintStyle.Render("  warm pools for fast cold starts, and suspend/resume lifecycle."))
		lines = append(lines, hintStyle.Render("  Requires: agent-sandbox CRDs installed + gVisor/Kata runtime on nodes."))
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  Enable Agent Sandbox isolation?"))

	case wizStepPersonaChannels:
		lines = append(lines, stepStyle.Render("  Step 7: Channel Bindings"))
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("  Toggle channels to bind to all personas in this pack."))
		lines = append(lines, hintStyle.Render("  Type a number to toggle, press Enter when done."))
		lines = append(lines, "")
		for i, ch := range w.personaChannels {
			tog := "○"
			if ch.enabled {
				tog = "●"
			}
			lines = append(lines, menuNumStyle.Render(fmt.Sprintf("  [%d]", i+1))+
				menuStyle.Render(fmt.Sprintf(" %s %s", tog, ch.chType)))
		}
		lines = append(lines, "")

	case wizStepPersonaChannelToken:
		if w.personaChannelIdx < len(w.personaChannels) {
			ch := w.personaChannels[w.personaChannelIdx]
			lines = append(lines, stepStyle.Render(fmt.Sprintf("  Step 7b: %s Token", strings.Title(ch.chType))))
			lines = append(lines, "")
			lines = append(lines, hintStyle.Render(fmt.Sprintf("  Paste %s or press Enter to skip.", ch.tokenKey)))
		}
		lines = append(lines, "")

	case wizStepPersonaHeartbeat:
		lines = append(lines, stepStyle.Render("  Step 8: Heartbeat Interval"))
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("  How often should personas wake up?"))
		lines = append(lines, hintStyle.Render("  This overrides each persona's default schedule."))
		lines = append(lines, "")
		lines = append(lines, menuNumStyle.Render("  1)")+menuStyle.Render(" Every 30 minutes"))
		lines = append(lines, menuNumStyle.Render("  2)")+menuStyle.Render(" Every hour")+hintStyle.Render(" (default)"))
		lines = append(lines, menuNumStyle.Render("  3)")+menuStyle.Render(" Every 6 hours"))
		lines = append(lines, menuNumStyle.Render("  4)")+menuStyle.Render(" Once a day (midnight)"))
		lines = append(lines, menuNumStyle.Render("  5)")+menuStyle.Render(" Pack default"))
		lines = append(lines, "")

	case wizStepPersonaConfirm:
		lines = append(lines, stepStyle.Render("  Step 9: Confirm"))
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  Summary:"))
		lines = append(lines, hintStyle.Render("  Pack:      ")+valueStyle.Render(w.ensembleName))
		lines = append(lines, hintStyle.Render("  Provider:  ")+valueStyle.Render(w.providerName))
		lines = append(lines, hintStyle.Render("  Model:     ")+valueStyle.Render(w.modelName))
		if w.githubRepo != "" {
			lines = append(lines, hintStyle.Render("  GitHub:    ")+valueStyle.Render(w.githubRepo))
		}
		if w.teamTask != "" {
			display := w.teamTask
			if len(display) > 50 {
				display = display[:47] + "..."
			}
			lines = append(lines, hintStyle.Render("  Task:      ")+valueStyle.Render(display))
		}
		hbLabel := w.heartbeatLabel
		if hbLabel == "" {
			hbLabel = "every hour"
		}
		lines = append(lines, hintStyle.Render("  Heartbeat: ")+valueStyle.Render(hbLabel))
		var chNames []string
		for _, ch := range w.personaChannels {
			if ch.enabled {
				chNames = append(chNames, ch.chType)
			}
		}
		if len(chNames) > 0 {
			lines = append(lines, hintStyle.Render("  Channels:  ")+valueStyle.Render(strings.Join(chNames, ", ")))
		} else {
			lines = append(lines, hintStyle.Render("  Channels:  ")+valueStyle.Render("none"))
		}
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("  This will create an auth secret and activate the pack."))
		lines = append(lines, hintStyle.Render("  The controller will stamp out one Agent per agent config."))
		lines = append(lines, "")

	case wizStepPersonaApplying:
		lines = append(lines, labelStyle.Render("  Activating ensemble..."))

	case wizStepPersonaDone:
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  ✅ Ensemble activated!"))
		lines = append(lines, "")
		if len(w.resultMsgs) > 0 {
			for _, msg := range w.resultMsgs {
				lines = append(lines, "  "+msg)
			}
		}
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("  Press Enter to switch to Agents view."))
	}

	if w.err != "" {
		lines = append(lines, "")
		lines = append(lines, tuiErrorStyle.Render("  ✗ "+w.err))
	}

	// Scroll + pad to fill available height.
	if len(lines) > h {
		maxOffset := len(lines) - h
		if w.scrollOffset > maxOffset {
			w.scrollOffset = maxOffset
		}
		lines = lines[w.scrollOffset:]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}

	return strings.Join(lines, "\n") + "\n"
}

// renderEnsembleDetailPane renders the right-hand detail pane showing
// the contents of the selected Ensemble during the persona wizard.
func (m tuiModel) renderEnsembleDetailPane(w, h int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#e8562a"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#f0ece4")).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))
	dimStyle := tuiDimStyle
	skillStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#d4cfc6"))

	var pack *sympoziumv1alpha1.Ensemble
	for i := range m.ensembles {
		if m.ensembles[i].Name == m.wizard.ensembleName {
			pack = &m.ensembles[i]
			break
		}
	}
	if pack == nil {
		lines := []string{" Pack not found"}
		for len(lines) < h {
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}

	var lines []string
	lines = append(lines, "")
	lines = append(lines, titleStyle.Render(" Pack Contents"))
	lines = append(lines, dimStyle.Render(" "+strings.Repeat("─", w-2)))
	lines = append(lines, "")

	// Pack metadata
	lines = append(lines, labelStyle.Render(" Name:      ")+valueStyle.Render(pack.Name))
	if pack.Spec.Category != "" {
		lines = append(lines, labelStyle.Render(" Category:  ")+valueStyle.Render(pack.Spec.Category))
	}
	if pack.Spec.Version != "" {
		lines = append(lines, labelStyle.Render(" Version:   ")+valueStyle.Render(pack.Spec.Version))
	}
	lines = append(lines, labelStyle.Render(" Agents:    ")+valueStyle.Render(fmt.Sprintf("%d", len(pack.Spec.AgentConfigs))))
	if pack.Spec.Description != "" {
		lines = append(lines, "")
		// Word-wrap description to fit the pane.
		desc := pack.Spec.Description
		maxDescW := w - 3
		if maxDescW < 10 {
			maxDescW = 10
		}
		for len(desc) > 0 {
			chunk := desc
			if len(chunk) > maxDescW {
				// Try to break at a space.
				cut := maxDescW
				if idx := strings.LastIndex(chunk[:cut], " "); idx > 0 {
					cut = idx
				}
				chunk = desc[:cut]
				desc = strings.TrimLeft(desc[cut:], " ")
			} else {
				desc = ""
			}
			lines = append(lines, dimStyle.Render(" "+chunk))
		}
	}

	// Status line
	if pack.Status.Phase != "" {
		phase := pack.Status.Phase
		phaseStyle := dimStyle
		if phase == "Ready" {
			phaseStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))
		} else if phase == "Error" {
			phaseStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#F38BA8"))
		}
		installed := fmt.Sprintf("%d/%d", pack.Status.InstalledCount, len(pack.Spec.AgentConfigs))
		lines = append(lines, labelStyle.Render(" Status:    ")+phaseStyle.Render(phase)+
			dimStyle.Render("  (")+valueStyle.Render(installed)+dimStyle.Render(" installed)"))
	}

	lines = append(lines, "")
	lines = append(lines, dimStyle.Render(" "+strings.Repeat("─", w-2)))
	lines = append(lines, "")

	// Each persona
	for i, p := range pack.Spec.AgentConfigs {
		name := p.DisplayName
		if name == "" {
			name = p.Name
		}

		// Check if excluded
		excluded := false
		for _, ex := range pack.Spec.ExcludeAgentConfigs {
			if ex == p.Name {
				excluded = true
				break
			}
		}

		marker := valueStyle.Render(" ● ")
		if excluded {
			marker = dimStyle.Render(" ○ ")
		}

		numStr := dimStyle.Render(fmt.Sprintf("%d. ", i+1))
		lines = append(lines, numStr+marker+labelStyle.Render(name))

		// Schedule info
		if p.Schedule != nil {
			sched := ""
			if p.Schedule.Interval != "" {
				sched = "every " + p.Schedule.Interval
			} else if p.Schedule.Cron != "" {
				sched = "cron: " + p.Schedule.Cron
			}
			if sched != "" {
				lines = append(lines, dimStyle.Render("    ⏱  "+sched)+" "+dimStyle.Render("("+p.Schedule.Type+")"))
			}
		}

		// Skills
		if len(p.Skills) > 0 {
			lines = append(lines, dimStyle.Render("    🔧 ")+skillStyle.Render(strings.Join(p.Skills, ", ")))
		}

		// Channels
		if len(p.Channels) > 0 {
			lines = append(lines, dimStyle.Render("    📡 ")+dimStyle.Render(strings.Join(p.Channels, ", ")))
		}

		// Memory
		if p.Memory != nil && p.Memory.Enabled {
			seedCount := len(p.Memory.Seeds)
			lines = append(lines, dimStyle.Render(fmt.Sprintf("    🧠 memory enabled (%d seeds)", seedCount)))
		}

		// Tool policy summary
		if p.ToolPolicy != nil {
			parts := []string{}
			if len(p.ToolPolicy.Allow) > 0 {
				parts = append(parts, fmt.Sprintf("allow:%d", len(p.ToolPolicy.Allow)))
			}
			if len(p.ToolPolicy.Deny) > 0 {
				parts = append(parts, fmt.Sprintf("deny:%d", len(p.ToolPolicy.Deny)))
			}
			if len(parts) > 0 {
				lines = append(lines, dimStyle.Render("    🔒 "+strings.Join(parts, " ")))
			}
		}

		// Add a blank line between personas (but not after the last one).
		if i < len(pack.Spec.AgentConfigs)-1 {
			lines = append(lines, "")
		}
	}

	// Apply scroll offset, then pad/truncate to fit height.
	totalLines := len(lines)
	scroll := m.wizard.packDetailScroll
	if scroll > totalLines-h {
		scroll = totalLines - h
	}
	if scroll < 0 {
		scroll = 0
	}
	if scroll > 0 {
		lines = lines[scroll:]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	if len(lines) > h {
		lines = lines[:h]
	}

	// Show scroll indicator if content overflows.
	if totalLines > h {
		scrollPct := 0
		if totalLines-h > 0 {
			scrollPct = scroll * 100 / (totalLines - h)
		}
		indicator := dimStyle.Render(fmt.Sprintf(" ↕ %d%% (%d/%d)", scrollPct, scroll+1, totalLines))
		if len(lines) > 0 {
			lines[len(lines)-1] = indicator
		}
	}

	return strings.Join(lines, "\n")
}

// tuiPersonaApply activates a Ensemble by creating the auth secret,
// patching the pack with authRefs + channel config, and letting the
// controller reconciler stamp out instances.
func tuiPersonaApply(ns string, w *wizardState) (string, error) {
	ctx := context.Background()
	var msgs []string

	secretName := fmt.Sprintf("%s-%s-key", w.ensembleName, w.providerName)

	// 1. Create AI provider secret.
	if w.providerName == "bedrock" && w.awsRegion != "" {
		existing := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, existing); err == nil {
			_ = k8sClient.Delete(ctx, existing)
		}
		secretData := map[string]string{"AWS_REGION": w.awsRegion}
		if w.awsAccessKeyID != "" {
			secretData["AWS_ACCESS_KEY_ID"] = w.awsAccessKeyID
			secretData["AWS_SECRET_ACCESS_KEY"] = w.awsSecretAccessKey
			if w.awsSessionToken != "" {
				secretData["AWS_SESSION_TOKEN"] = w.awsSessionToken
			}
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns},
			StringData: secretData,
		}
		if err := k8sClient.Create(ctx, secret); err != nil {
			return "", fmt.Errorf("create provider secret: %w", err)
		}
		msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Created secret: %s", secretName)))
	} else if w.apiKey != "" {
		existing := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: ns}, existing); err == nil {
			_ = k8sClient.Delete(ctx, existing)
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns},
			StringData: map[string]string{w.secretEnvKey: w.apiKey},
		}
		if err := k8sClient.Create(ctx, secret); err != nil {
			return "", fmt.Errorf("create provider secret: %w", err)
		}
		msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Created secret: %s", secretName)))
	} else if w.secretEnvKey != "" {
		msgs = append(msgs, tuiDimStyle.Render(fmt.Sprintf("⚠ No API key — create secret later: kubectl create secret generic %s --from-literal=%s=<key>",
			secretName, w.secretEnvKey)))
	}

	// 2. Create channel secrets for enabled channels.
	for i := range w.personaChannels {
		ch := &w.personaChannels[i]
		if !ch.enabled || ch.token == "" {
			continue
		}
		chSecretName := fmt.Sprintf("%s-%s-secret", w.ensembleName, ch.chType)
		existing := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: chSecretName, Namespace: ns}, existing); err == nil {
			_ = k8sClient.Delete(ctx, existing)
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: chSecretName, Namespace: ns},
			StringData: map[string]string{ch.tokenKey: ch.token},
		}
		if err := k8sClient.Create(ctx, secret); err != nil {
			return "", fmt.Errorf("create channel secret: %w", err)
		}
		msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Created secret: %s", chSecretName)))
	}

	// 3. Patch the Ensemble with authRefs and channel config.
	var pack sympoziumv1alpha1.Ensemble
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: w.ensembleName, Namespace: ns}, &pack); err != nil {
		return "", fmt.Errorf("get Ensemble: %w", err)
	}

	// Enable the pack — this is the explicit activation step.
	pack.Spec.Enabled = true

	// On first activation, enable all personas by default.
	// On re-activation (updating auth/model), preserve the user's exclusion choices.
	firstActivation := len(pack.Spec.AuthRefs) == 0
	if firstActivation {
		pack.Spec.ExcludeAgentConfigs = nil
	}

	pack.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{
		{
			Provider: w.providerName,
			Secret:   secretName,
		},
	}

	// Store GitHub repo as skill params so the controller passes it to instances.
	if w.githubRepo != "" {
		if pack.Spec.SkillParams == nil {
			pack.Spec.SkillParams = make(map[string]map[string]string)
		}
		pack.Spec.SkillParams["github-gitops"] = map[string]string{"repo": w.githubRepo}
	}

	// Store team task override.
	if w.teamTask != "" {
		pack.Spec.TaskOverride = w.teamTask
	}

	// Store base URL for local/custom providers (e.g. Ollama, LM Studio, Azure OpenAI).
	pack.Spec.BaseURL = w.baseURL

	// Store agent-sandbox setting.
	if w.agentSandboxEnabled {
		pack.Spec.AgentSandbox = &sympoziumv1alpha1.AgentSandboxDefaults{
			Enabled:      true,
			RuntimeClass: "gvisor",
		}
	} else {
		pack.Spec.AgentSandbox = nil
	}

	// Update each persona with the chosen model and channel bindings.
	var enabledChannels []string
	channelConfigs := make(map[string]string)
	for _, ch := range w.personaChannels {
		if ch.enabled {
			enabledChannels = append(enabledChannels, ch.chType)
			if ch.token != "" {
				channelConfigs[ch.chType] = fmt.Sprintf("%s-%s-secret", w.ensembleName, ch.chType)
			}
		}
	}
	if len(channelConfigs) > 0 {
		pack.Spec.ChannelConfigs = channelConfigs
	}
	for i := range pack.Spec.AgentConfigs {
		pack.Spec.AgentConfigs[i].Model = w.modelName
		if len(enabledChannels) > 0 {
			pack.Spec.AgentConfigs[i].Channels = enabledChannels
		}
	}

	// Apply heartbeat override if the user chose something other than "pack default".
	if w.heartbeatCron != "" {
		for i := range pack.Spec.AgentConfigs {
			if pack.Spec.AgentConfigs[i].Schedule != nil {
				pack.Spec.AgentConfigs[i].Schedule.Cron = w.heartbeatCron
				pack.Spec.AgentConfigs[i].Schedule.Interval = ""
			}
		}
	}

	if err := k8sClient.Update(ctx, &pack); err != nil {
		return "", fmt.Errorf("update Ensemble: %w", err)
	}
	msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Activated Ensemble: %s (%d agents)", w.ensembleName, len(pack.Spec.AgentConfigs))))
	msgs = append(msgs, tuiDimStyle.Render("  Controller will create agents shortly..."))

	return strings.Join(msgs, "\n"), nil
}

// tuiOnboardApply creates all K8s resources for the onboard wizard.
// Uses the K8s client directly — no kubectl exec — so it's TUI-safe.
// onboardHeartbeatTask returns the heartbeat task, incorporating the user's
// team objective if one was provided during onboarding.
func onboardHeartbeatTask(teamTask string) string {
	base := "Review your memory. Summarise what you know so far and note anything that needs attention."
	if teamTask != "" {
		return fmt.Sprintf("OBJECTIVE: %s\n\n%s", teamTask, base)
	}
	return base
}

func tuiOnboardApply(ns string, w *wizardState) (string, error) {
	ctx := context.Background()
	var msgs []string

	providerSecretName := fmt.Sprintf("%s-%s-key", w.instanceName, w.providerName)
	channelSecretName := fmt.Sprintf("%s-%s-secret", w.instanceName, w.channelType)
	policyName := "default-policy"

	// 1. Create AI provider secret.
	if w.providerName == "bedrock" && w.awsRegion != "" {
		// Bedrock uses multiple AWS credential keys in the secret.
		existing := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: providerSecretName, Namespace: ns}, existing); err == nil {
			_ = k8sClient.Delete(ctx, existing)
		}
		secretData := map[string]string{"AWS_REGION": w.awsRegion}
		if w.awsAccessKeyID != "" {
			secretData["AWS_ACCESS_KEY_ID"] = w.awsAccessKeyID
			secretData["AWS_SECRET_ACCESS_KEY"] = w.awsSecretAccessKey
			if w.awsSessionToken != "" {
				secretData["AWS_SESSION_TOKEN"] = w.awsSessionToken
			}
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: providerSecretName, Namespace: ns},
			StringData: secretData,
		}
		if err := k8sClient.Create(ctx, secret); err != nil {
			return "", fmt.Errorf("create provider secret: %w", err)
		}
		msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Created secret: %s", providerSecretName)))
	} else if w.apiKey != "" {
		// Delete existing if present.
		existing := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: providerSecretName, Namespace: ns}, existing); err == nil {
			_ = k8sClient.Delete(ctx, existing)
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: providerSecretName, Namespace: ns},
			StringData: map[string]string{w.secretEnvKey: w.apiKey},
		}
		if err := k8sClient.Create(ctx, secret); err != nil {
			return "", fmt.Errorf("create provider secret: %w", err)
		}
		msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Created secret: %s", providerSecretName)))
	} else if w.secretEnvKey != "" {
		msgs = append(msgs, tuiDimStyle.Render(fmt.Sprintf("⚠ No API key — create secret later: kubectl create secret generic %s --from-literal=%s=<key>",
			providerSecretName, w.secretEnvKey)))
	}

	// 2. Create channel secret.
	if w.channelType != "" && w.channelToken != "" {
		existing := &corev1.Secret{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: channelSecretName, Namespace: ns}, existing); err == nil {
			_ = k8sClient.Delete(ctx, existing)
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: channelSecretName, Namespace: ns},
			StringData: map[string]string{w.channelTokenKey: w.channelToken},
		}
		if err := k8sClient.Create(ctx, secret); err != nil {
			return "", fmt.Errorf("create channel secret: %w", err)
		}
		msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Created secret: %s", channelSecretName)))
	}

	// 3. Apply default policy.
	if w.applyPolicy {
		pol := &sympoziumv1alpha1.SympoziumPolicy{
			ObjectMeta: metav1.ObjectMeta{Name: policyName, Namespace: ns},
			Spec: sympoziumv1alpha1.SympoziumPolicySpec{
				ToolGating: &sympoziumv1alpha1.ToolGatingSpec{
					DefaultAction: "allow",
					Rules: []sympoziumv1alpha1.ToolGatingRule{
						{Tool: "exec_command", Action: "ask"},
						{Tool: "write_file", Action: "allow"},
						{Tool: "network_request", Action: "deny"},
					},
				},
				SubagentPolicy: &sympoziumv1alpha1.SubagentPolicySpec{
					MaxDepth:      3,
					MaxConcurrent: 5,
				},
				SandboxPolicy: &sympoziumv1alpha1.SandboxPolicySpec{
					Required:     false,
					DefaultImage: "ghcr.io/sympozium-ai/sympozium/sandbox:latest",
					MaxCPU:       "4",
					MaxMemory:    "8Gi",
					AgentSandboxPolicy: &sympoziumv1alpha1.AgentSandboxPolicySpec{
						Required:              false,
						DefaultRuntimeClass:   "gvisor",
						AllowedRuntimeClasses: []string{"gvisor", "kata"},
					},
				},
				FeatureGates: map[string]bool{
					"browser-automation": false,
					"code-execution":     true,
					"file-access":        true,
				},
			},
		}
		if err := k8sClient.Create(ctx, pol); err != nil {
			// If already exists, update it.
			var existingPol sympoziumv1alpha1.SympoziumPolicy
			if getErr := k8sClient.Get(ctx, types.NamespacedName{Name: policyName, Namespace: ns}, &existingPol); getErr == nil {
				existingPol.Spec = pol.Spec
				if err2 := k8sClient.Update(ctx, &existingPol); err2 != nil {
					return "", fmt.Errorf("update policy: %w", err2)
				}
				msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Updated policy: %s", policyName)))
			} else {
				return "", fmt.Errorf("apply policy: %w", err)
			}
		} else {
			msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Created policy: %s", policyName)))
		}
	}

	// 4. Create Agent.
	inst := &sympoziumv1alpha1.Agent{
		ObjectMeta: metav1.ObjectMeta{
			Name:      w.instanceName,
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.AgentSpec{
			Agents: sympoziumv1alpha1.AgentsSpec{
				Default: sympoziumv1alpha1.AgentConfig{
					Model:   w.modelName,
					BaseURL: w.baseURL,
				},
			},
		},
	}

	// Only add AuthRefs when an API key was provided.
	if w.apiKey != "" {
		inst.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{
			{
				Secret: providerSecretName,
			},
		}
	}

	if w.channelType != "" {
		chSpec := sympoziumv1alpha1.ChannelSpec{
			Type: w.channelType,
		}
		// WhatsApp uses QR pairing — no secret needed
		if w.channelType != "whatsapp" && channelSecretName != "" {
			chSpec.ConfigRef = sympoziumv1alpha1.SecretRef{
				Secret: channelSecretName,
			}
		}
		inst.Spec.Channels = []sympoziumv1alpha1.ChannelSpec{chSpec}
	}
	if w.applyPolicy {
		inst.Spec.PolicyRef = policyName
	}
	if w.agentSandboxEnabled {
		inst.Spec.Agents.Default.AgentSandbox = &sympoziumv1alpha1.AgentSandboxDefaults{
			Enabled:      true,
			RuntimeClass: "gvisor",
		}
	}
	if w.runTimeout != "" {
		inst.Spec.Agents.Default.RunTimeout = w.runTimeout
	}

	// Default skills: k8s-ops + llmfit + memory.
	inst.Spec.Skills = []sympoziumv1alpha1.SkillRef{
		{SkillPackRef: "k8s-ops"},
		{SkillPackRef: "llmfit"},
		{SkillPackRef: "memory"},
	}

	// Add github-gitops skill if a repo was specified.
	if w.githubRepo != "" {
		inst.Spec.Skills = append(inst.Spec.Skills, sympoziumv1alpha1.SkillRef{
			SkillPackRef: "github-gitops",
			Params:       map[string]string{"repo": w.githubRepo},
		})
	}

	// Memory is on by default.
	inst.Spec.Memory = &sympoziumv1alpha1.MemorySpec{
		Enabled:   true,
		MaxSizeKB: 256,
	}

	// Try create; if it exists, update.
	if err := k8sClient.Create(ctx, inst); err != nil {
		var existing sympoziumv1alpha1.Agent
		if getErr := k8sClient.Get(ctx, types.NamespacedName{Name: w.instanceName, Namespace: ns}, &existing); getErr == nil {
			existing.Spec = inst.Spec
			if err2 := k8sClient.Update(ctx, &existing); err2 != nil {
				return "", fmt.Errorf("update instance: %w", err2)
			}
			msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Updated Agent: %s", w.instanceName)))
		} else {
			return "", fmt.Errorf("create instance: %w", err)
		}
	} else {
		msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Created Agent: %s", w.instanceName)))
	}

	// 5. Create a heartbeat schedule (unless disabled).
	heartbeatCron := w.heartbeatCron
	if heartbeatCron == "" && w.heartbeatLabel != "disabled" {
		heartbeatCron = "0 * * * *" // default to hourly
	}
	if heartbeatCron != "" {
		heartbeatName := fmt.Sprintf("%s-heartbeat", w.instanceName)
		heartbeat := &sympoziumv1alpha1.SympoziumSchedule{
			ObjectMeta: metav1.ObjectMeta{
				Name:      heartbeatName,
				Namespace: ns,
			},
			Spec: sympoziumv1alpha1.SympoziumScheduleSpec{
				AgentRef:          w.instanceName,
				Schedule:          heartbeatCron,
				Task:              onboardHeartbeatTask(w.teamTask),
				Type:              "heartbeat",
				ConcurrencyPolicy: "Forbid",
				IncludeMemory:     true,
			},
		}
		if err := k8sClient.Create(ctx, heartbeat); err != nil {
			var existingSched sympoziumv1alpha1.SympoziumSchedule
			if getErr := k8sClient.Get(ctx, types.NamespacedName{Name: heartbeatName, Namespace: ns}, &existingSched); getErr == nil {
				existingSched.Spec = heartbeat.Spec
				if err2 := k8sClient.Update(ctx, &existingSched); err2 != nil {
					return "", fmt.Errorf("update heartbeat schedule: %w", err2)
				}
				msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Updated heartbeat: %s", heartbeatName)))
			} else {
				return "", fmt.Errorf("create heartbeat schedule: %w", err)
			}
		} else {
			hbLabel := w.heartbeatLabel
			if hbLabel == "" {
				hbLabel = "every hour"
			}
			msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Created heartbeat: %s (%s, reviews memory)", heartbeatName, hbLabel)))
		}
	} else {
		msgs = append(msgs, tuiDimStyle.Render("⏭ Heartbeat disabled — no schedule created"))
	}

	msgs = append(msgs, "")
	msgs = append(msgs, tuiSuccessStyle.Render("✅ Onboarding complete!"))
	msgs = append(msgs, "")
	msgs = append(msgs, tuiDimStyle.Render("Next: press R or type /run "+w.instanceName+" <task> to spawn an agent pod"))

	return strings.Join(msgs, "\n"), nil
}

// ── Helpers ──────────────────────────────────────────────────────────────────

func shortDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func padRight(s string, w int) string {
	sw := lipgloss.Width(s)
	if sw >= w {
		return s
	}
	return s + strings.Repeat(" ", w-sw)
}

func joinPanesHorizontally(left, right string, leftW, rightW int) string {
	leftLines := strings.Split(left, "\n")
	rightLines := strings.Split(right, "\n")

	// Trim a trailing empty line that Split produces when string ends with \n.
	if len(leftLines) > 0 && leftLines[len(leftLines)-1] == "" {
		leftLines = leftLines[:len(leftLines)-1]
	}

	sepStr := lipgloss.NewStyle().Foreground(lipgloss.Color("#333330")).Render("│")

	// Never let the right pane make the output taller than the left.
	maxLines := len(leftLines)

	var b strings.Builder
	for i := 0; i < maxLines; i++ {
		var l, r string
		if i < len(leftLines) {
			l = leftLines[i]
		}
		if i < len(rightLines) {
			r = rightLines[i]
		}
		// Truncate left line if it exceeds leftW.
		lw := lipgloss.Width(l)
		if lw > leftW {
			l = ansiTruncate(l, leftW)
			lw = lipgloss.Width(l)
		}
		if lw < leftW {
			l += strings.Repeat(" ", leftW-lw)
		}
		// Truncate right line if it exceeds rightW to prevent terminal wrapping.
		rw := lipgloss.Width(r)
		if rw > rightW {
			r = ansiTruncate(r, rightW)
		}
		b.WriteString(l + sepStr + r)
		if i < maxLines-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen < 4 {
		return s[:maxLen]
	}
	return s[:maxLen-1] + "…"
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// stripAnsi removes ANSI escape sequences so we can measure visible width.
func stripAnsi(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Skip until we find the terminating letter.
			j := i + 2
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
				j++
			}
			if j < len(s) {
				j++ // skip the terminator
			}
			i = j
		} else {
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

// ansiTruncate truncates a string to maxVisible visible characters while
// preserving all ANSI escape sequences. This ensures styled (colored,
// bold, background) text is not destroyed when truncating to fit a pane.
func ansiTruncate(s string, maxVisible int) string {
	var out strings.Builder
	visible := 0
	i := 0
	for i < len(s) && visible < maxVisible {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			// Copy the entire ANSI sequence through.
			j := i + 2
			for j < len(s) && !((s[j] >= 'A' && s[j] <= 'Z') || (s[j] >= 'a' && s[j] <= 'z')) {
				j++
			}
			if j < len(s) {
				j++ // include the terminator
			}
			out.WriteString(s[i:j])
			i = j
		} else {
			out.WriteByte(s[i])
			visible++
			i++
		}
	}
	// Append a reset sequence so truncated styles don't bleed.
	out.WriteString("\x1b[0m")
	return out.String()
}

// wrapText wraps a plain-text string to fit within maxWidth visible characters,
// breaking at word boundaries where possible. Returns one or more lines.
// An empty input returns a single empty-string line.
func wrapText(s string, maxWidth int) []string {
	if maxWidth < 1 {
		maxWidth = 1
	}
	s = strings.TrimRight(s, " \t\r")
	if s == "" {
		return []string{""}
	}

	var lines []string
	for _, paragraph := range strings.Split(s, "\n") {
		paragraph = strings.TrimRight(paragraph, " \t\r")
		if paragraph == "" {
			lines = append(lines, "")
			continue
		}
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		current := words[0]
		for _, word := range words[1:] {
			if len(current)+1+len(word) <= maxWidth {
				current += " " + word
			} else {
				lines = append(lines, current)
				current = word
			}
		}
		lines = append(lines, current)
	}
	// Hard-wrap any lines that are still too long (e.g. a single long word).
	var result []string
	for _, line := range lines {
		for len(line) > maxWidth {
			result = append(result, line[:maxWidth])
			line = line[maxWidth:]
		}
		result = append(result, line)
	}
	return result
}

// ---------- serve command ----------

func newServeCmd() *cobra.Command {
	var localPort string
	var openBrowser bool
	var svcNamespace string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Open the Sympozium web dashboard via port-forward",
		Long: `Port-forwards the in-cluster API server (which hosts the embedded web UI)
to a local port and optionally opens a browser.

The web UI runs inside the sympozium-apiserver pod that was deployed by
'sympozium install' or the Helm chart. This command simply creates a
'kubectl port-forward' tunnel so you can access it locally.

The API server authenticates requests with a bearer token stored in a
Kubernetes Secret. This command retrieves it automatically and prints
the login URL.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ns := svcNamespace
			if ns == "" {
				ns = "sympozium-system"
			}

			// Retrieve the UI token from the cluster secret.
			fmt.Println("  Retrieving UI token...")
			tokenBytes, err := exec.Command("kubectl", "get", "secret", "sympozium-ui-token",
				"-n", ns, "-o", "jsonpath={.data.token}").CombinedOutput()
			if err != nil {
				fmt.Fprintf(os.Stderr, "  Warning: could not retrieve UI token secret: %v\n", err)
				fmt.Fprintln(os.Stderr, "  You may need to set one manually — see 'sympozium serve --help'.")
			}
			var token string
			if len(tokenBytes) > 0 {
				decoded, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(tokenBytes)))
				if decErr == nil {
					token = string(decoded)
				}
			}

			listenAddr := fmt.Sprintf("localhost:%s", localPort)

			// Carry the token in the URL fragment so the web UI logs in
			// automatically: the fragment never leaves the browser, keeping
			// the token out of server logs and Referer headers.
			webURL := fmt.Sprintf("http://%s", listenAddr)
			if token != "" {
				webURL = fmt.Sprintf("http://%s/#token=%s", listenAddr, url.QueryEscape(token))
			}

			fmt.Printf("\n  Starting port-forward to svc/sympozium-apiserver in namespace %s...\n", ns)
			fmt.Printf("  ➜  Web UI (auto-login):  %s\n", webURL)
			if token != "" {
				fmt.Printf("  ➜  Token:   %s\n", token)
			} else {
				fmt.Println("  ➜  Token:   (not found — check the sympozium-ui-token secret)")
			}
			fmt.Println("  Press Ctrl+C to stop.")

			if openBrowser {
				// Give port-forward a moment to bind, then open browser.
				go func() {
					time.Sleep(2 * time.Second)
					// Try common openers.
					for _, opener := range []string{"xdg-open", "open", "sensible-browser"} {
						if p, err := exec.LookPath(opener); err == nil {
							_ = exec.Command(p, webURL).Start()
							return
						}
					}
				}()
			}

			// Run kubectl port-forward in a reconnect loop.
			// port-forward is inherently fragile (pod restarts, network
			// namespace closures, etc.), so we automatically reconnect
			// with exponential backoff.
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			backoff := 1 * time.Second
			const maxBackoff = 30 * time.Second

			for {
				// Before starting, check if the local port is already in
				// use (e.g. a lingering kubectl process from a previous
				// iteration).  If so, try to reach the service through it.
				// If reachable, the forward is healthy — just wait for it
				// to go away instead of spamming errors.
				ln, listenErr := net.Listen("tcp4", "127.0.0.1:"+localPort)
				if listenErr != nil {
					// Port is occupied.  Probe whether the existing
					// forward is actually working.
					probeConn, probeErr := net.DialTimeout("tcp4", "127.0.0.1:"+localPort, 2*time.Second)
					if probeErr == nil {
						probeConn.Close()
						// Forward is live — nothing to do.  Wait and
						// re-check so we can take over once the old
						// process exits.
						select {
						case <-time.After(5 * time.Second):
							continue
						case <-ctx.Done():
							return nil
						}
					}
					// Port is held but not responding — kill stale
					// kubectl port-forward processes on this port.
					killCmd := exec.Command("bash", "-c",
						fmt.Sprintf("lsof -ti tcp:%s | xargs kill 2>/dev/null", localPort))
					_ = killCmd.Run()
					// Give the OS a moment to release the socket.
					select {
					case <-time.After(2 * time.Second):
					case <-ctx.Done():
						return nil
					}
					continue
				}
				ln.Close()

				pf := exec.CommandContext(ctx, "kubectl", "port-forward",
					"-n", ns,
					"--address", "127.0.0.1",
					"svc/sympozium-apiserver",
					fmt.Sprintf("%s:8080", localPort),
				)
				pf.Stdout = os.Stdout
				pf.Stderr = os.Stderr

				if err := pf.Run(); err != nil {
					// If the context was cancelled (Ctrl+C), exit cleanly.
					if ctx.Err() != nil {
						fmt.Println("\n  Port-forward stopped.")
						return nil
					}
					fmt.Printf("\n  Port-forward lost — reconnecting in %s...\n", backoff)
					select {
					case <-time.After(backoff):
						backoff *= 2
						if backoff > maxBackoff {
							backoff = maxBackoff
						}
					case <-ctx.Done():
						return nil
					}
					continue
				}
				// port-forward exited cleanly (shouldn't normally happen)
				if ctx.Err() != nil {
					return nil
				}
				backoff = 1 * time.Second // reset on clean exit
			}
		},
	}

	cmd.Flags().StringVar(&localPort, "port", "9090", "Local port to forward to")
	cmd.Flags().BoolVar(&openBrowser, "open", false, "Open a browser automatically")
	cmd.Flags().StringVar(&svcNamespace, "service-namespace", "sympozium-system", "Namespace of the sympozium-apiserver service")

	return cmd
}
