# Your First AgentRun

This guide walks through creating and running your first agent using only
YAML files and kubectl — no TUI required. By the end, you'll have an agent
that runs a task and you'll know how to check its status and read its output.

---

## Prerequisites

- A running Kubernetes cluster (Kind, minikube, etc.)
- Sympozium installed (`sympozium install`)
- An API key for an LLM provider (OpenAI, Anthropic, etc.) or a local
  inference server (Ollama, LM Studio)

---

## Step 1: Create an API key Secret

Sympozium reads provider credentials from Kubernetes Secrets. Create one for
your provider:

**OpenAI:**

```bash
kubectl create secret generic my-openai-key \
  --from-literal=OPENAI_API_KEY=sk-...
```

**Anthropic:**

```bash
kubectl create secret generic my-anthropic-key \
  --from-literal=ANTHROPIC_API_KEY=sk-ant-...
```

**Local provider (Ollama / LM Studio):**

```bash
kubectl create secret generic local-key \
  --from-literal=OPENAI_API_KEY=not-needed
```

---

## Step 2: Create an Agent

An Agent is the per-user/per-tenant configuration that declares
which model to use, which skills to mount, and which policy to follow.

Save this as `instance.yaml`:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: my-first-agent
spec:
  agents:
    default:
      model: gpt-4o
  authRefs:
    - provider: openai
      secret: my-openai-key
  skills:
    - skillPackRef: k8s-ops
  policyRef: default-policy
```

Apply it:

```bash
kubectl apply -f instance.yaml
```

Verify it's ready:

```bash
kubectl get agent my-first-agent
```

You should see `Phase: Running` (or `Pending` briefly while the controller reconciles).

> **Using a local provider?** Add a `baseURL` to point at your inference server:
> ```yaml
> agents:
>   default:
>     model: llama3
>     baseURL: "http://172.18.0.1:11434/v1"
> ```
> See the [Ollama guide](./ollama.md) or [LM Studio guide](./lm-studio.md) for details.

---

## Step 3: Create an AgentRun

An AgentRun is a single agent invocation — it tells Sympozium to run a task
using a specific instance's configuration.

Save this as `run.yaml`:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: my-first-run
spec:
  agentRef: my-first-agent
  agentId: primary
  sessionKey: "session-001"
  task: "List all pods in every namespace and summarise their health. Report any pods that are not Running."
  model:
    provider: openai
    model: gpt-4o
    authSecretRef: my-openai-key
  skills:
    - skillPackRef: k8s-ops
  timeout: "5m"
```

Apply it:

```bash
kubectl apply -f run.yaml
```

---

## Step 4: Watch the run

### Watch the phase transitions

```bash
kubectl get agentrun my-first-run -w
```

You'll see the phase progress:

```
NAME           INSTANCE         PHASE     POD                        AGE
my-first-run   my-first-agent   Pending                              0s
my-first-run   my-first-agent   Running   my-first-run-agent-xxxxx   5s
my-first-run   my-first-agent   Succeeded my-first-run-agent-xxxxx   45s
```

### Watch the pod

The AgentRun creates a Job, which creates a pod. You can watch the pod directly:

```bash
kubectl get pods -l sympozium.ai/agent-run=my-first-run -w
```

---

## Step 5: Read the output

### From the AgentRun status

The agent's final response is stored in the AgentRun's `status.result` field:

```bash
kubectl get agentrun my-first-run -o jsonpath='{.status.result}'
```

Or view the full status:

```bash
kubectl get agentrun my-first-run -o yaml
```

The status includes:

| Field | Description |
|-------|-------------|
| `phase` | Final phase (`Succeeded` or `Failed`) |
| `result` | The agent's final response text |
| `error` | Error message (if failed) |
| `podName` | The pod that ran the agent |
| `startedAt` | When the run started |
| `completedAt` | When the run finished |
| `tokenUsage` | LLM token counts and timing |
| `traceID` | OpenTelemetry trace ID (if observability is enabled) |

### From the pod logs

For more detail (including intermediate tool calls and reasoning), check the
pod logs:

```bash
kubectl logs -l sympozium.ai/agent-run=my-first-run -c agent
```

> **Tip:** If the pod has been cleaned up (default behaviour), set
> `cleanup: keep` in the AgentRun spec to retain the pod for debugging.

---

## Step 6: Clean up

Delete the AgentRun (the Job and pod are garbage-collected automatically):

```bash
kubectl delete agentrun my-first-run
```

To keep the instance for future runs, leave it in place. To remove everything:

```bash
kubectl delete agent my-first-agent
kubectl delete agentrun my-first-run
```

---

## Common variations

### Run with Anthropic

```yaml
spec:
  agentRef: my-first-agent
  agentId: primary
  sessionKey: "session-002"
  task: "Check cluster health."
  model:
    provider: anthropic
    model: claude-sonnet-4-20250514
    authSecretRef: my-anthropic-key
    thinking: medium
  skills:
    - skillPackRef: k8s-ops
  timeout: "5m"
```

### Run with sandbox enabled

```yaml
spec:
  agentRef: my-first-agent
  agentId: primary
  sessionKey: "session-003"
  task: "Analyse the deployment manifests in /workspace."
  model:
    provider: openai
    model: gpt-4o
    authSecretRef: my-openai-key
  sandbox:
    enabled: true
    image: ghcr.io/sympozium-ai/sympozium/sandbox:latest
  timeout: "5m"
```

### Keep the pod for debugging

```yaml
spec:
  agentRef: my-first-agent
  agentId: primary
  sessionKey: "session-004"
  task: "Debug the failing cronjob."
  model:
    provider: openai
    model: gpt-4o
    authSecretRef: my-openai-key
  skills:
    - skillPackRef: k8s-ops
  timeout: "10m"
  cleanup: keep
```

### Run without a schedule (one-off)

Every AgentRun is one-off by default. If you want recurring runs, create a
SympoziumSchedule or use an Ensemble with a schedule — see
[Writing Ensembles](./writing-ensembles.md).

---

## What happens under the hood

```
1. You apply the AgentRun CR
   └── Controller picks it up

2. Controller resolves the Agent
   └── Finds model config, skills, policy

3. Controller creates a Job
   └── Pod spec includes:
       ├── agent container (agent-runner)
       ├── ipc-bridge sidecar (NATS)
       └── skill sidecars (e.g. skill-k8s-ops)

4. Agent starts
   └── Reads skills from /skills/
   └── Executes the task using LLM + tools

5. Agent completes
   └── Result written to status.result
   └── Phase set to Succeeded (or Failed)
   └── Pod cleaned up (unless cleanup: keep)
   └── Ephemeral RBAC garbage-collected
```

---

## Troubleshooting

| Issue | Check |
|-------|-------|
| AgentRun stays `Pending` | `kubectl describe agentrun <name>` — look at conditions and events |
| Pod not created | `kubectl get jobs -l sympozium.ai/agent-run=<name>` — is the Job there? |
| Pod in `ImagePullBackOff` | Check image names and registry access |
| Agent fails with auth error | Verify the Secret exists and has the correct key (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, etc.) |
| Agent times out | Increase `timeout`, or use a faster model |
| Result is empty | Check pod logs: `kubectl logs <pod> -c agent` |
| `Failed` with no error | Check pod logs and events for OOM kills or evictions |

---

## Next steps

- [Writing Ensembles](./writing-ensembles.md) — bundle agents into reusable teams
- [Writing Skills](./writing-skills.md) — create custom tools for your agents
- [Local Models guide](./local-models.md) — run models in-cluster with no API keys
- [LM Studio guide](./lm-studio.md) / [Ollama guide](./ollama.md) — use external local inference
- [Agent Sandboxing](../concepts/agent-sandbox.md) — isolate agent execution
