# Custom Resources

Sympozium models every agentic concept as a Kubernetes Custom Resource:

| CRD | Kubernetes Analogy | Purpose |
|-----|--------------------|---------|
| `Agent` | Namespace / Tenant | Per-user gateway — channels, provider config, memory settings, skill bindings |
| `AgentRun` | Job / [Sandbox CR](agent-sandbox.md) | Single agent execution — task, model, result capture, memory extraction. Optionally uses Agent Sandbox CRDs for kernel-level isolation |
| `SympoziumPolicy` | NetworkPolicy | Feature and tool gating — what an agent can and cannot do |
| `SkillPack` | ConfigMap | Portable skill bundles — kubectl, Helm, or custom tools — mounted into agent pods as files, with optional sidecar containers for cluster ops |
| `SympoziumSchedule` | CronJob | Recurring tasks — heartbeats, sweeps, scheduled runs with cron expressions |
| `Ensemble` | Helm Chart / Operator Bundle | Pre-configured agent bundles — activating a pack stamps out Agents, Schedules, and memory for each persona |
| `Model` | Deployment + Service | [Cluster-local inference](../guides/local-models.md) — declares a model (GGUF or HuggingFace), controller deploys an inference server (llama.cpp, vLLM, or TGI) and exposes an OpenAI-compatible endpoint |
| `MCPServer` | Deployment + Service | Managed [Model Context Protocol](../mcp-servers.md) server — external tool providers with auto-discovery and allow/deny filtering |
| `SympoziumConfig` | Cluster configuration | Platform-wide singleton — gateway, canary, and pricing settings |

---

## Agent

The core resource representing an agent identity. Each instance has:

- An LLM provider configuration (model, API key reference, base URL)
- Skill bindings (which SkillPacks are active)
- Channel connections (Telegram, Slack, etc.)
- Memory settings (enabled/disabled, max size)
- A policy reference
- Optional node selector for pinning agent pods to specific nodes (e.g. GPU nodes running Ollama)

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: my-agent
spec:
  agents:
    default:
      model: gpt-4o
  skills:
    - skillPackRef: k8s-ops
    - skillPackRef: code-review
  policyRef: default-policy
```

---

## AgentRun

Represents a single agent execution. The controller reconciles each AgentRun into an ephemeral Kubernetes Job containing the agent container, IPC bridge, and any skill sidecars.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: quick-check
spec:
  agentRef: my-agent
  agentId: default
  sessionKey: "quick-check-001"
  task: "How many nodes are in the cluster?"
  model:
    provider: openai
    model: gpt-4o
    authSecretRef: my-openai-key
  skills:
    - skillPackRef: k8s-ops
  timeout: "5m"
```

Phase transitions: `Pending` → `Running` → `Succeeded` (or `Failed`). When [lifecycle hooks](lifecycle-hooks.md) with `postRun` are defined: `Pending` → `Running` → `PostRunning` → `Succeeded` (or `Failed`).

A [response gate](lifecycle-hooks.md#retrying-a-rejected-response) can retry a rejected run. Each attempt is its own AgentRun, linked by `status.retryOf` and `status.attempt`; a superseded attempt ends `Failed` with `status.gateVerdict: retried`. Use `kubectl get agentruns -o wide` to see the chain.

Setting `spec.backend: celln` routes the run to a hardware-isolated microVM instead of a Job — no ensembles, delegation, or shared memory, and the run's own `model:` field is ignored in favor of whatever AI provider is configured on the KVM host. See [Celln Backend](celln-backend.md).

---

## SympoziumPolicy

Gates features and tools at admission time. The webhook evaluates policies before a pod is created.

| Policy | Who it is for | Key rules |
|--------|---------------|-----------|
| **Permissive** | Dev clusters, demos | All tools allowed, no approval needed |
| **Default** | General use | `execute_command` requires approval, everything else allowed |
| **Restrictive** | Production, security | All tools denied by default, must be explicitly allowed |

---

## SkillPack

Portable skill bundles mounted into agent pods as files. Can optionally declare sidecar containers with runtime tools and RBAC rules. See [Skills & Sidecars](skills.md) for details.

---

## SympoziumSchedule

Cron-based recurring agent runs. See [Scheduled Tasks](scheduled-tasks.md) for details.

---

## Ensemble

Pre-configured agent bundles. See [Ensembles](ensembles.md) for details.
