#!/bin/sh
# One repository-wide local-CI lock, shared by every checkout of this
# repository. lockf blocks until the lock is free and the kernel releases it
# when the holder exits, so there is no stale state to detect or recover.
set -eu

[ "$#" -gt 0 ] || { echo "local-ci: lease wrapper requires a command" >&2; exit 64; }
[ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" != 1 ] || {
    echo "local-ci: nested lease invocation refused; use the existing owner" >&2
    exit 1
}
# A worker sandbox can write only the daemon-prepared lease directory.
lease_dir=${DARK_FACTORY_LOCAL_CI_DIRECTORY-}
if [ -z "$lease_dir" ]; then
    common=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null) || {
        echo "local-ci: cannot resolve the git common directory" >&2
        exit 1
    }
    lease_dir=$common/dark-factory-local-ci
fi
(umask 077 && mkdir -p "$lease_dir/.dark-factory-local-ci.lock")
export DARK_FACTORY_LOCAL_CI_LEASE_HELD=1
lease_log=$lease_dir/.dark-factory-local-ci.lock/lease.log
lease_requested_at=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
export lease_log lease_requested_at
exec lockf -k "$lease_dir/.dark-factory-local-ci.lock/descriptor" sh -c '
    lease_acquired_at=$(date -u "+%Y-%m-%dT%H:%M:%SZ")
    lease_status=0
    "$@" || lease_status=$?
    lease_released_at=$(date -u "+%Y-%m-%dT%H:%M:%SZ")
    printf "command=%s requested_at=%s acquired_at=%s released_at=%s\\n" \
        "$*" "$lease_requested_at" "$lease_acquired_at" "$lease_released_at" >>"$lease_log"
    exit "$lease_status"
' sh "$@"
