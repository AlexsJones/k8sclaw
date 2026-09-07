# Experimental Celln router deployment

This chart wiring is part of [epic #426](https://github.com/sympozium-ai/sympozium/issues/426),
not a claim that the deployed execution plane has passed M0. It requires a
router image implementing Celln PRs #70, #75 and #80, and dispatchers with the
versioned capabilities endpoint from #80. The old v0.4.13 router cannot
consume these arguments. Build and pin the actual image digest; a digest pins
bytes, but does not by itself prove that the bytes implement the required CLI.

## Operator prerequisites

- Provision each dispatcher independently, including signed tools, warm motes,
  trust roots, bounded model grants where applicable, and persistent state.
  Use stable, individually addressable HTTP origins. A load-balancing Service
  in front of different dispatchers is not a stable execution owner.
- Every router replica receives the same complete endpoint list and one shared
  backend credential. Configure that credential on every listed dispatcher;
  the legacy installer's independent per-host tokens are not compatible with
  this topology without explicit reprovisioning.
- Create separate client and backend Secrets in `celln-system`, each with a
  `token` key. Copy only the client credential into the controller namespace's
  `celln.tokenSecret`. Tokens must contain at least 24 ASCII graphic bytes and
  differ in content. Helm checks distinct Secret names; the router checks
  actual token content. Never put model keys or token contents in Helm values.
- Provide an existing PVC in `celln-system`, writable by UID/GID 10001. All
  replicas must see the same ledger with coherent cross-node POSIX `flock`,
  atomic publication and file/directory `fsync`. RWX is not sufficient evidence.
  Qualify the actual storage implementation with concurrent claims, process
  death, replica restart and node loss. Independent hostPath or emptyDir
  volumes cannot provide this property.
- Create a third, distinct read-only discovery credential in both namespaces:
  `celln.capabilityTokenSecret` for the API server and
  `celln.router.capabilityTokenSecret` for the router (key `token`). Never mount
  the client execution credential or backend credential in the API-server Pod
  for discovery. The router permits this token only on `GET /v1/capabilities`.
- Both router transport legs currently use plaintext unless an external
  protected path is provided. The router does not implement TLS. The mandatory
  ingress NetworkPolicy restricts callers, but does not encrypt credentials.
  The two insecure flags are explicit acknowledgements, not encryption.

The following is a configuration outline, **not a runnable proof**. Substitute
qualified storage, real endpoints, existing Secrets and a built image digest:

```yaml
celln:
  enabled: true
  installer:
    enabled: false
  tokenSecret: celln-router-client
  capabilityTokenSecret: celln-discovery
  # Plaintext example only for an explicitly accepted isolated test network.
  allowInsecureHttp: true
  router:
    replicas: 2
    image:
      repository: YOUR_REGISTRY/celln-router
      digest: sha256:REPLACE_WITH_ACTUAL_64_HEX_DIGEST
    backends:
      - http://dispatcher-a:8787
      - http://dispatcher-b:8787
    allowInsecureBackends: true
    clientTokenSecret: celln-router-client
    backendTokenSecret: celln-dispatcher-backend
    capabilityTokenSecret: celln-discovery
    ownershipClaim: celln-router-ownership
```

An HTTPS `routerUrl` requires a separately configured TLS endpoint. Merely
changing the URL does not add TLS to this Service. Any proxy topology also
needs an explicitly reviewed ingress policy; the current policy admits only
controller-labelled Pods and API-server-labelled Pods in the control-plane
namespace. Each peer intersects namespace AND Pod labels; tenant Pods copying
labels do not gain ingress. A deployed TLS path remains an acceptance gate.

The API server enables discovery only with `CELLN_ENABLED=true`; the chart sets
that flag, the router URL, explicit HTTP acknowledgement and
`CELLN_CAPABILITY_TOKEN_FILE`. It reloads the read-only file for each probe,
refuses redirects, bounds responses and applies a five-second HTTP deadline.
There is no fallback to TCP, public health, or execution credentials. Disabled,
missing/invalid credentials, incompatible reports and zero eligible nodes report
unavailable. A positive result means authenticated compatible **node preflight**
only. The UI shows this qualification instead of a green reachability claim.
Signed artifacts, selected Harness compatibility, model grants and prewarming
remain run-specific checks; discovery does not certify them.

## Upgrade, rotation and rollback boundaries

This changes a router DaemonSet to a Deployment and changes Celln from default
on to opt-in. Existing releases must supply the new settings explicitly. A
bare `--set celln.enabled=true` is no longer enough. Do not perform a rolling
migration while old routers can accept new requests without durable ownership.
Quiesce submissions, reconcile outstanding runs against their original owners,
and preserve receipts/audit before retiring the old router. An empty new ledger
does not recover pre-migration ownership.

The chart mounts Secrets as directories, not `subPath`, allowing Kubernetes
Secret projection updates to reach the router's per-request credential reload.
Projection is asynchronous; coordinate dispatcher and router backend rotation,
then router and controller client rotation. There is no dual-token overlap
protocol, so do not claim zero-downtime rotation.
Rotate the discovery credential independently on the router/API-server pair.
All three configured router tokens must remain distinct. When upgrading this
chart, provision discovery Secrets and compatible dispatcher/router images first;
older router images refuse the new flag rather than silently enabling discovery.

The ownership PVC is operator-managed and is not deleted by this chart. Retain
it across upgrades and rollbacks. Do not roll back to a router that ignores the
ledger, delete tombstones, or retry an ambiguous request under a fresh ID as an
automatic recovery action: these can repeat external side effects. A removed
or inaccessible owner must refuse, not silently reschedule. Ledger capacity
currently stops new IDs at 100,000; there is no safe automatic tombstone GC.

The optional legacy installer mutates host files and systemd services. Removing
its DaemonSet or rolling back Helm does not revert those changes. Its pinned
release, dispatcher provisioning, upgrade and host rollback procedures still
need coordinated qualification before this is a supported turnkey installer.

## Evidence required before declaring deployment complete

### API startup without streaming

With `nats.enabled=false`, the chart passes an empty event-bus URL and the API
server skips NATS initialization entirely. With a configured but unavailable
NATS endpoint, the API limits initial stream provisioning to five seconds and
starts without streaming if it fails. That failed initialization is not retried
by the running API: restore NATS and restart the API to enable streaming. An
already initialized bus retains its unlimited background reconnect policy.
The UI flag is explicitly set false when `apiserver.webUI.enabled=false`.

These changes address the startup finding in #431. See the
[actual-process startup and reconnect evidence](../evidence/apiserver-nats-startup-2026-09-07.md)
for tested cases and limits; review and merge remain required before closing it.

Chart render tests cover flags, credential/storage mounts, image pinning,
explicit host opt-in and ingress selectors. They do not prove runtime storage,
TLS or KVM behavior. M0 still requires actual controller → Service → router →
two eligible KVM dispatchers, replica/node loss, ambiguous acceptance, timeout,
deletion/cancellation, credential rotation and install/upgrade/rollback tests.
The existing host-controller AI proof does not satisfy this deployed-path gate.
