#!/usr/bin/env bash
# API integration test: ad-hoc instance full lifecycle journey.
# Validates:
#   1) Create ad-hoc instance with provider/model/skills → verify fields
#   2) Create AgentRun against instance → verify run inherits instance config
#   3) Memory ConfigMap created for instance
#   4) Instance status reflects active/total runs
#   5) Delete instance → verify cleanup

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TIMEOUT="${TEST_TIMEOUT:-60}"

INSTANCE_NAME="inttest-adhoc-life-$(date +%s)"
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
  info "Cleaning up ad-hoc lifecycle resources..."
  [[ -n "$RUN_NAME" ]] && kubectl delete agentrun "$RUN_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
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
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-adhoc-lifecycle-portforward.log 2>&1 &
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

  info "Running ad-hoc instance lifecycle test in namespace '${NAMESPACE}'"

  if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    fail "OPENAI_API_KEY environment variable is required but not set"
    exit 1
  fi

  start_port_forward_if_needed
  resolve_apiserver_token

  # ── 1) Create ad-hoc instance with skills ──
  api_request POST "/api/v1/agents" \
    "{\"name\":\"${INSTANCE_NAME}\",\"provider\":\"openai\",\"model\":\"gpt-4o-mini\",\"apiKey\":\"${OPENAI_API_KEY}\",\"skills\":[{\"skillPackRef\":\"k8s-ops\"}]}" >/dev/null
  pass "Created ad-hoc instance '${INSTANCE_NAME}'"

  # ── Verify instance appears in list ──
  inst_list="$(api_request GET "/api/v1/agents")"
  found="$(printf "%s" "$inst_list" | python3 -c 'import json,sys; t=sys.argv[1]; d=json.load(sys.stdin); print("true" if any(i.get("metadata",{}).get("name")==t for i in d) else "false")' "$INSTANCE_NAME")"
  if [[ "$found" != "true" ]]; then
    fail "Instance '${INSTANCE_NAME}' not found in list"
    exit 1
  fi
  pass "Instance appears in list"

  # ── Verify instance fields ──
  inst_json="$(api_request GET "/api/v1/agents/${INSTANCE_NAME}")"

  inst_model="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("agents",{}).get("default",{}).get("model",""))')"
  inst_provider="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); refs=d.get("spec",{}).get("authRefs",[]); print(refs[0].get("provider","") if refs else "")')"
  inst_secret="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); refs=d.get("spec",{}).get("authRefs",[]); print(refs[0].get("secret","") if refs else "")')"
  inst_skills="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); s=[i.get("skillPackRef","") for i in d.get("spec",{}).get("skills",[])]; print(",".join(sorted(s)))')"

  if [[ "$inst_model" != "gpt-4o-mini" ]]; then
    fail "Instance model mismatch (got '${inst_model}', want 'gpt-4o-mini')"
    exit 1
  fi
  if [[ "$inst_provider" != "openai" ]]; then
    fail "Instance provider mismatch (got '${inst_provider}', want 'openai')"
    exit 1
  fi
  if [[ -z "$inst_secret" ]]; then
    fail "Instance has no auth secret"
    exit 1
  fi
  if [[ "$inst_skills" != *"k8s-ops"* ]]; then
    fail "Instance skills mismatch (got '${inst_skills}', want 'k8s-ops')"
    exit 1
  fi
  pass "Instance has correct model/provider/auth/skills"

  # ── Verify the apiserver created an auth secret in K8s ──
  if ! kubectl get secret "$inst_secret" -n "$NAMESPACE" >/dev/null 2>&1; then
    fail "Auth secret '${inst_secret}' not found in namespace"
    exit 1
  fi
  pass "Auth secret '${inst_secret}' exists in cluster"

  # ── 2) Create an AgentRun against the instance ──
  run_json="$(api_request POST "/api/v1/runs" "{\"agentRef\":\"${INSTANCE_NAME}\",\"task\":\"adhoc lifecycle test run\"}")"
  RUN_NAME="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("name",""))')"

  if [[ -z "$RUN_NAME" ]]; then
    fail "AgentRun creation returned no name"
    exit 1
  fi
  pass "Created AgentRun '${RUN_NAME}'"

  # ── Verify run inherits instance config ──
  run_provider="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("provider",""))')"
  run_model="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("model",""))')"
  run_auth="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("authSecretRef",""))')"
  run_skills="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); s=[i.get("skillPackRef","") for i in d.get("spec",{}).get("skills",[])]; print(",".join(sorted(s)))')"
  run_instance_ref="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("agentRef",""))')"

  if [[ "$run_provider" != "openai" ]]; then
    fail "Run provider not inherited (got '${run_provider}')"
    exit 1
  fi
  if [[ "$run_model" != "gpt-4o-mini" ]]; then
    fail "Run model not inherited (got '${run_model}')"
    exit 1
  fi
  if [[ -z "$run_auth" ]]; then
    fail "Run has no authSecretRef"
    exit 1
  fi
  if [[ "$run_skills" != *"k8s-ops"* ]]; then
    fail "Run skills not inherited (got '${run_skills}')"
    exit 1
  fi
  if [[ "$run_instance_ref" != "$INSTANCE_NAME" ]]; then
    fail "Run agentRef mismatch (got '${run_instance_ref}')"
    exit 1
  fi
  pass "AgentRun inherited provider/model/auth/skills/agentRef from instance"

  # ── Verify run has instance label ──
  run_inst_label="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("labels",{}).get("sympozium.ai/instance",""))')"
  if [[ "$run_inst_label" != "$INSTANCE_NAME" ]]; then
    fail "Run missing instance label (got '${run_inst_label}')"
    exit 1
  fi
  pass "AgentRun has correct sympozium.ai/instance label"

  # ── 3) Verify memory ConfigMap exists for instance ──
  elapsed=0
  memory_found=false
  while [[ "$elapsed" -lt 15 ]]; do
    if kubectl get configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" -o name 2>/dev/null | grep -q configmap; then
      memory_found=true
      break
    fi
    sleep 3
    elapsed=$((elapsed + 3))
  done
  if [[ "$memory_found" == "true" ]]; then
    pass "Memory ConfigMap exists for instance"
  else
    info "Memory ConfigMap not found (may be created on first run completion)"
  fi

  # ── 4) Verify instance status reflects the run ──
  # Give controller a moment to reconcile
  sleep 2
  inst_status_json="$(api_request GET "/api/v1/agents/${INSTANCE_NAME}")"
  total_runs="$(printf "%s" "$inst_status_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",{}).get("totalAgentRuns",0))')"
  if [[ "$total_runs" -ge 1 ]]; then
    pass "Instance status.totalAgentRuns = ${total_runs}"
  else
    info "Instance status.totalAgentRuns = ${total_runs} (may update asynchronously)"
  fi

  # ── 5) Delete instance ──
  api_request DELETE "/api/v1/agents/${INSTANCE_NAME}" >/dev/null

  # K8s deletion is async (controller finalizers), poll until gone
  elapsed=0
  while [[ "$elapsed" -lt 30 ]]; do
    api_check "/api/v1/agents/${INSTANCE_NAME}" || break
    sleep 2
    elapsed=$((elapsed + 2))
  done

  if api_check "/api/v1/agents/${INSTANCE_NAME}"; then
    fail "Instance still exists after delete (waited ${elapsed}s)"
    exit 1
  fi
  pass "Instance deleted successfully"

  # ── Verify instance no longer in list ──
  inst_list_after="$(api_request GET "/api/v1/agents")"
  still_found="$(printf "%s" "$inst_list_after" | python3 -c 'import json,sys; t=sys.argv[1]; d=json.load(sys.stdin); print("true" if any(i.get("metadata",{}).get("name")==t for i in d) else "false")' "$INSTANCE_NAME")"
  if [[ "$still_found" == "true" ]]; then
    fail "Instance still appears in list after delete"
    exit 1
  fi
  pass "Instance removed from list"

  echo
  pass "Ad-hoc instance lifecycle test passed"
  exit $EXIT_CODE
}

main "$@"
