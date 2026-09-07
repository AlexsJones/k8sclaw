# Authenticated Celln host issuer service

`sympozium celln-tool serve-issuer --config /absolute/issuer.json` runs the
managed issuer on the Celln host. The controller can request bounded provisioning
over TLS without mounting the host's profile store or provider credential files.
This is a controller-only service, not a tenant API or an execution endpoint.
It is not yet wired into the AgentRun controller, Helm or node distribution.

## Operator configuration

Supply an explicit kubeconfig with read-only access to the configured approval
ConfigMaps, Agents, AgentRuns, AgentRuntimes and CellnTools. The client is uncached;
tenant RBAC must deny writes to approval sources. No Kubernetes Secrets are read
by the issuance protocol. Run on the host with the independently managed Celln
policy/artifact root, credential mapping and Celln binary described in
[local issuance](celln-local-issuance.md).

```json
{
  "apiVersion": "sympozium.ai/celln-issuer-service-v1",
  "listen": "127.0.0.1:8788",
  "certificateFile": "/etc/sympozium-issuer/tls.crt",
  "privateKeyFile": "/etc/sympozium-issuer/tls.key",
  "tokenFile": "/etc/sympozium-issuer/controller-token",
  "cellnBinary": "/usr/local/bin/celln",
  "policyRoot": "/var/lib/celln",
  "composerPublisher": "<64-hex independently approved composer key>",
  "profileLifetimeMs": 300000,
  "sweepIntervalMs": 5000,
  "bindings": [{
    "agent": {"namespace": "team-a", "name": "assistant"},
    "operatorGrants": {"namespace": "operators", "name": "team-a-operator"},
    "runtimeGrants": {"namespace": "operators", "name": "team-a-runtime"},
    "agentGrants": {"namespace": "operators", "name": "team-a-agent"},
    "modelPolicy": {"namespace": "operators", "name": "team-a-model"}
  }]
}
```

The composer value is a placeholder, not a test trust root. Four distinct
authority sources and a distinct Agent binding are required. Configuration is
strict JSON bounded to 1 MiB; paths must be absolute. Lifetime must be 1–300000 ms
and sweep interval 1000–30000 ms. There is no legacy/non-expiring service mode.
Changing the configuration requires a controlled service restart.

```sh
sympozium --kubeconfig /etc/sympozium-issuer/kubeconfig \
  celln-tool serve-issuer --config /etc/sympozium-issuer/issuer.json
```

TLS 1.3 is mandatory even on loopback; there is no plaintext or skip-verification
flag. Supply a certificate trusted by the controller with the correct hostname
or IP SAN. Restrict host/firewall/cluster ingress to the controller. NetworkPolicy,
certificate issuance/rotation and production installation remain deployment
qualification work, not a guarantee supplied by this command.

The separate controller bearer credential must be an independently generated
high-entropy token, 24–4096 visible ASCII bytes, in an operator-only file. It is
not the Celln dispatch credential or model API key. The service rereads it per
request; rotate by atomically replacing the file. Missing, malformed, duplicate
or wrong credentials refuse. TLS key/certificate reload requires restart.

## Protocol

Both endpoints require `Authorization: Bearer ...` over TLS, with no query
parameters. Responses carry `Cache-Control: no-store`.

- `GET /v1/issuer/status`: `sympozium.ai/celln-issuer-status-v1`, local
  `provisioningGateOpen`, `executionAuthorized: false`, and
  `artifactReadiness: not_checked`. A closed gate returns 503. An open gate is
  not Kubernetes availability, selection readiness or permission to dispatch.
- `POST /v1/issuances`: JSON `sympozium.ai/celln-issuer-request-v1` containing
  `frozen`, `approval` and actual materialized `artifacts` from catalogue planning.
  Unknown fields, trailing JSON, non-JSON/compressed bodies and bodies exceeding
  1 MiB refuse. Host paths, tokens, authority-source configuration, runtime
  commands and certificate/readiness assertions are not accepted request fields.

The host revalidates every observation against its own configured live sources,
authenticates signed artifacts, performs KVM sealed member verification and
creates the bounded profile/grant through the managed lifecycle gate. A caller
cannot authorize a different Agent or substitute approval sources through the
request. The response is `sympozium.ai/celln-issuer-response-v1`, with `issued`,
`executed: false` and `artifactReadiness: not_checked`. Model credentials and host
credential paths do not appear in the result. Errors are deliberately generic.

At most two authenticated handlers proceed concurrently; excess work returns
429. Managed issuance itself serializes with reconciliation. Request work has a
90-second context budget, with bounded HTTP header/body/idle/write timeouts.
Signals close the listener/gate and cancel in-flight issuance before joining
the service and reconciler. Host expiry remains the admission bound after a crash.

A lost response is an ambiguous delivery of a durably journaled outcome, not
permission to refresh expiry or execute again. Retry only the identical frozen
request; the original profile/grant/window identity remains fixed. Never mint a
new run or execution ID merely to resolve a network error. There is no upload,
signing, provider-call, dispatch or automatic replay endpoint here.

## Evidence and remaining integration

Tests exercise the actual HTTPS server, credential rotation/refusal, strict
request handling, bounded concurrency and shutdown. The explicit KVM test uses
real signed composition → TLS request → managed provisioning → actual sealed
member verification → identical retry → approval deletion → periodic withdrawal
→ host refusal. Kubernetes is still a fake client and no model calls are made.

The [verified client](celln-issuer-client.md) now provides strict remote response
validation and an operator `issue-remote` command. Next: wire durable controller
issuance, selection-specific serving-node prewarm, catalogue-backed AgentRun
dispatch and correlated results. Deployed
Kubernetes/RBAC/network/TLS qualification, UI/YAML selection, conversational
lifecycle and final real-model release acceptance remain open.
