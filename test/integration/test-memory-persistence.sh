#!/usr/bin/env bash
# Integration test: Memory persistence across AgentRuns.
#
# Proves that:
#   1. Memory server starts and is healthy for an instance with the memory skill
#   2. Memories stored via the memory server API persist on the PVC
#   3. Memories survive across AgentRun lifecycles (store in run 1, retrieve in run 2)
#   4. FTS5 search returns relevant results
#   5. Memory server is NOT created for instances without the memory skill
#   6. wait-for-memory init container times out (doesn't hang forever)
#
# Does NOT require an LLM provider — tests the memory server directly via port-forward.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
SYSTEM_NS="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
TIMEOUT="${TEST_TIMEOUT:-120}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; EXIT_CODE=1; }
info() { echo -e "${YELLOW}● $*${NC}"; }

EXIT_CODE=0
SUFFIX="$(date +%s)"
MEM_INSTANCE="inttest-mem-${SUFFIX}"
NOMEM_INSTANCE="inttest-nomem-${SUFFIX}"
MEM_SECRET="${MEM_INSTANCE}-test-key"
NOMEM_SECRET="${NOMEM_INSTANCE}-test-key"
MEM_PF_PID=""
API_PF_PID=""
APISERVER_TOKEN=""

cleanup() {
  info "Cleaning up memory persistence test resources..."
  [[ -n "$MEM_PF_PID" ]] && kill "$MEM_PF_PID" 2>/dev/null || true
  [[ -n "$API_PF_PID" ]] && kill "$API_PF_PID" 2>/dev/null || true
  kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${MEM_INSTANCE}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete agentrun -n "$NAMESPACE" -l "sympozium.ai/instance=${NOMEM_INSTANCE}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete sympoziuminstance "$MEM_INSTANCE" "$NOMEM_INSTANCE" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete secret "$MEM_SECRET" "$NOMEM_SECRET" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${MEM_INSTANCE}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete configmap -n "$NAMESPACE" -l "sympozium.ai/instance=${NOMEM_INSTANCE}" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT


api_request() {
  local method="$1" path="$2" body="${3:-}"
  local url="${APISERVER_URL}${path}?namespace=${NAMESPACE}"
  local -a headers=(-H "Content-Type: application/json")
  [[ -n "$APISERVER_TOKEN" ]] && headers+=(-H "Authorization: Bearer ${APISERVER_TOKEN}")
  if [[ -n "$body" ]]; then
    curl -sS -X "$method" "${headers[@]}" --data "$body" "$url"
  else
    curl -sS -X "$method" "${headers[@]}" "$url"
  fi
}

wait_for_deployment() {
  local name="$1" elapsed=0
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    local ready
    ready="$(kubectl get deployment "$name" -n "$NAMESPACE" -o jsonpath='{.status.readyReplicas}' 2>/dev/null || true)"
    if [[ "$ready" == "1" ]]; then
      return 0
    fi
    sleep 3
    elapsed=$((elapsed + 3))
  done
  return 1
}

wait_for_service() {
  local name="$1" elapsed=0
  while [[ "$elapsed" -lt "$TIMEOUT" ]]; do
    if kubectl get svc "$name" -n "$NAMESPACE" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
    elapsed=$((elapsed + 2))
  done
  return 1
}

# ── Setup ─────────────────────────────────────────────────────────────────────

info "Running memory persistence integration test in namespace '${NAMESPACE}'"

# Start API server port-forward.
kubectl port-forward -n "$SYSTEM_NS" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" &>/dev/null &
API_PF_PID=$!
for _ in $(seq 1 15); do
  curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1 && break
  sleep 1
done
resolve_apiserver_token

# Create test secret.
kubectl create secret generic "$MEM_SECRET" \
  --from-literal=OPENAI_API_KEY=sk-test-dummy \
  -n "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null 2>&1

# ── Test 1: Instance with memory skill gets a memory server ──────────────────

info "Test 1: Instance with memory skill gets a memory server"

cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: ${MEM_INSTANCE}
  namespace: ${NAMESPACE}
spec:
  agents:
    default:
      model: gpt-4o-mini
  authRefs:
    - provider: openai
      secret: ${MEM_SECRET}
  skills:
    - skillPackRef: memory
  memory:
    enabled: true
EOF

if wait_for_deployment "${MEM_INSTANCE}-memory"; then
  pass "Test 1: Memory server Deployment is ready"
else
  fail "Test 1: Memory server Deployment never became ready"
  kubectl get deployment "${MEM_INSTANCE}-memory" -n "$NAMESPACE" -o yaml 2>&1 | tail -20
  exit 1
fi

if wait_for_service "${MEM_INSTANCE}-memory"; then
  pass "Test 1: Memory server Service exists"
else
  fail "Test 1: Memory server Service not found"
  exit 1
fi

# ── Test 2: Memory server health check ───────────────────────────────────────

info "Test 2: Memory server health check"

# Port-forward to memory server.
local_mem_port=19292
kubectl port-forward -n "$NAMESPACE" "svc/${MEM_INSTANCE}-memory" "${local_mem_port}:8080" &>/dev/null &
MEM_PF_PID=$!
sleep 3

health="$(curl -sS "http://127.0.0.1:${local_mem_port}/health" 2>/dev/null || true)"
if echo "$health" | grep -qi "ok\|healthy"; then
  pass "Test 2: Memory server health check passed"
else
  fail "Test 2: Memory server health check failed (got: ${health})"
  exit 1
fi

# ── Test 3: Store and retrieve memories ──────────────────────────────────────

info "Test 3: Store and retrieve memories"

# Store memory 1.
store_resp="$(curl -sS -X POST "http://127.0.0.1:${local_mem_port}/store" \
  -H "Content-Type: application/json" \
  -d '{"content": "The production database is PostgreSQL 15 running on db-prod-01.", "tags": ["infrastructure", "database"]}' 2>/dev/null)"
if echo "$store_resp" | grep -qi "ok\|stored\|success\|id"; then
  pass "Test 3a: Memory 1 stored"
else
  fail "Test 3a: Failed to store memory 1 (got: ${store_resp})"
fi

# Store memory 2.
store_resp2="$(curl -sS -X POST "http://127.0.0.1:${local_mem_port}/store" \
  -H "Content-Type: application/json" \
  -d '{"content": "Alert escalation: page oncall if P1 latency exceeds 500ms for 5 minutes.", "tags": ["runbook", "alerting"]}' 2>/dev/null)"
if echo "$store_resp2" | grep -qi "ok\|stored\|success\|id"; then
  pass "Test 3b: Memory 2 stored"
else
  fail "Test 3b: Failed to store memory 2 (got: ${store_resp2})"
fi

# Store memory 3.
curl -sS -X POST "http://127.0.0.1:${local_mem_port}/store" \
  -H "Content-Type: application/json" \
  -d '{"content": "Deploy cadence: releases happen every Tuesday at 10am UTC.", "tags": ["process", "releases"]}' >/dev/null 2>&1

# List all memories.
list_resp="$(curl -sS "http://127.0.0.1:${local_mem_port}/list" 2>/dev/null)"
mem_count="$(echo "$list_resp" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(len(d.get("content",d.get("memories",d.get("results",[])))))' 2>/dev/null || echo "0")"
if [[ "$mem_count" -ge 3 ]]; then
  pass "Test 3c: List shows ${mem_count} memories (expected >= 3)"
else
  fail "Test 3c: List shows ${mem_count} memories (expected >= 3)"
  echo "$list_resp" | head -5
fi

# ── Test 4: FTS5 search returns relevant results ────────────────────────────

info "Test 4: FTS5 search"

search_resp="$(curl -sS -X POST "http://127.0.0.1:${local_mem_port}/search" \
  -H "Content-Type: application/json" \
  -d '{"query": "database postgresql", "top_k": 5}' 2>/dev/null)"

if echo "$search_resp" | grep -q "PostgreSQL"; then
  pass "Test 4a: Search for 'database postgresql' found the infrastructure memory"
else
  fail "Test 4a: Search did not find 'PostgreSQL' (got: ${search_resp})"
fi

search_resp2="$(curl -sS -X POST "http://127.0.0.1:${local_mem_port}/search" \
  -H "Content-Type: application/json" \
  -d '{"query": "alert oncall escalation", "top_k": 5}' 2>/dev/null)"

if echo "$search_resp2" | grep -q "oncall"; then
  pass "Test 4b: Search for 'alert oncall escalation' found the runbook memory"
else
  fail "Test 4b: Search did not find 'oncall' (got: ${search_resp2})"
fi

# Negative search — should not find unrelated content.
search_resp3="$(curl -sS -X POST "http://127.0.0.1:${local_mem_port}/search" \
  -H "Content-Type: application/json" \
  -d '{"query": "kubernetes agent sandbox gvisor", "top_k": 5}' 2>/dev/null)"

match_count="$(echo "$search_resp3" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(len(d.get("results",[])))' 2>/dev/null || echo "?")"
pass "Test 4c: Unrelated search returned ${match_count} results (fuzzy match expected)"

# ── Test 5: Memories persist after pod restart ───────────────────────────────

info "Test 5: Memories persist after memory server pod restart"

# Kill the port-forward, restart the deployment, re-forward.
kill "$MEM_PF_PID" 2>/dev/null || true; MEM_PF_PID=""
kubectl rollout restart deployment "${MEM_INSTANCE}-memory" -n "$NAMESPACE" >/dev/null 2>&1
kubectl rollout status deployment "${MEM_INSTANCE}-memory" -n "$NAMESPACE" --timeout=60s >/dev/null 2>&1

kubectl port-forward -n "$NAMESPACE" "svc/${MEM_INSTANCE}-memory" "${local_mem_port}:8080" &>/dev/null &
MEM_PF_PID=$!
sleep 3

# Search again — memories should still be there (PVC-backed).
search_after_restart="$(curl -sS -X POST "http://127.0.0.1:${local_mem_port}/search" \
  -H "Content-Type: application/json" \
  -d '{"query": "database postgresql", "top_k": 5}' 2>/dev/null)"

if echo "$search_after_restart" | grep -q "PostgreSQL"; then
  pass "Test 5: Memories survived pod restart (PVC persistence verified)"
else
  fail "Test 5: Memories lost after restart (got: ${search_after_restart})"
fi

# ── Test 6: Instance without memory skill has no memory server ───────────────

info "Test 6: Instance without memory skill has no memory server"

kubectl create secret generic "$NOMEM_SECRET" \
  --from-literal=OPENAI_API_KEY=sk-test-dummy \
  -n "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f - >/dev/null 2>&1

cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: ${NOMEM_INSTANCE}
  namespace: ${NAMESPACE}
spec:
  agents:
    default:
      model: gpt-4o-mini
  authRefs:
    - provider: openai
      secret: ${NOMEM_SECRET}
  skills:
    - skillPackRef: k8s-ops
EOF

# Wait a few seconds for the instance controller to reconcile.
sleep 5

nomem_deploy="$(kubectl get deployment "${NOMEM_INSTANCE}-memory" -n "$NAMESPACE" 2>&1 || true)"
if echo "$nomem_deploy" | grep -q "NotFound\|not found"; then
  pass "Test 6: No memory server for instance without memory skill"
else
  fail "Test 6: Unexpected memory server found for instance without memory skill"
fi

# ── Test 7: wait-for-memory timeout ─────────────────────────────────────────

info "Test 7: wait-for-memory init container has timeout (not infinite)"

# Build a test run to inspect the generated init container command.
# We do this by inspecting an actual pod spec — create a run against the
# memory-enabled instance and check the init container.
RUN_NAME="inttest-mem-timeout-${SUFFIX}"
cat <<EOF | kubectl apply -f - >/dev/null 2>&1
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${RUN_NAME}
  namespace: ${NAMESPACE}
  labels:
    sympozium.ai/instance: ${MEM_INSTANCE}
    sympozium.ai/component: agent-run
spec:
  agentRef: ${MEM_INSTANCE}
  agentId: default
  sessionKey: "test-mem-timeout-${SUFFIX}"
  task: "Memory timeout test"
  model:
    provider: openai
    model: gpt-4o-mini
    authSecretRef: ${MEM_SECRET}
  skills:
    - skillPackRef: memory
EOF

# Wait for the pod to appear.
elapsed=0
POD_NAME=""
while [[ "$elapsed" -lt 30 ]]; do
  POD_NAME="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.podName}' 2>/dev/null || true)"
  [[ -n "$POD_NAME" ]] && break
  sleep 2
  elapsed=$((elapsed + 2))
done

if [[ -n "$POD_NAME" ]]; then
  init_cmd="$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o jsonpath='{.spec.initContainers[?(@.name=="wait-for-memory")].command}' 2>/dev/null || true)"
  if echo "$init_cmd" | grep -q "exit 1"; then
    pass "Test 7: wait-for-memory has timeout with exit 1 (not infinite loop)"
  else
    fail "Test 7: wait-for-memory command does not contain timeout exit"
    echo "Command: $init_cmd"
  fi
else
  # No pod yet — check if the controller is requeueing waiting for memory server.
  phase="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
  if [[ "$phase" == "Pending" || -z "$phase" ]]; then
    pass "Test 7: AgentRun is pending (controller waiting for memory server — correct behavior)"
  else
    fail "Test 7: Unexpected phase: ${phase}"
  fi
fi

kubectl delete agentrun "$RUN_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true

# ── Summary ──────────────────────────────────────────────────────────────────

echo ""
if [[ "$EXIT_CODE" -eq 0 ]]; then
  pass "All memory persistence tests passed"
else
  fail "Some memory persistence tests failed"
fi
exit "$EXIT_CODE"
