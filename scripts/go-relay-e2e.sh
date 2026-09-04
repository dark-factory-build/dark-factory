#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
. "$script_dir/go-e2e-tools.sh"
case "$#" in
    0) ;;
    *) echo "usage: $0" >&2; exit 2 ;;
esac

trusted_git=/Library/Developer/CommandLineTools/usr/bin/git
if [ ! -x "$trusted_git" ]; then
    echo "go-relay-e2e: the CommandLineTools git is required for real attempts" >&2
    exit 1
fi

e2e_root=$(go_e2e_temporary_directory dark-factory-go-relay-e2e)
# A daemon's Unix socket path must fit in 103 bytes, which a macOS TMPDIR does
# not leave room for, so the factory homes live under /private/tmp. The trap
# owns both roots, so a stage timeout cleans up what the harness cannot.
relay_root=$(/usr/bin/mktemp -d /private/tmp/df-relay-e2e.XXXXXX)

cleanup() {
    status=$?
    trap - EXIT HUP INT TERM
    /bin/rm -rf -- "$e2e_root" "$relay_root"
    exit "$status"
}
trap cleanup EXIT HUP INT TERM

go=$(go_e2e_resolve_tool go "${DARK_FACTORY_E2E_GO-}")
node=$(go_e2e_resolve_tool node "${DARK_FACTORY_E2E_NODE-}")
corepack=$(go_e2e_resolve_tool corepack "${DARK_FACTORY_E2E_COREPACK-}")

CDPATH= cd -- "$repository_root"
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
# All three binaries share one directory so factoryd's sibling self-location
# selects exactly the freshly built runner and factoryctl.
"$go" build -o "$e2e_root/factoryd" ./cmd/factoryd
"$go" build -o "$e2e_root/factoryctl" ./cmd/factoryctl
"$go" build -o "$e2e_root/factory-runner" ./cmd/factory-runner

(
    CDPATH= cd -- "$repository_root/web"
    export COREPACK_ENABLE_NETWORK=0 CI=true
    "$node" "$corepack" pnpm install --offline --frozen-lockfile --ignore-scripts
    "$node" "$corepack" pnpm --filter @dark-factory/client run build
)

# The relay Worker runs from its own lockfile, exactly as relay/scripts/local-ci.sh
# installs it. CI starts from a clean tree, so this always installs: npm comes
# from beside the resolved Node, and its cache goes in the trap-cleaned root
# because the gate's HOME is not writable.
npm="$(/usr/bin/dirname "$node")/npm"
[ -f "$npm" ] && [ -x "$npm" ] || {
    echo "go-relay-e2e: npm is not installed beside $node" >&2
    exit 1
}
(
    CDPATH= cd -- "$repository_root/relay"
    export npm_config_cache="$e2e_root/npm-cache"
    "$node" "$npm" ci --ignore-scripts --no-audit --no-fund
)

export DARK_FACTORY_E2E_FACTORYD="$e2e_root/factoryd"
export DARK_FACTORY_E2E_FACTORYCTL="$e2e_root/factoryctl"
export DARK_FACTORY_E2E_RUNNER="$e2e_root/factory-runner"
export DARK_FACTORY_E2E_RELAY_ROOT="$relay_root"

"$node" "$repository_root/web/packages/client/test/e2e/go-relay.mjs"
echo "go-relay-e2e: PASS"
