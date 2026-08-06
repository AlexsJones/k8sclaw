#!/usr/bin/env bash
# API smoke test: validates core Sympozium API flows without running LLM jobs.
# Coverage:
#   - Namespaces
#   - Skills (list/get)
#   - Policies (list/get)
#   - Ensembles (install-defaults/list/get)
#   - Ad-hoc Instances (create/list/get/delete)
#   - Schedules (create/list/get/delete)

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"

INSTANCE_NAME="inttest-api-instance-$(date +%s)"
SCHEDULE_NAME="inttest-api-schedule-$(date +%s)"

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
  info "Cleaning up smoke-test resources..."
  # Best effort cleanup via API
  api_delete "/api/v1/schedules/${SCHEDULE_NAME}" >/dev/null 2>&1 || true
  api_delete "/api/v1/agents/${INSTANCE_NAME}" >/dev/null 2>&1 || true
  # kubectl fallback: agentruns, schedule, instance, any auto-created secret, configmaps
  kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete sympoziumschedule "$SCHEDULE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete secret "${INSTANCE_NAME}-openai-key" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true

  stop_port_forward
}
trap cleanup EXIT

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "Required command not found: $1"
    exit 1
  fi
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
  local url
  url="$(url_with_namespace "$path")"

  local tmp
  tmp="$(mktemp)"

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

  local resp
  resp="$(cat "$tmp")"
  rm -f "$tmp"

  if [[ "$code" -lt 200 || "$code" -ge 300 ]]; then
    fail "API ${method} ${path} failed (HTTP ${code})"
    echo "$resp"
    return 1
  fi

  printf "%s" "$resp"
}

api_get() {
  api_request GET "$1"
}

api_post() {
  api_request POST "$1" "${2:-}"
}

api_delete() {
  api_request DELETE "$1"
}

json_len() {
  python3 -c 'import json,sys; d=json.load(sys.stdin); print(len(d) if isinstance(d,list) else 0)'
}

json_first_name() {
  python3 -c 'import json,sys; d=json.load(sys.stdin); print(d[0]["metadata"]["name"] if isinstance(d,list) and d and "metadata" in d[0] and "name" in d[0]["metadata"] else "")'
}

json_first_namespace() {
  python3 -c 'import json,sys; d=json.load(sys.stdin); print(d[0]["metadata"].get("namespace", "") if isinstance(d,list) and d and "metadata" in d[0] else "")'
}

json_contains_name() {
  local target="$1"
  python3 -c 'import json,sys; target=sys.argv[1]; d=json.load(sys.stdin); print("true" if any(i.get("metadata",{}).get("name")==target for i in (d if isinstance(d,list) else [])) else "false")' "$target"
}


start_port_forward_if_needed() {
  if [[ "${SKIP_PORT_FORWARD}" == "1" ]]; then
    return 0
  fi

  if curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1; then
    return 0
  fi

  info "Starting port-forward to sympozium-apiserver on :${PORT_FORWARD_LOCAL_PORT}"
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-api-smoke-portforward.log 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-api-smoke-portforward.log || true
      exit 1
    fi
    if curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1; then
      pass "Port-forward ready"
      return 0
    fi
    sleep 1
  done

  fail "Timed out waiting for API server via port-forward"
  cat /tmp/sympozium-api-smoke-portforward.log || true
  exit 1
}

main() {
  require_cmd kubectl
  require_cmd curl
  require_cmd python3

  info "Running API smoke tests in namespace '${NAMESPACE}'"

  if ! kubectl get crd agents.sympozium.ai >/dev/null 2>&1; then
    fail "Sympozium CRDs not installed"
    exit 1
  fi

  if ! kubectl get svc -n "${APISERVER_NAMESPACE}" sympozium-apiserver >/dev/null 2>&1; then
    fail "sympozium-apiserver service not found in namespace '${APISERVER_NAMESPACE}'"
    exit 1
  fi

  start_port_forward_if_needed
  resolve_apiserver_token

  # 1) Namespaces endpoint
  ns_json="$(api_get /api/v1/namespaces)"
  has_ns="$(printf "%s" "$ns_json" | python3 -c 'import json,sys; ns="'"${NAMESPACE}"'"; d=json.load(sys.stdin); print("true" if ns in d else "false")')"
  if [[ "$has_ns" != "true" ]]; then
    fail "Namespace list does not contain '${NAMESPACE}'"
    exit 1
  fi
  pass "Namespaces endpoint OK"

  # 2) Skills list/get
  skills_json="$(api_get /api/v1/skills)"
  skills_count="$(printf "%s" "$skills_json" | json_len)"
  if [[ "$skills_count" -lt 1 ]]; then
    fail "Skills list is empty"
    exit 1
  fi
  first_skill="$(printf "%s" "$skills_json" | json_first_name)"
  first_skill_ns="$(printf "%s" "$skills_json" | json_first_namespace)"
  api_get "/api/v1/skills/${first_skill}?namespace=${first_skill_ns}" >/dev/null
  pass "Skills list/get OK"

  # 3) Policies list/get
  policies_json="$(api_get /api/v1/policies)"
  policies_count="$(printf "%s" "$policies_json" | json_len)"
  if [[ "$policies_count" -lt 1 ]]; then
    fail "Policies list is empty"
    exit 1
  fi
  first_policy="$(printf "%s" "$policies_json" | json_first_name)"
  first_policy_ns="$(printf "%s" "$policies_json" | json_first_namespace)"
  api_get "/api/v1/policies/${first_policy}?namespace=${first_policy_ns}" >/dev/null
  pass "Policies list/get OK"

  # 4) Ensembles install-defaults + list/get
  api_post /api/v1/ensembles/install-defaults >/dev/null
  packs_json="$(api_get /api/v1/ensembles)"
  packs_count="$(printf "%s" "$packs_json" | json_len)"
  if [[ "$packs_count" -lt 6 ]]; then
    fail "Expected at least 6 default Ensembles, got ${packs_count}"
    exit 1
  fi
  first_pack="$(printf "%s" "$packs_json" | json_first_name)"
  api_get "/api/v1/ensembles/${first_pack}" >/dev/null
  pass "Ensembles install/list/get OK"

  # 5) Ad-hoc Instance create/list/get
  create_instance_body="$(cat <<EOF
{"name":"${INSTANCE_NAME}","provider":"openai","model":"gpt-4o-mini"}
EOF
)"
  api_post /api/v1/agents "$create_instance_body" >/dev/null

  inst_list_json="$(api_get /api/v1/agents)"
  found_instance="$(printf "%s" "$inst_list_json" | json_contains_name "$INSTANCE_NAME")"
  if [[ "$found_instance" != "true" ]]; then
    fail "Created instance '${INSTANCE_NAME}' not found in list"
    exit 1
  fi
  api_get "/api/v1/agents/${INSTANCE_NAME}" >/dev/null
  pass "Ad-hoc instance create/list/get OK"

  # 6) Schedule create/list/get/delete
  create_schedule_body="$(cat <<EOF
{"name":"${SCHEDULE_NAME}","agentRef":"${INSTANCE_NAME}","schedule":"*/10 * * * *","task":"heartbeat smoke","type":"heartbeat"}
EOF
)"
  api_post /api/v1/schedules "$create_schedule_body" >/dev/null

  sched_list_json="$(api_get /api/v1/schedules)"
  found_schedule="$(printf "%s" "$sched_list_json" | json_contains_name "$SCHEDULE_NAME")"
  if [[ "$found_schedule" != "true" ]]; then
    fail "Created schedule '${SCHEDULE_NAME}' not found in list"
    exit 1
  fi
  api_get "/api/v1/schedules/${SCHEDULE_NAME}" >/dev/null
  pass "Schedule create/list/get OK"

  api_delete "/api/v1/schedules/${SCHEDULE_NAME}" >/dev/null
  # K8s deletion is async — poll briefly
  elapsed=0
  while [[ "$elapsed" -lt 15 ]]; do
    api_get "/api/v1/schedules/${SCHEDULE_NAME}" >/dev/null 2>&1 || break
    sleep 2
    elapsed=$((elapsed + 2))
  done
  if api_get "/api/v1/schedules/${SCHEDULE_NAME}" >/dev/null 2>&1; then
    fail "Schedule '${SCHEDULE_NAME}' still exists after delete"
    exit 1
  fi
  pass "Schedule delete OK"

  # 7) Instance delete
  api_delete "/api/v1/agents/${INSTANCE_NAME}" >/dev/null
  elapsed=0
  while [[ "$elapsed" -lt 15 ]]; do
    api_get "/api/v1/agents/${INSTANCE_NAME}" >/dev/null 2>&1 || break
    sleep 2
    elapsed=$((elapsed + 2))
  done
  if api_get "/api/v1/agents/${INSTANCE_NAME}" >/dev/null 2>&1; then
    fail "Instance '${INSTANCE_NAME}' still exists after delete"
    exit 1
  fi
  pass "Ad-hoc instance delete OK"

  echo
  pass "API smoke suite passed"
}

main "$@"
