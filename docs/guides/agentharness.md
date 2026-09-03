# AgentHarness: bring your agent into Sympozium

## The problem

Sympozium already runs agents as isolated Kubernetes Jobs, but the agent loop
was traditionally tied to Sympozium's built-in `agent-runner`. If your team
already uses Codex, Claude Code, Goose, DSH, or another harness, you had to
choose between that harness and Sympozium's policy, memory, skills, MCP,
observability, and AgentRun lifecycle.

<figure markdown="span">
  <img src="../assets/agentharness/overview.svg" alt="AgentHarness overview: an approved external harness runs inside an isolated AgentRun pod while Sympozium retains policy, identity, gated skills and MCP, memory, events, results, and observability." width="1200">
  <figcaption>AgentHarness changes the agent loop, not the platform boundary around the run.</figcaption>
</figure>

## What AgentHarness unlocks

AgentHarness lets an approved external adapter become the primary process in a
normal AgentRun. You can bring an existing harness into Sympozium while the
platform still controls:

- admission policy and approved runtime selection;
- per-run Kubernetes identity, token, filesystem, and NATS boundaries;
- SkillPack sidecars, MCP servers, memory, channels, schedules, and ensembles;
- result reporting, timeouts, accounting, and observability.

Normal AgentRuns are not replaced. Harness mode changes only the process that
drives the agent loop; the surrounding AgentRun machinery remains the same.

## Persistent interactive sessions (experimental)

Most harness work remains deliberately one-shot: an `AgentRun` creates an
isolated Job, records a terminal result, and exits. A `HarnessSession` is the
separate opt-in path for a user who needs to keep an approved harness pod
alive and interact with it over several turns. It does **not** keep a completed
`AgentRun` alive or change its lifecycle.

The feature is available only to an `AgentRuntime` that explicitly declares
the `v1alpha2` `openai-chat` contract. The controller then creates a private
Deployment and ClusterIP Service, waits for the adapter's `/healthz`, and the
authenticated Sympozium API proxies chat requests. The browser never receives
a pod IP, service URL, Kubernetes credential, or direct NATS access.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: HarnessSession
metadata:
  name: analyst-session
  namespace: default
spec:
  agentRef: analyst
  runtimeRef: pi-session-v0-84-4
  desiredState: running
```

For a person using the Agent, the primary route is **Agents → _your Agent_ →
Chat**. Creating an Agent with a session-capable runtime automatically creates
and starts its deterministic `<agent>-chat` session. The page shows **Starting**
until it reaches `Ready`, then **Open chat** enters the conversation. Existing
Agents can also create or resume a session from Chat. The **Harnesses** page is
the operator-facing inventory for inspecting approved runtimes and managing
additional sessions, not the normal way to begin a conversation.

The Chat panel reports whether the session is connected, starting, or stopped.
Stopping removes the private workload while preserving the `HarnessSession`
record; **Resume chat** starts a new private workload using the same approved
runtime and Agent credential allowlist. The visible transcript is retained on
the current browser/device (bounded to the latest 200 turns) so a refresh does
not make the conversation appear empty. It is deliberately **not** copied into
CR status, ConfigMaps, or the browser-to-pod API.

The maintained Pi and Hermes adapters implement the experimental `v1alpha2`
session contract. They serialize turns and store adapter-owned session state on
the `HarnessSession` PVC, so the conversation survives pod restart and explicit
stop/resume. Both continue to disable tools and skills. Treat a session as a
bounded interactive workspace, not durable platform Agent memory or a general
exposed OpenAI gateway.

Each authenticated chat receives an `X-Sympozium-Request-ID`. The session
status records request/active/error counts, the latest request lifecycle and
timestamps, and `lastActivityTime`. Disconnecting the client cancels the
upstream request and the adapter terminates its active model subprocess. Usage
accounting is explicitly reported as `unavailable` until an adapter supplies
trustworthy usage; missing metrics are never presented as zero.

Set `spec.idleTimeout` to a positive duration such as `30m` to stop idle
compute. Activity is measured at the API proxy, and an in-flight request blocks
idle shutdown. On timeout, the Deployment, Service, and NetworkPolicy are
removed while the session CR and PVC remain available for Resume.

For operators, the trust boundary is unchanged:

- only a Ready, digest-pinned approved runtime can be selected;
- the backing Agent must explicitly allow the model credential the runtime
  requests;
- model secret keys are injected individually from the existing allowlist;
- remote MCP servers and their credentials are rejected for external adapters;
  SkillPack tools use the bounded, policy-enforcing loopback MCP server;
- the session pod has no service-account token, privilege escalation, or
  writable root filesystem; and
- only Sympozium's API server proxies the fixed `/v1/chat/completions` target,
  with bounded requests/responses and redirects refused.

<figure markdown="span">
  <img src="../assets/agentharness/lifecycle.svg" alt="AgentHarness lifecycle: approve a digest-pinned AgentRuntime, select it on an AgentRun, execute it in an isolated pod, then validate and record the result." width="1200">
  <figcaption>Approval precedes selection; every run still finishes through the normal AgentRun record.</figcaption>
</figure>

<details>
<summary>Animated walkthroughs</summary>

<figure markdown="span">
  <img src="../assets/agentharness/boundary-focus.gif" alt="Animated AgentHarness trust-boundary diagram highlighting operator control, the isolated external harness, and the platform-managed outputs." width="1200">
  <figcaption>Focus on the three ownership zones: operator approval, adapter execution, and platform-managed outputs.</figcaption>
</figure>

<figure markdown="span">
  <img src="../assets/agentharness/lifecycle-focus.gif" alt="Animated four-stage AgentHarness lifecycle, highlighting approval, selection, execution, and recorded result in sequence." width="1200">
  <figcaption>The lifecycle animation follows the order in which a harness run is made safe and auditable.</figcaption>
</figure>

</details>

Persistent Agent memory remains platform-managed: Sympozium mounts and updates
it through its normal result-extraction path. An adapter must not assume the
`agent-runner` conversation-memory or thinking controls apply; `useContext:
false` and `model.thinking` are rejected for harness runs until the adapter
contract defines mediated equivalents. The historical/default
`useContext: true` remains accepted because it requests no adapter behavior.

!!! warning "This is an adapter boundary, not arbitrary image execution"
    An operator approves a contract-compatible adapter image, normally by
    immutable digest. Sympozium does not bless or maintain every upstream
    harness image, and a user cannot bypass policy by naming an arbitrary
    upstream image in an AgentRun.

## Who does what

| Role | Responsibility |
|---|---|
| Platform operator | Approves `AgentRuntime` resources, image digests, policies, credentials, resource limits, and support status. |
| Agent author | Selects an approved runtime on the `Agent`; configures the model, skills, and policy. |
| Adapter maintainer | Wraps a harness, implements the versioned contract, publishes images, and runs conformance tests. |
| End user | Creates a normal AgentRun and inspects the resolved runtime and result. |

## Quickstart

### Built-in persistent catalog

The Helm chart installs the maintained experimental Pi and Hermes persistent runtimes in
the chart namespace by default, just as it installs the Ensemble catalog. In
the web UI, switch to that namespace (normally `sympozium-system`) and open
**Agents → Harnesses**. The `harness-examples` policy permits only those exact
digest-pinned images; select that policy and either runtime explicitly on an
Agent before running it. No Agent or credential is created by the catalog.

The default interactive catalog contains only session-capable runtimes. The
one-shot Pi and Hermes adapters remain available as explicit AgentRun examples
but are not installed or shown as default interactive harnesses. Current
session adapters deliberately provide no
MCP/SkillPack tools, native tools, persona mapping, subagents, or trusted usage
metrics. See the [adapter conformance report](https://github.com/sympozium-ai/harness-adapters/blob/main/docs/conformance.md)
before enabling them.

### Bring your own adapter

The checked-in examples use placeholder image digests and credentials. Replace
those values with an adapter image and Secret that your operator has approved.

### 1. Enable the policy

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumPolicy
metadata:
  name: harness-enabled
  namespace: default
spec:
  harnessPolicy:
    enabled: true
    # The reference adapter reports no token usage. Production adapters should
    # emit real metrics instead of enabling this exception.
    allowUnmetered: true
  imagePolicy:
    allowedRegistries:
      - ghcr.io/acme/codex-adapter@sha256:<64-hex-digest>
```

Apply it with `kubectl apply -f policy.yaml`. Harness execution is denied when
the backing Agent has no policy or the policy does not explicitly enable it.

### 2. Register an approved runtime

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRuntime
metadata:
  name: codex-v1
  namespace: default
spec:
  image: ghcr.io/acme/codex-adapter@sha256:<64-hex-digest>
  contractVersion: v1alpha1
  capabilities:
    - persona
  supportOwner: platform-ai@example.com
  conformance:
    status: conformant
    owner: platform-ai
```

An `AgentRuntime` is administrator-owned. Its digest, contract version,
capabilities, support owner, conformance state, model mapping, and resource
settings describe what the platform is willing to run.

### 2a. Verify it in the Harnesses registry

After applying the runtime and policy, wait for the runtime to become ready:

```bash
kubectl -n default get agentruntime codex-v1 \
  -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}{"\\n"}'
```

It must report `True` before it can be bound to a run. In the web UI, open
**Agents → Harnesses**. This is the administrator-facing inventory of approved
adapters: confirm the runtime is ready, inspect the resolved immutable digest,
contract, support owner, and conformance status, then select it from the
Agent's **Harness** tab. The registry is deliberately read-only; approval is
made by applying or updating the `AgentRuntime` and `SympoziumPolicy`, not by
pasting an image into the UI.

### 3. Select it on an Agent

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: review-agent
  namespace: default
spec:
  policyRef: harness-enabled
  runtimeRef: codex-v1
  agents:
    default:
      model: claude-sonnet
```

With `runtimeRef`, the runtime is inherited by normal entrypoints. Users do
not need to repeat an object-form harness task for every run.

The web UI exposes the same administrator action on the Agent detail page:
choose **Agent Harness Runtime**, select an approved runtime, or select
**Built-in agent-runner** to clear it. This is distinct from persona settings
because Ensemble reconciliation intentionally preserves an administrator-set
runtime reference.

For a one-off override, open **Runs → New Run** and choose **Harness runtime**.
Leaving that field on **Use agent default** preserves normal AgentRun behavior
and inherits the Agent's `runtimeRef`; choosing a runtime creates an explicit
harness AgentRun for that invocation only.

### 4. Create a normal AgentRun

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: review-example
  namespace: default
spec:
  agentRef: review-agent
  agentId: primary
  task: Review the latest pull request and report actionable findings.
```

The controller resolves the Agent's runtime, creates the ordinary isolated
AgentRun Job, and records the result. Inspect it with:

```bash
kubectl -n default get agentrun review-example -o yaml
kubectl -n default get job -l sympozium.ai/agent-run=review-example
```

For a complete persistent Pi/Hermes catalog and session example, see
[`config/samples/harnesssession_persistent.yaml`](https://github.com/sympozium-ai/sympozium/blob/main/config/samples/harnesssession_persistent.yaml).
For inline one-shot object-form authoring, see
[`config/samples/agentrun_harness.yaml`](https://github.com/sympozium-ai/sympozium/blob/main/config/samples/agentrun_harness.yaml).
For the complete runtime resource, see
[`config/samples/agentruntime_sample.yaml`](https://github.com/sympozium-ai/sympozium/blob/main/config/samples/agentruntime_sample.yaml).
For the user journey, control meanings, proof shown after a run, and UX
delivery criteria, see [AgentHarness user experience](agentharness-ux.md).

## Trust and capability language

AgentRuntime capabilities are declarations by the adapter maintainer. They are
not proof that an image implements a behavior. Documentation and UI should
always distinguish:

- **Platform-enforced:** policy, digest admission, run identity, NATS subjects,
  mounts, SkillPack permissions, and lifecycle behavior.
- **Adapter-claimed:** persona translation, native tool filtering, model
  configuration, or other behavior declared by the runtime.
- **Unavailable:** a capability that the current adapter or platform cannot
  mediate, such as unsupported subagents or resume semantics.

The adapter receives the versioned contract and must emit the structured
Sympozium result protocol. The full adapter contract is documented in
[`Writing a Harness Adapter`](../modes/harness-adapters.md). The maintained
adapter program, conformance expectations, and experimental Pi/Hermes plans
live in [sympozium-ai/harness-adapters](https://github.com/sympozium-ai/harness-adapters).

## What to inspect after a run

Run detail shows the runtime identity, executed digest, contract version,
support owner, and adapter-claimed capabilities. Operators should be able to
answer four questions without reading pod internals:

1. Which AgentRuntime was requested or inherited?
2. Which immutable image digest actually ran?
3. Which capabilities were platform-enforced versus adapter-claimed?
4. Did the run fail at policy, image pull, startup, MCP, result validation,
   timeout/cancellation, or metrics/accounting?

`AgentRun.status` and controller logs remain the source of truth for low-level
pod and admission diagnostics.

## Support boundaries

Sympozium maintains the contract, platform integration, security boundary, and
reference/conformance tooling. Adapter maintainers maintain their harness
integration, upstream compatibility, image vulnerabilities, and support tier.
An operator should record the owner and conformance URL on `AgentRuntime` and
should pin production runs to a reviewed digest.

## Current status and feedback

The security and execution foundation is tracked in
[#349](https://github.com/sympozium-ai/sympozium/issues/349). UX, API, and docs
work is tracked in [#360](https://github.com/sympozium-ai/sympozium/issues/360).
See the [AgentHarness discussion](https://github.com/sympozium-ai/sympozium/discussions/366)
for the current merged state and open design questions. Adapter maintainers
and operators are invited to share use cases, contract proposals, and failure
diagnostics there.
