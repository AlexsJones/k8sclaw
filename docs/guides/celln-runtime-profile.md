# Celln runtime profile: metadata and independent readiness

This is an incremental implementation of epic #426, not a new runnable Harness
mode. An administrator may declare `AgentRuntime.spec.celln` alongside the
existing required digest-pinned OCI `spec.image`. Existing OCI-only objects and
their `Ready` semantics are unchanged. Celln-only objects are not supported.

The optional profile records revision, executable/closure/mote BLAKE3 hashes,
publisher key, canonical entry point, platform, lane, lifecycle and resource
ceilings. Only the existing experimental `celln.reference-functions/v1`
contract, `linux/amd64`, agent lane, disposable one-shot lifecycle, no runtime
data and no workspace are representable. Task input is capped at 2048 bytes.
These limits are declarations, not execution grants. They do not expand the
reference adapter's model mediation or its two-function protocol.

The runtime controller maintains a separate `CellnReady=False` condition:

- `NotConfigured`: no Celln profile exists.
- `VerificationUnavailable`: a profile exists, but the independent artifact
  admission, adapter conformance and distribution verifier is not implemented.

Both carry the observed generation. OCI `Ready=True`, informational
`spec.conformance.status=conformant`, and stale positive Celln status cannot
make this controller approve Celln placement. Existing experimental direct
AgentRun Celln dispatch does not read this profile; existing Harness/Celln
combination refusals remain in place. No fallback to OCI is added.

Next steps remain revision/UID-bound authority resolution, trusted verification,
signed composition, exact artifact distribution/prewarming and catalogue-backed
dispatch. There is deliberately no positive Celln readiness path yet. This
metadata profile does not implement the full adapter contract in the
[ADR](../design/celln-runtime-tool-contract.md).

Verification:

```sh
go test -race ./internal/controller -run TestAgentRuntime
CELLN_CATALOGUE_KUBECONFIG=/absolute/isolated/kubeconfig \
  bash test/integration/test-celln-runtime-profile.sh
```

The integration script requires `kind-celln-deployed`, applies only the
AgentRuntime CRD, and removes its temporary namespace. It tests API-server
schema acceptance/refusal, not controller rollout, admission signatures or KVM.
Controller condition transitions are covered by fake-client reconciliation
tests. No provider credentials or model calls are involved.

On 2026-09-07 the isolated Kubernetes v1.35.0 API-server test passed profile
round-trip, OCI-only compatibility and all 12 negative cases. The temporary
namespace was removed. Race-enabled API, controller, webhook and API-server
package tests and both Helm chart lints passed. This is schema and controller
logic evidence, not a deployed runtime-controller or real-model execution test.
