#!/bin/sh
set -eu

[ "$#" -eq 0 ] || {
    echo "usage: go-ci.sh" >&2
    exit 64
}

script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
CDPATH= cd -- "$repository_root"

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
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
    go test -timeout=20m -count=1 -p 1 ./...

echo "go-ci: serial full Go race tests"
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off \
    go test -race -timeout=30m -count=1 -p 1 ./...

echo "go-ci: TypeScript client proof"
(
    cd "$repository_root/web"
    COREPACK_ENABLE_NETWORK=0 corepack pnpm --offline run typecheck
    COREPACK_ENABLE_NETWORK=0 corepack pnpm --offline run test
)

echo "go-ci: git diff --check"
git diff --check
echo "go-ci: PASS"
