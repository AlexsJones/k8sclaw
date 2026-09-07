#!/usr/bin/env bash
# API-server schema proof only; no model calls or runtime execution.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${CELLN_CATALOGUE_KUBECONFIG:?explicit isolated kubeconfig required}"
k() { kubectl --kubeconfig "$CELLN_CATALOGUE_KUBECONFIG" "$@"; }
[[ $(k config current-context) == kind-celln-deployed ]] || { echo 'Refusing non-test context' >&2; exit 1; }
namespace="celln-runtime-proof-$$"
k create namespace "$namespace"
cleanup() { k delete namespace "$namespace" --ignore-not-found --wait=false >/dev/null; }
trap cleanup EXIT
k apply -f config/crd/bases/sympozium.ai_agentruntimes.yaml
k wait --for=condition=Established crd/agentruntimes.sympozium.ai --timeout=30s
# Reuse dummy immutable identities; these are metadata, not admitted artifacts.
body=$(jq '{apiVersion:"sympozium.ai/v1alpha1",kind:"AgentRuntime",metadata:{name:"profile"},spec:{image:"example.invalid/harness@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",celln:{revision:"v1",contractVersion:"celln.reference-functions/v1",executable:.spec.executable,closure:.spec.closure,mote:.spec.closure,publisherKey:.spec.publisherKey,entryPoint:"/runtime/harness",platform:"linux/amd64",lane:"agent",lifecycle:"disposable-one-shot",limits:{timeoutMillis:30000,memoryBytes:33554432,taskBytes:2048,outputBytes:4096,workspace:"none"}}}}' test/integration/fixtures/celln-tool-catalogue.json)
k create -n "$namespace" -f - -o json <<<"$body" | jq -e '.spec.celln.contractVersion == "celln.reference-functions/v1" and .spec.celln.limits.taskBytes == 2048' >/dev/null
echo 'PASS runtime profile survives API-server round trip'
jq 'del(.spec.celln) | .metadata.name="oci-only"' <<<"$body" | k create -n "$namespace" --dry-run=server -f - >/dev/null
echo 'PASS existing OCI-only schema remains accepted'
refuse() {
  local input=$1 result
  if result=$(k create -n "$namespace" --dry-run=server -f - <<<"$input" 2>&1); then
    echo 'Unexpected schema acceptance' >&2; exit 1
  fi
  [[ $result == *'is invalid:'* || $result == *'(Invalid)'* ]] || { echo "Wrong refusal: $result" >&2; exit 1; }
}
for patch in \
  '{"contractVersion":"arbitrary-oci/v1"}' \
  '{"executable":{"hash":"mutable"}}' \
  '{"publisherKey":"unapproved-name"}' \
  '{"entryPoint":"/runtime/../escape"}' \
  '{"platform":"linux/arm64"}' \
  '{"lane":"tool"}' \
  '{"lifecycle":"persistent"}' \
  '{"runtimeData":[{"hash":"blake3:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]}' \
  '{"limits":{"taskBytes":2049}}' \
  '{"limits":{"memoryBytes":0}}' \
  '{"limits":{"workspace":"read-write"}}'; do
  refuse "$(jq --argjson patch "$patch" '.metadata.name="invalid-profile" | .spec.celln *= $patch' <<<"$body")"
done
refuse "$(jq 'del(.spec.image) | .metadata.name="celln-only"' <<<"$body")"
echo 'PASS unknown contracts, invalid identities, unsupported authority and missing OCI image refused'
echo 'AgentRuntime CRD remains installed; temporary metadata is removed by cleanup'
