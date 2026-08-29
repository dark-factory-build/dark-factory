#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
case "$#" in
    0) ;;
    *) echo "usage: $0" >&2; exit 2 ;;
esac

trusted_git=/Library/Developer/CommandLineTools/usr/bin/git
if [ ! -x "$trusted_git" ]; then
    echo "go-service-e2e: the CommandLineTools git is required for real attempts" >&2
    exit 1
fi

e2e_root=$(/usr/bin/mktemp -d /private/tmp/dark-factory-go-service-e2e.XXXXXX)

cleanup() {
    status=$?
    trap - EXIT HUP INT TERM
    # The Go test boots its disposable label out itself; the sweep below is
    # the last-resort guarantee for a killed test process. Only labels this
    # script's own test run could have minted are touched.
    for label in $(/bin/launchctl list 2>/dev/null | /usr/bin/awk '{print $3}' | /usr/bin/grep "^com\.dark-factory\.e2e\." 2>/dev/null || true); do
        /bin/launchctl bootout "gui/$(/usr/bin/id -u)/$label" 2>/dev/null || true
    done
    /bin/rm -rf -- "$e2e_root"
    exit "$status"
}
trap cleanup EXIT HUP INT TERM

CDPATH= cd -- "$repository_root"
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
# All three binaries share one directory: the installed service is exactly
# the sibling set the invoking factoryctl shipped with.
go build -o "$e2e_root/factoryd" ./cmd/factoryd
go build -o "$e2e_root/factoryctl" ./cmd/factoryctl
go build -o "$e2e_root/factory-runner" ./cmd/factory-runner

export DARK_FACTORY_SERVICE_E2E=1
export DARK_FACTORY_E2E_FACTORYD="$e2e_root/factoryd"
export DARK_FACTORY_E2E_FACTORYCTL="$e2e_root/factoryctl"
export DARK_FACTORY_E2E_RUNNER="$e2e_root/factory-runner"

go test -timeout=8m -count=1 -p 1 -run TestBlackBoxServiceLifecycle ./internal/e2e
echo "go-service-e2e: PASS"
