#!/usr/bin/env bash
# API integration test: web-endpoint skill enable/disable and status endpoint.
# Coverage:
#   - Enable web-endpoint via PATCH /api/v1/agents/{name}
#   - GET /api/v1/agents/{name}/web-endpoint status
#   - Disable web-endpoint via PATCH
#   - Verify skill list reflects changes

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"

INSTANCE_NAME="inttest-webep-$(date +%s)"
PF_PID=""
APISERVER_TOKEN="${APISERVER_TOKEN:-}"
failures=0

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; failures=$((failures + 1)); }
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
  info "Cleaning up web-endpoint test resources..."
  api_request DELETE "/api/v1/agents/${INSTANCE_NAME}" >/dev/null 2>&1 || true
  # kubectl fallback: instance, auto-created secret, configmaps
  kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete secret "${INSTANCE_NAME}-openai-key" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
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
  kubectl port-forward -n "$APISERVER_NAMESPACE" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-api-webep-portforward.log 2>&1 &
  PF_PID=$!
  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-api-webep-portforward.log || true
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

  info "Running web-endpoint API test in namespace '${NAMESPACE}'"

  if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    fail "OPENAI_API_KEY environment variable is required but not set"
    exit 1
  fi

  start_port_forward_if_needed
  resolve_apiserver_token

  # --- Create a plain instance (no web-endpoint) ---
  info "Creating test instance"
  api_request POST "/api/v1/agents" \
    "{\"name\":\"${INSTANCE_NAME}\",\"provider\":\"openai\",\"model\":\"gpt-4o-mini\",\"apiKey\":\"${OPENAI_API_KEY}\"}" >/dev/null
  pass "Instance created"

  # --- Check web-endpoint status (should be disabled) ---
  info "Checking initial web-endpoint status"
  status_json="$(api_request GET "/api/v1/agents/${INSTANCE_NAME}/web-endpoint")"
  enabled="$(printf "%s" "$status_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("enabled", False))')"
  if [[ "$enabled" == "False" ]]; then
    pass "Web-endpoint initially disabled"
  else
    fail "Web-endpoint should be disabled initially (got: $enabled)"
  fi

  # --- Enable web-endpoint via PATCH ---
  info "Enabling web-endpoint"
  patch_json="$(api_request PATCH "/api/v1/agents/${INSTANCE_NAME}" \
    '{"webEndpoint":{"enabled":true,"hostname":"test.example.com","rateLimit":{"requestsPerMinute":120}}}')"

  # Verify skill was added
  has_webep="$(printf "%s" "$patch_json" | python3 -c '
import json, sys
inst = json.load(sys.stdin)
skills = inst.get("spec", {}).get("skills", [])
print(any(s.get("skillPackRef") in ("web-endpoint", "skillpack-web-endpoint") for s in skills))
')"
  if [[ "$has_webep" == "True" ]]; then
    pass "Web-endpoint skill added to instance"
  else
    fail "Web-endpoint skill not found after enable"
  fi

  # Verify params
  hostname_set="$(printf "%s" "$patch_json" | python3 -c '
import json, sys
inst = json.load(sys.stdin)
skills = inst.get("spec", {}).get("skills", [])
for s in skills:
    if s.get("skillPackRef") in ("web-endpoint", "skillpack-web-endpoint"):
        print(s.get("params", {}).get("hostname", ""))
        break
')"
  if [[ "$hostname_set" == "test.example.com" ]]; then
    pass "Hostname parameter set correctly"
  else
    fail "Hostname parameter mismatch (got: $hostname_set)"
  fi

  rpm_set="$(printf "%s" "$patch_json" | python3 -c '
import json, sys
inst = json.load(sys.stdin)
skills = inst.get("spec", {}).get("skills", [])
for s in skills:
    if s.get("skillPackRef") in ("web-endpoint", "skillpack-web-endpoint"):
        print(s.get("params", {}).get("rate_limit_rpm", ""))
        break
')"
  if [[ "$rpm_set" == "120" ]]; then
    pass "Rate limit parameter set correctly"
  else
    fail "Rate limit parameter mismatch (got: $rpm_set)"
  fi

  # --- Check web-endpoint status (should be enabled) ---
  info "Checking web-endpoint status after enable"
  status_json="$(api_request GET "/api/v1/agents/${INSTANCE_NAME}/web-endpoint")"
  enabled="$(printf "%s" "$status_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("enabled", False))')"
  if [[ "$enabled" == "True" ]]; then
    pass "Web-endpoint status reports enabled"
  else
    fail "Web-endpoint status should be enabled (got: $enabled)"
  fi

  # --- Disable web-endpoint via PATCH ---
  info "Disabling web-endpoint"
  api_request PATCH "/api/v1/agents/${INSTANCE_NAME}" \
    '{"webEndpoint":{"enabled":false}}' >/dev/null

  # --- Verify disabled ---
  status_json="$(api_request GET "/api/v1/agents/${INSTANCE_NAME}/web-endpoint")"
  enabled="$(printf "%s" "$status_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("enabled", False))')"
  if [[ "$enabled" == "False" ]]; then
    pass "Web-endpoint disabled successfully"
  else
    fail "Web-endpoint should be disabled after removal (got: $enabled)"
  fi

  # --- Re-enable to test idempotency ---
  info "Re-enabling web-endpoint (idempotency check)"
  api_request PATCH "/api/v1/agents/${INSTANCE_NAME}" \
    '{"webEndpoint":{"enabled":true}}' >/dev/null
  api_request PATCH "/api/v1/agents/${INSTANCE_NAME}" \
    '{"webEndpoint":{"enabled":true,"rateLimit":{"requestsPerMinute":200}}}' >/dev/null

  # Should still have exactly one web-endpoint skill
  inst_json="$(api_request GET "/api/v1/agents/${INSTANCE_NAME}")"
  webep_count="$(printf "%s" "$inst_json" | python3 -c '
import json, sys
inst = json.load(sys.stdin)
skills = inst.get("spec", {}).get("skills", [])
print(sum(1 for s in skills if s.get("skillPackRef") in ("web-endpoint", "skillpack-web-endpoint")))
')"
  if [[ "$webep_count" == "1" ]]; then
    pass "Idempotent enable: exactly one web-endpoint skill"
  else
    fail "Expected 1 web-endpoint skill after double-enable, got $webep_count"
  fi

  # --- Summary ---
  if [[ $failures -gt 0 ]]; then
    fail "$failures check(s) failed"
    exit 1
  fi
  pass "Web-endpoint API test passed"
}

main "$@"
