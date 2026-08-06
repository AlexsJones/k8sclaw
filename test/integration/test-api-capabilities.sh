#!/usr/bin/env bash
# API integration test: optional capability checks using CLAUDE_TOKEN and GITHUB_TOKEN.
# - CLAUDE_TOKEN: validates Anthropic provider wiring via API-created instance.
# - GITHUB_TOKEN: validates github-gitops token endpoint + secret persistence.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"

CLAUDE_TOKEN="${CLAUDE_TOKEN:-}"
GITHUB_TOKEN="${GITHUB_TOKEN:-}"

CLAUDE_INSTANCE_NAME="inttest-api-claude-instance-$(date +%s)"
GITHUB_SECRET_NAME="github-gitops-token"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; }
info() { echo -e "${YELLOW}● $*${NC}"; }

PF_PID=""
APISERVER_TOKEN="${APISERVER_TOKEN:-}"
HAD_GITHUB_SECRET="false"
ORIGINAL_GITHUB_TOKEN_B64=""

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
  info "Cleaning up capability API test resources..."
  api_request DELETE "/api/v1/agents/${CLAUDE_INSTANCE_NAME}" >/dev/null 2>&1 || true
  # kubectl fallback: instance, auto-created anthropic secret, configmaps
  kubectl delete sympoziuminstance "$CLAUDE_INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete secret "${CLAUDE_INSTANCE_NAME}-anthropic-key" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${CLAUDE_INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true

  if [[ "$HAD_GITHUB_SECRET" == "true" && -n "$ORIGINAL_GITHUB_TOKEN_B64" ]]; then
    kubectl patch secret "$GITHUB_SECRET_NAME" -n "$APISERVER_NAMESPACE" --type=merge -p "{\"data\":{\"GH_TOKEN\":\"${ORIGINAL_GITHUB_TOKEN_B64}\"}}" >/dev/null 2>&1 || true
  fi

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
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-api-capabilities-portforward.log 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-api-capabilities-portforward.log || true
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

  info "Running optional capability API tests in namespace '${NAMESPACE}'"

  start_port_forward_if_needed
  resolve_apiserver_token

  local_ran="false"

  if [[ -n "$CLAUDE_TOKEN" ]]; then
    local_ran="true"
    info "Validating Anthropic provider wiring via CLAUDE_TOKEN"

    api_request POST "/api/v1/agents" "{\"name\":\"${CLAUDE_INSTANCE_NAME}\",\"provider\":\"anthropic\",\"model\":\"claude-3-5-sonnet\",\"apiKey\":\"${CLAUDE_TOKEN}\"}" >/dev/null

    inst_json="$(api_request GET "/api/v1/agents/${CLAUDE_INSTANCE_NAME}")"
    provider="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); refs=d.get("spec",{}).get("authRefs",[]); print(refs[0].get("provider","") if refs else "")')"
    secret_name="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); refs=d.get("spec",{}).get("authRefs",[]); print(refs[0].get("secret","") if refs else "")')"

    if [[ "$provider" != "anthropic" ]]; then
      fail "Anthropic auth ref provider mismatch (got '${provider}')"
      exit 1
    fi
    if [[ -z "$secret_name" ]]; then
      fail "Anthropic auth ref secret not set"
      exit 1
    fi
    kubectl get secret "$secret_name" -n "$NAMESPACE" >/dev/null

    pass "Anthropic provider auth wiring validated"
  else
    info "CLAUDE_TOKEN not set — skipping Anthropic capability check"
  fi

  if [[ -n "$GITHUB_TOKEN" ]]; then
    local_ran="true"
    info "Validating GitHub token endpoint + secret persistence"

    if kubectl get secret "$GITHUB_SECRET_NAME" -n "$APISERVER_NAMESPACE" >/dev/null 2>&1; then
      HAD_GITHUB_SECRET="true"
      ORIGINAL_GITHUB_TOKEN_B64="$(kubectl get secret "$GITHUB_SECRET_NAME" -n "$APISERVER_NAMESPACE" -o jsonpath='{.data.GH_TOKEN}' 2>/dev/null || true)"
    fi

    api_request POST "/api/v1/skills/github-gitops/auth/token" "{\"token\":\"${GITHUB_TOKEN}\"}" >/dev/null
    status_json="$(api_request GET "/api/v1/skills/github-gitops/auth/status")"
    status="$(printf "%s" "$status_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",""))')"
    if [[ "$status" != "complete" ]]; then
      fail "GitHub auth status expected 'complete', got '${status}'"
      exit 1
    fi

    gh_token_len="$(kubectl get secret "$GITHUB_SECRET_NAME" -n "$APISERVER_NAMESPACE" -o jsonpath='{.data.GH_TOKEN}' 2>/dev/null | python3 -c 'import sys; s=sys.stdin.read().strip(); print(len(s))')"
    if [[ "$gh_token_len" -lt 10 ]]; then
      fail "GitHub token secret appears empty/invalid"
      exit 1
    fi

    pass "GitHub token endpoint + secret persistence validated"
  else
    info "GITHUB_TOKEN not set — skipping GitHub capability check"
  fi

  if [[ "$local_ran" != "true" ]]; then
    info "No optional tokens provided; capability checks skipped (set CLAUDE_TOKEN and/or GITHUB_TOKEN)"
  fi

  pass "Optional capability API tests passed"
}

main "$@"
