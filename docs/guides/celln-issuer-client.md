# Verified remote issuer client

`cellnreview.NewIssuerClient` is the controller-side client for the
[host issuer service](celln-issuer-service.md). It is also available through the
operator `celln-tool issue-remote AGENT` command. It is not yet called by the
AgentRun reconciler and does not dispatch an execution.

The client takes operator configuration only: an HTTPS origin, absolute controller
bearer-token file, and an optional absolute CA bundle. Omitting the CA bundle uses
system trust; supplying one restricts trust to that bundle. TLS 1.3, certificate
chain and hostname verification are mandatory. URL credentials, path prefixes,
queries, fragments and plaintext origins refuse. Ambient HTTP proxies are not
used, redirects are not followed, and there is no skip-verification option.
Recreate the client to reload a changed CA bundle. Token contents are reread for
each request so mounted-file rotation does not require recreation.

## Identity and retry contract

`Issue(ctx, loader, frozen, approval, artifacts)` builds the expected execution
candidate independently from the caller's live, uncached API reader and trusted
authority sources. It sends one provisioning POST, with no application-level
retry. Requests and responses are bounded to 1 MiB; requests embedded in returned
issuance records are bounded to 64 KiB. The response must explicitly say that
execution was not performed and readiness was not checked.
The complete operation has a 100-second context ceiling (or the caller's earlier
deadline), including API revalidation; the API reader must honor cancellation.

Before returning, the client checks the candidate's approval, profile identity,
grant hash and complete returned execution request, then revalidates live approval
again. Only the grant self-reference and three known Rust serialization defaults
are normalized: absent `forge` to null, absent `inputs` to an empty array and
absent invocation `args` to an empty array. Unknown fields and any changed task,
persona, caller, execution ID, tools, schemas, artifact or resource ceiling
refuse. JSON numbers retain their exact representation; they are not rounded
through floating-point conversion.

Transport/read failures return `ErrIssuerOutcomeUnknown`. A lost response can
mean the host has durably issued a profile. Preserve the original frozen request,
approval, artifact identities and candidate before the call; retry only those
same values. Other refusal/validation errors also do not authorize changing
identity or bypassing host replay rules. The client neither renews expiry nor
creates another run/attempt, withdraws local files, executes a task or claims
that a serving node is warm.

## Operator command

`issue-remote` uses the existing independent grant-source, `--run`,
`--model-policy`, explicit `--tool NAME@REVISION`, `--execution-mote` and
`--execution-closure` flags, plus:

```sh
--issuer-url https://issuer.example.internal:8788 \
--issuer-token-file /etc/sympozium/controller-issuer-token \
--issuer-ca-file /etc/sympozium/issuer-ca.pem
```

There are no local policy-root, Celln binary, signing-key, composer or lifetime
flags: those remain host service configuration. The output is labelled
`sympozium.ai/celln-remote-issuance-report-v1`, not a local withdrawal report.
Loss of output does not trigger local filesystem withdrawal; the remote durable
window and periodic host reconciliation remain in force.

This command is an explicit operator provisioning invocation and resolves the
current plan each time. Do not blindly rerun it after an ambiguous outcome if
approval or run state may have changed. Durable controller retry must instead
reuse its saved `frozen`/`approval`/`artifacts` through the client API. The
`IssueForRun` helper below persists that state; wiring the AgentRun reconciler
remains the next integration gate.

## Durable AgentRun provisioning

`IssueForRun(ctx, writer, uncachedReader, runKey, loader, seed)` saves
`status.cellnIssuance` before contacting the host. The first call requires an
`IssuerRequest` seed. A retry may pass nil to resume the saved request; supplying
a changed seed refuses. The operation has a 110-second ceiling.

The saved `Prepared` record pins the operator issuer endpoint, frozen selection,
model approval, artifacts, independently derived candidate and payload SHA-256.
After verified issuance, a status update commits `Issued` and the returned result.
The CRD enforces immutable payload/target/hash/result and a monotonic phase,
including refusal to remove issuance or the entire status. These schema rules
protect transitions, not payload authenticity: the helper independently derives
and validates the candidate and live run/approval again on resume.

A failed preparation write makes no remote call. A lost preparation acknowledgement
resumes the stored payload. A lost remote outcome or failed result commit retries
the identical issuance, subject to host journal/expiry rules. A lost acknowledgement
after result commit resumes the validated stored outcome without another issuance
POST. API conflicts never authorize replacing a concurrent plan. A changed run
identity/spec, terminal/deleting run, existing dispatch identity, changed target
or changed approval refuses. No retry extends the host profile expiry.

The endpoint string is not a cryptographic host-instance identity. Operators must
use a stable per-host issuer endpoint; DNS/load-balancer failover and fleet
ownership are not established by this helper. A stored result is not a readiness
or continuing authorization claim: serving-side checks and expiry still apply.
Status contains task/policy metadata, but no provider credential contents;
namespace read permissions must reflect that sensitivity.

This helper does not set `cellnRequest`, `cellnActionID`, run phase or readiness.
Until the catalogue dispatch bridge is connected, the legacy reconciler explicitly
refuses any run with issuance state instead of silently forging its task.

Failure-injection tests cover both sides of each status commit, immutable retry
identity and the no-legacy-dispatch guard. The opt-in
`test/integration/test-celln-issuance-status.sh` checks transitions against the
isolated Kind API server without Jobs or model calls. The separate KVM composition
test exercises durable preparation, actual TLS/host issuance, saved-result resume
and managed approval withdrawal with fake Kubernetes metadata. Neither test proves
the final deployed catalogue-backed execution journey.

## Evidence

Tests cover verified TLS, token rotation, untrusted certificates, changed
responses, malformed/oversized results, redirects, a lost response, post-response
approval withdrawal and refusal of unsafe configuration. The explicit real-KVM
test uses this client against the real TLS issuer service and actual Celln
compositor/member verifier; identical retry keeps profile/grant identity and
periodic approval withdrawal refuses further issuance. Kubernetes is a fake
client, no model calls are made, and this is not a deployed controller proof.
