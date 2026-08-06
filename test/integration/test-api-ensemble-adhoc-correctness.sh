#!/usr/bin/env bash
# API integration test: Ensemble + ad-hoc correctness checks.
# Validates:
#  1) Ensemble enablement propagates authRef/provider/model/skills to stamped instances (and runs).
#  2) Ad-hoc instances behave equivalently for auth/provider/model/skills inheritance.
#  3) Ensemble deactivation removes stamped instances.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TIMEOUT="${TEST_TIMEOUT:-180}"

PACK_NAME="inttest-pack-$(date +%s)"
PERSONA_NAME="analyst"
PACK_INSTANCE_NAME="${PACK_NAME}-${PERSONA_NAME}"
PACK_EXPECTED_SECRET="${PACK_NAME}-openai-key"
PACK_RUN_NAME=""

ADHOC_INSTANCE_NAME="inttest-adhoc-$(date +%s)"
ADHOC_EXPECTED_SECRET="${ADHOC_INSTANCE_NAME}-openai-key"
ADHOC_RUN_NAME=""

MODEL_NAME="gpt-4o-mini"
EXPECTED_SKILLS_CSV="code-review,k8s-ops,memory"

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
  info "Cleaning up Ensemble/ad-hoc correctness resources..."
  [[ -n "$PACK_RUN_NAME" ]] && api_request DELETE "/api/v1/runs/${PACK_RUN_NAME}" >/dev/null 2>&1 || true
  [[ -n "$ADHOC_RUN_NAME" ]] && api_request DELETE "/api/v1/runs/${ADHOC_RUN_NAME}" >/dev/null 2>&1 || true

  api_request DELETE "/api/v1/schedules/${PACK_INSTANCE_NAME}-schedule" >/dev/null 2>&1 || true
  api_request DELETE "/api/v1/agents/${PACK_INSTANCE_NAME}" >/dev/null 2>&1 || true
  api_request DELETE "/api/v1/agents/${ADHOC_INSTANCE_NAME}" >/dev/null 2>&1 || true
  api_request DELETE "/api/v1/ensembles/${PACK_NAME}" >/dev/null 2>&1 || true

  # kubectl fallback: agentruns, schedules, instances, pack, secrets, configmaps
  kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${PACK_INSTANCE_NAME}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${ADHOC_INSTANCE_NAME}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete sympoziumschedule "${PACK_INSTANCE_NAME}-schedule" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete sympoziuminstance "$PACK_INSTANCE_NAME" "$ADHOC_INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete ensemble "$PACK_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete secret "$PACK_EXPECTED_SECRET" "$ADHOC_EXPECTED_SECRET" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${PACK_INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${ADHOC_INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true

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
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-api-correctness-portforward.log 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-api-correctness-portforward.log || true
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

assert_instance_fields() {
  local inst_json="$1"
  local expected_secret="$2"
  local expected_model="$3"
  local expected_provider="$4"
  local expected_skills_csv="$5"
  local scope="$6"

  local provider secret model skills_csv
  provider="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); refs=d.get("spec",{}).get("authRefs",[]); print(refs[0].get("provider","") if refs else "")')"
  secret="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); refs=d.get("spec",{}).get("authRefs",[]); print(refs[0].get("secret","") if refs else "")')"
  model="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("agents",{}).get("default",{}).get("model",""))')"
  skills_csv="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); s=sorted([i.get("skillPackRef","") for i in d.get("spec",{}).get("skills",[]) if i.get("skillPackRef")]); print(",".join(s))')"

  if [[ "$provider" != "$expected_provider" ]]; then
    fail "${scope}: provider mismatch (got '${provider}', want '${expected_provider}')"
    exit 1
  fi
  if [[ "$secret" != "$expected_secret" ]]; then
    fail "${scope}: auth secret mismatch (got '${secret}', want '${expected_secret}')"
    exit 1
  fi
  if [[ "$model" != "$expected_model" ]]; then
    fail "${scope}: model mismatch (got '${model}', want '${expected_model}')"
    exit 1
  fi
  if [[ "$skills_csv" != "$expected_skills_csv" ]]; then
    fail "${scope}: skills mismatch (got '${skills_csv}', want '${expected_skills_csv}')"
    exit 1
  fi
}

assert_run_fields() {
  local run_json="$1"
  local expected_secret="$2"
  local expected_model="$3"
  local expected_provider="$4"
  local expected_skills_csv="$5"
  local scope="$6"

  local provider secret model skills_csv
  provider="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("provider",""))')"
  secret="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("authSecretRef",""))')"
  model="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("model",""))')"
  skills_csv="$(printf "%s" "$run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); s=sorted([i.get("skillPackRef","") for i in d.get("spec",{}).get("skills",[]) if i.get("skillPackRef")]); print(",".join(s))')"

  if [[ "$provider" != "$expected_provider" ]]; then
    fail "${scope}: run provider mismatch (got '${provider}', want '${expected_provider}')"
    exit 1
  fi
  if [[ "$secret" != "$expected_secret" ]]; then
    fail "${scope}: run auth secret mismatch (got '${secret}', want '${expected_secret}')"
    exit 1
  fi
  if [[ "$model" != "$expected_model" ]]; then
    fail "${scope}: run model mismatch (got '${model}', want '${expected_model}')"
    exit 1
  fi
  if [[ "$skills_csv" != "$expected_skills_csv" ]]; then
    fail "${scope}: run skills mismatch (got '${skills_csv}', want '${expected_skills_csv}')"
    exit 1
  fi
}

main() {
  require_cmd kubectl
  require_cmd curl
  require_cmd python3

  info "Running Ensemble/ad-hoc correctness API test in namespace '${NAMESPACE}'"

  if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    fail "OPENAI_API_KEY environment variable is required but not set"
    exit 1
  fi

  start_port_forward_if_needed
  resolve_apiserver_token

  # Create a dedicated temporary Ensemble (disabled by default).
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK_NAME}
  namespace: ${NAMESPACE}
spec:
  description: "Integration test temporary pack"
  category: "integration"
  version: "1.0.0"
  enabled: false
  agentConfigs:
    - name: ${PERSONA_NAME}
      displayName: "Integration Analyst"
      systemPrompt: "Integration test persona"
      model: ${MODEL_NAME}
      skills:
        - code-review
        - k8s-ops
      schedule:
        type: scheduled
        cron: "*/10 * * * *"
        task: "integration task"
EOF
  pass "Created temporary Ensemble '${PACK_NAME}'"

  # Pre-create auth secret and patch Ensemble to use it explicitly.
  kubectl create secret generic "$PACK_EXPECTED_SECRET" \
    --from-literal=OPENAI_API_KEY="${OPENAI_API_KEY}" \
    -n "$NAMESPACE" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null

  # (1) Enable Ensemble with auth/model propagation.
  api_request PATCH "/api/v1/ensembles/${PACK_NAME}" "{\"enabled\":true,\"provider\":\"openai\",\"secretName\":\"${PACK_EXPECTED_SECRET}\",\"model\":\"${MODEL_NAME}\"}" >/dev/null

  elapsed=0
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    if api_request GET "/api/v1/agents/${PACK_INSTANCE_NAME}" >/dev/null 2>&1; then
      break
    fi
    sleep 5
    elapsed=$((elapsed + 5))
  done
  if [[ "$elapsed" -ge "$TIMEOUT" ]]; then
    fail "Timed out waiting for Ensemble instance '${PACK_INSTANCE_NAME}'"
    exit 1
  fi

  pack_inst_json="$(api_request GET "/api/v1/agents/${PACK_INSTANCE_NAME}")"
  assert_instance_fields "$pack_inst_json" "$PACK_EXPECTED_SECRET" "$MODEL_NAME" "openai" "$EXPECTED_SKILLS_CSV" "Ensemble instance"
  pass "Ensemble instance propagated auth/provider/model/skills"

  pack_run_json="$(api_request POST "/api/v1/runs" "{\"agentRef\":\"${PACK_INSTANCE_NAME}\",\"task\":\"pack run correctness\"}")"
  PACK_RUN_NAME="$(printf "%s" "$pack_run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("name",""))')"
  assert_run_fields "$pack_run_json" "$PACK_EXPECTED_SECRET" "$MODEL_NAME" "openai" "$EXPECTED_SKILLS_CSV" "Ensemble run"
  pass "Ensemble run inherited provider/model/auth/skills"

  # (2) Ad-hoc instance parity.
  api_request POST "/api/v1/agents" "{\"name\":\"${ADHOC_INSTANCE_NAME}\",\"provider\":\"openai\",\"model\":\"${MODEL_NAME}\",\"apiKey\":\"${OPENAI_API_KEY}\",\"skills\":[{\"skillPackRef\":\"code-review\"},{\"skillPackRef\":\"k8s-ops\"},{\"skillPackRef\":\"memory\"}]}" >/dev/null

  adhoc_inst_json="$(api_request GET "/api/v1/agents/${ADHOC_INSTANCE_NAME}")"
  assert_instance_fields "$adhoc_inst_json" "$ADHOC_EXPECTED_SECRET" "$MODEL_NAME" "openai" "$EXPECTED_SKILLS_CSV" "Ad-hoc instance"
  pass "Ad-hoc instance has correct auth/provider/model/skills"

  adhoc_run_json="$(api_request POST "/api/v1/runs" "{\"agentRef\":\"${ADHOC_INSTANCE_NAME}\",\"task\":\"adhoc run correctness\"}")"
  ADHOC_RUN_NAME="$(printf "%s" "$adhoc_run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("name",""))')"
  assert_run_fields "$adhoc_run_json" "$ADHOC_EXPECTED_SECRET" "$MODEL_NAME" "openai" "$EXPECTED_SKILLS_CSV" "Ad-hoc run"
  pass "Ad-hoc run inherited provider/model/auth/skills"

  # (3) Deactivation cleanup removes stamped instances.
  api_request PATCH "/api/v1/ensembles/${PACK_NAME}" "{\"enabled\":false}" >/dev/null

  elapsed=0
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    if ! api_request GET "/api/v1/agents/${PACK_INSTANCE_NAME}" >/dev/null 2>&1; then
      break
    fi
    sleep 5
    elapsed=$((elapsed + 5))
  done
  if [[ "$elapsed" -ge "$TIMEOUT" ]]; then
    fail "Timed out waiting for Ensemble deactivation cleanup of instance '${PACK_INSTANCE_NAME}'"
    exit 1
  fi

  pass "Ensemble deactivation removed stamped instance(s)"
  pass "Ensemble/ad-hoc correctness API test passed"
}

main "$@"
