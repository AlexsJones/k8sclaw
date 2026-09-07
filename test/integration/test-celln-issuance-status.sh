#!/usr/bin/env bash
# Real API-server transition proof; no Agent, Job or model execution is created.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${CELLN_CATALOGUE_KUBECONFIG:?explicit isolated kubeconfig required}"
k() { kubectl --kubeconfig "$CELLN_CATALOGUE_KUBECONFIG" "$@"; }
[[ $(k config current-context) == kind-celln-deployed ]] || { echo 'Refusing non-test context' >&2; exit 1; }
k apply -f config/crd/bases/sympozium.ai_agentruns.yaml >/dev/null
k wait --for=condition=Established crd/agentruns.sympozium.ai --timeout=30s >/dev/null
namespace="celln-issuance-schema-$$"
k create namespace "$namespace" >/dev/null
cleanup() { k delete namespace "$namespace" --ignore-not-found --wait=false >/dev/null; }
trap cleanup EXIT
body='{"apiVersion":"sympozium.ai/v1alpha1","kind":"AgentRun","metadata":{"name":"status-only"},"spec":{"agentRef":"not-created","agentId":"schema","sessionKey":"schema","backend":"celln","task":"schema only","model":{"provider":"deepseek","model":"deepseek-chat","authSecretRef":""}}}'
k create -n "$namespace" -f - <<<"$body" >/dev/null
prepared='{"phase":"Prepared","target":"https://issuer.example/v1/issuances","payload":"{}","payloadSHA256":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'
patch=$(jq -n --argjson saved "$prepared" '{status:{cellnIssuance:$saved}}')
k patch agentrun status-only -n "$namespace" --subresource=status --type=merge -p "$patch" >/dev/null
k get agentrun status-only -n "$namespace" -o json | jq -e '.status.cellnIssuance.phase=="Prepared"' >/dev/null
refuse() {
  local patch=$1 output
  if output=$(k patch agentrun status-only -n "$namespace" --subresource=status --type=merge --dry-run=server -p "$patch" 2>&1); then
    echo 'Unexpected issuance status acceptance' >&2; exit 1
  fi
  [[ $output == *'is invalid:'* || $output == *'(Invalid)'* ]] || { echo "Wrong refusal: $output" >&2; exit 1; }
}
refuse '{"status":{"cellnIssuance":{"target":"https://other.example/v1/issuances"}}}'
refuse '{"status":{"cellnIssuance":{"payload":"changed"}}}'
refuse '{"status":{"cellnIssuance":{"payloadSHA256":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}'
refuse '{"status":{"cellnIssuance":null}}'
refuse '{"status":null}'
refuse '{"status":{"cellnIssuance":{"phase":"Issued"}}}'
refuse '{"status":{"cellnIssuance":{"result":"{}"}}}'
k patch agentrun status-only -n "$namespace" --subresource=status --type=merge -p '{"status":{"cellnIssuance":{"phase":"Issued","result":"{}"}}}' >/dev/null
refuse '{"status":{"cellnIssuance":{"phase":"Prepared","result":null}}}'
refuse '{"status":{"cellnIssuance":{"result":"changed"}}}'
refuse '{"status":{"cellnIssuance":{"result":null}}}'
k get agentrun status-only -n "$namespace" -o json | jq -e '.status.cellnIssuance.phase=="Issued" and .status.cellnIssuance.result=="{}" and (.status.cellnActionId // "")==""' >/dev/null
[[ $(k get jobs -n "$namespace" -o json | jq '.items|length') == 0 ]]
echo 'PASS real API-server prepared/issued transition and 10 immutable-state refusals; zero Jobs/model calls'
