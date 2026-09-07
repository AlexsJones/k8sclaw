// Workspace subcommand: inspect and manage WorkspaceSessions and their
// backing PVCs. Pairs with the per-session PVC feature shipped in PR4a.
package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	k8serr "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
)

// newWorkspaceCmd returns the `sympozium workspace` command tree.
func newWorkspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"workspaces", "ws"},
		Short:   "Manage per-session WorkspaceSessions and their PVCs",
		Long: `Inspect and manage WorkspaceSessions.

A WorkspaceSession is the persistent /workspace for one (Agent, sessionKey)
pair. It owns a backing PVC and survives across AgentRuns of the same
session — used by harnesses (codex, claude-code) and any agent that needs
continuity across turns.`,
	}

	cmd.AddCommand(
		newWorkspaceListCmd(),
		newWorkspaceShowCmd(),
		newWorkspaceDeleteCmd(),
		newWorkspaceExecCmd(),
	)
	return cmd
}

func newWorkspaceListCmd() *cobra.Command {
	var (
		agentFilter    string
		ensembleFilter string
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List WorkspaceSessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			var list sympoziumv1alpha1.WorkspaceSessionList
			if err := k8sClient.List(ctx, &list, client.InNamespace(namespace)); err != nil {
				return err
			}

			// WorkspaceSession objects aren't tagged with the ensemble
			// label directly — resolve it from the parent Agent so
			// --ensemble works.
			agentEnsembles := map[string]string{}
			if ensembleFilter != "" {
				var agents sympoziumv1alpha1.AgentList
				if err := k8sClient.List(ctx, &agents, client.InNamespace(namespace)); err != nil {
					return err
				}
				for _, a := range agents.Items {
					agentEnsembles[a.Name] = a.Labels["sympozium.ai/ensemble"]
				}
			}

			rows := make([]sympoziumv1alpha1.WorkspaceSession, 0, len(list.Items))
			for _, ws := range list.Items {
				if agentFilter != "" && ws.Spec.AgentRef != agentFilter {
					continue
				}
				if ensembleFilter != "" && agentEnsembles[ws.Spec.AgentRef] != ensembleFilter {
					continue
				}
				rows = append(rows, ws)
			}
			sort.Slice(rows, func(i, j int) bool {
				return rows[i].Name < rows[j].Name
			})

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tAGENT\tPHASE\tPVC\tSIZE\tLAST-TOUCHED\tAGE")
			for _, ws := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					ws.Name,
					ws.Spec.AgentRef,
					string(ws.Status.Phase),
					ws.Status.PVCName,
					ws.Spec.Size,
					formatTouched(ws.Status.LastTouchedAt),
					formatAge(ws.CreationTimestamp.Time),
				)
			}
			return w.Flush()
		},
	}
	cmd.Flags().StringVar(&agentFilter, "agent", "", "Only show sessions owned by this Agent")
	cmd.Flags().StringVar(&ensembleFilter, "ensemble", "", "Only show sessions whose Agent belongs to this Ensemble")
	return cmd
}

func newWorkspaceShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show a WorkspaceSession with its PVC and last AgentRun",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			name := args[0]

			ws := &sympoziumv1alpha1.WorkspaceSession{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, ws); err != nil {
				return fmt.Errorf("get workspacesession: %w", err)
			}

			fmt.Println("WorkspaceSession")
			fmt.Printf("  Name:             %s\n", ws.Name)
			fmt.Printf("  Namespace:        %s\n", ws.Namespace)
			fmt.Printf("  Agent:            %s\n", ws.Spec.AgentRef)
			fmt.Printf("  Phase:            %s\n", ws.Status.Phase)
			fmt.Printf("  Size:             %s\n", ws.Spec.Size)
			if ws.Spec.StorageClassName != "" {
				fmt.Printf("  StorageClass:     %s\n", ws.Spec.StorageClassName)
			}
			if ws.Spec.IdleTTL != nil {
				fmt.Printf("  IdleTTL:          %s\n", ws.Spec.IdleTTL.Duration)
			} else {
				fmt.Printf("  IdleTTL:          (controller default)\n")
			}
			fmt.Printf("  LastTouched:      %s\n", formatTouched(ws.Status.LastTouchedAt))
			fmt.Printf("  LastRun:          %s\n", orDash(ws.Status.LastRunName))
			fmt.Printf("  Age:              %s\n", formatAge(ws.CreationTimestamp.Time))
			if ws.Status.RecreatedInfo != nil {
				fmt.Printf("  Recreated:        %s at %s\n",
					ws.Status.RecreatedInfo.Reason,
					ws.Status.RecreatedInfo.At.Format(time.RFC3339))
				if ws.Status.RecreatedInfo.Message != "" {
					fmt.Printf("                    %s\n", ws.Status.RecreatedInfo.Message)
				}
			}
			if len(ws.Status.Conditions) > 0 {
				fmt.Println("  Conditions:")
				for _, c := range ws.Status.Conditions {
					msg := c.Message
					if msg == "" {
						msg = c.Reason
					}
					fmt.Printf("    %-16s %-8s %s\n", c.Type, c.Status, msg)
				}
			}

			// PVC details.
			if ws.Status.PVCName != "" {
				pvc := &corev1.PersistentVolumeClaim{}
				err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ws.Status.PVCName}, pvc)
				switch {
				case k8serr.IsNotFound(err):
					fmt.Println("\nPVC")
					fmt.Printf("  Name:             %s (NOT FOUND — sweeper or operator may have deleted it)\n", ws.Status.PVCName)
				case err != nil:
					return fmt.Errorf("get pvc: %w", err)
				default:
					req := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
					fmt.Println("\nPVC")
					fmt.Printf("  Name:             %s\n", pvc.Name)
					fmt.Printf("  Phase:            %s\n", pvc.Status.Phase)
					fmt.Printf("  Requested:        %s\n", req.String())
					if cap, ok := pvc.Status.Capacity[corev1.ResourceStorage]; ok {
						fmt.Printf("  Capacity:         %s\n", cap.String())
					}
					if pvc.Spec.StorageClassName != nil && *pvc.Spec.StorageClassName != "" {
						fmt.Printf("  StorageClass:     %s\n", *pvc.Spec.StorageClassName)
					}
					if pvc.Spec.VolumeName != "" {
						fmt.Printf("  PV:               %s\n", pvc.Spec.VolumeName)
					}
				}
			}

			// Last AgentRun summary.
			if ws.Status.LastRunName != "" {
				run := &sympoziumv1alpha1.AgentRun{}
				err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ws.Status.LastRunName}, run)
				switch {
				case k8serr.IsNotFound(err):
					fmt.Println("\nLast AgentRun")
					fmt.Printf("  Name:             %s (deleted)\n", ws.Status.LastRunName)
				case err != nil:
					return fmt.Errorf("get last AgentRun: %w", err)
				default:
					fmt.Println("\nLast AgentRun")
					fmt.Printf("  Name:             %s\n", run.Name)
					fmt.Printf("  Phase:            %s\n", run.Status.Phase)
					fmt.Printf("  StartedAt:        %s\n", formatTime(run.Status.StartedAt))
					fmt.Printf("  CompletedAt:      %s\n", formatTime(run.Status.CompletedAt))
				}
			}
			return nil
		},
	}
}

func newWorkspaceDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a WorkspaceSession (cascades to its PVC)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			name := args[0]

			ws := &sympoziumv1alpha1.WorkspaceSession{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, ws); err != nil {
				return err
			}

			// Refuse to delete while a live AgentRun still references this
			// session — the PVC delete would be blocked by the kubelet
			// anyway, and we'd rather give a clear error than leave a
			// zombie WorkspaceSession behind.
			if !force {
				live, err := liveRunsForWorkspace(ctx, ws)
				if err != nil {
					return fmt.Errorf("checking for live AgentRuns: %w", err)
				}
				if len(live) > 0 {
					names := make([]string, len(live))
					for i, r := range live {
						names[i] = fmt.Sprintf("%s (%s)", r.Name, r.Status.Phase)
					}
					return fmt.Errorf("refusing to delete: %d non-terminal AgentRun(s) still reference this session: %s\nuse --force to delete anyway", len(live), strings.Join(names, ", "))
				}
			}

			if err := k8sClient.Delete(ctx, ws); err != nil {
				return err
			}
			fmt.Printf("workspacesession/%s deleted (PVC %s will cascade)\n", name, ws.Status.PVCName)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Delete even when live AgentRuns still reference this session")
	return cmd
}

func newWorkspaceExecCmd() *cobra.Command {
	var (
		image    string
		ttl      time.Duration
		waitTime time.Duration
	)
	cmd := &cobra.Command{
		Use:   "exec <name>",
		Short: "Spawn a transient debug pod that mounts the session's PVC at /workspace",
		Long: `Spawn a transient debug pod that mounts the session's PVC at /workspace.

The pod is created with restartPolicy=Never and activeDeadlineSeconds set
from --ttl so the kubelet reaps it even if you forget to delete it.

Sympozium does not stream the shell itself — it prints the kubectl
command to attach. This keeps the CLI dependency-free and works against
any cluster where you have RBAC to exec into pods.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			name := args[0]

			ws := &sympoziumv1alpha1.WorkspaceSession{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, ws); err != nil {
				return err
			}
			if ws.Status.PVCName == "" {
				return fmt.Errorf("workspacesession %s has no PVC yet (phase: %s); wait for it to become Bound", name, ws.Status.Phase)
			}

			// Bail out cleanly if a live AgentRun is currently attached —
			// RWO PVCs cannot multi-attach so the debug pod would fail
			// to schedule with a cryptic kubelet error.
			live, err := liveRunsForWorkspace(ctx, ws)
			if err != nil {
				return fmt.Errorf("checking for live AgentRuns: %w", err)
			}
			if len(live) > 0 {
				return fmt.Errorf("PVC %s is mounted by AgentRun %s (phase: %s); wait for it to terminate before debugging",
					ws.Status.PVCName, live[0].Name, live[0].Status.Phase)
			}

			deadline := int64(ttl.Seconds())
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: fmt.Sprintf("ws-debug-%s-", ws.Name),
					Namespace:    namespace,
					Labels: map[string]string{
						"sympozium.ai/component":        "workspace-debug",
						"sympozium.ai/workspacesession": ws.Name,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:         corev1.RestartPolicyNever,
					ActiveDeadlineSeconds: &deadline,
					Containers: []corev1.Container{{
						Name:       "debug",
						Image:      image,
						Command:    []string{"/bin/sh", "-c", fmt.Sprintf("sleep %d", deadline)},
						WorkingDir: "/workspace",
						VolumeMounts: []corev1.VolumeMount{
							{Name: "workspace", MountPath: "/workspace"},
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "workspace",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: ws.Status.PVCName,
							},
						},
					}},
				},
			}
			if err := k8sClient.Create(ctx, pod); err != nil {
				return fmt.Errorf("creating debug pod: %w", err)
			}

			fmt.Printf("Debug pod created: %s/%s\n", pod.Namespace, pod.Name)
			fmt.Printf("  PVC mounted:   %s at /workspace\n", ws.Status.PVCName)
			fmt.Printf("  Auto-cleanup:  pod is killed after %s (kubelet ActiveDeadlineSeconds)\n", ttl)
			fmt.Println()
			fmt.Println("Waiting for pod to be Ready…")
			if err := waitForPodReady(ctx, pod.Namespace, pod.Name, waitTime); err != nil {
				fmt.Fprintf(os.Stderr, "warning: %v\n", err)
			}
			fmt.Println()
			fmt.Println("Attach with:")
			fmt.Printf("  kubectl exec -it -n %s %s -- /bin/sh\n", pod.Namespace, pod.Name)
			fmt.Println()
			fmt.Println("Clean up early with:")
			fmt.Printf("  kubectl delete pod -n %s %s\n", pod.Namespace, pod.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&image, "image", "alpine:3.20", "Image to use for the debug pod")
	cmd.Flags().DurationVar(&ttl, "ttl", 1*time.Hour, "How long the debug pod lives before the kubelet kills it")
	cmd.Flags().DurationVar(&waitTime, "wait", 30*time.Second, "How long to wait for the pod to become Ready before printing the attach command")
	return cmd
}

// --- helpers ----------------------------------------------------------------

// liveRunsForWorkspace returns AgentRuns in the WorkspaceSession's
// namespace that share its (agent, session-key-hash) labels and are not
// in a terminal phase. The session-key-hash label is the same value the
// controller stamps on AgentRuns, so we can find live attachers without
// re-hashing the raw session key here.
func liveRunsForWorkspace(ctx context.Context, ws *sympoziumv1alpha1.WorkspaceSession) ([]sympoziumv1alpha1.AgentRun, error) {
	hash := ws.Labels["sympozium.ai/session-key-hash"]
	if hash == "" {
		// Older sessions or partial labels: fall back to AgentRef-only
		// list and filter by SessionKey equality. Acceptable cost on a
		// CLI command.
		var runs sympoziumv1alpha1.AgentRunList
		if err := k8sClient.List(ctx, &runs,
			client.InNamespace(ws.Namespace),
			client.MatchingLabels{"sympozium.ai/instance": ws.Spec.AgentRef},
		); err != nil {
			return nil, err
		}
		out := make([]sympoziumv1alpha1.AgentRun, 0)
		for _, r := range runs.Items {
			if r.Spec.SessionKey == ws.Spec.SessionKey && !isTerminalRunPhase(r.Status.Phase) {
				out = append(out, r)
			}
		}
		return out, nil
	}

	var runs sympoziumv1alpha1.AgentRunList
	if err := k8sClient.List(ctx, &runs,
		client.InNamespace(ws.Namespace),
		client.MatchingLabels{
			"sympozium.ai/instance":         ws.Spec.AgentRef,
			"sympozium.ai/session-key-hash": hash,
		},
	); err != nil {
		return nil, err
	}
	out := make([]sympoziumv1alpha1.AgentRun, 0, len(runs.Items))
	for _, r := range runs.Items {
		if !isTerminalRunPhase(r.Status.Phase) {
			out = append(out, r)
		}
	}
	return out, nil
}

func isTerminalRunPhase(p sympoziumv1alpha1.AgentRunPhase) bool {
	return p == sympoziumv1alpha1.AgentRunPhaseSucceeded ||
		p == sympoziumv1alpha1.AgentRunPhaseFailed
}

// waitForPodReady polls until the pod reports Ready=True or the deadline
// elapses. Returns nil on success, error on timeout / not-found / failure.
func waitForPodReady(ctx context.Context, ns, name string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pod := &corev1.Pod{}
		if err := k8sClient.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, pod); err != nil {
			return err
		}
		switch pod.Status.Phase {
		case corev1.PodRunning:
			for _, c := range pod.Status.Conditions {
				if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
					return nil
				}
			}
		case corev1.PodFailed, corev1.PodSucceeded:
			return fmt.Errorf("pod %s entered terminal phase %s before Ready", name, pod.Status.Phase)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("pod %s not Ready after %s", name, timeout)
}

func formatTouched(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	d := time.Since(t.Time).Round(time.Second)
	return fmt.Sprintf("%s ago", d)
}

func formatAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return time.Since(t).Round(time.Second).String()
}

func formatTime(t *metav1.Time) string {
	if t == nil || t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339)
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
