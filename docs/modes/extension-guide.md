# Adding a Task Mode

A "task mode" controls how an `AgentRun`'s `spec.task` is dispatched. The
`AgentRun` CR exposes a polymorphic `task` field (string OR object).
Object form is dispatched by the controller's [`taskmodes`][taskmodes]
registry to a registered [`TaskModeHandler`][taskmodehandler].

This page walks through extending Sympozium with a new mode. See
[sidecar-driven.md](sidecar-driven.md) and [harness.md](harness.md) for
in-depth walkthroughs of the two modes shipped today.

## Built-in modes

| Mode | Doc | Since |
|------|-----|-------|
| `sidecar-driven` | [sidecar-driven.md](sidecar-driven.md) | v0.10.44 |
| `harness` | [harness.md](harness.md) | v0.10.48 |

The controller ships `sidecar-driven` and `harness` by default. External contributions can register more from their own repos without forking sympozium — see [Downstream registration](#downstream-registration).

## Five-step extension guide

Each step is a small, self-contained change. You should not need to touch the controller's reconcile loop.

### 1. Implement `TaskModeHandler`

Create a new file in [`internal/controller/taskmodes/`][taskmodes]. The interface ([`taskmodes.go`][taskmodehandler]):

```go
type TaskModeHandler interface {
    Mode() string                                                            // unique identifier, e.g. "my-mode"
    Capabilities() Capabilities                                              // what the mode can honour
    Validate(task *v1alpha1.TaskSpec) error                                  // per-mode required fields
    ConfigureAgentContainer(task *v1alpha1.TaskSpec, agentEnv *[]corev1.EnvVar) error
    AdjustSidecars(task *v1alpha1.TaskSpec, sidecars []SidecarContext) ([]SidecarAdjustment, error)
}
```

- `Capabilities` declares what your mode honours from the AgentRun spec: `OutputSchema`, `ToolFilter`, `Persona`, `Subagents`, `Resume`. Be honest — a run asking for something you declare is admitted, and if you then ignore it the field is silently dropped, which is exactly what this descriptor exists to prevent. Declare `Resume: false` unless your mode can genuinely be parked mid-run.
- `Validate` runs before `Configure*`. Return a clear error so the reconcile loop can surface it on `AgentRun.status.error`.
- `ConfigureAgentContainer` mutates the agent env slice in place. Use `append`, don't replace — the central loop has already set `TASK`, `MODEL_*`, `SYSTEM_PROMPT`, etc.
- `AdjustSidecars` returns per-sidecar mutations the controller applies during container build. Return `nil, nil` if your mode doesn't touch sidecars.

Two optional interfaces cover cases the four-method contract can't:

- `TaskCapabilityReporter` — implement `TaskCapabilities(task)` when your mode's support varies from task to task rather than being fixed. `Capabilities()` then reports the ceiling (what the mode can do at best) and callers holding a `TaskSpec` use `CapabilitiesFor`, which routes through your per-task answer. `harness` uses it to read a declaration off the task, because the image an operator names is the only thing that knows what it honours — see [harness.md](harness.md).
- `AgentContainerOverrider` — implement `OverrideAgentContainer(task)` when something other than `agent-runner` should be the pod's primary process. It replaces the image, argv and env (`SetEnv` replaces by name, so it can displace `TASK`) and can attach its own volumes. The controller applies it last, after every central assignment.

!!! warning "Capability claims gate admission"
    `internal/webhook/policy_enforcer.go` denies an AgentRun that asks for a capability your descriptor lacks, and `resolveTaskModeAdjustments` repeats the check for clusters running without the webhook. Getting a claim wrong in either direction is a user-visible bug: too generous and the field is silently ignored, too stingy and working runs are rejected.

The CRD schema (`task.tool`, `task.parameters`) is intentionally loose — the mode owns its field semantics. If you need new fields, document them in your mode's docs page; the controller won't enforce them.

### 2. Register the handler

In [`internal/controller/taskmodes/register.go`][register], append the new handler to the `init()`:

```go
func init() {
    Register(NewSidecarDrivenHandler())
    Register(NewHarnessHandler())
    Register(NewMyModeHandler())
}
```

For downstream repos that want to add their own modes without forking sympozium, import `internal/controller/taskmodes` and call `Register` from that repo's `main.go` init. See [Downstream registration](#downstream-registration) below.

!!! warning "Duplicate registration panics"
    The registry panics on duplicate `Mode()` values at startup. Two handlers competing for the same mode is a programming error and must be caught in CI, not at runtime.

### 3. Update the CRD's `task` schema (only if you need new top-level fields)

The current `oneOf: [string, object]` schema constrains only `mode` (required) and allows `tool` / `parameters` (optional, open shape via `x-kubernetes-preserve-unknown-fields`).

If your mode needs additional fields (e.g. `sidecars: [name]` to bind to a specific sidecar), update all three:

- `config/crd/bases/sympozium.ai_agentruns.yaml` (source of truth)
- `charts/sympozium-crds/templates/sympozium.ai_agentruns.yaml` (Helm-synced copy)
- `charts/sympozium/crds/sympozium.ai_agentruns.yaml` (bundled CRDs copy)

Then run `make generate && make helm-sync` to keep them consistent. The CI gate `make helm-sync-check` will fail if the copies drift.

### 4. Update the agent-runner's mode list

The agent-runner hard-codes its supported `AGENT_MODE` values in [`cmd/agent-runner/main.go`][agent-runner]:

```go
var supportedAgentModes = []string{
    supportedAgentModePromptServer, // "prompt-server"
}
```

Add your mode's `AGENT_MODE` identifier here. The startup log line and the unknown-mode error message are generated from this slice, so a contributor who sets `AGENT_MODE=my-agent-mode` will see a clear error listing the supported modes.

!!! tip "When is this step required?"
    Only if your mode changes how the agent-runner behaves. If your mode only configures sidecars (and the agent-runner still runs as `prompt-server`), you can skip this step.

### 5. Tests + docs

- Write unit tests for your handler against a fake `SidecarContext` slice — see [`internal/controller/taskmodes/taskmodes_test.go`][taskmodes-tests] for the `SidecarDrivenHandler` test patterns.
- Pin your capability descriptor against what the mode *actually does*, one assertion per claim, so the two cannot drift apart silently.
- Write controller-level tests in `internal/controller/agentrun_*_test.go` for the dispatch wiring (unknown mode rejected, validation error propagated, JSON round-trips for both shapes).
- If your mode denies anything at admission, add a webhook test asserting the denial names the mode and the reason — see [`internal/webhook/task_mode_capabilities_test.go`](https://github.com/sympozium-ai/sympozium/blob/main/internal/webhook/task_mode_capabilities_test.go).
- Add a `docs/modes/<mode-name>.md` page modelled on [sidecar-driven.md](sidecar-driven.md) or [harness.md](harness.md): lifecycle, tool resolution, parameter passing, examples, links to your handler's source.

## Downstream registration

A downstream repo (a sidecar orchestrator framework, an enterprise fork) can register its own modes without forking sympozium. Import the registry and call `Register` from the consumer's `init()` or `main()`:

```go
import (
    "github.com/sympozium-ai/sympozium/internal/controller/taskmodes"
)

func init() {
    taskmodes.Register(NewMyModeHandler())
}
```

The handler's `Mode()` must not collide with any built-in. To avoid collisions, namespace your mode identifier (e.g. `acme-batch-runner` rather than `batch-runner`).

!!! note "Reconciler visibility"
    The sympozium controller will dispatch any registered mode. Make sure your handler's `Validate` is strict — an under-validated handler can ship pods with the wrong env vars.

## What stays central in the controller

The controller's reconcile loop is stable across modes. It only does:

1. `taskmodes.Get(task.Mode)` — registry lookup.
2. If not found: surface `"unknown task.mode \"<x>\"; supported modes: [<sorted>]"` on `AgentRun.status.error`, mark `phase: Failed`.
3. Otherwise: `handler.Validate` → capability check → `handler.ConfigureAgentContainer` → `handler.AdjustSidecars` → optional `OverrideAgentContainer` → render containers with the adjustments.

You should not need to touch this path. If you find yourself wanting to add a new branch to it, that's a signal that your mode would be better expressed as a handler.

## Reference

- [`internal/controller/taskmodes/taskmodes.go`][taskmodehandler] — interface definition
- [`internal/controller/taskmodes/register.go`][register] — built-in registration
- [`internal/controller/taskmodes/capabilities.go`](https://github.com/sympozium-ai/sympozium/blob/main/internal/controller/taskmodes/capabilities.go) — the capability descriptor and the admission check
- [`internal/controller/taskmodes/override.go`](https://github.com/sympozium-ai/sympozium/blob/main/internal/controller/taskmodes/override.go) — replacing the agent container
- [`internal/controller/taskmodes/sidecar_driven.go`][sidecar-driven-handler] — reference implementation
- [`internal/controller/taskmodes/harness.go`](https://github.com/sympozium-ai/sympozium/blob/main/internal/controller/taskmodes/harness.go) — reference implementation for a mode that replaces the agent container
- [`internal/controller/taskmodes/taskmodes_test.go`][taskmodes-tests] — test patterns
- [`api/v1alpha1/taskspec.go`](https://github.com/sympozium-ai/sympozium/blob/main/api/v1alpha1/taskspec.go) — polymorphic `TaskSpec` type
- [`cmd/agent-runner/main.go`][agent-runner] — `supportedAgentModes` slice
- [sidecar-driven.md](sidecar-driven.md) — reference mode write-up

[taskmodes]: https://github.com/sympozium-ai/sympozium/tree/main/internal/controller/taskmodes
[taskmodehandler]: https://github.com/sympozium-ai/sympozium/blob/main/internal/controller/taskmodes/taskmodes.go
[register]: https://github.com/sympozium-ai/sympozium/blob/main/internal/controller/taskmodes/register.go
[sidecar-driven-handler]: https://github.com/sympozium-ai/sympozium/blob/main/internal/controller/taskmodes/sidecar_driven.go
[taskmodes-tests]: https://github.com/sympozium-ai/sympozium/blob/main/internal/controller/taskmodes/taskmodes_test.go
[agent-runner]: https://github.com/sympozium-ai/sympozium/blob/main/cmd/agent-runner/main.go
