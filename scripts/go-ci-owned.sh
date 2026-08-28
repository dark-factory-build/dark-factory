#!/bin/sh
set -eu

go_gate_ci_web_proof() {
    echo "go-ci: TypeScript client proof"
    go_gate_web_test_stage
}

go_gate_ci_main() {
script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
CDPATH= cd -- "$repository_root"
. "$script_dir/local-ci-environment.sh"
. "$script_dir/go-gate-environment.sh"
. "$script_dir/go-fast-stage.sh"
if ! go_gate_environment_setup; then go_gate_environment_cleanup || true; exit 1; fi

go_gate_status=0
go_gate_cleanup() {
    go_gate_status=$?
    trap - EXIT HUP INT TERM
    go_gate_environment_cleanup || go_gate_status=1
    exit "$go_gate_status"
}
go_gate_signal() {
    go_gate_signal_number=$1
    trap - EXIT HUP INT TERM
    if [ -n "${go_gate_supervisor_pid-}" ]; then
        /bin/kill -TERM "$go_gate_supervisor_pid" 2>/dev/null || true
        wait "$go_gate_supervisor_pid" 2>/dev/null || true
        go_gate_supervisor_pid=
    fi
    go_gate_environment_cleanup || true
    exit $((128 + go_gate_signal_number))
}
trap go_gate_cleanup EXIT
trap 'go_gate_signal 1' HUP
trap 'go_gate_signal 2' INT
trap 'go_gate_signal 15' TERM

echo "go-ci: fast gate"
go_gate_fast_stage || exit $?
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
echo "go-ci: serial full Go tests"
go_gate_stage 1200 "$go_gate_go" test -timeout=20m -count=1 -p 1 ./...
echo "go-ci: serial full Go race tests"
go_gate_stage 1800 "$go_gate_go" test -race -timeout=30m -count=1 -p 1 ./...

go_gate_ci_web_proof || exit $?
echo "go-ci: serial real Go/TypeScript browser PTY E2E"
go_gate_stage 600 "$script_dir/go-browser-e2e.sh"
echo "go-ci: serial real Go/TypeScript browser PTY race E2E"
go_gate_stage 600 "$script_dir/go-browser-e2e.sh" --race
echo "go-ci: serial black-box daemon lifecycle E2E"
go_gate_stage 900 "$script_dir/go-daemon-e2e.sh"
echo "go-ci: git diff --check"
go_gate_stage 120 "$go_gate_git" diff --check
echo "go-ci: NOTE: final system census remains a cutover-only gate"
echo "go-ci: PASS"
}

case "$0" in
    */go-ci-owned.sh) go_gate_ci_main "$@" ;;
esac
