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
Both `celln.argv/v1` and `celln.json-stdio/v1` are identified explicitly; changing
the ABI changes the full-spec identity and invalidates previous grants. JSON
catalogue descriptions and tool deadlines must fit the adapter's 512-byte and
30-second ceilings. This does not replace content/schema conformance checks.

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

### Live grant-source loader (implementation in progress)

`cellnauthority.Loader` reads the live Agent, its same-namespace `runtimeRef`,
and explicitly selected tool names/revisions using an injected uncached
Kubernetes API reader. Three distinct ConfigMap references are configured by
the operator/controller, not supplied by a run: operator, runtime and Agent
grant layers. Each `grants.json` document uses
`sympozium.ai/celln-grants-v1`, declares its layer, binds both subjects by
namespace/name/UID/generation/full-spec SHA256, and carries exact tool grants.
The loader records source UID/resourceVersion/document digest, intersects
all layers, then rereads subjects, tools and sources to refuse observed changes.
An empty selection remains empty but still requires the configured subject
approvals. It currently accepts JSON tool-lane selections only.

The reader cannot prove ConfigMap ownership from labels: deployment RBAC must
prevent tenant writes to these configured locations. Caller ownership of the
Agent must also be authenticated separately. This loader is **not yet wired to
API handlers or dispatch**, does not issue a host model grant, and does not turn
runtime metadata into conformance or selection readiness. Its rereads are not
an atomic Kubernetes transaction or an execution lease; fresh authorization
and host enforcement remain mandatory before new work.

The read-only operator command exposes this planning path:

```sh
sympozium --namespace tenant celln-tool plan my-agent \
  --grant-namespace operator-system \
  --operator-grants reviewed-operator-grants \
  --runtime-grants reviewed-runtime-grants \
  --agent-grants reviewed-agent-grants \
  --tool uppercase-v1@v1 --tool length-v1@v1
```

The command emits a snapshot plus `prepared.composition`, the exact
`celln.dev/composition-plan-v1` input for Celln's compositor. It also maps
the approved selected schemas and limits to JSON Harness borrowed-tool
descriptors. The runtime's full spec is retained and rechecked before
preparation. Because tools share cell memory, the entire cell is capped by
the strictest selected memory ceiling; there is no invented per-tool memory
isolation. A ceiling too small to support the runtime must fail subsequent
conformance, not be silently increased.

This operator report explicitly says `executionAuthorized: false`,
`artifactReadiness: not_checked` and `conformance: not_checked`. Choosing source
flags does not establish their ownership or issue a host grant. A future
tenant-facing API must select sources from trusted controller configuration,
never forward these flags from user input. No objects or artifacts are written.

Adding `--run RUN_NAME` binds the selection to an existing same-namespace
Celln AgentRun. The output contains a versioned `frozen` record with the run
UID/generation/full-spec identity, grant-source revisions, complete runtime
spec, exact ordered tools and prepared artifacts. The loader revalidates that
record before output. `Loader.Revalidate` refuses any observed change rather
than returning a new plan or minting an execution ID. It must also run before
any future execution side effect; a saved report is not a durable approval.
Controller persistence/dispatch integration and ambiguous-execution recovery
are still required. Planning does not submit or mark the AgentRun as running.

### Build the selected local composition

`celln-tool compose` requires an existing run and the same explicit grant
sources, plus operator-owned binary, policy/store, signing-key and new output
paths. For example, replace `plan` above with `compose` and add:

```sh
  --run my-run \
  --celln-binary /opt/celln/bin/celln \
  --policy-root /var/lib/celln \
  --key-file /secure/composer.seed \
  --output-dir /var/tmp/new-harness-composition
```

This verifies each source's exact publisher, root entry point and executable
using Celln's verifier, and verifies the selected schema bytes. Source
descriptors must already be staged in `closures`, schema bytes in `tool-schemas`,
and member blobs in `tools` under that operator root. It invokes Celln's
compositor with bounded output and a 60-second subprocess deadline, without
inheriting provider credentials. Each successful source check and the resulting
composition must agree on the Celln trust-policy digest. Kubernetes approvals
are revalidated immediately before and after the build.

The output is a local signed composition, not an admitted or distributed image,
a prewarmed mote, or a host model grant. No AgentRun status is changed. Refusal
after a build can leave its explicitly named output directory for diagnosis;
it must not be consumed as an authorized execution. Temporary plan files are
removed on return. Controller execution still needs these checks connected to
its own trusted configuration, persistence and readiness path.

The Sympozium wrapper tests use a fake Kubernetes client and scripted verifier
responses to check exact arguments, source binding, policy changes, withdrawal
and cleanup. These tests do not replace the Celln compositor's real-image/KVM
tests or establish a complete real catalogue-to-cell execution proof.

The opt-in `TestComposeRealCellnArtifacts` additionally passed against the
actual Celln binary and signed native Harness/uppercase/length bytes. It built
and verified a 32 MiB composition, then refused its descriptor after source
withdrawal. [Recorded evidence](../evidence/celln-catalogue-composition-2026-09-07.json)
explicitly distinguishes real artifacts from fake Kubernetes metadata: no
model request or cell ran in this test.

Prepare the fixture in the Celln checkout with the public-seed
`prepare_composition_fixture` example, giving it a new directory and an existing
native JSON proof package. Then run in the Sympozium checkout:

```sh
CELLN_COMPOSITION_FIXTURE=/absolute/path/to/generated/fixture \
CELLN_COMPOSITION_BINARY=/absolute/path/to/celln \
  go test -race ./internal/cellnreview -run TestComposeRealCellnArtifacts -count=1 -v
```

Race-enabled tests use a Kubernetes fake client to exercise actual loader
lookups, stale subjects, withdrawn grants, untrusted tenant lookalikes,
source mutation, malformed documents and restrictive selections. They do not
claim live API-server RBAC or catalogue-backed execution proof.

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
