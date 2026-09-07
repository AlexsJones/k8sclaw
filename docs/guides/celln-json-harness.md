# Native JSON Harness in Celln: explicit binding

Progresses [epic #426](https://github.com/sympozium-ai/sympozium/issues/426).
The advanced AgentRun binding now supports `celln.json-tools/v1` and emits
`celln.dev/v1alpha3` to Celln. The native agent loop executes inside the cell,
not in the controller. This is not yet the catalogue-backed selector described
in [the target UX](celln-harness-selection-ux.md).

## API and authority

Like the reference binding, this path requires `CELLN_HARNESS_ENABLED=true` on
the controller, `backend: celln`, a string task, explicitly admitted mote and
signed runtime closure, agent lane, and host-granted DeepSeek access. It does
not reuse arbitrary OCI images or convert existing HarnessSessions to Celln.

The following is a **partial schema illustration**, not a runnable manifest:

```yaml
spec:
  backend: celln
  task: Uppercase "celln", then measure its length.
  systemPrompt: Use the explicitly lent tools.
  model:
    provider: deepseek
    model: deepseek-chat
    authSecretRef: "" # credentials stay in the independent host grant
  celln:
    # Also supply admitted mote, runtime/closure, invocation and capabilities.
    harness:
      contractVersion: celln.json-tools/v1
      modelGrant:
        hash: blake3:<operator-grant-digest>
      json:
        maxTurns: 3
        maxCalls: 2
      borrowedTools:
        - name: uppercase
          path: /uppercase
          hash: blake3:<executable-digest>
          description: Uppercase text
          jsonStdio:
            abi: celln.json-stdio/v1
            inputSchema: blake3:<input-schema-digest>
            outputSchema: blake3:<output-schema-digest>
            inputBytes: 1024
            outputBytes: 1024
            timeoutMs: 1000
```

Select 0–16 tools; an empty list grants none. JSON options and per-tool IO
descriptors are required only for this adapter. The reference adapter still
requires exactly two tools and forbids JSON descriptors. Unknown/mixed contracts
and invalid limits refuse in Kubernetes CEL/schema validation and the controller.
Reference system prompts now refuse rather than being silently ignored.

The controller takes persona from `spec.systemPrompt` (at most 2048 bytes), task
from the string task, and model from `spec.model`. It freezes these together
with loop ceilings, exact tool/schema/ABI identities and host-grant revision in
`status.cellnRequest`. Retry keeps the same request/execution identity. v1alpha1
or v1alpha2 receipts cannot stand in for a v1alpha3 result.

The Celln host independently checks a v2 operator grant against the exact caller,
mote/runtime/closure, model, persona/loop options and ordered tool descriptors.
Schema bytes are read by hash from its bounded schema store. Possessing hashes
or creating catalogue metadata does not create this grant. Custom provider
headers, caller-selected credential Secrets, unsupported providers, ModelRef,
workspace, immutable inputs, remote MCP and persistent sessions remain refused.

## Runtime/catalogue representation is not readiness

`AgentRuntime.spec.celln` can now declare the JSON adapter with explicit `json`
loop ceilings. `CellnTool` and `CellnToolSubmission` can name
`celln.json-stdio/v1`; descriptions/deadlines must fit that adapter's bounds.
The pure authority resolver includes ABI in full-spec identity and still
requires every grant layer. Existing argv approvals cannot be reinterpreted as
JSON approvals.

These resources are not yet wired to high-level run selection. `CellnReady`
remains independently false; OCI Ready or operator-reviewed metadata cannot
supply missing composition, functional verification, distribution or serving
process prewarm. Celln-only AgentRuntime objects still require a future schema
migration; the existing OCI image requirement is unchanged.

## Evidence

On 2026-09-07 the [actual-controller proof](../evidence/celln-json-harness-controller-2026-09-07.json)
passed: real Kubernetes AgentRun → host Sympozium controller → authenticated
host dispatcher → warm-forked KVM cell → real DeepSeek → uppercase then length
→ `CELLN has length 5`. The controller run made three model requests, persisted
the exact JSON binding and matching v1alpha3 receipt, and correlated the host
grant and closure audit. No Job was created; audit records dissolution and node
state reports zero live cells. The separate direct-dispatch phase made three
additional requests.

The isolated cluster's existing test controller was deliberately paused to
avoid competing reconcilers and restored to 1/1 available afterward. The test
namespace and temporary credential copy were removed. Framework was untouched.
This is an actual controller/API proof, **not deployed router/catalogue UI proof**.
Evidence records the tested base plus working-tree changes and controller hash;
it does not claim that the base commit already contained these changes.

Portable tests cover wire compatibility with Celln's committed request fixture,
frozen persona/schema/limit identities, receipt downgrade, empty selection,
contract/ABI confusion and authority-layer preservation. The real API-server
suite checks JSON runtime/catalogue/submission round trips, dry-run AgentRun
preservation and 15 negative cases. No AgentRun executes in the schema suite.
Full Go race tests (with a real NATS server), build/vet and both Helm lints pass.

```sh
CELLN_CATALOGUE_KUBECONFIG=/absolute/isolated/kubeconfig \
  bash test/integration/test-celln-json-contract.sh
```

For the billable proof, use Celln's JSON Harness package/test described in
[Celln's dispatch guide](https://github.com/sympozium-ai/celln/blob/main/docs/JSON_HARNESS_DISPATCH.md)
and set `CELLN_HARNESS_CONTROLLER_HOOK` to this checkout's
`test/integration/test-celln-harness-controller.sh`.
`CELLN_CONTROLLER_KUBECONFIG` must explicitly select `kind-celln-m0` or
`kind-celln-deployed`. The latter additionally requires
`CELLN_PAUSE_TEST_CONTROLLER=1`, no unfinished AgentRuns and the expected
single-replica local test controller. The hook pauses/restores only that named
test deployment with UID/replica checks. Use the paired Celln proof-hook change
that permits the JSON controller test. Never pass a production kubeconfig.

Next: trusted catalogue selection/grant issuance, exact signed composition and
distribution/prewarm, then the UI/YAML user journey and conversational lifecycle.
This proof does not complete the epic.
