# Celln Backend (Hermetic Execution)

Sympozium optionally integrates with [Celln](https://github.com/sympozium-ai/celln) to run a single bounded, high-risk, or sensitive computation in a hardware-isolated microVM instead of a Kubernetes Job. It is selected per run with `spec.backend: "celln"` on an `AgentRun`.

Celln is enabled by default: `celln.enabled=true` installs the router and
controller configuration with Sympozium. Its installer DaemonSet runs
privileged, with `hostPID`, and a read-write mount of the host root filesystem
to set up KVM; it and the router schedule only on nodes explicitly labelled
`celln.dev/kvm=true`. That label is the deliberate host-setup boundary. Disable
Celln entirely with `helm upgrade --set celln.enabled=false`. This page covers
the one requirement that's easy to miss: Celln needs its own AI provider access
on the host, separate from whatever provider your `Agent`/`AgentRun` is configured with.

The web UI's Runs page shows a live banner if any listed run uses `backend: celln` while the router is unreachable, and the New Run dialog checks live reachability when you select the Celln backend — both backed by `GET /api/v1/capabilities`.

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

### Dispatcher authentication

The controller requires `CELLN_TOKEN_FILE`, an operator-provisioned file
containing the Celln bearer credential (at least 24 bytes). This is distinct
from model-provider credentials and is never read from AgentRun task text.
The file is re-read for each POST/GET so mounted Secret rotation takes effect.
Redirects are refused rather than forwarding credentials or changing methods.

With Helm, create the Secret in the **controller namespace** with key `token`
and set `celln.tokenSecret` to its name. The token must match the selected
dispatcher/router's configured credential. Prefer an HTTPS `celln.routerUrl`;
non-loopback plaintext requires explicit `celln.allowInsecureHttp: true` (or
`CELLN_ALLOW_INSECURE_HTTP=true` outside Helm). This opt-in does not encrypt
traffic. Missing or invalid credentials refuse dispatch without backend fallback.

These settings configure the controller client; they do not distribute a
credential to host dispatchers or make legacy installer images compatible.
Operators must configure both ends deliberately.

```
AgentRun (backend: celln)
  │
  ├─ Controller POSTs a celln.dev/v1alpha1 ExecutionRequest to CELLN_ROUTER_URL
  │   (celln-router.celln-system.svc.cluster.local:8787)
  │
  ├─ Router (one pod per KVM node) forwards to the host-level
  │   celln dispatcher — a systemd service on that node, installed
  │   by the celln-installer DaemonSet
  │
  └─ Dispatcher verifies the pinned program (or forges one for task-only
     requests), seals a KVM cell from a warm mote, runs it, and returns
     a receipt plus bounded display output. status.cellnActionId tracks the poll.
```

The installer and router only schedule onto nodes labeled `celln.dev/kvm: "true"` — Celln needs `/dev/kvm` and is not a container-level isolation mechanism, so it can't run on arbitrary nodes the way the `job` backend can.

## Trust model: task text never grants tool authority

Celln draws a hard line between two authority levels for code running inside a cell: the **tool lane** (an already-attested host binary, sealed in read-only — full but narrow authority) and the **agent lane** (agent-authored code, generated at run time — gets only what's explicitly loaned to it, permanently, no matter how well it's built). Full model: [tool lane](https://sympozium-ai.github.io/celln/tool-lane.html) / [agent lane](https://sympozium-ai.github.io/celln/agent-lane.html).

A task-only `backend: celln` run sends a `forge` request in the agent lane.
Reproducibility does not promote generated code to tool authority. Its default
bounds remain 256MiB guest memory, 64KiB output and workspace `none`.

For an existing immutable program, set `spec.celln` explicitly. It contains
`mote: {hash}`, `tools: [{alias, hash}]`, optional bounded `inputs` (name, hash,
mediaType, bytes), `invocation: {alias, args}`, `lane`, and `capabilities`
(workspace, optional egress, memoryBytes, outputBytes). `spec.timeout` supplies
the end-to-end deadline. In this mode task text is not sent to a model and
does not influence the executable or its arguments. The dispatcher must still
independently approve and verify the artifacts and refuse unsupported authority.
Requesting the tool lane does not turn an agent-authored artifact into a tool.

The controller freezes the full versioned request in `status.cellnRequest`
before the first POST. Retries use that snapshot, not a changed spec. A terminal
success requires a complete receipt with matching request, phase, pinned mote,
invoked tool and input identities, valid timestamps and bounded output metadata.
The validated JSON is retained in `status.cellnReceipt`, including for failed or
cancelled executions that actually produced a receipt. Pre-execution refusal may
have none. `status.result` remains lossy UTF-8 display text; use the receipt's
immutable output reference, not that mutable text, as an artifact identity.

This does not add whole-agent, sidecar, shared-memory or ensemble execution.

Deleting a dispatched AgentRun requests authenticated remote cancellation.
The controller retains its finalizer while Celln returns `202 Cancelling` or
cannot be reached; acknowledgement is not proof of teardown. Its deadline
backstop also cancels remotely and waits for a terminal record before failing
the run. Terminal status set by another controller does not bypass this cleanup.
This relies on the configured dispatcher's execution registry; it is not
distributed failover or recovery of a lost registry after a dispatcher crash.

## Enabling / Disabling Celln

```bash
sympozium install --enable-hermetic-workloads
# or: make install ENABLE_HERMETIC_WORKLOADS=true
# or: helm upgrade --install sympozium charts/sympozium/ --set celln.enabled=true ...
```

```yaml
# values.yaml — explicitly disable the chart's default-enabled integration
celln:
  enabled: false
```

When `false`, no `celln-system` namespace, installer, or router is deployed, and the controller/apiserver aren't given a router URL or credential mount.

**Disabling it does not remove `"celln"` as a valid `backend` value on the CRD.** A run submitted while disabled is still schema-valid but refuses when the controller lacks the required transport configuration. There is no fallback to a Job. Communicate configuration changes to authors of Celln-backed runs.

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

For a repeatable static-program proof, use Celln's `make conformance-kvm` with
`CELLN_SYMPOZIUM_PROOF` pointing to this repository's
`test/integration/test-celln-real-controller.sh` and `CELLN_KIND_BIN` pointing to
Kind. The script uses a fresh Podman-backed Kind cluster and isolated kubeconfig,
builds the production controller, and records actual AgentRun status plus Celln
receipts/audits. It never uses the existing Kubernetes context and needs no model
credential. The controller and KVM dispatcher run on the host; Kind supplies the
real Kubernetes API, not nested hardware isolation.

- [Celln repository](https://github.com/sympozium-ai/celln) — the execution runtime itself: the cell/tool-lending model, hardware isolation guarantees, and `scripts/setup-host.sh` (what the installer DaemonSet runs on each node).
- [Custom Resources](custom-resources.md) — the `AgentRun.spec.backend` field.
