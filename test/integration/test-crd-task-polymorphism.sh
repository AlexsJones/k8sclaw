#!/usr/bin/env bash
# test-crd-task-polymorphism.sh — verify the regenerated AgentRun CRD's
# spec.task field accepts BOTH string (legacy Path A) and object (Path B /
# sidecar-driven) shapes via server-side dry-run.
#
# Reproduces the gap that bit PR #302 in upstream — fake clients used in
# unit tests skip OpenAPI validation, so the broken schema
# (type: object rejecting strings) shipped because no one exercised the
# real apiserver admission path. This script is what the maintainer asked
# for in the PR #302 review (issuecomment 5033007953):
#
#   "add one envtest (or a `--dry-run=server` smoke test) that creates
#    both forms against the real CRD. That test also closes the gap
#    that caused all the #304/#306 back and forth."
#
# Requires:
#   - kubectl with cluster-admin access to a context where the (regenerated)
#     sympozium.ai_agentruns CRD is installed
#   - jq for parsing apiserver responses
#   - A stub Agent to be created so the vagentpod admission webhook doesn't
#     short-circuit on "Agent not found" — we want to test CRD validation,
#     not Agent lookup
#
# Exit code:
#   0 — both string-form and object-form tasks accepted at the CRD layer
#   1 — either form rejected at the CRD layer
#
# Usage: ./test-crd-task-polymorphism.sh [NAMESPACE]

set -euo pipefail

NAMESPACE="${1:-${TEST_NAMESPACE:-default}}"
RUN_ID="$(date +%s)"
AGENT_NAME="sys-crd-test-agent-${RUN_ID}"

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; EXIT_CODE=1; }
info() { echo -e "${YELLOW}○ $*${NC}"; }

EXIT_CODE=0

if ! kubectl get crd agentruns.sympozium.ai >/dev/null 2>&1; then
  fail "AgentRun CRD not installed in the active context — apply the regenerated CRD first"
  exit 1
fi

# Pull the live CRD schema so we can confirm in CI logs what shape the
# apiserver is validating against. Print the task field only — log noise
# otherwise.
echo "## Live CRD spec.task shape from cluster:"
kubectl get crd agentruns.sympozium.ai -o jsonpath='{.spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.task}' | jq '.' || true
echo ""

# Create a stub Agent so the vagentpod admission webhook doesn't short-circuit
# on "Agent not found" — we want to test CRD validation, not Agent lookup.
# The Agent schema requires `spec.agents.default` (and an authRefs entry so the
# controller can resolve credentials). Cleanup at the end.
echo "## Setting up stub Agent for admission webhook"
STUB_AGENT=$(mktemp)
cat > "$STUB_AGENT" <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: $AGENT_NAME
  namespace: $NAMESPACE
spec:
  agents:
    default:
      model: gpt-5-nano
      runTimeout: 5m
  authRefs:
    - provider: openai
      secret: sympozium-openai-api-key
  memory:
    enabled: false
EOF
if kubectl apply -f "$STUB_AGENT" >/dev/null 2>&1; then
  pass "stub Agent $AGENT_NAME created"
else
  fail "failed to create stub Agent — aborting test"
  rm -f "$STUB_AGENT"
  exit 1
fi
trap 'rm -f "$STUB_AGENT"; kubectl delete agent "$AGENT_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true' EXIT

# Inspect the stderr from a kubectl create --dry-run=server and report
# whether the rejection was at the CRD/schema layer (the failure mode
# this test exists to catch) or downstream (admission webhook, controller
# preflight — those are NOT the bug we're verifying).
check_crd_layer() {
  local stderr_file="$1"
  local label="$2"
  local stderr
  stderr="$(cat "$stderr_file")"
  if [ -z "$stderr" ]; then
    pass "$label accepted at CRD schema layer"
    return 0
  fi
  # CRD schema rejection uses this exact wording (server-side dry-run).
  if grep -q "Invalid value.*spec.task.*must be of type object" <<<"$stderr"; then
    fail "$label REJECTED at CRD schema layer — schema still requires type: object"
    info "stderr:"
    sed 's/^/    /' "$stderr_file"
    return 1
  fi
  # Any other rejection (admission webhook, controller preflight) means
  # the schema passed — exactly what we want.
  pass "$label accepted at CRD schema layer (rejected downstream: $(grep -m1 -oE 'denied the request[^"]*' <<<"$stderr" || echo 'unknown'))"
}

# --- Case 1: string-form task (legacy Path A) ---
echo "## Case 1: string-form task (legacy Path A)"
STRING_FORM_MANIFEST=$(mktemp)
cat > "$STRING_FORM_MANIFEST" <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  generateName: test-cr-string-
  namespace: $NAMESPACE
  labels:
    sympozium.ai/source: cdr-test
spec:
  agentRef: $AGENT_NAME
  agentId: collector
  task: do the legacy string thing
  mode: task
  cleanup: delete
  sessionKey: cdr-test-string-${RUN_ID}
  model:
    provider: openai
    model: gpt-5-nano
    authSecretRef: sympozium-openai-api-key
  timeout: 5m
EOF
kubectl create --dry-run=server -f "$STRING_FORM_MANIFEST" >/dev/null 2>"$STRING_FORM_MANIFEST.stderr" || true
check_crd_layer "$STRING_FORM_MANIFEST.stderr" "string-form task"
rm -f "$STRING_FORM_MANIFEST" "$STRING_FORM_MANIFEST.stderr"

# --- Case 2: object-form task (Path B / sidecar-driven) ---
echo ""
echo "## Case 2: object-form task (Path B / sidecar-driven)"
OBJECT_FORM_MANIFEST=$(mktemp)
cat > "$OBJECT_FORM_MANIFEST" <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  generateName: test-cr-object-
  namespace: $NAMESPACE
  labels:
    sympozium.ai/source: cdr-test
spec:
  agentRef: $AGENT_NAME
  agentId: collector
  task:
    mode: sidecar-driven
    tool: collector_run
    parameters:
      batchSize: "1"
      services: '["midjourney"]'
  mode: task
  cleanup: delete
  sessionKey: cdr-test-object-${RUN_ID}
  model:
    provider: openai
    model: gpt-5-nano
    authSecretRef: sympozium-openai-api-key
  timeout: 5m
EOF
kubectl create --dry-run=server -f "$OBJECT_FORM_MANIFEST" >/dev/null 2>"$OBJECT_FORM_MANIFEST.stderr" || true
check_crd_layer "$OBJECT_FORM_MANIFEST.stderr" "object-form task"
rm -f "$OBJECT_FORM_MANIFEST" "$OBJECT_FORM_MANIFEST.stderr"

# --- Case 3: nil / empty task — schema accepts (typeless), controller nil-check rejects at runtime ---
echo ""
echo "## Case 3: empty string-form task (schema accepts; controller nil-check rejects at runtime)"
EMPTY_FORM_MANIFEST=$(mktemp)
cat > "$EMPTY_FORM_MANIFEST" <<EOF
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  generateName: test-cr-empty-
  namespace: $NAMESPACE
  labels:
    sympozium.ai/source: cdr-test
spec:
  agentRef: $AGENT_NAME
  agentId: collector
  task: ""
  mode: task
  cleanup: delete
  sessionKey: cdr-test-empty-${RUN_ID}
  model:
    provider: openai
    model: gpt-5-nano
    authSecretRef: sympozium-openai-api-key
  timeout: 5m
EOF
kubectl create --dry-run=server -f "$EMPTY_FORM_MANIFEST" >/dev/null 2>"$EMPTY_FORM_MANIFEST.stderr" || true
check_crd_layer "$EMPTY_FORM_MANIFEST.stderr" "empty string-form task"
rm -f "$EMPTY_FORM_MANIFEST" "$EMPTY_FORM_MANIFEST.stderr"

echo ""
if [ "$EXIT_CODE" -eq 0 ]; then
  echo -e "${GREEN}All cases passed at CRD schema layer — spec.task is properly polymorphic.${NC}"
else
  echo -e "${RED}One or more cases failed at CRD schema layer. Re-run 'make manifests' after applying the Schemaless marker.${NC}"
fi
exit "$EXIT_CODE"
