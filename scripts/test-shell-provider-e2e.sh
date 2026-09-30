#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
CDPATH= cd -- "$repository_root"
exec "$script_dir/with-local-ci-lease.sh" go test -count=1 -timeout=5m -run '^TestShellProviderDelegateFanInPRProposal$' ./internal/daemon
