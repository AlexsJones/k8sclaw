#!/usr/bin/env bash
# Billable companion to Celln's opt-in Harness dispatcher fixture.
set -euo pipefail
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
: "${CELLN_CONTROLLER_KUBECONFIG:?explicit isolated Kind kubeconfig required}"
: "${CELLN_PROOF_NAMESPACE:?fixture-owned namespace required}"
: "${CELLN_PROOF_REQUEST:?fixture request required}"
: "${CELLN_PROOF_EVIDENCE:?evidence directory required}"
: "${CELLN_ROUTER_URL:?loopback fixture dispatcher required}"
: "${CELLN_TOKEN_FILE:?fixture token file required}"
export KUBECONFIG="$CELLN_CONTROLLER_KUBECONFIG"
test_context=$(kubectl config current-context)
[[ "$test_context" == kind-celln-m0 || "$test_context" == kind-celln-deployed ]]
[[ "$CELLN_PROOF_NAMESPACE" =~ ^celln-harness-proof-[0-9]+$ ]]
[[ "$CELLN_ROUTER_URL" == http://127.0.0.1:* ]]
work=$(mktemp -d /tmp/sympozium-harness-controller.XXXXXX)
controller_pid=""
owned=false
paused=false
paused_uid=""
cleanup() {
  if [[ -n "$controller_pid" ]]; then kill "$controller_pid" 2>/dev/null || true; wait "$controller_pid" 2>/dev/null || true; fi
  if [[ "$owned" == true ]]; then kubectl delete namespace "$CELLN_PROOF_NAMESPACE" --wait=false; fi
  if [[ "$paused" == true ]]; then
    local state
    state=$(kubectl get deployment harness-proof-controller -n sympozium-system -o json)
    if [[ $(jq -r '.metadata.uid' <<<"$state") == "$paused_uid" ]] && [[ $(jq -r '.spec.replicas' <<<"$state") == 0 ]]; then
      kubectl scale deployment harness-proof-controller -n sympozium-system --current-replicas=0 --replicas=1
      kubectl rollout status deployment harness-proof-controller -n sympozium-system --timeout=60s
    else
      echo 'Test controller changed during proof; refusing to overwrite its state' >&2
      return 1
    fi
  fi
  case "$work" in /tmp/sympozium-harness-controller.*) rm -r -- "$work" ;; esac
}
trap cleanup EXIT
trap 'exit 130' INT TERM
if [[ "$test_context" == kind-celln-deployed ]]; then
  [[ ${CELLN_PAUSE_TEST_CONTROLLER:-} == 1 ]] || { echo 'Explicit test-controller pause required to avoid competing reconcilers' >&2; exit 1; }
  kubectl get agentruns -A -o json | jq -e 'all(.items[]; .status.phase=="Succeeded" or .status.phase=="Failed" or .status.phase=="Cancelled")' >/dev/null
  controller_state=$(kubectl get deployment harness-proof-controller -n sympozium-system -o json)
  jq -e '.spec.replicas==1 and (.spec.template.spec.containers[0].image|startswith("localhost/sympozium-celln-controller:"))' <<<"$controller_state" >/dev/null
  paused_uid=$(jq -r '.metadata.uid' <<<"$controller_state")
  paused=true
  kubectl scale deployment harness-proof-controller -n sympozium-system --current-replicas=1 --replicas=0
  stop_deadline=$((SECONDS+60))
  until kubectl get deployment harness-proof-controller -n sympozium-system -o json | jq -e '(.status.replicas // 0)==0 and .status.observedGeneration>=.metadata.generation' >/dev/null; do
    (( SECONDS < stop_deadline )) || { echo 'Test controller did not stop' >&2; exit 1; }
    sleep 1
  done
fi
version=$(jq -r '.apiVersion' "$CELLN_PROOF_REQUEST")
[[ "$version" == celln.dev/v1alpha2 || "$version" == celln.dev/v1alpha3 ]]
mkdir -p "$CELLN_PROOF_EVIDENCE"
evidence=$(cd "$CELLN_PROOF_EVIDENCE" && pwd)
kubectl apply -f "$repo/config/crd/bases" >/dev/null
kubectl wait --for=condition=Established --timeout=60s crd --all >/dev/null
kubectl create namespace "$CELLN_PROOF_NAMESPACE"
owned=true
jq -n --arg ns "$CELLN_PROOF_NAMESPACE" '{apiVersion:"sympozium.ai/v1alpha1",kind:"Agent",metadata:{name:"reference",namespace:$ns},spec:{agents:{default:{model:"deepseek-chat"}},memory:{enabled:false}}}' | kubectl create -f -
(cd "$repo" && go build -o "$work/controller" ./cmd/controller)
env -u OTEL_EXPORTER_OTLP_ENDPOINT -u SYMPOZIUM_PRICING_CONFIGMAP \
  NATS_URL="" AGENT_SANDBOX_ENABLED=false CELLN_HARNESS_ENABLED=true "$work/controller" \
  --metrics-bind-address=0 --health-probe-bind-address=0 --max-run-history=100 \
  >"$evidence/controller.log" 2>&1 &
controller_pid=$!
jq --arg ns "$CELLN_PROOF_NAMESPACE" '{apiVersion:"sympozium.ai/v1alpha1",kind:"AgentRun",metadata:{name:"harness-proof",namespace:$ns},spec:{agentRef:"reference",agentId:"reference",sessionKey:"harness-proof",backend:"celln",task:.harness.task,systemPrompt:(.harness.json.system // ""),model:{provider:"deepseek",model:.harness.model,authSecretRef:""},timeout:"180s",celln:{mote:.mote,tools:.tools,invocation:.invocation,lane:"agent",capabilities:(.capabilities|del(.timeoutMs)),harness:(.harness|del(.task,.model,.json.system))}}}' "$CELLN_PROOF_REQUEST" >"$evidence/agentrun-request.json"
kubectl create -f "$evidence/agentrun-request.json"
deadline=$((SECONDS+190))
while (( SECONDS < deadline )); do
  kill -0 "$controller_pid"
  kubectl get agentrun harness-proof -n "$CELLN_PROOF_NAMESPACE" -o json >"$evidence/agentrun-status.json"
  if jq -e '.status.phase=="Succeeded" or .status.phase=="Failed"' "$evidence/agentrun-status.json" >/dev/null; then break; fi
  sleep 1
done
jq -e --arg version "$version" '.status.phase=="Succeeded" and (.status.cellnRequest|fromjson|.apiVersion==$version and .harness.model=="deepseek-chat") and (.status.cellnReceipt|fromjson|.apiVersion==$version and .phase=="succeeded")' "$evidence/agentrun-status.json" >/dev/null || { jq '.status' "$evidence/agentrun-status.json"; tail -50 "$evidence/controller.log"; exit 1; }
id=$(jq -r '.status.cellnActionId' "$evidence/agentrun-status.json")
# Token travels via curl stdin config, not process arguments.
node() { printf 'header = "Authorization: Bearer %s"\n' "$(<"$CELLN_TOKEN_FILE")" | curl --config - --fail --silent --show-error --max-time 10 "$CELLN_ROUTER_URL$1"; }
node "/v1/executions/$id/audit" >"$evidence/audit.json"
jq -e --slurpfile status "$evidence/agentrun-status.json" '.receipt==($status[0].status.cellnReceipt|fromjson) and .execution.pilot.lane=="agent" and .execution.broker.requests==3 and .execution.modelGrant==($status[0].status.cellnRequest|fromjson|.harness.modelGrant.hash) and any(.events[];.phase=="Dissolved")' "$evidence/audit.json" >/dev/null
jq -r '.status.result // empty' "$evidence/agentrun-status.json" >"$evidence/result.txt"
if [[ "$version" == celln.dev/v1alpha3 ]]; then
  jq -e --slurpfile request "$CELLN_PROOF_REQUEST" '(.status.cellnRequest|fromjson|.harness)==$request[0].harness' "$evidence/agentrun-status.json" >/dev/null
  jq -e '[.status.result|split("\n")[]|select(startswith("CELLN_HARNESS_EVENT "))|ltrimstr("CELLN_HARNESS_EVENT ")|fromjson] as $events | [$events[]|select(.type=="tool")] as $calls | ($calls|length)==2 and $calls[0].name=="uppercase" and $calls[0].result=={"text":"CELLN"} and $calls[1].name=="length" and $calls[1].arguments=={"text":"CELLN"} and $calls[1].result=={"length":5} and any($events[];.type=="completed" and .answer=="CELLN has length 5") and ([$events[]|select(.type=="model")]|length)==3' "$evidence/agentrun-status.json" >/dev/null
else
rg -q '"answer":"84"' "$evidence/result.txt"
jq -e '[.status.result|split("\n")[]|select(startswith("CELLN_HARNESS_EVENT "))|ltrimstr("CELLN_HARNESS_EVENT ")|fromjson] as $events | [$events[]|select(.type=="tool")] as $calls | ($calls|length)==2 and $calls[0].name=="add" and $calls[0].args==["37","5"] and $calls[0].result=="42\n" and $calls[1].name=="multiply" and $calls[1].args==["42","2"] and $calls[1].result=="84\n" and ([$events[]|select(.type=="model")]|length)==3' "$evidence/agentrun-status.json" >/dev/null
fi
kubectl get jobs -n "$CELLN_PROOF_NAMESPACE" -o json >"$evidence/jobs.json"
jq -e '.items|length==0' "$evidence/jobs.json" >/dev/null
node /v1/node >"$evidence/node.json"
jq -e '.node.live_cells==0' "$evidence/node.json" >/dev/null
jq -n --arg revision "$(git -C "$repo" rev-parse HEAD)" --argjson dirty "$(if [[ -z "$(git -C "$repo" status --porcelain)" ]]; then echo false; else echo true; fi)" --arg controllerSha256 "$(sha256sum "$work/controller" | cut -d' ' -f1)" '{status:"passed",scope:"actual host controller + isolated Kind API + authenticated host dispatcher + KVM; not deployed router topology",revision:$revision,dirty:$dirty,controllerSha256:$controllerSha256}' >"$evidence/summary.json"
echo "PASS: actual Sympozium controller → AgentRun → authenticated Celln → in-cell DeepSeek/two-tool loop; evidence $evidence"
