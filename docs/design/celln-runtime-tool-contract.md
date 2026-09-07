# ADR: Harness identity, Celln placement and approved tool lending

Date: 2026-09-07. Status: proposed implementation contract for
[epic #426](https://github.com/sympozium-ai/sympozium/issues/426), M1–M4.
This ADR specifies intended behavior. The first
[catalogue metadata/schema and role slice](../guides/celln-tool-catalogue.md)
is implemented; runtime profiles, approval/distribution, lending resolution and
conversation extensions are **not yet implemented**. Existing execution guards remain authoritative until
each extension and its conformance tests ship.

## Context and decision

The user outcome is an approved Harness running its real agent loop inside a
cell, with an explicit selection of approved borrowed tools, including a tool
the user submitted for admission. Selecting an image, installing tools, and
granting authority are different actions.

Current foundations are the existing administrator-owned `AgentRuntime`,
namespace-scoped Agent/run identity, frozen Celln request, authenticated signed
closures, and the experimental `celln.reference-functions/v1` binding. The
reference's two integer functions and fixed model grant prove a mechanism, not
a general supported Harness or catalogue. M0 discovery proves node preflight,
not named artifact readiness. Existing `AgentRuntime.spec.conformance` is
informational and cannot be reused as an authorization decision.

We will reuse `AgentRuntime` for runtime identity, add a separately validated
Celln artifact profile, and introduce a namespace-scoped executable-tool
catalogue. The controller resolves user selections into an immutable execution
plan; neither the UI nor the runtime may manufacture executable authority.

| User choice | Identity / execution boundary | Required gate |
| --- | --- | --- |
| Harness | Existing `runtimeRef`, pinned resource UID and artifact revision | Administrator approval and adapter conformance for the selected placement |
| Runtime placement | Job or Celln | Placement-specific compatibility and readiness; no silent fallback |
| Tool placement | Existing mediated tools or Celln tool execution | Explicit bridge contract and permitted tool identities |
| Borrowed tools | Ordered, explicitly selected catalogue revisions | Intersection of policy, runtime support, Agent grants and run selection |

“Celln tool execution” keeps the Harness outside the cell; “Harness in Celln”
puts the loop inside it. UI, status and receipts must not confuse these modes.
A mote is the substrate at rest; every cell is a live sealed mote.

## 1. Runtime profile and compatibility

The proposed optional `AgentRuntime.spec.celln` profile binds:

- Adapter contract/version and immutable runtime executable hash/entry point.
- Signed closure hash, manifest publisher identity and verification evidence.
- Mote/platform ABI requirements, executable member graph, interpreter/lane
  classification and immutable runtime-data requirements.
- Supported model/tool protocols, bounded task/config/result envelopes and
  supported lifecycle semantics, including explicit unsupported features.
- Maximum authority and resource ceilings; these are ceilings, not grants.

Existing `spec.image`, OCI model/session settings and their current Ready
behavior retain their meaning. A Celln-only profile will require a deliberate
schema/validation migration allowing image omission only when a valid Celln
profile exists; an empty image must not accidentally enter the Job builder.
Mixed OCI/Celln profiles need independent readiness conditions and resolved
revisions. Invalid Celln configuration must not turn an otherwise valid OCI
profile into a different runtime, and OCI Ready must never imply Celln Ready.

Celln profile conformance is enforced, not a tenant-supplied string. A trusted
controller records observed generation, exact profile digest, publisher/policy
revisions and evidence after validation. User-controlled status, stale
generation, a deleted/recreated runtime UID or an informational conformance
marker cannot satisfy this gate. Unknown adapter contracts refuse.

The initial general adapter must consume structured task/persona/model/tool
descriptors and return bounded structured completion plus tool-call events.
All executable dependencies are admitted and sealed. Runtime configuration,
conversation text and tool results are data, never an implicit exec grant.
An interpreter cannot promote runtime-generated code into the tool lane.

## 2. Catalogue and submission ownership

Use a new `CellnTool` catalogue for **executable artifacts**. Do not overload
SkillPack installation or MCPServer identity: remote MCP is separately mediated
service authority, and privileged sidecars carry different trust boundaries.

Each immutable catalogue revision must contain:

- Namespace, stable display name and revision; executable entry point/hash;
  signed closure hash and member graph; publisher key and signature evidence.
- Description, versioned argument/result JSON schemas, schema hashes and an
  invocation ABI. Supported schema features must be enumerated and bounded;
  unknown keywords cannot silently disable validation. The reference adapter's
  two integer-string arguments are not a generic JSON-schema implementation.
- Supported architecture, runtime ABI, lane and required broker protocols.
- Bounds on arguments/results, time, memory, inputs and outputs; workspace
  requirements; exact allowed egress operations and side-effect classification.
- Immutable package provenance (including a source OCI digest when relevant),
  dependency audit and support owner. A source OCI digest is not the signed
  Celln closure digest and must never be substituted for it.

Users create `CellnToolSubmission` objects naming immutable package bytes and
requested behavior. Submissions confer **no execution authority**. Operator
review decides publisher trust and permitted behavior, then publishes a
catalogue revision. Users cannot create approved catalogue entries, set
approval status, change trust roots, or approve their own submissions through
either Kubernetes RBAC or an API-server confused-deputy path. Signing authenticates
origin; it does not replace review of behavior and limits.

Catalogue revisions are immutable. Updating a tool creates a new revision;
revocation is a separate policy action. Cross-namespace references refuse in
the initial contract. A later export/import mechanism must preserve publisher,
namespace authorization and immutable identity rather than accepting a bare
foreign object name.

## 3. Distribution, readiness and composition

The operator distribution controller fetches the exact admitted bytes into
eligible Celln node stores, verifies hashes/signatures/closure dependencies and
prepares a compatible warm mote. Node observations bind node identity and boot
epoch, catalogue/profile revisions, policy revision, ABI and bounded expiry.
Readiness expires across restart, revocation or missing artifacts. Merely
seeing a readable store, node KVM preflight or a cached hash is insufficient.

The existing dispatcher accepts one precomposed signed runtime closure. The
first catalogue-backed implementation must respect that limit: an operator
composition step constructs and signs the exact runtime-plus-selected-tools
closure, rejects path/hash collisions, verifies every executable dependency,
and distributes/prewarms it before advertising the selection as ready. A cache
key includes runtime, ordered tool revisions, ABI and policy revision.

Do not submit several tool refs that the dispatcher refuses, silently drop a
tool, or fall back to forge. Arbitrary dynamic multi-tool composition is a
separate Celln capability extension, requiring delivery/sealing evidence.
The runtime tool protocol exposes only lent entry points; necessary closure
libraries are executable dependencies, not additional user-callable tools.

## 4. Resolution and shrinking authority

Effective authority is the intersection of administrator policy, admitted
runtime/tool ceilings, Agent grants and explicit per-run/session selection.
Empty selection means no borrowed tools; omission is not “all installed tools”.
Numeric ceilings take the minimum; destination/method/tool sets intersect;
unsupported workspace or schema combinations refuse. A model-generated name,
path, schema, provider option or tool description cannot broaden authority.

Before dispatch, freeze an execution plan containing resource names **and UIDs**,
catalogue/profile revisions, all executable/closure/schema hashes, effective
limits, provider/model/parameter grant revision, policy decision, run UID and
session/turn/tool-call identity where applicable. Retry uses the same plan and
execution ID. Renaming or recreating a catalogue object does not retarget it.
Current authorization is rechecked before new work: a frozen grant is not a
revocation exemption. A denied retry must not mint a fresh execution ID.

The host independently validates the frozen plan against its signed artifacts
and grants. Discovery and invocation enforce the same lent-tool set. Provider
credentials stay host/control-plane-side; selected provider/model and budgets
must not silently fall back to ambient host configuration. Private MCP services
need a separate authenticated mediator, not weaker public-HTTPS SSRF checks.

Admission withdrawal prevents future plans and future mediated calls. Live
revocation requires a host-enforced generation check plus cancel/dissolve and
acknowledged teardown; a control-plane flag alone cannot revoke already lent
executable pages. Until fleet live withdrawal is implemented and proven, the
UI must describe the narrower guarantee and refuse workflows requiring more.

## 5. Conversation and lifecycle strategy

Choose **external conversation state with disposable cells per bounded turn**,
not an indefinitely alive one-shot cell. A new explicit Celln session mode may
reuse HarnessSession identity only after its API/controller/proxy distinguish
this model from the existing Deployment/Service session. No existing session
is migrated implicitly and OCI sessions keep their current behavior.

Each session has a tenant-bound ordered transcript/checkpoint, immutable
runtime/tool revision set and a monotonic turn sequence. One active turn owns
a compare-and-swap lease. The turn freezes its checkpoint input and executes
in a bounded cell; completion atomically commits output/receipt and advances
the checkpoint. Transcript content is untrusted data and bounded before entry.

Reconnect reads committed events/results; it does not re-execute a turn.
An ambiguous attempt remains unresolved until its original owner provides
evidence or an operator reconciles it—no automatic replay of side effects.
Cancellation reaches that owner, waits for dissolution, and records cancelled
turn state without advancing a successful checkpoint. Resume after cancellation
is a new explicit turn, never a replay hidden behind the same UI interaction.

No guest PVC or arbitrary retained HOME is implied. Workspace starts at none;
later retained/exported data needs explicit admission and retention policy,
bounded immutable inputs/artifacts, and access checked on every read/export.
Coordinate checkpoint/workspace semantics with #408/#409 and Celln #9. Refuse
Celln conversations until their state/turn/cleanup contract is actually delivered.

Applicable success accounting, metering, lifecycle gates, results and user
events should use common control-plane paths. Memory/delegation/Ensemble,
streaming and hooks remain explicit capabilities: implement their mediation or
refuse them. No silently bypassed gate counts as successful integration.

## 6. UX and delivery gates

The user journey is: submit tool → see review/refusal → select approved Harness
→ choose runtime placement → lend explicit approved revisions → review effective
permissions/readiness → run → inspect tool calls, results and correlated audit
→ revoke/cancel. Pending distribution/prewarm is shown as pending, not runnable.
Runtime, tool and placement refusals must appear before launch where possible
and remain actionable after authoritative admission rechecks.

Implement and verify in these slices:

1. Versioned runtime profile/catalogue/submission schemas, RBAC/webhook trust
   boundary and pure resolution/authority tests, including stale/recreated UIDs.
2. Operator review, exact artifact distribution and signed composition/prewarm;
   no Ready status from metadata-only or host-side assertions of guest guarantees.
3. Catalogue-backed one-shot selection and frozen-plan dispatch. Prove an
   admitted user-supplied signed dynamic tool and two selected tools in a real
   model task; reject a third unselected tool with actual guest attempts.
4. Full supported Harness adapter and explicit model/MCP mediation, then the
   external-state conversational turn lifecycle and user-visible completion.
5. Full #426 M4 journey, adversarial/regression suites and measured performance.

M0 release/installer/TLS, interrupted-owner recovery and durable retention gates
remain open independently. Neither this design nor the reference arithmetic
proof completes M1–M4 or authorizes a production-ready claim.
