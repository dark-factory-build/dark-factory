#!/bin/sh
set -eu
script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
[ "$#" -le 2 ] || { echo "usage: scripts/local-ci.sh [--full|--warm] [daemon|packages|source] | --affected [BASE]" >&2; exit 2; }
case "${1-}" in
    '') local_ci_mode=full ;;
    --full) local_ci_mode=full ;;
    --warm) local_ci_mode=warm ;;
    --affected) local_ci_mode=affected ;;
    *) echo "usage: scripts/local-ci.sh [--full|--warm] [daemon|packages|source] | --affected [BASE]" >&2; exit 2 ;;
esac
local_ci_shard=${2-}
affected_base=
if [ "$local_ci_mode" = affected ]; then
    affected_base=$local_ci_shard
    local_ci_shard=
fi
case "$local_ci_shard" in
    ''|daemon|packages|source) ;;
    *) echo "usage: scripts/local-ci.sh [--full|--warm] [daemon|packages|source] | --affected [BASE]" >&2; exit 2 ;;
esac
in_source_shard() { [ "$local_ci_shard" != daemon ] && [ "$local_ci_shard" != packages ]; }
/usr/bin/env -i PATH=/usr/bin:/bin HOME=/dev/null TMPDIR=/tmp GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_COUNT=0 /usr/bin/git rev-parse --git-common-dir >/dev/null 2>&1 || {
    echo "local-ci: cannot resolve the git common directory" >&2
    exit 1
}
. "$script_dir/local-ci-environment.sh"
export DARK_FACTORY_LOCAL_CI=1
if [ "$local_ci_mode" = full ] \
    && [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" != 1 ]; then
    exec "$script_dir/with-local-ci-lease.sh" "$script_dir/local-ci.sh" "--$local_ci_mode" ${local_ci_shard:+"$local_ci_shard"}
fi
if [ "$local_ci_mode" = warm ]; then
    ./scripts/go-check.sh
    /bin/sh "$script_dir/go-ci-owned.sh" --cacheable
    echo "local-ci: PASS (warm)"
    exit 0
fi
if [ "$local_ci_mode" = affected ]; then
    affected_base=$(git rev-parse --verify "${affected_base:-$(git merge-base origin/main HEAD)}^{commit}")
    ./scripts/go-check.sh
    if [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" = 1 ]; then /bin/sh "$script_dir/go-ci-owned.sh" --client-built --affected "$affected_base"; else "$script_dir/with-local-ci-lease.sh" /bin/sh "$script_dir/go-ci-owned.sh" --client-built --affected "$affected_base"; fi
    echo "local-ci: PASS (affected since $affected_base)"
    exit 0
fi
if [ "$local_ci_mode" = full ] && in_source_shard; then
    echo "local-ci: repository contract fixtures"
    ./scripts/test-release.sh
    python3 ./scripts/test-factory-browser.py; /bin/sh ./scripts/test-go-gates.sh
fi
if [ "$local_ci_mode" = full ]; then
    if in_source_shard; then
        echo "local-ci: ordinary source gate"
        ./scripts/go-check.sh
    fi
    echo "local-ci: process-sensitive gate"
    if [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" = 1 ]; then
        /bin/sh "$script_dir/go-ci-owned.sh" --client-built ${local_ci_shard:+"$local_ci_shard"}
    else
        "$script_dir/with-local-ci-lease.sh" /bin/sh "$script_dir/go-ci-owned.sh" --client-built ${local_ci_shard:+"$local_ci_shard"}
    fi
fi
if [ "$local_ci_mode" = full ] && in_source_shard; then
    echo "local-ci: release gate"; ./scripts/test-prepare-release-source.sh
fi
echo "local-ci: PASS ($local_ci_mode${local_ci_shard:+ $local_ci_shard})"
