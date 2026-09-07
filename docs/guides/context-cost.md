# Controlling Context Cost

An agent run is a loop: the model asks for tools, the runner executes them, and
the results are appended to the conversation. Because every round re-sends the
whole conversation, **a tool result added at round 5 is billed again on every
remaining round**. With `MAX_TOOL_ITERATIONS` at its default of 50, a single
50 KB page fetch can be paid for dozens of times.

This is usually the dominant cost in a long run — not the system prompt. A
typical run's input tokens climb steadily round over round; the gap between the
first request and the last is accumulated tool output.

## What the knobs do

**All of this is off by default.** An unconfigured run sends exactly what it
always did — both mechanisms change what the model can see, so neither is
imposed silently. Individual tools keep their own output caps (`execute_command`
and `read_file` at 8 KB, `fetch_url` at 50 KB by default and up to 100 KB when
the model raises `maxChars`).

| Variable | Default | Effect |
|---|---|---|
| `CONTEXT_TOOL_RESULT_MAX_BYTES` | `0` (off) | Truncates each tool result as it enters the conversation. |
| `CONTEXT_HISTORY_BUDGET_BYTES` | `0` (off) | Once live tool-result bytes exceed this, older results are replaced with short placeholders. |
| `CONTEXT_HISTORY_BUDGET_LOW_BYTES` | half the budget | How far down to drain when elision fires. |
| `CONTEXT_KEEP_RECENT_RESULTS` | `3` | Newest results never elided — the model is still reasoning over these. The in-flight round's results are protected on top of this window. Only applies when elision is on. |
| `CONTEXT_ELISION_SPILL_DIR` | `/workspace/.sympozium/elided` | Where an elided result's full output is written so the model can read it back. Set to `off` to elide destructively. Only applies when elision is on. |

Set them via `spec.env` on an AgentRun, or on the Agent so every run inherits them.

```yaml
spec:
  env:
    - name: CONTEXT_HISTORY_BUDGET_BYTES
      value: "60000"
    - name: CONTEXT_KEEP_RECENT_RESULTS
      value: "3"
```

The two knobs are independent and address different things. The per-result cap
stops one enormous result from entering at all — most useful for sidecar tools,
which have no output limit of their own (native tools and MCP tool output carry
the caps above; MCP output is capped at 8 KB). The history budget addresses
accumulation: many individually-reasonable results adding up over a long run.
For most cases the history budget alone is the one worth setting.

## Why elision is batched, not continuous

Providers cache prompts by exact byte prefix. Appending to the end of the
conversation leaves the prefix intact, which is why long runs on a
cache-enabled provider still report high cache-hit rates. **Rewriting anything
mid-conversation invalidates the cache from that point on.**

So elision deliberately does not trim a little each round. When the high-water
mark is crossed it drains all the way to the low-water mark in one pass, paying
a single cache write and then running every remaining round against a much
smaller prefix. An entry is never elided twice, so a pass over unchanged history
does nothing at all.

This matters most on OpenAI-compatible providers, the only ones where the
runner currently benefits from prefix caching (see
[Reading `cached_input_tokens`](#reading-cached_input_tokens)). On Anthropic and
Bedrock there is no cache to invalidate yet, so batching costs nothing there and
keeps the behaviour identical once caching is turned on.

Results from the round currently in flight are never elided, regardless of
`CONTEXT_KEEP_RECENT_RESULTS`. A single round can issue more parallel tool calls
than that window, and handing the model a placeholder for a call it just made —
before it has seen the answer — would invite it to simply run the tool again.

For the same reason, the tool *set* is never subsetted per round: the tool
schemas sit in the cached prefix, and shrinking them dynamically would
invalidate the cache on every call to save something the cache already makes
cheap.

## Choosing a budget

Start by measuring. With `DETAILED_LOG_PATH` set, every round writes a `response`
event to `llm.jsonl` carrying `input_tokens`, `cached_input_tokens`, and
`tool_result_bytes`:

```bash
jq -c 'select(.event=="response")
       | {round, input_tokens, cached_input_tokens, tool_result_bytes}' llm.jsonl
```

If `tool_result_bytes` climbs steadily, set `CONTEXT_HISTORY_BUDGET_BYTES` to
roughly where you want it to plateau. Elision events are logged too:

```bash
jq -c 'select(.event=="elision")' llm.jsonl
```

### Reading `cached_input_tokens`

**Today this is only ever non-zero on OpenAI and OpenAI-compatible providers
such as OpenRouter**, which cache automatically once a prompt is large enough.
Anthropic and Bedrock both require the request to carry explicit cache
breakpoints (`cache_control` blocks and `cachePoint` blocks respectively), and
the agent runner does not emit them yet — so on those providers the field
reports `0` every round. That is an accurate reading, not a broken metric:
there is no caching happening to report. The field is plumbed through all three
providers so that enabling caching later is a one-line change.

Where it is populated, note that the number means different things per
provider: OpenAI and OpenRouter count cached tokens *inside* `input_tokens`,
while Anthropic reports the uncached remainder in `input_tokens` and the cached
portion separately. That is why the runner logs the two side by side rather
than as a hit-rate percentage.

## Elided output is recoverable

Elision moves output out of the conversation; it does not destroy it. Before a
result is replaced, its full text is written to `CONTEXT_ELISION_SPILL_DIR` and
the placeholder names that path:

```
[earlier execute_command result elided — 48213 bytes saved to
 /workspace/.sympozium/elided/0007.txt; read_file to recover.]
```

So the model pays for a path instead of a payload, and gets the detail back on
demand — `read_file` paginates, so the size of the original does not matter.
That is usually better than re-running the tool, whose side effects may not be
repeatable and whose cost is paid again.

Files are named by ledger position alone — deliberately nothing else, so no
model- or MCP-supplied string reaches a filesystem path. They are written `0600`
under a `0700` directory, and only ever created when elision actually fires.
They live as long as the pod does — or longer with a PVC-backed workspace, where
postRun hooks can read them too.

Spilling only turns on when the placeholder's instruction can actually be
followed. At startup the runner checks that:

- **`read_file` survived the tool policy.** A run with
  `toolPolicy.deny: [read_file]`, or an `allow` list that omits it, has no way
  back to the file.
- **The spill directory is under a root `read_file` will open** — `/workspace`,
  `/skills`, `/tmp`, or `/ipc`. Pointing `CONTEXT_ELISION_SPILL_DIR` at, say,
  `/var/log/agent` would produce files the agent cannot read.

If either fails the runner logs why and elision stays destructive, rather than
writing files nobody can reach and promising the model otherwise:

```
WARNING: not spilling elided tool output — read_file is not available to this run.
Elision stays destructive; set CONTEXT_ELISION_SPILL_DIR to a readable root or
allow read_file to make elided results recoverable
```

The same fallback applies per result if a write fails at runtime (a read-only or
absent `/workspace`) — logged once, then a bare placeholder. Elision still
happens in all these cases, because the context pressure is real either way.

## Trade-off

An elided result is one `read_file` away rather than in front of the model, so
it will not be reasoned over unless the model goes and fetches it. On tasks
where the agent must correlate evidence gathered early with findings much later,
set `CONTEXT_KEEP_RECENT_RESULTS` higher or leave elision off and rely on the
per-result cap alone.
