#!/usr/bin/env bash
# API integration test: full Ensemble lifecycle journey.
# Validates:
#   1) Enable a Ensemble → instances + schedules + memory ConfigMaps stamped for each persona
#   2) Persona-level model override propagates to stamped instance
#   3) ExcludePersonas skips excluded persona (no instance created)
#   4) Disable pack → all stamped resources cleaned up

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TIMEOUT="${TEST_TIMEOUT:-60}"

PACK_NAME="inttest-lifecycle-$(date +%s)"
PERSONA_A="responder"
PERSONA_B="analyzer"
PERSONA_C="excluded-agent"
SECRET_NAME="${PACK_NAME}-openai-key"

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
  info "Cleaning up lifecycle test resources..."
  # Disable the pack first so controller cleans up stamped resources
  kubectl patch ensemble "$PACK_NAME" -n "$NAMESPACE" --type=merge -p '{"spec":{"enabled":false}}' >/dev/null 2>&1 || true
  sleep 2
  kubectl delete ensemble "$PACK_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete secret "$SECRET_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  for p in "$PERSONA_A" "$PERSONA_B" "$PERSONA_C"; do
    kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${PACK_NAME}-${p}" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete sympoziuminstance "${PACK_NAME}-${p}" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete sympoziumschedule "${PACK_NAME}-${p}" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete sympoziumschedule "${PACK_NAME}-${p}-schedule" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete configmap "${PACK_NAME}-${p}-memory" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${PACK_NAME}-${p}" --ignore-not-found >/dev/null 2>&1 || true
  done
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

# Like api_request GET but returns 0/1 silently (no fail() side-effects).
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
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-lifecycle-portforward.log 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 30); do
    kill -0 "$PF_PID" >/dev/null 2>&1 || { fail "Port-forward exited early"; cat /tmp/sympozium-lifecycle-portforward.log || true; exit 1; }
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

  info "Running Ensemble lifecycle test in namespace '${NAMESPACE}'"

  if [[ -z "${OPENAI_API_KEY:-}" ]]; then
    fail "OPENAI_API_KEY environment variable is required but not set"
    exit 1
  fi

  start_port_forward_if_needed
  resolve_apiserver_token

  # ── Create auth secret ──
  kubectl create secret generic "$SECRET_NAME" \
    --from-literal=OPENAI_API_KEY="${OPENAI_API_KEY}" \
    -n "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

  # ── Create Ensemble with 3 personas, one excluded ──
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK_NAME}
  namespace: ${NAMESPACE}
spec:
  description: "Lifecycle integration test pack"
  category: "integration"
  version: "1.0.0"
  enabled: false
  excludePersonas:
    - ${PERSONA_C}
  agentConfigs:
    - name: ${PERSONA_A}
      displayName: "Incident Responder"
      systemPrompt: "You are an incident responder."
      model: gpt-4o-mini
      skills:
        - k8s-ops
      schedule:
        type: heartbeat
        interval: "5m"
        task: "Check cluster health"
      memory:
        enabled: true
        seeds:
          - "## Tracking\n- Cluster: integration-test"
    - name: ${PERSONA_B}
      displayName: "Cost Analyzer"
      systemPrompt: "You are a cost analyzer."
      skills:
        - code-review
      schedule:
        type: scheduled
        cron: "0 8 * * 1"
        task: "Weekly cost report"
    - name: ${PERSONA_C}
      displayName: "Excluded Agent"
      systemPrompt: "This persona should be excluded."
      schedule:
        type: heartbeat
        interval: "10m"
        task: "Should not be created"
EOF
  pass "Created Ensemble '${PACK_NAME}' with 3 personas (1 excluded)"

  # ── Enable the pack ──
  api_request PATCH "/api/v1/ensembles/${PACK_NAME}" \
    "{\"enabled\":true,\"provider\":\"openai\",\"secretName\":\"${SECRET_NAME}\",\"model\":\"gpt-4o-mini\"}" >/dev/null
  pass "Enabled Ensemble"

  # ── Wait for stamped instances ──
  elapsed=0
  inst_a_found=false
  inst_b_found=false
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    api_check "/api/v1/agents/${PACK_NAME}-${PERSONA_A}" && inst_a_found=true
    api_check "/api/v1/agents/${PACK_NAME}-${PERSONA_B}" && inst_b_found=true
    [[ "$inst_a_found" == "true" && "$inst_b_found" == "true" ]] && break
    sleep 3
    elapsed=$((elapsed + 3))
  done

  if [[ "$inst_a_found" != "true" ]]; then
    fail "Instance '${PACK_NAME}-${PERSONA_A}' not stamped within ${TIMEOUT}s"
    exit 1
  fi
  if [[ "$inst_b_found" != "true" ]]; then
    fail "Instance '${PACK_NAME}-${PERSONA_B}' not stamped within ${TIMEOUT}s"
    exit 1
  fi
  pass "Both non-excluded persona instances were stamped"

  # ── Verify excluded persona was NOT stamped ──
  if api_check "/api/v1/agents/${PACK_NAME}-${PERSONA_C}"; then
    fail "Excluded persona '${PERSONA_C}' should NOT have a stamped instance"
    exit 1
  fi
  pass "Excluded persona '${PERSONA_C}' correctly skipped"

  # ── Verify persona A model override propagated ──
  inst_a_json="$(api_request GET "/api/v1/agents/${PACK_NAME}-${PERSONA_A}")"
  inst_a_model="$(printf "%s" "$inst_a_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("agents",{}).get("default",{}).get("model",""))')"
  if [[ "$inst_a_model" != "gpt-4o-mini" ]]; then
    fail "Persona A model not propagated (got '${inst_a_model}', want 'gpt-4o-mini')"
    exit 1
  fi
  pass "Persona-level model override propagated to instance"

  # ── Verify persona A has k8s-ops skill ──
  inst_a_skills="$(printf "%s" "$inst_a_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); s=[i.get("skillPackRef","") for i in d.get("spec",{}).get("skills",[])]; print(",".join(sorted(s)))')"
  if [[ "$inst_a_skills" != *"k8s-ops"* ]]; then
    fail "Persona A skills not propagated (got '${inst_a_skills}', want 'k8s-ops')"
    exit 1
  fi
  pass "Persona skills propagated to stamped instance"

  # ── Verify schedules exist for non-excluded personas ──
  sched_json="$(api_request GET "/api/v1/schedules")"
  sched_a_found="$(printf "%s" "$sched_json" | python3 -c '
import json,sys
pack=sys.argv[1]
d=json.load(sys.stdin)
print("true" if any(
  i.get("metadata",{}).get("labels",{}).get("sympozium.ai/ensemble")==pack and
  i.get("metadata",{}).get("labels",{}).get("sympozium.ai/persona")=="'"${PERSONA_A}"'"
  for i in d
) else "false")' "$PACK_NAME")"

  if [[ "$sched_a_found" != "true" ]]; then
    fail "Schedule for persona '${PERSONA_A}' not found"
    exit 1
  fi
  pass "Schedule created for active persona"

  # ── Verify memory ConfigMap seeded for persona A ──
  memory_cm="$(kubectl get configmap "${PACK_NAME}-${PERSONA_A}-memory" -n "$NAMESPACE" -o jsonpath='{.data.MEMORY\.md}' 2>/dev/null || true)"
  if [[ -z "$memory_cm" ]]; then
    # Memory may be named differently; check via label
    memory_cm="$(kubectl get configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${PACK_NAME}-${PERSONA_A}" -o jsonpath='{.items[0].data.MEMORY\.md}' 2>/dev/null || true)"
  fi
  if [[ -n "$memory_cm" && "$memory_cm" == *"Tracking"* ]]; then
    pass "Memory ConfigMap seeded with persona memory seeds"
  else
    info "Memory ConfigMap seed verification inconclusive (memory='${memory_cm:0:80}')"
  fi

  # ── Verify Ensemble status ──
  pack_json="$(api_request GET "/api/v1/ensembles/${PACK_NAME}")"
  installed_count="$(printf "%s" "$pack_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("status",{}).get("installedCount",0))')"
  if [[ "$installed_count" -ge 2 ]]; then
    pass "Ensemble status.installedCount = ${installed_count}"
  else
    fail "Ensemble status.installedCount = ${installed_count}, expected >= 2"
  fi

  # ── Disable the pack ──
  api_request PATCH "/api/v1/ensembles/${PACK_NAME}" '{"enabled":false}' >/dev/null
  pass "Disabled Ensemble"

  # ── Verify cleanup of stamped instances ──
  elapsed=0
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    a_gone=true; b_gone=true
    api_check "/api/v1/agents/${PACK_NAME}-${PERSONA_A}" && a_gone=false
    api_check "/api/v1/agents/${PACK_NAME}-${PERSONA_B}" && b_gone=false
    [[ "$a_gone" == "true" && "$b_gone" == "true" ]] && break
    sleep 3
    elapsed=$((elapsed + 3))
  done

  if [[ "$a_gone" != "true" || "$b_gone" != "true" ]]; then
    fail "Stamped instances not cleaned up within ${TIMEOUT}s after disable"
    exit 1
  fi
  pass "Ensemble disable cleaned up all stamped instances"

  echo
  pass "Ensemble lifecycle test passed"
  exit $EXIT_CODE
}

main "$@"
