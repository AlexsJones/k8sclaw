# Task Mode: `harness` (External Agent Harnesses)

Sympozium can experimentally run **a conforming adapter for someone else's agent harness** as the pod's primary process
instead of `agent-runner`. It is selected per run with `task.mode: harness` on an `AgentRun`,
and the adapter arrives as an image the operator names. The backing Agent must be bound to a
`SympoziumPolicy` with `spec.harnessPolicy.enabled: true`; external runtimes fail closed by
default.

The bet is that Sympozium's own strengths — policy CRDs and the admission webhook, the
synthetic membrane, ensembles and relationships, gVisor/Kata isolation, response gates, cost
estimation, channels, schedules — do not care what binary drove the agent loop. This mode
makes that explicit: which harness runs inside the Pod becomes the operator's choice.

**BYO means a contract-compatible adapter image, not an arbitrary upstream harness image.**
Sympozium ships the seam, not the harnesses. There is no built-in harness image and no
list of blessed ones. An adapter tracks its upstream harness's release cadence — flags,
config formats, auth shapes — which is work this repo deliberately does not take on. Writing
one is [harness-adapters.md](harness-adapters.md).

This page documents the `v1alpha1` **one-shot AgentRun** contract. Continuing
chat is a separate `HarnessSession` using a session-capable `v1alpha2` runtime;
see the [AgentHarness guide](../guides/agentharness.md#persistent-interactive-sessions-experimental).

## What harness mode is (and isn't)

A harness run **keeps** everything that lives outside the agent process: the `/workspace`
PVC, the `/ipc` contract and its bridge, `/skills`, SkillPack sidecars, the MCP server
registry, response gates, retries, cost estimation, memory extraction, ensembles, schedules
and the run-detail UI. All of it works on a run it never drove, because the result contract
is unchanged.

The web New Run form can inherit an administrator-approved runtime from the
Agent or select a one-run override. Inline object-form authoring remains
available through
[`config/samples/agentrun_harness.yaml`](https://github.com/sympozium-ai/sympozium/blob/main/config/samples/agentrun_harness.yaml).
Agent runtime defaults are inherited by ordinary AgentRun entrypoints.

It deliberately does **not** give you:

- **`agent-runner`'s tool loop.** The harness brings its own, or none.
- **Any guarantee about what the image honours.** `spec.systemPrompt`, `spec.toolPolicy` and
  the rest reach the container as environment variables; whether the adapter translates them
  is the adapter's business. What it *claims* to translate is `parameters.capabilities`, and
  that claim is what admission checks.
- **Sympozium-built images.** `parameters.image` is required. There is no default.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumPolicy
metadata:
  name: byo-enabled
spec:
  harnessPolicy:
    enabled: true
  imagePolicy:
    allowedRegistries:
      - ghcr.io/acme/my-harness@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
---
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: my-agent
spec:
  policyRef: byo-enabled
  agents:
    default:
      model: deepseek-chat
  authRefs:
    - provider: deepseek
      secret: my-deepseek-key
---
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: review-the-pr
spec:
  agentRef: my-agent
  agentId: primary
  task:
    mode: harness
    parameters:
      image: ghcr.io/acme/my-harness@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      prompt: "Review the latest pull request and comment on code quality"
      capabilities: "persona"
  systemPrompt: "You are a careful reviewer."
  model:
    provider: deepseek
    model: deepseek-chat
    authSecretRef: my-deepseek-key
  timeout: "10m"
```

### Parameters

| Parameter | Required | Meaning |
|---|---|---|
| `image` | one of `image`/`runtime` | The adapter image that becomes the pod's primary process. Must be **digest-pinned** (`@sha256:…`); a mutable tag is rejected. Bounded by `SympoziumPolicy.imagePolicy.allowedRegistries`. |
| `runtime` | one of `image`/`runtime` | The name of an admin-approved [`AgentRuntime`](#the-agentruntime-resource) in the run's namespace. The runtime supplies the digest-pinned image and capabilities. Mutually exclusive with `image`. |
| `prompt` | yes | The task text. Object-form tasks carry no top-level prompt field, so harness mode takes it from here and sets `TASK` from it. |
| `capabilities` | no | Comma-separated list of what the image honours. Empty means it claims nothing. Ignored when `runtime` is set — the runtime's own declaration wins. |
| `args` | no | Extra argv, as a **JSON array string** (`'["--profile","headless"]'`). `parameters` is `map[string]string`, so an array has to travel encoded. |

The image keeps its own `ENTRYPOINT`. Sympozium has no argv to impose on a binary it did not
build; only `args` is passed through.

## Complementary to the Celln backend

Harness mode and [`backend: celln`](../concepts/celln-backend.md) are the same idea applied
at two different layers, and they compose with different things:

| | `backend: celln` | `task.mode: harness` |
|---|---|---|
| Selected by | `spec.backend` | `spec.task.mode` |
| Changes | **where** the run executes | **what process runs** inside the Job |
| Composes with | nothing — it bypasses the pod entirely | `agentSandbox`, gates, ensembles, MCP, skills, memory |
| Runtime owned in | [the celln repo](https://github.com/sympozium-ai/celln) | the adapter's repo |
| Footprint when unused | zero (`celln.enabled=false`) | zero (no image, no chart resource) |

Two consequences worth knowing:

- **`mode: harness` + `backend: celln` is rejected at admission.** Celln dispatches the task
  string to its router and never builds a pod, so there is no agent container to replace.
  Admitting it would run the task with the harness image silently ignored, which is exactly
  the failure the [capability descriptor](#capability-descriptors) exists to prevent.
- **`agentSandbox` works normally.** It builds its pod through `buildAgentPodTemplate`, which
  wraps `buildContainers`, so task-mode dispatch applies and a harness run gets kernel-level
  isolation like any other.

Harness mode requires an explicit policy opt-in because it changes the trusted primary
process and exposes the run's model and MCP credentials to that process. Celln's enable flag
instead controls deployment of its privileged installer DaemonSet; the two gates protect
different boundaries.

## What Sympozium supplies

Nothing new was built for this. The mode reuses what the platform already had:

| Input | Mechanism |
|---|---|
| Task text | `TASK` env, or `/ipc/input/task.json` |
| Model credentials | per-key `SecretKeyRef` from the `allowedAuthSecretKeys` allowlist — never `EnvFrom` |
| Model routing | `MODEL_NAME`, `MODEL_BASE_URL`, `MODEL_PROVIDER` |
| Persona | `SYSTEM_PROMPT` |
| Tool policy | `TOOL_POLICY_ALLOW` / `TOOL_POLICY_DENY` — only reaches a run whose image declares `toolFilter` |
| MCP servers | the registry ConfigMap the controller already generates, mounted at `MCP_CONFIG_PATH`, plus one `MCP_AUTH_<SERVER>` per authenticated server. A replaced agent container gets the **JSON** rendering (`mcp-servers.json`) because its adapter is a shell script with `jq`; the mcp-bridge sidecar keeps reading the YAML one |
| Skills | `/skills/`, as today. SkillPack **tools** arrive through the skill tool server — see [SkillPack tools](#skillpack-tools). The raw tool manifest is deliberately *not* mounted: it lists tools the policy denies, and a harness has no way to dispatch them anyway |
| Sandbox / pod security | unchanged — non-root, `readOnlyRootFilesystem`, `drop: [ALL]`, RuntimeDefault seccomp |
| Writable `$HOME` | an `emptyDir` at `/home/agent` (volume `harness-home`), with `HOME`, `XDG_CONFIG_HOME` and `XDG_CACHE_HOME` pointed at it — **not** a relaxed security context |
| Adapter contract | `SYMPOZIUM_HARNESS_CONTRACT_VERSION=v1alpha1`; reject versions the adapter does not understand |
| Working directory | `/workspace`, regardless of the image's own `WORKDIR` |
| Result | `/ipc/output/result.json` plus the `__SYMPOZIUM_RESULT__` stdout marker |
| `/ipc` | **only** `input/` (read-only) and `output/` — see [/ipc is not a shared surface](#ipc-is-not-a-shared-surface) |

The exact contract, in both directions, is [harness-adapters.md](harness-adapters.md).

## The part that's easy to miss: `spec.model` is not guaranteed

**Sympozium cannot verify that a harness routes to the model your `AgentRun` names.** It sets
`MODEL_NAME`, `MODEL_BASE_URL` and `MODEL_PROVIDER`, and injects the provider credential
per-key from the `allowedAuthSecretKeys` allowlist. Whether the adapter maps those onto
whatever its harness reads — and many harnesses have their own opinionated provider
resolution, config files and CLI logins — is the adapter's business.

A harness with its own credential baked into the image, or one that silently prefers an
ambient config over `MODEL_BASE_URL`, will run against a model your manifest never named,
and the run will succeed. Read the adapter's README before pointing production traffic at it.

This is deliberately **not** a capability. `spec.model` is required on every AgentRun, so a
`model` capability would be requested by every run and declared by every image — noise, not
signal. The honest framing is that model routing is a property of the adapter you chose.

## Capability descriptors

Nothing used to stop an AgentRun asking a mode for something it could not honour — the field
was accepted and then quietly dropped. Every `TaskModeHandler` now carries a descriptor
saying what it supports, and the mismatch is rejected before the run exists:

```
task.mode "harness" does not support [toolFilter] requested by this AgentRun
(mode supports: [persona])
```

| Capability | Meaning |
|---|---|
| `outputSchema` | JSON-Schema structured output |
| `toolFilter` | honours `spec.toolPolicy` |
| `persona` | honours `spec.systemPrompt` |
| `subagents` | can spawn child runs |
| `resume` | can be parked and resumed mid-run |

For harness mode the descriptor is **the operator's own declaration**. Sympozium did not
build the image and cannot inspect it, so `parameters.capabilities` is the whole basis for
admission, and an image that declares nothing gets nothing. Declaring more than the adapter
actually translates is how you get the silent drop back — a claim here is a promise the
image keeps.

Checked in two places, because they fail in different circumstances:

- **Admission** (`internal/webhook/policy_enforcer.go`) — the good error, at `kubectl apply`
  time. A mode with no handler registered in the webhook binary passes here; the
  downstream-registration path in [extension-guide.md](extension-guide.md) depends on that.
- **The controller** (`resolveTaskModeAdjustments`) — the webhook is a separate, optional
  deployment, so the check repeats there. A cluster without the webhook still fails the run
  loudly rather than degrading in silence.

Only what an AgentRun states unambiguously is counted as a request: `spec.systemPrompt` →
`persona`, a non-empty `spec.toolPolicy` → `toolFilter`, and `spec.toolPolicy.allow` naming
`spawn_subagents` or `delegate_to_persona` → `subagents`. `outputSchema` and `resume` are not
expressible on an AgentRun today — the first is requested per call over `/ipc/prompts/`, the
second is decided by the gate machinery — so neither is checked yet.

Harness mode is deliberately **absent** from `GET /api/v1/capabilities`. That endpoint
reports *environmental* availability — is the Celln router dialable, is the Sandbox CRD
present. Harness mode has no daemon, CRD or node label to probe; whether a given run can work
depends on its own image and registry policy, and that already surfaces at admission.

!!! warning "This applies retroactively to `sidecar-driven`"
    `sidecar-driven` declares `toolFilter: false`, because `runPromptServer` passes
    `Tools: nil` — in prompt-server mode the LLM answers individual prompts and has no tool
    surface at all. A `sidecar-driven` AgentRun that also set `spec.toolPolicy` was always a
    no-op; it is now **denied at admission**. Remove the `toolPolicy` block: nothing is lost,
    because nothing was ever enforced.

## `/ipc` is not a shared surface

The agent container is normally given the whole `/ipc` volume, and the bridge
watches eight directories under it. Each turns a dropped JSON file into a
control-plane action:

| Directory | Effect of a file appearing there |
|---|---|
| `spawn/` | creates sub-agent runs |
| `tools/` | exec request — a skill sidecar runs it |
| `messages/` | outbound channel message (Slack, WhatsApp, Telegram) |
| `schedules/` | creates a schedule |
| `prompts/`, `context/` | sidecar-initiated LLM prompts |
| `input/` | the task (read) |
| `output/` | the run result |

That is safe for `agent-runner`, which is Sympozium's own code and writes those
files only for tools it chose to register — policy and writer are one trusted
process, which is why the skill sidecars execute what arrives without checking
authority. Harness mode separates them.

So a harness gets two `subPath` mounts and nothing else: `/ipc/input`
(read-only) and `/ipc/output`. The other six directories are **not in its mount
namespace** — not filtered, not checked, absent. A harness cannot spawn a child
run or message a channel by writing a file, whatever its capability descriptor
claims, because there is nowhere to write it.

`agent-runner` is unaffected and still mounts the volume root.

## SkillPack tools

Removing `/ipc/tools` would leave a harness unable to use SkillPack tools at
all, so they come back through a gate instead. When a harness run has sidecars
that declare tools, the pod gains a **`skill-tools`** container: Sympozium's own
code, running the `mcp-bridge` image, listening on `127.0.0.1:8771`.

It runs as a **native sidecar** — an init container with `restartPolicy: Always`
and a startup probe — so the kubelet has it listening before the harness
container starts. Ordinary containers start concurrently, and an adapter whose
MCP client fails loudly on an unreachable server at boot would otherwise lose
that race intermittently.

It is an ordinary MCP server, and it appears in the MCP registry the harness
already reads as one more entry named `sympozium-skills`.

Operator-configured remote MCP servers are deliberately different: their
registries can contain arbitrary endpoints, headers, and Secret-derived
credentials. Harness AgentRuns that inherit `Agent.spec.mcpServers` are
rejected before pod creation. External adapters receive only the trusted
loopback SkillPack registry; remote MCP remains unavailable until Sympozium
provides an equally trusted local mediator.

!!! warning "`sympozium*` is a reserved name prefix"
    A harness namespaces tools by server name, so an operator server also called
    `sympozium-skills` would shadow this one — and the agent's SkillPack tool
    calls would go somewhere with no policy check in between. Any `mcpServers`
    entry whose name begins with `sympozium` (in any case) is therefore
    **rejected**: at admission by the webhook, and again in the controller,
    where it fails the run rather than building a registry with a shadowed
    entry. The prefix, not one fixed name, so a future internal server needs no
    new rule and no existing manifest breaks when one is added. **An adapter needs no
code for this** — it is one more server in a list it already translates. Tools
appear to the model namespaced by that server, e.g. `mcp__sympozium-skills__kubectl_get`.

What it changes is where the policy is enforced:

- It holds `spec.toolPolicy`, given to it by the controller from the CR. The
  agent never touches it.
- `tools/list` returns only the permitted tools.
- `tools/call` applies the same decision again, because a client may call a name
  it was never offered.
- It is the only thing in the pod that turns a harness request into an exec
  request, and it builds that request with the same `pkg/sidecartools` code
  `agent-runner` uses — so the same tool produces the same argv either way.

For SkillPack tools, then, `spec.toolPolicy` is **enforced**, not advertised, and
it does not depend on the adapter honouring anything.

!!! warning "`toolFilter` still means the harness's own tools"
    `spec.toolPolicy` covers the harness's built-in tools too, and only the
    adapter can filter those. So a run that sets `spec.toolPolicy` still needs
    an image declaring `toolFilter`, even when every tool it wants restricted is
    a SkillPack tool. The skill tool server makes the claim true for its half; it
    does not replace it.

## Trust model

Celln draws a hard line between an attested host binary and agent-authored code. Harness mode
has a direct analogue, and the line falls in the same place:

- **The image is infrastructure.** It is operator-chosen and registry-gated — an agent cannot
  name its own harness, because `parameters.image` is a field on the AgentRun the operator
  authored, checked against `SympoziumPolicy.imagePolicy.allowedRegistries` both at admission
  and again in the controller. See [Bounding which harnesses may run](#bounding-which-harnesses-may-run)
  for what that list does and does not promise.
- **Everything the harness writes is agent lane.** What lands in `/workspace` and
  `/ipc/output` was produced by an LLM and is treated as adversarial, exactly as for
  `agent-runner`. Validate anything a harness writes before acting on it. The narrowed `/ipc`
  mount is what keeps "adversarial" from meaning "can create sub-agent runs".
- **The harness has no Kubernetes identity.** Every AgentRun gets a unique
  ServiceAccount, but automatic token mounting is disabled. A pod-bound token is
  projected only into trusted SkillPack sidecars that explicitly declare RBAC;
  it is not mounted into the harness or IPC bridge. Concurrent runs therefore do
  not union their Kubernetes permissions.
- **Pod-wide host privileges are incompatible.** Harness admission rejects
  lifecycle RBAC and SkillPacks with host access, host networking, host PID, or
  privileged execution until those paths have a separately mediated boundary.

The result payload is assembled with `jq --arg`, so harness output is encoded as a JSON
string and cannot forge a result structure. It cannot forge the marker either: an agent that
prints `__SYMPOZIUM_RESULT__` mid-run is overtaken by the real one, because the controller
parses the **last** marker in the log.

### Bounding which harnesses may run

`SympoziumPolicy.imagePolicy.allowedRegistries` is the control, and it is worth reading
literally, because in harness mode the image is not an accessory to the run — it *is* the
agent process.

**It matches by string prefix.** There is no parsing into registry, repository and tag, so the
granularity is whatever you write:

| Entry | Admits |
|---|---|
| `docker.io/` | every image on Docker Hub |
| `ghcr.io/acme/` | one organisation |
| `ghcr.io/acme/harness:v1` | one image and tag |
| `ghcr.io/acme/harness@sha256:…` | one exact digest |

Two consequences, because the field looks stricter than it is:

- **There is no component boundary.** `ghcr.io/acme` without the trailing slash also admits
  `ghcr.io/acmecorp-evil/…`, and `:v1` also admits `:v1-evil`. End each entry at a `/`, or at
  a complete tag or digest.
- **A short name carries no registry.** `docker.io/library/` does not match `alpine:3.20`.
  That direction fails closed.

The harness gate and the image allowlist are separate controls:

| Situation | Effect |
|---|---|
| The Agent has no `policyRef` | Harness execution is denied. |
| The bound policy omits `harnessPolicy` or sets `enabled: false` | Harness execution is denied. |
| `harnessPolicy.enabled: true`, but no `imagePolicy` | Harness execution is enabled with no image restriction. This is experimental-only and not recommended for shared clusters. |
| `harnessPolicy.enabled: true` and `allowedRegistries` is empty | Harness execution is enabled with no image restriction. |

A third, independent control always applies to the image itself: **it must be digest-pinned.**
A tag-only or otherwise unpinned reference is rejected regardless of the allowlist, because
`v1` can be retagged under the operator and the harness image is the pod's primary process.
The digest Sympozium admitted is recorded on `status.harnessImageDigest`, so run detail and
audit can show exactly which artifact executed rather than which tag was requested.

The check runs in **both** the admission webhook and the controller. The webhook gives the
better error, at `kubectl apply` time; the controller repeats it because the webhook is a
separate, optional deployment, and a cluster without it would otherwise have no bound at all
on which external harness executes. A rejection there fails the run with the image named on
`status.error`, and no Job is created.

## The `AgentRuntime` resource

Inline `parameters.image` + `parameters.capabilities` on every run is the experimental path.
The administrator-owned form is an **`AgentRuntime`**: the platform team writes one resource
that pins the digest, declares the contract version and capabilities, records a support owner
and conformance status, and a run references it by name instead of repeating those claims.

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: AgentRuntime
metadata:
  name: codex-v1
spec:
  image: ghcr.io/acme/codex-adapter@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
  contractVersion: v1alpha1
  capabilities: [persona]
  model:
    provider: anthropic
    model: claude-sonnet-4-6
    authSecretRef: acme-codex-key
  supportOwner: platform-ai@acme.example
```

The controller validates the runtime and records `status.resolvedImageDigest` plus a `Ready`
condition. The same rules as inline harness mode apply: the image must be digest-pinned, and
`outputSchema` / `subagents` / `resume` are rejected because no external runtime can implement
them safely yet.

A run references a runtime by name instead of repeating its image and capability claims:

```yaml
task:
  mode: harness
  parameters:
    runtime: codex-v1
    prompt: "Review the latest pull request"
```

Admission and the controller resolve the runtime into its image and capabilities before any
other check, so the image allowlist, digest recording, and capability gating apply to the
resolved values exactly as they do for an inline image. A runtime that does not exist or is
not `Ready` is rejected at admission.

An Agent can bind a default runtime so its ordinary string-form runs inherit it — including
channel, schedule, API, and UI runs — without authoring an object-form task:

```yaml
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: my-agent
spec:
  runtimeRef: codex-v1
  # ...
```

The controller converts the string task into harness mode, preserves the original text as the
adapter prompt, and resolves the runtime from the Agent. An explicit object-form harness task
with an `image` or `runtime` overrides the Agent default.

Agent-level runtime selection is not expressible through an Ensemble persona because it is an
administrator decision rather than a persona concept. Set it out of band on the generated
Agent; Ensemble reconciliation preserves that administrator-owned field.

## Graceful degradation

| Scenario | Behavior |
|---|---|
| `parameters.image` missing | Run fails validation in the controller with the missing-parameter name; no pod is created. |
| Image outside `allowedRegistries` | **Denied at admission**, naming the image; and again in the controller, which fails the run without creating a Job. |
| Image not digest-pinned (`:tag`, bare name, truncated digest) | **Denied at admission and by the controller**, naming the digest-pinning requirement; no pod is created. |
| No explicit `harnessPolicy.enabled: true` | **Denied at admission and by the controller**; no pod is created. |
| Explicit opt-in but no image allowlist | Admitted with no image restriction; use only for controlled experimentation. |
| A capability is requested but not declared | **Denied at admission**, naming the mode and the capability. |
| `backend: celln` | **Denied at admission**: celln never builds the container harness mode replaces. |
| Lifecycle RBAC or a host-access/privileged SkillPack | **Denied at admission and by the controller**; these surfaces are not isolated from an external primary container. |
| Image declares a capability it does not honour | Admitted and run. The field is silently dropped — this is the one failure the descriptor cannot catch, and why a claim is a promise. |
| Image ignores the result contract | Run fails with no result rather than hanging: no marker, no `/ipc/output/result.json`, and the container exit drives the phase. |
| Harness writes to `/ipc/spawn`, `/ipc/tools`, … | Not possible: those paths are not in its mount namespace. A write fails with "no such file or directory". |
| SkillPack attached, no tools declared | No `skill-tools` container. Nothing to serve. |
| `tools/call` for a tool the policy denies | Refused by the skill tool server, and **no exec request is written** — the sidecar never sees it. |
| Image not pullable | Ordinary Kubernetes `ImagePullBackOff` on the agent container; the run times out per `spec.timeout`. |

## Operating notes

### Cost fidelity

External harnesses report token usage differently or not at all. Adapters omit `metrics` from
the result payload rather than reporting zeros, so `status.tokenUsage` stays **absent** —
matching the existing `costEstimate` convention of absent-not-zero. A run through a harness
will not show a token count until its adapter can source a real one.

### Resources

The agent container keeps its default requests (250m / 512Mi) and limits (1 CPU / 1Gi).
Node-based harnesses sit close to that memory limit; raise it on the Agent if one is
OOM-killed.

### Unsupported controls

Harness declarations are currently limited to `persona` and `toolFilter`. Claims for
`outputSchema`, `subagents`, or `resume` are rejected because Sympozium has no mediated
surface for an external process to implement them safely. `mode: server`, `dryRun`, and
`canaryMode` are also rejected rather than being silently handled by logic that only exists
inside `agent-runner`.

## See Also

- [Writing a harness adapter](harness-adapters.md) — the contract, in both directions
- [AgentHarness guide](../guides/agentharness.md) — approved runtimes and persistent sessions
- [Celln Backend](../concepts/celln-backend.md) — the same division of labour, at the
  execution-substrate layer
- [Adding a Task Mode](extension-guide.md) — the seam this mode is built on
- `internal/controller/taskmodes/harness.go` — the handler
- `internal/controller/taskmodes/capabilities.go` — the descriptor and the admission check
- `internal/controller/taskmodes/override.go` — how a mode replaces the agent container
