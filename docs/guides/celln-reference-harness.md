# Experimental in-cell reference Harness binding

Tracks [epic #426](https://github.com/sympozium-ai/sympozium/issues/426).
This advanced AgentRun API is **disabled by default**. It is a narrow one-shot
prototype, not the existing container-replacing `task.mode: harness`, a Pi/Hermes
adapter, HarnessSession persistence, or the completed Harness + Celln UX.

## What is now connected

`spec.celln.harness` selects `celln.reference-functions/v1`, an immutable
operator-approved model grant and two explicit borrowed executable tools. The
existing `spec.celln.mote` and single `spec.celln.tools` runtime/closure entry
identify the sealed runtime. Each borrowed tool has a name, canonical guest
path, BLAKE3 hash and description; the Celln operator independently verifies
the exact selection against its grant and signed closure.

The controller takes the task from the string-form `spec.task` and selected
model from `spec.model.model`, freezes them with all artifact/grant identities
in `status.cellnRequest`, and sends `celln.dev/v1alpha2`. Retry uses that frozen
request, not mutated spec fields. Terminal receipt version must match the
frozen request; v1alpha1 receipts cannot stand in for v1alpha2 success.

The initial adapter supports two static functions taking two integer-string
arguments. It runs its model loop inside a warm-forked cell and feeds tool
results back to the provider. It does not load an arbitrary OCI image or MCP
server. AgentRuntime remains the separate approved OCI runtime contract; this
increment does not introduce its Celln artifact profile or selection UI.

## Explicit activation and restrictions

The controller must have `CELLN_HARNESS_ENABLED=true` to submit this new binding.
The default and ordinary v1alpha1 Celln behavior are unchanged. Disabling the
flag prevents submission/retry; it does not revoke an already running cell.
Do not enable this broadly before the deployed-path and product gates pass.

The reference bridge currently requires:

- `backend: celln`, string task, `lane: agent`, `workspace: none`, no inputs or
  invocation arguments, one signed runtime closure and exactly two borrowed tools.
- `model.provider: deepseek`, nonempty model alias, empty `authSecretRef`.
  The explicit operator grant supplies credentials; they are not inherited from
  ambient node provider configuration or passed through Kubernetes to the guest.
- Default DeepSeek base URL (empty or `https://api.deepseek.com`) and exactly
  `https://api.deepseek.com` in Celln egress. Thinking other than off, custom
  headers/header secrets, ModelRef and model node selectors refuse rather than
  being silently ignored.

The request's new section is:

```yaml
celln:
  # Supply independently admitted mote, runtime/closure, invocation and limits.
  harness:
    contractVersion: celln.reference-functions/v1
    modelGrant:
      hash: blake3:<operator-grant-file-digest>
    borrowedTools:
      - name: add
        path: /add
        hash: blake3:<admitted-add-binary-digest>
        description: Add two integer strings.
      - name: multiply
        path: /multiply
        hash: blake3:<admitted-multiply-binary-digest>
        description: Multiply two integer strings.
```

This is a schema illustration, not runnable admission: replace placeholders and
provision the exact approved artifacts/grant on Celln. Its grant pins caller
`sympozium:<namespace>/<AgentRun-name>`, mote, runtime, closure, model and complete
tool definitions. Possession of the request hashes alone grants no authority.

## Measured evidence

On 2026-09-07 the actual built controller used the isolated three-node Kind API
and authenticated host Celln dispatcher to complete an AgentRun:
`add(37,5) → 42`, then `multiply(42,2) → 84`, then model answer `84`. The three
model responses and tool results were produced by the loop inside the cell.
Sympozium persisted the frozen v1alpha2 request and matching receipt. Audit
correlated the grant revision, signed member graph, agent lane and dissolution;
no Job was created and the node reported zero live cells afterward.

The paired Celln branch `feat/harness-dispatch-binding` contains the durable
record at `docs/evidence/harness-dispatch-controller-2026-09-07.json` and the
contract/reproduction guide at `docs/HARNESS_DISPATCH.md`.
`test/integration/test-celln-harness-controller.sh` is the explicitly billable
companion hook: it requires the fixture's running dispatcher, package and token
file, and an explicit `CELLN_CONTROLLER_KUBECONFIG` whose context is
`kind-celln-m0`. It never accepts the default production kubeconfig. The hook
updates test-cluster CRDs and removes only its newly created test namespace and
host controller. Temporary provider credential copies were removed separately.

Validation passed: `go build ./...`; controller/API/webhook/orchestrator tests
with the race detector; generated CRDs/deepcopy; Celln `make ci`; actual
authenticated dispatcher and actual-controller model tests. Request-freezing,
unsupported model options, disabled activation and receipt-version mismatch
have offline regression coverage.

## Not complete yet

The controller and dispatcher in this test ran on the host, not as the deployed
router/DaemonSet stack. This is not a multi-node failover or production Helm
acceptance result. Celln's attempt tombstone prevents replay only within the
same durable state root; fleet deduplication/metering, live grant withdrawal,
cancellation during model calls and operator recovery/retention remain gates.

Next: approved AgentRuntime Celln profiles and tool admission/catalogue bindings,
the explicit opt-in UX, deployed router/DaemonSet proof, broader adapter/tool
contracts and conversational lifecycle. Tool transcript events are not
independently hardware-attested per-tool receipts. Keep epic #426 open.
