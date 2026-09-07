# Catalogue-backed local host issuance

`sympozium celln-tool issue AGENT` is an operator-only bridge from live
Kubernetes approvals to the local Celln host issuer. It does not submit an
execution, distribute artifacts to a fleet, advertise readiness or implement
the end-user UI/controller journey.

It requires the existing three independent tool-grant source flags, `--run`,
`--model-policy`, actual `--execution-mote` and `--execution-closure` hashes,
plus operator-configured `--celln-binary`, `--policy-root` and
`--composer-publisher`. Runtime/tool members and schemas must already exist in
the host stores, and the mote must already be independently admitted.

The operator maintains `POLICY_ROOT/model-credentials.json`:

```json
{
  "apiVersion": "sympozium.ai/celln-host-credentials-v1",
  "profiles": {"approved-deepseek": "/operator-owned/deepseek-token"}
}
```

The selected name comes from the independently reviewed model policy; the
credential path comes only from this host mapping. The bridge never reads the
credential contents, puts them in Kubernetes, or sends them to the guest.
Tenant access to artifact stores must not permit writes to host credentials,
issuer profiles, grant files or the operator-configured approval sources.

## Issuance and withdrawal

The bridge builds the exact request from its revalidated frozen selection and
live run. It compares the composed descriptor's ordered signed source identities
with the selected sources and asks Celln to authenticate a private captured copy
of those descriptor bytes. It then obtains the normalized request binding from
the real Celln parser and atomically creates a private, content-derived host
profile without overwriting different existing bytes.

Celln performs sealed guest member verification and issues a v3 grant. The bridge
rechecks the returned request binding, current Kubernetes approval, host mapping
revision and composed descriptor before returning. Local issuance/withdrawal
operations share a nonblocking OS file lock; unsupported platforms refuse.

Failure after profile publication removes exactly that profile, including a
matching profile retained by an earlier identical retry. Changed/unrelated bytes
are not deleted, and a cleanup failure is returned alongside the original error.
Grant/audit files remain intact; a v3 grant cannot pass its host profile check
after that profile is removed. This is local refusal at a new resolution, not a
fleet-wide active-cell withdrawal guarantee.

Persist the complete command output as an issuance report. To withdraw it:

```sh
sympozium celln-tool withdraw-grant issuance-report.json \
  --policy-root /absolute/operator/host-root
```

This removes only the profile with the matching saved name and SHA256. Already
absent profiles are an idempotent success. It does not remove credentials,
grants, execution journals or audit records.
Withdrawal bypasses Kubernetes client initialization, so local recovery remains
available when the API server is unreachable.

## Recovery limits and remaining controller work

This is **not an autonomous approval watcher**. Approval withdrawal after a
successful invocation requires explicit withdrawal, or the future controller
reconciler. A failed initial approval check cannot discover and withdraw all
profiles from older invocations. Keep saved issuance identities for recovery.

Before profile publication, issuance durably writes a `pending` record under
`POLICY_ROOT/sympozium-issuer-journal`. After all final checks, it atomically
replaces that record with `issued`, including the frozen selection, candidate
and full issued result, before returning success. Withdrawal first records
`withdrawing`, then removes the exact profile and records `withdrawn`. Records
are private, bounded to 1 MiB, fsynced and retained for reconciliation.

If the process dies, the OS lock releases. Recovery withdraws exact profiles
for `pending` or `withdrawing` records and durably marks them `withdrawn`; it
never reissues a grant or executes a task. Every new local bridge issuance runs
this recovery step under the lock first. It can also be invoked without a
Kubernetes connection:

```sh
sympozium celln-tool recover-grants --policy-root /absolute/operator/host-root
```

Committed `issued` outcomes survive loss of stdout/the caller and can be read
through `ReadIssuance` or the retained journal. They are history, not current
permission: revalidate approvals and reconcile existing execution identity
before dispatch/retry. Recovery does not delete grant/audit records, replay
side effects, or withdraw every committed grant automatically.

Recovery scans at most 1024 directory entries and refuses corrupt records,
identity mismatches or a larger journal; operators must reconcile/archive
history before exhausting that bound. Automatic retention is not implemented.
Changed profile bytes are not removed, and any recovery error blocks new bridge
issuance. Legacy profiles without journal records still require an explicit
saved-report withdrawal or operator audit.

While the bridge is down, an unfinished profile can remain effective until
recovery runs; the host dispatcher does not consult this journal. Deployment
startup/dispatch must be gated on recovery. Do not expose this local operator
primitive as an unattended production service until controller reconciliation,
ongoing approval withdrawal/expiry and startup recovery gates are integrated.

### One-pass current-approval reconciliation

`cellnreview.ReconcileIssued` is the controller integration primitive for checking
one committed issuance. Its caller supplies the trusted, uncached Kubernetes
reader and independent approval-source configuration; the saved journal cannot
choose its own authority sources. Under the issuer lock it first recovers pending
records, then revalidates the frozen run, selection and model approval with a
five-second context deadline (or the caller's earlier deadline). The reader must
honor context cancellation. Missing/changed approvals, API errors or timeout,
and a removed/retargeted host credential mapping trigger durable withdrawal of
the exact profile. Token contents at the same approved host path are not read.

An unchanged observation does not rewrite or expand authority. Reconciliation
never reissues, dispatches, or restores a withdrawn profile when the API returns.
It preserves grant/audit records and refuses to delete different profile bytes.
Filesystem failures are returned; they must not be reported as successful
withdrawal. This API is not yet registered as a controller or autonomous watcher.

An `issued` result is only a point-in-time observation, not a readiness result or
permission lease. A watcher dying between checks would leave profiles usable.
Before unattended dispatch, add host-enforced expiry (or an equivalent enforced
gate), startup recovery and continuous reconciliation. Existing v3 grants pin
the profile's exact bytes, so changing an expiry field in that profile cannot be
treated as transparent lease renewal. This also does not cancel an active cell
or establish fleet-wide revocation.

A successful issuance does not prove that the serving process has a warm mote,
that the requested Harness/tool behavior conforms functionally, or that a model
can complete the task. The remaining controller path must persist issuance
identity, maintain withdrawal, prewarm the selected serving node, dispatch once,
and correlate terminal results and receipts. Conversation and UI/YAML acceptance
remain separate gates.

## Evidence

Portable race tests cover idempotent publication, concurrent-issuer refusal,
failed issuer/report/binding checks, post-issuance approval/mapping/composition
withdrawal, capacity-independent lock release, and refusal to delete unrelated
profile bytes. These tests use fake Kubernetes and a mocked command runner.
Abrupt child-process exits additionally prove recovery before host issuance,
after host issuance, after durable commit and during withdrawal. They bypass
all Go defers and verify retained audit data, committed outcomes, released
locks and repeatable recovery. This is process-crash testing, not a filesystem
power-loss or deployed-controller proof.

The explicit real test uses the real compositor, signed runtime plus two tools,
real Celln issuer and KVM member verification, then repeats issuance and proves
profile withdrawal refuses further issuance while retaining grant bytes. It
still uses fake Kubernetes and makes **zero model calls**. Enable it only with
`CELLN_ISSUANCE_KVM=1`, `CELLN_ISSUANCE_MATERIALIZER` (the public-seed
`prepare_issuance_fixture` executable), `CELLN_HARNESS_PACKAGE`,
`CELLN_COMPOSITION_FIXTURE` and `CELLN_COMPOSITION_BINARY`, then run:

```sh
go test -race ./internal/cellnreview -run '^TestComposeRealCellnArtifacts$' -count=1 -v
```

Require the explicit real-KVM issuance PASS line; an opt-in skip is not proof.
