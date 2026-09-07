# Independent Celln model authority

Tool approval does not grant permission to spend a model credential. The
control-plane `cellnauthority.ModelLoader` reads a separate operator-configured
ConfigMap, with its document in `data["model-policy.json"]`. Its location must
come from trusted deployment configuration, not an AgentRun. RBAC must deny
tenant writes; labels and same-named tenant ConfigMaps do not prove ownership.
The model policy source must differ from each of the three tool-grant sources.

The `sympozium.ai/celln-model-policy-v1` document contains:

- `agent` and `runtime`: complete `Subject` identities from the reviewed
  selection (namespace, name, UID, generation and full-spec SHA256).
- `provider`, `model`, `url`: exact model authority. The currently supported
  contract is `deepseek`, an explicit model, and
  `https://api.deepseek.com/chat/completions`.
- `credentialProfile`: an opaque 1–64 character alphanumeric/underscore/hyphen
  name for an independently configured host credential mapping. Not a path,
  secret value, Kubernetes Secret reference, or authorization by itself.
- `maxRequests`: 1–6, at least the selected runtime's maximum turns.
- `maxOutputTokens`: 512, matching the current host broker contract.
- `maxTotalOutputTokens`: 512–3072. A smaller total may exhaust before all
  turns complete; it must never be silently expanded.

Resolution revalidates the frozen selection, reads the policy, compares the
live run's full identity and explicit model options, then revalidates selection
and policy revision again. Nonempty tenant credential references, provider
headers, model discovery, node selectors and unsupported thinking modes refuse
instead of falling back to an ambient provider or credential. Documents are
bounded to 64 KiB and unknown fields or trailing JSON refuse.

The resulting `sympozium.ai/celln-model-approval-v1` record pins the complete
frozen-selection SHA256, run-derived caller, policy content and source
namespace/name/UID/resourceVersion/exact-document SHA256. Revalidation compares
the entire record and never substitutes a new plan or execution ID.

Operators can add `--model-policy CONFIGMAP` to `celln-tool plan` or
`celln-tool compose`, along with `--run` and the existing grant-source flags.
The policy resides in `--grant-namespace`. Composition revalidates model
approval after building artifacts. These commands print a `modelApproval`
observation and keep `executionAuthorized: false`.

## Remaining issuance boundary

This implementation reads no credentials, writes no Kubernetes resources or
host grant files, and performs no model calls. Tests use a fake Kubernetes API;
they are not deployment/RBAC or live-model proof. Observed rereads are not an
atomic transaction, lease or fleet-wide withdrawal guarantee.

The host issuer must still independently authenticate its control-plane caller,
resolve `credentialProfile` through operator-owned host configuration, validate
the actual admitted/prewarmed mote and composed closure, bind exact runtime,
borrowed tools, persona and budgets, and publish a content-addressed grant only
after fresh policy checks. Distribution, issuer/controller integration and the
catalogue-backed real-model E2E remain required. An uploaded approval JSON or
successful prewarm observation alone must never create model authority.
