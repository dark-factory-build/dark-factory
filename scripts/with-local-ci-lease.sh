#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
export LOCAL_CI_LEASE_HELPER="$script_dir/local-ci-lease.sh"
. "$LOCAL_CI_LEASE_HELPER"

local_ci_lease_run "$@"
