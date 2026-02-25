# Writing Integration Tests

Integration tests verify that KubeClaw tools work end-to-end in a real cluster.
Unlike unit tests, they exercise the full pipeline: controller → Job → agent-runner → LLM → tool invocation.

## Prerequisites

- A Kind (or other) cluster with KubeClaw installed (`make install && kubectl apply -k config/`)
- An OpenAI API key (or other supported provider)
- `kubectl` configured to talk to the cluster

## Running Existing Tests

```bash
# Run all integration tests
make test-integration

# Run with a specific model
TEST_MODEL=gpt-5.2 ./test/integration/test-write-file.sh

# Override timeout (seconds)
TEST_TIMEOUT=180 ./test/integration/test-write-file.sh

# Use a pre-existing secret instead of OPENAI_API_KEY env var
kubectl create secret generic inttest-openai-key --from-literal=OPENAI_API_KEY=sk-...
./test/integration/test-write-file.sh
```

## How Integration Tests Work

Each test follows the same pattern:

1. **Create resources** — a test `ClawInstance` and `AgentRun` with a deterministic task
2. **Wait for completion** — poll `status.phase` until `Succeeded` or `Failed`
3. **Validate results** — check pod logs, `status.result`, or the pod filesystem
4. **Clean up** — delete all test resources

The LLM is the real LLM, not a mock. The task prompt is carefully worded to produce
a deterministic, verifiable outcome (e.g., "write exactly this string to this file").

## Writing a New Test

### 1. Create the script

Place your test in `test/integration/`:

```bash
touch test/integration/test-my-tool.sh
chmod +x test/integration/test-my-tool.sh
```

### 2. Use the standard template

```bash
#!/usr/bin/env bash
set -euo pipefail

# --- Configuration ---
NAMESPACE="${TEST_NAMESPACE:-default}"
INSTANCE_NAME="inttest-my-tool"
RUN_NAME="inttest-my-tool-run"
SECRET_NAME="inttest-openai-key"
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

cleanup() {
    info "Cleaning up..."
    kubectl delete agentrun "$RUN_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete clawinstance "$INSTANCE_NAME" -n "$NAMESPACE" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete jobs -n "$NAMESPACE" -l "kubeclaw.io/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
    kubectl delete pods -n "$NAMESPACE" -l "kubeclaw.io/agentrun=$RUN_NAME" --ignore-not-found >/dev/null 2>&1 || true
}

# Ensure secret exists
if ! kubectl get secret "$SECRET_NAME" -n "$NAMESPACE" >/dev/null 2>&1; then
    if [[ -z "${OPENAI_API_KEY:-}" ]]; then
        fail "Set OPENAI_API_KEY or create secret '$SECRET_NAME'"
        exit 1
    fi
    kubectl create secret generic "$SECRET_NAME" \
        --from-literal=OPENAI_API_KEY="$OPENAI_API_KEY" -n "$NAMESPACE"
fi

cleanup 2>/dev/null || true
sleep 2

# --- Create ClawInstance ---
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
EOF

# --- Create AgentRun ---
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
  sessionKey: "inttest-$(date +%s)"
  task: |
    YOUR DETERMINISTIC TASK PROMPT HERE.
  model:
    provider: openai
    model: ${MODEL}
    authSecretRef: ${SECRET_NAME}
  timeout: "3m"
EOF

# --- Wait for completion ---
elapsed=0
phase=""
pod=""
while [[ $elapsed -lt $TIMEOUT ]]; do
    phase=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" \
        -o jsonpath='{.status.phase}' 2>/dev/null || echo "")
    # Capture pod name early, before cleanup removes it
    if [[ -z "$pod" ]]; then
        pod=$(kubectl get pods -n "$NAMESPACE" \
            -l "kubeclaw.io/agentrun=$RUN_NAME" \
            -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    fi
    [[ "$phase" == "Succeeded" || "$phase" == "Failed" ]] && break
    sleep 5
    elapsed=$((elapsed + 5))
done

[[ "$phase" == "Succeeded" ]] && pass "AgentRun succeeded" || { fail "AgentRun phase: $phase"; cleanup; exit 1; }

# --- Validate ---
result=$(kubectl get agentrun "$RUN_NAME" -n "$NAMESPACE" \
    -o jsonpath='{.status.result}' 2>/dev/null || echo "")

# YOUR VALIDATION LOGIC HERE
# Example:
# if echo "$result" | grep -qi "expected_string"; then
#     pass "Result contains expected output"
# else
#     fail "Result missing expected output"
#     failures=$((failures + 1))
# fi

# --- Cleanup & exit ---
cleanup
if [[ $failures -gt 0 ]]; then
    fail "$failures check(s) failed"
    exit 1
fi
pass "Test complete"
```

### 3. Add to the Makefile (optional)

If you want it to run as part of `make test-integration`, it already runs all scripts
in `test/integration/`. Or add a specific target:

```makefile
test-integration-my-tool: ## Run my-tool integration test
	./test/integration/test-my-tool.sh
```

## Writing Good Task Prompts

The task prompt is the most important part. Since you're asking a real LLM to do
something and then checking the result, the prompt must be:

- **Deterministic** — ask for a specific, verifiable output (exact string, exact filename)
- **Minimal** — one tool call, one action — don't test multiple things at once
- **Unambiguous** — tell the model exactly what to do, not what to figure out

### Good examples

```
Use the write_file tool to write the exact text "kubeclaw-integration-ok"
to /workspace/test-output.txt. Do not add any extra content.
```

```
Use the list_directory tool to list the contents of /workspace.
Report what you find.
```

```
Use the read_file tool to read /skills/k8s-ops.yaml and report
the first line of the file.
```

### Bad examples

```
Do some Kubernetes operations and tell me what you find.
```
This is non-deterministic — you can't verify the output.

```
Write a Python script that calculates fibonacci numbers.
```
This tests the LLM's coding ability, not the tool.

## Validation Strategies

| What to check | How | Reliability |
|---|---|---|
| `status.phase` | `kubectl get agentrun -o jsonpath='{.status.phase}'` | High — always works |
| `status.result` | Check for keywords in the LLM's response text | High — survives pod cleanup |
| Pod logs | `kubectl logs <pod> -c agent` | Medium — pod may be gone |
| File on disk | `kubectl exec <pod> -c agent -- cat /path` | Low — pod must still be running |

**Tip:** Capture the pod name during the polling loop (while the run is `Running`),
not after it completes. The Job/pod is cleaned up quickly after the agent finishes.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `TEST_MODEL` | `gpt-4o-mini` | LLM model to use |
| `TEST_TIMEOUT` | `120` | Max seconds to wait for completion |
| `TEST_NAMESPACE` | `default` | Kubernetes namespace |
| `OPENAI_API_KEY` | _(none)_ | Used to create the secret if it doesn't exist |

## Existing Tests

| Test | File | What it verifies |
|---|---|---|
| write_file | `test/integration/test-write-file.sh` | Agent uses `write_file` tool to create a file with specific content |
| k8s-ops nodes | `test/integration/test-k8s-ops-nodes.sh` | Agent uses `execute_command` with k8s-ops skill to run `kubectl get nodes` |
| telegram | `test/integration/test-telegram-channel.sh` | Channel deployment pipeline + optional full E2E with real bot |
