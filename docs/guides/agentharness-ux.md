# AgentHarness user experience

AgentHarness brings an existing agent harness into Sympozium without turning
the cluster into a general-purpose container launcher. It solves the gap
between a team's preferred agent loop and the platform controls needed to run
that loop safely: an isolated AgentRun, policy, per-run identity, skills,
memory, MCP, scheduling, and observable results.

The useful mental model is simple:

```text
Platform operator approves an adapter  ->  Agent owner chooses a default
                                            ->  Run operator optionally overrides it
                                            ->  Run detail proves what ran
```

The harness is only the process that drives the agent loop. An AgentRun is
still the unit of execution, lifecycle, scheduling, access control, result,
and audit. Choosing no harness preserves the built-in `agent-runner` path.

## Choose the right interaction

AgentHarness deliberately has two execution experiences. They are complementary
and must not be blended in the UI.

| Need | Use | What happens |
|---|---|---|
| A normal, continuing conversation | **Agent → Chat** with a session-capable runtime | Sympozium creates or resumes one private `HarnessSession` Deployment. The harness owns live conversation context. |
| A bounded task, automation, schedule, or auditable batch outcome | **AgentRun** | Sympozium creates an isolated Job, injects the normal run inputs/memory, records a terminal result, then the pod exits. |

For a persistent-capable Agent, **Chat** is the primary affordance. Selecting a
runtime does not silently consume a pod forever: the first **Start chat**
action creates the durable session, and subsequent visits resume it. The
**Harnesses** page remains an operator inventory and an advanced way to manage
additional sessions; it is not the required first click for ordinary chat.

The chat panel makes lifecycle visible:

- **Connected** — the controller has marked the private session ready and the
  API proxy can deliver a turn.
- **Starting** — the controller is creating or resuming the Deployment; input
  remains disabled until it is ready.
- **Stop session** — removes the private workload without deleting its
  `HarnessSession` audit record.
- **Resume chat** — requests the same approved runtime and Agent binding again.

The browser retains the latest 200 visible turns per namespace/session, so a
refresh does not look like a new conversation. This is a presentation aid on
that device, not platform memory: the private adapter owns live context and a
pod restart can lose it. Sympozium intentionally does not write message bodies
to CRDs or ConfigMaps. Cross-device/history retention needs a separately
designed, access-controlled transcript store.

## The three decisions

### 1. Approve a runtime (platform operator)

Before anyone can use a harness, an operator creates an `AgentRuntime` and a
policy that permits harness mode. The runtime pins the image, contract version,
support owner, conformance state, and adapter-declared capabilities. The policy
adds the independent security decision: which images and features may execute
in a namespace.

This is intentionally an administrator workflow, not a free-text image field
on a Run. A person starting a run should choose an approved runtime, never
paste `vendor/harness:latest` and hope it is safe.

The setup screen is currently declarative (`AgentRuntime` and
`SympoziumPolicy` resources). The product direction is an administrator view
that presents the same registry with image digest, contract, owner,
conformance, readiness, resource profile, and policy eligibility. It must not
hide the YAML or make approval implicit.

### 2. Set an Agent default (agent owner)

On **Agent detail**, the **Agent Harness Runtime** card answers: “What should
this agent normally use?”

| Choice | Meaning |
|---|---|
| **Built-in agent-runner** | Clear the Agent default. Ordinary runs use the native Sympozium loop. |
| **An approved runtime** | Store `spec.runtimeRef`. Ordinary runs inherit this adapter. |

The selector is an administrator-owned setting and is deliberately kept out of
Ensemble persona reconciliation: a persona describes an agent's role, whereas
a runtime is a deployment and trust decision.

Each runtime option should make the decision legible before saving:

- runtime name and readiness;
- immutable image digest and contract version;
- support owner and conformance status;
- capability labels marked **adapter-claimed**, not platform guarantees;
- a warning when the applicable policy will deny harness runs or accounting is
  unavailable.

An unready runtime must be visibly unavailable rather than selectable. Clearing
the choice needs copy that says it affects future runs only; historical runs
retain their recorded provenance.

### 3. Override for one run (run operator)

On **Runs → New Run**, **Harness runtime** answers a different question:
“Should this particular run depart from its agent default?”

| Choice | Result |
|---|---|
| **Use agent default** | The run stays normal, or inherits the Agent's `runtimeRef` if it has one. |
| **An approved runtime** | This run is explicitly created in harness mode with that runtime. It does not edit the Agent. |

The default must remain **Use agent default**. This makes accidental migration
impossible and preserves the existing AgentRun experience. The New Run form
should state the resolved outcome beneath the selector—for example, “This run
will use `review-agent`’s `codex-v1` default” or “This run will use the built-in
agent-runner.”

If policy makes the selected runtime ineligible, the form should block submit
and explain the remedy: select another approved runtime, use the default path,
or ask a platform operator to change the policy. Server-side admission remains
the authoritative enforcement point.

## What a completed run must explain

Run detail is the proof screen. A user should not need pod logs to answer what
happened. For every harness run, show a **Harness provenance** card with:

| Question | UI field |
|---|---|
| What was requested? | Explicit runtime override, Agent default, or no runtime. |
| What resolved? | `AgentRuntime` name and contract version. |
| What executed? | Immutable image digest actually recorded for the Job. |
| Who supports it? | Runtime support owner and conformance reference. |
| What is guaranteed? | Platform-enforced boundaries: policy, identity, mounts, NATS, lifecycle. |
| What is only claimed? | Adapter capabilities such as persona translation or native tool filtering. |
| Was usage accounted for? | Metered usage, unavailable/unknown, or a clearly labelled policy-approved unmetered exception. |

Do not display adapter claims as verification. In particular, model routing,
adapter-native tool filtering, and upstream harness behavior are claims until
the contract supplies independently verifiable evidence. The UI should use
labels such as **requested**, **resolved**, **recorded**, **platform-enforced**,
and **adapter-claimed** rather than a single ambiguous “runtime status.”

## Failure language and recovery

Harness failures need a stable category and a practical next action. The
controller remains the source of truth; the UI should group raw Kubernetes and
adapter details under these categories.

| Category | Plain-language message | Next action |
|---|---|---|
| Policy denied | “This runtime is not permitted for this AgentRun.” | Choose an eligible runtime or ask the operator to approve the policy. |
| Runtime not ready | “The selected runtime is not ready to run.” | Wait for it to become ready or select a ready runtime. |
| Image/startup | “The approved adapter could not start.” | Inspect image pull and startup diagnostics; contact the runtime owner. |
| Contract/result | “The adapter started but did not return a valid Sympozium result.” | Inspect adapter logs and run contract conformance tests. |
| MCP/sidecar | “A platform integration required by this run was unavailable.” | Inspect the named integration and retry only when it is healthy. |
| Timeout/cancelled | “The run ended before the adapter completed.” | Adjust the run limit or retry if cancellation was intentional. |
| Accounting | “The run completed but usage accounting is unavailable.” | Treat it as unmetered only if policy explicitly allows that exception. |

The current product records runtime identity, executed digest, contract,
support owner, and capabilities in run detail. The richer requested-versus-
resolved presentation, policy eligibility preview, accounting state, and
failure taxonomy above are the UX acceptance criteria still tracked in
[#360](https://github.com/sympozium-ai/sympozium/issues/360).

## Examples

### Keep a normal AgentRun

Do nothing in the runtime selectors. A familiar run remains:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: normal-review
spec:
  agentRef: review-agent
  agentId: primary
  task: Review the latest pull request.
```

This uses the built-in runner unless `review-agent.spec.runtimeRef` is set.

### Make a harness the Agent default

An operator or authorized agent owner sets the runtime once:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: review-agent
spec:
  policyRef: harness-enabled
  runtimeRef: codex-v1
```

Every ordinary AgentRun for that Agent now resolves `codex-v1`; the Run form
can still choose **Use agent default**.

### Make a one-off harness run

The New Run selector produces this explicit form:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: one-off-harness-review
spec:
  agentRef: review-agent
  agentId: primary
  task:
    mode: harness
    parameters:
      runtime: codex-v1
      prompt: Review the latest pull request.
```

This does not modify the Agent default. The webhook requires an approved
runtime, permitted policy, and a compatible adapter contract.

### Prove the full path

Use the digest-pinned reference adapter smoke manifest when validating a
cluster installation or an upgrade:

```bash
kubectl apply -f examples/harness-reference/manifests.yaml
kubectl wait --for=jsonpath='{.status.phase}'=Succeeded \
  agentrun/reference-harness-smoke -n sympozium-harness-reference --timeout=2m
```

It is a deterministic contract fixture, not a production agent harness. See
[the reference-adapter README](../../examples/harness-reference/README.md) and
[Writing a Harness Adapter](../modes/harness-adapters.md) for the contract.

## UX delivery checklist

An AgentHarness UX is complete when a user can safely make and audit these
decisions without knowing Kubernetes internals:

- [x] Choose an approved default runtime from Agent detail.
- [x] Choose an approved one-run override from New Run.
- [x] Leave the selector at a safe, compatibility-preserving default.
- [x] Inspect the resolved runtime and executed digest on Run detail.
- [x] Start or resume a private persistent chat from a session-capable Agent.
- [x] Display persistent-session connection state and provide stop/resume
  controls without turning it into an AgentRun.
- [x] Retain a bounded visible transcript on the current browser/device without
  copying message content into Kubernetes objects.
- [ ] See runtime readiness and policy eligibility before submitting.
- [ ] Distinguish requested routing, platform enforcement, and adapter claims.
- [ ] See accounting state and the reason an unmetered exception was allowed.
- [ ] Receive actionable, stable failure categories.

Those remaining items are product work, not documentation promises. They are
the recommended next UX slices for [#360](https://github.com/sympozium-ai/sympozium/issues/360).
