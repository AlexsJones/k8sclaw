#!/usr/bin/env bash
# API integration test: schedule dispatch behavior.
# Validates that creating a schedule via API eventually creates an AgentRun.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TIMEOUT="${TEST_TIMEOUT:-180}"

INSTANCE_NAME="inttest-api-dispatch-instance-$(date +%s)"
SCHEDULE_NAME="inttest-api-dispatch-schedule-$(date +%s)"
SECRET_NAME="${INSTANCE_NAME}-openai-key"
RUN_NAME=""

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; }
info() { echo -e "${YELLOW}● $*${NC}"; }

PF_PID=""
APISERVER_TOKEN="${APISERVER_TOKEN:-}"

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
  info "Cleaning up schedule-dispatch API test resources..."
  [[ -n "$RUN_NAME" ]] && api_request DELETE "/api/v1/runs/${RUN_NAME}" >/dev/null 2>&1 || true
  api_request DELETE "/api/v1/schedules/${SCHEDULE_NAME}" >/dev/null 2>&1 || true
  api_request DELETE "/api/v1/agents/${INSTANCE_NAME}" >/dev/null 2>&1 || true
  # kubectl fallback: agentruns (schedule may dispatch multiple), schedule, instance, secret, configmaps
  kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete sympoziumschedule "$SCHEDULE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
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
  local method="$1"
  local path="$2"
  local body="${3:-}"
  local url="$(url_with_namespace "$path")"
  local tmp="$(mktemp)"
  local -a headers
  headers=(-H "Content-Type: application/json")
  if [[ -n "${APISERVER_TOKEN}" ]]; then
    headers+=(-H "Authorization: Bearer ${APISERVER_TOKEN}")
  fi

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
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-api-dispatch-portforward.log 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-api-dispatch-portforward.log || true
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

main() {
  require_cmd kubectl
  require_cmd curl
  require_cmd python3

  info "Running schedule-dispatch API test in namespace '${NAMESPACE}'"

  if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    fail "OPENAI_API_KEY environment variable is required but not set"
    exit 1
  fi

  start_port_forward_if_needed
  resolve_apiserver_token

  # Provide apiKey so the apiserver creates/auth-wires a provider secret; this
  # lets us assert schedule->run inheritance of provider/auth metadata.
  api_request POST "/api/v1/agents" "{\"name\":\"${INSTANCE_NAME}\",\"provider\":\"openai\",\"model\":\"gpt-4o-mini\",\"apiKey\":\"${OPENAI_API_KEY}\"}" >/dev/null
  pass "Created ad-hoc instance '${INSTANCE_NAME}'"

  api_request POST "/api/v1/schedules" "{\"name\":\"${SCHEDULE_NAME}\",\"agentRef\":\"${INSTANCE_NAME}\",\"schedule\":\"* * * * *\",\"task\":\"dispatch smoke\",\"type\":\"scheduled\"}" >/dev/null
  pass "Created schedule '${SCHEDULE_NAME}'"

  # SympoziumSchedule is implemented by the Sympozium controller, not by
  # Kubernetes CronJob. Verify no native CronJob was created.
  if kubectl get cronjob "$SCHEDULE_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    fail "Unexpected Kubernetes CronJob '${SCHEDULE_NAME}' exists (expected Sympozium controller dispatch)"
    exit 1
  fi
  pass "No Kubernetes CronJob created (Sympozium schedule controller path)"

  elapsed=0
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    sched_json="$(api_request GET "/api/v1/schedules/${SCHEDULE_NAME}")"

    total_runs="$(printf "%s" "$sched_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",{}).get("totalRuns",0))')"
    RUN_NAME="$(printf "%s" "$sched_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",{}).get("lastRunName", ""))')"

    if [[ "$total_runs" -ge 1 && -n "$RUN_NAME" ]]; then
      pass "Schedule dispatched run '${RUN_NAME}' (totalRuns=${total_runs})"
      break
    fi

    sleep 5
    elapsed=$((elapsed + 5))
  done

  if [[ "$elapsed" -ge "$TIMEOUT" ]]; then
    fail "Timed out waiting for schedule dispatch"
    exit 1
  fi

  runs_json="$(api_request GET "/api/v1/runs")"
  found_run="$(printf "%s" "$runs_json" | python3 -c 'import json,sys; target=sys.argv[1]; d=json.load(sys.stdin); print("true" if any(i.get("metadata",{}).get("name")==target for i in d) else "false")' "$RUN_NAME")"
  if [[ "$found_run" != "true" ]]; then
    fail "Run '${RUN_NAME}' not found in /api/v1/runs"
    exit 1
  fi

  # Validate Sympozium-specific run metadata produced by schedule controller.
  run_json="$(api_request GET "/api/v1/runs/${RUN_NAME}")"

  run_schedule_label="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("labels",{}).get("sympozium.ai/schedule",""))')"
  run_instance_label="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("labels",{}).get("sympozium.ai/instance",""))')"
  run_model_provider="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("provider", ""))')"
  run_auth_secret="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("authSecretRef", ""))')"

  if [[ "$run_schedule_label" != "$SCHEDULE_NAME" ]]; then
    fail "Run missing/incorrect schedule label: got '${run_schedule_label}', want '${SCHEDULE_NAME}'"
    exit 1
  fi
  if [[ "$run_instance_label" != "$INSTANCE_NAME" ]]; then
    fail "Run missing/incorrect instance label: got '${run_instance_label}', want '${INSTANCE_NAME}'"
    exit 1
  fi
  if [[ "$run_model_provider" != "openai" ]]; then
    fail "Run model provider not inherited from instance auth config (got '${run_model_provider}')"
    exit 1
  fi
  if [[ "$run_auth_secret" != "$SECRET_NAME" ]]; then
    fail "Run auth secret not inherited from instance (got '${run_auth_secret}', want '${SECRET_NAME}')"
    exit 1
  fi

  pass "Run has Sympozium schedule labels and inherited provider/auth metadata"

  pass "Schedule-dispatch API test passed"
}

main "$@"
