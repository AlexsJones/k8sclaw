// Package main provides the sympozium CLI tool for managing Sympozium resources.
package main

import (
	"bufio"
	"context"
	cryptoRand "crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/alexsjones/sympozium/api/v1alpha1"
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
		Long: `Sympozium CLI for managing SympoziumInstances, AgentRuns, SympoziumPolicies,
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
		newInstancesCmd(),
		newRunsCmd(),
		newPoliciesCmd(),
		newSkillsCmd(),
		newFeaturesCmd(),
		newVersionCmd(),
		newTUICmd(),
		newServeCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
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

func newInstancesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "instances",
		Aliases: []string{"instance", "inst"},
		Short:   "Manage SympoziumInstances",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List SympoziumInstances",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var list sympoziumv1alpha1.SympoziumInstanceList
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
			Short: "Get a SympoziumInstance",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var inst sympoziumv1alpha1.SympoziumInstance
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
			Short: "Delete a SympoziumInstance",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				inst := &sympoziumv1alpha1.SympoziumInstance{
					ObjectMeta: metav1.ObjectMeta{Name: args[0], Namespace: namespace},
				}
				if err := k8sClient.Delete(ctx, inst); err != nil {
					return err
				}
				fmt.Printf("sympoziuminstance/%s deleted\n", args[0])
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
				fmt.Fprintln(w, "NAME\tINSTANCE\tPHASE\tPOD\tTOKENS\tAGE")
				for _, run := range list.Items {
					age := time.Since(run.CreationTimestamp.Time).Round(time.Second)
					tokens := "-"
					if run.Status.TokenUsage != nil {
						tokens = fmt.Sprintf("%d/%d", run.Status.TokenUsage.InputTokens, run.Status.TokenUsage.OutputTokens)
					}
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
						run.Name, run.Spec.InstanceRef,
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
				fmt.Fprintln(w, "NAME\tBOUND INSTANCES\tAGE")
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
	ghRepo        = "AlexsJones/sympozium"
	manifestAsset = "sympozium-manifests.tar.gz"
)

// ── Onboard ──────────────────────────────────────────────────────────────────

func newOnboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "onboard",
		Short: "Interactive setup wizard for Sympozium",
		Long: `Walks you through creating your first SympoziumInstance, connecting a
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
	fmt.Println("\n📋 Step 1/6 — Checking cluster...")
	if err := initClient(); err != nil {
		fmt.Println("\n  ❌ Cannot connect to your cluster.")
		fmt.Println("  Make sure kubectl is configured and run: sympozium install")
		return err
	}

	// Quick health check: can we list CRDs?
	ctx := context.Background()
	var instances sympoziumv1alpha1.SympoziumInstanceList
	if err := k8sClient.List(ctx, &instances, client.InNamespace(namespace)); err != nil {
		fmt.Println("\n  ❌ Sympozium CRDs not found. Run 'sympozium install' first.")
		return fmt.Errorf("CRDs not installed: %w", err)
	}
	fmt.Println("  ✅ Sympozium is installed and CRDs are available.")

	// ── Step 2: Instance name ────────────────────────────────────────────
	fmt.Println("\n📋 Step 2/6 — Create your SympoziumInstance")
	fmt.Println("  An instance represents you (or a tenant) in the system.")
	instanceName := prompt(reader, "  Instance name", "my-agent")

	// ── Step 3: AI provider ──────────────────────────────────────────────
	fmt.Println("\n📋 Step 3/6 — AI Provider")
	fmt.Println("  Which model provider do you want to use?")
	fmt.Println("    1) OpenAI")
	fmt.Println("    2) Anthropic")
	fmt.Println("    3) Azure OpenAI")
	fmt.Println("    4) Ollama          (local, no API key needed)")
	fmt.Println("    5) Other / OpenAI-compatible")
	providerChoice := prompt(reader, "  Choice [1-5]", "1")

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

	// ── Step 4: Channel ──────────────────────────────────────────────────
	fmt.Println("\n📋 Step 4/6 — Connect a Channel (optional)")
	fmt.Println("  Channels let your agent receive messages from external platforms.")
	fmt.Println("    1) Telegram  — easiest, just talk to @BotFather")
	fmt.Println("    2) Slack")
	fmt.Println("    3) Discord")
	fmt.Println("    4) WhatsApp")
	fmt.Println("    5) Skip — I'll add a channel later")
	channelChoice := prompt(reader, "  Choice [1-5]", "5")

	var channelType, channelTokenKey, channelToken string
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
		channelToken = promptSecret(reader, "  Bot OAuth Token")
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
	fmt.Println("\n📋 Step 5/6 — Default Policy")
	fmt.Println("  A SympoziumPolicy controls what tools agents can use, sandboxing, etc.")
	applyPolicy := promptYN(reader, "  Apply the default policy?", true)

	// ── Step 6: Heartbeat interval ──────────────────────────────────────
	fmt.Println("\n📋 Step 6/6 — Heartbeat Schedule")
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
	fmt.Printf("  Instance:   %s  (namespace: %s)\n", instanceName, namespace)
	fmt.Printf("  Provider:   %s  (model: %s)\n", providerName, modelName)
	if baseURL != "" {
		fmt.Printf("  Base URL:   %s\n", baseURL)
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
		if err := kubectl("create", "secret", "generic", channelSecretName,
			"-n", namespace,
			fmt.Sprintf("--from-literal=%s=%s", channelTokenKey, channelToken)); err != nil {
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

	// 4. Create SympoziumInstance.
	fmt.Printf("  Creating SympoziumInstance %s...\n", instanceName)
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
	instanceYAML := buildSympoziumInstanceYAML(instanceName, namespace, modelName, baseURL,
		providerName, instanceSecret, channelType, chSecret,
		policyName, applyPolicy)
	if err := kubectlApplyStdin(instanceYAML); err != nil {
		return fmt.Errorf("apply instance: %w", err)
	}

	// 5. Create heartbeat schedule (unless disabled).
	if heartbeatCron != "" {
		heartbeatName := fmt.Sprintf("%s-heartbeat", instanceName)
		fmt.Printf("  Creating heartbeat schedule %s (%s)...\n", heartbeatName, heartbeatLabel)
		heartbeatYAML := fmt.Sprintf(`apiVersion: sympozium.ai/v1alpha1
kind: SympoziumSchedule
metadata:
  name: %s
  namespace: %s
spec:
  instanceRef: %s
  schedule: "%s"
  task: "Review your memory. Summarise what you know so far and note anything that needs attention."
  type: heartbeat
  concurrencyPolicy: Forbid
  includeMemory: true
`, heartbeatName, namespace, instanceName, heartbeatCron)
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
	fmt.Printf("  • Check your instance:   sympozium instances get %s\n", instanceName)
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
    defaultImage: ghcr.io/alexsjones/sympozium/sandbox:latest
    maxCPU: "4"
    maxMemory: 8Gi
  featureGates:
    browser-automation: false
    code-execution: true
    file-access: true
`, name, ns)
}

func buildSympoziumInstanceYAML(name, ns, model, baseURL, provider, providerSecret,
	channelType, channelSecret, policyName string, hasPolicy bool) string {

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

	return fmt.Sprintf(`apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: %s
  namespace: %s
spec:
%s  agents:
    default:
      model: %s
%s%s%s  skills:
    - skillPackRef: k8s-ops
  memory:
    enabled: true
    maxSizeKB: 256
`, name, ns, channelsBlock, model, baseURLLine, authRefsBlock, policyBlock)
}

func kubectlApplyStdin(yaml string) error {
	cmd := exec.Command("kubectl", "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(yaml)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func newInstallCmd() *cobra.Command {
	var manifestVersion string
	var imageTag string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install Sympozium into the current Kubernetes cluster",
		Long: `Downloads the Sympozium release manifests from GitHub and applies
them to your current Kubernetes cluster using kubectl.

Installs CRDs, the controller manager, API server, admission webhook,
RBAC rules, and network policies.

Use --image-tag to override the container image tag in the manifests,
for example when you have sideloaded images into Kind with a custom tag.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(manifestVersion, imageTag)
		},
	}
	cmd.Flags().StringVar(&manifestVersion, "version", "", "Release version to install (default: latest)")
	cmd.Flags().StringVar(&imageTag, "image-tag", "", "Override image tag in manifests (e.g. 'latest')")
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

func runInstall(ver, imageTag string) error {
	if ver == "" || ver == "latest" {
		if version != "dev" && ver == "" {
			ver = version
		} else {
			v, err := resolveLatestTag()
			if err != nil {
				return err
			}
			ver = v
		}
	}

	fmt.Printf("  Installing Sympozium %s...\n", ver)

	// Download manifest bundle.
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", ghRepo, ver, manifestAsset)
	tmpDir, err := os.MkdirTemp("", "sympozium-install-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	bundlePath := filepath.Join(tmpDir, manifestAsset)
	fmt.Println("  Downloading manifests...")
	if err := downloadFile(url, bundlePath); err != nil {
		return fmt.Errorf("download manifests: %w", err)
	}

	// Extract.
	fmt.Println("  Extracting...")
	tar := exec.Command("tar", "-xzf", bundlePath, "-C", tmpDir)
	tar.Stderr = os.Stderr
	if err := tar.Run(); err != nil {
		return fmt.Errorf("extract manifests: %w", err)
	}

	// Rewrite image tags if --image-tag was provided.
	if imageTag != "" {
		fmt.Printf("  Rewriting image tags to :%s...\n", imageTag)
		sed := exec.Command("find", filepath.Join(tmpDir, "config"), "-name", "*.yaml", "-exec",
			"sed", "-i",
			fmt.Sprintf(`s|ghcr.io/alexsjones/sympozium/\([^:]*\):[^ ]*|ghcr.io/alexsjones/sympozium/\1:%s|g`, imageTag),
			"{}", "+")
		sed.Stderr = os.Stderr
		if err := sed.Run(); err != nil {
			return fmt.Errorf("rewrite image tags: %w", err)
		}
	}

	// Apply CRDs first (server-side apply to handle schema updates cleanly).
	fmt.Println("  Applying CRDs...")
	if err := kubectl("apply", "--server-side", "--force-conflicts", "-f", filepath.Join(tmpDir, "config/crd/bases/")); err != nil {
		return err
	}

	// Create namespace before RBAC (ServiceAccounts reference it).
	// Ignore AlreadyExists error on re-installs.
	fmt.Println("  Creating namespace...")
	_ = kubectl("create", "namespace", "sympozium-system")

	// Deploy NATS event bus.
	fmt.Println("  Deploying NATS event bus...")
	if err := kubectl("apply", "-f", resolveConfigPath(tmpDir, "config/nats/")); err != nil {
		return err
	}

	// Install cert-manager if not present, then apply webhook certificate.
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
		// The webhook needs a few extra seconds after the Deployment is Available
		// to finish TLS bootstrapping. Retry the certificate creation.
		fmt.Println("  Waiting for cert-manager webhook TLS to bootstrap...")
		time.Sleep(10 * time.Second)
	}

	fmt.Println("  Creating webhook certificate...")
	// Retry with backoff — cert-manager's webhook may still be bootstrapping TLS.
	var certErr error
	for attempt := 0; attempt < 5; attempt++ {
		if certErr = kubectl("apply", "-f", resolveConfigPath(tmpDir, "config/cert/")); certErr == nil {
			break
		}
		wait := time.Duration(5*(attempt+1)) * time.Second
		fmt.Printf("  Cert-manager webhook not ready, retrying in %s...\n", wait)
		time.Sleep(wait)
	}
	if certErr != nil {
		return fmt.Errorf("creating webhook certificate (cert-manager webhook may not be ready): %w", certErr)
	}

	// Apply RBAC.
	fmt.Println("  Applying RBAC...")
	if err := kubectl("apply", "-f", filepath.Join(tmpDir, "config/rbac/")); err != nil {
		return err
	}

	// Apply manager (controller + apiserver).
	fmt.Println("  Deploying control plane...")
	if err := kubectl("apply", "-f", filepath.Join(tmpDir, "config/manager/")); err != nil {
		return err
	}

	// Apply webhook (use --server-side --force-conflicts to overwrite stale configs).
	fmt.Println("  Deploying webhook...")
	if err := kubectl("apply", "--server-side", "--force-conflicts", "-f", filepath.Join(tmpDir, "config/webhook/")); err != nil {
		return err
	}

	// Apply network policies.
	fmt.Println("  Applying network policies...")
	if err := kubectl("apply", "-f", filepath.Join(tmpDir, "config/network/")); err != nil {
		return err
	}

	// Install default SkillPacks into sympozium-system.
	skillsDir := filepath.Join(tmpDir, "config/skills/")
	if _, err := os.Stat(skillsDir); err == nil {
		fmt.Println("  Installing default SkillPacks...")
		if err := kubectl("apply", "--server-side", "--force-conflicts", "-f", skillsDir); err != nil {
			// Non-fatal — skills are optional.
			fmt.Printf("  Warning: failed to install default skills: %v\n", err)
		}
	}

	// Install default SympoziumPolicies (permissive, restrictive, network-isolated).
	policiesDir := filepath.Join(tmpDir, "config/policies/")
	if _, err := os.Stat(policiesDir); err == nil {
		fmt.Println("  Installing default SympoziumPolicies...")
		if err := kubectl("apply", "--server-side", "--force-conflicts", "-f", policiesDir); err != nil {
			// Non-fatal — policies are optional.
			fmt.Printf("  Warning: failed to install default policies: %v\n", err)
		}
	}

	// Install default PersonaPacks (e.g. platform-team, devops-essentials).
	personasDir := filepath.Join(tmpDir, "config/personas/")
	if _, err := os.Stat(personasDir); err == nil {
		fmt.Println("  Installing default PersonaPacks...")
		if err := kubectl("apply", "--server-side", "--force-conflicts", "-f", personasDir); err != nil {
			// Non-fatal — persona packs are optional.
			fmt.Printf("  Warning: failed to install default persona packs: %v\n", err)
		}
	}

	// Generate a random UI token for the web dashboard (if not already present).
	fmt.Println("  Creating web UI token secret...")
	if err := kubectlQuiet("get", "secret", "sympozium-ui-token", "-n", "sympozium-system"); err != nil {
		// Secret doesn't exist — create one with a random token.
		createSecret := exec.Command("kubectl", "create", "secret", "generic", "sympozium-ui-token",
			"-n", "sympozium-system",
			"--from-literal=token="+generateToken(32))
		createSecret.Stderr = os.Stderr
		if secErr := createSecret.Run(); secErr != nil {
			fmt.Printf("  Warning: failed to create UI token secret: %v\n", secErr)
		}
	} else {
		fmt.Println("  UI token secret already exists, skipping.")
	}

	fmt.Println("\n  Sympozium installed successfully!")
	fmt.Println("  Run: sympozium")
	fmt.Println("\n  To access the web dashboard:")
	fmt.Println("    sympozium serve")
	return nil
}

func runUninstall() error {
	fmt.Println("  Removing Sympozium...")

	// Delete default SkillPacks from sympozium-system first (before CRDs go away).
	fmt.Println("  Removing default SkillPacks...")
	_ = kubectl("delete", "skillpacks.sympozium.ai", "--ignore-not-found",
		"-n", "sympozium-system", "-l", "sympozium.ai/builtin=true")

	// Delete in reverse order.
	manifests := []string{
		"https://raw.githubusercontent.com/" + ghRepo + "/main/config/network/policies.yaml",
		"https://raw.githubusercontent.com/" + ghRepo + "/main/config/webhook/manifests.yaml",
		"https://raw.githubusercontent.com/" + ghRepo + "/main/config/manager/manager.yaml",
		"https://raw.githubusercontent.com/" + ghRepo + "/main/config/rbac/role.yaml",
	}
	for _, m := range manifests {
		_ = kubectl("delete", "--ignore-not-found", "-f", m)
	}

	// Strip finalizers from all Sympozium CRD instances so CRD deletion doesn't
	// hang waiting for the (now-deleted) controller to reconcile them.
	fmt.Println("  Removing finalizers from Sympozium resources...")
	for _, res := range []string{"agentruns", "sympoziuminstances", "sympoziumpolicies", "skillpacks", "sympoziumschedules", "personapacks"} {
		stripFinalizers(res)
	}

	// CRDs last.
	crdBase := "https://raw.githubusercontent.com/" + ghRepo + "/main/config/crd/bases/"
	crds := []string{
		"sympozium.ai_sympoziuminstances.yaml",
		"sympozium.ai_agentruns.yaml",
		"sympozium.ai_sympoziumpolicies.yaml",
		"sympozium.ai_skillpacks.yaml",
		"sympozium.ai_sympoziumschedules.yaml",
		"sympozium.ai_personapacks.yaml",
	}
	for _, c := range crds {
		_ = kubectl("delete", "--ignore-not-found", "-f", crdBase+c)
	}

	fmt.Println("  Sympozium uninstalled.")
	return nil
}

// stripFinalizers patches all instances of a Sympozium CRD to remove finalizers.
func stripFinalizers(resource string) {
	// List all resource names across all namespaces.
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

func resolveLatestTag() (string, error) {
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(fmt.Sprintf("https://github.com/%s/releases/latest", ghRepo))
	if err != nil {
		return "", fmt.Errorf("resolve latest release: %w", err)
	}
	defer resp.Body.Close()

	loc := resp.Header.Get("Location")
	if loc == "" {
		return "", fmt.Errorf("no releases found at github.com/%s", ghRepo)
	}
	parts := strings.Split(loc, "/tag/")
	if len(parts) < 2 {
		return "", fmt.Errorf("unexpected redirect URL: %s", loc)
	}
	return parts[1], nil
}

func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, url)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
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

// resolveConfigPath checks for a config path in the extracted bundle first,
// then falls back to the local working tree (for dev builds run from source).
func resolveConfigPath(bundleDir, relPath string) string {
	bundled := filepath.Join(bundleDir, relPath)
	if _, err := os.Stat(bundled); err == nil {
		return bundled
	}
	// Dev fallback: check if we're running from the source tree.
	if _, err := os.Stat(relPath); err == nil {
		return relPath
	}
	// Return the bundled path anyway; kubectl will report the error.
	return bundled
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
	viewPersonas tuiViewKind = iota
	viewInstances
	viewRuns
	viewPolicies
	viewSkills
	viewChannels
	viewSchedules
	viewPods
)

var viewNames = []string{"Personas", "Instances", "Runs", "Policies", "Skills", "Channels", "Schedules", "Pods"}

// detailPaneState controls the visibility of the right-hand detail pane.
type detailPaneState int

const (
	paneCollapsed  detailPaneState = iota // hidden (default)
	panePanel                             // side panel (~35%)
	paneFullscreen                        // takes over the whole screen
)

// ── Styles ───────────────────────────────────────────────────────────────────

var (
	tuiBannerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#E94560")).
			Background(lipgloss.Color("#0F0F23"))

	tuiTabStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#585B70")).
			Background(lipgloss.Color("#0F0F23")).
			Padding(0, 1)

	tuiTabActiveStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#E94560")).
				Background(lipgloss.Color("#1E1E2E")).
				Padding(0, 1)

	tuiColHeaderStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#74C7EC")).
				Background(lipgloss.Color("#11111B"))

	tuiRowStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#CDD6F4"))

	tuiRowAltStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A6ADC8")).
			Background(lipgloss.Color("#11111B"))

	tuiRowSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#1E1E2E")).
				Background(lipgloss.Color("#74C7EC"))

	tuiDimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#585B70"))

	tuiErrorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F38BA8")).
			Bold(true)

	tuiSuccessStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A6E3A1")).
			Bold(true)

	tuiRunningStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#A6E22E"))

	tuiPromptStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E94560")).
			Bold(true)

	tuiHeaderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#74C7EC")).
			Bold(true)

	tuiStatusBarStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#BAC2DE")).
				Background(lipgloss.Color("#181825"))

	tuiStatusKeyStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#E94560")).
				Background(lipgloss.Color("#181825")).
				Bold(true)

	tuiLogBorderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#45475A"))

	tuiSepStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#313244"))

	tuiCountStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F5C2E7")).
			Bold(true)

	tuiModalBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#E94560")).
				Padding(1, 2).
				Background(lipgloss.Color("#16213E"))

	tuiModalTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("#E94560")).
				Background(lipgloss.Color("#16213E"))

	tuiModalCmdStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#E94560"))

	tuiModalDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#A0A0A0"))

	tuiSuggestStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#CDD6F4")).
			Background(lipgloss.Color("#1E1E2E")).
			Padding(0, 1)

	tuiSuggestSelectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#1A1A2E")).
				Background(lipgloss.Color("#E94560")).
				Bold(true).
				Padding(0, 1)

	tuiSuggestDescStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#666666")).
				Background(lipgloss.Color("#1E1E2E"))

	tuiSuggestDescSelectedStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("#1A1A2E")).
					Background(lipgloss.Color("#E94560"))

	// Feed pane styles
	tuiFeedTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#F5C2E7")).
				Bold(true)

	tuiFeedPromptStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#89DCEB"))

	tuiFeedMetaStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#585B70"))
)

func newTUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Interactive terminal UI for managing Sympozium",
		Long:  `Launch an interactive terminal interface with slash commands for managing SympoziumInstances, AgentRuns, policies, and more.`,
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
type suggestionsMsg struct {
	items []suggestion
}
type dataRefreshMsg struct {
	instances    *[]sympoziumv1alpha1.SympoziumInstance
	runs         *[]sympoziumv1alpha1.AgentRun
	policies     *[]sympoziumv1alpha1.SympoziumPolicy
	skills       *[]sympoziumv1alpha1.SkillPack
	channels     *[]channelRow
	pods         *[]podRow
	schedules    *[]sympoziumv1alpha1.SympoziumSchedule
	personaPacks *[]sympoziumv1alpha1.PersonaPack
	fetchErr     string
}

// ── Suggestion ───────────────────────────────────────────────────────────────

type suggestion struct {
	text string
	desc string
}

var slashCommandSuggestions = []suggestion{
	{"/instances", "List SympoziumInstances"},
	{"/runs", "List AgentRuns"},
	{"/run", "Create AgentRun: /run <inst> <task>"},
	{"/abort", "Abort run: /abort <run>"},
	{"/result", "Show run result: /result <run>"},
	{"/status", "Cluster or run status"},
	{"/channels", "View channels for instance"},
	{"/channel", "Add channel: /channel <inst> <type> <secret>"},
	{"/pods", "View agent pods: /pods <inst>"},
	{"/provider", "Set provider: /provider <inst> <provider> <model>"},
	{"/policies", "List SympoziumPolicies"},
	{"/skills", "List SkillPacks"},
	{"/features", "Feature gates: /features <policy>"},
	{"/delete", "Delete: /delete <type> <name>"},
	{"/schedule", "Create schedule: /schedule <inst> <cron> <task>"},
	{"/schedules", "View schedules"},
	{"/personas", "View PersonaPacks"},
	{"/persona", "Manage persona pack: /persona delete <name>"},
	{"/memory", "View memory: /memory <inst>"},
	{"/ns", "Switch namespace: /ns <name>"},
	{"/onboard", "Interactive setup wizard"},
	{"/help", "Show help modal"},
	{"/quit", "Exit the TUI"},
}

var deleteTypeSuggestions = []suggestion{
	{"instance", "Delete a SympoziumInstance"},
	{"run", "Delete an AgentRun"},
	{"policy", "Delete a SympoziumPolicy"},
	{"schedule", "Delete a SympoziumSchedule"},
	{"persona", "Delete a PersonaPack"},
	{"channel", "Remove a channel from instance"},
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

var tuiCommands = []struct{ cmd, desc string }{
	{"/instances", "List SympoziumInstances"},
	{"/runs", "List AgentRuns"},
	{"/run <inst> <task>", "Create a new AgentRun"},
	{"/abort <run>", "Abort a running AgentRun"},
	{"/result <run>", "Show the LLM response"},
	{"/status [run]", "Cluster / run status"},
	{"/channels [inst]", "View channels (tab 5)"},
	{"/channel <i> <type> <sec>", "Add channel to instance"},
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
	{"Esc", "Go back / return to Instances"},
	{"R", "Run task on selected instance"},
	{"O", "Launch onboard wizard"},
	{"x", "Delete selected resource"},
	{"e", "Edit memory / heartbeat config"},
	{"Enter", "Detail / drill in / onboard persona"},
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
	wizStepNone               wizardStep = iota
	wizStepCheckCluster                  // auto — verify CRDs
	wizStepInstanceName                  // text: instance name
	wizStepProvider                      // menu 1-6: provider
	wizStepModel                         // text: model name
	wizStepBaseURL                       // text: base URL (some providers)
	wizStepAPIKey                        // text: API key (non-ollama)
	wizStepChannel                       // menu 1-5: channel type
	wizStepPolicy                        // y/n: apply default policy
	wizStepHeartbeat                     // menu 1-5: heartbeat interval
	wizStepConfirm                       // y/n: confirm summary
	wizStepChannelActionToken            // text: channel token after confirm
	wizStepApplying                      // auto — create resources
	wizStepWhatsAppQR                    // auto — stream QR from pod logs
	wizStepDone                          // auto — show result

	// Persona wizard steps
	wizStepPersonaPick         // menu: select a persona pack
	wizStepPersonaProvider     // menu 1-6: provider
	wizStepPersonaBaseURL      // text: base URL
	wizStepPersonaAPIKey       // text: API key
	wizStepPersonaModel        // text: model name
	wizStepPersonaChannels     // multi-toggle: channels to bind
	wizStepPersonaConfirm      // y/n: confirm summary
	wizStepPersonaChannelToken // text: channel token (per selected channel, after confirm)
	wizStepPersonaApplying     // auto — patch pack + create resources
	wizStepPersonaDone         // auto — show result
)

type wizardState struct {
	active     bool
	step       wizardStep
	err        string // error from last step
	resultMsgs []string

	// Collected values
	instanceName    string
	providerChoice  string // "1"–"6"
	providerName    string
	modelName       string
	baseURL         string
	secretEnvKey    string
	apiKey          string
	channelChoice   string // "1"–"5"
	channelType     string
	channelTokenKey string
	channelToken    string
	applyPolicy     bool
	heartbeatCron   string // cron expression for heartbeat schedule
	heartbeatLabel  string // human-readable label (e.g. "every hour")

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
	personaPackName   string                 // which pack we're installing
	personaChannels   []personaChannelChoice // channels the user is toggling
	personaChannelIdx int                    // which channel we're collecting a token for
	packDetailScroll  int                    // scroll offset for the pack detail pane
}

func (w *wizardState) reset() {
	*w = wizardState{}
}

func (w *wizardState) personaHasEnabledChannel(chType string) bool {
	for _, ch := range w.personaChannels {
		if ch.enabled && ch.chType == chType {
			return true
		}
	}
	return false
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

	// Wizard
	wizard wizardState

	// Cached K8s data
	instances    []sympoziumv1alpha1.SympoziumInstance
	runs         []sympoziumv1alpha1.AgentRun
	policies     []sympoziumv1alpha1.SympoziumPolicy
	skills       []sympoziumv1alpha1.SkillPack
	channels     []channelRow
	pods         []podRow
	schedules    []sympoziumv1alpha1.SympoziumSchedule
	personaPacks []sympoziumv1alpha1.PersonaPack

	// Input
	input        textinput.Model
	inputFocused bool

	// Log
	logLines []string

	// Cluster
	namespace string
	connected bool

	// Autocomplete
	suggestions []suggestion
	suggestIdx  int
	lastInput   string

	// Delete confirmation
	confirmDelete      bool
	deleteResourceKind string // e.g. "instance", "run", "pod"
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
	editTaskInput         bool              // sub-modal for task text entry
	editTaskTI            textinput.Model   // text input for task sub-modal
	editChannelTokenInput bool              // sub-modal for channel token entry
	editChannelTokenTI    textinput.Model   // text input for channel token sub-modal
	editChannelTokenIdx   int               // index into editChannels being configured
	editChannelNewTokens  map[int]string    // idx → token for channels needing secret creation
	editSkills            []editSkillItem   // toggleable skills list
	editChannels          []editChannelItem // channel bindings
	editPersonaPackName   string            // non-empty when editing a PersonaPack
	editPersonas          []editPersonaItem // toggleable personas list

	// Detail pane
	detailPane       detailPaneState // collapsed, panel, or fullscreen
	feedInputFocused bool            // typing in the feed chat
	feedInput        textinput.Model
	feedScrollOffset int // 0 = pinned to bottom; >0 = scrolled up N lines
}

// editMemoryForm holds the editable memory fields for a SympoziumInstance.
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
	name     string // SkillPack name
	enabled  bool   // whether it's in the instance's Skills list
	category string // e.g. "kubernetes"
}

// editChannelItem represents a channel binding in the edit modal.
type editChannelItem struct {
	chType    string // telegram, slack, discord, whatsapp
	enabled   bool   // whether channel is bound to the instance
	secretRef string // secret name for credentials
	tokenKey  string // env var name for the token (e.g. TELEGRAM_BOT_TOKEN)
}

// editPersonaItem represents a toggleable persona in the PersonaPack edit modal.
type editPersonaItem struct {
	name        string // persona name within the pack
	displayName string // human-readable name
	enabled     bool   // true = active, false = excluded
}

var editScheduleTypes = []string{"heartbeat", "scheduled", "sweep"}
var editConcurrencyPolicies = []string{"Forbid", "Allow", "Replace"}
var editMemoryFieldCount = 3    // enabled, maxSizeKB, systemPrompt
var editHeartbeatFieldCount = 6 // schedule, task, type, concurrencyPolicy, includeMemory, suspend
var editTabNames = []string{"Memory", "Heartbeat", "Skills", "Channels"}
var availableChannelTypes = []string{"telegram", "slack", "discord", "whatsapp"}

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

	return tuiModel{
		namespace:    ns,
		connected:    k8sClient != nil,
		input:        ti,
		feedInput:    fi,
		inputFocused: false,
		activeView:   viewPersonas,
		logLines:     []string{tuiDimStyle.Render("Sympozium TUI ready — press ? for help, / to enter commands")},
	}
}

// selectedInstanceForFeed returns the instance name that the feed pane should
// display runs for, based on the current view and selected row.
func (m tuiModel) selectedInstanceForFeed() string {
	switch m.activeView {
	case viewInstances:
		if m.selectedRow < len(m.instances) {
			return m.instances[m.selectedRow].Name
		}
	case viewRuns:
		if m.selectedRow < len(m.runs) {
			return m.runs[m.selectedRow].Spec.InstanceRef
		}
	case viewChannels:
		filtered := m.filteredChannels()
		if m.selectedRow < len(filtered) {
			return filtered[m.selectedRow].InstanceName
		}
	case viewPods:
		filtered := m.filteredPods()
		if m.selectedRow < len(filtered) {
			return filtered[m.selectedRow].Instance
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
		if m.runs[i].Spec.InstanceRef == instName {
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
		sb.WriteString(fmt.Sprintf("User: %s\n", r.Spec.Task))
		if (phase == "Succeeded" || phase == "Completed") && r.Status.Result != "" {
			sb.WriteString(fmt.Sprintf("Assistant: %s\n", r.Status.Result))
		} else if phase == "Failed" {
			sb.WriteString(fmt.Sprintf("Assistant: [error: %s]\n", r.Status.Error))
		} else {
			sb.WriteString("Assistant: [pending]\n")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, refreshDataCmd(), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func refreshDataCmd() tea.Cmd {
	return func() tea.Msg {
		if k8sClient == nil {
			return dataRefreshMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()

		var (
			inst    sympoziumv1alpha1.SympoziumInstanceList
			runs    sympoziumv1alpha1.AgentRunList
			pols    sympoziumv1alpha1.SympoziumPolicyList
			skls    sympoziumv1alpha1.SkillPackList
			scheds  sympoziumv1alpha1.SympoziumScheduleList
			packs   sympoziumv1alpha1.PersonaPackList
			podList corev1.PodList
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
		wg.Add(7)

		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &inst); err != nil {
				addErr(fmt.Sprintf("instances: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &runs); err != nil {
				addErr(fmt.Sprintf("runs: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &pols); err != nil {
				addErr(fmt.Sprintf("policies: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &skls); err != nil {
				addErr(fmt.Sprintf("skills: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &scheds); err != nil {
				addErr(fmt.Sprintf("schedules: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &podList, client.MatchingLabels{"app.kubernetes.io/managed-by": "sympozium"}); err != nil {
				addErr(fmt.Sprintf("pods: %v", err))
			}
		}()
		go func() {
			defer wg.Done()
			if err := k8sClient.List(ctx, &packs); err != nil {
				addErr(fmt.Sprintf("personapacks: %v", err))
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
		if !containsPrefix(errs, "personapacks:") {
			msg.personaPacks = &packs.Items
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
						Instance: r.Spec.InstanceRef,
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
				m.editPersonaPackName = ""
				m.addLog(tuiDimStyle.Render("Edit cancelled"))
				return m, nil
			case "tab":
				if m.editPersonaPackName != "" {
					return m, nil // no tabs in persona pack mode
				}
				m.editTab = (m.editTab + 1) % len(editTabNames)
				m.editField = 0
				return m, nil
			case "shift+tab":
				if m.editPersonaPackName != "" {
					return m, nil // no tabs in persona pack mode
				}
				m.editTab = (m.editTab + len(editTabNames) - 1) % len(editTabNames)
				m.editField = 0
				return m, nil
			case "j", "down":
				max := editMemoryFieldCount
				if m.editPersonaPackName != "" {
					max = len(m.editPersonas)
				} else if m.editTab == 1 {
					max = editHeartbeatFieldCount
				} else if m.editTab == 2 {
					max = len(m.editSkills)
				} else if m.editTab == 3 {
					max = len(m.editChannels)
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
				if m.editPersonaPackName != "" {
					if m.editField >= 0 && m.editField < len(m.editPersonas) {
						m.editPersonas[m.editField].enabled = !m.editPersonas[m.editField].enabled
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
						m.editSkills[m.editField].enabled = !m.editSkills[m.editField].enabled
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
				}
				return m, nil
			case "left", "h":
				// Cycle enum fields backward
				if m.editTab == 1 {
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
				if m.editTab == 1 {
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
				if m.editPersonaPackName != "" {
					if m.editField >= 0 && m.editField < len(m.editPersonas) {
						m.editPersonas[m.editField].enabled = !m.editPersonas[m.editField].enabled
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
						m.editSkills[m.editField].enabled = !m.editSkills[m.editField].enabled
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
				}
				return m, nil
			case "ctrl+s":
				// Apply changes
				m.showEditModal = false
				editPackName := m.editPersonaPackName
				m.editPersonaPackName = ""
				if editPackName != "" {
					return m, m.applyPersonaPackEdit(editPackName)
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
						m.addLog(tuiErrorStyle.Render("No instance selected"))
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
							tuiDimStyle.Render("⚠ WhatsApp QR pairing skipped — scan later via: kubectl logs -l sympozium.ai/channel=whatsapp,sympozium.ai/instance="+m.wizard.instanceName+" -n "+m.namespace))
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
		case "esc":
			// Go back: clear drill-in filter or return to Instances view.
			if m.drillInstance != "" {
				m.drillInstance = ""
				m.activeView = viewInstances
				m.selectedRow = 0
				m.tableScroll = 0
				return m, nil
			}
			if m.activeView != viewInstances {
				m.activeView = viewInstances
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
			m.activeView = viewPersonas
			m.selectedRow = 0
			m.tableScroll = 0
			return m, nil
		case "2":
			m.activeView = viewInstances
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
			return m, refreshDataCmd()
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
		if msg.personaPacks != nil {
			m.personaPacks = *msg.personaPacks
		}
		if msg.fetchErr != "" {
			m.addLog(tuiErrorStyle.Render("✗ Fetch error: " + msg.fetchErr))
			m.connected = false
		} else {
			m.connected = true
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
				return m, pollWhatsAppQRCmd(m.namespace, m.wizard.instanceName)
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
			m.wizard.resultMsgs = strings.Split(msg.output, "\n")
			if m.wizard.personaHasEnabledChannel("whatsapp") {
				m.wizard.instanceName = m.firstPersonaInstanceName(m.wizard.personaPackName)
				if m.wizard.instanceName == "" {
					m.wizard.instanceName = m.wizard.personaPackName
				}
				m.wizard.step = wizStepWhatsAppQR
				m.wizard.qrStatus = "waiting"
				m.wizard.resultMsgs = append(m.wizard.resultMsgs,
					tuiDimStyle.Render(fmt.Sprintf("Showing WhatsApp pairing for %s", m.wizard.instanceName)))
				m.input.Placeholder = "Waiting for WhatsApp pod... (press Esc to skip)"
				return m, pollWhatsAppQRCmd(m.namespace, m.wizard.instanceName)
			}
			m.wizard.step = wizStepPersonaDone
			m.input.Placeholder = "Press Enter to switch to Instances"
			return m, nil
		}
		if msg.err != nil {
			m.addLog(tuiErrorStyle.Render("✗ " + msg.err.Error()))
		} else if msg.output != "" {
			m.addLog(msg.output)
		}
		return m, refreshDataCmd()

	case whatsappQRPollMsg:
		if m.wizard.active && m.wizard.step == wizStepWhatsAppQR {
			if msg.err != nil {
				m.wizard.qrErr = msg.err.Error()
				m.wizard.qrStatus = "error"
				// Retry despite error
				return m, pollWhatsAppQRCmd(m.namespace, m.wizard.instanceName)
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
			return m, pollWhatsAppQRCmd(m.namespace, m.wizard.instanceName)
		}
		return m, nil

	case suggestionsMsg:
		m.suggestions = msg.items
		m.suggestIdx = 0
		return m, nil

	case tickMsg:
		return m, tea.Batch(refreshDataCmd(), tickCmd())
	}

	if m.inputFocused {
		m.input, tiCmd = m.input.Update(msg)
		return m, tiCmd
	}
	return m, nil
}

func (m tuiModel) activeViewCount() int {
	switch m.activeView {
	case viewInstances:
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
	case viewPersonas:
		return len(m.personaPacks)
	}
	return 0
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

func (m *tuiModel) addLog(s string) {
	// Split multi-line output into individual lines so that the log pane
	// layout (which assumes one visual line per entry) is not broken.
	for _, line := range strings.Split(s, "\n") {
		m.logLines = append(m.logLines, line)
	}
	if len(m.logLines) > maxLogLines {
		m.logLines = m.logLines[len(m.logLines)-maxLogLines:]
	}
}

func (m tuiModel) handleRowAction() (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewRuns:
		if m.selectedRow < len(m.runs) {
			name := m.runs[m.selectedRow].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiRunStatus(m.namespace, name)
			})
		}
	case viewInstances:
		if m.selectedRow < len(m.instances) {
			inst := m.instances[m.selectedRow]
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
		if m.selectedRow < len(m.policies) {
			name := m.policies[m.selectedRow].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiListFeatures(m.namespace, name)
			})
		}
	case viewChannels:
		filtered := m.filteredChannels()
		if m.selectedRow < len(filtered) {
			ch := filtered[m.selectedRow]
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
		filtered := m.filteredPods()
		if m.selectedRow < len(filtered) {
			p := filtered[m.selectedRow]
			m.addLog(fmt.Sprintf("%s │ inst:%s phase:%s node:%s ip:%s restarts:%d",
				p.Name, p.Instance, p.Phase, p.Node, p.IP, p.Restarts))
		}
	case viewSchedules:
		if m.selectedRow < len(m.schedules) {
			s := m.schedules[m.selectedRow]
			nextRun := "?"
			if s.Status.NextRunTime != nil {
				nextRun = shortDuration(time.Until(s.Status.NextRunTime.Time))
			}
			m.addLog(fmt.Sprintf("%s │ inst:%s cron:%s type:%s phase:%s runs:%d next:%s",
				s.Name, s.Spec.InstanceRef, s.Spec.Schedule, s.Spec.Type, s.Status.Phase, s.Status.TotalRuns, nextRun))
		}
	case viewPersonas:
		if m.selectedRow < len(m.personaPacks) {
			pp := m.personaPacks[m.selectedRow]
			// Start the persona onboarding wizard with this pack pre-selected.
			return m.startPersonaWizard(pp.Name)
		}
	}
	return m, nil
}

func (m tuiModel) handleRowLogs() (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewPods:
		filtered := m.filteredPods()
		if m.selectedRow < len(filtered) {
			podName := filtered[m.selectedRow].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiPodLogs(m.namespace, podName)
			})
		}
	case viewRuns:
		if m.selectedRow < len(m.runs) {
			run := m.runs[m.selectedRow]
			if run.Status.PodName != "" {
				return m, m.asyncCmd(func() (string, error) {
					return tuiPodLogs(m.namespace, run.Status.PodName)
				})
			}
			m.addLog(tuiDimStyle.Render("No pod yet for run: " + run.Name))
		}
	case viewInstances:
		if m.selectedRow < len(m.instances) {
			inst := m.instances[m.selectedRow]
			// Show events for the instance.
			return m, m.asyncCmd(func() (string, error) {
				return tuiResourceEvents(m.namespace, "SympoziumInstance", inst.Name)
			})
		}
	case viewPolicies:
		if m.selectedRow < len(m.policies) {
			name := m.policies[m.selectedRow].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiResourceEvents(m.namespace, "SympoziumPolicy", name)
			})
		}
	case viewSkills:
		if m.selectedRow < len(m.skills) {
			name := m.skills[m.selectedRow].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiResourceEvents(m.namespace, "SkillPack", name)
			})
		}
	case viewChannels:
		filtered := m.filteredChannels()
		if m.selectedRow < len(filtered) {
			ch := filtered[m.selectedRow]
			return m, m.asyncCmd(func() (string, error) {
				return tuiResourceEvents(m.namespace, "SympoziumInstance", ch.InstanceName)
			})
		}
	case viewSchedules:
		if m.selectedRow < len(m.schedules) {
			name := m.schedules[m.selectedRow].Name
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
	case viewInstances:
		if m.selectedRow < len(m.instances) {
			name := m.instances[m.selectedRow].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "sympoziuminstance", name)
			})
		}
	case viewRuns:
		if m.selectedRow < len(m.runs) {
			name := m.runs[m.selectedRow].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "agentrun", name)
			})
		}
	case viewPolicies:
		if m.selectedRow < len(m.policies) {
			name := m.policies[m.selectedRow].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "sympoziumpolicy", name)
			})
		}
	case viewSkills:
		if m.selectedRow < len(m.skills) {
			name := m.skills[m.selectedRow].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "skillpack", name)
			})
		}
	case viewPods:
		filtered := m.filteredPods()
		if m.selectedRow < len(filtered) {
			podName := filtered[m.selectedRow].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "pod", podName)
			})
		}
	case viewChannels:
		filtered := m.filteredChannels()
		if m.selectedRow < len(filtered) {
			ch := filtered[m.selectedRow]
			// Describe the parent instance for the channel.
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "sympoziuminstance", ch.InstanceName)
			})
		}
	case viewSchedules:
		if m.selectedRow < len(m.schedules) {
			name := m.schedules[m.selectedRow].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "sympoziumschedule", name)
			})
		}
	case viewPersonas:
		if m.selectedRow < len(m.personaPacks) {
			name := m.personaPacks[m.selectedRow].Name
			return m, m.asyncCmd(func() (string, error) {
				return tuiDescribeResource(m.namespace, "personapack", name)
			})
		}
	}
	return m, nil
}

func (m tuiModel) handleRowDelete() (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewInstances:
		if m.selectedRow < len(m.instances) {
			inst := m.instances[m.selectedRow]
			name := inst.Name
			ns := m.namespace
			// Check if this instance belongs to a PersonaPack.
			packName := inst.Labels["sympozium.ai/persona-pack"]
			personaName := inst.Labels["sympozium.ai/persona"]
			if packName != "" && personaName != "" {
				m.confirmDelete = true
				m.deleteResourceKind = "persona in pack " + packName
				m.deleteResourceName = personaName
				m.deleteFunc = func() (string, error) {
					return tuiDisablePackPersona(ns, packName, personaName)
				}
			} else {
				m.confirmDelete = true
				m.deleteResourceKind = "instance"
				m.deleteResourceName = name
				m.deleteFunc = func() (string, error) { return tuiDelete(ns, "instance", name) }
			}
		}
	case viewRuns:
		if m.selectedRow < len(m.runs) {
			name := m.runs[m.selectedRow].Name
			m.confirmDelete = true
			m.deleteResourceKind = "run"
			m.deleteResourceName = name
			ns := m.namespace
			m.deleteFunc = func() (string, error) { return tuiDelete(ns, "run", name) }
		}
	case viewPolicies:
		if m.selectedRow < len(m.policies) {
			name := m.policies[m.selectedRow].Name
			m.confirmDelete = true
			m.deleteResourceKind = "policy"
			m.deleteResourceName = name
			ns := m.namespace
			m.deleteFunc = func() (string, error) { return tuiDelete(ns, "policy", name) }
		}
	case viewChannels:
		filtered := m.filteredChannels()
		if m.selectedRow < len(filtered) {
			ch := filtered[m.selectedRow]
			m.confirmDelete = true
			m.deleteResourceKind = "channel"
			m.deleteResourceName = ch.InstanceName + "/" + ch.Type
			instName := ch.InstanceName
			chType := ch.Type
			ns := m.namespace
			m.deleteFunc = func() (string, error) { return tuiRemoveChannel(ns, instName, chType) }
		}
	case viewPods:
		filtered := m.filteredPods()
		if m.selectedRow < len(filtered) {
			podName := filtered[m.selectedRow].Name
			m.confirmDelete = true
			m.deleteResourceKind = "pod"
			m.deleteResourceName = podName
			ns := m.namespace
			m.deleteFunc = func() (string, error) { return tuiDeletePod(ns, podName) }
		}
	case viewSchedules:
		if m.selectedRow < len(m.schedules) {
			name := m.schedules[m.selectedRow].Name
			m.confirmDelete = true
			m.deleteResourceKind = "schedule"
			m.deleteResourceName = name
			ns := m.namespace
			m.deleteFunc = func() (string, error) { return tuiDelete(ns, "schedule", name) }
		}
	case viewPersonas:
		if m.selectedRow < len(m.personaPacks) {
			pack := m.personaPacks[m.selectedRow]
			name := pack.Name
			ns := m.namespace
			// Collect all persona names to disable.
			var allNames []string
			for _, p := range pack.Spec.Personas {
				allNames = append(allNames, p.Name)
			}
			m.confirmDelete = true
			m.deleteResourceKind = "all personas in pack"
			m.deleteResourceName = name
			m.deleteFunc = func() (string, error) {
				return tuiDisableAllPackPersonas(ns, name, allNames)
			}
		}
	}
	return m, nil
}

func (m tuiModel) handleRowEdit() (tea.Model, tea.Cmd) {
	switch m.activeView {
	case viewInstances:
		if m.selectedRow >= len(m.instances) {
			return m, nil
		}
		inst := m.instances[m.selectedRow]
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
			if sched.Spec.InstanceRef == inst.Name {
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
		for _, sp := range m.skills {
			m.editSkills = append(m.editSkills, editSkillItem{
				name:     sp.Name,
				enabled:  enabledSkills[sp.Name],
				category: sp.Spec.Category,
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
		m.showEditModal = true
	case viewSchedules:
		if m.selectedRow >= len(m.schedules) {
			return m, nil
		}
		sched := m.schedules[m.selectedRow]
		m.editScheduleName = sched.Name
		m.editInstanceName = sched.Spec.InstanceRef
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
			if inst.Name == sched.Spec.InstanceRef {
				if inst.Spec.Memory != nil {
					m.editMemory = editMemoryForm{
						enabled:      inst.Spec.Memory.Enabled,
						maxSizeKB:    fmt.Sprintf("%d", inst.Spec.Memory.MaxSizeKB),
						systemPrompt: inst.Spec.Memory.SystemPrompt,
					}
				}
				enabledSkills := make(map[string]bool)
				for _, sr := range inst.Spec.Skills {
					if sr.SkillPackRef != "" {
						enabledSkills[sr.SkillPackRef] = true
					}
				}
				for _, sp := range m.skills {
					m.editSkills = append(m.editSkills, editSkillItem{
						name:     sp.Name,
						enabled:  enabledSkills[sp.Name],
						category: sp.Spec.Category,
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
	case viewPersonas:
		if m.selectedRow >= len(m.personaPacks) {
			return m, nil
		}
		pp := m.personaPacks[m.selectedRow]
		m.editPersonaPackName = pp.Name
		m.editInstanceName = ""
		m.editScheduleName = ""
		m.editTab = 0
		m.editField = 0

		// Build persona toggle list from the pack spec.
		excluded := make(map[string]bool)
		for _, e := range pp.Spec.ExcludePersonas {
			excluded[e] = true
		}
		m.editPersonas = nil
		for _, p := range pp.Spec.Personas {
			dn := p.DisplayName
			if dn == "" {
				dn = p.Name
			}
			m.editPersonas = append(m.editPersonas, editPersonaItem{
				name:        p.Name,
				displayName: dn,
				enabled:     !excluded[p.Name],
			})
		}
		m.showEditModal = true
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

		// Apply memory, skills, and channel changes to SympoziumInstance.
		if instName != "" {
			var inst sympoziumv1alpha1.SympoziumInstance
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: instName, Namespace: ns}, &inst); err != nil {
				return cmdResultMsg{err: fmt.Errorf("get instance %q: %w", instName, err)}
			}
			maxKB := 256
			if v, err := strconv.Atoi(mem.maxSizeKB); err == nil && v > 0 {
				maxKB = v
			}
			inst.Spec.Memory = &sympoziumv1alpha1.MemorySpec{
				Enabled:      mem.enabled,
				MaxSizeKB:    maxKB,
				SystemPrompt: mem.systemPrompt,
			}

			// Apply skill toggles to instance.
			var skillRefs []sympoziumv1alpha1.SkillRef
			for _, sk := range skills {
				if sk.enabled {
					skillRefs = append(skillRefs, sympoziumv1alpha1.SkillRef{
						SkillPackRef: sk.name,
					})
				}
			}
			inst.Spec.Skills = skillRefs

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
			inst.Spec.Channels = channelSpecs

			if err := k8sClient.Update(ctx, &inst); err != nil {
				return cmdResultMsg{err: fmt.Errorf("update instance %q: %w", instName, err)}
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
					InstanceRef:       instName,
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

// applyPersonaPackEdit saves the persona enable/disable toggles back to the PersonaPack.
func (m tuiModel) applyPersonaPackEdit(packName string) tea.Cmd {
	ns := m.namespace
	personas := make([]editPersonaItem, len(m.editPersonas))
	copy(personas, m.editPersonas)
	return func() tea.Msg {
		ctx := context.Background()

		var pack sympoziumv1alpha1.PersonaPack
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: packName, Namespace: ns}, &pack); err != nil {
			return cmdResultMsg{err: fmt.Errorf("get PersonaPack %q: %w", packName, err)}
		}

		// Build new ExcludePersonas list from disabled toggles.
		var excludes []string
		for _, p := range personas {
			if !p.enabled {
				excludes = append(excludes, p.name)
			}
		}
		pack.Spec.ExcludePersonas = excludes

		if err := k8sClient.Update(ctx, &pack); err != nil {
			return cmdResultMsg{err: fmt.Errorf("update PersonaPack %q: %w", packName, err)}
		}

		enabled := 0
		for _, p := range personas {
			if p.enabled {
				enabled++
			}
		}
		result := tuiSuccessStyle.Render(fmt.Sprintf("✓ PersonaPack %s updated: %d/%d personas enabled", packName, enabled, len(personas)))
		return cmdResultMsg{output: result}
	}
}

func (m tuiModel) handleRunPrompt() (tea.Model, tea.Cmd) {
	var instName string
	switch m.activeView {
	case viewInstances:
		if m.selectedRow < len(m.instances) {
			instName = m.instances[m.selectedRow].Name
		}
	case viewRuns:
		if m.selectedRow < len(m.runs) {
			instName = m.runs[m.selectedRow].Spec.InstanceRef
		}
	case viewChannels:
		filtered := m.filteredChannels()
		if m.selectedRow < len(filtered) {
			instName = filtered[m.selectedRow].InstanceName
		}
	case viewPods:
		filtered := m.filteredPods()
		if m.selectedRow < len(filtered) {
			instName = filtered[m.selectedRow].Instance
		}
	}
	if instName == "" {
		if len(m.instances) > 0 {
			instName = m.instances[0].Name
		} else {
			m.addLog(tuiErrorStyle.Render("No instances available to run against"))
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
				{"install", "Install a PersonaPack"},
				{"delete", "Delete a PersonaPack"},
			} {
				if prefix == "" || strings.HasPrefix(s.text, prefix) {
					matches = append(matches, s)
				}
			}
			m.suggestions = matches
			return nil
		}
		if argIdx == 2 {
			return m.fetchSuggestionsAsync(func() []suggestion { return fetchPersonaPackSuggestions(ns, prefix) })
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
	var list sympoziumv1alpha1.SympoziumInstanceList
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
		if activeOnly && (phase == "Completed" || phase == "Failed") {
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

func fetchPersonaPackSuggestions(ns, prefix string) []suggestion {
	if k8sClient == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var list sympoziumv1alpha1.PersonaPackList
	if err := k8sClient.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil
	}
	var out []suggestion
	for _, pp := range list.Items {
		if prefix == "" || strings.HasPrefix(strings.ToLower(pp.Name), prefix) {
			desc := pp.Spec.Category
			if desc == "" {
				desc = fmt.Sprintf("%d personas", len(pp.Spec.Personas))
			}
			out = append(out, suggestion{text: pp.Name, desc: desc})
		}
	}
	return out
}

func fetchDeleteTargetSuggestions(ns, resourceType, prefix string) []suggestion {
	switch resourceType {
	case "instance", "inst":
		return fetchInstanceSuggestions(ns, prefix)
	case "run":
		return fetchRunSuggestions(ns, prefix, false)
	case "policy", "pol":
		return fetchPolicySuggestions(ns, prefix)
	case "persona":
		return fetchPersonaPackSuggestions(ns, prefix)
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

	case "/instances", "/inst":
		m.activeView = viewInstances
		m.selectedRow = 0
		m.addLog("Switched to Instances view")
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
			m.addLog(fmt.Sprintf("Channels for instance: %s", args[0]))
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
			m.addLog(fmt.Sprintf("Pods for instance: %s", args[0]))
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

	case "/personas":
		m.activeView = viewPersonas
		m.selectedRow = 0
		m.tableScroll = 0
		m.addLog("Switched to Personas view")
		return m, nil

	case "/persona":
		if len(args) < 1 {
			m.addLog(tuiErrorStyle.Render("Usage: /persona delete <pack-name>"))
			m.addLog(tuiDimStyle.Render("  Tip: go to the Personas tab and press Enter on a pack to onboard."))
			return m, nil
		}
		subCmd := strings.ToLower(args[0])
		switch subCmd {
		case "delete":
			if len(args) < 2 {
				m.addLog(tuiErrorStyle.Render("Usage: /persona delete <pack-name>"))
				return m, nil
			}
			packName := args[1]
			ns := m.namespace
			return m, m.asyncCmd(func() (string, error) { return tuiDeletePersonaPack(ns, packName) })
		default:
			m.addLog(tuiErrorStyle.Render("Unknown sub-command. Usage: /persona delete <pack-name>"))
			m.addLog(tuiDimStyle.Render("  Tip: go to the Personas tab and press Enter on a pack to onboard."))
		}
		return m, nil

	case "/schedule":
		if len(args) < 3 {
			m.addLog(tuiErrorStyle.Render("Usage: /schedule <instance> <cron> <task>"))
			return m, nil
		}
		inst := args[0]
		cronExpr := args[1]
		task := strings.Join(args[2:], " ")
		return m, m.asyncCmd(func() (string, error) { return tuiCreateSchedule(m.namespace, inst, cronExpr, task) })

	case "/memory":
		if len(args) < 1 {
			m.addLog(tuiErrorStyle.Render("Usage: /memory <instance>"))
			return m, nil
		}
		inst := args[0]
		return m, m.asyncCmd(func() (string, error) { return tuiShowMemory(m.namespace, inst) })

	case "/channel":
		if len(args) < 3 {
			m.addLog(tuiErrorStyle.Render("Usage: /channel <instance> <type> <secret-name>"))
			return m, nil
		}
		inst, chType, secret := args[0], args[1], args[2]
		return m, m.asyncCmd(func() (string, error) { return tuiAddChannel(m.namespace, inst, chType, secret) })

	case "/rmchannel":
		if len(args) < 2 {
			m.addLog(tuiErrorStyle.Render("Usage: /rmchannel <instance> <channel-type>"))
			return m, nil
		}
		return m, m.asyncCmd(func() (string, error) { return tuiRemoveChannel(m.namespace, args[0], args[1]) })

	case "/provider":
		if len(args) < 3 {
			m.addLog(tuiErrorStyle.Render("Usage: /provider <instance> <provider> <model>"))
			return m, nil
		}
		inst, prov, model := args[0], args[1], args[2]
		return m, m.asyncCmd(func() (string, error) { return tuiSetProvider(m.namespace, inst, prov, model) })

	case "/baseurl":
		if len(args) < 2 {
			m.addLog(tuiErrorStyle.Render("Usage: /baseurl <instance> <url>"))
			return m, nil
		}
		return m, m.asyncCmd(func() (string, error) { return tuiSetBaseURL(m.namespace, args[0], args[1]) })

	case "/run":
		if len(args) < 2 {
			m.addLog(tuiErrorStyle.Render("Usage: /run <instance> <task>  (or press R to quick-run)"))
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
		return m, refreshDataCmd()

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
	m.wizard.personaPackName = packName
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

func (m tuiModel) firstPersonaInstanceName(packName string) string {
	for _, pack := range m.personaPacks {
		if pack.Name != packName || len(pack.Spec.Personas) == 0 {
			continue
		}
		return fmt.Sprintf("%s-%s", packName, pack.Spec.Personas[0].Name)
	}
	return ""
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
		showPackPane := m.wizard.personaMode && m.width >= 90 && m.wizard.personaPackName != ""
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
			rightPane := m.renderPersonaPackDetailPane(rightW, paneH)
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
	chrome := 1 + 1 + 1 + 1 + 1 + inputH + suggestH + 1 // header+tabs+colhdr+sep(above log)+sep(below log)+input+suggest+statusbar
	available := m.height - chrome
	if available < 4 {
		available = 4
	}
	tableH := available / 2
	logH := available - tableH
	if tableH < 2 {
		tableH = 2
	}
	if logH < 3 {
		logH = 3
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

	// 5. Separator
	view.WriteString(tuiSepStyle.Render(strings.Repeat("─", m.width)))
	view.WriteString("\n")

	// 6. Log pane
	view.WriteString(m.renderLog(logH))

	// 7. Separator
	view.WriteString(tuiSepStyle.Render(strings.Repeat("─", m.width)))
	view.WriteString("\n")

	// 8. Suggestions + Input
	if len(m.suggestions) > 0 {
		view.WriteString(m.renderSuggestions())
	}
	if m.inputFocused {
		m.input.Width = m.width - 4 // reserve space for prompt and padding
		view.WriteString(" " + m.input.View())
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

	ns := tuiDimStyle.Render(" ns:") + lipgloss.NewStyle().Foreground(lipgloss.Color("#F5C2E7")).Render(m.namespace)

	counts := tuiDimStyle.Render(" │ ") +
		tuiCountStyle.Render(fmt.Sprintf("%d", len(m.instances))) + tuiDimStyle.Render(" inst ") +
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
	return lipgloss.NewStyle().Background(lipgloss.Color("#0F0F23")).Render(left + pad)
}

func (m tuiModel) renderTabBar() string {
	var tabs strings.Builder
	for i, name := range viewNames {
		label := fmt.Sprintf(" %d:%s ", i+1, name)
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
			Foreground(lipgloss.Color("#F5C2E7")).
			Background(lipgloss.Color("#0F0F23")).
			Render("⊳ " + m.drillInstance))
	}
	left := tabs.String()
	w := lipgloss.Width(left)
	pad := ""
	if m.width > w {
		pad = strings.Repeat(" ", m.width-w)
	}
	return lipgloss.NewStyle().Background(lipgloss.Color("#0F0F23")).Render(left + pad)
}

func (m tuiModel) renderTable(tableH int) string {
	var b strings.Builder

	switch m.activeView {
	case viewInstances:
		b.WriteString(m.renderInstancesTable(tableH))
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
	case viewPersonas:
		b.WriteString(m.renderPersonasTable(tableH))
	}

	return b.String()
}

func (m tuiModel) renderInstancesTable(tableH int) string {
	var b strings.Builder

	header := fmt.Sprintf(" %-22s %-12s %-20s %-8s %-12s %-8s", "NAME", "PHASE", "SKILLS", "PODS", "TOKENS", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if len(m.instances) == 0 {
		b.WriteString(m.renderEmptyTable(tableH-1, "No instances — press O to onboard or type /onboard"))
		return b.String()
	}

	// Pre-compute total token usage per instance from completed runs.
	instanceTokens := make(map[string]int)
	for _, run := range m.runs {
		if run.Status.TokenUsage != nil {
			instanceTokens[run.Spec.InstanceRef] += run.Status.TokenUsage.TotalTokens
		}
	}

	for i := 0; i < tableH-1; i++ {
		idx := i + m.tableScroll
		if idx >= len(m.instances) {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		inst := m.instances[idx]
		age := shortDuration(time.Since(inst.CreationTimestamp.Time))

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

		row := fmt.Sprintf(" %-22s %-12s %-20s %-8d %-12s %-8s",
			truncate(inst.Name, 22), inst.Status.Phase, truncate(skillStr, 20), inst.Status.ActiveAgentPods, tokStr, age)

		b.WriteString(m.styleRow(idx, row))
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

	header := fmt.Sprintf(" %-26s %-18s %-12s %-14s %-18s %-8s", "NAME", "INSTANCE", "PHASE", "TRIGGER", "POD", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if len(m.runs) == 0 {
		b.WriteString(m.renderEmptyTable(tableH-1, "No runs — try: /run <instance> <task>"))
		return b.String()
	}

	// Styles for trigger badges.
	triggerHeartbeat := lipgloss.NewStyle().Foreground(lipgloss.Color("#F5C2E7")).Bold(true)
	triggerScheduled := lipgloss.NewStyle().Foreground(lipgloss.Color("#89DCEB"))
	triggerSweep := lipgloss.NewStyle().Foreground(lipgloss.Color("#FAB387"))

	for i := 0; i < tableH-1; i++ {
		idx := i + m.tableScroll
		if idx >= len(m.runs) {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		run := m.runs[idx]
		age := shortDuration(time.Since(run.CreationTimestamp.Time))
		pod := run.Status.PodName
		if pod == "" {
			pod = "-"
		}
		phase := string(run.Status.Phase)
		if phase == "" {
			phase = "Pending"
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
		nameCol := fmt.Sprintf(" %-26s %-18s ", truncate(run.Name, 26), truncate(run.Spec.InstanceRef, 18))
		phaseCol := fmt.Sprintf("%-12s ", phase)
		trigCol := fmt.Sprintf("%-14s ", truncate(triggerText, 14))
		restCol := fmt.Sprintf("%-18s %-8s", truncate(pod, 18), age)

		if idx == m.selectedRow {
			b.WriteString(tuiRowSelectedStyle.Render(padRight(nameCol+phaseCol+trigCol+restCol, m.width)))
		} else {
			style := tuiRowStyle
			if idx%2 == 1 {
				style = tuiRowAltStyle
			}
			// Colorize phase.
			switch phase {
			case "Running":
				phaseCol = tuiRunningStyle.Render(fmt.Sprintf("%-12s ", phase))
			case "Completed":
				phaseCol = tuiSuccessStyle.Render(fmt.Sprintf("%-12s ", phase))
			case "Failed", "Timeout":
				phaseCol = tuiErrorStyle.Render(fmt.Sprintf("%-12s ", phase))
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
			b.WriteString(style.Render(nameCol) + phaseCol + trigCol + style.Render(restCol))
			// Pad remaining.
			renderedW := lipgloss.Width(style.Render(nameCol)) + lipgloss.Width(phaseCol) + lipgloss.Width(trigCol) + lipgloss.Width(style.Render(restCol))
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

	header := fmt.Sprintf(" %-26s %-18s %-8s", "NAME", "BOUND INSTANCES", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if len(m.policies) == 0 {
		b.WriteString(m.renderEmptyTable(tableH-1, "No policies found"))
		return b.String()
	}

	for i := 0; i < tableH-1; i++ {
		idx := i + m.tableScroll
		if idx >= len(m.policies) {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		pol := m.policies[idx]
		age := shortDuration(time.Since(pol.CreationTimestamp.Time))
		row := fmt.Sprintf(" %-26s %-18d %-8s", truncate(pol.Name, 26), pol.Status.BoundInstances, age)
		b.WriteString(m.styleRow(idx, row))
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) renderSkillsTable(tableH int) string {
	var b strings.Builder

	header := fmt.Sprintf(" %-26s %-10s %-26s %-8s", "NAME", "SKILLS", "CONFIGMAP", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if len(m.skills) == 0 {
		b.WriteString(m.renderEmptyTable(tableH-1, "No skill packs found"))
		return b.String()
	}

	for i := 0; i < tableH-1; i++ {
		idx := i + m.tableScroll
		if idx >= len(m.skills) {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		sk := m.skills[idx]
		age := shortDuration(time.Since(sk.CreationTimestamp.Time))
		cm := sk.Status.ConfigMapName
		if cm == "" {
			cm = "-"
		}
		row := fmt.Sprintf(" %-26s %-10d %-26s %-8s", truncate(sk.Name, 26), len(sk.Spec.Skills), truncate(cm, 26), age)
		b.WriteString(m.styleRow(idx, row))
		b.WriteString("\n")
	}
	return b.String()
}

func (m tuiModel) renderChannelsTable(tableH int) string {
	var b strings.Builder

	filterLabel := ""
	if m.drillInstance != "" {
		filterLabel = " [" + m.drillInstance + "]"
	}
	header := fmt.Sprintf(" %-20s %-12s %-22s %-14s %-10s %-20s", "INSTANCE"+filterLabel, "TYPE", "SECRET", "STATUS", "CHECKED", "MESSAGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	filtered := m.filteredChannels()
	if len(filtered) == 0 {
		msg := "No channels — try: /channel <instance> <type> <secret>"
		if m.drillInstance != "" {
			msg = fmt.Sprintf("No channels on %s — try: /channel %s telegram my-secret", m.drillInstance, m.drillInstance)
		}
		b.WriteString(m.renderEmptyTable(tableH-1, msg))
		return b.String()
	}

	for i := 0; i < tableH-1; i++ {
		idx := i + m.tableScroll
		if idx >= len(filtered) {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		ch := filtered[idx]
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

		if idx == m.selectedRow {
			b.WriteString(tuiRowSelectedStyle.Render(padRight(nameCol+statusCol+restCol, m.width)))
		} else {
			style := tuiRowStyle
			if idx%2 == 1 {
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
	header := fmt.Sprintf(" %-30s %-20s %-12s %-16s %-16s %-10s %-8s", "NAME"+filterLabel, "INSTANCE", "PHASE", "NODE", "IP", "RESTARTS", "AGE")
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

	for i := 0; i < tableH-1; i++ {
		idx := i + m.tableScroll
		if idx >= len(filtered) {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		p := filtered[idx]
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

		if idx == m.selectedRow {
			b.WriteString(tuiRowSelectedStyle.Render(padRight(nameCol+phaseCol+restCol, m.width)))
		} else {
			style := tuiRowStyle
			if idx%2 == 1 {
				style = tuiRowAltStyle
			}
			switch p.Phase {
			case "Running":
				phaseCol = tuiRunningStyle.Render(fmt.Sprintf("%-12s ", p.Phase))
			case "Succeeded":
				phaseCol = tuiSuccessStyle.Render(fmt.Sprintf("%-12s ", p.Phase))
			case "Failed":
				phaseCol = tuiErrorStyle.Render(fmt.Sprintf("%-12s ", p.Phase))
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

	header := fmt.Sprintf(" %-24s %-18s %-18s %-12s %-10s %-10s %-8s", "NAME", "INSTANCE", "SCHEDULE", "TYPE", "PHASE", "RUNS", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if len(m.schedules) == 0 {
		b.WriteString(m.renderEmptyTable(tableH-1, "No schedules — try: /schedule <instance> <cron> <task>"))
		return b.String()
	}

	for i := 0; i < tableH-1; i++ {
		idx := i + m.tableScroll
		if idx >= len(m.schedules) {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
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

		nameCol := fmt.Sprintf(" %-24s %-18s %-18s ", truncate(s.Name, 24), truncate(s.Spec.InstanceRef, 18), truncate(s.Spec.Schedule, 18))
		typeCol := fmt.Sprintf("%-12s ", schedType)
		phaseCol := fmt.Sprintf("%-10s ", phase)
		restCol := fmt.Sprintf("%-10d %-8s", s.Status.TotalRuns, age)

		if idx == m.selectedRow {
			b.WriteString(tuiRowSelectedStyle.Render(padRight(nameCol+typeCol+phaseCol+restCol, m.width)))
		} else {
			style := tuiRowStyle
			if idx%2 == 1 {
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

func (m tuiModel) renderPersonasTable(tableH int) string {
	var b strings.Builder

	header := fmt.Sprintf(" %-24s %-14s %-10s %-10s %-12s %-8s", "NAME", "CATEGORY", "AGENTS", "INSTALLED", "PHASE", "AGE")
	b.WriteString(tuiColHeaderStyle.Render(padRight(header, m.width)))
	b.WriteString("\n")

	if len(m.personaPacks) == 0 {
		b.WriteString(m.renderEmptyTable(tableH-1, "No PersonaPacks found — run 'sympozium install' to add built-in packs"))
		return b.String()
	}

	for i := 0; i < tableH-1; i++ {
		idx := i + m.tableScroll
		if idx >= len(m.personaPacks) {
			b.WriteString(strings.Repeat(" ", m.width) + "\n")
			continue
		}
		pp := m.personaPacks[idx]
		age := shortDuration(time.Since(pp.CreationTimestamp.Time))
		phase := pp.Status.Phase
		if phase == "" {
			phase = "Pending"
		}
		cat := pp.Spec.Category
		if cat == "" {
			cat = "-"
		}

		agentCount := len(pp.Spec.Personas)
		row := fmt.Sprintf(" %-24s %-14s %-10d %-10d %-12s %-8s",
			truncate(pp.Name, 24), truncate(cat, 14), agentCount, pp.Status.InstalledCount, phase, age)

		if idx == m.selectedRow {
			b.WriteString(tuiRowSelectedStyle.Render(padRight(row, m.width)))
		} else {
			style := tuiRowStyle
			if idx%2 == 1 {
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
	title := tuiLogBorderStyle.Render("─── Log ")
	titleW := lipgloss.Width(title)
	if m.width > titleW {
		title += tuiSepStyle.Render(strings.Repeat("─", m.width-titleW))
	}
	b.WriteString(title + "\n")

	start := len(m.logLines) - (logH - 1)
	if start < 0 {
		start = 0
	}
	visible := m.logLines[start:]
	maxW := m.width - 1 // 1 for leading space
	if maxW < 10 {
		maxW = 10
	}
	for i := 0; i < logH-1; i++ {
		if i < len(visible) {
			line := visible[i]
			// Truncate to fit pane while preserving ANSI styles.
			if lipgloss.Width(line) > maxW {
				line = ansiTruncate(line, maxW)
			}
			b.WriteString(" " + line)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderDetailPane dispatches to the correct detail pane content based on the
// active tab. Channels tab never shows a detail pane (handled by caller).
func (m tuiModel) renderDetailPane(width, height int) string {
	switch m.activeView {
	case viewInstances:
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
		allLines = append(allLines, tuiDimStyle.Render("  Select an instance"))
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
		line := statusStyle.Render(" "+statusIcon+" ") + lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4")).Render(ch.Type)
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

	// Find instances that use this skill, then find their runs.
	usingInstances := make(map[string]bool)
	for _, inst := range m.instances {
		for _, sk := range inst.Spec.Skills {
			if sk.SkillPackRef == skillName || sk.ConfigMapRef == skillName {
				usingInstances[inst.Name] = true
			}
		}
	}

	var matchedRuns []sympoziumv1alpha1.AgentRun
	for _, run := range m.runs {
		if usingInstances[run.Spec.InstanceRef] {
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
		case "Running":
			phaseStyle = tuiRunningStyle
		case "Failed", "Timeout":
			phaseStyle = tuiErrorStyle
		}
		nameLine := " " + lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4")).Render(truncate(run.Name, contentW))
		allLines = append(allLines, nameLine)
		metaLine := tuiDimStyle.Render("   "+run.Spec.InstanceRef+" • ") + phaseStyle.Render(phase) + tuiDimStyle.Render(" • "+age)
		allLines = append(allLines, metaLine)

		task := extractUserMessage(run.Spec.Task)
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
	for _, line := range m.logLines {
		if strings.Contains(line, podPrefix) {
			podLogs = append(podLogs, line)
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

// renderDetailFeed shows the conversation feed for the selected instance (used
// by Instances and Runs tabs).
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

	runs := m.runsForInstance(inst)
	if len(runs) == 0 {
		allLines = append(allLines, "")
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
		task := extractUserMessage(run.Spec.Task)
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
		case "Running":
			allLines = append(allLines, tuiRunningStyle.Render("   ⏳ Running..."))
		case "Failed", "Timeout":
			errMsg := run.Status.Error
			if errMsg == "" {
				errMsg = phase
			}
			for _, wl := range wrapText(errMsg, contentW) {
				allLines = append(allLines, tuiErrorStyle.Render("   ✗ "+wl))
			}
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
	case viewInstances:
		// Fall through to chat fullscreen (same as Runs tab)
	}

	// Instances tab, Runs tab and fallback: show the chat fullscreen with input.
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
			task := extractUserMessage(run.Spec.Task)
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
			case "Running":
				allLines = append(allLines, tuiRunningStyle.Render("   ⏳ Running..."))
			case "Failed", "Timeout":
				errMsg := run.Status.Error
				if errMsg == "" {
					errMsg = phase
				}
				for _, wl := range wrapText(errMsg, contentW) {
					allLines = append(allLines, tuiErrorStyle.Render("   ✗ "+wl))
				}
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
			out = append(out, tuiDimStyle.Render(" Select an instance first"))
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
	out = append(out, lipgloss.NewStyle().Background(lipgloss.Color("#181825")).Render(left+pad))

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
	bar := lipgloss.NewStyle().Background(lipgloss.Color("#181825")).Render(left + pad)

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
			"f", "detail pane",
			"F", "fullscreen",
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
	return lipgloss.NewStyle().Background(lipgloss.Color("#181825")).Render(left + pad)
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

func (m tuiModel) renderEditModal(base string) string {
	var content strings.Builder

	// Title
	if m.editPersonaPackName != "" {
		content.WriteString(tuiModalTitleStyle.Render("  ✎  Edit PersonaPack " + m.editPersonaPackName))
	} else {
		title := "Edit " + m.editInstanceName
		if m.editScheduleName != "" {
			title += " / " + m.editScheduleName
		}
		content.WriteString(tuiModalTitleStyle.Render("  ✎  " + title))
	}
	content.WriteString("\n\n")

	// Tab bar (not shown for persona pack edit)
	if m.editPersonaPackName == "" {
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
		Foreground(lipgloss.Color("#1A1A2E")).
		Background(lipgloss.Color("#E94560")).
		Bold(true)

	label := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#CDD6F4")).
		Background(lipgloss.Color("#16213E")).
		Width(20)

	value := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#89DCEB")).
		Background(lipgloss.Color("#16213E"))

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

	if m.editPersonaPackName != "" {
		// PersonaPack edit — persona toggles
		content.WriteString(tuiDimStyle.Render("  Toggle personas on/off with space or enter:") + "\n\n")
		if len(m.editPersonas) == 0 {
			content.WriteString(tuiDimStyle.Render("  No personas defined in this pack.") + "\n")
		} else {
			for i, p := range m.editPersonas {
				tog := "○"
				if p.enabled {
					tog = "●"
				}
				lbl := fmt.Sprintf("  %s %s", tog, p.displayName)
				if p.name != p.displayName {
					lbl += tuiDimStyle.Render(" (" + p.name + ")")
				}
				if m.editField == i {
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
				lbl := fmt.Sprintf("  %s %s%s", tog, sk.name, cat)
				if m.editField == i {
					lbl = highlight.Render(fmt.Sprintf("▸ %s %s%s", tog, sk.name, cat))
				} else {
					lbl = value.Render(lbl)
				}
				content.WriteString("  " + lbl + "\n")
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
	} else if m.editPersonaPackName != "" {
		content.WriteString("\n")
		content.WriteString(tuiDimStyle.Render("  ↑↓ navigate · space/enter toggle · ctrl+s apply · esc cancel"))
	} else {
		content.WriteString("\n")
		content.WriteString(tuiDimStyle.Render("  tab switch tabs · ↑↓ navigate · enter toggle/edit"))
		content.WriteString("\n")
		content.WriteString(tuiDimStyle.Render("  ←→ cycle enums · type text fields · ctrl+s apply · esc cancel"))
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
	var inst sympoziumv1alpha1.SympoziumInstance
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
	if authSecret == "" {
		return "", fmt.Errorf("instance %q has no API key configured (authRefs is empty) — "+
			"activate the persona pack through the TUI onboarding wizard or add an authRef manually", instance)
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
			InstanceRef: instance,
			Task:        task,
			Model: sympoziumv1alpha1.ModelSpec{
				Provider:      provider,
				Model:         inst.Spec.Agents.Default.Model,
				BaseURL:       inst.Spec.Agents.Default.BaseURL,
				AuthSecretRef: authSecret,
			},
			Skills:  inst.Spec.Skills,
			Timeout: &metav1.Duration{Duration: 10 * time.Minute},
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

func tuiAbortRun(ns, name string) (string, error) {
	ctx := context.Background()
	var run sympoziumv1alpha1.AgentRun
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: ns}, &run); err != nil {
		return "", fmt.Errorf("run %q not found: %w", name, err)
	}
	if run.Status.Phase == "Completed" || run.Status.Phase == "Failed" {
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
		run.Name, phase, pod, truncate(run.Spec.Task, 40)))

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
	if run.Status.Error != "" {
		b.WriteString("\n" + tuiErrorStyle.Render("  ✗ "+run.Status.Error))
	}
	return b.String(), nil
}

func tuiClusterStatus(ns string) (string, error) {
	ctx := context.Background()
	var instances sympoziumv1alpha1.SympoziumInstanceList
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
		case "Completed":
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
	case "instance", "inst":
		obj := &sympoziumv1alpha1.SympoziumInstance{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if err := k8sClient.Delete(ctx, obj); err != nil {
			return "", fmt.Errorf("delete instance: %w", err)
		}
		return tuiSuccessStyle.Render(fmt.Sprintf("✓ Deleted instance: %s", name)), nil
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
	case "persona":
		obj := &sympoziumv1alpha1.PersonaPack{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if err := k8sClient.Delete(ctx, obj); err != nil {
			return "", fmt.Errorf("delete persona pack: %w", err)
		}
		return tuiSuccessStyle.Render(fmt.Sprintf("✓ Deleted PersonaPack: %s (owned resources will be garbage-collected)", name)), nil
	default:
		return "", fmt.Errorf("unknown type: %s (use: instance, run, policy, schedule, persona, channel)", resourceType)
	}
}

func tuiAddChannel(ns, instanceName, chType, secretName string) (string, error) {
	ctx := context.Background()
	var inst sympoziumv1alpha1.SympoziumInstance
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: ns}, &inst); err != nil {
		return "", fmt.Errorf("instance %q not found: %w", instanceName, err)
	}

	// Check if channel type already exists.
	for _, ch := range inst.Spec.Channels {
		if strings.EqualFold(ch.Type, chType) {
			return "", fmt.Errorf("channel %q already exists on %s — use /rmchannel first", chType, instanceName)
		}
	}

	inst.Spec.Channels = append(inst.Spec.Channels, sympoziumv1alpha1.ChannelSpec{
		Type: strings.ToLower(chType),
		ConfigRef: sympoziumv1alpha1.SecretRef{
			Secret: secretName,
		},
	})
	if err := k8sClient.Update(ctx, &inst); err != nil {
		return "", fmt.Errorf("update instance: %w", err)
	}
	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Added %s channel to %s (secret: %s)", chType, instanceName, secretName)), nil
}

func tuiRemoveChannel(ns, instanceName, chType string) (string, error) {
	ctx := context.Background()
	var inst sympoziumv1alpha1.SympoziumInstance
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

	inst.Spec.Channels = newChannels
	if err := k8sClient.Update(ctx, &inst); err != nil {
		return "", fmt.Errorf("update instance: %w", err)
	}
	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Removed %s channel from %s", chType, instanceName)), nil
}

func tuiSetProvider(ns, instanceName, provider, model string) (string, error) {
	ctx := context.Background()
	var inst sympoziumv1alpha1.SympoziumInstance
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: ns}, &inst); err != nil {
		return "", fmt.Errorf("instance %q not found: %w", instanceName, err)
	}

	old := inst.Spec.Agents.Default.Model
	inst.Spec.Agents.Default.Model = model
	// BaseURL is cleared when switching provider (user can set it separately with /baseurl).
	if provider != "openai-compatible" {
		inst.Spec.Agents.Default.BaseURL = ""
	}

	if err := k8sClient.Update(ctx, &inst); err != nil {
		return "", fmt.Errorf("update instance: %w", err)
	}
	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Set %s provider=%s model=%s (was: %s)", instanceName, provider, model, old)), nil
}

func tuiCreateSchedule(ns, instanceName, cronExpr, task string) (string, error) {
	ctx := context.Background()

	// Verify instance exists.
	var inst sympoziumv1alpha1.SympoziumInstance
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
			InstanceRef:   instanceName,
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

func tuiInstallPersonaPack(ns, packName string) (string, error) {
	ctx := context.Background()

	// Check if pack already exists.
	var existing sympoziumv1alpha1.PersonaPack
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: packName, Namespace: ns}, &existing); err == nil {
		return "", fmt.Errorf("PersonaPack %q already exists (phase: %s, %d/%d personas installed)",
			packName, existing.Status.Phase, existing.Status.InstalledCount, existing.Status.PersonaCount)
	}

	// Look for a built-in pack YAML on disk. If not found, create a minimal one.
	// The user is expected to have applied the pack YAML via kubectl or sympozium install.
	return "", fmt.Errorf("PersonaPack %q not found in cluster. Apply it first:\n  kubectl apply -f config/personas/%s.yaml", packName, packName)
}

func tuiDeletePersonaPack(ns, packName string) (string, error) {
	ctx := context.Background()

	var pack sympoziumv1alpha1.PersonaPack
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: packName, Namespace: ns}, &pack); err != nil {
		return "", fmt.Errorf("PersonaPack %q not found: %w", packName, err)
	}

	if err := k8sClient.Delete(ctx, &pack); err != nil {
		return "", fmt.Errorf("delete PersonaPack %q: %w", packName, err)
	}

	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Deleted PersonaPack %s (owned resources will be garbage-collected)", packName)), nil
}

func tuiDisablePackPersona(ns, packName, personaName string) (string, error) {
	ctx := context.Background()

	var pack sympoziumv1alpha1.PersonaPack
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: packName, Namespace: ns}, &pack); err != nil {
		return "", fmt.Errorf("PersonaPack %q not found: %w", packName, err)
	}

	// Check if already excluded.
	for _, p := range pack.Spec.ExcludePersonas {
		if p == personaName {
			return tuiDimStyle.Render(fmt.Sprintf("Persona %q is already disabled in pack %s", personaName, packName)), nil
		}
	}

	pack.Spec.ExcludePersonas = append(pack.Spec.ExcludePersonas, personaName)
	if err := k8sClient.Update(ctx, &pack); err != nil {
		return "", fmt.Errorf("update PersonaPack %q: %w", packName, err)
	}

	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Disabled persona %q in pack %s (controller will clean up resources)", personaName, packName)), nil
}

func tuiDisableAllPackPersonas(ns, packName string, personaNames []string) (string, error) {
	ctx := context.Background()

	var pack sympoziumv1alpha1.PersonaPack
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: packName, Namespace: ns}, &pack); err != nil {
		return "", fmt.Errorf("PersonaPack %q not found: %w", packName, err)
	}

	// Build full exclusion list (deduplicated).
	excluded := make(map[string]bool)
	for _, e := range pack.Spec.ExcludePersonas {
		excluded[e] = true
	}
	for _, name := range personaNames {
		excluded[name] = true
	}
	pack.Spec.ExcludePersonas = make([]string, 0, len(excluded))
	for name := range excluded {
		pack.Spec.ExcludePersonas = append(pack.Spec.ExcludePersonas, name)
	}

	if err := k8sClient.Update(ctx, &pack); err != nil {
		return "", fmt.Errorf("update PersonaPack %q: %w", packName, err)
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
	var inst sympoziumv1alpha1.SympoziumInstance
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: instanceName, Namespace: ns}, &inst); err != nil {
		return "", fmt.Errorf("instance %q not found: %w", instanceName, err)
	}

	old := inst.Spec.Agents.Default.BaseURL
	if old == "" {
		old = "(default)"
	}
	inst.Spec.Agents.Default.BaseURL = baseURL
	if err := k8sClient.Update(ctx, &inst); err != nil {
		return "", fmt.Errorf("update instance: %w", err)
	}
	return tuiSuccessStyle.Render(fmt.Sprintf("✓ Set %s baseURL=%s (was: %s)", instanceName, baseURL, old)), nil
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
		var instances sympoziumv1alpha1.SympoziumInstanceList
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
		w.step = wizStepInstanceName
		m.input.Placeholder = "Instance name (default: my-agent)"
		return m, nil

	case wizStepInstanceName:
		if val == "" {
			val = "my-agent"
		}
		w.instanceName = val
		w.step = wizStepProvider
		m.input.Placeholder = "Choice [1-6] (default: 1 — OpenAI)"
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
		w.baseURL = val
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
		if w.secretEnvKey == "" || w.apiKey != "" {
			// Already have the key (or don't need one) — skip to channel.
			w.step = wizStepChannel
			m.input.Placeholder = "Channel [1-5] (default: 5 — skip)"
			return m, nil
		}
		// Edge case: key was skipped — already handled above.
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
		case "2":
			w.channelType = "slack"
			w.channelTokenKey = "SLACK_BOT_TOKEN"
		case "3":
			w.channelType = "discord"
			w.channelTokenKey = "DISCORD_BOT_TOKEN"
		case "4":
			w.channelType = "whatsapp"
			w.channelTokenKey = "" // WhatsApp uses QR pairing, no token needed
		default:
			w.channelType = ""
			w.channelTokenKey = ""
		}
		w.step = wizStepPolicy
		m.input.Placeholder = "Apply default policy? [Y/n]"
		return m, nil

	case wizStepChannelActionToken:
		w.channelToken = val
		w.step = wizStepApplying
		return m, m.asyncCmd(func() (string, error) {
			return tuiOnboardApply(m.namespace, w)
		})

	case wizStepPolicy:
		v := strings.ToLower(val)
		w.applyPolicy = (v == "" || v == "y" || v == "yes")
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
		// Channel-specific action happens at the end, right before apply.
		if w.channelType != "" && w.channelType != "whatsapp" && w.channelTokenKey != "" {
			w.step = wizStepChannelActionToken
			m.input.Placeholder = fmt.Sprintf("%s token (%s, Enter to skip)", w.channelType, w.channelTokenKey)
			return m, nil
		}
		w.step = wizStepApplying
		return m, m.asyncCmd(func() (string, error) {
			return tuiOnboardApply(m.namespace, w)
		})

	case wizStepDone:
		// User pressed Enter on final screen — close wizard.
		personaMode := w.personaMode
		w.reset()
		m.inputFocused = false
		m.input.Blur()
		m.input.Placeholder = "Type / for commands or press ? for help..."
		if personaMode {
			m.activeView = viewInstances
			m.selectedRow = 0
			m.tableScroll = 0
		}
		return m, refreshDataCmd()

	// ── Persona Wizard Steps ─────────────────────────────────────────────
	case wizStepPersonaPick:
		if val == "" {
			// No selection yet — show available packs.
			m.input.Placeholder = "Pack name or number"
			return m, nil
		}
		// Resolve number to name.
		if idx, err := strconv.Atoi(val); err == nil {
			packs := m.personaPacks
			if idx >= 1 && idx <= len(packs) {
				val = packs[idx-1].Name
			}
		}
		// Verify pack exists in cluster.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var pack sympoziumv1alpha1.PersonaPack
		if err := k8sClient.Get(ctx, types.NamespacedName{Name: val, Namespace: m.namespace}, &pack); err != nil {
			w.err = fmt.Sprintf("PersonaPack %q not found in cluster. Have you run 'sympozium install'?", val)
			return m, nil
		}
		w.err = ""
		w.personaPackName = val

		// If already activated, allow re-running the wizard to change auth/model settings.
		if len(pack.Spec.AuthRefs) > 0 && pack.Status.Phase == "Ready" {
			m.addLog(tuiDimStyle.Render(fmt.Sprintf("PersonaPack %q is already activated — re-running wizard to update auth/model settings", val)))
		}

		w.step = wizStepPersonaProvider
		m.input.Placeholder = "Choice [1-6] (default: 1 — OpenAI)"
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
		w.baseURL = val
		if w.secretEnvKey == "" {
			// Ollama — no key needed, skip to model.
			w.step = wizStepPersonaModel
			m.input.Placeholder = "Model name (default: llama3)"
			return m, nil
		}
		w.step = wizStepPersonaAPIKey
		m.input.Placeholder = fmt.Sprintf("%s (paste key, Enter to skip)", w.secretEnvKey)
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
		w.step = wizStepPersonaChannels
		m.input.Placeholder = "Toggle channels with number, Enter when done"
		return m, nil

	case wizStepPersonaChannels:
		val = strings.TrimSpace(val)
		if val == "" {
			// Done selecting channels — move to confirmation.
			w.step = wizStepPersonaConfirm
			m.input.Placeholder = "Proceed? [Y/n]"
			return m, nil
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
		return m.advancePersonaChannelAction()

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
		w.personaChannelIdx = 0
		return m.advancePersonaChannelAction()

	case wizStepPersonaDone:
		w.reset()
		m.inputFocused = false
		m.input.Blur()
		m.input.Placeholder = "Type / for commands or press ? for help..."
		// Switch to Instances view so user sees the newly created agents.
		m.activeView = viewInstances
		m.selectedRow = 0
		m.tableScroll = 0
		return m, refreshDataCmd()
	}

	return m, nil
}

// advancePersonaChannelAction runs post-confirm channel-specific actions.
// It prompts for required channel tokens, then applies the PersonaPack.
func (m tuiModel) advancePersonaChannelAction() (tea.Model, tea.Cmd) {
	w := &m.wizard
	for w.personaChannelIdx < len(w.personaChannels) {
		ch := w.personaChannels[w.personaChannelIdx]
		if ch.enabled && ch.tokenKey != "" {
			// This channel needs a token.
			w.step = wizStepPersonaChannelToken
			m.input.SetValue("")
			m.input.Placeholder = fmt.Sprintf("%s token (%s, Enter to skip)", ch.chType, ch.tokenKey)
			return m, nil
		}
		w.personaChannelIdx++
	}
	// All channel actions complete — apply.
	w.step = wizStepPersonaApplying
	ns := m.namespace
	return m, m.asyncCmd(func() (string, error) {
		return tuiPersonaApply(ns, w)
	})
}

// renderWizardPanel renders the full wizard overlay panel.
func (m tuiModel) renderWizardPanel(h int) string {
	w := &m.wizard

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E94560"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#74C7EC")).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))
	menuStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#CDD6F4"))
	menuNumStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#F5C2E7")).Bold(true)
	hintStyle := tuiDimStyle
	stepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FAB387")).Bold(true)

	// Persona wizard has its own renderer, except for shared QR/done screens.
	if w.personaMode && w.step != wizStepWhatsAppQR && w.step != wizStepDone {
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

	if w.step > wizStepInstanceName {
		stepNum = 2
		lines = append(lines, hintStyle.Render("  Instance: ")+valueStyle.Render(w.instanceName))
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
	if w.step > wizStepChannel {
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
	if w.step > wizStepHeartbeat {
		stepNum = 6
		hbLabel := w.heartbeatLabel
		if hbLabel == "" {
			hbLabel = "every hour"
		}
		lines = append(lines, hintStyle.Render("  Heartbeat: ")+valueStyle.Render(hbLabel))
	}

	if w.step >= wizStepInstanceName && w.step <= wizStepHeartbeat {
		lines = append(lines, "")
	}

	// Show current step prompt.
	switch w.step {
	case wizStepCheckCluster:
		lines = append(lines, stepStyle.Render("  📋 Step 1/6 — Checking cluster..."))

	case wizStepInstanceName:
		lines = append(lines, stepStyle.Render("  📋 Step 1/6 — Create your SympoziumInstance"))
		lines = append(lines, menuStyle.Render("  An instance represents you (or a tenant) in the system."))
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  Enter instance name:"))

	case wizStepProvider:
		lines = append(lines, stepStyle.Render("  📋 Step 2/6 — AI Provider"))
		lines = append(lines, menuStyle.Render("  Which model provider do you want to use?"))
		lines = append(lines, "")
		lines = append(lines, menuNumStyle.Render("  1)")+menuStyle.Render(" OpenAI"))
		lines = append(lines, menuNumStyle.Render("  2)")+menuStyle.Render(" Anthropic"))
		lines = append(lines, menuNumStyle.Render("  3)")+menuStyle.Render(" Azure OpenAI"))
		lines = append(lines, menuNumStyle.Render("  4)")+menuStyle.Render(" Ollama          (local, no API key needed)"))
		lines = append(lines, menuNumStyle.Render("  5)")+menuStyle.Render(" Other / OpenAI-compatible"))

	case wizStepBaseURL:
		lines = append(lines, stepStyle.Render("  📋 Step 2/6 — AI Provider (continued)"))
		lines = append(lines, labelStyle.Render("  Enter base URL:"))

	case wizStepAPIKey:
		lines = append(lines, stepStyle.Render("  📋 Step 2/6 — AI Provider (continued)"))
		lines = append(lines, labelStyle.Render(fmt.Sprintf("  Paste your %s:", w.secretEnvKey)))
		envVal := os.Getenv(w.secretEnvKey)
		if envVal != "" {
			lines = append(lines, hintStyle.Render(fmt.Sprintf("  Press Enter to use %s from environment.", w.secretEnvKey)))
		} else {
			lines = append(lines, hintStyle.Render("  Press Enter to skip — you can add it later."))
		}
		lines = append(lines, hintStyle.Render("  (providing a key lets us fetch your available models)"))

	case wizStepModel:
		lines = append(lines, stepStyle.Render("  📋 Step 2/6 — Select Model"))
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

	case wizStepChannel:
		lines = append(lines, stepStyle.Render("  📋 Step 3/6 — Connect a Channel (optional)"))
		lines = append(lines, menuStyle.Render("  Channels let your agent receive messages from external platforms."))
		lines = append(lines, "")
		lines = append(lines, menuNumStyle.Render("  1)")+menuStyle.Render(" Telegram  — easiest, just talk to @BotFather"))
		lines = append(lines, menuNumStyle.Render("  2)")+menuStyle.Render(" Slack"))
		lines = append(lines, menuNumStyle.Render("  3)")+menuStyle.Render(" Discord"))
		lines = append(lines, menuNumStyle.Render("  4)")+menuStyle.Render(" WhatsApp  — scan a QR code to link"))
		lines = append(lines, menuNumStyle.Render("  5)")+menuStyle.Render(" Skip — I'll add a channel later"))

	case wizStepChannelActionToken:
		lines = append(lines, stepStyle.Render("  📋 Finalize Channel Setup"))
		lines = append(lines, labelStyle.Render(fmt.Sprintf("  Paste your %s token:", w.channelType)))
		lines = append(lines, hintStyle.Render("  Press Enter to skip and configure it later."))

	case wizStepPolicy:
		lines = append(lines, stepStyle.Render("  📋 Step 4/6 — Default Policy"))
		lines = append(lines, menuStyle.Render("  A SympoziumPolicy controls what tools agents can use, sandboxing, etc."))
		lines = append(lines, labelStyle.Render("  Apply the default policy?"))

	case wizStepHeartbeat:
		lines = append(lines, stepStyle.Render("  📋 Step 5/6 — Heartbeat Schedule"))
		lines = append(lines, menuStyle.Render("  A heartbeat lets your agent wake up periodically to review memory"))
		lines = append(lines, menuStyle.Render("  and note anything that needs attention."))
		lines = append(lines, "")
		lines = append(lines, menuNumStyle.Render("  1)")+menuStyle.Render(" Every 30 minutes"))
		lines = append(lines, menuNumStyle.Render("  2)")+menuStyle.Render(" Every hour")+hintStyle.Render("  (recommended)"))
		lines = append(lines, menuNumStyle.Render("  3)")+menuStyle.Render(" Every 6 hours"))
		lines = append(lines, menuNumStyle.Render("  4)")+menuStyle.Render(" Once a day (9am)"))
		lines = append(lines, menuNumStyle.Render("  5)")+menuStyle.Render(" Disabled — no heartbeat"))

	case wizStepConfirm:
		lines = append(lines, stepStyle.Render("  📋 Step 6/6 — Confirm"))
		lines = append(lines, "")
		lines = append(lines, tuiSepStyle.Render("  "+strings.Repeat("━", 50)))
		lines = append(lines, labelStyle.Render("  Summary"))
		lines = append(lines, tuiSepStyle.Render("  "+strings.Repeat("━", 50)))
		lines = append(lines, hintStyle.Render("  Instance:  ")+valueStyle.Render(w.instanceName)+
			hintStyle.Render("  (namespace: ")+valueStyle.Render(m.namespace)+hintStyle.Render(")"))
		lines = append(lines, hintStyle.Render("  Provider:  ")+valueStyle.Render(w.providerName)+
			hintStyle.Render("  (model: ")+valueStyle.Render(w.modelName)+hintStyle.Render(")"))
		if w.baseURL != "" {
			lines = append(lines, hintStyle.Render("  Base URL:  ")+valueStyle.Render(w.baseURL))
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
	lines = append(lines, titleStyle.Render("  ║       Sympozium · Persona Pack Wizard       ║"))
	lines = append(lines, titleStyle.Render("  ╚═══════════════════════════════════════════╝"))
	lines = append(lines, "")

	// Recap completed values.
	if w.personaPackName != "" && w.step > wizStepPersonaPick {
		// Show pack info.
		lines = append(lines, hintStyle.Render("  Pack: ")+valueStyle.Render(w.personaPackName))
		for _, pp := range m.personaPacks {
			if pp.Name == w.personaPackName {
				lines = append(lines, hintStyle.Render("  Category: ")+valueStyle.Render(pp.Spec.Category)+
					hintStyle.Render("  Personas: ")+valueStyle.Render(fmt.Sprintf("%d", len(pp.Spec.Personas))))
				for _, p := range pp.Spec.Personas {
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

	// Current step.
	switch w.step {
	case wizStepPersonaPick:
		stepNum := 1
		lines = append(lines, stepStyle.Render(fmt.Sprintf("  Step %d: Select a PersonaPack", stepNum)))
		lines = append(lines, "")
		if len(m.personaPacks) == 0 {
			lines = append(lines, hintStyle.Render("  No PersonaPacks found in cluster."))
			lines = append(lines, hintStyle.Render("  Run 'sympozium install' to install built-in packs."))
		} else {
			for i, pp := range m.personaPacks {
				activated := ""
				if len(pp.Spec.AuthRefs) > 0 && pp.Status.Phase == "Ready" {
					activated = " ✓ activated"
				}
				lines = append(lines, menuNumStyle.Render(fmt.Sprintf("  [%d]", i+1))+
					menuStyle.Render(fmt.Sprintf(" %s", pp.Name))+
					hintStyle.Render(fmt.Sprintf(" — %s (%d personas)%s",
						pp.Spec.Category, len(pp.Spec.Personas), activated)))
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
		lines = append(lines, menuNumStyle.Render("  [5]")+menuStyle.Render(" Custom")+hintStyle.Render(" — Any OpenAI-compatible API"))
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

	case wizStepPersonaChannels:
		lines = append(lines, stepStyle.Render("  Step 5: Channel Bindings"))
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
			lines = append(lines, stepStyle.Render(fmt.Sprintf("  Step 6b: Finalize %s", strings.Title(ch.chType))))
			lines = append(lines, "")
			lines = append(lines, hintStyle.Render(fmt.Sprintf("  Paste %s or press Enter to skip.", ch.tokenKey)))
		}
		lines = append(lines, "")

	case wizStepPersonaConfirm:
		lines = append(lines, stepStyle.Render("  Step 6: Confirm"))
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  Summary:"))
		lines = append(lines, hintStyle.Render("  Pack:     ")+valueStyle.Render(w.personaPackName))
		lines = append(lines, hintStyle.Render("  Provider: ")+valueStyle.Render(w.providerName))
		lines = append(lines, hintStyle.Render("  Model:    ")+valueStyle.Render(w.modelName))
		var chNames []string
		for _, ch := range w.personaChannels {
			if ch.enabled {
				chNames = append(chNames, ch.chType)
			}
		}
		if len(chNames) > 0 {
			lines = append(lines, hintStyle.Render("  Channels: ")+valueStyle.Render(strings.Join(chNames, ", ")))
		} else {
			lines = append(lines, hintStyle.Render("  Channels: ")+valueStyle.Render("none"))
		}
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("  This will create an auth secret and activate the pack."))
		lines = append(lines, hintStyle.Render("  The controller will stamp out one instance per persona."))
		lines = append(lines, "")

	case wizStepPersonaApplying:
		lines = append(lines, labelStyle.Render("  Activating persona pack..."))

	case wizStepPersonaDone:
		lines = append(lines, "")
		lines = append(lines, labelStyle.Render("  ✅ Persona pack activated!"))
		lines = append(lines, "")
		if len(w.resultMsgs) > 0 {
			for _, msg := range w.resultMsgs {
				lines = append(lines, "  "+msg)
			}
		}
		lines = append(lines, "")
		lines = append(lines, hintStyle.Render("  Press Enter to switch to Instances view."))
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

// renderPersonaPackDetailPane renders the right-hand detail pane showing
// the contents of the selected PersonaPack during the persona wizard.
func (m tuiModel) renderPersonaPackDetailPane(w, h int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E94560"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#74C7EC")).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#A6E3A1"))
	dimStyle := tuiDimStyle
	skillStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FAB387"))

	var pack *sympoziumv1alpha1.PersonaPack
	for i := range m.personaPacks {
		if m.personaPacks[i].Name == m.wizard.personaPackName {
			pack = &m.personaPacks[i]
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
	lines = append(lines, labelStyle.Render(" Personas:  ")+valueStyle.Render(fmt.Sprintf("%d", len(pack.Spec.Personas))))
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
		installed := fmt.Sprintf("%d/%d", pack.Status.InstalledCount, len(pack.Spec.Personas))
		lines = append(lines, labelStyle.Render(" Status:    ")+phaseStyle.Render(phase)+
			dimStyle.Render("  (")+valueStyle.Render(installed)+dimStyle.Render(" installed)"))
	}

	lines = append(lines, "")
	lines = append(lines, dimStyle.Render(" "+strings.Repeat("─", w-2)))
	lines = append(lines, "")

	// Each persona
	for i, p := range pack.Spec.Personas {
		name := p.DisplayName
		if name == "" {
			name = p.Name
		}

		// Check if excluded
		excluded := false
		for _, ex := range pack.Spec.ExcludePersonas {
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
		if i < len(pack.Spec.Personas)-1 {
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

// tuiPersonaApply activates a PersonaPack by creating the auth secret,
// patching the pack with authRefs + channel config, and letting the
// controller reconciler stamp out instances.
func tuiPersonaApply(ns string, w *wizardState) (string, error) {
	ctx := context.Background()
	var msgs []string

	secretName := fmt.Sprintf("%s-%s-key", w.personaPackName, w.providerName)

	// 1. Create AI provider secret.
	if w.apiKey != "" {
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
		chSecretName := fmt.Sprintf("%s-%s-secret", w.personaPackName, ch.chType)
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

	// 3. Patch the PersonaPack with authRefs and channel config.
	var pack sympoziumv1alpha1.PersonaPack
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: w.personaPackName, Namespace: ns}, &pack); err != nil {
		return "", fmt.Errorf("get PersonaPack: %w", err)
	}

	// Enable the pack — this is the explicit activation step.
	pack.Spec.Enabled = true

	// On first activation, enable all personas by default.
	// On re-activation (updating auth/model), preserve the user's exclusion choices.
	firstActivation := len(pack.Spec.AuthRefs) == 0
	if firstActivation {
		pack.Spec.ExcludePersonas = nil
	}

	pack.Spec.AuthRefs = []sympoziumv1alpha1.SecretRef{
		{
			Provider: w.providerName,
			Secret:   secretName,
		},
	}

	// Update each persona with the chosen model and channel bindings.
	var enabledChannels []string
	channelConfigs := make(map[string]string)
	for _, ch := range w.personaChannels {
		if ch.enabled {
			enabledChannels = append(enabledChannels, ch.chType)
			if ch.token != "" {
				channelConfigs[ch.chType] = fmt.Sprintf("%s-%s-secret", w.personaPackName, ch.chType)
			}
		}
	}
	if len(channelConfigs) > 0 {
		pack.Spec.ChannelConfigs = channelConfigs
	}
	for i := range pack.Spec.Personas {
		pack.Spec.Personas[i].Model = w.modelName
		if len(enabledChannels) > 0 {
			pack.Spec.Personas[i].Channels = enabledChannels
		}
	}

	if err := k8sClient.Update(ctx, &pack); err != nil {
		return "", fmt.Errorf("update PersonaPack: %w", err)
	}
	msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Activated PersonaPack: %s (%d personas)", w.personaPackName, len(pack.Spec.Personas))))
	msgs = append(msgs, tuiDimStyle.Render("  Controller will create instances shortly..."))

	return strings.Join(msgs, "\n"), nil
}

// tuiOnboardApply creates all K8s resources for the onboard wizard.
// Uses the K8s client directly — no kubectl exec — so it's TUI-safe.
func tuiOnboardApply(ns string, w *wizardState) (string, error) {
	ctx := context.Background()
	var msgs []string

	providerSecretName := fmt.Sprintf("%s-%s-key", w.instanceName, w.providerName)
	channelSecretName := fmt.Sprintf("%s-%s-secret", w.instanceName, w.channelType)
	policyName := "default-policy"

	// 1. Create AI provider secret.
	if w.apiKey != "" {
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
					DefaultImage: "ghcr.io/alexsjones/sympozium/sandbox:latest",
					MaxCPU:       "4",
					MaxMemory:    "8Gi",
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

	// 4. Create SympoziumInstance.
	inst := &sympoziumv1alpha1.SympoziumInstance{
		ObjectMeta: metav1.ObjectMeta{
			Name:      w.instanceName,
			Namespace: ns,
		},
		Spec: sympoziumv1alpha1.SympoziumInstanceSpec{
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

	// Default skills: k8s-ops.
	inst.Spec.Skills = []sympoziumv1alpha1.SkillRef{
		{SkillPackRef: "k8s-ops"},
	}

	// Memory is on by default.
	inst.Spec.Memory = &sympoziumv1alpha1.MemorySpec{
		Enabled:   true,
		MaxSizeKB: 256,
	}

	// Try create; if it exists, update.
	if err := k8sClient.Create(ctx, inst); err != nil {
		var existing sympoziumv1alpha1.SympoziumInstance
		if getErr := k8sClient.Get(ctx, types.NamespacedName{Name: w.instanceName, Namespace: ns}, &existing); getErr == nil {
			existing.Spec = inst.Spec
			if err2 := k8sClient.Update(ctx, &existing); err2 != nil {
				return "", fmt.Errorf("update instance: %w", err2)
			}
			msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Updated SympoziumInstance: %s", w.instanceName)))
		} else {
			return "", fmt.Errorf("create instance: %w", err)
		}
	} else {
		msgs = append(msgs, tuiSuccessStyle.Render(fmt.Sprintf("✓ Created SympoziumInstance: %s", w.instanceName)))
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
				InstanceRef:       w.instanceName,
				Schedule:          heartbeatCron,
				Task:              "Review your memory. Summarise what you know so far and note anything that needs attention.",
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

	sepStr := lipgloss.NewStyle().Foreground(lipgloss.Color("#313244")).Render("│")

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
				decoded, decErr := exec.Command("sh", "-c",
					fmt.Sprintf("echo %s | base64 -d", string(tokenBytes))).CombinedOutput()
				if decErr == nil {
					token = strings.TrimSpace(string(decoded))
				}
			}

			listenAddr := fmt.Sprintf("127.0.0.1:%s", localPort)

			fmt.Printf("\n  Starting port-forward to svc/sympozium-apiserver in namespace %s...\n", ns)
			fmt.Printf("  ➜  Web UI:  http://%s\n", listenAddr)
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
					url := fmt.Sprintf("http://%s", listenAddr)
					// Try common openers.
					for _, opener := range []string{"xdg-open", "open", "sensible-browser"} {
						if p, err := exec.LookPath(opener); err == nil {
							_ = exec.Command(p, url).Start()
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
				pf := exec.CommandContext(ctx, "kubectl", "port-forward",
					"-n", ns,
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

	cmd.Flags().StringVar(&localPort, "port", "8080", "Local port to forward to")
	cmd.Flags().BoolVar(&openBrowser, "open", false, "Open a browser automatically")
	cmd.Flags().StringVar(&svcNamespace, "service-namespace", "sympozium-system", "Namespace of the sympozium-apiserver service")

	return cmd
}
