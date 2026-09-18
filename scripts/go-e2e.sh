#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
. "$script_dir/go-e2e-tools.sh"
mode=${1-all}
case "$mode" in
    browser|browser-race|daemon|all) ;;
    *) echo "usage: $0 [browser|browser-race|daemon|all] [--client-built]" >&2; exit 2 ;;
esac
[ "$#" -le 2 ] && { [ "$#" -lt 2 ] || [ "$2" = --client-built ]; } || exit 2
client_built=${2-}
e2e_root=$(go_e2e_temporary_directory dark-factory-go-e2e)

cleanup() {
    status=$?
    trap - EXIT HUP INT TERM
    /bin/rm -rf -- "$e2e_root"
    exit "$status"
}
trap cleanup EXIT HUP INT TERM

go=$(go_e2e_resolve_tool go "${DARK_FACTORY_E2E_GO-}")

CDPATH= cd -- "$repository_root"
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
unset DARK_FACTORY_BROWSER_E2E DARK_FACTORY_DAEMON_E2E DARK_FACTORY_SERVICE_E2E
set -- ./cmd/factory-runner ./cmd/factoryctl
case "$mode" in
    daemon|all)
        [ -x /Library/Developer/CommandLineTools/usr/bin/git ] || {
            echo "go-e2e: CommandLineTools git is required for real attempts" >&2; exit 1;
        }
        set -- "$@" ./cmd/factoryd ;;
    browser-race) set -- -race "$@" ;;
esac
# One fresh sibling directory; factoryd resolves this exact runner and CLI.
"$go" build -o "$e2e_root/" "$@"
export DARK_FACTORY_E2E_RUNNER="$e2e_root/factory-runner"
export DARK_FACTORY_E2E_FACTORYCTL="$e2e_root/factoryctl"

if [ "$mode" != daemon ]; then
    node=$(go_e2e_resolve_tool node "${DARK_FACTORY_E2E_NODE-}")
    corepack=$(go_e2e_resolve_tool corepack "${DARK_FACTORY_E2E_COREPACK-}")
    # Only the combined source gate supplies this explicit same-checkout contract.
    if [ "$client_built" != --client-built ]; then
        (
            CDPATH= cd -- "$repository_root/web"
            export COREPACK_ENABLE_NETWORK=0 CI=true
            "$node" "$corepack" pnpm install --offline --frozen-lockfile --ignore-scripts
            "$node" "$corepack" pnpm --filter @dark-factory/client run build
        )
    fi

    export DARK_FACTORY_BROWSER_E2E=1
    export DARK_FACTORY_E2E_NODE="$node"
    export DARK_FACTORY_E2E_NODE_SCRIPT="$repository_root/web/packages/client/test/e2e/go-browser-pty.mjs"

    count=${DARK_FACTORY_BROWSER_E2E_COUNT:-1}
    case "$count" in
        ''|*[!0-9]*|0) echo "go-browser-e2e: repeat count must be a positive integer" >&2; exit 1 ;;
    esac

    if [ "$mode" = browser-race ]; then
        "$go" test -race -timeout=2m -count="$count" -p 1 ./internal/e2e
        echo "go-browser-e2e: PASS ($count serial race run(s))"
    else
        "$go" test -timeout=2m -count="$count" -p 1 ./internal/e2e
        echo "go-browser-e2e: PASS ($count serial run(s))"
    fi
    unset DARK_FACTORY_BROWSER_E2E
fi

case "$mode" in
    daemon|all)
        export DARK_FACTORY_DAEMON_E2E=1
        export DARK_FACTORY_E2E_FACTORYD="$e2e_root/factoryd"
        "$go" test -v -timeout=10m -count=1 -p 1 -run 'TestBlackBoxDaemonLifecycle|TestBlackBoxDaemonHandoverReplacesFactorydUnderALiveProvider' ./internal/e2e
        echo "go-daemon-e2e: PASS" ;;
esac
