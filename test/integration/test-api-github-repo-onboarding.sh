#!/usr/bin/env bash
# API integration test: GitHub repo onboarding flow.
# Validates:
#  1) Ad-hoc instance created via API with github-gitops skill and repo param.
#  2) Ensemble with github-gitops skill propagates skillParams to stamped instances.
#  3) Skill params (repo) survive through instance creation and run creation.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TIMEOUT="${TEST_TIMEOUT:-180}"

PACK_NAME="inttest-ghrepo-pack-$(date +%s)"
PERSONA_NAME="dev"
PACK_INSTANCE_NAME="${PACK_NAME}-${PERSONA_NAME}"
PACK_SECRET_NAME="${PACK_NAME}-openai-key"

ADHOC_INSTANCE_NAME="inttest-ghrepo-adhoc-$(date +%s)"
ADHOC_SECRET_NAME="${ADHOC_INSTANCE_NAME}-openai-key"

GITHUB_REPO="octocat/Hello-World"
TEAM_TASK="Build the v2 REST API with backward compatibility"
MODEL_NAME="gpt-4o-mini"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; FAILURES=$((FAILURES + 1)); }
info() { echo -e "${YELLOW}● $*${NC}"; }

FAILURES=0
PF_PID=""
APISERVER_TOKEN="${APISERVER_TOKEN:-}"
ADHOC_RUN_NAME=""
PACK_RUN_NAME=""

stop_port_forward() {
  if [[ -n "${PF_PID}" ]] && kill -0 "${PF_PID}" >/dev/null 2>&1; then
    kill "${PF_PID}" >/dev/null 2>&1 || true
    for _ in {1..5}; do
      if ! kill -0 "${PF_PID}" >/dev/null 2>&1; then break; fi
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
  info "Cleaning up GitHub repo onboarding test resources..."
  [[ -n "$ADHOC_RUN_NAME" ]] && api_request DELETE "/api/v1/runs/${ADHOC_RUN_NAME}" >/dev/null 2>&1 || true
  [[ -n "$PACK_RUN_NAME" ]] && api_request DELETE "/api/v1/runs/${PACK_RUN_NAME}" >/dev/null 2>&1 || true

  api_request DELETE "/api/v1/agents/${ADHOC_INSTANCE_NAME}" >/dev/null 2>&1 || true
  api_request DELETE "/api/v1/agents/${PACK_INSTANCE_NAME}" >/dev/null 2>&1 || true
  api_request DELETE "/api/v1/ensembles/${PACK_NAME}" >/dev/null 2>&1 || true

  kubectl delete ensemble "$PACK_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete secret "$PACK_SECRET_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete secret "$ADHOC_SECRET_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true

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
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-api-ghrepo-portforward.log 2>&1 &
  PF_PID=$!

  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-api-ghrepo-portforward.log || true
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

# Extract github-gitops repo param from an instance JSON.
extract_github_repo() {
  local inst_json="$1"
  printf "%s" "$inst_json" | python3 -c '
import json, sys
d = json.load(sys.stdin)
skills = d.get("spec", {}).get("skills", [])
for sk in skills:
    if sk.get("skillPackRef") == "github-gitops":
        print(sk.get("params", {}).get("repo", ""))
        sys.exit(0)
print("")
'
}

# Check if github-gitops skill is present in instance.
has_github_skill() {
  local inst_json="$1"
  printf "%s" "$inst_json" | python3 -c '
import json, sys
d = json.load(sys.stdin)
skills = d.get("spec", {}).get("skills", [])
for sk in skills:
    if sk.get("skillPackRef") == "github-gitops":
        print("yes")
        sys.exit(0)
print("no")
'
}

main() {
  require_cmd kubectl
  require_cmd curl
  require_cmd python3

  info "Running GitHub repo onboarding API test in namespace '${NAMESPACE}'"

  start_port_forward_if_needed
  resolve_apiserver_token

  # ──────────────────────────────────────────────────────────────
  # Part 1: Ad-hoc instance with github-gitops skill + repo param
  # ──────────────────────────────────────────────────────────────
  info "Part 1: Ad-hoc instance with github-gitops repo param"

  api_request POST "/api/v1/agents" "{
    \"name\": \"${ADHOC_INSTANCE_NAME}\",
    \"provider\": \"openai\",
    \"model\": \"${MODEL_NAME}\",
    \"apiKey\": \"inttest-dummy-key\",
    \"skills\": [
      {\"skillPackRef\": \"k8s-ops\"},
      {\"skillPackRef\": \"github-gitops\", \"params\": {\"repo\": \"${GITHUB_REPO}\"}}
    ]
  }" >/dev/null
  pass "Created ad-hoc instance '${ADHOC_INSTANCE_NAME}'"

  # Verify instance has github-gitops skill with repo param.
  adhoc_inst_json="$(api_request GET "/api/v1/agents/${ADHOC_INSTANCE_NAME}")"

  got_skill="$(has_github_skill "$adhoc_inst_json")"
  if [[ "$got_skill" != "yes" ]]; then
    fail "Ad-hoc instance missing github-gitops skill"
  else
    pass "Ad-hoc instance has github-gitops skill"
  fi

  got_repo="$(extract_github_repo "$adhoc_inst_json")"
  if [[ "$got_repo" != "$GITHUB_REPO" ]]; then
    fail "Ad-hoc instance github repo mismatch (got '${got_repo}', want '${GITHUB_REPO}')"
  else
    pass "Ad-hoc instance github-gitops repo param is '${GITHUB_REPO}'"
  fi

  # Verify repo param survives into a run.
  adhoc_run_json="$(api_request POST "/api/v1/runs" "{\"agentRef\":\"${ADHOC_INSTANCE_NAME}\",\"task\":\"test github repo param\"}")"
  ADHOC_RUN_NAME="$(printf "%s" "$adhoc_run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("name",""))')"

  run_repo="$(extract_github_repo "$adhoc_run_json")"
  if [[ "$run_repo" != "$GITHUB_REPO" ]]; then
    fail "Ad-hoc run github repo mismatch (got '${run_repo}', want '${GITHUB_REPO}')"
  else
    pass "Ad-hoc run inherited github-gitops repo param"
  fi

  # ──────────────────────────────────────────────────────────────
  # Part 2: Ensemble with github-gitops + skillParams
  # ──────────────────────────────────────────────────────────────
  info "Part 2: Ensemble with skillParams for github-gitops"

  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: sympozium.ai/v1alpha1
kind: Ensemble
metadata:
  name: ${PACK_NAME}
  namespace: ${NAMESPACE}
spec:
  description: "Integration test pack with github-gitops"
  category: "integration"
  version: "1.0.0"
  enabled: false
  skillParams:
    github-gitops:
      repo: "${GITHUB_REPO}"
  taskOverride: "${TEAM_TASK}"
  agentConfigs:
    - name: ${PERSONA_NAME}
      displayName: "Test Developer"
      systemPrompt: "Integration test developer persona"
      model: ${MODEL_NAME}
      skills:
        - github-gitops
        - code-review
      schedule:
        type: scheduled
        cron: "*/10 * * * *"
        task: "test github task"
EOF
  pass "Created temporary Ensemble '${PACK_NAME}' with skillParams"

  # Create auth secret.
  kubectl create secret generic "$PACK_SECRET_NAME" \
    --from-literal=OPENAI_API_KEY="inttest-dummy-key" \
    -n "$NAMESPACE" \
    --dry-run=client -o yaml | kubectl apply -f - >/dev/null

  # Enable the pack.
  api_request PATCH "/api/v1/ensembles/${PACK_NAME}" \
    "{\"enabled\":true,\"provider\":\"openai\",\"secretName\":\"${PACK_SECRET_NAME}\",\"model\":\"${MODEL_NAME}\"}" >/dev/null

  # Wait for the controller to stamp out the instance.
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
  pass "Ensemble controller created instance '${PACK_INSTANCE_NAME}'"

  # Verify instance has github-gitops with repo param.
  pack_inst_json="$(api_request GET "/api/v1/agents/${PACK_INSTANCE_NAME}")"

  got_skill="$(has_github_skill "$pack_inst_json")"
  if [[ "$got_skill" != "yes" ]]; then
    fail "Pack instance missing github-gitops skill"
  else
    pass "Pack instance has github-gitops skill"
  fi

  got_repo="$(extract_github_repo "$pack_inst_json")"
  if [[ "$got_repo" != "$GITHUB_REPO" ]]; then
    fail "Pack instance github repo mismatch (got '${got_repo}', want '${GITHUB_REPO}')"
  else
    pass "Pack instance github-gitops repo param is '${GITHUB_REPO}'"
  fi

  # Verify repo param survives into a run.
  pack_run_json="$(api_request POST "/api/v1/runs" "{\"agentRef\":\"${PACK_INSTANCE_NAME}\",\"task\":\"test pack github repo\"}")"
  PACK_RUN_NAME="$(printf "%s" "$pack_run_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("metadata",{}).get("name",""))')"

  run_repo="$(extract_github_repo "$pack_run_json")"
  if [[ "$run_repo" != "$GITHUB_REPO" ]]; then
    fail "Pack run github repo mismatch (got '${run_repo}', want '${GITHUB_REPO}')"
  else
    pass "Pack run inherited github-gitops repo param"
  fi

  # Verify taskOverride is persisted on the Ensemble.
  pack_json="$(kubectl get ensemble "$PACK_NAME" -n "$NAMESPACE" -o json)"
  got_task="$(printf "%s" "$pack_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("taskOverride",""))')"
  if [[ "$got_task" != "$TEAM_TASK" ]]; then
    fail "Pack taskOverride mismatch (got '${got_task}', want '${TEAM_TASK}')"
  else
    pass "Pack taskOverride is set correctly"
  fi

  # Verify the schedule task includes the team objective.
  sched_name="${PACK_INSTANCE_NAME}-schedule"
  elapsed=0
  while [[ "$elapsed" -lt 30 ]]; do
    if kubectl get sympoziumschedule "$sched_name" -n "$NAMESPACE" >/dev/null 2>&1; then
      break
    fi
    sleep 2
    elapsed=$((elapsed + 2))
  done
  if kubectl get sympoziumschedule "$sched_name" -n "$NAMESPACE" >/dev/null 2>&1; then
    sched_json="$(kubectl get sympoziumschedule "$sched_name" -n "$NAMESPACE" -o json)"
    sched_task="$(printf "%s" "$sched_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("task",""))')"
    if echo "$sched_task" | grep -q "TEAM OBJECTIVE:"; then
      pass "Schedule task contains TEAM OBJECTIVE prefix"
    else
      fail "Schedule task missing TEAM OBJECTIVE prefix (got: ${sched_task:0:80})"
    fi
    if echo "$sched_task" | grep -q "test github task"; then
      pass "Schedule task preserves persona's original task"
    else
      fail "Schedule task missing persona's original task"
    fi
  else
    info "Schedule '${sched_name}' not found — skipping task override check (controller may not have created it yet)"
  fi

  # ──────────────────────────────────────────────────────────────
  # Summary
  # ──────────────────────────────────────────────────────────────
  echo ""
  if [[ "$FAILURES" -gt 0 ]]; then
    fail "GitHub repo onboarding test completed with ${FAILURES} failure(s)"
    exit 1
  else
    pass "All GitHub repo onboarding tests passed!"
  fi
}

main "$@"
