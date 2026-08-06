#!/usr/bin/env bash
# Integration test: SYMPOZIUM_UI_TOKEN rotation must propagate to a running
# apiserver without a pod restart.
#
# Asserts the file-mount wiring works in a real cluster:
#   1. Read current token from the Secret backing the apiserver volume mount.
#   2. Mint a Bearer request to /api/v1/runs → expect 2xx or 4xx (NOT 401).
#   3. Write a new token to the Secret via kubectl.
#   4. Wait for the projected file to be updated by kubelet.
#   5. Mint the SAME request with the NEW token → expect 2xx or 4xx.
#   6. Mint with the OLD token → expect 401.
#   7. Repeat steps 3-6 to confirm a second rotation also works without
#      any `kubectl rollout restart`.
#   8. Verify the apiserver pod's startTime did not change across all
#      rotations.

set -euo pipefail

APISERVER_NAMESPACE="${SYMPOZIUM_NAMESPACE:-sympozium-system}"
# shellcheck source=lib/resolve-token.sh
source "$(dirname "${BASH_SOURCE[0]}")/lib/resolve-token.sh"

APISERVER_URL="${APISERVER_URL:-http://127.0.0.1:19090}"
PORT_FORWARD_LOCAL_PORT="${APISERVER_PORT:-19090}"
SKIP_PORT_FORWARD="${SKIP_PORT_FORWARD:-0}"
TEST_NAMESPACE="${TEST_NAMESPACE:-default}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; EXIT_CODE=1; }
info() { echo -e "${YELLOW}● $*${NC}"; }

EXIT_CODE=0
PF_PID=""

cleanup() {
  if [[ -n "${PF_PID}" ]] && kill -0 "${PF_PID}" >/dev/null 2>&1; then
    kill "${PF_PID}" >/dev/null 2>&1 || true
    wait "${PF_PID}" >/dev/null 2>&1 || true
  fi
  if [[ -n "${ORIGINAL_TOKEN_B64:-}" && -n "${SECRET_NAME:-}" ]]; then
    # Restore the original token bytes (best effort).
    kubectl patch secret "${SECRET_NAME}" -n "${APISERVER_NAMESPACE}" \
      --type=merge \
      -p "{\"data\":{\"token\":\"${ORIGINAL_TOKEN_B64}\"}}" >/dev/null 2>&1 || true
  fi
  if command -v pkill >/dev/null 2>&1; then
    pkill -f "kubectl port-forward -n ${APISERVER_NAMESPACE} svc/sympozium-apiserver ${PORT_FORWARD_LOCAL_PORT}:8080" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

require_cmd() {
  if ! command -v "$1" >/dev/null 2>&1; then
    fail "Required command not found: $1"
    exit 1
  fi
}

start_port_forward() {
  if [[ "${SKIP_PORT_FORWARD}" == "1" ]]; then
    return 0
  fi
  if curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1; then
    return 0
  fi
  info "Starting port-forward to sympozium-apiserver on :${PORT_FORWARD_LOCAL_PORT}"
  kubectl port-forward -n "${APISERVER_NAMESPACE}" svc/sympozium-apiserver \
    "${PORT_FORWARD_LOCAL_PORT}:8080" >/tmp/sympozium-token-reload-pf.log 2>&1 &
  PF_PID=$!
  for _ in $(seq 1 30); do
    if ! kill -0 "$PF_PID" >/dev/null 2>&1; then
      fail "Port-forward exited early"
      cat /tmp/sympozium-token-reload-pf.log || true
      exit 1
    fi
    if curl -fsS "${APISERVER_URL}/healthz" >/dev/null 2>&1; then
      pass "Port-forward ready"
      return 0
    fi
    sleep 1
  done
  fail "Timed out waiting for API server via port-forward"
  cat /tmp/sympozium-token-reload-pf.log || true
  exit 1
}

# Read token bytes from the Secret that the apiserver volume mounts.
read_token_bytes() {
  if [[ -z "${SECRET_NAME:-}" ]]; then
    SECRET_NAME="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver \
      -o jsonpath='{.spec.template.spec.volumes[?(@.name=="sympozium-ui-token")].secret.secretName}' 2>/dev/null || true)"
  fi
  if [[ -z "${SECRET_NAME}" ]]; then
    fail "Could not determine the sympozium-ui-token Secret from the deployment"
    exit 1
  fi
  kubectl get secret -n "${APISERVER_NAMESPACE}" "${SECRET_NAME}" \
    -o jsonpath='{.data.token}' 2>/dev/null | base64 -d 2>/dev/null
}

# write_token_bytes updates the Secret with a new random token. The encoded
# base64 is required because the Secret stores data as base64.
write_token_bytes() {
  local new_token="$1"
  local new_token_b64
  new_token_b64="$(printf "%s" "$new_token" | base64 | tr -d '\n')"
  kubectl patch secret "${SECRET_NAME}" -n "${APISERVER_NAMESPACE}" \
    --type=merge \
    -p "{\"data\":{\"token\":\"${new_token_b64}\"}}" >/dev/null
}

# wait_for_token waits for kubelet to project the new token bytes to the
# apiserver pod, then for the apiserver to accept the new token. We can't
# read the file directly because the distroless image has no shell, so we
# poll the apiserver's /api/v1/runs endpoint with the new bearer token and
# break early once it returns anything other than 401.
#
# Args: $1 = expected token (plaintext)
wait_for_token() {
  local expected="$1"
  # On the velatir-agents-dev cluster the projected Secret volume
  # propagation latency has been observed at 60-90s (much higher than the
  # upstream 1-2s default). We use a 120s budget to keep the test stable;
  # the early-break on a 200/400 response keeps the common case fast.
  local timeout=120 elapsed=0
  apiserver_pod_name="$(kubectl get pod -n "${APISERVER_NAMESPACE}" \
    -l app.kubernetes.io/component=apiserver \
    -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  if [[ -z "$apiserver_pod_name" ]]; then
    fail "Could not find apiserver pod"
    exit 1
  fi
  local secret_data
  secret_data="$(kubectl get secret -n "${APISERVER_NAMESPACE}" "${SECRET_NAME}" \
    -o jsonpath='{.data.token}' 2>/dev/null || true)"
  local decoded
  decoded="$(printf "%s" "$secret_data" | base64 -d 2>/dev/null || true)"
  if [[ "$decoded" != "$expected" ]]; then
    fail "Secret data does not match expected token (got length ${#decoded}, want length ${#expected})"
    return 1
  fi
  info "Waiting up to ${timeout}s for kubelet to project + apiserver to accept the new token..."
  while [[ "$elapsed" -lt "$timeout" ]]; do
    local code
    code="$(request_health "$expected")"
    if [[ "$code" != "401" ]]; then
      return 0
    fi
    sleep 2
    elapsed=$((elapsed + 2))
  done
  return 1
}

# apiserver_pod_start returns the StartTime of the apiserver pod.
apiserver_pod_start() {
  kubectl get pod -n "${APISERVER_NAMESPACE}" \
    -l app.kubernetes.io/component=apiserver \
    -o jsonpath='{.items[0].status.startTime}' 2>/dev/null || true
}

# request_health does a GET /api/v1/runs with the supplied bearer token
# (or no token if empty). Returns the HTTP code.
request_health() {
  local token="$1"
  local url="${APISERVER_URL}/api/v1/runs?namespace=${TEST_NAMESPACE}"
  local -a headers=()
  if [[ -n "$token" ]]; then
    headers=(-H "Authorization: Bearer ${token}")
  fi
  curl -sS -o /dev/null -w "%{http_code}" "${headers[@]}" "$url"
}

# assert_authorized passes when the response is NOT 401. The token rotation
# test only cares that the token was accepted; the exact response body is
# irrelevant (the apiserver may return 200 with an empty list, 400 because
# of a missing agentRef, etc.).
assert_authorized() {
  local actual="$1" label="$2"
  if [[ "$actual" == "401" ]]; then
    fail "${label}: got 401 Unauthorized (token not accepted)"
    return 1
  fi
  if [[ "$actual" -lt 200 || "$actual" -ge 500 ]]; then
    fail "${label}: got HTTP ${actual} (server error or invalid response)"
    return 1
  fi
  pass "${label}: HTTP ${actual} (authorized)"
  return 0
}

rotate_and_assert() {
  local round="$1"
  local initial_token="$2" old_token="$3" new_token="$4"

  info "Round ${round}: writing new token to Secret"
  write_token_bytes "$new_token"
  if ! wait_for_token "$new_token"; then
    fail "Round ${round}: apiserver did not accept the new token within 120s"
    return 1
  fi
  pass "Round ${round}: projected file updated to new token"

  # 5. New token must authorize.
  local code
  code="$(request_health "$new_token")"
  assert_authorized "$code" "Round ${round}: new token" || return 1

  # 6. Old token must be rejected.
  code="$(request_health "$old_token")"
  if [[ "$code" != "401" ]]; then
    fail "Round ${round}: old token got HTTP ${code}, want 401"
    return 1
  fi
  pass "Round ${round}: old token correctly rejected (401)"

  # Capture the new "current" for the next round.
  eval "$5=\$new_token"
  return 0
}

main() {
  require_cmd kubectl
  require_cmd curl
  require_cmd base64
  require_cmd python3

  info "Running token-reload integration test in namespace '${APISERVER_NAMESPACE}'"

  if ! kubectl get crd agents.sympozium.ai >/dev/null 2>&1; then
    fail "Sympozium CRDs not installed"
    exit 1
  fi

  if ! kubectl get svc -n "${APISERVER_NAMESPACE}" sympozium-apiserver >/dev/null 2>&1; then
    fail "sympozium-apiserver service not found in namespace '${APISERVER_NAMESPACE}'"
    exit 1
  fi

  start_port_forward

  # Capture the original Secret value so we can restore on exit.
  SECRET_NAME="$(kubectl get deploy -n "${APISERVER_NAMESPACE}" sympozium-apiserver \
    -o jsonpath='{.spec.template.spec.volumes[?(@.name=="sympozium-ui-token")].secret.secretName}' 2>/dev/null || true)"
  if [[ -z "${SECRET_NAME}" ]]; then
    fail "Deployment does not mount a sympozium-ui-token volume — chart not yet updated"
    exit 1
  fi
  ORIGINAL_TOKEN_B64="$(kubectl get secret -n "${APISERVER_NAMESPACE}" "${SECRET_NAME}" \
    -o jsonpath='{.data.token}' 2>/dev/null || true)"

  local initial_token current_token old_token new_token pod_start_before
  initial_token="$(read_token_bytes)"
  if [[ -z "$initial_token" ]]; then
    fail "Initial token is empty"
    exit 1
  fi
  pass "Initial token resolved (length ${#initial_token})"

  # 2. Initial request must authorize.
  local code
  code="$(request_health "$initial_token")"
  assert_authorized "$code" "Initial request" || exit 1

  pod_start_before="$(apiserver_pod_start)"
  info "Apiserver pod StartTime before rotations: ${pod_start_before}"

  current_token="$initial_token"

  # 3-6. Round 1.
  old_token="$current_token"
  new_token="reload-test-$(date +%s)-1"
  if ! rotate_and_assert 1 "$initial_token" "$old_token" "$new_token" current_token; then
    exit 1
  fi

  # 3-6. Round 2 (verify multiple rotations work).
  old_token="$current_token"
  new_token="reload-test-$(date +%s)-2"
  if ! rotate_and_assert 2 "$initial_token" "$old_token" "$new_token" current_token; then
    exit 1
  fi

  # 8. Confirm the apiserver pod was NOT restarted across the two rotations.
  local pod_start_after
  pod_start_after="$(apiserver_pod_start)"
  info "Apiserver pod StartTime after rotations:  ${pod_start_after}"
  if [[ "$pod_start_before" != "$pod_start_after" ]]; then
    fail "Apiserver pod was restarted during the test (startTime changed)"
    exit 1
  fi
  pass "Apiserver pod was not restarted across two rotations"

  echo
  if [[ "$EXIT_CODE" -eq 0 ]]; then
    pass "Token reload integration suite passed"
  else
    fail "Token reload integration suite failed"
  fi
  exit "$EXIT_CODE"
}

main "$@"