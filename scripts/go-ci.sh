#!/bin/sh
set -eu

[ "$#" -eq 0 ] || { echo "usage: go-ci.sh" >&2; exit 64; }
script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
CDPATH= cd -- "$repository_root"

# The lease helper itself performs a small Git preflight before the gate root
# exists. Keep that one call independent of operator Git configuration.
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1
unset GIT_CONFIG_COUNT GIT_CONFIG_PARAMETERS GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0 \
    GIT_CONFIG_KEY_1 GIT_CONFIG_VALUE_1 GIT_CONFIG_KEY_2 GIT_CONFIG_VALUE_2
case "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" in
    1) ;;
    *) exec "$script_dir/with-local-ci-lease.sh" "$script_dir/go-ci.sh" "$@" ;;
esac

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

echo "go-ci: fast gate"
go_gate_fast_stage || exit $?
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
echo "go-ci: serial full Go tests"
go_gate_stage 1200 "$go_gate_go" test -timeout=20m -count=1 -p 1 ./...
echo "go-ci: serial full Go race tests"
go_gate_stage 1800 "$go_gate_go" test -race -timeout=30m -count=1 -p 1 ./...

echo "go-ci: TypeScript client proof"
(
    cd "$repository_root/web" || exit
    export COREPACK_ENABLE_NETWORK=0
    go_gate_stage 600 "$go_gate_corepack" pnpm --offline run typecheck || exit
    go_gate_stage 600 "$go_gate_corepack" pnpm --offline run test
)
echo "go-ci: git diff --check"
go_gate_stage 120 "$go_gate_git" diff --check
echo "go-ci: NOTE: final shell-provider/browser E2E and system census remain cutover-only gates"
echo "go-ci: PASS"
