# Writing a Harness Adapter

Sympozium's [`harness` task mode](harness.md) runs an external agent harness as the pod's
primary process. An **adapter** is the image that makes a particular harness fit: it reads
the task the way Sympozium supplies it, runs the harness, and hands the answer back on the
result contract.

Sympozium ships no adapters. This page is the contract, so one can be written and maintained
without touching this repository or waiting on a Sympozium release. The same division as the
[Celln backend](../concepts/celln-backend.md), where the execution runtime lives in its own
repo.

## The contract

### In

Everything arrives as environment variables and mounted files on an ordinary container.

| | |
|---|---|
| `TASK` | The task text, from `task.parameters.prompt`. Falls back to `/ipc/input/task.json` (`.task`) when an orchestrator wrote one. |
| `SYSTEM_PROMPT` | `spec.systemPrompt`. Honour it only if you declare `persona`. |
| `MODEL_NAME`, `MODEL_BASE_URL`, `MODEL_PROVIDER` | `spec.model`. Map these onto whatever your harness reads — see [Model routing](#model-routing-is-yours) below. |
| provider credential | Injected per-key by `SecretKeyRef` from the `allowedAuthSecretKeys` allowlist (`ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `DEEPSEEK_API_KEY`, …). Already on the container; never widen the allowlist to make an adapter work. |
| `TOOL_POLICY_ALLOW`, `TOOL_POLICY_DENY` | Comma-separated. Honour them only if you declare `toolFilter`. |
| `MCP_CONFIG_PATH` | Path to the MCP server registry, as **JSON**: `{"servers":[{"name","url","toolsPrefix","timeout","headers","auth":{"type":"bearer","secretKey":"MCP_AUTH_X"}}]}`. Read the token from the env var `auth.secretKey` names — it is on the container. Connect to **every** entry: one of them may be `sympozium-skills`, which is how the run's SkillPack tools reach you. |
| `HOME` | `/home/agent`, an `emptyDir`. The only writable path besides `/workspace`, `/ipc/output` and `/tmp`. |
| `SYMPOZIUM_RESULT_PATH` | Where to write the result. Defaults to `/ipc/output/result.json`; read it from env rather than hardcoding. |
| `/workspace` | The run's PVC, and the container's working directory. |
| `/skills/` | Skill files, if the Agent has any. |
| `task.parameters.args` | Passed through as the container's `args`. Your `ENTRYPOINT` receives them as `"$@"`. |

You get `/ipc/input` (read-only) and `/ipc/output`, and no other part of `/ipc`. The rest of
that volume is a control-plane surface — a file dropped in `/ipc/spawn` creates a sub-agent
run, one in `/ipc/messages` posts to Slack — and an adapter is not a trusted enough writer for
it. Do not try to reach those paths; they are not in your mount namespace.

### Out

Two things, both required, because two different readers consume them:

1. **`$SYMPOZIUM_RESULT_PATH`** — the `ipc-bridge` sidecar watches this file and publishes the
   completion event.
2. **The `__SYMPOZIUM_RESULT__` stdout marker** — the controller parses this out of the agent
   container's logs.

```
__SYMPOZIUM_RESULT__
{"status":"success","response":"...the harness's answer..."}
__SYMPOZIUM_END__
```

On failure, `{"status":"error","error":"..."}` and a non-zero exit.

**Build the payload with a JSON encoder, never string interpolation.** Harness output is
LLM-generated and adversarial; `jq --arg` encodes it as a JSON string so it cannot forge a
result structure. It cannot forge the marker either — the controller takes the **last** one
in the log, so a mid-run print is overtaken by the real one.

**Omit `metrics`.** If your harness does not report real token usage, leave the field out
rather than sending zeros: `status.tokenUsage` stays absent, matching Sympozium's
absent-not-zero convention. Reporting zeros claims the run was free.

### Image requirements

The pod security context is not relaxed for harness mode, so the image must already fit it:

- Runs as **UID 1000**, non-root.
- **`readOnlyRootFilesystem: true`** — write only under `$HOME`, `/workspace`, `/ipc`, `/tmp`.
  Anything your harness wants to put in `~/.config` or `~/.cache` works, because `$HOME` is
  the `emptyDir`; point its own home/state variable there too if it has one.
- `drop: [ALL]`, RuntimeDefault seccomp.
- Its own `ENTRYPOINT`. Sympozium sets no command.

## Declaring capabilities honestly

`task.parameters.capabilities` is a comma-separated list of what your image honours:
`outputSchema`, `toolFilter`, `persona`, `subagents`, `resume`. Sympozium did not build the
image and cannot inspect it, so this declaration is the entire basis on which a run is
admitted.

Declare only what the adapter actually translates:

- **Too generous** — the run is admitted and the field is silently dropped at runtime. This is
  the failure the whole mechanism exists to prevent, and the one case it cannot catch.
- **Too stingy** — working runs are rejected at `kubectl apply`.

Prefer stingy. A rejection is a message an operator can act on; a silent drop is a policy
that quietly does not apply. If your harness has *no* name-based tool filter, do not declare
`toolFilter` because it has *some* related-sounding config — check what it actually filters.

Put the reasoning in your adapter's README. The next person to bump the upstream version
needs to know which claims were checked against what.

## SkillPack tools come to you as MCP

If the run has SkillPacks that declare tools, the pod runs a Sympozium-owned MCP server on
loopback and adds it to the registry at `MCP_CONFIG_PATH` as `sympozium-skills`. Connect to it
the same way you connect to any other entry — **there is nothing adapter-specific to write.**

That server holds `spec.toolPolicy` and applies it at both `tools/list` and `tools/call`, and
it is the only thing in the pod that can dispatch to a skill sidecar. Its name is protected:
`sympozium*` is a reserved prefix, and an operator `mcpServers` entry claiming it is rejected,
so a registry entry with that name is always the real one. So those tools are
policy-enforced regardless of what your adapter does, and you cannot bypass it even if you
want to.

Your `toolFilter` claim still covers your own built-in tools. The two are separate halves of
the same AgentRun field.

## Model routing is yours

Sympozium sets `MODEL_*` and injects the credential, then stops. It cannot verify that your
harness routes to the model the `AgentRun` names, and many harnesses have opinionated
provider resolution — config files, CLI logins, ambient environment — that can quietly win
over what you pass.

Map the values explicitly, prefer them over ambient config where the harness lets you, and
say in your README what happens if it does not. A harness running against a model the
manifest never named still *succeeds*, which is the worst way for this to be wrong.

## A minimal adapter

```bash
#!/usr/bin/env bash
set -euo pipefail

RESULT_PATH="${SYMPOZIUM_RESULT_PATH:-/ipc/output/result.json}"

emit() {  # $1 = success|error, $2 = body
  local payload
  payload="$(jq -cn --arg status "$1" --arg body "$2" \
    'if $status == "error" then {status:$status, error:$body}
     else {status:$status, response:$body} end')"
  mkdir -p "$(dirname "$RESULT_PATH")"
  printf '%s' "$payload" > "$RESULT_PATH"
  printf '__SYMPOZIUM_RESULT__\n%s\n__SYMPOZIUM_END__\n' "$payload"
}

TASK_TEXT="${TASK:-}"
if [ -z "$TASK_TEXT" ] && [ -r /ipc/input/task.json ]; then
  TASK_TEXT="$(jq -r '.task // ""' /ipc/input/task.json)"
fi
[ -n "$TASK_TEXT" ] || { emit error "no task supplied"; exit 1; }

argv=(my-harness --headless)
[ -n "${SYSTEM_PROMPT:-}" ] && argv+=(--system-prompt "$SYSTEM_PROMPT")   # declare: persona
argv+=("$@" "$TASK_TEXT")

out="$(mktemp "${TMPDIR:-/tmp}/harness.XXXXXX")"
set +e
"${argv[@]}" 2>&1 | tee "$out"
rc="${PIPESTATUS[0]}"
set -e

if [ "$rc" -ne 0 ]; then
  emit error "harness exited ${rc}: $(tail -c 2000 "$out")"
  exit "$rc"
fi
emit success "$(cat "$out")"
```

An adapter whose harness is configured by files rather than flags does the same job with one
more step: generate the config into `$HOME` and point the harness at it. Build it with `jq`
too — a generated config that interpolates `SYSTEM_PROMPT` or an MCP URL by string
concatenation is an injection hole.

## Publishing one

Adapters are ordinary container images. Publish yours wherever you like and tell operators to
add that registry to `SympoziumPolicy.imagePolicy.allowedRegistries`.

Two things worth putting in the README, because operators cannot discover them from the image:

- **The capability line to use**, and why each one is or is not on it.
- **A version-bump check.** Upstream harnesses change flags and config formats; a one-command
  smoke test that proves the adapter still composes is what keeps a bump from becoming a
  silent behaviour change.

## See Also

- [Task Mode: `harness`](harness.md) — the mode itself, and what Sympozium supplies
- [Adding a Task Mode](extension-guide.md) — for changing Sympozium's behaviour rather than
  bringing a harness
