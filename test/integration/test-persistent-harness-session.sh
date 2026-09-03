#!/usr/bin/env bash
# End-to-end proof for the Agent-first persistent harness lifecycle.
# Requires a deployed v1alpha2 Pi runtime and a real model credential.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19091}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19091}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
RUNTIME_NAME="${PERSISTENT_HARNESS_RUNTIME:-pi-session-v0-84-4}"
PROVIDER="${TEST_PROVIDER:-openai}"
MODEL="${TEST_MODEL:-gpt-4o-mini}"
MODEL_BASE_URL="${TEST_BASE_URL:-}"
MODEL_API_KEY="${TEST_API_KEY:-${OPENAI_API_KEY:-}}"
TIMEOUT="${TEST_TIMEOUT:-300}"

STAMP="$(date +%s)"
AGENT_NAME="inttest-persistent-${STAMP}"
SESSION_NAME="${AGENT_NAME}-chat"
BAD_AGENT_NAME="inttest-persistent-bad-${STAMP}"
BAD_SESSION_NAME="${BAD_AGENT_NAME}-chat"
MEMORY_TOKEN="sympozium-${STAMP}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
pass() { echo -e "${GREEN}PASS $*${NC}"; }
fail() { echo -e "${RED}FAIL $*${NC}"; exit 1; }
info() { echo -e "${YELLOW}---- $*${NC}"; }

PF_PID=""
APISERVER_TOKEN="${APISERVER_TOKEN:-}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

url_with_namespace() {
  local path="$1"
  if [[ "$path" == *"?"* ]]; then printf '%s%s&namespace=%s' "$APISERVER_URL" "$path" "$NAMESPACE"; else printf '%s%s?namespace=%s' "$APISERVER_URL" "$path" "$NAMESPACE"; fi
}

api_request() {
  local method="$1" path="$2" body="${3:-}" url
  url="$(url_with_namespace "$path")"
  local -a args=(-fsS -X "$method" -H "Content-Type: application/json")
  [[ -n "$APISERVER_TOKEN" ]] && args+=(-H "Authorization: Bearer ${APISERVER_TOKEN}")
  [[ -n "$body" ]] && args+=(--data "$body")
  curl "${args[@]}" "$url"
}

stop_port_forward() {
  if [[ -n "$PF_PID" ]] && kill -0 "$PF_PID" >/dev/null 2>&1; then kill "$PF_PID" >/dev/null 2>&1 || true; wait "$PF_PID" >/dev/null 2>&1 || true; fi
}

cleanup() {
  info "Cleaning up persistent harness integration resources"
  kubectl delete harnesssession "$SESSION_NAME" "$BAD_SESSION_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete agent "$AGENT_NAME" "$BAD_AGENT_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete secret "${AGENT_NAME}-${PROVIDER}-key" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  stop_port_forward
}
trap cleanup EXIT

wait_for_session_phase() {
  local name="$1" wanted="$2" elapsed=0 phase=""
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    phase="$(kubectl get harnesssession "$name" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    [[ "$phase" == "$wanted" ]] && return 0
    [[ "$phase" == "Failed" && "$wanted" != "Failed" ]] && return 1
    sleep 2; elapsed=$((elapsed + 2))
  done
  return 1
}

for command in kubectl curl jq; do command -v "$command" >/dev/null 2>&1 || fail "required command not found: $command"; done
[[ -n "$MODEL_API_KEY" ]] || fail "set OPENAI_API_KEY or TEST_API_KEY for the real persistent chat proof"
kubectl get crd harnesssessions.sympozium.ai >/dev/null 2>&1 || fail "HarnessSession CRD is not installed"

if [[ "$SKIP_PORT_FORWARD" != "1" ]] && ! curl -fsS "$APISERVER_URL/healthz" >/dev/null 2>&1; then
  info "Starting API server port-forward on ${PORT_FORWARD_LOCAL_PORT}"
  kubectl port-forward -n "$APISERVER_NAMESPACE" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-persistent-harness-portforward.log 2>&1 &
  PF_PID=$!
  for _ in $(seq 1 30); do curl -fsS "$APISERVER_URL/healthz" >/dev/null 2>&1 && break; sleep 1; done
  curl -fsS "$APISERVER_URL/healthz" >/dev/null 2>&1 || fail "API server port-forward did not become ready"
fi
resolve_apiserver_token

info "Installing persistent default runtime into ${NAMESPACE}"
api_request POST /api/v1/runtimes/install-defaults '{}' >/dev/null
kubectl get agentruntime "$RUNTIME_NAME" -n "$NAMESPACE" >/dev/null 2>&1 || fail "persistent runtime ${RUNTIME_NAME} was not installed"
elapsed=0
while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
  RUNTIME_READY="$(kubectl get agentruntime "$RUNTIME_NAME" -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || true)"
  [[ "$RUNTIME_READY" == "True" ]] && break
  sleep 2; elapsed=$((elapsed + 2))
done
[[ "${RUNTIME_READY:-}" == "True" ]] || fail "persistent runtime ${RUNTIME_NAME} did not become Ready"

RUNS_BEFORE="$(kubectl get agentruns -n "$NAMESPACE" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
CREATE_BODY="$(jq -cn --arg name "$AGENT_NAME" --arg provider "$PROVIDER" --arg model "$MODEL" --arg baseURL "$MODEL_BASE_URL" --arg apiKey "$MODEL_API_KEY" --arg runtimeRef "$RUNTIME_NAME" '{name:$name,provider:$provider,model:$model,baseURL:$baseURL,apiKey:$apiKey,runtimeRef:$runtimeRef,policyRef:"harness-examples",skills:[],channels:[]}')"
api_request POST /api/v1/agents "$CREATE_BODY" >/dev/null

kubectl get harnesssession "$SESSION_NAME" -n "$NAMESPACE" >/dev/null 2>&1 || fail "Agent creation did not auto-create ${SESSION_NAME}"
pass "Agent creation auto-created deterministic HarnessSession"

if ! wait_for_session_phase "$SESSION_NAME" Ready; then
  kubectl get harnesssession "$SESSION_NAME" -n "$NAMESPACE" -o yaml || true
  kubectl get pods -n "$NAMESPACE" -l "app.kubernetes.io/instance=${SESSION_NAME}" -o wide || true
  fail "persistent session did not become Ready"
fi
pass "persistent session reached Ready"

FIRST_BODY="$(jq -cn --arg token "$MEMORY_TOKEN" '{messages:[{role:"user",content:("Remember this exact token for the next turn: "+$token+". Reply only STORED.")}]}')"
FIRST_RESPONSE=""
# The Deployment can report Ready just before its Service endpoint update is
# visible to the API server. Retry only that bounded propagation window.
for _ in $(seq 1 10); do
  FIRST_RESPONSE="$(api_request POST "/api/v1/harness-sessions/${SESSION_NAME}/chat" "$FIRST_BODY" 2>/dev/null || true)"
  [[ -n "$FIRST_RESPONSE" ]] && break
  sleep 2
done
if [[ -z "$FIRST_RESPONSE" ]]; then
  kubectl logs deployment/"$SESSION_NAME" -n "$NAMESPACE" --tail=100 || true
  fail "session adapter remained unavailable after initial startup"
fi

PVC_UID="$(kubectl get pvc "$SESSION_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')"
OLD_POD="$(kubectl get pods -n "$NAMESPACE" -l "app.kubernetes.io/instance=${SESSION_NAME}" -o jsonpath='{.items[0].metadata.name}')"
kubectl delete pod "$OLD_POD" -n "$NAMESPACE" --wait=false >/dev/null
elapsed=0; NEW_POD=""
while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
  NEW_POD="$(kubectl get pods -n "$NAMESPACE" -l "app.kubernetes.io/instance=${SESSION_NAME}" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  [[ -n "$NEW_POD" && "$NEW_POD" != "$OLD_POD" ]] && break
  sleep 2; elapsed=$((elapsed + 2))
done
[[ -n "$NEW_POD" && "$NEW_POD" != "$OLD_POD" ]] || fail "session pod was not recreated"
kubectl wait --for=condition=Ready "pod/${NEW_POD}" -n "$NAMESPACE" --timeout="${TIMEOUT}s" >/dev/null || fail "replacement session pod did not become Ready"

SECOND_BODY='{"messages":[{"role":"user","content":"What exact token did I ask you to remember? Reply with the token only."}]}'
SECOND_RESPONSE=""
# A replacement pod can report Ready just before its Service endpoint update
# reaches the API server's connection. Retry that short propagation window.
for _ in $(seq 1 10); do
  SECOND_RESPONSE="$(api_request POST "/api/v1/harness-sessions/${SESSION_NAME}/chat" "$SECOND_BODY" 2>/dev/null || true)"
  [[ -n "$SECOND_RESPONSE" ]] && break
  sleep 2
done
[[ -n "$SECOND_RESPONSE" ]] || fail "session adapter remained unavailable after pod restart"
SECOND_TEXT="$(jq -r '.choices[0].message.content // ""' <<<"$SECOND_RESPONSE")"
[[ "$SECOND_TEXT" == *"$MEMORY_TOKEN"* ]] || fail "conversation state did not survive restart; response: ${SECOND_TEXT}"
[[ "$(kubectl get pvc "$SESSION_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')" == "$PVC_UID" ]] || fail "pod restart replaced the durable state claim"
pass "conversation state survived pod restart"

RUNS_AFTER="$(kubectl get agentruns -n "$NAMESPACE" --no-headers 2>/dev/null | wc -l | tr -d ' ')"
[[ "$RUNS_AFTER" == "$RUNS_BEFORE" ]] || fail "persistent chat created AgentRuns (${RUNS_BEFORE} -> ${RUNS_AFTER})"
pass "persistent chat created no AgentRuns"

info "Proving streaming and explicit stop/resume"
STREAM_BODY='{"stream":true,"messages":[{"role":"user","content":"Reply with STREAM-OK only."}]}'
STREAM_RESPONSE="$(api_request POST "/api/v1/harness-sessions/${SESSION_NAME}/chat" "$STREAM_BODY")"
SSE_EVENTS="$(sed -n 's/^data: //p' <<<"$STREAM_RESPONSE")"
SSE_CONTENT_EVENTS=0
while IFS= read -r event; do
  [[ -n "$event" ]] || continue
  [[ "$event" == "[DONE]" ]] && continue
  jq -e . >/dev/null <<<"$event" || fail "session stream contained invalid JSON: ${event}"
  if jq -e '.choices[0].delta.content | type == "string" and length > 0' >/dev/null <<<"$event"; then
    SSE_CONTENT_EVENTS=$((SSE_CONTENT_EVENTS + 1))
  fi
done <<<"$SSE_EVENTS"
[[ "$SSE_CONTENT_EVENTS" -gt 0 && "$STREAM_RESPONSE" == *'data: [DONE]'* ]] || fail "session stream was not valid SSE: ${STREAM_RESPONSE}"
pass "persistent chat returned SSE content and [DONE]"

api_request PATCH "/api/v1/harness-sessions/${SESSION_NAME}" '{"desiredState":"stopped"}' >/dev/null
wait_for_session_phase "$SESSION_NAME" Draining || fail "session did not stop"
for _ in $(seq 1 30); do
  kubectl get deployment "$SESSION_NAME" -n "$NAMESPACE" >/dev/null 2>&1 || break
  sleep 1
done
kubectl get deployment "$SESSION_NAME" -n "$NAMESPACE" >/dev/null 2>&1 && fail "stopped session retained its Deployment"
kubectl get pvc "$SESSION_NAME" -n "$NAMESPACE" >/dev/null 2>&1 || fail "stopped session lost its durable state claim"
[[ "$(kubectl get pvc "$SESSION_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')" == "$PVC_UID" ]] || fail "stop replaced the durable state claim"
pass "stop removed the workload and preserved durable state"

api_request PATCH "/api/v1/harness-sessions/${SESSION_NAME}" '{"desiredState":"running"}' >/dev/null
wait_for_session_phase "$SESSION_NAME" Ready || fail "stopped session did not resume"
RESUMED_RESPONSE=""
for _ in $(seq 1 10); do
  RESUMED_RESPONSE="$(api_request POST "/api/v1/harness-sessions/${SESSION_NAME}/chat" "$SECOND_BODY" 2>/dev/null || true)"
  [[ -n "$RESUMED_RESPONSE" ]] && break
  sleep 2
done
[[ -n "$RESUMED_RESPONSE" ]] || fail "session adapter remained unavailable after resume"
RESUMED_TEXT="$(jq -r '.choices[0].message.content // ""' <<<"$RESUMED_RESPONSE")"
[[ -n "$RESUMED_TEXT" ]] || fail "session adapter returned no content after resume"
[[ "$(kubectl get pvc "$SESSION_NAME" -n "$NAMESPACE" -o jsonpath='{.metadata.uid}')" == "$PVC_UID" ]] || fail "resume replaced the durable state claim"
pass "conversation state survived explicit stop/resume"

REQUEST_COUNT="$(kubectl get harnesssession "$SESSION_NAME" -n "$NAMESPACE" -o jsonpath='{.status.requestCount}')"
LAST_REQUEST_ID="$(kubectl get harnesssession "$SESSION_NAME" -n "$NAMESPACE" -o jsonpath='{.status.lastRequestID}')"
LAST_REQUEST_STATE="$(kubectl get harnesssession "$SESSION_NAME" -n "$NAMESPACE" -o jsonpath='{.status.lastRequestState}')"
USAGE_ACCOUNTING="$(kubectl get harnesssession "$SESSION_NAME" -n "$NAMESPACE" -o jsonpath='{.status.usageAccounting}')"
[[ "${REQUEST_COUNT:-0}" -ge 4 && -n "$LAST_REQUEST_ID" && "$LAST_REQUEST_STATE" == "succeeded" && "$USAGE_ACCOUNTING" == "unavailable" ]] || fail "request audit status is incomplete"
pass "request IDs, lifecycle counters, and honest unavailable usage are recorded"

info "Proving client disconnect cancellation"
CANCEL_BODY='{"stream":true,"messages":[{"role":"user","content":"Write a detailed 3000 word technical essay. Do not finish early."}]}'
CANCEL_URL="$(url_with_namespace "/api/v1/harness-sessions/${SESSION_NAME}/chat")"
CANCEL_ARGS=(-sS --max-time 1 -X POST -H "Content-Type: application/json")
[[ -n "$APISERVER_TOKEN" ]] && CANCEL_ARGS+=(-H "Authorization: Bearer ${APISERVER_TOKEN}")
curl "${CANCEL_ARGS[@]}" --data "$CANCEL_BODY" "$CANCEL_URL" >/dev/null 2>&1 || true
for _ in $(seq 1 30); do
  LAST_REQUEST_STATE="$(kubectl get harnesssession "$SESSION_NAME" -n "$NAMESPACE" -o jsonpath='{.status.lastRequestState}' 2>/dev/null || true)"
  ACTIVE_REQUESTS="$(kubectl get harnesssession "$SESSION_NAME" -n "$NAMESPACE" -o jsonpath='{.status.activeRequests}' 2>/dev/null || true)"
  [[ "$LAST_REQUEST_STATE" == "cancelled" && "${ACTIVE_REQUESTS:-0}" == "0" ]] && break
  sleep 1
done
[[ "$LAST_REQUEST_STATE" == "cancelled" && "${ACTIVE_REQUESTS:-0}" == "0" ]] || fail "cancelled request remained active or was not audited"
pass "client disconnect cancelled and audited in-flight model work"

info "Proving trustworthy idle timeout"
kubectl patch harnesssession "$SESSION_NAME" -n "$NAMESPACE" --type=merge -p '{"spec":{"idleTimeout":"8s"}}' >/dev/null
wait_for_session_phase "$SESSION_NAME" Draining || fail "idle timeout did not stop the session"
IDLE_REASON="$(kubectl get harnesssession "$SESSION_NAME" -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].reason}')"
[[ "$IDLE_REASON" == "IdleTimeout" ]] || fail "idle timeout stopped without an actionable reason: ${IDLE_REASON}"
kubectl get pvc "$SESSION_NAME" -n "$NAMESPACE" >/dev/null 2>&1 || fail "idle timeout deleted durable state"
pass "idle timeout stopped compute and preserved durable state"

info "Proving actionable reconciliation failure status"
BAD_BODY="$(jq -cn --arg name "$BAD_AGENT_NAME" --arg model "$MODEL" --arg runtimeRef "$RUNTIME_NAME" '{name:$name,provider:"openai",model:$model,runtimeRef:$runtimeRef,policyRef:"harness-examples",skills:[],channels:[]}')"
api_request POST /api/v1/agents "$BAD_BODY" >/dev/null
wait_for_session_phase "$BAD_SESSION_NAME" Failed || fail "invalid persistent Agent did not reach Failed"
FAILURE_MESSAGE="$(kubectl get harnesssession "$BAD_SESSION_NAME" -n "$NAMESPACE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}')"
[[ -n "$FAILURE_MESSAGE" ]] || fail "Failed session has no actionable condition message"
pass "failed reconciliation exposes: ${FAILURE_MESSAGE}"

info "Proving session deletion and owned-resource cleanup"
api_request DELETE "/api/v1/harness-sessions/${SESSION_NAME}" >/dev/null
for _ in $(seq 1 30); do
  kubectl get harnesssession "$SESSION_NAME" -n "$NAMESPACE" >/dev/null 2>&1 || break
  sleep 1
done
kubectl get harnesssession "$SESSION_NAME" -n "$NAMESPACE" >/dev/null 2>&1 && fail "deleted HarnessSession still exists"
for resource in deployment service networkpolicy persistentvolumeclaim; do
  for _ in $(seq 1 30); do
    kubectl get "$resource" "$SESSION_NAME" -n "$NAMESPACE" >/dev/null 2>&1 || break
    sleep 1
  done
  kubectl get "$resource" "$SESSION_NAME" -n "$NAMESPACE" >/dev/null 2>&1 && fail "deleted HarnessSession retained ${resource}/${SESSION_NAME}"
done
pass "deletion removed the session and all owned resources"

pass "persistent harness session integration test complete"
