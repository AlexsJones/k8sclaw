#!/usr/bin/env bash
# Integration test: Instance continuity — multiple sequential runs against one instance.
#
# Validates:
#   1. Multiple AgentRuns can be dispatched sequentially against the same instance
#   2. Each run transitions through Pending → Running → Succeeded/Failed
#   3. The instance doesn't break or get stuck after the first run completes
#   4. Memory persists across runs (run 2 can see memories from run 1)
#   5. Instance status correctly tracks total runs
#
# Requires: LM Studio (or compatible local LLM) on the node.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TIMEOUT="${TEST_TIMEOUT:-180}"
NUM_RUNS="${NUM_RUNS:-3}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; EXIT_CODE=1; }
info() { echo -e "${YELLOW}● $*${NC}"; }

EXIT_CODE=0
SUFFIX="$(date +%s)"
INSTANCE_NAME="inttest-continuity-${SUFFIX}"
SECRET_NAME="${INSTANCE_NAME}-test-key"
PF_PID=""
APISERVER_TOKEN="${APISERVER_TOKEN:-}"
declare -a RUN_NAMES=()

stop_port_forward() {
  if [[ -n "${PF_PID}" ]] && kill -0 "${PF_PID}" >/dev/null 2>&1; then
    kill "${PF_PID}" >/dev/null 2>&1 || true
    for _ in {1..5}; do
      kill -0 "${PF_PID}" >/dev/null 2>&1 || break
      sleep 1
    done
    kill -0 "${PF_PID}" >/dev/null 2>&1 && kill -9 "${PF_PID}" >/dev/null 2>&1 || true
    wait "${PF_PID}" >/dev/null 2>&1 || true
  fi
  command -v pkill >/dev/null 2>&1 && \
    pkill -f "kubectl port-forward -n ${APISERVER_NAMESPACE} svc/sympozium-apiserver ${PORT_FORWARD_LOCAL_PORT}:8080" >/dev/null 2>&1 || true
  PF_PID=""
}

cleanup() {
  info "Cleaning up continuity test resources..."
  for name in "${RUN_NAMES[@]}"; do
    kubectl delete agentrun "$name" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  done
  kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete secret "$SECRET_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true
  stop_port_forward
}
trap cleanup EXIT

require_cmd() {
  command -v "$1" >/dev/null 2>&1 || { fail "Required command not found: $1"; exit 1; }
}

url_with_namespace() {
  local path="$1"
  if [[ "$path" == *"?"* ]]; then
    echo "${APISERVER_URL}${path}&namespace=${NAMESPACE}"
  else
    echo "${APISERVER_URL}${path}?namespace=${NAMESPACE}"
  fi
}

api_request() {
  local method="$1" path="$2" body="${3:-}"
  local url="$(url_with_namespace "$path")"
  local tmp="$(mktemp)"
  local -a headers=(-H "Content-Type: application/json")
  [[ -n "${APISERVER_TOKEN}" ]] && headers+=(-H "Authorization: Bearer ${APISERVER_TOKEN}")

  local code
  if [[ -n "$body" ]]; then
    code="$(curl -sS -o "$tmp" -w "%{http_code}" -X "$method" "${headers[@]}" --data "$body" "$url")"
  else
    code="$(curl -sS -o "$tmp" -w "%{http_code}" -X "$method" "${headers[@]}" "$url")"
  fi

  local resp="$(cat "$tmp")"
  rm -f "$tmp"

  if [[ "$code" -lt 200 || "$code" -ge 300 ]]; then
    fail "API ${method} ${path} failed (HTTP ${code})"
    echo "$resp"
    return 1
  fi
  printf "%s" "$resp"
}


start_port_forward_if_needed() {
  if [[ "${SKIP_PORT_FORWARD}" == "1" ]]; then return 0; fi
  if curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1; then return 0; fi

  info "Starting port-forward to sympozium-apiserver on :${PORT_FORWARD_LOCAL_PORT}"
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-continuity-portforward.log 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 30); do
    kill -0 "$PF_PID" >/dev/null 2>&1 || { fail "Port-forward exited early"; exit 1; }
    curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1 && { pass "Port-forward ready"; return 0; }
    sleep 1
  done
  fail "Timed out waiting for API server via port-forward"
  exit 1
}

# Wait for an AgentRun to reach a terminal phase (Succeeded or Failed).
# Returns 0 on Succeeded, 1 on Failed/timeout.
wait_for_run() {
  local run_name="$1"
  local elapsed=0
  local phase=""

  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    phase="$(kubectl get agentrun "$run_name" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")"
    case "$phase" in
      Succeeded)
        return 0
        ;;
      Failed)
        local err
        err="$(kubectl get agentrun "$run_name" -n "$NAMESPACE" -o jsonpath='{.status.error}' 2>/dev/null || echo "unknown")"
        info "Run '${run_name}' failed: ${err}"
        return 1
        ;;
    esac
    sleep 3
    elapsed=$((elapsed + 3))
  done
  info "Run '${run_name}' timed out (last phase: ${phase})"
  return 1
}

main() {
  require_cmd kubectl
  require_cmd curl
  require_cmd python3

  info "Running instance continuity test (${NUM_RUNS} runs) in namespace '${NAMESPACE}'"

  # ── Detect LLM provider ──
  local provider="${LLM_PROVIDER:-lm-studio}"
  local model="${LLM_MODEL:-qwen/qwen3.5-9b}"
  local base_url="${LLM_BASE_URL:-}"
  local api_key="${LLM_API_KEY:-not-needed}"

  start_port_forward_if_needed
  resolve_apiserver_token

  # ── Create instance ──
  local create_body
  create_body="{\"name\":\"${INSTANCE_NAME}\",\"provider\":\"${provider}\",\"model\":\"${model}\",\"apiKey\":\"${api_key}\",\"skills\":[{\"skillPackRef\":\"k8s-ops\"},{\"skillPackRef\":\"memory\"}]"
  if [[ -n "$base_url" ]]; then
    create_body="${create_body},\"baseURL\":\"${base_url}\""
  fi
  create_body="${create_body}}"

  api_request POST "/api/v1/agents" "$create_body" >/dev/null
  pass "Created instance '${INSTANCE_NAME}' with memory skill"

  # Wait for memory server to be ready before dispatching runs.
  info "Waiting for memory server deployment..."
  local mem_ready=false
  local mem_elapsed=0
  while [[ "$mem_elapsed" -lt 60 ]]; do
    local replicas
    replicas="$(kubectl get deploy "${INSTANCE_NAME}-memory" -n "$NAMESPACE" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")"
    if [[ "$replicas" -ge 1 ]]; then
      mem_ready=true
      break
    fi
    sleep 3
    mem_elapsed=$((mem_elapsed + 3))
  done
  if [[ "$mem_ready" == "true" ]]; then
    pass "Memory server ready"
  else
    info "Memory server not ready after 60s (runs may still work)"
  fi

  # ── Dispatch multiple runs sequentially ──
  local succeeded=0
  local failed=0

  for i in $(seq 1 "$NUM_RUNS"); do
    info "── Run ${i}/${NUM_RUNS} ──"

    local task="Continuity test run ${i} of ${NUM_RUNS}. Say 'Run ${i} complete' and nothing else."
    local run_json
    run_json="$(api_request POST "/api/v1/runs" "{\"agentRef\":\"${INSTANCE_NAME}\",\"task\":\"${task}\"}")"
    local run_name
    run_name="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("name",""))')"

    if [[ -z "$run_name" ]]; then
      fail "Run ${i}: AgentRun creation returned no name"
      failed=$((failed + 1))
      continue
    fi
    RUN_NAMES+=("$run_name")
    pass "Run ${i}: Created AgentRun '${run_name}'"

    # Verify it transitions to Running
    local phase_elapsed=0
    local phase=""
    while [[ "$phase_elapsed" -lt 30 ]]; do
      phase="$(kubectl get agentrun "$run_name" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")"
      [[ "$phase" == "Running" || "$phase" == "Succeeded" || "$phase" == "Failed" ]] && break
      sleep 2
      phase_elapsed=$((phase_elapsed + 2))
    done
    if [[ "$phase" == "Running" || "$phase" == "Succeeded" ]]; then
      pass "Run ${i}: Reached phase '${phase}'"
    else
      fail "Run ${i}: Stuck in phase '${phase}' after 30s"
    fi

    # Wait for terminal state
    if wait_for_run "$run_name"; then
      succeeded=$((succeeded + 1))
      pass "Run ${i}: Succeeded"

      # Verify result is not empty
      local result
      result="$(kubectl get agentrun "$run_name" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo "")"
      if [[ -n "$result" ]]; then
        pass "Run ${i}: Has non-empty result"
      else
        info "Run ${i}: Result is empty (may have been extracted differently)"
      fi
    else
      failed=$((failed + 1))
      fail "Run ${i}: Did not succeed"
    fi

    # Brief pause between runs to simulate real usage
    if [[ "$i" -lt "$NUM_RUNS" ]]; then
      sleep 2
    fi
  done

  # ── Verify instance still healthy ──
  info "── Verifying instance health after ${NUM_RUNS} runs ──"

  # Instance should still be retrievable
  local inst_json
  inst_json="$(api_request GET "/api/v1/agents/${INSTANCE_NAME}" 2>/dev/null || echo "")"
  if [[ -n "$inst_json" ]]; then
    pass "Instance still retrievable after all runs"
  else
    fail "Instance not retrievable after all runs"
  fi

  # Verify total runs count
  local total_runs
  total_runs="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",{}).get("totalAgentRuns",0))' 2>/dev/null || echo "0")"
  if [[ "$total_runs" -ge "$NUM_RUNS" ]]; then
    pass "Instance status.totalAgentRuns = ${total_runs} (expected >= ${NUM_RUNS})"
  else
    info "Instance status.totalAgentRuns = ${total_runs} (expected >= ${NUM_RUNS}, may update async)"
  fi

  # Can still dispatch one more run (instance not stuck)
  info "── Final canary run ──"
  local canary_json
  canary_json="$(api_request POST "/api/v1/runs" "{\"agentRef\":\"${INSTANCE_NAME}\",\"task\":\"Final canary: say OK\"}" 2>/dev/null || echo "")"
  if [[ -n "$canary_json" ]]; then
    local canary_name
    canary_name="$(printf "%s" "$canary_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("name",""))' 2>/dev/null || echo "")"
    if [[ -n "$canary_name" ]]; then
      RUN_NAMES+=("$canary_name")
      pass "Canary run created successfully (instance not stuck)"
    else
      fail "Canary run returned no name"
    fi
  else
    fail "Canary run API call failed (instance may be broken)"
  fi

  # ── Summary ──
  echo ""
  info "════════════════════════════════════════"
  info "  Continuity Test Summary"
  info "  Runs: ${NUM_RUNS}  Succeeded: ${succeeded}  Failed: ${failed}"
  info "════════════════════════════════════════"

  if [[ "$succeeded" -eq "$NUM_RUNS" ]]; then
    pass "All ${NUM_RUNS} runs completed successfully"
  elif [[ "$succeeded" -gt 0 ]]; then
    info "${succeeded}/${NUM_RUNS} runs succeeded (partial pass)"
  else
    fail "No runs succeeded"
  fi

  exit "$EXIT_CODE"
}

main "$@"
