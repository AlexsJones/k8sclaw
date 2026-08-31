#!/bin/sh
# A deterministic v1alpha1 adapter-contract fixture. This is intentionally not
# an upstream harness integration: it exists to prove the container boundary.
set -eu

result_path="${SYMPOZIUM_RESULT_PATH:-/ipc/output/result.json}"
contract="${SYMPOZIUM_HARNESS_CONTRACT_VERSION:-}"

emit() {
  status="$1"
  case "$status" in
    success) payload='{"status":"success","response":"reference adapter completed"}' ;;
    *) payload='{"status":"error","error":"reference adapter preflight failed"}' ;;
  esac
  mkdir -p "$(dirname "$result_path")"
  printf '%s' "$payload" > "$result_path"
  printf '__SYMPOZIUM_RESULT__\n%s\n__SYMPOZIUM_END__\n' "$payload"
}

if [ "$contract" != "v1alpha1" ]; then
  emit error
  exit 1
fi
if ! touch "$HOME/.sympozium-reference-adapter"; then
  emit error
  exit 1
fi
if [ -z "${TASK:-}" ]; then
  emit error
  exit 1
fi

# Keep output fixed and JSON-safe; the conformance test verifies the task
# separately through the container environment.
emit success
