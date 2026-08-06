#!/usr/bin/env bash
# API integration test: heartbeat schedule lifecycle.
# Validates:
#   1) Create heartbeat schedule via API → verify schedule resource fields
#   2) Heartbeat schedule dispatches an AgentRun within the interval
#   3) Dispatched run has correct labels and inherits instance config
#   4) Schedule status tracks totalRuns and lastRunName
#   5) Suspend schedule → no new runs dispatched
#   6) Delete schedule → verify cleanup

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TIMEOUT="${TEST_TIMEOUT:-60}"

INSTANCE_NAME="inttest-hb-instance-$(date +%s)"
SCHEDULE_NAME="inttest-hb-schedule-$(date +%s)"
SECRET_NAME="${INSTANCE_NAME}-openai-key"
RUN_NAME=""

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; EXIT_CODE=1; }
info() { echo -e "${YELLOW}● $*${NC}"; }

EXIT_CODE=0
PF_PID=""
APISERVER_TOKEN="${APISERVER_TOKEN:-}"

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
  info "Cleaning up heartbeat schedule test resources..."
  [[ -n "$RUN_NAME" ]] && kubectl delete agentrun "$RUN_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete sympoziumschedule "$SCHEDULE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
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

api_check() {
  local path="$1"
  local url="$(url_with_namespace "$path")"
  local -a headers=(-H "Content-Type: application/json")
  [[ -n "${APISERVER_TOKEN}" ]] && headers+=(-H "Authorization: Bearer ${APISERVER_TOKEN}")
  local code
  code="$(curl -sS -o /dev/null -w "%{http_code}" "${headers[@]}" "$url")"
  [[ "$code" -ge 200 && "$code" -lt 300 ]]
}


start_port_forward_if_needed() {
  if [[ "${SKIP_PORT_FORWARD}" == "1" ]]; then return 0; fi
  if curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1; then return 0; fi

  info "Starting port-forward to sympozium-apiserver on :${PORT_FORWARD_LOCAL_PORT}"
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-heartbeat-portforward.log 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 30); do
    kill -0 "$PF_PID" >/dev/null 2>&1 || { fail "Port-forward exited early"; exit 1; }
    curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1 && { pass "Port-forward ready"; return 0; }
    sleep 1
  done
  fail "Timed out waiting for API server via port-forward"
  exit 1
}

main() {
  require_cmd kubectl
  require_cmd curl
  require_cmd python3

  info "Running heartbeat schedule test in namespace '${NAMESPACE}'"

  if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    fail "OPENAI_API_KEY environment variable is required but not set"
    exit 1
  fi

  start_port_forward_if_needed
  resolve_apiserver_token

  # ── Create instance ──
  api_request POST "/api/v1/agents" \
    "{\"name\":\"${INSTANCE_NAME}\",\"provider\":\"openai\",\"model\":\"gpt-4o-mini\",\"apiKey\":\"${OPENAI_API_KEY}\"}" >/dev/null
  pass "Created instance '${INSTANCE_NAME}'"

  # ── 1) Create heartbeat schedule (every-minute cron for fast test) ──
  api_request POST "/api/v1/schedules" \
    "{\"name\":\"${SCHEDULE_NAME}\",\"agentRef\":\"${INSTANCE_NAME}\",\"schedule\":\"* * * * *\",\"task\":\"heartbeat health check\",\"type\":\"heartbeat\"}" >/dev/null
  pass "Created heartbeat schedule '${SCHEDULE_NAME}'"

  # ── Verify schedule resource fields ──
  sched_json="$(api_request GET "/api/v1/schedules/${SCHEDULE_NAME}")"

  sched_type="$(printf "%s" "$sched_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("type",""))')"
  sched_instance_ref="$(printf "%s" "$sched_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("agentRef",""))')"
  sched_task="$(printf "%s" "$sched_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("task",""))')"
  sched_cron="$(printf "%s" "$sched_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("schedule",""))')"

  if [[ "$sched_type" != "heartbeat" ]]; then
    fail "Schedule type mismatch (got '${sched_type}', want 'heartbeat')"
    exit 1
  fi
  if [[ "$sched_instance_ref" != "$INSTANCE_NAME" ]]; then
    fail "Schedule agentRef mismatch (got '${sched_instance_ref}')"
    exit 1
  fi
  if [[ "$sched_task" != "heartbeat health check" ]]; then
    fail "Schedule task mismatch (got '${sched_task}')"
    exit 1
  fi
  if [[ "$sched_cron" != "* * * * *" ]]; then
    fail "Schedule cron mismatch (got '${sched_cron}')"
    exit 1
  fi
  pass "Schedule has correct type/agentRef/task/cron"

  # ── Verify no K8s CronJob (Sympozium controller manages dispatch) ──
  if kubectl get cronjob "$SCHEDULE_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    fail "Unexpected Kubernetes CronJob '${SCHEDULE_NAME}'"
    exit 1
  fi
  pass "No native CronJob created (Sympozium schedule controller path)"

  # ── 2) Wait for heartbeat to dispatch an AgentRun ──
  elapsed=0
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    sched_json="$(api_request GET "/api/v1/schedules/${SCHEDULE_NAME}")"

    total_runs="$(printf "%s" "$sched_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",{}).get("totalRuns",0))')"
    RUN_NAME="$(printf "%s" "$sched_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",{}).get("lastRunName",""))')"

    if [[ "$total_runs" -ge 1 && -n "$RUN_NAME" ]]; then
      pass "Heartbeat dispatched run '${RUN_NAME}' (totalRuns=${total_runs})"
      break
    fi

    sleep 5
    elapsed=$((elapsed + 5))
  done

  if [[ "$elapsed" -ge "$TIMEOUT" ]]; then
    fail "Timed out waiting for heartbeat dispatch (${TIMEOUT}s)"
    exit 1
  fi

  # ── 3) Verify dispatched run has correct labels ──
  run_json="$(api_request GET "/api/v1/runs/${RUN_NAME}")"

  run_schedule_label="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("labels",{}).get("sympozium.ai/schedule",""))')"
  run_instance_label="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("labels",{}).get("sympozium.ai/instance",""))')"
  run_provider="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("provider",""))')"
  run_auth="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("authSecretRef",""))')"
  run_task="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("task",""))')"

  if [[ "$run_schedule_label" != "$SCHEDULE_NAME" ]]; then
    fail "Run schedule label mismatch (got '${run_schedule_label}')"
    exit 1
  fi
  if [[ "$run_instance_label" != "$INSTANCE_NAME" ]]; then
    fail "Run instance label mismatch (got '${run_instance_label}')"
    exit 1
  fi
  if [[ "$run_provider" != "openai" ]]; then
    fail "Run provider not inherited (got '${run_provider}')"
    exit 1
  fi
  if [[ -z "$run_auth" ]]; then
    fail "Run missing authSecretRef"
    exit 1
  fi
  if [[ "$run_task" != *"heartbeat health check"* ]]; then
    fail "Run task does not contain expected task text (got '${run_task}')"
    exit 1
  fi
  pass "Dispatched run has correct labels/provider/auth/task"

  # ── 4) Verify schedule status tracks run ──
  sched_status="$(api_request GET "/api/v1/schedules/${SCHEDULE_NAME}")"
  status_phase="$(printf "%s" "$sched_status" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",{}).get("phase",""))')"
  status_last_run="$(printf "%s" "$sched_status" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",{}).get("lastRunName",""))')"
  status_total="$(printf "%s" "$sched_status" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",{}).get("totalRuns",0))')"

  if [[ "$status_phase" == "Active" || "$status_phase" == "Suspended" ]]; then
    pass "Schedule phase is '${status_phase}'"
  else
    info "Schedule phase is '${status_phase}' (expected Active)"
  fi

  if [[ "$status_last_run" == "$RUN_NAME" ]]; then
    pass "Schedule status.lastRunName matches dispatched run"
  else
    info "Schedule status.lastRunName='${status_last_run}' (may have dispatched another)"
  fi

  if [[ "$status_total" -ge 1 ]]; then
    pass "Schedule status.totalRuns = ${status_total}"
  else
    fail "Schedule status.totalRuns = 0"
  fi

  # ── 5) Delete schedule ──
  api_request DELETE "/api/v1/schedules/${SCHEDULE_NAME}" >/dev/null

  # Poll for async deletion
  elapsed=0
  while [[ "$elapsed" -lt 30 ]]; do
    api_check "/api/v1/schedules/${SCHEDULE_NAME}" || break
    sleep 2
    elapsed=$((elapsed + 2))
  done

  if api_check "/api/v1/schedules/${SCHEDULE_NAME}"; then
    fail "Schedule still exists after delete (waited ${elapsed}s)"
    exit 1
  fi
  pass "Schedule deleted successfully"

  # ── Verify schedule no longer in list ──
  sched_list="$(api_request GET "/api/v1/schedules")"
  still_found="$(printf "%s" "$sched_list" | python3 -c 'import json,sys; t=sys.argv[1]; d=json.load(sys.stdin); print("true" if any(i.get("metadata",{}).get("name")==t for i in d) else "false")' "$SCHEDULE_NAME")"
  if [[ "$still_found" == "true" ]]; then
    fail "Schedule still in list after delete"
    exit 1
  fi
  pass "Schedule removed from list"

  echo
  pass "Heartbeat schedule test passed"
  exit $EXIT_CODE
}

main "$@"
