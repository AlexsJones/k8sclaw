# Operator review and verified catalogue publication

An operator workflow toward #335 / epic #426. This publishes reviewed catalogue
metadata after checking local signed identities and bounded schema bytes. It is
**not runnable admission**: filesystem/member/adapter conformance, signed
composition, distribution/prewarm and dispatch integration remain required.
No positive Ready status is written.

## Review requirements

Review behavior, provenance, publisher, lane, entry point, schemas, effects and
resource limits. A signature authenticates a publisher, not safe behavior.
Review binds the exact submission UID and full-spec SHA256 from `inspect`.
Changed/recreated/deleting objects refuse. The CLI uses the caller's kubeconfig
identity and requires existing namespace-scoped reviewer RBAC to publish. It
borrows no service credential; submitters still cannot publish approvals.

The operator supplies absolute paths for a trusted Celln binary, existing
publisher/revocation policy root and staged bundle. Nothing fetches tenant URLs,
pulls sourceImage, imports submitted publishers into policy or executes tools.
Stage `closure.json`, `toolfs.ext2`, `arguments.schema.json` and
`result.schema.json`. Keep operator-controlled `trusted-closures.json` separate
from untrusted submissions. Celln must support `closure verify --toolfs` and
`schema verify`; older binaries fail closed.

```sh
sympozium --kubeconfig /absolute/operator/kubeconfig -n tenant \
  celln-tool inspect tool-v1

# After reviewing the spec, copy its exact identity fields:
sympozium --kubeconfig /absolute/operator/kubeconfig -n tenant \
  celln-tool approve tool-v1 \
  --reviewed-uid '<inspected submission UID>' \
  --reviewed-spec-sha256 'sha256:<inspected full-spec digest>' \
  --celln-binary /absolute/trusted/celln \
  --policy-root /absolute/operator/policy \
  --bundle-dir /absolute/operator/staged-bundle
```

Verification uses a stripped environment, 30-second limit per process and
bounded output. Closure/publisher/member/filesystem and both schema identities
must match; interpreter closures cannot be labelled tool-lane code. Policy and
object identity are rechecked before publication. Reports come directly from
the trusted process, never from submission annotations or status.

The command creates a same-name/spec CellnTool with review annotations binding
submission UID/spec, policy and filesystem hashes. Existing revisions are never
overwritten. Untrusted labels, annotations, owners and status are not copied.
Review annotations are historical provenance, **not grants, signed attestations,
conformance or readiness leases**. Publication cannot transact atomically with
local policy changes: verification/dispatch must independently recheck current
policy and identities before new work, including after withdrawal.

## Evidence and remaining gates

Race tests cover stale review, incompatible reports, schema mismatch, process
failure, bounded/trailing output, policy change, recreation/deletion during review,
status stripping and no overwrite. Real-binary tests use explicit
`CELLN_REVIEW_BINARY` and `CELLN_REVIEW_FIXTURE` from Celln's
`prepare_review_fixture` example; ordinary CI explicitly skips that external test.

The actual API/CLI test additionally needs `CELLN_CATALOGUE_KUBECONFIG` and
`SYMPOZIUM_REVIEW_BINARY`:

```sh
bash test/integration/test-celln-tool-review.sh
```

It refuses contexts other than `kind-celln-deployed`, creates temporary namespace
resources, and removes that namespace on exit. The 2026-09-07 run passed reviewed
publication, stale review, immutable-create and exact-schema-byte refusals. No
Ready condition, cell, Job or provider call occurred. The deliberately
non-executable fixture proves metadata review, not runnable conformance.

Next: actual filesystem/member/adapter conformance in a bounded verification
plane; exact signed composition/distribution/prewarm with expiring node evidence;
then trusted approval plus authority resolution in explicit user lending and
dispatch. Do not enable a runnable selector based on these annotations.
