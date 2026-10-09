#!/bin/sh
set -eu

client_built=
shard=
for argument; do
    case "$argument" in
        --client-built) client_built=$argument ;;
        daemon|packages|source) shard=$argument ;;
        *) echo "usage: scripts/go-ci-owned.sh [--client-built] [daemon|packages|source]" >&2; exit 2 ;;
    esac
done
# With no shard every stage runs; CI runs each shard on its own Mac.
in_shard() { [ -z "$shard" ] || [ "$shard" = "$1" ]; }
script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
CDPATH= cd -- "$repository_root"
. "$script_dir/local-ci-environment.sh"
. "$script_dir/go-gate-environment.sh"

go_gate_supervisor_pid=
go_gate_signal() {
    signal=$1
    trap - EXIT HUP INT TERM
    go_gate_join_supervisor
    exit $((128 + signal))
}
trap 'go_gate_signal 1' HUP
trap 'go_gate_signal 2' INT
trap 'go_gate_signal 15' TERM

export GOTOOLCHAIN=local
go=${DF_CI_GO-}
[ -n "$go" ] || {
    echo "go-ci: Go is unavailable; add Go's bin directory to factoryd --tool-path (and its install root to --toolchain-read-roots)" >&2
    exit 1
}
# These three packages own process boundaries that have demonstrated cross-
# package scheduling sensitivity. Keep each causal stage uncached and isolated;
# every other package is discovered below so a new package cannot silently skip
# tests. The five ordinary packages and internal/e2e have their own gates.
if in_shard source; then
    echo "go-ci: Git boundary resource census"
    go_gate_stage 1200 "$go" test -short -timeout=20m -count=1 ./internal/change

    echo "go-ci: Change worker process tests"
    go_gate_stage 1200 "$go" test -short -timeout=20m -count=1 ./internal/changeworker
fi

if in_shard daemon; then
    echo "go-ci: daemon process tests"
    go_gate_stage 1200 "$go" test -short -timeout=20m -count=1 ./internal/daemon
fi

if in_shard packages; then
echo "go-ci: process-sensitive Go tests"
set --
packages=$("$go" list ./...)
for package in $packages; do
    case "$package" in
        github.com/dark-factory-build/dark-factory/internal/browserprotocol|\
        github.com/dark-factory-build/dark-factory/internal/provider|\
        github.com/dark-factory-build/dark-factory/internal/opgraph|\
        github.com/dark-factory-build/dark-factory/internal/change|\
        github.com/dark-factory-build/dark-factory/internal/changeworker|\
        github.com/dark-factory-build/dark-factory/internal/daemon|\
        github.com/dark-factory-build/dark-factory/internal/e2e)
            ;;
        *) set -- "$@" "$package" ;;
    esac
done
if [ "$#" -gt 0 ]; then
    go_gate_stage 1200 "$go" test -short -timeout=20m -count=1 "$@"
fi
fi

if in_shard source; then
    echo "go-ci: browser, daemon and runner E2E"
    go_gate_stage 1500 "$script_dir/go-e2e.sh" all ${client_built:+"$client_built"}
fi
echo "go-ci: PASS"
