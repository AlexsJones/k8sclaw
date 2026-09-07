# Lifecycle Hooks

Run containers before and after your agent — fetch context from external systems, upload artifacts, notify Slack, clean up resources. Lifecycle hooks let you wire arbitrary setup and teardown logic into the agent execution flow without modifying the agent itself.

## How It Works

```
AgentRun created
  → Pending phase
    → Controller creates workspace PVC (if postRun defined)
    → Controller creates lifecycle RBAC (if rbac defined)
    → PreRun init containers execute sequentially
  → Running phase
    → Agent container runs
  → PostRunning phase (if postRun defined)
    → Controller creates follow-up Job
    → PostRun init containers execute sequentially
  → Succeeded / Failed
    → Workspace PVC cleaned up
    → Lifecycle RBAC garbage-collected (owner reference)
```

### PreRun Hooks

PreRun hooks execute as **init containers** before the agent starts. They have access to:

| Path | Description |
|------|-------------|
| `/workspace` | Shared working directory — write files here for the agent to read |
| `/ipc` | IPC bus (tool calls, task input) |
| `/tmp` | Scratch space |

**Use cases:** Fetch incident context from PagerDuty, clone a git repo, download test data, warm caches.

#### Skipping a run

A preRun hook can **skip the run entirely** when there is no work to do — for
example, a hook that polls a queue or inbox and finds it empty. This avoids the
LLM call and its token cost.

To skip, the hook writes the marker file `/ipc/control/skip` and exits `0`. Any
text written to the file is recorded as the human-readable skip reason. The
agent container then short-circuits before any LLM call, and the AgentRun lands
in the terminal **`Skipped`** phase (distinct from `Succeeded`/`Failed`).

```yaml
spec:
  lifecycle:
    preRun:
      - name: check-queue
        image: curlimages/curl:latest
        command: ["sh", "-c",
          "test -s /workspace/queue.json || echo 'queue empty' > /ipc/control/skip"]
```

Notes:

- **Exit `0`, don't fail.** A non-zero exit fails the whole Pod (standard init
  container behavior) and marks the run `Failed` — that is *not* a skip.
- When a run is skipped, **postRun hooks are bypassed** (including gate hooks)
  and memory is not persisted — there was no agent output to process.
- Channel-triggered runs that are skipped send **no reply** (the skip is silent).
- Skipped runs are counted separately from successes/failures in gateway metrics
  (`skippedCount`).

### PostRun Hooks

PostRun hooks execute in a **follow-up Job** after the agent completes. They receive everything preRun hooks get, plus:

| Env Var | Description |
|---------|-------------|
| `AGENT_EXIT_CODE` | `"0"` on success, non-zero on failure |
| `AGENT_RESULT` | The agent's final response text (truncated to 32Ki) |

The workspace is shared between the agent and postRun hooks via a PersistentVolumeClaim. PostRun failures are **best-effort** — they're recorded as a `PostRunFailed` Condition but don't change the agent's final phase.

#### PostRun timeouts

PostRun hooks run sequentially in one Job, and that Job's budget is the **sum of the hooks' `timeout` fields** — 5 minutes each when unset, and never less than 10 minutes in total.

```yaml
postRun:
  - name: upload-artifacts
    image: amazon/aws-cli:latest
    timeout: 30m          # a slow upload gets the room it needs
```

Two things to know:

- The budget is measured from when the **postRun Job** starts, not from when the agent run started. A long-running agent still gets its full postRun budget.
- Kubernetes has no per-init-container timeout, so `timeout` bounds the Job as a whole rather than each container. Use it to give a slow hook room, not to police a fast one — a hook that overruns simply eats into what is left for the hooks after it.

**Use cases:** Upload artifacts to S3, post a summary to Slack, clean up temporary resources, trigger downstream pipelines.

### Environment Variables

All lifecycle hook containers receive these env vars:

| Env Var | Description |
|---------|-------------|
| `AGENT_RUN_ID` | Unique identifier for this agent run |
| `INSTANCE_NAME` | The Agent this run belongs to |
| `AGENT_NAMESPACE` | Kubernetes namespace |
| Custom env vars | Any `spec.env` entries from the AgentRun |

## RBAC for Hooks

Every AgentRun gets a unique ServiceAccount with automatic token mounting disabled. Lifecycle hooks receive **no Kubernetes token or permissions** by default. If your hooks need to interact with the Kubernetes API (for example, to create or delete ConfigMaps), declare the required RBAC rules:

```yaml
spec:
  lifecycle:
    rbac:
      - apiGroups: [""]
        resources: ["configmaps"]
        verbs: ["get", "list", "create", "delete"]
    preRun:
      - name: create-context
        image: soldevelo/kubectl:1.36
        command: ["kubectl", "create", "configmap", "run-context",
                  "--from-literal=started=$(date)"]
    postRun:
      - name: cleanup-context
        image: soldevelo/kubectl:1.36
        command: ["kubectl", "delete", "configmap", "run-context"]
```

The controller creates a namespace-scoped Role and RoleBinding for the run, bound only to that AgentRun's ServiceAccount. It projects a pod-bound, 10-minute token into the declared hook containers, not into the agent, harness, IPC bridge, or unrelated sidecars. The ServiceAccount and namespace-scoped RBAC are garbage-collected when the AgentRun is deleted; cluster-scoped RBAC is removed when the run completes.

## Examples

### Fetch PagerDuty incidents before the agent runs

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: oncall-agent
spec:
  agents:
    default:
      model: gpt-4o
      lifecycle:
        preRun:
          - name: fetch-incidents
            image: curlimages/curl:latest
            command: ["sh", "-c",
              "curl -s -H 'Authorization: Token token=$PD_TOKEN' \
               https://api.pagerduty.com/incidents?statuses[]=triggered \
               > /workspace/context/incidents.json"]
            env:
              - name: PD_TOKEN
                value: "your-pagerduty-token"
```

The agent's system prompt can then instruct it to read `/workspace/context/incidents.json` for current incident context.

### Upload artifacts to S3 after completion

```yaml
spec:
  lifecycle:
    postRun:
      - name: upload-report
        image: amazon/aws-cli:latest
        command: ["sh", "-c",
          "aws s3 cp /workspace/report.md s3://my-bucket/reports/$AGENT_RUN_ID.md"]
        env:
          - name: AWS_ACCESS_KEY_ID
            value: "AKIA..."
          - name: AWS_SECRET_ACCESS_KEY
            value: "..."
```

### Create and clean up a ConfigMap

```yaml
spec:
  lifecycle:
    rbac:
      - apiGroups: [""]
        resources: ["configmaps"]
        verbs: ["create", "delete", "get"]
    preRun:
      - name: create-config
        image: soldevelo/kubectl:1.36
        command: ["sh", "-c",
          "kubectl create configmap agent-scratch --from-literal=run=$AGENT_RUN_ID"]
    postRun:
      - name: delete-config
        image: soldevelo/kubectl:1.36
        command: ["kubectl", "delete", "configmap", "agent-scratch"]
```

### Ensemble with lifecycle hooks

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: oncall-team
spec:
  agentConfigs:
    - name: triage-agent
      systemPrompt: "You are an SRE triage agent..."
      lifecycle:
        preRun:
          - name: fetch-alerts
            image: curlimages/curl:latest
            command: ["sh", "-c", "curl -s $ALERTMANAGER_URL/api/v2/alerts > /workspace/context/alerts.json"]
```

## Response Gate

A **response gate** is a PostRun hook with `gate: true`. It holds the agent's output from reaching users until the gate explicitly approves, rejects, or rewrites it. This is useful for compliance checks, content filtering, PII scanning, or human-in-the-loop approval workflows.

### How It Works

Without a gate, the agent's response is published to channels (Slack, Telegram, web UI) the instant the agent finishes. With a gate:

1. The IPC bridge suppresses the completion event
2. The PostRun Job runs the gate hook container
3. The gate hook inspects the agent's output (via `AGENT_RESULT` env var)
4. The gate hook writes a verdict by patching an annotation on the AgentRun
5. The controller reads the verdict, applies it, and publishes the (possibly rewritten) response

If no verdict is written (hook fails, times out, or the hook is designed for manual approval), the `gateDefault` field controls behavior: `"block"` (default) replaces the output with an error, `"allow"` passes it through.

### Declaring a Gated Instance

Add `gate: true` to one PostRun hook in your instance's lifecycle config. At most one PostRun hook may be a gate. The gate hook needs RBAC permission to patch the AgentRun annotation:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: gated-agent
spec:
  agents:
    default:
      model: gpt-4o
      lifecycle:
        gateDefault: block   # or "allow"
        rbac:
          - apiGroups: ["sympozium.ai"]
            resources: ["agentruns"]
            verbs: ["get", "patch"]
        postRun:
          - name: content-filter
            image: my-org/content-filter:latest
            gate: true
            command: ["sh", "-c"]
            args:
              - |
                # Inspect $AGENT_RESULT, then patch the verdict:
                kubectl patch agentrun $AGENT_RUN_ID -n $AGENT_NAMESPACE \
                  --type=merge \
                  -p "{\"metadata\":{\"annotations\":{\"sympozium.ai/gate-verdict\":\
                  \"{\\\"action\\\":\\\"approve\\\"}\"}}}"
```

### Verdict Format

The gate hook patches the annotation `sympozium.ai/gate-verdict` with a JSON object:

| Action | Effect | Fields |
|--------|--------|--------|
| `approve` | Passes the original response through unchanged | `{"action":"approve"}` |
| `reject` | Replaces the response with a custom message | `{"action":"reject","response":"Blocked by policy"}` |
| `rewrite` | Replaces the response with a sanitized version | `{"action":"rewrite","response":"cleaned output"}` |
| `retry` | Hands the rejection back to the agent for another attempt — see [Retrying a rejected response](#retrying-a-rejected-response) | `{"action":"retry","reason":"npm run check failed","response":"<gate output>"}` |

All actions accept an optional `reason` field for audit logging.

### Retrying a Rejected Response

`reject` and `rewrite` are terminal: the run ends and the user sees the gate's
message. `retry` instead feeds the gate's output back to the agent as a **new
attempt**, so the agent can fix what the gate objected to.

This is not the Job's `backoffLimit` (which is disabled). That replays the pod
with an identical task and no knowledge of why the last attempt was rejected. A
retry attempt is a fresh AgentRun whose task carries the feedback.

Enable it with `lifecycle.retry`:

```yaml
spec:
  lifecycle:
    gateDefault: block
    retry:
      maxAttempts: 3          # total attempts including the first
      backoff: 30s            # optional delay before the next attempt starts
      maxChainTokens: 200000  # cumulative across the chain; 0 = unlimited
      on: [gate]              # only "gate" is wired today
    postRun:
      - name: build-check
        image: my-org/build-check:latest
        gate: true
        command: ["sh", "-c"]
        args:
          - |
            if OUT=$(npm run check 2>&1); then
              VERDICT='{"action":"approve"}'
            else
              VERDICT=$(jq -nc --arg out "$OUT" \
                '{action:"retry", reason:"npm run check failed", response:$out}')
            fi
            kubectl patch agentrun $AGENT_RUN_ID -n $AGENT_NAMESPACE --type=merge \
              -p "{\"metadata\":{\"annotations\":{\"sympozium.ai/gate-verdict\":$(echo $VERDICT | jq -Rs .)}}}"
```

The successor attempt receives a structured card in place of its task:

```
## Retry 2 of 3

### Original Task
<the original task, with earlier retry cards stripped>

### Your Previous Attempt
<the rejected output>

### Why It Was Rejected
npm run check failed

### Gate Output
<the gate's stdout/stderr>
```

Gate output is clipped to 4000 characters, with the clip announced in the card.
Set `SYMPOZIUM_RETRY_GATE_OUTPUT_MAX_CHARS` on the controller to change it — a
test-suite dump needs more room than a lint summary.

#### Inspecting a chain

Each attempt is named `<run>-retry-<n>` and records its lineage in
`status.attempt` / `status.retryOf`, plus labels for querying:

```bash
kubectl get agentruns -l sympozium.ai/retry-of=my-run
kubectl get agentrun my-run-retry-2 -o jsonpath='{.status.retryOf}{"\n"}'
```

A superseded attempt ends in the `Failed` phase with
`status.gateVerdict: retried` and a `Retried` condition naming its successor. It
publishes nothing — no channel reply, no failure event — because the chain has
not finished. When the attempts or the token budget run out, the last attempt
resolves as `retries-exhausted` and `gateDefault` decides whether its output is
blocked or passed through.

#### Why this is safe

**The retry decision is not agent-controlled.** The gate hook is an
operator-declared image in `lifecycle.postRun`, and the agent has no permission
to patch the `sympozium.ai/gate-verdict` annotation for its own run. An agent
cannot grant itself another attempt. That property is the reason retry is safe
here and would not be if the agent could self-retry.

Two further bounds on a gate that always says `retry`:

- `maxAttempts` is capped at admission by the bound `SympoziumPolicy`, so an
  operator sets the ceiling regardless of what a run requests:

  ```yaml
  apiVersion: sympozium.ai/v1alpha1
  kind: SympoziumPolicy
  metadata:
    name: default-governance
  spec:
    lifecyclePolicy:
      maxRetryAttempts: 3
  ```

- `maxChainTokens` caps the cumulative `status.tokenUsage.totalTokens` of every
  attempt in the chain.

### Manual (Human-in-the-Loop) Approval

If you want a human to approve or reject each response:

1. Set the gate hook to sleep indefinitely, and declare a matching `timeout` — otherwise the reviewer only gets the 10-minute default before `gateDefault` decides for them
2. Set `gateDefault: block` so unapproved responses are blocked
3. Use the web UI or API to approve or reject

```yaml
postRun:
  - name: manual-approval-gate
    image: busybox:1.36
    gate: true
    timeout: 24h        # how long a human has to respond
    command: ["sh", "-c", "sleep 86400"]
```

The `requireApproval` toggle in the API and UI configures exactly this, with a 24-hour window.

In the web UI, gated runs show an amber "Approval" badge on the runs list and an approval bar on the run detail page with Approve and Reject buttons. A warning toast fires when a run requires approval.

Via the API:

```bash
# Approve
curl -X POST http://localhost:9090/api/v1/runs/my-run/gate-verdict?namespace=default \
  -H 'Content-Type: application/json' \
  -d '{"action":"approve","reason":"reviewed by operator"}'

# Reject
curl -X POST http://localhost:9090/api/v1/runs/my-run/gate-verdict?namespace=default \
  -H 'Content-Type: application/json' \
  -d '{"action":"reject","response":"Content not approved","reason":"PII detected"}'
```

### Example: PII Scanner Gate

```yaml
spec:
  lifecycle:
    gateDefault: block
    rbac:
      - apiGroups: ["sympozium.ai"]
        resources: ["agentruns"]
        verbs: ["get", "patch"]
    postRun:
      - name: pii-scanner
        image: my-org/pii-scanner:latest
        gate: true
        command: ["sh", "-c"]
        args:
          - |
            if echo "$AGENT_RESULT" | pii-detect --strict; then
              VERDICT='{"action":"reject","response":"Response blocked: PII detected","reason":"pii-scanner"}'
            else
              VERDICT='{"action":"approve","reason":"pii-scanner-clean"}'
            fi
            kubectl patch agentrun $AGENT_RUN_ID -n $AGENT_NAMESPACE --type=merge \
              -p "{\"metadata\":{\"annotations\":{\"sympozium.ai/gate-verdict\":$(echo $VERDICT | jq -Rs .)}}}"
```

### Gate Status in the UI

| State | Web UI Indicator |
|-------|-----------------|
| Awaiting approval | Amber "Approval" badge on runs list, amber approval bar on detail page |
| Approved | Green "approved" banner on detail page |
| Rejected | Red "rejected" banner on detail page |
| Rewritten | Blue "rewritten" banner on detail page |
| Retried | Amber "retried" banner on detail page — superseded, not failed |
| Retries exhausted | Red "retries-exhausted" banner on detail page |
| Timeout/error | Red "timeout" or "error" banner on detail page |

## Agent Sandbox Compatibility

Lifecycle hooks work with both standard Job mode and [Agent Sandbox](agent-sandbox.md) mode:

- **PreRun hooks** are injected as init containers into the Sandbox CR — they execute inside the gVisor/Kata sandbox.
- **PostRun hooks** always run as a separate follow-up Job (outside the sandbox), since the sandbox is torn down after the agent completes.
- The workspace PVC is shared between both.

## Phases

With lifecycle hooks, the AgentRun phase transitions become:

`Pending` → `Running` → `PostRunning` → `Succeeded` (or `Failed`)

The `PostRunning` phase is only entered when `postRun` hooks are defined. Without them, the flow is the standard `Pending` → `Running` → `Succeeded`/`Failed`.

When a preRun hook [skips the run](#skipping-a-run), the flow short-circuits to the terminal `Skipped` phase: `Pending` → `Running` → `Skipped` (postRun hooks are bypassed).
