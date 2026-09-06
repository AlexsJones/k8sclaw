#!/usr/bin/env bash
# Invoked explicitly by Celln's real-KVM fixture. Never uses the user's cluster.
set -euo pipefail
repo=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
: "${CELLN_ROUTER_URL:?isolated fixture URL required}"
: "${CELLN_TOKEN_FILE:?isolated fixture credential required}"
: "${CELLN_PROOF_REQUEST:?pinned fixture request required}"
: "${CELLN_PROOF_EVIDENCE:?evidence directory required}"
: "${CELLN_PROOF_ROOT:?isolated dispatcher root required}"
: "${CELLN_PROOF_BINARY:?actual Celln binary required}"
kind_bin=${CELLN_KIND_BIN:-kind}
for tool in go jq rg kubectl curl podman timeout; do command -v "$tool" >/dev/null; done
[[ -x "$kind_bin" ]] || command -v "$kind_bin" >/dev/null
[[ "$CELLN_ROUTER_URL" == http://127.0.0.1:* ]]
work=$(mktemp -d /tmp/sympozium-celln-proof.XXXXXX)
export KUBECONFIG="$work/kubeconfig"
export KIND_EXPERIMENTAL_PROVIDER=podman
cluster="celln-controller-proof-$$"
controller_pid=""
owned=false
cleanup() {
  if [[ -n "$controller_pid" ]]; then kill "$controller_pid" 2>/dev/null || true; wait "$controller_pid" 2>/dev/null || true; fi
  if [[ "$owned" == true ]]; then "$kind_bin" delete cluster --name "$cluster"; fi
  case "$work" in /tmp/sympozium-celln-proof.*) rm -r -- "$work" ;; esac
}
trap cleanup EXIT
trap 'exit 130' INT TERM
mkdir -p "$CELLN_PROOF_EVIDENCE"
evidence=$(cd "$CELLN_PROOF_EVIDENCE" && pwd)
if "$kind_bin" get clusters | rg -Fx "$cluster"; then echo "refusing existing cluster" >&2; exit 1; fi
owned=true
"$kind_bin" create cluster --name "$cluster" --wait 120s
kubectl create -f "$repo/config/crd/bases"
kubectl wait --for=condition=Established --timeout=60s crd --all
kubectl create namespace celln-proof
jq -n '{apiVersion:"sympozium.ai/v1alpha1",kind:"Agent",metadata:{name:"reference",namespace:"celln-proof"},spec:{agents:{default:{model:"unused"}},memory:{enabled:false}}}' | kubectl create -f -
(cd "$repo" && go build -o "$work/controller" ./cmd/controller)
env -u OTEL_EXPORTER_OTLP_ENDPOINT -u SYMPOZIUM_PRICING_CONFIGMAP \
  NATS_URL="" AGENT_SANDBOX_ENABLED=false "$work/controller" \
  --metrics-bind-address=0 --health-probe-bind-address=0 --max-run-history=100 \
  >"$evidence/controller.log" 2>&1 &
controller_pid=$!

node() { curl --fail --silent --show-error --max-time 10 -H "Authorization: Bearer $(<"$CELLN_TOKEN_FILE")" "$CELLN_ROUTER_URL$1"; }
create_run() {
  local name=$1 mode=$2 timeout_value=$3
  jq --arg name "$name" --arg mode "$mode" --arg timeout "$timeout_value" '
    {apiVersion:"sympozium.ai/v1alpha1",kind:"AgentRun",metadata:{name:$name,namespace:"celln-proof"},spec:{
      agentRef:"reference",agentId:"reference",sessionKey:$name,model:{provider:"openai",model:"unused",authSecretRef:""},backend:"celln",timeout:$timeout,
      celln:{mote:.mote,tools:.tools,inputs:.inputs,invocation:{alias:.invocation.alias,args:[$mode]},lane:"tool",capabilities:(.capabilities|del(.timeoutMs))}}}
    | if $mode == "inputs" then .spec.celln.invocation.args += ["first-input"] else . end
    | if ($mode|startswith("workspace-")) then .spec.celln.inputs=[] | .spec.celln.capabilities.workspace=($mode|ltrimstr("workspace-")) | .spec.celln.invocation.args=["workspace",.spec.celln.capabilities.workspace] else . end
    | if $mode == "unsupported" then .spec.celln.tools[0].closure={hash:.spec.celln.mote.hash} else . end
    | if $mode == "unapproved-input" then .spec.celln.inputs[0].hash=("blake3:"+("b"*64)) else . end
  ' "$CELLN_PROOF_REQUEST" >"$evidence/$name-request.json"
  kubectl create -f "$evidence/$name-request.json"
}
terminal() {
  local name=$1 phase=$2
  local deadline=$((SECONDS+60))
  while (( SECONDS < deadline )); do
    kill -0 "$controller_pid"
    kubectl get agentrun "$name" -n celln-proof -o json >"$evidence/$name-status.json"
    if jq -e '.status.phase == "Succeeded" or .status.phase == "Failed"' "$evidence/$name-status.json" >/dev/null; then break; fi
    sleep 1
  done
  jq -e --arg phase "$phase" '.status.phase == $phase' "$evidence/$name-status.json" >/dev/null || { jq '.status' "$evidence/$name-status.json"; tail -60 "$evidence/controller.log"; return 1; }
}
collect() {
  local name=$1
  local id
  id=$(jq -r '.status.cellnActionId' "$evidence/$name-status.json")
  node "/v1/executions/$id" >"$evidence/$name-result.json"
  node "/v1/executions/$id/audit" >"$evidence/$name-audit.json"
  jq -e --slurpfile status "$evidence/$name-status.json" '
    .requestId == $status[0].status.cellnActionId and .receipt == ($status[0].status.cellnReceipt|fromjson)
    and .caller == ("sympozium:celln-proof/"+$status[0].metadata.name)
    and .execution.cellId == .receipt.cellId and .execution.pilot.lane == "tool"
    and any(.events[]; .phase == "Dissolved")
  ' "$evidence/$name-audit.json" >/dev/null
  node /v1/node >"$evidence/$name-node.json"
  jq -e '.node.live_cells == 0 and .node.memory_bytes == 268435456 and .node.egress_slots == 1' "$evidence/$name-node.json" >/dev/null
}
for mode in silent failed spoof inputs workspace-none workspace-read-only workspace-read-write; do
  create_run "proof-$mode" "$mode" 15s
  phase=Succeeded
  if [[ "$mode" == failed || "$mode" == spoof ]]; then phase=Failed; fi
  terminal "proof-$mode" "$phase"
  collect "proof-$mode"
done
jq -e '.execution.exitCode == 0 and .receipt.output == null' "$evidence/proof-silent-audit.json" >/dev/null
jq -e '.execution.exitCode == 7' "$evidence/proof-failed-audit.json" >/dev/null
jq -e '.execution.exitCode == 9' "$evidence/proof-spoof-audit.json" >/dev/null
jq -e --slurpfile ref "$CELLN_PROOF_REQUEST" '.receipt.resolved.inputs == [$ref[0].inputs[0].hash]' "$evidence/proof-inputs-audit.json" >/dev/null
for mode in unsupported unapproved-input; do
  create_run "proof-$mode" "$mode" 15s
  terminal "proof-$mode" Failed
  jq -e '.status.cellnReceipt == null or .status.cellnReceipt == ""' "$evidence/proof-$mode-status.json" >/dev/null
done
create_run proof-timeout timeout 1s
terminal proof-timeout Failed
collect proof-timeout
jq -e '.execution.watchdogStopped == true' "$evidence/proof-timeout-audit.json" >/dev/null
create_run proof-cancel timeout 30s
deadline=$((SECONDS+30))
while (( SECONDS < deadline )); do
  kubectl get agentrun proof-cancel -n celln-proof -o json >"$evidence/proof-cancel-status.json"
  id=$(jq -r '.status.cellnActionId // ""' "$evidence/proof-cancel-status.json")
  # Audit execution details arrive when the worker returns. The live cell
  # registry, unlike an HTTP reservation, proves a cell was actually forked.
  "$CELLN_PROOF_BINARY" --root "$CELLN_PROOF_ROOT" --json ps >"$evidence/proof-cancel-before.json"
  if [[ -n "$id" ]] && jq -se 'length == 1 and .[0].status == "running" and .[0].description == "reference"' "$evidence/proof-cancel-before.json" >/dev/null; then break; fi
  sleep 1
done
jq -se 'length == 1 and .[0].status == "running"' "$evidence/proof-cancel-before.json" >/dev/null
sleep 0.3
kubectl delete agentrun proof-cancel -n celln-proof --wait=false
kubectl wait --for=delete agentrun/proof-cancel -n celln-proof --timeout=30s
node "/v1/executions/$id/audit" >"$evidence/proof-cancel-audit.json"
jq -e --slurpfile before "$evidence/proof-cancel-before.json" '.receipt.phase == "cancelled" and .receipt.cellId == $before[0].id and .receipt.output.bytes > 0 and any(.events[]; .phase == "Dissolved")' "$evidence/proof-cancel-audit.json" >/dev/null
node /v1/node >"$evidence/final-node.json"
jq -e '.node.live_cells == 0 and .node.memory_bytes == 268435456' "$evidence/final-node.json" >/dev/null
kubectl get jobs -n celln-proof -o json >"$evidence/jobs.json"
jq -e '.items|length == 0' "$evidence/jobs.json" >/dev/null
jq -n --arg revision "$(git -C "$repo" rev-parse HEAD)" --argjson dirty "$(if [[ -z "$(git -C "$repo" status --porcelain)" ]]; then echo false; else echo true; fi)" \
  --arg controllerSha256 "$(sha256sum "$work/controller" | cut -d' ' -f1)" --arg kind "$("$kind_bin" version)" \
  '{suite:"sympozium-real-controller-celln-kvm",status:"passed",revision:$revision,dirty:$dirty,controllerSha256:$controllerSha256,kind:$kind,
  cases:["silent","failed","spoof","inputs","workspace-none","workspace-read-only","workspace-read-write","unsupported","unapproved-input","timeout","delete-cancel"]}' >"$evidence/summary.json"
echo "PASS: production Sympozium controller → Kubernetes AgentRun → authenticated Celln → KVM receipt; evidence $evidence"
