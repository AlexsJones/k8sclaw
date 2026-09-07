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

Process death between profile publication and final validation can leave an
orphan profile/grant. The OS lock releases on exit, but the profile is not
automatically deleted. Operators must audit retained profiles against current
approval and withdraw stale profiles before dispatch/retry. Do not expose this
local operator primitive as a tenant-facing or unattended production service
until durable reconciliation/expiry and crash recovery are implemented.

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
