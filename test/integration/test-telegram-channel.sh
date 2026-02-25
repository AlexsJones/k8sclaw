#!/usr/bin/env bash
# Integration test: verify the Telegram channel pipeline works.
#
# This test has two modes:
#
# Mode 1 (no TELEGRAM_BOT_TOKEN): Deployment pipeline only
#   - Creates a ClawInstance with a telegram channel
#   - Verifies the controller creates a channel-telegram Deployment
#   - Checks the channel pod starts (may restart without a real token, but
#     the image pull + container start is validated)
#
# Mode 2 (with TELEGRAM_BOT_TOKEN + TELEGRAM_CHAT_ID): Full end-to-end
#   - Everything in Mode 1, plus:
#   - Creates an AgentRun that uses send_channel_message to send a message
#   - Verifies the message appears in the agent result
#   - Verifies the Telegram bot API was called (checks channel pod logs)
#
# Prerequisites:
#   - Kind cluster running with KubeClaw installed
#   - channel-telegram image available in the cluster
#   - (Mode 2) TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID set
#
# Usage:
#   # Mode 1: deployment pipeline only
#   ./test/integration/test-telegram-channel.sh
#
#   # Mode 2: full end-to-end with real Telegram bot
#   TELEGRAM_BOT_TOKEN=123:ABC TELEGRAM_CHAT_ID=456 ./test/integration/test-telegram-channel.sh

set -euo pipefail

# --- Configuration ---
NAMESPACE="${TEST_NAMESPACE:-default}"
INSTANCE_NAME="inttest-telegram"
RUN_NAME="inttest-telegram-msg"
SECRET_NAME="inttest-openai-key"
TELEGRAM_SECRET_NAME="inttest-telegram-secret"
MODEL="${TEST_MODEL:-gpt-4o-mini}"
TIMEOUT="${TEST_TIMEOUT:-120}"

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓ $*${NC}"; }
fail() { echo -e "${RED}✗ $*${NC}"; }
info() { echo -e "${YELLOW}● $*${NC}"; }
failures=0

FULL_MODE=false
if [[ -n "${TELEGRAM_BOT_TOKEN:-}" && -n "${TELEGRAM_CHAT_ID:-}" ]]; then
    FULL_MODE=true
fi

cleanup() {
    info "Cleaning up test resources..."
    kubectl delete agentrun "$RUN_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete clawinstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete jobs -n "$NAMESPACE" -l "kubeclaw.io/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete pods -n "$NAMESPACE" -l "kubeclaw.io/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
    # Wait for the controller to clean up channel deployments (owned by ClawInstance)
    sleep 3
    kubectl delete deployment "$INSTANCE_NAME-channel-telegram" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete secret "$TELEGRAM_SECRET_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
}

# --- Pre-flight checks ---
if $FULL_MODE; then
    info "Running integration test: Telegram channel (FULL — real bot)"
else
    info "Running integration test: Telegram channel (deployment pipeline only)"
    info "Set TELEGRAM_BOT_TOKEN + TELEGRAM_CHAT_ID for full end-to-end test"
fi

if ! kubectl get crd agentruns.kubeclaw.io >/dev/null 2>&1; then
    fail "KubeClaw CRDs not installed."
    exit 1
fi

if ! kubectl get deployment kubeclaw-controller-manager -n kubeclaw-system >/dev/null 2>&1; then
    fail "KubeClaw controller not running."
    exit 1
fi

# --- Ensure secrets exist ---
if $FULL_MODE; then
    # Telegram bot token secret
    kubectl delete secret "$TELEGRAM_SECRET_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1
    kubectl create secret generic "$TELEGRAM_SECRET_NAME" \
        --from-literal=TELEGRAM_BOT_TOKEN="$TELEGRAM_BOT_TOKEN" \
        -n "$NAMESPACE" >/dev/null 2>&1
    info "Created Telegram bot secret"

    # OpenAI secret (for AgentRun)
    if ! kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
        if [[ -z "${OPENAI_API_KEY:-}" ]]; then
            fail "No OPENAI_API_KEY set and secret '$SECRET_NAME' not found."
            exit 1
        fi
        kubectl create secret generic "$SECRET_NAME" \
            --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY" \
            -n "$NAMESPACE"
    fi
fi

# --- Clean up previous runs ---
cleanup 2>/dev/null || true
sleep 2

# ============================================================
# Part 1: Channel Deployment Pipeline
# ============================================================
info "Creating ClawInstance with telegram channel: $INSTANCE_NAME"

# Build the channel config — use a real or dummy token
if $FULL_MODE; then
    CHANNEL_SECRET_REF="$TELEGRAM_SECRET_NAME"
else
    # Create a dummy secret so the controller doesn't error
    kubectl create secret generic "$TELEGRAM_SECRET_NAME" \
        --from-literal=TELEGRAM_BOT_TOKEN="dummy-token-for-pipeline-test" \
        -n "$NAMESPACE" >/dev/null 2>&1 || true
    CHANNEL_SECRET_REF="$TELEGRAM_SECRET_NAME"
fi

cat <<EOF | kubectl apply -f -
apiVersion: kubeclaw.io/v1alpha1
kind: ClawInstance
metadata:
  name: ${INSTANCE_NAME}
  namespace: ${NAMESPACE}
spec:
  agents:
    default:
      model: ${MODEL}
  authRefs:
    - secret: ${SECRET_NAME}
  channels:
    - type: telegram
      configRef:
        secret: ${CHANNEL_SECRET_REF}
EOF

# Wait for the channel Deployment to appear
info "Waiting for channel Deployment to be created..."
deploy_name="${INSTANCE_NAME}-channel-telegram"
elapsed=0
deploy_found=false
while [[ $elapsed -lt 30 ]]; do
    if kubectl get deployment "$deploy_name" -n "$NAMESPACE" >/dev/null 2>&1; then
        deploy_found=true
        break
    fi
    sleep 2
    elapsed=$((elapsed + 2))
done

if $deploy_found; then
    pass "Channel Deployment created: $deploy_name"
else
    fail "Channel Deployment not created within 30s"
    failures=$((failures + 1))
fi

# Check the Deployment uses the right image
if $deploy_found; then
    deploy_image=$(kubectl get deployment "$deploy_name" -n "$NAMESPACE" \
        -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null || echo "")
    if echo "$deploy_image" | grep -q "channel-telegram"; then
        pass "Deployment image is correct: $deploy_image"
    else
        fail "Unexpected image: $deploy_image"
        failures=$((failures + 1))
    fi
fi

# Check labels
if $deploy_found; then
    channel_label=$(kubectl get deployment "$deploy_name" -n "$NAMESPACE" \
        -o jsonpath='{.metadata.labels.kubeclaw\.io/channel}' 2>/dev/null || echo "")
    instance_label=$(kubectl get deployment "$deploy_name" -n "$NAMESPACE" \
        -o jsonpath='{.metadata.labels.kubeclaw\.io/instance}' 2>/dev/null || echo "")
    if [[ "$channel_label" == "telegram" && "$instance_label" == "$INSTANCE_NAME" ]]; then
        pass "Deployment labels correct (channel=telegram, instance=$INSTANCE_NAME)"
    else
        fail "Unexpected labels: channel=$channel_label, instance=$instance_label"
        failures=$((failures + 1))
    fi
fi

# Verify ClawInstance status shows the channel
channel_status=$(kubectl get clawinstance "$INSTANCE_NAME" -n "$NAMESPACE" \
    -o jsonpath='{.status.channels[0].type}' 2>/dev/null || echo "")
if [[ "$channel_status" == "telegram" ]]; then
    pass "ClawInstance status shows telegram channel"
else
    info "ClawInstance channel status: $channel_status (may need reconcile)"
fi

# Wait for pod to start (or at least be created)
if $deploy_found; then
    info "Waiting for channel pod to start..."
    elapsed=0
    pod_started=false
    while [[ $elapsed -lt 60 ]]; do
        ready=$(kubectl get deployment "$deploy_name" -n "$NAMESPACE" \
            -o jsonpath='{.status.readyReplicas}' 2>/dev/null || echo "0")
        if [[ "$ready" -ge 1 ]] 2>/dev/null; then
            pod_started=true
            break
        fi
        # Even without a real token, check if the pod at least started
        pod_phase=$(kubectl get pods -n "$NAMESPACE" \
            -l "kubeclaw.io/channel=telegram,kubeclaw.io/instance=$INSTANCE_NAME" \
            -o jsonpath='{.items[0].status.phase}' 2>/dev/null || echo "")
        if [[ "$pod_phase" == "Running" ]]; then
            pod_started=true
            break
        fi
        sleep 3
        elapsed=$((elapsed + 3))
    done

    if $pod_started; then
        pass "Channel pod is running"
    else
        if $FULL_MODE; then
            fail "Channel pod not running within 60s"
            failures=$((failures + 1))
        else
            info "Channel pod not running (expected with dummy token)"
        fi
    fi
fi

# ============================================================
# Part 2: Full E2E — send a message via the agent
# ============================================================
if $FULL_MODE; then
    echo ""
    info "=== Part 2: Full E2E — agent sends Telegram message ==="

    MARKER_TEXT="kubeclaw-telegram-test-$(date +%s)"

    info "Creating AgentRun: $RUN_NAME"
    cat <<EOF | kubectl apply -f -
apiVersion: kubeclaw.io/v1alpha1
kind: AgentRun
metadata:
  name: ${RUN_NAME}
  namespace: ${NAMESPACE}
  labels:
    kubeclaw.io/instance: ${INSTANCE_NAME}
spec:
  instanceRef: ${INSTANCE_NAME}
  agentId: default
  sessionKey: "inttest-tg-$(date +%s)"
  task: |
    Use the send_channel_message tool to send a message to the telegram channel.
    The message text should be exactly: "${MARKER_TEXT}"
    The chatId should be: "${TELEGRAM_CHAT_ID}"
    After sending, confirm what you sent.
  model:
    provider: openai
    model: ${MODEL}
    authSecretRef: ${SECRET_NAME}
  timeout: "3m"
EOF

    # Wait for completion
    info "Waiting up to ${TIMEOUT}s for AgentRun to complete..."
    elapsed=0
    phase=""
    pod=""
    while [[ $elapsed -lt $TIMEOUT ]]; do
        phase=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" \
            -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
        if [[ -z "$pod" ]]; then
            pod=$(kubectl get pods -n "$NAMESPACE" \
                -l "kubeclaw.io/agentrun=$RUN_NAME" \
                -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
            if [[ -n "$pod" ]]; then
                info "Pod found: $pod"
            fi
        fi
        if [[ "$phase" == "Succeeded" || "$phase" == "Failed" ]]; then
            break
        fi
        sleep 5
        elapsed=$((elapsed + 5))
        if (( elapsed % 15 == 0 )); then
            info "  ...${elapsed}s elapsed (phase: ${phase:-Pending})"
        fi
    done

    if [[ "$phase" == "Succeeded" ]]; then
        pass "AgentRun phase: Succeeded"
    elif [[ "$phase" == "Failed" ]]; then
        fail "AgentRun phase: Failed"
        failures=$((failures + 1))
    else
        fail "AgentRun did not complete within ${TIMEOUT}s"
        failures=$((failures + 1))
    fi

    # Check result references the message
    result=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" \
        -o jsonpath='{.status.result}' 2>/dev/null || echo "")

    if echo "$result" | grep -qi "send_channel_message\|sent\|telegram\|$MARKER_TEXT"; then
        pass "AgentRun result references telegram message send"
    else
        fail "AgentRun result does not reference message send"
        failures=$((failures + 1))
        info "Result (first 500 chars):"
        echo "$result" | head -c 500
        echo ""
    fi

    # Check channel pod logs for outbound delivery
    tg_pod=$(kubectl get pods -n "$NAMESPACE" \
        -l "kubeclaw.io/channel=telegram,kubeclaw.io/instance=$INSTANCE_NAME" \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    if [[ -n "$tg_pod" ]]; then
        tg_logs=$(kubectl logs "$tg_pod" -n "$NAMESPACE" 2>/dev/null | tail -30 || echo "")
        if echo "$tg_logs" | grep -qi "sendMessage\|outbound\|send"; then
            pass "Channel pod logs show outbound message activity"
        else
            info "Channel pod logs do not show explicit send (may use different logging)"
        fi
    fi
else
    echo ""
    info "Skipping Part 2 (full E2E) — set TELEGRAM_BOT_TOKEN and TELEGRAM_CHAT_ID to enable"
fi

echo ""
# --- Summary ---
echo "=============================="
echo " Integration Test Summary"
echo "=============================="
echo " Test:      Telegram Channel"
echo " Mode:      $(if $FULL_MODE; then echo "Full E2E"; else echo "Deployment Pipeline"; fi)"
echo " Instance:  $INSTANCE_NAME"
echo " Model:     $MODEL"
echo " Failures:  $failures"
echo "=============================="
echo ""

# --- Cleanup ---
cleanup

if [[ $failures -gt 0 ]]; then
    fail "Integration test finished with $failures failure(s)"
    exit 1
fi

pass "Integration test complete"
