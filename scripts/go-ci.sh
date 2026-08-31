#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
. "$script_dir/local-ci-environment.sh"
[ "$#" -eq 0 ] || { echo "usage: scripts/go-ci.sh" >&2; exit 64; }
# The local-ci entrypoint sources the same bootstrap before acquiring its
# lease; this wrapper retains the standalone process-sensitive entrypoint.
exec "$script_dir/with-local-ci-lease.sh" /bin/sh "$script_dir/go-ci-owned.sh" "$@"
