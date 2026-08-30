# Writing Orchestrator Sidecars

!!! warning "Trust model — read this before authoring or installing one"
    An orchestrator sidecar **fully controls how the AgentRun executes**. It owns the workflow loop, decides when to call the LLM, decides what to persist, and writes the run-completion signal. If it declares Kubernetes RBAC, it receives the AgentRun's unique, short-lived projected identity; otherwise no Kubernetes token is mounted. A malicious or buggy orchestrator with declared credentials can:

    - Issue arbitrary LLM calls billed to the AgentRun.
    - Read, exfiltrate, or tamper with anything the sidecar has access to (secrets, mounted volumes, network).
    - Force the run into any terminal state, including fabricating a successful result.
    - Call the LLM in any pattern it likes — including patterns that bypass the safety guarantees of the run-level `useContext` policy.

    This is a fundamentally broader trust grant than a [tool-sidecar](writing-tool-sidecars.md), whose actions are gated by the LLM's tool calls, the SympoziumPolicy, and the dispatch admission webhook. **Only run orchestrator sidecars you have written yourself, or that come from a source you fully trust and have audited.** Treat the SkillPack image reference for an orchestrator sidecar with the same scrutiny you would apply to a CI runner image that holds production deploy credentials.

This guide explains how to build a sidecar that runs in [sidecar-driven mode](../modes/sidecar-driven.md). Unlike a [tool-sidecar](writing-tool-sidecars.md), an orchestrator sidecar drives the workflow end-to-end and calls the LLM as a sub-call.

If you just need to give an agent access to a CLI binary, you don't want this — see [Writing Tool Sidecars](writing-tool-sidecars.md) for the agent-driven mode pattern.

---

## What changes vs. tool-sidecars

| | Tool-sidecar (agent-driven) | Orchestrator sidecar (sidecar-driven) |
|---|---|---|
| Workflow driver | agent-runner (LLM main loop) | **your sidecar** |
| agent-runner mode | default (`AGENT_MODE` unset) | `prompt-server` |
| IPC channel | `/ipc/tools/exec-request-{id}.json` | `/ipc/prompts/request-{id}.json` |
| `/ipc/done` written by | agent-runner | **your sidecar** (in a `finally` block) |
| Required shim | `tool-executor.sh` | none — your application code |

Everything else about the pod is the same: shared `/ipc` emptyDir volume, `SYMPOZIUM_SKILL_PACK` env, the SkillPack's `secretRef` / `env` / `volumeMounts` still apply.

---

## Sidecar anatomy

Your orchestrator sidecar needs:

1. A **primary subcommand** (the one named by `task.tool`) that reads `SYMPOZIUM_RUN_CONFIG_JSON` and runs the workflow.
2. Zero or more **helper subcommands** (called internally from the orchestrator) for steps like persisting records, fetching resources, or finalising output.
3. An **IPC loop** that calls the LLM via `/ipc/prompts/`, polling for results.
4. A **finally block** that writes `/ipc/run-result.json` and `/ipc/done` so the agent-runner exits cleanly.

The SkillPack declares the primary tool and any helpers:

```yaml
spec:
  sidecar:
    image: registry.example.com/my-orchestrator:TAG
    tools:
      - name: my_orchestrator_run       # ← task.tool matches here
        exec: ["python", "/app/orchestrator.py"]
        subcommand: run
        inputMode: args
      - name: my_orchestrator_persist   # ← called from inside run
        exec: ["python", "/app/orchestrator.py"]
        subcommand: persist
        inputMode: stdin
```

The controller overrides the container's command to `[exec..., subcommand]` when `task.tool` resolves to `my_orchestrator_run`. Helper subcommands are invoked by your orchestrator's own code — they are not exposed to the LLM.

---

## Reading the run config

The controller serialises `task.parameters` to JSON and sets it as `SYMPOZIUM_RUN_CONFIG_JSON`:

```python
import os, json
config = json.loads(os.environ["SYMPOZIUM_RUN_CONFIG_JSON"])
# config is a dict, e.g. {"batchSize": "10", "inputDir": "/data/photos"}
```

Empty or nil parameters serialise to `{}` (not `null`) so your sidecar's `JSON.parse` returns an object the input schema can validate. The [`SidecarDrivenHandler`][sidecar-driven-handler] enforces this.

---

## The IPC channels

Three channels live on the shared `/ipc` volume. All writes from your sidecar should use **atomic rename** (write `.tmp`, then `mv`) — the agent-runner and the IPC bridge both ignore `*.tmp` files.

| Path | Direction | Schema | Purpose |
|------|-----------|--------|---------|
| `/ipc/prompts/request-{id}.json` | sidecar → agent-runner | [`PromptRequest`][ipc-protocol] | LLM prompt request |
| `/ipc/prompts/result-{id}.json` | agent-runner → sidecar | [`PromptResult`][ipc-protocol] | LLM response |
| `/ipc/context/clear-{id}.json` | sidecar → agent-runner | [`ClearContextRequest`][ipc-protocol] | Reset conversation history (fire-and-forget) |
| `/ipc/run-result.json` | sidecar → agent-runner | free-form JSON | Final summary (surfaced on `AgentRun.status.result`) |
| `/ipc/done` | sidecar → agent-runner | empty file | Run completion signal |

!!! note "Atomic writes"
    The agent-runner and IPC bridge ignore files ending in `.tmp`. Always write payload files as `request-{id}.json.tmp`, then `mv` to the final name — this guarantees the reader never sees a partial payload.

---

## Calling the LLM

Write a request file with a unique id, poll for the matching result:

```python
import os, json, time, uuid

PROMPTS = "/ipc/prompts"

def atomic_write(path: str, payload: dict) -> None:
    tmp = path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(payload, f)
        f.flush()
        os.fsync(f.fileno())
    os.rename(tmp, path)

def call_llm(prompt: str, *, schema: dict | None = None, timeout: float = 60.0) -> dict:
    """Send a prompt to the LLM and block until the result arrives."""
    request_id = uuid.uuid4().hex
    request = {"requestId": request_id, "prompt": prompt}
    if schema is not None:
        request["schema"] = json.dumps(schema)   # see "Structured output" below
    atomic_write(f"{PROMPTS}/request-{request_id}.json", request)

    result_path = f"{PROMPTS}/result-{request_id}.json"
    deadline = time.time() + timeout
    while time.time() < deadline:
        if os.path.exists(result_path):
            with open(result_path) as f:
                return json.load(f)
        time.sleep(0.2)
    raise TimeoutError(f"LLM call {request_id} did not return in {timeout}s")
```

### Structured output

Set `schema` on the request to a JSON Schema describing the shape you want back. When the provider supports it (OpenAI and OpenAI-compatible endpoints), the model is constrained to emit JSON matching the schema and the agent-runner validates the response. On Anthropic / Bedrock, the agent-runner validates the model's text output as JSON and surfaces parse failures on `status="error"`.

```python
schema = {
    "type": "object",
    "properties": {
        "tags": {"type": "array", "items": {"type": "string"}},
        "confidence": {"type": "number", "minimum": 0, "maximum": 1},
    },
    "required": ["tags"],
}
response = call_llm(prompt, schema=schema)
if response["status"] != "success":
    raise RuntimeError(f"LLM error: {response.get('error')}")
tags = response["parsed"]["tags"]   # validated against your schema
```

### UseContext (run-level policy)

Whether the prompt is appended to conversation history or answered in isolation is set by `spec.useContext` on the AgentRun and surfaces as the `USE_CONTEXT` env var. The agent-runner reads it once at startup — your sidecar cannot override it per request. This is intentional: context isolation is a run-time policy decision, not a per-call opt-in.

---

## Clearing context

To flatten the conversation history between independent iterations (one image, one record, one row), drop a clear-context request:

```python
CONTEXT = "/ipc/context"

def clear_context(reason: str = "") -> None:
    request_id = uuid.uuid4().hex
    atomic_write(
        f"{CONTEXT}/clear-{request_id}.json",
        {"requestId": request_id, "reason": reason},
    )
```

The agent-runner discards the conversation history accumulated since the last clear. There is no result file — clear-context is fire-and-forget.

---

## The orchestrator loop

```python
def main() -> None:
    config = json.loads(os.environ["SYMPOZIUM_RUN_CONFIG_JSON"])
    os.makedirs(PROMPTS, exist_ok=True)
    os.makedirs(CONTEXT, exist_ok=True)

    processed = 0
    for record in iter_records(config):
        # Cheap local pre-processing (no LLM call needed)
        candidates = local_extract(record)

        # Ask the LLM to make the decision
        response = call_llm(
            prompt=(
                "Refine these candidate tags for the image. "
                "Drop any that don't apply; add any that are obvious.\n"
                f"Image id: {record.id}\nCandidates: {json.dumps(candidates)}"
            ),
            schema={
                "type": "object",
                "properties": {
                    "tags": {"type": "array", "items": {"type": "string"}},
                },
                "required": ["tags"],
            },
        )
        if response["status"] != "success":
            log.warning("LLM error on %s: %s", record.id, response.get("error"))
            continue

        persist(record, response["parsed"]["tags"])
        processed += 1

        # Flatten context so the next record doesn't inherit this turn's history
        clear_context(reason=f"after {record.id}")

    return {"processed": processed}
```

!!! tip "When to clear context"
    Clear between every **independent** unit of work (one record, one row, one image). Don't clear mid-decision — the LLM may need the running history to make a multi-step choice within a single record.

---

## Terminating the run

After all work is done, write the final summary and `/ipc/done` in a `finally` block:

```python
import sys

def write_run_result(summary: dict) -> None:
    atomic_write("/ipc/run-result.json", summary)

def write_done() -> None:
    with open("/ipc/done", "w") as f:
        f.write("done")

if __name__ == "__main__":
    summary: dict = {}
    try:
        summary = main()
    except Exception as exc:
        summary = {"error": str(exc), "phase": "orchestrator"}
    finally:
        try:
            write_run_result(summary)
        finally:
            write_done()
    sys.exit(0 if "error" not in summary else 1)
```

The agent-runner observes `/ipc/done` (not `/ipc/run-result.json`) as the completion signal. If you skip the `finally`, an exception in your workflow leaves the agent-runner polling forever.

!!! warning "Why the `finally` matters"
    Earlier sidecar-initiated designs wrote `/ipc/done` from the agent-runner as soon as it saw `/ipc/run-result.json`. If your orchestrator was mid-prompt↔LLM call when this happened, the new `/ipc/done` caused `promptLLM` to return "observed before result arrived", which misclassified successful records as failed and routed them to an unreliable fallback branch. Writing `/ipc/done` from your `finally` block guarantees there are no in-flight prompts when the signal arrives. See [Sidecar-Driven Mode](../modes/sidecar-driven.md#why-the-sidecar-owns-ipcdone) for the full rationale.

---

## `tool-executor.sh` does not apply

Tool-sidecars bridge `/ipc/tools/exec-request-*.json` using a small shell script (`tool-executor.sh`). Orchestrator sidecars do **not** use this script — your application code drives the workflow and talks to the LLM directly via `/ipc/prompts/`.

You can still declare helper subcommands (like `my_orchestrator_persist` in the example above) in `spec.sidecar.tools[]` if you want the agent-runner or another sidecar to be able to invoke them — see [Writing Tool Sidecars → Native Sidecar Tools](writing-tool-sidecars.md#native-sidecar-tools). Helpers called only from inside your orchestrator's own code don't need to be declared on the SkillPack, but declaring them keeps the contract explicit.

---

## Environment summary

Your orchestrator sidecar runs with:

| Env var | Source | Purpose |
|---------|--------|---------|
| `SYMPOZIUM_RUN_CONFIG_JSON` | controller | JSON-marshalled `task.parameters` (always a JSON object — see [Sidecar-Driven Mode → Parameter passing](../modes/sidecar-driven.md#parameter-passing)) |
| `SYMPOZIUM_SKILL_PACK` | controller | The SkillPack name (for logging) |
| `USE_CONTEXT` | controller | `"true"` if `spec.useContext` is unset/true; `"false"` if explicitly disabled. Read by the agent-runner only. |
| (your declared env vars) | `spec.sidecar.env[]` | Per-deployment config |
| (your secrets) | `spec.sidecar.secretRef` | Mounted at `secretMountPath` |

---

## Common pitfalls

| Symptom | Cause | Fix |
|---------|-------|-----|
| agent-runner never exits | orchestrator threw before writing `/ipc/done` | Wrap the main work in `try/finally`; the `finally` must always write `/ipc/done` |
| LLM call returns `status: "error"` with parse message | Schema was set but provider is Anthropic/Bedrock | Either drop the schema (free-form text response) or post-validate the response yourself |
| LLM seems to remember previous record's decision | Missing clear-context between iterations | Call `clear_context()` after each independent unit of work |
| Sidecar exits early with no result | Helper subcommand not declared on SkillPack | Declare it in `spec.sidecar.tools[]` if it's invoked across the IPC boundary |
| `JSON.parse` throws on the orchestrator's first action | `SYMPOZIUM_RUN_CONFIG_JSON` is unset | Always guard: `if "SYMPOZIUM_RUN_CONFIG_JSON" not in os.environ: ...` |

---

## See also

- [Sidecar-Driven Mode](../modes/sidecar-driven.md) — the controller/handler view of the same flow
- [Adding a Task Mode](../modes/extension-guide.md) — extend Sympozium with a new dispatch mode
- [Writing Tool Sidecars](writing-tool-sidecars.md) — the agent-driven mode pattern (tool-executor.sh, native sidecar tools)
- [Skills & Sidecars](../concepts/skills.md) — SkillPack CRD structure, RBAC

[ipc-protocol]: https://github.com/sympozium-ai/sympozium/blob/main/internal/ipc/protocol.go
[sidecar-driven-handler]: https://github.com/sympozium-ai/sympozium/blob/main/internal/controller/taskmodes/sidecar_driven.go
