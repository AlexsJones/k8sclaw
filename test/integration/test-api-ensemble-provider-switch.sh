#!/usr/bin/env bash
# API integration test: Ensemble provider switch propagation.
# Verifies OpenAI -> Anthropic update propagates to stamped instance and new runs,
# while retaining model/skills coherence.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TIMEOUT="${TEST_TIMEOUT:-180}"

PACK_NAME="inttest-pack-switch-$(date +%s)"
PERSONA_NAME="switcher"
INSTANCE_NAME="${PACK_NAME}-${PERSONA_NAME}"
OPENAI_SECRET="${PACK_NAME}-openai-key"
ANTHROPIC_SECRET="${PACK_NAME}-anthropic-key"
OPENAI_MODEL="gpt-4o-mini"
ANTHROPIC_MODEL="claude-3-5-sonnet"
EXPECTED_SKILLS_CSV="code-review,k8s-ops,memory"

RUN_OPENAI=""
RUN_ANTHROPIC=""
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

on_error() {
  local exit_code=$?
  fail "Provider-switch test failed at line ${1}: ${2} (exit=${exit_code})"
  exit "$exit_code"
}
trap 'on_error ${LINENO} "${BASH_COMMAND}"' ERR

cleanup() {
  info "Cleaning up provider-switch resources..."
  [[ -n "$RUN_OPENAI" ]] && api_request DELETE "/api/v1/runs/${RUN_OPENAI}" >/dev/null 2>&1 || true
  [[ -n "$RUN_ANTHROPIC" ]] && api_request DELETE "/api/v1/runs/${RUN_ANTHROPIC}" >/dev/null 2>&1 || true
  api_request DELETE "/api/v1/agents/${INSTANCE_NAME}" >/dev/null 2>&1 || true
  api_request DELETE "/api/v1/ensembles/${PACK_NAME}" >/dev/null 2>&1 || true
  # kubectl fallback: agentruns, instance, pack, secrets, configmaps
  kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete ensemble "$PACK_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete secret "$OPENAI_SECRET" "$ANTHROPIC_SECRET" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true
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
  local -a headers=(-H "Content-Type: application/json")
  [[ -n "$APISERVER_TOKEN" ]] && headers+=(-H "Authorization: Bearer ${APISERVER_TOKEN}")

  local attempts=3
  local delay=1
  local attempt=1
  local code=""
  local resp=""

  while [[ "$attempt" -le "$attempts" ]]; do
    local tmp
    tmp="$(mktemp)"

    if [[ -n "$body" ]]; then
      if ! code="$(curl -sS -o "$tmp" -w "%{http_code}" -X "$method" "${headers[@]}" --data "$body" "$url")"; then
        rm -f "$tmp"
        if [[ "$attempt" -lt "$attempts" ]]; then
          sleep "$delay"
          attempt=$((attempt + 1))
          continue
        fi
        fail "API ${method} ${path} failed (network error after ${attempts} attempts)"
        return 1
      fi
    else
      if ! code="$(curl -sS -o "$tmp" -w "%{http_code}" -X "$method" "${headers[@]}" "$url")"; then
        rm -f "$tmp"
        if [[ "$attempt" -lt "$attempts" ]]; then
          sleep "$delay"
          attempt=$((attempt + 1))
          continue
        fi
        fail "API ${method} ${path} failed (network error after ${attempts} attempts)"
        return 1
      fi
    fi

    resp="$(cat "$tmp")"
    rm -f "$tmp"

    if [[ "$code" -ge 200 && "$code" -lt 300 ]]; then
      printf "%s" "$resp"
      return 0
    fi

    if [[ "$attempt" -lt "$attempts" && "$code" -ge 500 ]]; then
      sleep "$delay"
      attempt=$((attempt + 1))
      continue
    fi

    fail "API ${method} ${path} failed (HTTP ${code})"
    echo "$resp"
    return 1
  done

  fail "API ${method} ${path} failed (exhausted retries)"
  return 1
}


start_port_forward_if_needed() {
  [[ "$SKIP_PORT_FORWARD" == "1" ]] && return 0
  curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1 && return 0

  info "Starting port-forward to sympozium-apiserver on :${PORT_FORWARD_LOCAL_PORT}"
  kubectl port-forward -n "$APISERVER_NAMESPACE" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-api-provider-switch-portforward.log 2>&1 &
  PF_PID=$!
  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-api-provider-switch-portforward.log || true
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

assert_instance() {
  local json="$1" provider="$2" secret="$3" model="$4"
  local got_provider got_secret got_model got_skills
  got_provider="$(printf "%s" "$json" | python3 -c 'import json,sys; d=json.load(sys.stdin); refs=d.get("spec",{}).get("authRefs",[]); print(refs[0].get("provider","") if refs else "")')"
  got_secret="$(printf "%s" "$json" | python3 -c 'import json,sys; d=json.load(sys.stdin); refs=d.get("spec",{}).get("authRefs",[]); print(refs[0].get("secret","") if refs else "")')"
  got_model="$(printf "%s" "$json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("agents",{}).get("default",{}).get("model",""))')"
  got_skills="$(printf "%s" "$json" | python3 -c 'import json,sys; d=json.load(sys.stdin); s=sorted([i.get("skillPackRef","") for i in d.get("spec",{}).get("skills",[]) if i.get("skillPackRef")]); print(",".join(s))')"

  [[ "$got_provider" == "$provider" ]] || { fail "instance provider mismatch: $got_provider != $provider"; exit 1; }
  [[ "$got_secret" == "$secret" ]] || { fail "instance secret mismatch: $got_secret != $secret"; exit 1; }
  [[ "$got_model" == "$model" ]] || { fail "instance model mismatch: $got_model != $model"; exit 1; }
  [[ "$got_skills" == "$EXPECTED_SKILLS_CSV" ]] || { fail "instance skills mismatch: $got_skills != $EXPECTED_SKILLS_CSV"; exit 1; }
}

assert_run() {
  local json="$1" provider="$2" secret="$3" model="$4"
  local got_provider got_secret got_model got_skills
  got_provider="$(printf "%s" "$json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("provider",""))')"
  got_secret="$(printf "%s" "$json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("authSecretRef",""))')"
  got_model="$(printf "%s" "$json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("model",{}).get("model",""))')"
  got_skills="$(printf "%s" "$json" | python3 -c 'import json,sys; d=json.load(sys.stdin); s=sorted([i.get("skillPackRef","") for i in d.get("spec",{}).get("skills",[]) if i.get("skillPackRef")]); print(",".join(s))')"

  [[ "$got_provider" == "$provider" ]] || { fail "run provider mismatch: $got_provider != $provider"; exit 1; }
  [[ "$got_secret" == "$secret" ]] || { fail "run secret mismatch: $got_secret != $secret"; exit 1; }
  [[ "$got_model" == "$model" ]] || { fail "run model mismatch: $got_model != $model"; exit 1; }
  [[ "$got_skills" == "$EXPECTED_SKILLS_CSV" ]] || { fail "run skills mismatch: $got_skills != $EXPECTED_SKILLS_CSV"; exit 1; }
}

wait_for_instance_model() {
  local want_model="$1"
  local elapsed=0
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    inst_json="$(api_request GET "/api/v1/agents/${INSTANCE_NAME}" 2>/dev/null || true)"
    if [[ -n "$inst_json" ]]; then
      got_model="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("agents",{}).get("default",{}).get("model",""))')"
      if [[ "$got_model" == "$want_model" ]]; then
        printf "%s" "$inst_json"
        return 0
      fi
    fi
    sleep 5
    elapsed=$((elapsed + 5))
  done
  return 1
}

main() {
  require_cmd kubectl
  require_cmd curl
  require_cmd python3

  info "Running Ensemble provider-switch propagation test in namespace '${NAMESPACE}'"

  if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    fail "OPENAI_API_KEY environment variable is required but not set"
    exit 1
  fi

  start_port_forward_if_needed
  resolve_apiserver_token

  # Temp Ensemble with deterministic skills.
  info "Creating temporary Ensemble '${PACK_NAME}'"
  cat <<EOF | kubectl apply -f -
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK_NAME}
  namespace: ${NAMESPACE}
spec:
  enabled: false
  agentConfigs:
    - name: ${PERSONA_NAME}
      systemPrompt: "Integration test persona for provider switch"
      model: ${OPENAI_MODEL}
      skills:
        - code-review
        - k8s-ops
EOF
  pass "Temporary Ensemble created"

  info "Creating provider auth secrets"
  kubectl create secret generic "$OPENAI_SECRET" --from-literal=OPENAI_API_KEY="${OPENAI_API_KEY}" -n "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
  kubectl create secret generic "$ANTHROPIC_SECRET" --from-literal=ANTHROPIC_API_KEY="${ANTHROPIC_API_KEY:-${OPENAI_API_KEY}}" -n "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -
  pass "Provider auth secrets ready"

  # Enable with OpenAI.
  info "Patching Ensemble to OpenAI"
  api_request PATCH "/api/v1/ensembles/${PACK_NAME}" "{\"enabled\":true,\"provider\":\"openai\",\"secretName\":\"${OPENAI_SECRET}\",\"model\":\"${OPENAI_MODEL}\"}" >/dev/null

  inst_openai="$(wait_for_instance_model "$OPENAI_MODEL" || true)"
  [[ -n "$inst_openai" ]] || { fail "Timed out waiting for OpenAI instance propagation"; exit 1; }
  assert_instance "$inst_openai" "openai" "$OPENAI_SECRET" "$OPENAI_MODEL"
  pass "OpenAI propagation to Ensemble instance verified"

  run_openai_json="$(api_request POST "/api/v1/runs" "{\"agentRef\":\"${INSTANCE_NAME}\",\"task\":\"provider switch openai run\"}")"
  RUN_OPENAI="$(printf "%s" "$run_openai_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("metadata",{}).get("name",""))')"
  assert_run "$run_openai_json" "openai" "$OPENAI_SECRET" "$OPENAI_MODEL"
  pass "OpenAI propagation to new runs verified"

  # Switch to Anthropic.
  info "Patching Ensemble to Anthropic"
  api_request PATCH "/api/v1/ensembles/${PACK_NAME}" "{\"enabled\":true,\"provider\":\"anthropic\",\"secretName\":\"${ANTHROPIC_SECRET}\",\"model\":\"${ANTHROPIC_MODEL}\"}" >/dev/null

  inst_anthropic="$(wait_for_instance_model "$ANTHROPIC_MODEL" || true)"
  [[ -n "$inst_anthropic" ]] || { fail "Timed out waiting for Anthropic instance propagation"; exit 1; }
  assert_instance "$inst_anthropic" "anthropic" "$ANTHROPIC_SECRET" "$ANTHROPIC_MODEL"
  pass "Anthropic propagation to Ensemble instance verified"

  run_anthropic_json="$(api_request POST "/api/v1/runs" "{\"agentRef\":\"${INSTANCE_NAME}\",\"task\":\"provider switch anthropic run\"}")"
  RUN_ANTHROPIC="$(printf "%s" "$run_anthropic_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("metadata",{}).get("name",""))')"
  assert_run "$run_anthropic_json" "anthropic" "$ANTHROPIC_SECRET" "$ANTHROPIC_MODEL"
  pass "Anthropic propagation to new runs verified"

  pass "Ensemble provider-switch propagation test passed"
}

main "$@"
