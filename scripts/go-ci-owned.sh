#!/bin/sh
set -eu

[ "$#" -eq 0 ] || { echo "usage: scripts/go-ci-owned.sh" >&2; exit 2; }
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
CDPATH= cd -- "$repository_root"
. "$script_dir/local-ci-environment.sh"
. "$script_dir/go-gate-environment.sh"

go_gate_supervisor_pid=
go_gate_signal() {
    signal=$1
    trap - EXIT HUP INT TERM
    if [ -n "$go_gate_supervisor_pid" ]; then
        kill -TERM "$go_gate_supervisor_pid" 2>/dev/null || true
        wait "$go_gate_supervisor_pid" 2>/dev/null || true
    fi
    exit $((128 + signal))
}
trap 'go_gate_signal 1' HUP
trap 'go_gate_signal 2' INT
trap 'go_gate_signal 15' TERM

export GOTOOLCHAIN=local
echo "go-ci: process-sensitive Go tests"
go_gate_stage 1200 go test -short -timeout=20m -count=1 \
    ./cmd/factory-runner \
    ./cmd/factoryctl \
    ./cmd/factoryd \
    ./internal/api \
    ./internal/browser \
    ./internal/change \
    ./internal/changeworker \
    ./internal/daemon \
    ./internal/install \
    ./internal/processcontract \
    ./internal/runner

echo "go-ci: browser terminal and PTY E2E"
go_gate_stage 600 "$script_dir/go-browser-e2e.sh"

echo "go-ci: daemon and runner lifecycle E2E"
go_gate_stage 900 "$script_dir/go-daemon-e2e.sh"
echo "go-ci: PASS"
