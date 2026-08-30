# Sympozium vs kagent

Both Sympozium and [kagent](https://kagent.dev/) are Kubernetes-native platforms for running AI agents. They share a CRD-driven, declarative model and support multiple LLM providers. The differences lie in **how agents execute, how tools are isolated, and how far the platform extends beyond a single agent run**.

---

## At a Glance

| Dimension | Sympozium | kagent |
|-----------|-----------|--------|
| **Agent runtime** | Ephemeral Pod (Kubernetes Job) per run | Long-running engine process (Python or Go ADK) |
| **Tool isolation** | Dedicated sidecar container per skill, ephemeral RBAC | In-process (MCP client inside the engine) |
| **Kernel-level sandboxing** | gVisor / Kata via `kubernetes-sigs/agent-sandbox`, warm pools | Not available |
| **Multi-tenancy** | Namespace-per-tenant, per-instance RBAC, admission webhooks | Namespace-scoped CRDs |
| **Agent packaging** | Ensembles (bundle personas, skills, schedules, memory seeds) | Individual Agent CRDs |
| **Persistent memory** | SQLite + FTS5 on PVC, survives across runs | Vector-backed memory (in-engine) |
| **Channels** | Telegram, Slack, Discord, WhatsApp as dedicated Deployments via NATS JetStream | Slack, Discord (in-engine integration) |
| **Scheduled runs** | SympoziumSchedule CRD with CronJob-style concurrency policies | Not available |
| **MCP support** | MCPServer CRD with auto-discovery, tool filtering, managed or external servers | MCP tools as CRDs, remote MCP server references |
| **Observability** | Built-in TUI + Web UI, resource views, live logs | Dashboard UI, OpenTelemetry tracing |
| **Human-in-the-loop** | Policy-driven via SympoziumPolicy CRD | Tool-level approve/reject in UI |

---

## Design Philosophy

### Agent Execution Model

This is the most fundamental difference between the two projects.

**Sympozium** runs every agent invocation as an **ephemeral Kubernetes Job**. The agent container starts, executes, and is garbage-collected. There is no long-lived agent process. This means:

- Each run gets a fresh pod with its own SecurityContext, resource limits, and network policy.
- Horizontal scaling is native — the cluster scheduler handles placement and resource pressure.
- Blast radius of a misbehaving agent is contained to a single short-lived pod.

**kagent** runs agents inside a **persistent engine process** (Python ADK or Go ADK) that handles the conversation loop. The controller provisions CRDs, but the agent runtime is a long-running service, not an ephemeral workload. This means:

- Faster turn-around for conversational interactions (no pod startup per message).
- Agent state lives in the engine process memory.
- Multiple agent runs may share the same engine process.

!!! note "Trade-off"
    kagent's persistent engine optimizes for low-latency chat. Sympozium's ephemeral model optimizes for isolation, auditability, and safe execution of cluster-admin operations.

### Tool Isolation

**Sympozium** injects every skill as a **dedicated sidecar container** in the agent pod. Each AgentRun gets a unique ServiceAccount; least-privilege RBAC and a short-lived projected token are exposed only to sidecars that declare Kubernetes access. Skills communicate with the agent container over shared workspace/IPC surfaces. A kubectl skill can have cluster access; the agent or external harness container does not receive its Kubernetes credential.

**kagent** loads tools **in-process** within the engine via MCP clients or built-in function calls. Tool execution shares the same process, memory space, and Kubernetes credentials as the agent itself.

!!! warning "Why this matters"
    If an agent can convince its LLM to call a tool with malicious arguments, in-process tools execute with the full privileges of the engine's ServiceAccount. Sidecar isolation means the agent container never holds the credentials — only the skill sidecar does, scoped to exactly the permissions that skill needs.

### Sandboxing

**Sympozium** integrates with the [`kubernetes-sigs/agent-sandbox`](https://deploy.sympozium.ai/docs/concepts/agent-sandbox/) project to offer kernel-level isolation via **gVisor** (user-space kernel) or **Kata Containers** (lightweight VM). `SandboxWarmPool` CRDs pre-provision ready environments so agents start instantly despite the stronger isolation boundary.

**kagent** does not currently offer kernel-level sandboxing beyond standard Kubernetes pod security.

### Multi-Tenancy

**Sympozium** is designed around an `Agent` CRD that represents a single tenant. Each instance gets its own channel connections, agent configurations, policy bindings, and memory store. Admission webhooks enforce policy boundaries. Multiple teams share a cluster without seeing each other's agents or data.

**kagent** uses Kubernetes namespace isolation and supports cross-namespace tool references, but does not have a first-class tenant abstraction or admission-webhook-based policy enforcement.

### Agent Lifecycle

**Sympozium** provides **Ensembles** — declarative bundles that stamp out an entire team of agents, their skills, schedules, and memory seeds in one apply. `SympoziumSchedule` CRDs drive cron-based recurring runs (health checks, alert triage, resource right-sizing) with CronJob-style concurrency policies (Forbid, Allow, Replace). Cleanup is automatic via ownerReferences.

**kagent** defines agents individually as CRDs. Scheduling and bundling are handled outside the platform.

### Channels & Messaging

**Sympozium** deploys each channel (Telegram, Slack, Discord, WhatsApp) as a **dedicated Kubernetes Deployment**. Messages flow through **NATS JetStream** with durable pub/sub — the channel pod and the agent pod are fully decoupled. This survives restarts, scales independently, and supports group chat.

**kagent** integrates Slack and Discord through in-engine handlers. Channel state lives in the engine process.

---

## When to Choose What

**Choose Sympozium when you need:**

- Agents that perform cluster-admin operations (kubectl, Helm, scaling) and you want strong isolation guarantees.
- Multi-tenant environments where teams share a cluster.
- Scheduled, unattended agent runs (overnight health sweeps, alert triage).
- Messaging integrations beyond Slack/Discord (Telegram, WhatsApp).
- Kernel-level sandboxing for untrusted or third-party agent code.

**Choose kagent when you need:**

- Low-latency conversational agents with fast turn-around (no pod startup per message).
- Google ADK, CrewAI, or LangGraph integration out of the box.
- A lighter-weight setup for single-tenant experimentation.
- A2A (Agent-to-Agent) protocol support.

---

## Summary

kagent and Sympozium solve similar problems from different angles. kagent is a solid choice for teams that want a lightweight, conversational agent framework with Google ADK integration. Sympozium is built for production multi-tenant environments where **isolation, scheduled automation, and safe cluster operations** are non-negotiable.
