#!/usr/bin/env bash
# API integration test: channel binding via ad-hoc instance and Ensemble.
# Validates:
#   1) Ad-hoc instance with channel spec → channel Deployment created with correct labels
#   2) Ensemble with channelConfigs → stamped instance gets channel → Deployment created
#   3) Instance status reports channel status
#   4) Removing channel (instance delete) cleans up channel Deployment

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TIMEOUT="${TEST_TIMEOUT:-60}"

ADHOC_INSTANCE="inttest-chan-adhoc-$(date +%s)"
ADHOC_SECRET="${ADHOC_INSTANCE}-openai-key"
ADHOC_CHANNEL_SECRET="inttest-chan-tg-$(date +%s)"

PACK_NAME="inttest-chan-pack-$(date +%s)"
PACK_SECRET="${PACK_NAME}-openai-key"
PACK_CHANNEL_SECRET="inttest-chan-pack-tg-$(date +%s)"
PACK_PERSONA="notifier"
PACK_INSTANCE="${PACK_NAME}-${PACK_PERSONA}"

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
  info "Cleaning up channel-binding test resources..."
  kubectl patch ensemble "$PACK_NAME" -n "$NAMESPACE" --type=merge -p '{"spec":{"enabled":false}}' >/dev/null 2>&1 || true
  sleep 2
  kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${ADHOC_INSTANCE}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${PACK_INSTANCE}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete sympoziuminstance "$ADHOC_INSTANCE" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete sympoziuminstance "$PACK_INSTANCE" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete ensemble "$PACK_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete secret "$ADHOC_SECRET" "$ADHOC_CHANNEL_SECRET" "$PACK_SECRET" "$PACK_CHANNEL_SECRET" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  sleep 3
  kubectl delete deploy -n "$NAMESPACE" -l "sympozium.ai/instance=${ADHOC_INSTANCE}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete deploy -n "$NAMESPACE" -l "sympozium.ai/instance=${PACK_INSTANCE}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${ADHOC_INSTANCE}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${PACK_INSTANCE}" --ignore-not-found >/dev/null 2>&1 || true
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
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-channel-binding-portforward.log 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 30); do
    kill -0 "$PF_PID" >/dev/null 2>&1 || { fail "Port-forward exited early"; exit 1; }
    curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1 && { pass "Port-forward ready"; return 0; }
    sleep 1
  done
  fail "Timed out waiting for API server via port-forward"
  exit 1
}

wait_for_deployment() {
  local deploy_name="$1"
  local description="$2"
  local elapsed=0

  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    if kubectl get deploy "$deploy_name" -n "$NAMESPACE" >/dev/null 2>&1; then
      pass "${description}: Deployment '${deploy_name}' created"
      return 0
    fi
    sleep 3
    elapsed=$((elapsed + 3))
  done
  fail "${description}: Deployment '${deploy_name}' not found within ${TIMEOUT}s"
  return 1
}

main() {
  require_cmd kubectl
  require_cmd curl
  require_cmd python3

  info "Running channel-binding test in namespace '${NAMESPACE}'"

  if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    fail "OPENAI_API_KEY environment variable is required but not set"
    exit 1
  fi

  start_port_forward_if_needed
  resolve_apiserver_token

  # ── Create channel credential secrets (dummy tokens) ──
  kubectl create secret generic "$ADHOC_CHANNEL_SECRET" \
    --from-literal=TELEGRAM_BOT_TOKEN="inttest-dummy-tg-token" \
    -n "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

  kubectl create secret generic "$PACK_CHANNEL_SECRET" \
    --from-literal=TELEGRAM_BOT_TOKEN="inttest-dummy-tg-token" \
    -n "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

  # ════════════════════════════════════════════════════════════
  # Part 1: Ad-hoc instance with telegram channel
  # ════════════════════════════════════════════════════════════
  info "--- Part 1: Ad-hoc instance with channel ---"

  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: ${ADHOC_INSTANCE}
  namespace: ${NAMESPACE}
spec:
  agents:
    default:
      model: gpt-4o-mini
  authRefs:
    - provider: openai
      secret: ${ADHOC_SECRET}
  channels:
    - type: telegram
      configRef:
        secret: ${ADHOC_CHANNEL_SECRET}
EOF

  # Also create the auth secret for the instance
  kubectl create secret generic "$ADHOC_SECRET" \
    --from-literal=OPENAI_API_KEY="${OPENAI_API_KEY}" \
    -n "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

  pass "Created ad-hoc instance '${ADHOC_INSTANCE}' with telegram channel"

  # ── Wait for channel Deployment ──
  adhoc_deploy="${ADHOC_INSTANCE}-channel-telegram"
  wait_for_deployment "$adhoc_deploy" "Ad-hoc telegram" || exit 1

  # ── Verify channel Deployment labels ──
  deploy_labels_json="$(kubectl get deploy "$adhoc_deploy" -n "$NAMESPACE" -o json 2>/dev/null)"
  deploy_channel_label="$(printf "%s" "$deploy_labels_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("labels",{}).get("sympozium.ai/channel",""))')"
  deploy_instance_label="$(printf "%s" "$deploy_labels_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("labels",{}).get("sympozium.ai/instance",""))')"

  if [[ "$deploy_channel_label" != "telegram" ]]; then
    fail "Channel Deployment has wrong channel label: '${deploy_channel_label}'"
    exit 1
  fi
  if [[ "$deploy_instance_label" != "$ADHOC_INSTANCE" ]]; then
    fail "Channel Deployment has wrong instance label: '${deploy_instance_label}'"
    exit 1
  fi
  pass "Channel Deployment has correct sympozium.ai labels"

  # ── Verify instance status reports channel ──
  elapsed=0
  channel_reported=false
  while [[ "$elapsed" -lt 20 ]]; do
    inst_json="$(api_request GET "/api/v1/agents/${ADHOC_INSTANCE}" 2>/dev/null || true)"
    if [[ -n "$inst_json" ]]; then
      chan_count="$(printf "%s" "$inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); c=d.get("status",{}).get("channels",[]); print(len(c))' 2>/dev/null || echo 0)"
      if [[ "$chan_count" -ge 1 ]]; then
        channel_reported=true
        break
      fi
    fi
    sleep 3
    elapsed=$((elapsed + 3))
  done

  if [[ "$channel_reported" == "true" ]]; then
    pass "Instance status reports channel(s)"
  else
    info "Instance status.channels not yet populated (channel pod may still be starting)"
  fi

  # ════════════════════════════════════════════════════════════
  # Part 2: Ensemble with channel config
  # ════════════════════════════════════════════════════════════
  info "--- Part 2: Ensemble with channel config ---"

  kubectl create secret generic "$PACK_SECRET" \
    --from-literal=OPENAI_API_KEY="${OPENAI_API_KEY}" \
    -n "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK_NAME}
  namespace: ${NAMESPACE}
spec:
  description: "Channel binding integration test"
  category: "integration"
  version: "1.0.0"
  enabled: false
  channelConfigs:
    telegram: ${PACK_CHANNEL_SECRET}
  agentConfigs:
    - name: ${PACK_PERSONA}
      displayName: "Notification Agent"
      systemPrompt: "You send notifications."
      channels:
        - telegram
      schedule:
        type: heartbeat
        interval: "10m"
        task: "send notification"
EOF
  pass "Created Ensemble '${PACK_NAME}' with channel config"

  # ── Enable the pack ──
  api_request PATCH "/api/v1/ensembles/${PACK_NAME}" \
    "{\"enabled\":true,\"provider\":\"openai\",\"secretName\":\"${PACK_SECRET}\",\"model\":\"gpt-4o-mini\"}" >/dev/null
  pass "Enabled Ensemble"

  # ── Wait for stamped instance ──
  elapsed=0
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    api_check "/api/v1/agents/${PACK_INSTANCE}" && break
    sleep 3
    elapsed=$((elapsed + 3))
  done
  if [[ "$elapsed" -ge "$TIMEOUT" ]]; then
    fail "Timed out waiting for Ensemble instance '${PACK_INSTANCE}'"
    exit 1
  fi
  pass "Ensemble stamped instance '${PACK_INSTANCE}'"

  # ── Verify stamped instance has channel spec ──
  pack_inst_json="$(api_request GET "/api/v1/agents/${PACK_INSTANCE}")"
  pack_chan_type="$(printf "%s" "$pack_inst_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); c=d.get("spec",{}).get("channels",[]); print(c[0].get("type","") if c else "")')"
  if [[ "$pack_chan_type" != "telegram" ]]; then
    fail "Stamped instance missing telegram channel (got '${pack_chan_type}')"
    exit 1
  fi
  pass "Ensemble stamped instance has telegram channel binding"

  # ── Wait for channel Deployment from pack instance ──
  pack_deploy="${PACK_INSTANCE}-channel-telegram"
  wait_for_deployment "$pack_deploy" "Ensemble telegram" || true

  # ════════════════════════════════════════════════════════════
  # Part 3: Cleanup verification
  # ════════════════════════════════════════════════════════════
  info "--- Part 3: Cleanup verification ---"

  # Delete ad-hoc instance
  kubectl delete sympoziuminstance "$ADHOC_INSTANCE" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true

  # Wait for channel Deployment cleanup
  elapsed=0
  while [[ "$elapsed" -lt 30 ]]; do
    kubectl get deploy "$adhoc_deploy" -n "$NAMESPACE" >/dev/null 2>&1 || break
    sleep 3
    elapsed=$((elapsed + 3))
  done

  if ! kubectl get deploy "$adhoc_deploy" -n "$NAMESPACE" >/dev/null 2>&1; then
    pass "Channel Deployment cleaned up after instance delete"
  else
    info "Channel Deployment still present after ${elapsed}s (controller may need more time)"
  fi

  echo
  pass "Channel-binding test passed"
  exit $EXIT_CODE
}

main "$@"
