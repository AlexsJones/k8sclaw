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
reuse its saved `frozen`/`approval`/`artifacts` through the client API. Persisting
that state and wiring the AgentRun reconciler remain the next integration gate.

## Evidence

Tests cover verified TLS, token rotation, untrusted certificates, changed
responses, malformed/oversized results, redirects, a lost response, post-response
approval withdrawal and refusal of unsafe configuration. The explicit real-KVM
test uses this client against the real TLS issuer service and actual Celln
compositor/member verifier; identical retry keeps profile/grant identity and
periodic approval withdrawal refuses further issuance. Kubernetes is a fake
client, no model calls are made, and this is not a deployed controller proof.
