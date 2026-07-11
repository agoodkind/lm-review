#!/usr/bin/env bash

set -euo pipefail

PHASE=${1:-}
BINARY_PATH=${2:-}
LAUNCHD_LABEL=${3:-}
SYSTEMD_UNIT=${4:-}
MAX_ATTEMPTS=20
RETRY_DELAY_SECONDS=0.25

service_pid() {
    local line
    local output
    local platform

    platform=$(uname)
    if [[ "$platform" == "Darwin" ]]; then
        output=$(launchctl print "gui/$(id -u)/$LAUNCHD_LABEL" 2>/dev/null) || return 0
        while IFS= read -r line; do
            if [[ "$line" =~ ^[[:space:]]*pid[[:space:]]*=[[:space:]]*([0-9]+) ]]; then
                printf '%s\n' "${BASH_REMATCH[1]}"
                return 0
            fi
        done <<< "$output"
        return 0
    fi

    output=$(systemctl --user show "$SYSTEMD_UNIT" --property MainPID --value 2>/dev/null) || return 0
    if [[ "$output" =~ ^[1-9][0-9]*$ ]]; then
        printf '%s\n' "$output"
    fi
}

run_check() {
    local expected_pid=$1

    if [[ -n "$expected_pid" ]]; then
        "$BINARY_PATH" inference-service-check \
            --phase "$PHASE" \
            --expected-pid "$expected_pid"
        return
    fi
    "$BINARY_PATH" inference-service-check --phase "$PHASE"
}

run_post_restart_check() {
    local attempt_count
    local expected_pid

    for ((attempt_count = 1; attempt_count <= MAX_ATTEMPTS; attempt_count++)); do
        expected_pid=$(service_pid)
        if [[ -n "$expected_pid" ]]; then
            if run_check "$expected_pid" >/dev/null 2>&1; then
                printf 'inference service is healthy on supervised PID %s\n' "$expected_pid"
                return 0
            fi
        fi
        sleep "$RETRY_DELAY_SECONDS"
    done

    expected_pid=$(service_pid)
    if [[ -z "$expected_pid" ]]; then
        printf 'inference service manager did not report a running PID after restart\n' >&2
        return 1
    fi
    run_check "$expected_pid"
}

if [[ -z "$PHASE" || -z "$BINARY_PATH" || -z "$LAUNCHD_LABEL" || -z "$SYSTEMD_UNIT" ]]; then
    printf 'usage: %s <preflight|post-restart> <binary> <launchd-label> <systemd-unit>\n' "$0" >&2
    exit 2
fi

if [[ "$PHASE" == "preflight" ]]; then
    run_check "$(service_pid)"
    exit 0
fi

if [[ "$PHASE" != "post-restart" ]]; then
    printf 'unknown inference service check phase: %s\n' "$PHASE" >&2
    exit 2
fi

run_post_restart_check
