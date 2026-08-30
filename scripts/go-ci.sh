#!/bin/sh
set -eu

[ "$#" -eq 0 ] || { echo "usage: scripts/go-ci.sh" >&2; exit 2; }
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec "$script_dir/with-local-ci-lease.sh" /bin/sh "$script_dir/go-ci-owned.sh"
