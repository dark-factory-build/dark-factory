#!/bin/sh
set -eu

[ "$#" -eq 0 ] || { echo "usage: go-check.sh" >&2; exit 64; }
script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd -P)
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
    go_gate_environment_cleanup || true
    exit $((128 + go_gate_signal_number))
}
trap go_gate_cleanup EXIT
trap 'go_gate_signal 1' HUP
trap 'go_gate_signal 2' INT
trap 'go_gate_signal 15' TERM

go_gate_fast_stage || exit $?
echo "go-check: PASS"
