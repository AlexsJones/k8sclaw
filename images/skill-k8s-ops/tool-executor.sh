#!/bin/bash
# tool-executor.sh — Watches /ipc/tools/ for exec-request-*.json files,
# executes the requested commands, and writes exec-result-*.json responses.
# This script runs as the main process in skill sidecar containers.

set -euo pipefail

TOOLS_DIR="/ipc/tools"
POLL_INTERVAL=0.2  # seconds

mkdir -p "$TOOLS_DIR"

echo "[tool-executor] started, watching $TOOLS_DIR for exec requests"

process_request() {
    local req_file="$1"
    local basename
    basename=$(basename "$req_file")

    # Extract the request ID from the filename: exec-request-{id}.json
    local id
    id="${basename#exec-request-}"
    id="${id%.json}"

    local result_file="$TOOLS_DIR/exec-result-${id}.json"

    # Parse the JSON request using jq.
    local command args workdir timeout_sec
    command=$(jq -r '.command // ""' "$req_file" 2>/dev/null) || return
    args=$(jq -r '(.args // []) | join(" ")' "$req_file" 2>/dev/null) || return
    workdir=$(jq -r '.workDir // "/workspace"' "$req_file" 2>/dev/null) || return
    timeout_sec=$(jq -r '.timeout // 30' "$req_file" 2>/dev/null) || return

    # Sanitize timeout.
    if [[ "$timeout_sec" -lt 1 ]]; then timeout_sec=30; fi
    if [[ "$timeout_sec" -gt 120 ]]; then timeout_sec=120; fi

    # Build the full command. If args are provided, append them.
    local full_cmd="$command"
    if [[ -n "$args" ]]; then
        full_cmd="$command $args"
    fi

    echo "[tool-executor] exec [$id]: $full_cmd (timeout=${timeout_sec}s, workdir=${workdir})"

    # Execute the command, capturing stdout/stderr and exit code.
    local stdout="" stderr="" exit_code=0 timed_out="false"
    local tmp_stdout tmp_stderr
    tmp_stdout=$(mktemp)
    tmp_stderr=$(mktemp)

    cd "$workdir" 2>/dev/null || cd /

    if timeout "$timeout_sec" bash -c "$full_cmd" >"$tmp_stdout" 2>"$tmp_stderr"; then
        exit_code=0
    else
        exit_code=$?
        # timeout(1) returns 124 when the command times out.
        if [[ $exit_code -eq 124 ]]; then
            timed_out="true"
        fi
    fi

    stdout=$(cat "$tmp_stdout")
    stderr=$(cat "$tmp_stderr")
    rm -f "$tmp_stdout" "$tmp_stderr"

    # Truncate output if too large (50KB limit per field).
    if [[ ${#stdout} -gt 51200 ]]; then
        stdout="${stdout:0:51200}...(truncated)"
    fi
    if [[ ${#stderr} -gt 51200 ]]; then
        stderr="${stderr:0:51200}...(truncated)"
    fi

    # Write the result JSON. Use jq to properly escape strings.
    # Write to a temp file first then atomically rename so the agent
    # never reads a partially-written result.
    local tmp_result="${result_file}.tmp"
    jq -n \
        --arg id "$id" \
        --argjson exitCode "$exit_code" \
        --arg stdout "$stdout" \
        --arg stderr "$stderr" \
        --argjson timedOut "$timed_out" \
        '{id: $id, exitCode: $exitCode, stdout: $stdout, stderr: $stderr, timedOut: $timedOut}' \
        > "$tmp_result"
    mv "$tmp_result" "$result_file"

    echo "[tool-executor] done [$id]: exit=$exit_code timed_out=$timed_out"
}

# Main loop: poll for new request files.
while true; do
    # Exit when the agent signals it is done.
    if [[ -f /ipc/done ]]; then
        echo "[tool-executor] agent done, exiting"
        exit 0
    fi

    for req_file in "$TOOLS_DIR"/exec-request-*.json; do
        # Skip if no matches (glob didn't expand).
        [[ -e "$req_file" ]] || continue

        # Check if this request has already been processed.
        local_basename=$(basename "$req_file")
        local_id="${local_basename#exec-request-}"
        local_id="${local_id%.json}"
        result_file="$TOOLS_DIR/exec-result-${local_id}.json"

        if [[ -e "$result_file" ]]; then
            # Already processed, skip.
            continue
        fi

        process_request "$req_file" &
    done

    sleep "$POLL_INTERVAL"
done
