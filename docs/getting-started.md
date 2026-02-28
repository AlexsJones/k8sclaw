# Getting Started with Sympozium

This guide walks you through installing Sympozium, activating your first
PersonaPack, and setting up practical agent patterns for SRE, security, and
DevOps workflows.

---

## Prerequisites

- A running Kubernetes cluster (Kind, minikube, EKS, GKE, AKS, etc.)
- `kubectl` configured and pointing at the cluster
- An LLM API key (OpenAI, Anthropic, Azure OpenAI, or a local Ollama instance)

## Install

### Homebrew (macOS / Linux)

```bash
brew install AlexsJones/sympozium/sympozium
```

### Shell installer

```bash
curl -fsSL https://sympozium.com/install.sh | sh
```

### From source

```bash
go install github.com/alexsjones/sympozium/cmd/sympozium@latest
```

Verify the install:

```bash
sympozium version
```

---

## Deploy the control plane

Sympozium needs its CRDs, controller, NATS event bus, and webhook installed in
your cluster. The CLI handles this automatically during onboarding, or you can
do it manually:

```bash
sympozium install
```

This creates the `sympozium-system` namespace and deploys all components. It is
idempotent — safe to run again if something changes.

---

## Onboard your agents

Sympozium offers two onboarding paths:

1. **PersonaPacks (recommended)** — activate a pre-built bundle of agents via
   the TUI wizard. One action creates multiple purpose-built agents with skills,
   schedules, memory, and tool policies.
2. **Manual onboard** — create a single SympoziumInstance with `sympozium onboard`.
   Best for custom setups or CI/headless environments.

### PersonaPack activation (recommended)

Launch the TUI:

```bash
sympozium
```

The TUI opens on the **Personas** tab, listing the built-in PersonaPacks:

| Pack | Personas | Focus |
|------|----------|-------|
| `platform-team` | security-guardian, sre-watchdog, platform-engineer | Security audit, cluster health, scheduled ops |
| `devops-essentials` | incident-responder, cost-analyzer | Incident triage, resource optimisation |

Press **Enter** on a pack to start the activation wizard:

| Step | What it does |
|------|-------------|
| **1 — Pick personas** | Review the personas in the pack, deselect any you don't need |
| **2 — Provider** | Choose your LLM provider (OpenAI, Anthropic, Azure OpenAI, Ollama, or custom endpoint) |
| **3 — API key** | Paste your API key (stored as a Kubernetes Secret) |
| **4 — Model** | Pick a model (e.g. `gpt-4o`, `claude-sonnet-4-20250514`, `llama3`) |
| **5 — Channels** | Optionally bind messaging channels (Telegram, Slack, Discord, WhatsApp) |
| **6 — Confirm** | Review and apply — the controller creates all agents automatically |

Within seconds you'll have multiple agents running on schedules, each with
their own skills, memory, and tool policies. The TUI switches to the
**Instances** tab where you can see them come online.

**What gets created:**

For each persona in the pack, the PersonaPack controller creates:

- A **SympoziumInstance** — the agent identity with model, skills, and auth
- A **SympoziumSchedule** — the recurring task (heartbeat, sweep, or cron)
- A **ConfigMap** — persistent memory seeded with initial context

All resources are owned by the PersonaPack — deleting the pack cascades to
everything it created.

### Manual onboard (single instance)

For a single custom agent, or in headless/CI environments:

```bash
sympozium onboard           # TUI wizard
sympozium onboard --console # plain text fallback for CI
```

The wizard walks you through six steps:

| Step | What it does |
|------|--------------|
| **1 — Cluster check** | Verifies the cluster is reachable and Sympozium is installed. Offers to run `sympozium install` if CRDs are missing. |
| **2 — Provider** | Choose your LLM provider (OpenAI, Anthropic, Azure OpenAI, Ollama, or any OpenAI-compatible endpoint). Enter a base URL if needed, then paste your API key. |
| **3 — Channel** | Optionally connect a messaging channel (Telegram, Slack, Discord, WhatsApp) or skip for now. |
| **4 — Policy** | Choose a policy preset: **Permissive** (everything allowed), **Default** (commands require approval), or **Restrictive** (very locked-down). |
| **5 — Heartbeat** | Pick how often the agent should wake up on its own: every 30 min, hourly (recommended), every 6 hours, daily at 9 AM, or disabled. |
| **6 — Confirm** | Review a summary of your choices and apply. |

The wizard creates:

- A **Kubernetes Secret** with your API key
- A **SympoziumInstance** custom resource (your agent identity)
- A **SympoziumPolicy** (tool-gating rules)
- A **SympoziumSchedule** heartbeat (unless you chose "disabled")

After onboarding you land in the TUI dashboard — your agent is live.

---

## The web dashboard

Sympozium ships with a full web UI embedded inside the API server pod. To
access it from your workstation, use the CLI:

```bash
sympozium serve
```

This port-forwards the in-cluster API server to `http://127.0.0.1:8080` and
prints the authentication token. Log in with the token shown in the terminal.

### Options

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | `8080` | Local port to forward to |
| `--open` | `false` | Automatically open a browser |
| `--service-namespace` | `sympozium-system` | Namespace of the apiserver service |

### Manual access

If you prefer to port-forward manually:

```bash
kubectl port-forward -n sympozium-system svc/sympozium-apiserver 8080:8080
```

Retrieve the UI token:

```bash
kubectl get secret sympozium-ui-token -n sympozium-system \
  -o jsonpath='{.data.token}' | base64 -d
```

### What you can do

The web dashboard provides a graphical interface for **all** Sympozium actions:

- **Dashboard** — cluster overview with instance counts, run stats, and recent activity
- **Instances** — list, create, and delete SympoziumInstances
- **Runs** — view all AgentRuns, inspect logs, create new runs
- **Policies** — browse SympoziumPolicy rules
- **Skills** — explore installed SkillPacks
- **Schedules** — list and manage SympoziumSchedules
- **Personas** — browse and activate PersonaPacks

### Helm values

When installing via Helm, configure the web UI through `values.yaml`:

```yaml
apiserver:
  webUI:
    enabled: true       # Serve the embedded web dashboard (default: true)
    token: ""           # Explicit token; leave blank to auto-generate a Secret
```

If `token` is left empty, Helm creates a `<release>-ui-token` Secret with a
random 32-character token.

---

## The TUI dashboard

Once onboarded, launch the terminal dashboard:

```bash
sympozium
```

From the dashboard you can:

- **Send tasks** to your agent by typing a message and pressing Enter.
- **View runs** — see live status of current and past AgentRuns.
- **Edit an instance** — open the edit modal (press `e`) to change the
  heartbeat schedule, review memory, or toggle skills.
- **Switch instances** — if you have multiple SympoziumInstances.

---

## Running your first task

Type a message into the input bar:

```
List all pods that are not Running across every namespace.
```

Sympozium creates an **AgentRun** CR, spins up an ephemeral pod, calls your LLM,
and uses the built-in tools to fulfil the task. You will see the result stream
back in the TUI.

### Built-in tools

Every agent pod ships with these seven tools:

| Tool | Description |
|------|-------------|
| `execute_command` | Run shell commands (kubectl, curl, jq…) in a skill sidecar |
| `read_file` | Read a file from the pod filesystem |
| `write_file` | Create or overwrite a file |
| `list_directory` | List directory contents |
| `send_channel_message` | Send a message to Telegram / Slack / Discord / WhatsApp |
| `fetch_url` | HTTP GET a URL and return the body |
| `schedule_task` | Create, update, suspend, or delete SympoziumSchedule CRDs |

Tools are governed by the **SympoziumPolicy** you selected during onboarding. The
default policy lets read-only tools run freely and asks for approval before
`execute_command`.

---

## Agent patterns (what PersonaPacks create)

The following patterns show the resources that PersonaPacks generate
automatically. You can also create these manually if you prefer fine-grained
control.

Below are three practical agent personas. Each combines a
**SympoziumInstance**, one or more **SkillPacks**, and a tailored **schedule** to
create a purpose-built agent.

> **Tip:** The `platform-team` PersonaPack creates the SRE and Security agents
> below automatically. The `devops-essentials` pack creates the Incident
> Responder. You only need to write YAML manually for custom personas.

### 1. SRE On-Call Agent

An always-on agent that monitors cluster health, triages incidents, and can
perform rollbacks.

**Skills:** `k8s-ops`, `incident-response`

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: sre-oncall
spec:
  agents:
    default:
      model: gpt-4o
  skills:
    - skillPackRef: k8s-ops
    - skillPackRef: incident-response
  policyRef: default-policy
```

**Heartbeat** — every 30 minutes:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumSchedule
metadata:
  name: sre-oncall-heartbeat
spec:
  instanceRef: sre-oncall
  schedule: "*/30 * * * *"
  type: heartbeat
  includeMemory: true
  concurrencyPolicy: Forbid
  task: |
    Quick cluster health check:
    1. Are all nodes Ready?
    2. Any pods not Running?
    3. Any Warning events in the last 30 minutes?
    Summarise findings. If something looks wrong, triage it.
```

**Example tasks to try:**

```
Why is the checkout-service pod crash-looping in the production namespace?
```
```
Roll back the payments-api deployment to the previous version.
```
```
Show me the top 5 resource-hungry pods across the cluster.
```

The `k8s-ops` skill gives the agent kubectl access through a sidecar container
with scoped RBAC. The `sre-observability` skill adds Prometheus/Loki/Kubernetes
metrics and log triage workflows with read-only observability RBAC.
`incident-response` provides structured triage, log analysis, and rollback
runbooks so the agent follows a consistent process.

---

### 2. Security Auditor Agent

A periodic agent that reviews cluster configuration and scans code for
anti-patterns. Runs on a daily schedule.

**Skills:** `code-review` (includes security-patterns)

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: security-auditor
spec:
  agents:
    default:
      model: gpt-4o
  skills:
    - skillPackRef: code-review
    - skillPackRef: k8s-ops
  policyRef: restrictive
```

**Heartbeat** — daily at 9 AM:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumSchedule
metadata:
  name: security-daily-scan
spec:
  instanceRef: security-auditor
  schedule: "0 9 * * *"
  type: scheduled
  includeMemory: true
  concurrencyPolicy: Forbid
  task: |
    Daily security audit:
    1. Check for pods running as root (runAsNonRoot not set).
    2. Check for containers with privileged: true or ALL capabilities.
    3. Look for Secrets mounted as environment variables instead of volumes.
    4. Check that NetworkPolicies exist in all non-system namespaces.
    5. Report findings with severity (Critical / High / Medium / Low).
```

**Example tasks to try:**

```
Audit RBAC — which ServiceAccounts have cluster-admin?
```
```
Check if any deployments are using the :latest image tag.
```
```
Review the Helm values in the staging namespace for hardcoded secrets.
```

The `restrictive` policy ensures the agent cannot run arbitrary commands
without approval — the right guardrail for a security-focused agent.

---

### 3. DevOps / Platform Engineer Agent

A general-purpose agent for day-to-day cluster operations, deploys, and
troubleshooting. Runs with a permissive policy on development clusters.

**Skills:** `k8s-ops`, `code-review`

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumInstance
metadata:
  name: devops
spec:
  agents:
    default:
      model: gpt-4o
  skills:
    - skillPackRef: k8s-ops
    - skillPackRef: code-review
  policyRef: permissive
```

**Heartbeat** — every hour:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumSchedule
metadata:
  name: devops-heartbeat
spec:
  instanceRef: devops
  schedule: "0 * * * *"
  type: heartbeat
  includeMemory: true
  concurrencyPolicy: Forbid
  task: |
    Check in: review any pending tasks in memory.
    Quick scan — any pods restarting or events firing?
```

**Example tasks to try:**

```
Scale the frontend deployment to 5 replicas in the staging namespace.
```
```
Create a new namespace called "feature-xyz" with a LimitRange and ResourceQuota.
```
```
Show me the rollout history for the api-gateway deployment and explain what
changed between revision 3 and 4.
```
```
Drain node worker-3 for maintenance, making sure no PDBs are violated.
```

With the `permissive` policy, this agent has free rein on a dev cluster — fast
iteration without approval gates.

---

## Built-in SkillPacks

Sympozium ships with four built-in SkillPacks. Enable them on any
SympoziumInstance:

| SkillPack | Category | What it includes |
|-----------|----------|------------------|
| **k8s-ops** | Kubernetes | Cluster overview, pod troubleshooting, resource management. Comes with a sidecar that has kubectl and cluster-scoped RBAC. |
| **sre-observability** | SRE | Observability triage with Prometheus queries, Loki/kubectl log analysis, and event correlation. Comes with a sidecar and read-only observability RBAC. |
| **incident-response** | SRE | Structured incident triage, log analysis, rollback procedures. |
| **code-review** | Development | Code review checklist, security anti-patterns, Go-specific review patterns. |

Apply them from the `config/skills/` directory:

```bash
kubectl apply -f config/skills/
```

Or enable them through the TUI edit modal (press `e` on your instance, go to
the **Skills** tab).

---

## Channels

Connect your agent to a messaging platform so you can interact over chat:

| Channel | How to connect |
|---------|----------------|
| **Telegram** | Create a bot with [@BotFather](https://t.me/BotFather), get the token, pass it during onboarding or set it in the SympoziumInstance channel config. |
| **Slack** | Create a Slack app with Socket Mode enabled, add the bot/app token during onboarding. |
| **Discord** | Create a Discord bot, grab the token, and connect it during onboarding. |
| **WhatsApp** | Use the WhatsApp Business API — Sympozium displays a QR code in the TUI for pairing. |

Channels are optional. You can always interact through the TUI or by creating
AgentRun CRs directly with kubectl.

---

## Policies at a glance

| Policy | Who it is for | Key rules |
|--------|---------------|-----------|
| **Permissive** | Dev clusters, demos | All tools allowed, no approval needed, generous resource limits |
| **Default** | General use | `execute_command` requires approval, everything else allowed |
| **Restrictive** | Production, security | All tools denied by default, must be explicitly allowed, sandbox required |

---

## Heartbeat schedules

The heartbeat wakes your agent up periodically to check in — review memory,
scan the cluster, or run a standing task.

| Preset | Cron | Good for |
|--------|------|----------|
| Every 30 min | `*/30 * * * *` | Active incident monitoring, SRE on-call |
| Every hour | `0 * * * *` | General ops, default for most users |
| Every 6 hours | `0 */6 * * *` | Light-touch monitoring, cost-sensitive setups |
| Daily at 9 AM | `0 9 * * *` | Daily audits, reports, security scans |
| Disabled | — | On-demand only, no background activity |

You can change the heartbeat at any time through the TUI edit modal or by
editing the SympoziumSchedule CR directly:

```bash
kubectl edit sympoziumschedule <instance>-heartbeat
```

---

## Creating AgentRuns with kubectl

You do not need the TUI to run tasks. Create an AgentRun CR directly:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: quick-check
spec:
  instanceRef: devops
  task: "How many nodes are in the cluster and what are their roles?"
  model:
    name: gpt-4o
    provider: openai
  skills:
    - k8s-ops
  timeout: "5m"
```

```bash
kubectl apply -f quick-check.yaml
kubectl get agentrun quick-check -w   # watch status.phase
```

The phase transitions: `Pending` → `Running` → `Succeeded` (or `Failed`).

---

## Creating custom PersonaPacks

You can create your own PersonaPack to bundle a set of agents tailored to your
team. Save this as a YAML file and apply it:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: PersonaPack
metadata:
  name: my-team
spec:
  description: "Custom agents for my team"
  category: custom
  version: "1.0.0"
  personas:
    - name: log-analyzer
      displayName: "Log Analyzer"
      systemPrompt: |
        You are a log analysis specialist. You parse structured and
        unstructured logs to identify errors, anomalies, and trends.
      skills:
        - k8s-ops
      schedule:
        type: sweep
        interval: "1h"
        task: "Scan pod logs across all namespaces for ERROR and FATAL entries from the last hour."
      memory:
        enabled: true
        seeds:
          - "Focus on patterns that repeat across multiple pods"
    - name: doc-writer
      displayName: "Documentation Writer"
      systemPrompt: |
        You are a technical writer. You review cluster configuration,
        CRDs, and RBAC policies, then produce clear documentation.
      skills:
        - k8s-ops
        - code-review
      schedule:
        type: scheduled
        cron: "0 8 * * 1"
        task: "Audit all namespaces and produce a weekly cluster inventory report."
      memory:
        enabled: true
```

```bash
kubectl apply -f my-team-personapack.yaml
```

The pack appears in the TUI Personas tab in `Pending` phase. Press Enter to
activate it with your API key — the controller does the rest.

---

## What's next

- **Write a custom SkillPack** — see [Writing Skills](writing-skills.md)
- **Add a new tool** — see [Writing Tools](writing-tools.md)
- **Write integration tests** — see [Writing Integration Tests](writing-integration-tests.md)
- **Read the full architecture** — see [Design Document](sympozium-design.md)
