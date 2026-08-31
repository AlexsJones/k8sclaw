#!/usr/bin/env bash
# Integration test: task mode `harness` (docs/modes/harness.md).
#
# The point of harness mode is that everything around the harness keeps
# working when the pod's primary process is someone else's binary. This test
# proves exactly that, end to end:
#
#   1. The agent container runs the harness image, not agent-runner
#   2. TASK reaches it, and $HOME is a writable emptyDir on a read-only rootfs
#   3. The pod security context is unchanged (non-root, readOnlyRootFilesystem,
#      drop: [ALL])
#   4. status.result is populated through the normal result contract
#   5. A postRun response gate still fires on that result and rewrites it
#   6. status.tokenUsage stays ABSENT — an external harness reports no tokens,
#      and absent-not-zero is the existing convention
#   7. An undeclared capability is rejected at admission
#   8. backend: celln, which never builds a pod, is rejected at admission
#
# It runs against a stand-in image implementing the adapter contract, so it
# needs no harness credentials and no network egress. Point it at a real
# adapter image with HARNESS_STANDIN_IMAGE (and give that image argv through
# the task's `args` if it needs any).
#
# Requires: Kind cluster with Sympozium deployed.

set -euo pipefail

NAMESPACE="${TEST_NAMESPACE:-default}"
TIMEOUT="${TEST_TIMEOUT:-240}"
INSTANCE_NAME="inttest-harness-$(date +%s)"
RUN_NAME="${INSTANCE_NAME}-run"
POLICY_NAME="${INSTANCE_NAME}-policy"
HARNESS_AUTH_SECRET="${HARNESS_AUTH_SECRET:-}"

# Sympozium builds no harness images, so the test supplies its own. The
# stand-in below is the smallest thing that honours the adapter contract, and
# it is digest-pinned because harness images must be (a mutable tag is
# rejected). Override with HARNESS_STANDIN_IMAGE to point at a real adapter.
STANDIN_IMAGE="${HARNESS_STANDIN_IMAGE:-alpine@sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e}"

# LM Studio via node-probe proxy — only used for the Agent's model config; the
# harness itself is what answers.
LM_STUDIO_BASE_URL="${LM_STUDIO_BASE_URL:-http://172.18.0.2:9473/proxy/lm-studio/v1}"
LM_STUDIO_MODEL="${LM_STUDIO_MODEL:-qwen/qwen3.5-9b}"

EXPECTED_ANSWER="harness answered"
GATE_MARKER="GATED-BY-INTEGRATION-TEST"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}PASS $*${NC}"; }
fail() { echo -e "${RED}FAIL $*${NC}"; FAILED=1; }
info() { echo -e "${YELLOW}---- $*${NC}"; }

FAILED=0

cleanup() {
  info "Cleaning up..."
  kubectl delete agentrun "$RUN_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete agentrun "${RUN_NAME}-capcheck" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete agentrun "${RUN_NAME}-cellncheck" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete agent "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete sympoziumpolicy "$POLICY_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete job -n "$NAMESPACE" -l "sympozium.ai/agent-run=${RUN_NAME}" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete pvc "${RUN_NAME}-workspace" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete role "sympozium-lifecycle-${RUN_NAME}" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
  kubectl delete rolebinding "sympozium-lifecycle-${RUN_NAME}" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ── Preflight ────────────────────────────────────────────────────────────────

if ! kubectl get crd agentruns.sympozium.ai >/dev/null 2>&1; then
  fail "AgentRun CRD not installed in the active context"
  exit 1
fi

info "Namespace '$NAMESPACE', harness image '${STANDIN_IMAGE}'"

# ── Explicitly opt in, then create the Agent ────────────────────────────────

cat <<EOF | kubectl apply -n "$NAMESPACE" -f -
apiVersion: sympozium.ai/v1alpha1
kind: SympoziumPolicy
metadata:
  name: ${POLICY_NAME}
spec:
  harnessPolicy:
    enabled: true
    allowUnmetered: true
---
apiVersion: sympozium.ai/v1alpha1
kind: Agent
metadata:
  name: ${INSTANCE_NAME}
spec:
  policyRef: ${POLICY_NAME}
  agents:
    default:
      model: ${LM_STUDIO_MODEL}
      baseURL: ${LM_STUDIO_BASE_URL}
EOF

# ── Build the task block ─────────────────────────────────────────────────────
#
# The stand-in is the smallest thing that honours the adapter contract: read
# TASK, prove HOME is writable, write /ipc/output/result.json, print the
# __SYMPOZIUM_RESULT__ marker. It arrives through parameters.args, which is
# how a plain alpine can stand in for an adapter image here.

STANDIN_SCRIPT='set -e
touch "$HOME/.writable" || { echo "HOME is not writable"; exit 1; }
mkdir -p /ipc/output
printf "%s" "{\"status\":\"success\",\"response\":\"$TASK\"}" > /ipc/output/result.json
printf "__SYMPOZIUM_RESULT__\n{\"status\":\"success\",\"response\":\"%s\"}\n__SYMPOZIUM_END__\n" "$TASK"'
# Compact the script into the JSON array string spec.task.parameters.args expects.
STANDIN_ARGS="$(jq -cn --arg s "$STANDIN_SCRIPT" '["/bin/sh","-c",$s]')"
TASK_BLOCK=$(cat <<EOF
  task:
    mode: harness
    parameters:
      prompt: "${EXPECTED_ANSWER}"
      image: ${STANDIN_IMAGE}
      args: '${STANDIN_ARGS}'
      capabilities: "persona"
EOF
)

# ── Create the AgentRun, with a postRun response gate ────────────────────────
#
# The gate rewrites the result. If it fires, the harness's answer went through
# the same contract every agent-runner run uses — which is the claim under
# test.

cat <<EOF | kubectl apply -n "$NAMESPACE" -f -
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${RUN_NAME}
spec:
  agentRef: ${INSTANCE_NAME}
  agentId: primary
  sessionKey: harness-test
${TASK_BLOCK}
  model:
    provider: openai-compatible
    model: ${LM_STUDIO_MODEL}
    baseURL: ${LM_STUDIO_BASE_URL}
    authSecretRef: "${HARNESS_AUTH_SECRET}"
  timeout: "4m"
  lifecycle:
    rbac:
      - apiGroups: ["sympozium.ai"]
        resources: ["agentruns"]
        verbs: ["get", "patch"]
    postRun:
      - name: response-gate
        gate: true
        image: soldevelo/kubectl:1.36
        command:
          - sh
          - -c
          - |
            echo "gate: AGENT_EXIT_CODE=\${AGENT_EXIT_CODE}"
            echo "gate: AGENT_RESULT=\${AGENT_RESULT}"
            # A constant, not the agent's output: AGENT_RESULT is
            # agent-controlled and would break the verdict JSON. The claim
            # under test is that the gate fired on a harness run at all.
            kubectl annotate agentrun ${RUN_NAME} -n ${NAMESPACE} --overwrite \
              'sympozium.ai/gate-verdict={"action":"rewrite","response":"${GATE_MARKER}","reason":"integration test"}'
EOF

info "AgentRun '${RUN_NAME}' created — polling..."

# ── Capture the pod spec while it exists ─────────────────────────────────────

POD_NAME=""
elapsed=0
while [[ $elapsed -lt $TIMEOUT ]]; do
  POD_NAME="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.podName}' 2>/dev/null || echo "")"
  [[ -n "$POD_NAME" ]] && break
  sleep 2
  elapsed=$((elapsed + 2))
done

if [[ -z "$POD_NAME" ]]; then
  fail "No pod was created within ${TIMEOUT}s"
  kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o yaml 2>/dev/null || true
  exit 1
fi

POD_JSON="$(kubectl get pod "$POD_NAME" -n "$NAMESPACE" -o json)"
AGENT_JSON="$(jq -r '.spec.containers[] | select(.name=="agent")' <<<"$POD_JSON")"

# 1. The agent container runs the harness image, not agent-runner.
AGENT_IMAGE="$(jq -r '.image' <<<"$AGENT_JSON")"
if [[ "$AGENT_IMAGE" == *"agent-runner"* ]]; then
  fail "agent container still runs agent-runner (${AGENT_IMAGE})"
else
  pass "agent container runs the harness image: ${AGENT_IMAGE}"
fi

# 2. TASK reaches the harness, and HOME points at the writable emptyDir.
TASK_ENV="$(jq -r '.env[] | select(.name=="TASK") | .value' <<<"$AGENT_JSON")"
if [[ -n "$TASK_ENV" ]]; then
  pass "TASK is set on the agent container (${TASK_ENV})"
else
  fail "TASK is empty on the agent container — the harness has no task"
fi

TASK_COUNT="$(jq -r '[.env[] | select(.name=="TASK")] | length' <<<"$AGENT_JSON")"
if [[ "$TASK_COUNT" == "1" ]]; then
  pass "TASK appears exactly once (the override replaced the central entry)"
else
  fail "TASK appears ${TASK_COUNT} times — the override appended instead of replacing"
fi

HOME_ENV="$(jq -r '.env[] | select(.name=="HOME") | .value' <<<"$AGENT_JSON")"
HOME_MOUNT="$(jq -r '[.volumeMounts[] | select(.name=="harness-home")] | length' <<<"$AGENT_JSON")"
if [[ "$HOME_ENV" == "/home/agent" && "$HOME_MOUNT" == "1" ]]; then
  pass "HOME is a mounted emptyDir at /home/agent"
else
  fail "HOME=${HOME_ENV}, harness-home mounts=${HOME_MOUNT} (want /home/agent and 1)"
fi

HOME_VOL_TYPE="$(jq -r '.spec.volumes[] | select(.name=="harness-home") | keys[] | select(. != "name")' <<<"$POD_JSON")"
if [[ "$HOME_VOL_TYPE" == "emptyDir" ]]; then
  pass "harness-home is an emptyDir"
else
  fail "harness-home volume source is '${HOME_VOL_TYPE}' (want emptyDir)"
fi

# 3. The pod security context is untouched.
RO_ROOTFS="$(jq -r '.securityContext.readOnlyRootFilesystem' <<<"$AGENT_JSON")"
PRIV_ESC="$(jq -r '.securityContext.allowPrivilegeEscalation' <<<"$AGENT_JSON")"
CAP_DROP="$(jq -r '.securityContext.capabilities.drop | join(",")' <<<"$AGENT_JSON")"
if [[ "$RO_ROOTFS" == "true" && "$PRIV_ESC" == "false" && "$CAP_DROP" == "ALL" ]]; then
  pass "pod security context unchanged (readOnlyRootFilesystem, no privilege escalation, drop: [ALL])"
else
  fail "security context relaxed: readOnlyRootFilesystem=${RO_ROOTFS} allowPrivilegeEscalation=${PRIV_ESC} drop=${CAP_DROP}"
fi

# ── Poll to a terminal phase ─────────────────────────────────────────────────

elapsed=0
last_phase=""
while [[ $elapsed -lt $TIMEOUT ]]; do
  phase="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")"
  if [[ -n "$phase" && "$phase" != "$last_phase" ]]; then
    info "Phase: $phase (${elapsed}s)"
    last_phase="$phase"
  fi
  [[ "$phase" == "Succeeded" || "$phase" == "Failed" ]] && break
  sleep 3
  elapsed=$((elapsed + 3))
done

FINAL_PHASE="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.phase}' 2>/dev/null || echo "")"
if [[ "$FINAL_PHASE" == "Succeeded" ]]; then
  pass "AgentRun reached Succeeded"
else
  fail "AgentRun ended with phase '${FINAL_PHASE}' (expected Succeeded)"
  kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.error}' 2>/dev/null && echo
  kubectl logs "$POD_NAME" -n "$NAMESPACE" -c agent --tail=40 2>/dev/null || true
fi

# 4. status.result is populated through the normal contract.
RESULT="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.result}' 2>/dev/null || echo "")"
if [[ -n "$RESULT" ]]; then
  pass "status.result populated (${#RESULT} bytes)"
else
  fail "status.result is empty — the result contract did not carry the harness's answer"
  kubectl logs "$POD_NAME" -n "$NAMESPACE" -c agent --tail=40 2>/dev/null || true
fi

# 5. The gate still fired on that result.
#
# The durable proof is status.gateVerdict plus the rewritten result, asserted
# below. The transient PostRunning phase is deliberately not sampled: the
# controller can move Running -> PostRunning -> Succeeded inside a single poll
# interval, so watching for the intermediate phase is racy by construction.
GATE_VERDICT="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.gateVerdict}' 2>/dev/null || echo "")"
if [[ "$GATE_VERDICT" == "rewritten" ]]; then
  pass "status.gateVerdict = rewritten"
else
  fail "status.gateVerdict = '${GATE_VERDICT}' (expected rewritten)"
fi

if [[ "$RESULT" == "$GATE_MARKER" ]]; then
  pass "the gate rewrote the harness's result"
else
  fail "result was not rewritten by the gate: '${RESULT}'"
fi

# 6. Token usage stays absent rather than zero.
TOKEN_USAGE="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.tokenUsage}' 2>/dev/null || echo "")"
if [[ -z "$TOKEN_USAGE" ]]; then
  pass "status.tokenUsage is absent (an external harness reports no tokens; absent-not-zero)"
else
  fail "status.tokenUsage is present (${TOKEN_USAGE}); the adapter should omit metrics it cannot source"
fi

# 6b. The exact artifact that ran is recorded, not the mutable tag.
EXPECTED_DIGEST="${STANDIN_IMAGE##*@}"
ACTUAL_DIGEST="$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" -o jsonpath='{.status.harnessImageDigest}' 2>/dev/null || echo "")"
if [[ "$ACTUAL_DIGEST" == "$EXPECTED_DIGEST" ]]; then
  pass "status.harnessImageDigest records the exact artifact (${ACTUAL_DIGEST})"
else
  fail "status.harnessImageDigest = '${ACTUAL_DIGEST}' (expected '${EXPECTED_DIGEST}')"
fi

# ── 7. An undeclared capability is rejected at admission ─────────────────────
#
# The image below declares only `persona`, so a run that also sets
# spec.toolPolicy is asking for enforcement the adapter never claimed. It must
# be denied rather than admitted and silently degraded.

info "Checking that an undeclared capability is rejected at admission"
CAPCHECK_STDERR="$(mktemp)"
cat <<EOF | kubectl create --dry-run=server -n "$NAMESPACE" -f - >/dev/null 2>"$CAPCHECK_STDERR" || true
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${RUN_NAME}-capcheck
spec:
  agentRef: ${INSTANCE_NAME}
  agentId: primary
  sessionKey: harness-capcheck
  task:
    mode: harness
    parameters:
      prompt: "anything"
      image: ${STANDIN_IMAGE}
      capabilities: "persona"
  model:
    provider: openai-compatible
    model: ${LM_STUDIO_MODEL}
    baseURL: ${LM_STUDIO_BASE_URL}
    authSecretRef: ""
  toolPolicy:
    deny:
      - execute_command
  timeout: "1m"
EOF

if grep -q "toolFilter" "$CAPCHECK_STDERR"; then
  pass "admission denied the unsupported capability, naming it"
elif [[ -s "$CAPCHECK_STDERR" ]]; then
  fail "run was rejected, but not for the capability mismatch: $(head -c 300 "$CAPCHECK_STDERR")"
else
  fail "admission ACCEPTED a toolPolicy against an image that never declared toolFilter (is the webhook deployed?)"
fi
rm -f "$CAPCHECK_STDERR"

# ── 8. backend: celln is rejected at admission ───────────────────────────────
#
# backend: celln dispatches the task string to the celln router and never
# builds a pod, so there is no agent container for harness mode to replace.
# Admitting it would run the task with the harness image silently ignored.

info "Checking that mode: harness + backend: celln is rejected at admission"
CELLNCHECK_STDERR="$(mktemp)"
cat <<EOF | kubectl create --dry-run=server -n "$NAMESPACE" -f - >/dev/null 2>"$CELLNCHECK_STDERR" || true
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${RUN_NAME}-cellncheck
spec:
  agentRef: ${INSTANCE_NAME}
  agentId: primary
  sessionKey: harness-cellncheck
  backend: celln
  task:
    mode: harness
    parameters:
      prompt: "anything"
      image: ${STANDIN_IMAGE}
  model:
    provider: openai-compatible
    model: ${LM_STUDIO_MODEL}
    baseURL: ${LM_STUDIO_BASE_URL}
    authSecretRef: ""
  timeout: "1m"
EOF

if grep -q "celln" "$CELLNCHECK_STDERR"; then
  pass "admission denied the harness/celln combination, naming the backend"
elif [[ -s "$CELLNCHECK_STDERR" ]]; then
  fail "run was rejected, but not for the backend collision: $(head -c 300 "$CELLNCHECK_STDERR")"
else
  fail "admission ACCEPTED mode: harness on backend: celln; the harness image would be silently ignored"
fi
rm -f "$CELLNCHECK_STDERR"

# ── 9. A tag-only harness image is rejected at admission ──────────────────────
#
# The image becomes the pod's primary process, so a mutable tag is not an
# acceptable trust anchor. A tag-only reference must be denied before any pod
# exists, not silently pinned to whatever the tag resolves to today.

info "Checking that a tag-only harness image is rejected at admission"
TAGCHECK_STDERR="$(mktemp)"
cat <<EOF | kubectl create --dry-run=server -n "$NAMESPACE" -f - >/dev/null 2>"$TAGCHECK_STDERR" || true
apiVersion: sympozium.ai/v1alpha1
kind: AgentRun
metadata:
  name: ${RUN_NAME}-tagcheck
spec:
  agentRef: ${INSTANCE_NAME}
  agentId: primary
  sessionKey: harness-tagcheck
  task:
    mode: harness
    parameters:
      prompt: "anything"
      image: alpine:3.20
  model:
    provider: openai-compatible
    model: ${LM_STUDIO_MODEL}
    baseURL: ${LM_STUDIO_BASE_URL}
    authSecretRef: ""
  timeout: "1m"
EOF

if grep -q "digest-pinned" "$TAGCHECK_STDERR"; then
  pass "admission denied the tag-only image, naming the digest-pinning requirement"
elif [[ -s "$TAGCHECK_STDERR" ]]; then
  fail "run was rejected, but not for the digest-pinning requirement: $(head -c 300 "$TAGCHECK_STDERR")"
else
  fail "admission ACCEPTED a tag-only harness image; it must be rejected"
fi
rm -f "$TAGCHECK_STDERR"

# ── Summary ──────────────────────────────────────────────────────────────────

echo
if [[ $FAILED -eq 0 ]]; then
  echo -e "${GREEN}All harness-mode assertions passed.${NC}"
else
  echo -e "${RED}One or more harness-mode assertions failed.${NC}"
fi
exit "$FAILED"
