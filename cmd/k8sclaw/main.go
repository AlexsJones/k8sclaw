// Package main provides the k8sclaw CLI tool for managing K8sClaw resources.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	k8sclawv1alpha1 "github.com/k8sclaw/k8sclaw/api/v1alpha1"
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
		Use:   "k8sclaw",
		Short: "K8sClaw - Kubernetes-native AI agent management",
		Long: `K8sClaw CLI for managing ClawInstances, AgentRuns, ClawPolicies,
SkillPacks, and feature gates in your Kubernetes cluster.`,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			// Skip K8s client init for commands that don't need it.
			switch cmd.Name() {
			case "version", "install", "uninstall", "onboard", "tui":
				return nil
			}
			return initClient()
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
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func initClient() error {
	scheme := runtime.NewScheme()
	if err := k8sclawv1alpha1.AddToScheme(scheme); err != nil {
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
		Short:   "Manage ClawInstances",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List ClawInstances",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var list k8sclawv1alpha1.ClawInstanceList
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
			Short: "Get a ClawInstance",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var inst k8sclawv1alpha1.ClawInstance
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
			Short: "Delete a ClawInstance",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				inst := &k8sclawv1alpha1.ClawInstance{
					ObjectMeta: metav1.ObjectMeta{Name: args[0], Namespace: namespace},
				}
				if err := k8sClient.Delete(ctx, inst); err != nil {
					return err
				}
				fmt.Printf("clawinstance/%s deleted\n", args[0])
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
				var list k8sclawv1alpha1.AgentRunList
				if err := k8sClient.List(ctx, &list, client.InNamespace(namespace)); err != nil {
					return err
				}
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "NAME\tINSTANCE\tPHASE\tPOD\tAGE")
				for _, run := range list.Items {
					age := time.Since(run.CreationTimestamp.Time).Round(time.Second)
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
						run.Name, run.Spec.InstanceRef,
						run.Status.Phase, run.Status.PodName, age)
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
				var run k8sclawv1alpha1.AgentRun
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
				var run k8sclawv1alpha1.AgentRun
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
		Short:   "Manage ClawPolicies",
	}

	cmd.AddCommand(
		&cobra.Command{
			Use:   "list",
			Short: "List ClawPolicies",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var list k8sclawv1alpha1.ClawPolicyList
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
			Short: "Get a ClawPolicy",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx := context.Background()
				var pol k8sclawv1alpha1.ClawPolicy
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
				var list k8sclawv1alpha1.SkillPackList
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
	enableCmd.Flags().String("policy", "", "Target ClawPolicy")

	disableCmd := &cobra.Command{
		Use:   "disable [feature]",
		Short: "Disable a feature gate",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return toggleFeature(args[0], false, cmd)
		},
	}
	disableCmd.Flags().String("policy", "", "Target ClawPolicy")

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List feature gates on a policy",
		RunE: func(cmd *cobra.Command, args []string) error {
			policyName, _ := cmd.Flags().GetString("policy")
			if policyName == "" {
				return fmt.Errorf("--policy is required")
			}
			ctx := context.Background()
			var pol k8sclawv1alpha1.ClawPolicy
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
	listCmd.Flags().String("policy", "", "Target ClawPolicy")

	cmd.AddCommand(enableCmd, disableCmd, listCmd)
	return cmd
}

func toggleFeature(feature string, enabled bool, cmd *cobra.Command) error {
	policyName, _ := cmd.Flags().GetString("policy")
	if policyName == "" {
		return fmt.Errorf("--policy is required")
	}

	ctx := context.Background()
	var pol k8sclawv1alpha1.ClawPolicy
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
			fmt.Printf("k8sclaw %s\n", version)
		},
	}
}

const (
	ghRepo         = "AlexsJones/k8sclaw"
	manifestAsset  = "k8sclaw-manifests.tar.gz"
)

// ── Onboard ──────────────────────────────────────────────────────────────────

func newOnboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "onboard",
		Short: "Interactive setup wizard for K8sClaw",
		Long: `Walks you through creating your first ClawInstance, connecting a
channel (Telegram, Slack, Discord, or WhatsApp), setting up your AI provider
credentials, and optionally applying a default ClawPolicy.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOnboard()
		},
	}
}

func runOnboard() error {
	reader := bufio.NewReader(os.Stdin)

	printBanner()

	// ── Step 1: Check K8sClaw is installed ───────────────────────────────
	fmt.Println("\n📋 Step 1/5 — Checking cluster...")
	if err := initClient(); err != nil {
		fmt.Println("\n  ❌ Cannot connect to your cluster.")
		fmt.Println("  Make sure kubectl is configured and run: k8sclaw install")
		return err
	}

	// Quick health check: can we list CRDs?
	ctx := context.Background()
	var instances k8sclawv1alpha1.ClawInstanceList
	if err := k8sClient.List(ctx, &instances, client.InNamespace(namespace)); err != nil {
		fmt.Println("\n  ❌ K8sClaw CRDs not found. Run 'k8sclaw install' first.")
		return fmt.Errorf("CRDs not installed: %w", err)
	}
	fmt.Println("  ✅ K8sClaw is installed and CRDs are available.")

	// ── Step 2: Instance name ────────────────────────────────────────────
	fmt.Println("\n📋 Step 2/5 — Create your ClawInstance")
	fmt.Println("  An instance represents you (or a tenant) in the system.")
	instanceName := prompt(reader, "  Instance name", "my-agent")

	// ── Step 3: AI provider ──────────────────────────────────────────────
	fmt.Println("\n📋 Step 3/5 — AI Provider")
	fmt.Println("  Which model provider do you want to use?")
	fmt.Println("    1) OpenAI")
	fmt.Println("    2) Anthropic")
	fmt.Println("    3) GitHub Copilot  (uses your Copilot subscription)")
	fmt.Println("    4) Azure OpenAI")
	fmt.Println("    5) Ollama          (local, no API key needed)")
	fmt.Println("    6) Other / OpenAI-compatible")
	providerChoice := prompt(reader, "  Choice [1-6]", "1")

	var providerName, secretEnvKey, modelName, baseURL string
	switch providerChoice {
	case "2":
		providerName = "anthropic"
		secretEnvKey = "ANTHROPIC_API_KEY"
		modelName = prompt(reader, "  Model name", "claude-sonnet-4-20250514")
	case "3":
		providerName = "github-copilot"
		secretEnvKey = "GITHUB_TOKEN"
		baseURL = "https://api.githubcopilot.com"
		modelName = prompt(reader, "  Model name", "gpt-4o")
		fmt.Println("\n  💡 Use a GitHub PAT with the 'copilot' scope.")
		fmt.Println("  Create one at: https://github.com/settings/tokens")
	case "4":
		providerName = "azure-openai"
		secretEnvKey = "AZURE_OPENAI_API_KEY"
		baseURL = prompt(reader, "  Azure OpenAI endpoint URL", "")
		modelName = prompt(reader, "  Deployment name", "gpt-4o")
	case "5":
		providerName = "ollama"
		secretEnvKey = ""
		baseURL = prompt(reader, "  Ollama URL", "http://ollama.default.svc:11434/v1")
		modelName = prompt(reader, "  Model name", "llama3")
		fmt.Println("  💡 No API key needed for Ollama.")
	case "6":
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
			fmt.Println("  ⚠  No API key provided — you can add it later:")
			fmt.Printf("  kubectl create secret generic %s-%s-key --from-literal=%s=<key>\n",
				instanceName, providerName, secretEnvKey)
		}
	}

	providerSecretName := fmt.Sprintf("%s-%s-key", instanceName, providerName)

	// ── Step 4: Channel ──────────────────────────────────────────────────
	fmt.Println("\n📋 Step 4/5 — Connect a Channel (optional)")
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
		channelTokenKey = "WHATSAPP_ACCESS_TOKEN"
		fmt.Println("\n  💡 Set up the WhatsApp Cloud API at https://developers.facebook.com")
		channelToken = promptSecret(reader, "  Access Token")
	default:
		channelType = ""
	}

	channelSecretName := fmt.Sprintf("%s-%s-secret", instanceName, channelType)

	// ── Step 5: Apply default policy? ────────────────────────────────────
	fmt.Println("\n📋 Step 5/5 — Default Policy")
	fmt.Println("  A ClawPolicy controls what tools agents can use, sandboxing, etc.")
	applyPolicy := promptYN(reader, "  Apply the default policy?", true)

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
		fmt.Println("  Applying default ClawPolicy...")
		policyYAML := buildDefaultPolicyYAML(policyName, namespace)
		if err := kubectlApplyStdin(policyYAML); err != nil {
			return fmt.Errorf("apply policy: %w", err)
		}
	}

	// 4. Create ClawInstance.
	fmt.Printf("  Creating ClawInstance %s...\n", instanceName)
	instanceYAML := buildClawInstanceYAML(instanceName, namespace, modelName, baseURL,
		providerName, providerSecretName, channelType, channelSecretName,
		policyName, applyPolicy)
	if err := kubectlApplyStdin(instanceYAML); err != nil {
		return fmt.Errorf("apply instance: %w", err)
	}

	// ── Done ─────────────────────────────────────────────────────────────
	fmt.Println("\n  ✅ Onboarding complete!")
	fmt.Println()
	fmt.Println("  Next steps:")
	fmt.Println("  ─────────────────────────────────────────────────")
	fmt.Printf("  • Check your instance:   k8sclaw instances get %s\n", instanceName)
	if channelType == "telegram" {
		fmt.Println("  • Send a message to your Telegram bot — it's live!")
	}
	fmt.Printf("  • Run an agent:          kubectl apply -f config/samples/agentrun_sample.yaml\n")
	fmt.Printf("  • View runs:             k8sclaw runs list\n")
	fmt.Printf("  • Feature gates:         k8sclaw features list --policy %s\n", policyName)
	fmt.Println()
	return nil
}

func printBanner() {
	fmt.Println()
	fmt.Println("  ╔═══════════════════════════════════════════╗")
	fmt.Println("  ║         K8sClaw · Onboarding Wizard       ║")
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
	return fmt.Sprintf(`apiVersion: k8sclaw.io/v1alpha1
kind: ClawPolicy
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
    defaultImage: ghcr.io/alexsjones/k8sclaw/sandbox:latest
    maxCPU: "4"
    maxMemory: 8Gi
  featureGates:
    browser-automation: false
    code-execution: true
    file-access: true
`, name, ns)
}

func buildClawInstanceYAML(name, ns, model, baseURL, provider, providerSecret,
	channelType, channelSecret, policyName string, hasPolicy bool) string {

	var channelsBlock string
	if channelType != "" {
		channelsBlock = fmt.Sprintf(`  channels:
    - type: %s
      configRef:
        secret: %s
`, channelType, channelSecret)
	}

	var policyBlock string
	if hasPolicy {
		policyBlock = fmt.Sprintf("  policyRef: %s\n", policyName)
	}

	var baseURLLine string
	if baseURL != "" {
		baseURLLine = fmt.Sprintf("      baseURL: %s\n", baseURL)
	}

	return fmt.Sprintf(`apiVersion: k8sclaw.io/v1alpha1
kind: ClawInstance
metadata:
  name: %s
  namespace: %s
spec:
%s  agents:
    default:
      model: %s
%s  authRefs:
    - provider: %s
      secret: %s
%s`, name, ns, channelsBlock, model, baseURLLine, provider, providerSecret, policyBlock)
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
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install K8sClaw into the current Kubernetes cluster",
		Long: `Downloads the K8sClaw release manifests from GitHub and applies
them to your current Kubernetes cluster using kubectl.

Installs CRDs, the controller manager, API server, admission webhook,
RBAC rules, and network policies.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(manifestVersion)
		},
	}
	cmd.Flags().StringVar(&manifestVersion, "version", "", "Release version to install (default: latest)")
	return cmd
}

func newUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Remove K8sClaw from the current Kubernetes cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUninstall()
		},
	}
}

func runInstall(ver string) error {
	if ver == "" {
		if version != "dev" {
			ver = version
		} else {
			v, err := resolveLatestTag()
			if err != nil {
				return err
			}
			ver = v
		}
	}

	fmt.Printf("  Installing K8sClaw %s...\n", ver)

	// Download manifest bundle.
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", ghRepo, ver, manifestAsset)
	tmpDir, err := os.MkdirTemp("", "k8sclaw-install-*")
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

	// Apply CRDs first.
	fmt.Println("  Applying CRDs...")
	if err := kubectl("apply", "-f", filepath.Join(tmpDir, "config/crd/bases/")); err != nil {
		return err
	}

	// Create namespace before RBAC (ServiceAccounts reference it).
	// Ignore AlreadyExists error on re-installs.
	fmt.Println("  Creating namespace...")
	_ = kubectl("create", "namespace", "k8sclaw-system")

	// Deploy NATS event bus.
	fmt.Println("  Deploying NATS event bus...")
	if err := kubectl("apply", "-f", resolveConfigPath(tmpDir, "config/nats/")); err != nil {
		return err
	}

	// Install cert-manager if not present, then apply webhook certificate.
	fmt.Println("  Checking cert-manager...")
	if err := kubectl("get", "namespace", "cert-manager"); err != nil {
		fmt.Println("  Installing cert-manager...")
		if err := kubectl("apply", "-f",
			"https://github.com/cert-manager/cert-manager/releases/download/v1.17.1/cert-manager.yaml"); err != nil {
			return fmt.Errorf("install cert-manager: %w", err)
		}
		fmt.Println("  Waiting for cert-manager to be ready...")
		_ = kubectl("wait", "--for=condition=Available", "deployment/cert-manager-webhook",
			"-n", "cert-manager", "--timeout=90s")
	}

	fmt.Println("  Creating webhook certificate...")
	if err := kubectl("apply", "-f", resolveConfigPath(tmpDir, "config/cert/")); err != nil {
		return err
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

	// Apply webhook.
	fmt.Println("  Deploying webhook...")
	if err := kubectl("apply", "-f", filepath.Join(tmpDir, "config/webhook/")); err != nil {
		return err
	}

	// Apply network policies.
	fmt.Println("  Applying network policies...")
	if err := kubectl("apply", "-f", filepath.Join(tmpDir, "config/network/")); err != nil {
		return err
	}

	fmt.Println("\n  K8sClaw installed successfully!")
	fmt.Println("  Run: kubectl get pods -n k8sclaw-system")
	return nil
}

func runUninstall() error {
	fmt.Println("  Removing K8sClaw...")

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

	// CRDs last.
	crdBase := "https://raw.githubusercontent.com/" + ghRepo + "/main/config/crd/bases/"
	crds := []string{
		"k8sclaw.io_clawinstances.yaml",
		"k8sclaw.io_agentruns.yaml",
		"k8sclaw.io_clawpolicies.yaml",
		"k8sclaw.io_skillpacks.yaml",
	}
	for _, c := range crds {
		_ = kubectl("delete", "--ignore-not-found", "-f", crdBase+c)
	}

	fmt.Println("  K8sClaw uninstalled.")
	return nil
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
