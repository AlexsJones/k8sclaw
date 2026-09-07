# API startup with disabled/unavailable NATS — 2026-09-07

Actual-process follow-up to #431 and the Celln discovery proof. Source
`b525711`, image `localhost/sympozium-capability-api:b525711`, configuration
SHA256 `651805d19751dcb207f398844b410f2ef7041d8234ae54d334a460d064f4989d`.
Binary SHA256:
`c91c2d93d9f8efbb1b7ebbdbcd5303b4eb61b9f6443e41a8f72328b2cb6d140d`.

Used the same isolated `kind-celln-deployed` cluster, API-only image and explicit
UID 65532 as the discovery proof. Applied the actual chart API Deployment/Service
and removed the prior test-only startupProbe. Both cases below retained the
normal chart liveness/readiness settings (10-second periods, failure threshold
three); neither case used an enlarged startup window. Framework was untouched.

| Case | Container started (UTC) | HTTP startup log (UTC) | Pod Ready (UTC) | Restarts |
| --- | --- | --- | --- | --- |
| Empty event-bus URL, NATS disabled | 12:08:22 | 12:08:22 | 12:08:23 | 0 |
| Explicit unreachable `nats://127.0.0.1:1` | 12:09:23 | 12:09:28 | 12:09:35 | 0 |

These are wall-clock observations with one-second timestamp precision, not
performance distributions. Disabled-case Pod UID:
`62e16223-7c05-4e17-b970-788c71aafc82`. Unreachable-case Pod UID:
`784c35c3-7a0c-4086-9672-5d1fceb37dce`.

For the unreachable case, the API logged `context deadline exceeded`, closed
the NATS connection, then started authenticated HTTP without streaming. Both
cases served authenticated `/api/v1/capabilities` and reported the two-node
Celln path preflight eligible, with artifact/Harness-readiness qualifications.

The disabled case used the chart's explicit `--serve-ui=false`; authenticated
`GET /` returned 404. The API remained protected by its public dummy fixture
token; no authentication bypass or provider credential was introduced.

Race-enabled tests passed for `cmd/apiserver`, `internal/eventbus`,
`internal/apiserver`, and `internal/controller`, including cancellation,
non-responsive NATS handshake, unreachable provisioning and chart UI flags.
Helm lint passed. Initial connection failure intentionally
requires API restart after NATS is restored; this is not automatic recovery of
an initially absent streaming service.

## Established connection restart regression

The opt-in `TestNATSRealServerReconnectAfterStartupContextExpires` uses a real
local `nats-server` 2.11.4, bound only to a dynamically selected loopback port,
with test-owned temporary JetStream storage. It calls the production
`NewNATSEventBusWithContext`, cancels that startup context immediately after
successful initialization, and then:

1. Publishes and receives an identified event on a live subscription.
2. SIGKILLs the owned server process and observes the original connection
   becoming disconnected.
3. Starts a new server process on the same port and retained store.
4. Requires the same connection object to reconnect, then publishes and receives
   another identified event on the original subscription channel.

This passed once, then three consecutive repetitions, then once in the full
race-enabled API/event-bus/controller suite: five actual server restart cycles.
The first test completed in 6.04 seconds; the three-repetition package run took
19.121 seconds including race-runtime overhead. No race reports occurred.
Server executable SHA256:
`4d3cd9e94ef7c6e811eda283df8b011cd43c1c093024c3af981f4c57f68dd6f7`.

Reproduction (does not modify repository dependency versions):

```sh
GOBIN="$PWD/target/nats-proof/bin" go install github.com/nats-io/nats-server/v2@v2.11.4
SYMPOZIUM_NATS_SERVER="$PWD/target/nats-proof/bin/nats-server" \
  go test -race ./internal/eventbus -run TestNATSRealServerReconnect -count=3
```

Without the explicit executable path this integration test skips; that skip is
not a reconnect pass. The test kills only its own server processes and Go removes
its temporary storage. It neither contacts a cluster NATS service nor needs
credentials. This proves retained-store process restart, not broker data loss,
replicated-cluster failover, zero event loss during a deleted consumer, or a new
end-to-end API streaming/browser test. #431 remains open until review and merge.
