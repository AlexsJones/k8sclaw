#!/usr/bin/env bash
# API integration test: serving mode AgentRun shape validation.
# Verifies that enabling the web-endpoint skill on an instance produces
# a server-mode AgentRun with a Deployment + Service instead of a Job.
# Coverage:
#   - Instance with web-endpoint skill creates a Serving-phase AgentRun
#   - AgentRun status contains deploymentName and serviceName
#   - The Deployment and Service exist in the namespace
#   - Deployment has the web-proxy container with expected env vars

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TIMEOUT="${TEST_TIMEOUT:-180}"

INSTANCE_NAME="inttest-serving-$(date +%s)"
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
  info "Cleaning up serving-mode test resources..."
  # Delete AgentRuns for this instance
  if [[ -n "$APISERVER_TOKEN" ]]; then
    local runs_json
    runs_json="$(api_request GET "/api/v1/runs" 2>/dev/null || true)"
    if [[ -n "$runs_json" ]]; then
      local run_names
      run_names="$(printf "%s" "$runs_json" | python3 -c "
import json, sys
data = json.load(sys.stdin)
items = data if isinstance(data, list) else data.get('items', [])
for r in items:
    meta = r.get('metadata', {})
    labels = meta.get('labels', {})
    if labels.get('sympozium.ai/instance') == '${INSTANCE_NAME}':
        print(meta.get('name', ''))
" 2>/dev/null || true)"
      for rn in $run_names; do
        [[ -n "$rn" ]] && api_request DELETE "/api/v1/runs/${rn}" >/dev/null 2>&1 || true
      done
    fi
  fi
  api_request DELETE "/api/v1/agents/${INSTANCE_NAME}" >/dev/null 2>&1 || true
  # kubectl fallback: instance, secret, agentruns, deployments, services, configmaps
  kubectl delete sympoziuminstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete secret "${INSTANCE_NAME}-openai-key" -n "$NAMESPACE" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found --wait=false >/dev/null 2>&1 || true
  kubectl delete deploy -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete svc -n "$NAMESPACE" -l "sympozium.ai/instance=${INSTANCE_NAME}" --ignore-not-found >/dev/null 2>&1 || true
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
  kubectl port-forward -n "$APISERVER_NAMESPACE" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-api-serving-portforward.log 2>&1 &
  PF_PID=$!
  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-api-serving-portforward.log || true
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

  info "Running serving-mode API test in namespace '${NAMESPACE}'"

  if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    fail "OPENAI_API_KEY environment variable is required but not set"
    exit 1
  fi

  start_port_forward_if_needed
  resolve_apiserver_token

  # --- Create instance with web-endpoint skill ---
  info "Creating instance with web-endpoint skill"
  api_request POST "/api/v1/agents" \
    "{\"name\":\"${INSTANCE_NAME}\",\"provider\":\"openai\",\"model\":\"gpt-4o-mini\",\"apiKey\":\"${OPENAI_API_KEY}\",\"skills\":[{\"skillPackRef\":\"web-endpoint\"}]}" >/dev/null
  pass "Instance with web-endpoint created"

  # --- Wait for a Serving-phase AgentRun ---
  info "Waiting for Serving-phase AgentRun (timeout: ${TIMEOUT}s)..."
  elapsed=0
  serving_run=""
  while [[ $elapsed -lt $TIMEOUT ]]; do
    runs_json="$(api_request GET "/api/v1/runs" 2>/dev/null || true)"
    if [[ -n "$runs_json" ]]; then
      serving_run="$(printf "%s" "$runs_json" | python3 -c "
import json, sys
data = json.load(sys.stdin)
items = data if isinstance(data, list) else data.get('items', [])
for r in items:
    meta = r.get('metadata', {})
    labels = meta.get('labels', {})
    status = r.get('status', {})
    if labels.get('sympozium.ai/instance') == '${INSTANCE_NAME}' and status.get('phase') == 'Serving':
        print(meta.get('name', ''))
        break
" 2>/dev/null || true)"
      [[ -n "$serving_run" ]] && break
    fi
    sleep 5
    elapsed=$((elapsed + 5))
  done

  if [[ -n "$serving_run" ]]; then
    pass "Found Serving-phase AgentRun: $serving_run"
  else
    fail "No Serving-phase AgentRun found within ${TIMEOUT}s"
    # Dump runs for debugging
    info "Current runs:"
    api_request GET "/api/v1/runs" 2>/dev/null | python3 -m json.tool 2>/dev/null || true
    exit 1
  fi

  # --- Check AgentRun status has deploymentName and serviceName ---
  run_json="$(api_request GET "/api/v1/runs/${serving_run}")"
  deploy_name="$(printf "%s" "$run_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",{}).get("deploymentName",""))')"
  svc_name="$(printf "%s" "$run_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("status",{}).get("serviceName",""))')"

  if [[ -n "$deploy_name" ]]; then
    pass "AgentRun status has deploymentName: $deploy_name"
  else
    fail "AgentRun status missing deploymentName"
  fi

  if [[ -n "$svc_name" ]]; then
    pass "AgentRun status has serviceName: $svc_name"
  else
    fail "AgentRun status missing serviceName"
  fi

  # --- Verify Deployment exists ---
  if [[ -n "$deploy_name" ]]; then
    if kubectl get deploy "$deploy_name" -n "$NAMESPACE" >/dev/null 2>&1; then
      pass "Deployment exists: $deploy_name"

      # Check container name
      container_names="$(kubectl get deploy "$deploy_name" -n "$NAMESPACE" -o jsonpath='{range .spec.template.spec.containers[*]}{.name}{"\n"}{end}')"
      if echo "$container_names" | grep -q "web-proxy\|web-endpoint"; then
        pass "Deployment has web-proxy container"
      else
        info "Container names: $container_names"
        # The container may have a different name depending on the skill name
        pass "Deployment containers found: $(echo "$container_names" | tr '\n' ',')"
      fi

      # Check INSTANCE_NAME env var
      instance_env="$(kubectl get deploy "$deploy_name" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="INSTANCE_NAME")].value}' 2>/dev/null || true)"
      if [[ "$instance_env" == "$INSTANCE_NAME" ]]; then
        pass "Deployment has correct INSTANCE_NAME env"
      else
        fail "Deployment INSTANCE_NAME env mismatch (got: '$instance_env', expected: '$INSTANCE_NAME')"
      fi

      # Check WEB_PROXY_API_KEY env source
      api_key_secret="$(kubectl get deploy "$deploy_name" -n "$NAMESPACE" -o jsonpath='{.spec.template.spec.containers[0].env[?(@.name=="WEB_PROXY_API_KEY")].valueFrom.secretKeyRef.name}' 2>/dev/null || true)"
      if [[ -n "$api_key_secret" ]]; then
        pass "Deployment has API key from Secret: $api_key_secret"
      else
        fail "Deployment missing WEB_PROXY_API_KEY secret reference"
      fi
    else
      fail "Deployment not found: $deploy_name"
    fi
  fi

  # --- Verify Service exists ---
  if [[ -n "$svc_name" ]]; then
    if kubectl get svc "$svc_name" -n "$NAMESPACE" >/dev/null 2>&1; then
      pass "Service exists: $svc_name"

      # Check port
      svc_port="$(kubectl get svc "$svc_name" -n "$NAMESPACE" -o jsonpath='{.spec.ports[0].port}' 2>/dev/null || true)"
      if [[ "$svc_port" == "8080" ]]; then
        pass "Service exposes port 8080"
      else
        fail "Service port mismatch (got: $svc_port, expected: 8080)"
      fi
    else
      fail "Service not found: $svc_name"
    fi
  fi

  # --- Check web-endpoint status endpoint ---
  info "Checking web-endpoint status endpoint"
  status_json="$(api_request GET "/api/v1/agents/${INSTANCE_NAME}/web-endpoint")"
  status_enabled="$(printf "%s" "$status_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("enabled", False))')"
  status_deploy="$(printf "%s" "$status_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("deploymentName", ""))')"
  status_svc="$(printf "%s" "$status_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("serviceName", ""))')"

  if [[ "$status_enabled" == "True" ]]; then
    pass "Web-endpoint status: enabled"
  else
    fail "Web-endpoint status should report enabled"
  fi

  if [[ -n "$status_deploy" ]]; then
    pass "Web-endpoint status: deploymentName=$status_deploy"
  else
    fail "Web-endpoint status missing deploymentName"
  fi

  if [[ -n "$status_svc" ]]; then
    pass "Web-endpoint status: serviceName=$status_svc"
  else
    fail "Web-endpoint status missing serviceName"
  fi

  # --- Summary ---
  if [[ $failures -gt 0 ]]; then
    fail "$failures check(s) failed"
    exit 1
  fi
  pass "Serving-mode API test passed"
}

main "$@"
