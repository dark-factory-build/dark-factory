#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
. "$script_dir/go-e2e-tools.sh"
case "$#" in
    0) ;;
    *) echo "usage: $0" >&2; exit 2 ;;
esac
if [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" != 1 ]; then
    exec "$script_dir/with-local-ci-lease.sh" "$script_dir/go-service-e2e.sh"
fi

trusted_git=/Library/Developer/CommandLineTools/usr/bin/git
if [ ! -x "$trusted_git" ]; then
    echo "go-service-e2e: the CommandLineTools git is required for real attempts" >&2
    exit 1
fi

e2e_root=$(go_e2e_temporary_directory dark-factory-go-service-e2e)
# The script mints the run's one disposable label so the exit trap can boot
# out exactly this run's job — never a concurrently running sibling gate's.
service_label="com.dark-factory.e2e.$$.$(/bin/date +%s)"

cleanup() {
    status=$?
    trap - EXIT HUP INT TERM
    # The Go test boots the label out itself; this is the last-resort
    # guarantee for a killed test process, scoped to this run's own label.
    /bin/launchctl bootout "gui/$(/usr/bin/id -u)/$service_label" 2>/dev/null || true
    /bin/rm -rf -- "$e2e_root"
    exit "$status"
}
trap cleanup EXIT HUP INT TERM

CDPATH= cd -- "$repository_root"
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
# All three binaries share one directory: the installed service is exactly
# the sibling set the invoking factoryctl shipped with.
go=$(go_e2e_resolve_tool go "${DARK_FACTORY_E2E_GO-}")
"$go" build -o "$e2e_root/factoryd" ./cmd/factoryd
"$go" build -o "$e2e_root/factoryctl" ./cmd/factoryctl
"$go" build -o "$e2e_root/factory-runner" ./cmd/factory-runner
# A second, byte-different sibling set: the install verb replaces a running
# installation with the invoking build, and that needs two of them.
for command in factoryd factoryctl factory-runner; do
    "$go" build -ldflags=-s -o "$e2e_root/next/$command" "./cmd/$command"
done

export DARK_FACTORY_SERVICE_E2E=1
export DARK_FACTORY_E2E_SERVICE_LABEL="$service_label"
export DARK_FACTORY_E2E_FACTORYD="$e2e_root/factoryd"
export DARK_FACTORY_E2E_FACTORYCTL="$e2e_root/factoryctl"
export DARK_FACTORY_E2E_RUNNER="$e2e_root/factory-runner"
export DARK_FACTORY_E2E_NEXT_FACTORYCTL="$e2e_root/next/factoryctl"

"$go" test -timeout=8m -count=1 -p 1 -run TestBlackBoxServiceLifecycle ./internal/e2e
echo "go-service-e2e: PASS"
