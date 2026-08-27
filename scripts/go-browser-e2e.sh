#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
race=0
case "$#:${1-}" in
    0:) ;;
    1:--race) race=1 ;;
    *) echo "usage: $0 [--race]" >&2; exit 2 ;;
esac
e2e_root=$(/usr/bin/mktemp -d /private/tmp/dark-factory-go-browser-e2e.XXXXXX)

cleanup() {
    status=$?
    trap - EXIT HUP INT TERM
    /bin/rm -rf -- "$e2e_root"
    exit "$status"
}
trap cleanup EXIT HUP INT TERM

node=$(/usr/bin/which node)
case "$node" in
    /*) ;;
    *) echo "go-browser-e2e: Node executable is not absolute" >&2; exit 1 ;;
esac

CDPATH= cd -- "$repository_root"
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
if [ "$race" -eq 1 ]; then
    go build -race -o "$e2e_root/factory-runner" ./cmd/factory-runner
    go build -race -o "$e2e_root/factoryctl" ./cmd/factoryctl
else
    go build -o "$e2e_root/factory-runner" ./cmd/factory-runner
    go build -o "$e2e_root/factoryctl" ./cmd/factoryctl
fi

(
    CDPATH= cd -- "$repository_root/web"
    export COREPACK_ENABLE_NETWORK=0
    corepack pnpm install --offline --frozen-lockfile --ignore-scripts
    corepack pnpm --filter @dark-factory/client run build
)

export DARK_FACTORY_BROWSER_E2E=1
export DARK_FACTORY_E2E_RUNNER="$e2e_root/factory-runner"
export DARK_FACTORY_E2E_FACTORYCTL="$e2e_root/factoryctl"
export DARK_FACTORY_E2E_NODE="$node"
export DARK_FACTORY_E2E_NODE_SCRIPT="$repository_root/web/packages/client/test/e2e/go-browser-pty.mjs"

count=${DARK_FACTORY_BROWSER_E2E_COUNT:-1}
case "$count" in
    ''|*[!0-9]*|0) echo "go-browser-e2e: repeat count must be a positive integer" >&2; exit 1 ;;
esac

if [ "$race" -eq 1 ]; then
    go test -race -timeout=2m -count="$count" -p 1 ./internal/e2e
    echo "go-browser-e2e: PASS ($count serial race run(s))"
else
    go test -timeout=2m -count="$count" -p 1 ./internal/e2e
    echo "go-browser-e2e: PASS ($count serial run(s))"
fi
