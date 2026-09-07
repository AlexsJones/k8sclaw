#!/usr/bin/env bash
# Real API-server schema/CEL proof. No AgentRun is persisted or executed.
set -euo pipefail
cd "$(dirname "$0")/../.."
: "${CELLN_CATALOGUE_KUBECONFIG:?explicit isolated kubeconfig required}"
k() { kubectl --kubeconfig "$CELLN_CATALOGUE_KUBECONFIG" "$@"; }
[[ $(k config current-context) == kind-celln-deployed ]] || { echo 'Refusing non-test context' >&2; exit 1; }
namespace="celln-json-schema-proof-$$"
k create namespace "$namespace"
cleanup() { k delete namespace "$namespace" --ignore-not-found --wait=false >/dev/null; }
trap cleanup EXIT
for resource in agentruns agentruntimes cellntools cellntoolsubmissions; do
  k apply -f "config/crd/bases/sympozium.ai_$resource.yaml" >/dev/null
  k wait --for=condition=Established "crd/$resource.sympozium.ai" --timeout=30s >/dev/null
done
tool=$(jq '.spec.invocationABI="celln.json-stdio/v1"' test/integration/fixtures/celln-tool-catalogue.json)
k create -n "$namespace" -f - -o json <<<"$tool" | jq -e '.spec.invocationABI=="celln.json-stdio/v1"' >/dev/null
jq '.kind="CellnToolSubmission"' <<<"$tool" | k create -n "$namespace" -f - -o json | jq -e '.spec.invocationABI=="celln.json-stdio/v1"' >/dev/null
runtime=$(jq '{apiVersion:"sympozium.ai/v1alpha1",kind:"AgentRuntime",metadata:{name:"json-profile"},spec:{image:"example.invalid/harness@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",celln:{revision:"v1",contractVersion:"celln.json-tools/v1",executable:.spec.executable,closure:.spec.closure,mote:.spec.closure,publisherKey:.spec.publisherKey,entryPoint:"/harness",platform:"linux/amd64",lane:"agent",lifecycle:"disposable-one-shot",json:{maxTurns:3,maxCalls:2},limits:{timeoutMillis:30000,memoryBytes:33554432,taskBytes:2048,outputBytes:4096,workspace:"none"}}}}' <<<"$tool")
k create -n "$namespace" -f - -o json <<<"$runtime" | jq -e '.spec.celln.json.maxTurns==3 and .spec.celln.contractVersion=="celln.json-tools/v1"' >/dev/null
run=$(jq '{apiVersion:"sympozium.ai/v1alpha1",kind:"AgentRun",metadata:{name:"json-schema-only"},spec:{agentRef:"not-created",agentId:"test",sessionKey:"schema-proof",backend:"celln",task:.harness.task,systemPrompt:.harness.json.system,model:{provider:"deepseek",model:.harness.model,authSecretRef:""},timeout:"180s",celln:{mote:.mote,tools:.tools,invocation:.invocation,lane:"agent",capabilities:(.capabilities|del(.timeoutMs)),harness:(.harness|del(.task,.model,.json.system))}}}' test/integration/fixtures/celln-json-dispatch-request.json)
k create -n "$namespace" --dry-run=server -f - -o json <<<"$run" | jq -e '.spec.celln.harness.json.maxTurns==3 and .spec.celln.harness.borrowedTools[0].jsonStdio.abi=="celln.json-stdio/v1" and .spec.systemPrompt=="Use the explicitly lent tools."' >/dev/null
jq '.spec.celln.harness.borrowedTools=[]' <<<"$run" | k create -n "$namespace" --dry-run=server -f - >/dev/null
echo 'PASS JSON catalogue/submission/runtime round trips and dry-run AgentRun including empty selection'
refuse() {
  local body=$1 result
  if result=$(k create -n "$namespace" --dry-run=server -f - <<<"$body" 2>&1); then
    echo 'Unexpected schema acceptance' >&2; exit 1
  fi
  [[ $result == *'is invalid:'* || $result == *'(Invalid)'* ]] || { echo "Wrong refusal: $result" >&2; exit 1; }
}
for filter in \
  'del(.spec.celln.harness.json)' \
  '.spec.celln.harness.contractVersion="celln.reference-functions/v1"' \
  'del(.spec.celln.harness.borrowedTools[0].jsonStdio)' \
  '.spec.celln.harness.json.maxTurns=7' \
  '.spec.celln.harness.json.maxCalls=17' \
  '.spec.celln.harness.borrowedTools[0].jsonStdio.abi="celln.argv/v1"' \
  '.spec.celln.harness.borrowedTools[0].jsonStdio.inputSchema="mutable"' \
  '.spec.celln.harness.borrowedTools[0].jsonStdio.outputBytes=65537' \
  '.spec.celln.harness.borrowedTools[0].jsonStdio.timeoutMs=30001'; do
  refuse "$(jq "$filter" <<<"$run")"
done
for filter in 'del(.spec.celln.json)' '.spec.celln.contractVersion="celln.reference-functions/v1"' '.spec.celln.json.maxTurns=0'; do
  refuse "$(jq '.metadata.name="invalid-profile" | '"$filter" <<<"$runtime")"
done
for filter in '.spec.limits.timeoutMillis=30001' '.spec.description=("x"*513)' '.spec.invocationABI="unknown"'; do
  refuse "$(jq '.metadata.name="invalid-tool" | '"$filter" <<<"$tool")"
done
echo 'PASS 15 mixed-contract/schema/limit refusals; no execution or positive readiness claimed'
