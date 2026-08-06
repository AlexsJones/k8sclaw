#!/usr/bin/env bash
# API integration test: AgentRun pod/container shape correctness.
# Verifies container counts and expected container names for runs created from
# instances with and without skill sidecars.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TIMEOUT="${TEST_TIMEOUT:-180}"

PLAIN_INSTANCE="inttest-shape-plain-$(date +%s)"
SKILL_INSTANCE="inttest-shape-skill-$(date +%s)"
PLAIN_RUN=""
SKILL_RUN=""
PF_PID=""
APISERVER_TOKEN="${APISERVER_TOKEN:-}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; }
info() { echo -e "${YELLOW}● $*${NC}"; }

stop_port_forward() {
  if [[ -n "${PF_PID}" ]] && kill -0 "${PF_PID}" >/dev/null 2>&1; then
    kill "${PF_PID}" >/dev/null 2>&1 || true
    for _ in {1..5}; do
      if ! kill -0 "${PF_PID}" >/dev/null 2>&1; then
        break
      fi
      sleep 1
    done
    if kill -0 "${PF_PID}" >/dev/null 2>&1; then
      kill -9 "${PF_PID}" >/dev/null 2>&1 || true
    fi
    wait "${PF_PID}" >/dev/null 2>&1 || true
  fi

  if command -v pkill >/dev/null 2>&1; then
    pkill -f "kubectl port-forward -n ${APISERVER_NAMESPACE} svc/sympozium-apiserver ${PORT_FORWARD_LOCAL_PORT}:8080" >/dev/null 2>&1 || true
  fi

  PF_PID=""
}

cleanup() {
  info "Cleaning up AgentRun container-shape resources..."
  [[ -n "$PLAIN_RUN" ]] && api_request DELETE "/api/v1/runs/${PLAIN_RUN}" >/dev/null 2>&1 || true
  [[ -n "$SKILL_RUN" ]] && api_request DELETE "/api/v1/runs/${SKILL_RUN}" >/dev/null 2>&1 || true
  api_request DELETE "/api/v1/agents/${PLAIN_INSTANCE}" >/dev/null 2>&1 || true
  api_request DELETE "/api/v1/agents/${SKILL_INSTANCE}" >/dev/null 2>&1 || true
  # kubectl fallback: secrets, agentruns, configmaps, instances
  kubectl delete secret "${PLAIN_INSTANCE}-openai-key" "${SKILL_INSTANCE}-openai-key" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  [[ -n "$PLAIN_RUN" ]] && kubectl delete agentrun "$PLAIN_RUN" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  [[ -n "$SKILL_RUN" ]] && kubectl delete agentrun "$SKILL_RUN" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete sympoziuminstance "$PLAIN_INSTANCE" "$SKILL_INSTANCE" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${PLAIN_INSTANCE}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${SKILL_INSTANCE}" --ignore-not-found >/dev/null 2>&1 || true
  stop_port_forward
}
trap cleanup EXIT

require_cmd() { command -v "$1" >/dev/null 2>&1 || { fail "Missing command: $1"; exit 1; }; }

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
  [[ -n "$APISERVER_TOKEN" ]] && headers+=(-H "Authorization: Bearer ${APISERVER_TOKEN}")

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
  [[ "$SKIP_PORT_FORWARD" == "1" ]] && return 0
  curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1 && return 0

  info "Starting port-forward to sympozium-apiserver on :${PORT_FORWARD_LOCAL_PORT}"
  kubectl port-forward -n "$APISERVER_NAMESPACE" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-api-shape-portforward.log 2>&1 &
  PF_PID=$!
  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-api-shape-portforward.log || true
      exit 1
    fi
    if curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1; then
      pass "Port-forward ready"
      return 0
    fi
    sleep 1
  done
  fail "Timed out waiting for API server via port-forward"
  exit 1
}

wait_for_pod_name() {
  local run_name="$1"
  local elapsed=0
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    run_json="$(api_request GET "/api/v1/runs/${run_name}" 2>/dev/null || true)"
    if [[ -n "$run_json" ]]; then
      pod_name="$(printf "%s" "$run_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",{}).get("podName", ""))')"
      if [[ -n "$pod_name" ]]; then
        printf "%s" "$pod_name"
        return 0
      fi
    fi
    sleep 5
    elapsed=$((elapsed + 5))
  done
  return 1
}

assert_pod_shape() {
  local pod_name="$1" expected_count="$2" expected_csv="$3" scope="$4"

  local names
  names="$(kubectl get pod "$pod_name" -n "$NAMESPACE" -o jsonpath='{range .spec.containers[*]}{.name}{"\n"}{end}' | sort)"
  local count
  count="$(printf "%s\n" "$names" | sed '/^$/d' | wc -l | tr -d ' ')"
  local got_csv
  got_csv="$(printf "%s\n" "$names" | sed '/^$/d' | paste -sd ',' -)"

  [[ "$count" == "$expected_count" ]] || { fail "$scope: container count mismatch ($count != $expected_count)"; echo "$got_csv"; exit 1; }
  [[ "$got_csv" == "$expected_csv" ]] || { fail "$scope: container names mismatch ($got_csv != $expected_csv)"; exit 1; }
}

main() {
  require_cmd kubectl
  require_cmd curl
  require_cmd python3

  info "Running AgentRun container-shape test in namespace '${NAMESPACE}'"

  if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    fail "OPENAI_API_KEY environment variable is required but not set"
    exit 1
  fi

  start_port_forward_if_needed
  resolve_apiserver_token

  api_request POST "/api/v1/agents" "{\"name\":\"${PLAIN_INSTANCE}\",\"provider\":\"openai\",\"model\":\"gpt-4o-mini\",\"apiKey\":\"${OPENAI_API_KEY}\"}" >/dev/null
  api_request POST "/api/v1/agents" "{\"name\":\"${SKILL_INSTANCE}\",\"provider\":\"openai\",\"model\":\"gpt-4o-mini\",\"apiKey\":\"${OPENAI_API_KEY}\",\"skills\":[{\"skillPackRef\":\"k8s-ops\"}]}" >/dev/null

  plain_run_json="$(api_request POST "/api/v1/runs" "{\"agentRef\":\"${PLAIN_INSTANCE}\",\"task\":\"pod shape plain\"}")"
  PLAIN_RUN="$(printf "%s" "$plain_run_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("metadata",{}).get("name",""))')"
  skill_run_json="$(api_request POST "/api/v1/runs" "{\"agentRef\":\"${SKILL_INSTANCE}\",\"task\":\"pod shape skill\"}")"
  SKILL_RUN="$(printf "%s" "$skill_run_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("metadata",{}).get("name",""))')"

  plain_pod="$(wait_for_pod_name "$PLAIN_RUN" || true)"
  [[ -n "$plain_pod" ]] || { fail "Timed out waiting for podName on plain run"; exit 1; }
  skill_pod="$(wait_for_pod_name "$SKILL_RUN" || true)"
  [[ -n "$skill_pod" ]] || { fail "Timed out waiting for podName on skill run"; exit 1; }

  assert_pod_shape "$plain_pod" "2" "agent,ipc-bridge" "plain instance run"
  pass "Plain instance run has expected containers: agent, ipc-bridge"

  assert_pod_shape "$skill_pod" "3" "agent,ipc-bridge,skill-k8s-ops" "skill instance run"
  pass "Skill-backed run has expected sidecar container shape"

  pass "AgentRun container-shape test passed"
}

main "$@"
