package controller

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	sympoziumv1alpha1 "github.com/sympozium-ai/sympozium/api/v1alpha1"
	"github.com/sympozium-ai/sympozium/internal/skilltools"
)

// The skill tool server: how a run whose agent container was replaced still
// reaches its SkillPack tools, without being handed the /ipc surface that
// agent-runner is trusted with.
//
// agent-runner enforces spec.toolPolicy itself and is the only writer of
// /ipc/tools/exec-request-*.json, so policy and writer are one trusted process
// and the skill sidecars can execute what arrives without checking authority.
// A harness is not that process. Its /ipc mount is narrowed to input and output
// (taskmodes/harness.go), so it cannot write exec requests at all.
//
// This sidecar closes the loop. It holds the policy, serves only the permitted
// tools over MCP on loopback, and is the one thing in the pod that turns a
// harness request into an exec request. spec.toolPolicy becomes enforced for
// these tools rather than merely declared — the adapter's toolFilter claim
// covers the harness's own tools, and this covers the SkillPack ones whatever
// the adapter does.
const (
	// skillToolsContainerName is the sidecar's container name.
	skillToolsContainerName = "skill-tools"

	// skillToolsPort is the loopback port it listens on. Pod containers share
	// a network namespace, so the harness reaches it at 127.0.0.1 and nothing
	// outside the pod can.
	skillToolsPort = 8771
)

// skillToolsURL is the address the registry entry points at.
func skillToolsURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/", skillToolsPort)
}

// RunNeedsSkillToolServer reports whether this run should carry the skill tool
// server: its task mode replaces the agent container, and it has SkillPack
// tools to serve.
//
// Answered against the filtered sidecar list, the one the pod actually runs —
// advertising a tool whose RequiresServer sidecar is absent would produce a
// registry entry for something that cannot answer.
func RunNeedsSkillToolServer(agentRun *sympoziumv1alpha1.AgentRun, resolved []resolvedSidecar) bool {
	if agentRun == nil || !taskModeReplacesAgentContainer(agentRun.Spec.Task) {
		return false
	}
	return sidecarsHaveTools(taskModeSidecars(resolved))
}

// taskModeSidecars drops server-only sidecars, which are not meaningful in a
// task-mode pod. Shared so the "does this run need the skill tool server"
// question and the manifest the server reads are answered from the same list.
func taskModeSidecars(resolved []resolvedSidecar) []resolvedSidecar {
	out := make([]resolvedSidecar, 0, len(resolved))
	for _, sc := range resolved {
		if sc.sidecar.RequiresServer {
			continue
		}
		out = append(out, sc)
	}
	return out
}

// validateMCPServerNames rejects an operator-configured MCP server that
// intrudes on Sympozium's reserved namespace.
//
// Sympozium adds its own server to the registry a harness reads, and the
// harness namespaces tools by server name. A second entry called
// sympozium-skills would either collide outright or shadow the internal one —
// and shadowing it means the agent's SkillPack tool calls go to a server the
// operator supplied, with no policy check between. That is a spoofing vector,
// not a naming inconvenience.
//
// Refusing here fails the run with the reason on status.error, because
// buildContainers is a spec-rejection path. The webhook makes the same check
// earlier and with a better error; this one covers a cluster without it.
func validateMCPServerNames(mcpServers []sympoziumv1alpha1.MCPServerRef) error {
	for _, srv := range mcpServers {
		if sympoziumv1alpha1.IsReservedName(srv.Name) {
			return fmt.Errorf(
				"mcpServer %q uses the reserved %q name prefix, which Sympozium uses for the servers it injects itself (e.g. %q); rename it",
				srv.Name, sympoziumv1alpha1.ReservedNamePrefix, skilltools.ServerName)
		}
	}
	return nil
}

// skillToolsRegistryEntry is the MCP registry row a harness uses to reach the
// server. It is added to the JSON rendering only: the mcp-bridge sidecar reads
// the YAML one, and pointing it at this server would have it connect to a peer
// in its own pod for tools it already dispatches itself.
func skillToolsRegistryEntry() mcpServerYAML {
	return mcpServerYAML{
		Name:    skilltools.ServerName,
		URL:     skillToolsURL(),
		Timeout: 120,
	}
}

// buildSkillToolsContainer returns the sidecar. It runs the mcp-bridge image —
// same concern, MCP inside an agent pod, and a second image would be another
// build and tag to keep in step for no gain.
func (r *AgentRunReconciler) buildSkillToolsContainer(agentRun *sympoziumv1alpha1.AgentRun) corev1.Container {
	readOnly := true
	runAsNonRoot := true
	runAsUser := int64(1000)

	env := []corev1.EnvVar{
		{Name: "MCP_SERVE_SKILL_TOOLS", Value: "true"},
		{Name: "SIDECAR_TOOLS_MANIFEST_PATH", Value: "/config/sidecar-tools/sidecar-tools.json"},
		{Name: "MCP_IPC_PATH", Value: "/ipc/tools"},
		{Name: "SKILL_TOOLS_ADDR", Value: fmt.Sprintf("127.0.0.1:%d", skillToolsPort)},
	}

	// The policy this server enforces comes from the CR, through the
	// controller. The agent never touches it.
	if tp := agentRun.Spec.ToolPolicy; tp != nil {
		if len(tp.Allow) > 0 {
			env = append(env, corev1.EnvVar{Name: "TOOL_POLICY_ALLOW", Value: strings.Join(tp.Allow, ",")})
		}
		if len(tp.Deny) > 0 {
			env = append(env, corev1.EnvVar{Name: "TOOL_POLICY_DENY", Value: strings.Join(tp.Deny, ",")})
		}
	}

	// A native sidecar (an init container that never exits), not an ordinary
	// one, so the kubelet starts it and waits for its startup probe *before*
	// the agent container runs. Ordinary containers start concurrently, and a
	// harness whose MCP client fails on an unreachable server at boot — a
	// reasonable, fail-loud default for an adapter — would lose that race
	// intermittently. Native sidecars also stop last, so a tool call in flight
	// during shutdown still has something to answer it.
	always := corev1.ContainerRestartPolicyAlways

	return corev1.Container{
		Name:            skillToolsContainerName,
		Image:           r.imageRef("mcp-bridge"),
		ImagePullPolicy: corev1.PullIfNotPresent,
		RestartPolicy:   &always,
		Env:             env,
		StartupProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				// HTTP probes originate in the kubelet's network namespace. They
				// cannot reach a listener deliberately bound to pod loopback: using
				// host 127.0.0.1 probes the node loopback, while omitting it probes
				// the pod IP. Run this probe in the sidecar instead, so the server
				// stays unreachable outside the shared pod namespace.
				Exec: &corev1.ExecAction{Command: []string{
					"/bin/sh", "-c",
					fmt.Sprintf("wget -q -O /dev/null http://127.0.0.1:%d/readyz", skillToolsPort),
				}},
			},
			PeriodSeconds:    1,
			FailureThreshold: 30,
		},
		SecurityContext: &corev1.SecurityContext{
			ReadOnlyRootFilesystem:   &readOnly,
			AllowPrivilegeEscalation: boolPtr(false),
			RunAsNonRoot:             &runAsNonRoot,
			RunAsUser:                &runAsUser,
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
		VolumeMounts: []corev1.VolumeMount{
			// The whole /ipc: this container is Sympozium's own code and is
			// the writer the skill sidecars expect.
			{Name: "ipc", MountPath: "/ipc"},
			{Name: "sidecar-tools", MountPath: "/config/sidecar-tools", ReadOnly: true},
		},
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("25m"),
				corev1.ResourceMemory: resource.MustParse("32Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		},
	}
}

// mountHarnessMCPRegistry gives a replaced agent container only the trusted
// loopback SkillPack MCP registry. Remote MCP servers are rejected for harness
// runs, so this function never injects their credentials.
//
// It gets the JSON rendering, not the YAML one the mcp-bridge reads: the
// adapter is a shell script, and asking it to parse YAML would mean either a
// YAML binary in every harness image or hand-rolled parsing of a file that
// carries auth material. The per-server tokens come with it — without them a
// registry entry naming an authSecret would connect unauthenticated and fail at
// the first tool call.
func mountHarnessMCPRegistry(agent *corev1.Container) {
	agent.VolumeMounts = append(agent.VolumeMounts, corev1.VolumeMount{
		Name:      "mcp-config",
		MountPath: "/config/mcp",
		ReadOnly:  true,
	})
	agent.Env = append(agent.Env,
		corev1.EnvVar{Name: "MCP_CONFIG_PATH", Value: "/config/mcp/mcp-servers.json"},
	)
}
