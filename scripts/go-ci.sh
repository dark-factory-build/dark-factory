#!/bin/sh
set -eu

[ "$#" -eq 0 ] || {
    echo "usage: go-ci.sh" >&2
    exit 64
}

script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
CDPATH= cd -- "$repository_root"

# The shared lease helper runs Git before the per-gate scratch environment
# exists. Disable all ambient Git configuration for that short preflight too.
export GIT_CONFIG_GLOBAL=/dev/null
export GIT_CONFIG_SYSTEM=/dev/null
export GIT_CONFIG_NOSYSTEM=1
unset GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS \
    GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0 GIT_CONFIG_KEY_1 GIT_CONFIG_VALUE_1 \
    GIT_CONFIG_KEY_2 GIT_CONFIG_VALUE_2 GIT_CONFIG_KEY_3 GIT_CONFIG_VALUE_3 \
    GIT_CONFIG_KEY_4 GIT_CONFIG_VALUE_4 GIT_CONFIG_KEY_5 GIT_CONFIG_VALUE_5 \
    GIT_CONFIG_KEY_6 GIT_CONFIG_VALUE_6 GIT_CONFIG_KEY_7 GIT_CONFIG_VALUE_7 \
    GIT_CONFIG_KEY_8 GIT_CONFIG_VALUE_8 GIT_CONFIG_KEY_9 GIT_CONFIG_VALUE_9

case "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" in
    1) ;;
    *) exec "$script_dir/with-local-ci-lease.sh" "$script_dir/go-ci.sh" "$@" ;;
esac

# shellcheck source=scripts/local-ci-environment.sh
. "$script_dir/local-ci-environment.sh"
# shellcheck source=scripts/go-gate-environment.sh
. "$script_dir/go-gate-environment.sh"
if ! go_gate_environment_setup; then
    go_gate_environment_cleanup || true
    exit 1
fi

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

echo "go-ci: fast gate"
# The environment marker makes go-check reuse this process's scratch root and
# dependency caches; it does not acquire another lease or clean the root.
"$script_dir/go-check.sh"

export GOTOOLCHAIN=local
export GOPROXY=off
export GOSUMDB=off
echo "go-ci: serial full Go tests"
go_gate_stage 1200 go test -timeout=20m -count=1 -p 1 ./...

echo "go-ci: serial full Go race tests"
go_gate_stage 1800 go test -race -timeout=30m -count=1 -p 1 ./...

echo "go-ci: serial process, Change and verification proofs"
# These packages contain the real-child, crash, Change-boundary, provider
# launch, and FD/process census tests. They own their independent fixture
# reapers; keep this pass explicit and serial instead of hiding it in ./....
go_gate_stage 1800 go test -timeout=20m -count=1 -p 1 \
    ./internal/processcontract ./internal/runner ./internal/changeworker \
    ./internal/daemon ./internal/change

echo "go-ci: TypeScript client proof"
(
    cd "$repository_root/web"
    export COREPACK_ENABLE_NETWORK=0
    go_gate_stage 600 corepack pnpm --offline run typecheck
    go_gate_stage 600 corepack pnpm --offline run test
)

echo "go-ci: git diff --check"
go_gate_stage 120 git diff --check
echo "go-ci: PASS"
