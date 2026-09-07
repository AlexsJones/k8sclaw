# Deployed Celln capability discovery — 2026-09-07

This is an actual API-server → router Service → two dispatcher preflight test
for epic #426, using Sympozium #430 and Celln #80. It is not a new AI execution,
browser test, artifact-admission proof, or production Helm release qualification.

## Environment and revisions

- Explicit kubeconfig for isolated `kind-celln-deployed`, Kubernetes v1.35.0,
  Calico v3.32.2, two worker node-containers on the same KVM host. Framework was
  untouched. Both dispatchers had zero live cells before upgrade.
- Sympozium API binary source `34f7fe1`, image
  `localhost/sympozium-capability-api:34f7fe1`, image configuration SHA256
  `0eea836ac107abfbf5e48f6d9570c4f13fbc13d4ddd222bd1e396ca1d945c332`.
  Binary SHA256:
  `fcf54b280ce47fd0791778af78c63e79d7cd17c37a7f9959d1759d9f35791797`.
- Celln router and dispatcher source `5a330cd`, binary SHA256
  `1604112136872eae02657f5904afbe691f85b214e335cda70fef2aad268f09f7`.
  Router pinned manifest SHA256:
  `2f7d0d1c1ae3fccd20eff79e71ba6318973fa689c1139c6363e383a9afb374e1`.
- Actual chart-rendered router, Service, mandatory ingress NetworkPolicy,
  API-server Deployment/Service and API-server RBAC. Existing controller RBAC,
  execution state and ownership PVC were preserved. This is selected resource
  application, not a full Helm install/upgrade.
- Three separate dummy credential classes: execution client, dispatcher backend,
  and read-only discovery. A fourth public dummy token authenticates the test
  caller to the API. No model key is involved.

## Positive and negative observations

At 12:01:12 UTC, authenticated `GET /api/v1/capabilities` on the actual API Pod
returned `celln.available=true` with the qualification:

> Node preflight eligible only; selected Harness, approved tools, model grants and warm artifacts still require validation

The API made its own request through the router Service; no host-side controller
or port-forward substituted for this path. Router discovery reported
`celln.dev/capabilities-v1alpha1`, `eligibleNodes: 2`, `preflightOnly: true`,
and `artifactReadiness: not_checked`. Both authenticated node reports had KVM,
CPU, kernel and readable-store preflight flags, zero live cells, one available
cell slot, 268435456 memory bytes and one egress slot. Both reported only
`celln.reference-functions/v1` and `persistentSessions: false`.

From the allowed API-server Pod, its read-only token received HTTP 401 for:

- `POST /v1/executions`;
- `GET /v1/executions/discovery-forbidden`;
- `GET /v1/executions/discovery-forbidden/audit`;
- `POST /v1/executions/discovery-forbidden/cancel`.

Network-policy controls used the same valid dummy discovery credential:

- A Pod in `celln-discovery-tenant` copying the API-server label timed out
  connecting to the router (curl exit 28, five seconds). It could reach the
  Kubernetes API Service's public `/version` (200), excluding general DNS or
  networking failure as the explanation.
- An unlabelled Pod in `sympozium-system` also timed out. Adding the permitted
  API-server label to that same Pod made discovery return 200; the label was
  then removed. This tests the label intersection, not just namespace reachability.

At 12:01:54 UTC, after replacing only the API-server namespace's discovery
Secret with a different valid-format dummy token, the API returned
`celln.available=false`: authenticated discovery was refused/unavailable.
The router retained its original credential. This confirms a mismatch is not
rescued by execution credentials or public health.

At 12:03:03 UTC the original discovery Secret had been restored and the API
again returned `celln.available=true` with the same preflight qualification.
The API Pod UID remained `c52dc527-3aae-49da-ab77-5093306c3266`, with restart
count zero throughout the credential test. The fixture's EXIT trap also
restores the original public dummy credential on failure.

## Startup finding and test-only adjustments

The helper image inherits root as its image user. The chart correctly refused
it under `runAsNonRoot`; the fixture then explicitly selected UID 65532.

NATS is absent in this isolated proof. The chart's default liveness window
repeatedly restarted the API before it opened port 8080. A diagnostic process
stack showed the main goroutine blocked in `NewNATSEventBus → ensureStream →
CreateOrUpdateStream → RequestWithContext`. A test-only 180-second startup probe
allowed the synchronous retries to finish; the API started without streaming at
12:00:31 UTC. The fixture also used explicit unreachable
`nats://127.0.0.1:1` and `--serve-ui=false` (the binary defaults UI serving on).

These are documented adjustments, not a claim that the default chart booted
successfully. Follow-up [#431](https://github.com/sympozium-ai/sympozium/issues/431)
tracks disabled/unavailable NATS startup and the UI flag mismatch. No streaming
or browser/UI behavior is certified here.

Persistent local fixtures and raw output are under the Celln integration
worktree's `target/deployed-kind.JcI9Gg/`, including `discovery-values.yaml`,
`discovery-proof.sh`, `discovery-denied.yaml`, and `evidence/discovery-proof.log`.
