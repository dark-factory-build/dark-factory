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
exec lockf -k "$lease_dir/.dark-factory-local-ci.lock/descriptor" "$@"
