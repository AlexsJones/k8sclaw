# Tool authority resolution core

`internal/cellnauthority` implements the pure, in-memory tool-selection portion
of the [Harness/Celln contract](../design/celln-runtime-tool-contract.md). It is
not yet connected to admission, API handlers or dispatch. No resource becomes
runnable by virtue of this package or a catalogue `Ready` condition.

Each selected tool must match a live same-namespace catalogue object by name,
UID, generation, revision and SHA256 digest of the complete Go-JSON spec. That
digest covers publisher, executable/closure/schema hashes, lane, entry point,
provenance and declared limits. It is metadata identity, **not** a Celln artifact
BLAKE3 hash or a signature-verification result. Recreated resources, changed
schemas and stale grants refuse, even when the display name is unchanged.

The resolver requires four explicit inputs: operator grants, runtime ceilings,
Agent grants and per-run selections. For every tool, all must name the exact
identity. Effective time, memory, argument and output limits take the minimum
of those inputs and the catalogue ceiling. Omitted selection means zero tools;
omitted grant layers grant nothing. There are no implicit defaults or wildcard
grants. External side effects required by a tool must be allowed by every layer;
they cannot be silently changed to “none”. Workspace, inputs and tool egress
remain refused under the initial delivery contract.

The result preserves explicit selection order and owns a copy of each spec.
Duplicates, ambiguous catalogue/grant entries, colliding entry points or any
denied member reject the whole selection: no partially authorized plan escapes.
The core accepts up to 16 selections but does not imply the current dispatcher
can execute 16 tool refs; exact signed composition is still required.

## Integration still required

These internal structs are not a tenant-facing authorization API. A future
controller must independently authenticate and load each grant source, verify
publisher/artifact/schema conformance and bind the runtime, policy and Agent
UIDs/generations/revisions plus run/model/session identities into the full frozen
plan. Supplying all grant structs from an untrusted request would bypass that
trust boundary. This package neither makes nor verifies those trust decisions.

Before new work, re-resolve against current grants and compare with the frozen
plan. Removed grants must refuse new work, not mint a fresh execution identity
to replay it. Live-cell withdrawal still requires host enforcement and confirmed
dissolution; this resolver provides no fleet revocation guarantee.

Tests cover all layers, empty selection, stale/recreated identity, metadata
mutation, forged status, explicit effects, unsupported authority, ordering,
atomic refusal, entry-point collisions and re-resolution after grant removal.
The fuzz property checks numeric intersections never exceed any input ceiling:

```sh
go test -race ./internal/cellnauthority
go test ./internal/cellnauthority -run '^$' \
  -fuzz FuzzCeilingsNeverExpand -fuzztime 10s -parallel 2
```

These are control-plane logic tests, not guest-isolation or full-product proof.

On 2026-09-07, race-enabled resolver/API/controller/webhook/API-server tests,
the full Go build and resolver `go vet` passed. The 10-second, two-worker fuzz
run completed 181,094 executions with no failure. No cluster resources or model
credentials were used for this increment.
