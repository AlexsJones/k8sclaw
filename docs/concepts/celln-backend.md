# Celln Backend (Hermetic Execution)

Sympozium optionally integrates with [Celln](https://github.com/sympozium-ai/celln) to run a single bounded, high-risk, or sensitive computation in a hardware-isolated microVM instead of a Kubernetes Job. It is selected per run with `spec.backend: "celln"` on an `AgentRun`.

Celln is **on by default** in the Helm chart. This page covers what that means, how to turn it off, and the one requirement that's easy to miss: Celln needs its own AI provider access on the host, separate from whatever provider your `Agent`/`AgentRun` is configured with.

## What Celln is (and isn't)

Celln runs one task in a sealed KVM cell and returns a bounded text result. It deliberately does **not** support ensembles, delegation, shared memory, IPC, NATS, streaming, or sub-agent spawns — anything that needs those capabilities must use the standard `job` backend (or `agentSandbox`; see [Agent Sandboxing](agent-sandbox.md)). Use it for individual computations you'd rather not run un-sandboxed: parsing untrusted input, running generated code, one-off risky operations.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: risky-computation
spec:
  agentRef: my-agent
  task: "Parse this untrusted file and summarize its structure"
  backend: celln
  timeout: "5m"
```

## How It Works

```
AgentRun (backend: celln)
  │
  ├─ Controller POSTs {id, task, timeout} to CELLN_ROUTER_URL
  │   (celln-router.celln-system.svc.cluster.local:8787)
  │
  ├─ Router (one pod per KVM node) forwards to the host-level
  │   celln dispatcher — a systemd service on that node, installed
  │   by the celln-installer DaemonSet
  │
  └─ Dispatcher asks its configured AI provider to write a program,
     attests and seals it into a real KVM cell, runs it, and returns
     the bounded output. status.cellnActionId tracks the poll.
```

The installer and router only schedule onto nodes labeled `celln.dev/kvm: "true"` — Celln needs `/dev/kvm` and is not a container-level isolation mechanism, so it can't run on arbitrary nodes the way the `job` backend can.

## Disabling Celln

```yaml
# values.yaml
celln:
  enabled: false
```

This is the master switch. When `false`, no `celln-system` namespace, installer, or router is deployed, and the controller isn't given a router URL — zero footprint.

**Disabling it does not remove `"celln"` as a valid `backend` value on the CRD.** A run submitted with `backend: celln` after disabling will still be admitted; the controller will attempt to reach the router at the default in-cluster DNS name, fail to resolve it, and the run transitions to `Failed` with a router-unreachable error. If you disable Celln, communicate that to whoever authors `AgentRun`s or agent defaults that set `backend: celln`.

## Enabling Celln: the AI provider requirement

This is the part that's easy to miss: **Celln's AI provider is configured independently of your `Agent`/`AgentRun`'s `model:` field.** A celln-backed run's task string goes to whatever provider is configured on the KVM *host* — the run's own `model.provider`/`model.name` are not passed through and are ignored for this backend.

The host-level dispatcher (not the Sympozium controller) needs one of:

- **An API key**, set via Helm — mounted into the `celln-installer` DaemonSet as a Secret, and written to `/etc/celln/agent-key` on the host for the dispatcher to read:
  ```yaml
  celln:
    anthropicApiKey: "sk-ant-..."   # needs the `claude` CLI on the host
    # openaiApiKey: "sk-..."        # needs the `codex` CLI on the host
    # deepseekApiKey: "sk-..."      # no CLI needed, plain API calls
    # openaiBaseUrl: ""             # optional, for an OpenAI-compatible proxy
  ```
  Set **one** of these. `anthropicApiKey`/`openaiApiKey` still require the corresponding CLI (`claude`/`codex`) to actually be installed and authenticated on the KVM node — the key alone isn't sufficient if the CLI is missing.

- **A locally running `ollama`** with a model already pulled, and no key set at all. The dispatcher auto-discovers it.

- **Nothing set** — the dispatcher searches the host for any authenticated CLI (`codex`, `claude`, `deepseek-api`, `ollama`, in that order) at startup and uses the first one it finds.

If none of the above is true on a given KVM node, that node's dispatcher is still installed and healthy from Kubernetes' point of view (the router's health check only verifies `/dev/kvm` and non-empty tool/mote stores, not provider availability) — the failure only surfaces when a task is actually dispatched, as an AgentRun `Failed` status with the provider's own auth error (e.g. *"`claude` has no saved login and ANTHROPIC_API_KEY is not set — authenticate it or set a key"*).

## Graceful Degradation

| Scenario | Behavior |
|----------|----------|
| `celln.enabled=false` | No `celln-system` namespace or resources. Runs with `backend: celln` fail at dispatch with a router-unreachable error, not at admission. |
| `celln.enabled=true`, no node labeled `celln.dev/kvm=true` | Installer/router DaemonSets deploy with zero pods scheduled. Runs fail the same way as above — nothing is listening at the router URL. |
| `celln.enabled=true`, KVM node(s) present, no AI provider reachable on the host | Router and dispatcher report healthy. The run reaches `Running`, then fails once the dispatcher's own provider check fails — see above. |
| Everything configured | Run dispatches, executes in a real sealed cell, and returns a bounded result. |

## See Also

- [Celln repository](https://github.com/sympozium-ai/celln) — the execution runtime itself: the cell/tool-lending model, hardware isolation guarantees, and `scripts/setup-host.sh` (what the installer DaemonSet runs on each node).
- [Custom Resources](custom-resources.md) — the `AgentRun.spec.backend` field.
