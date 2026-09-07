# Experimental Celln executable-tool catalogue metadata

Implements the first schema/RBAC slice of #335 and epic #426. This is **not an
executable catalogue yet**: an [operator review CLI](celln-tool-review.md) can
publish locally verified metadata, but no verifier/distribution controller, lending
resolver, API-server submission endpoint or selection UI is connected. Neither
resource creation nor status metadata grants Celln execution authority.

## Resources and ownership

`CellnToolSubmission` is namespace-scoped user intent. `CellnTool` is an
operator-managed catalogue revision. Both use an immutable spec and separate
status subresource; creating a submission with forged status does not preserve
that status. There is no default Approved/Ready condition.

Each spec includes a revision, description, support owner, publisher key,
executable/closure BLAKE3 references, canonical ASCII guest entry point,
invocation ABI, argument/result schema references, platform/lane and bounds.
Optional digest-pinned OCI source identity is provenance only; nothing fetches
or executes that image. Publisher keys and hashes here are declarations, not
verified signatures or behavior admission. Host verification remains necessary.

Schema references identify immutable JSON-schema documents that a future
verifier must fetch, hash-check and validate against the implemented dialect.
This API slice checks reference shape, not JSON-schema semantics or actual tool
arguments. Supported metadata ABIs are `celln.argv/v1` and the separately
versioned `celln.json-stdio/v1` on Linux/amd64. JSON descriptions and deadlines
must fit the native adapter's 512-character and 30-second schema ceilings;
the resolver additionally enforces description byte length. See the
[JSON binding guide](celln-json-harness.md) for current execution scope.
The current closed profile requires workspace none and refuses nonempty egress
or immutable-input requirements; those need delivery support before expansion.
No requested unsupported authority is silently translated into an execution.

Specs cannot be patched, even to reduce limits. Publish a new revision and
resolve its new UID/hash identity explicitly. Revocation will be a separate
policy/controller action, not mutation of a frozen executable revision.

The chart renders three **unbound** ClusterRoles:

| Role suffix | Allowed operations |
| --- | --- |
| `celln-tool-submitter` | Create/read/list/watch submissions only |
| `celln-tool-reviewer` | Read submissions; create/read/list/watch/delete catalogue entries |
| `celln-tool-verifier` | Read both resources; update/patch their status only |

Administrators may bind these through namespace-scoped RoleBindings. None is
automatically granted to API servers, agents, runtimes or existing controllers.
Do not use cluster-wide bindings for tenant submission. The verifier role is
trusted status authority, not a user role; even its future Ready status must
bind observed UID/generation, artifact and policy revisions before use.

## Verification

The portable API/controller/webhook regressions still apply. The no-model
metadata integration test requires an explicit isolated kubeconfig:

```sh
CELLN_CATALOGUE_KUBECONFIG=/absolute/path/to/isolated/kubeconfig \
  bash test/integration/test-celln-tool-catalogue.sh
```

It refuses any context other than `kind-celln-deployed`, applies only the two
catalogue CRDs and chart-rendered unbound roles, creates a fresh test namespace,
and uses service-account impersonation against the real API server. It tests
submission creation with status stripping, self-publication/status denial,
cross-namespace submission denial, reviewer/verifier separation, immutable
revisions, invalid hashes/paths/images and refused egress/zero-memory bounds.

Passed against isolated Kubernetes v1.35.0 on 2026-09-07. Temporary namespaces,
catalogue entries, submissions, service accounts, bindings and test roles were
removed. CRDs remain installed; no tools, cells, Jobs or model calls were created.
Fixture hashes are intentionally dummy metadata, not admitted executable bytes.
This is Kubernetes schema/RBAC evidence, not guest-isolation or BYO execution
evidence. Operator review and pure authority resolution are now available;
conformance, trusted dispatch integration and distribution remain next steps.
