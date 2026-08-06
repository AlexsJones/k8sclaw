#!/usr/bin/env bash
# API integration test: OpenTelemetry + observability endpoint correctness.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"

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
  info "Cleaning up observability API test resources..."
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
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-api-observability-portforward.log 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-api-observability-portforward.log || true
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

  info "Running observability API test in namespace '${NAMESPACE}'"

  # Ensure OTEL collector control-plane resources are present and healthy.
  kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-otel-collector >/dev/null
  available_replicas="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-otel-collector -o jsonpath='{.status.availableReplicas}' 2>/dev/null || echo 0)"
  if [[ -z "$available_replicas" || "$available_replicas" -lt 1 ]]; then
    fail "sympozium-otel-collector has no available replicas"
    exit 1
  fi
  pass "OTEL collector deployment is available"

  start_port_forward_if_needed
  resolve_apiserver_token

  metrics_json="$(api_request GET "/api/v1/observability/metrics")"

  collector_reachable="$(printf "%s" "$metrics_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); v=d.get("collectorReachable"); print("true" if v is True else ("false" if v is False else ""))')"
  collected_at="$(printf "%s" "$metrics_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("collectedAt",""))')"
  api_ns="$(printf "%s" "$metrics_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("namespace",""))')"
  agent_runs_total="$(printf "%s" "$metrics_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); v=d.get("agentRunsTotal",0); print(v)')"
  raw_metric_count="$(printf "%s" "$metrics_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(len(d.get("rawMetricNames",[])))')"

  if [[ -z "$collector_reachable" ]]; then
    fail "collectorReachable missing or not boolean in observability API response"
    exit 1
  fi
  if [[ -z "$collected_at" ]]; then
    fail "observability response missing collectedAt"
    exit 1
  fi
  if [[ "$api_ns" != "$NAMESPACE" ]]; then
    fail "observability namespace mismatch: got '${api_ns}', expected '${NAMESPACE}'"
    exit 1
  fi
  # When the collector is reachable but no agent runs have emitted OTLP
  # telemetry yet, rawMetricNames will be empty (cold start). This is
  # expected.  Only assert >= 3 when the collector is unreachable and the
  # fallback CRD-aggregation path populates hardcoded metric names.
  if [[ "$collector_reachable" != "true" && "$raw_metric_count" -lt 3 ]]; then
    fail "observability response rawMetricNames too small (got ${raw_metric_count})"
    exit 1
  fi
  if [[ "$collector_reachable" == "true" && "$raw_metric_count" -eq 0 ]]; then
    info "Collector reachable but no metrics yet (cold start) — rawMetricNames=0 is acceptable"
  fi

  # Numeric sanity check (non-negative).
  python3 - <<'PY' "$agent_runs_total"
import sys
v=float(sys.argv[1])
if v < 0:
    raise SystemExit(1)
PY

  pass "Observability API payload validated (collectorReachable=${collector_reachable})"
  pass "Observability API test passed"
}

main "$@"
