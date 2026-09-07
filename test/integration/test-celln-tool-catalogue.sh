#!/usr/bin/env bash
# Metadata/CEL/RBAC proof only. No model calls, tool execution or host changes.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${CELLN_CATALOGUE_KUBECONFIG:?explicit isolated kubeconfig required}"
k() { kubectl --kubeconfig "$CELLN_CATALOGUE_KUBECONFIG" "$@"; }
[[ $(k config current-context) == kind-celln-deployed ]] || { echo 'Refusing non-test context' >&2; exit 1; }
fixture=test/integration/fixtures/celln-tool-catalogue.json
namespace="celln-catalogue-proof-$$"
release="catalogue-proof-$$"
role_prefix="$release-sympozium-celln-tool"
k create namespace "$namespace"
cleanup() {
  k delete namespace "$namespace" --ignore-not-found --wait=false >/dev/null
  k delete clusterrole "$role_prefix-submitter" "$role_prefix-reviewer" "$role_prefix-verifier" --ignore-not-found >/dev/null
}
trap cleanup EXIT
k apply -f config/crd/bases/sympozium.ai_cellntools.yaml -f config/crd/bases/sympozium.ai_cellntoolsubmissions.yaml
helm template "$release" charts/sympozium --show-only templates/celln-tool-rbac.yaml | k apply -f -
for role in submitter reviewer verifier; do
  k create serviceaccount "$role" -n "$namespace"
  k create rolebinding "$role" -n "$namespace" --clusterrole="$role_prefix-$role" --serviceaccount="$namespace:$role"
done
as_user() { local role=$1; shift; k --as="system:serviceaccount:$namespace:$role" "$@"; }
refuse() {
  local expected=$1; shift
  local result
  if result=$("$@" 2>&1); then echo "Unexpected acceptance: $*" >&2; exit 1; fi
  [[ $result == *"$expected"* ]] || { echo "Wrong refusal: $result" >&2; exit 1; }
  echo "PASS refusal: $expected"
}
jq '.kind="CellnToolSubmission" | .status={conditions:[{type:"Ready",status:"True",reason:"Forged",message:"must be dropped",lastTransitionTime:"2026-09-07T00:00:00Z"}]}' "$fixture" |
  as_user submitter create -n "$namespace" -f - -o json |
  jq -e '(.status.conditions // [] | length) == 0' >/dev/null
echo 'PASS submission grants no Ready status'
refuse Forbidden as_user submitter create -n "$namespace" -f "$fixture"
refuse Forbidden as_user submitter patch cellntoolsubmission fixture-v1 -n "$namespace" --subresource=status --type=merge -p '{"status":{"observedGeneration":1}}'
jq '.kind="CellnToolSubmission"' "$fixture" |
  refuse Forbidden as_user submitter create -n default -f -
as_user reviewer create -n "$namespace" -f "$fixture"
refuse Forbidden as_user reviewer patch cellntool fixture-v1 -n "$namespace" --subresource=status --type=merge -p '{"status":{"observedGeneration":1}}'
refuse Forbidden as_user verifier create -n "$namespace" -f "$fixture" --dry-run=server
as_user verifier patch cellntool fixture-v1 -n "$namespace" --subresource=status --type=merge -p '{"status":{"observedGeneration":1}}'
refuse 'tool revisions are immutable' k patch cellntool fixture-v1 -n "$namespace" --type=merge -p '{"spec":{"revision":"v2"}}'
for patch in \
  '{"executable":{"hash":"mutable"}}' \
  '{"entryPoint":"/tools/../escape"}' \
  '{"sourceImage":"example:latest"}' \
  '{"limits":{"egress":["https://example.com"]}}' \
  '{"limits":{"memoryBytes":0}}'; do
  # A fresh name avoids conflating schema validation with immutable-update tests.
  body=$(jq --argjson patch "$patch" '.metadata.name="invalid-fixture" | .spec *= $patch' "$fixture")
  refuse Invalid as_user reviewer create -n "$namespace" --dry-run=server -f - <<<"$body"
done
echo 'PASS catalogue schema, immutable revisions and namespace-scoped role separation'
echo 'CRDs remain installed; test catalogue/submissions/roles are removed by cleanup'
