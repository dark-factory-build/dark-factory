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
    echo "go-daemon-e2e: the CommandLineTools git is required for real attempts" >&2
    exit 1
fi

e2e_root=$(/usr/bin/mktemp -d /private/tmp/dark-factory-go-daemon-e2e.XXXXXX)

cleanup() {
    status=$?
    trap - EXIT HUP INT TERM
    /bin/rm -rf -- "$e2e_root"
    exit "$status"
}
trap cleanup EXIT HUP INT TERM

CDPATH= cd -- "$repository_root"
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
# All three binaries share one directory so factoryd's sibling self-location
# selects exactly the freshly built runner and factoryctl.
go=$(go_e2e_resolve_tool go "${DARK_FACTORY_E2E_GO-}")
"$go" build -o "$e2e_root/factoryd" ./cmd/factoryd
"$go" build -o "$e2e_root/factoryctl" ./cmd/factoryctl
"$go" build -o "$e2e_root/factory-runner" ./cmd/factory-runner

export DARK_FACTORY_DAEMON_E2E=1
export DARK_FACTORY_E2E_FACTORYD="$e2e_root/factoryd"
export DARK_FACTORY_E2E_FACTORYCTL="$e2e_root/factoryctl"
export DARK_FACTORY_E2E_RUNNER="$e2e_root/factory-runner"

"$go" test -timeout=5m -count=1 -p 1 -run TestBlackBoxDaemonLifecycle ./internal/e2e
echo "go-daemon-e2e: PASS"
