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
if [ "${DARK_FACTORY_LOCAL_CI_ENV-}" != 1 ]; then
    ci_path=${PATH-}; ci_home=${HOME-}; ci_tmpdir=${TMPDIR-/tmp}; ci_cache_root=${DF_CI_CACHE_ROOT-${ci_home:-/var/empty}/Library/Caches/dark-factory/local-ci/trusted}
    ci_go_module_cache=${DF_CI_GO_MODULE_CACHE-}; ci_goproxy=${GOPROXY-https://proxy.golang.org,direct}; ci_gosumdb=${GOSUMDB-sum.golang.org}
    if [ -n "$ci_go_module_cache" ]; then ci_goproxy=off; ci_gosumdb=off; else ci_go_module_cache="$ci_cache_root/go-mod"; fi
    ci_go=${DF_CI_GO-}; [ -n "$ci_go" ] || ci_go=$(PATH="$ci_path" command -v go || true)
    ci_node=${DF_CI_NODE-}; [ -n "$ci_node" ] || ci_node=$(PATH="$ci_path" command -v node || true)
    ci_corepack=${DF_CI_COREPACK-}; [ -n "$ci_corepack" ] || ci_corepack=$(PATH="$ci_path" command -v corepack || true)
    [ -n "$ci_go" ] || { echo "local-ci: go is unavailable" >&2; exit 1; }; [ -n "$ci_node" ] && [ -n "$ci_corepack" ] || { echo "local-ci: Node/Corepack is unavailable" >&2; exit 1; }
    /bin/mkdir -p "$ci_cache_root" "$script_dir/../.tools/local-ci-state/data" "$script_dir/../.tools/local-ci-state/state"
    exec /usr/bin/env -i DARK_FACTORY_LOCAL_CI_ENV=1 DARK_FACTORY_LOCAL_CI=1 DARK_FACTORY_LOCAL_CI_DIRECTORY="${DARK_FACTORY_LOCAL_CI_DIRECTORY-}" DARK_FACTORY_LOCAL_CI_LEASE_HELD="${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" PATH="$ci_path" HOME=/var/empty TMPDIR="$ci_tmpdir" DF_CI_CACHE_ROOT="$ci_cache_root" DF_CI_GO="$ci_go" DF_CI_NODE="$ci_node" DF_CI_COREPACK="$ci_corepack" GOPATH="$ci_cache_root/go" GOCACHE="$ci_cache_root/go-build" DF_CI_GO_MODULE_CACHE="${DF_CI_GO_MODULE_CACHE-}" GOMODCACHE="$ci_go_module_cache" GOPROXY="$ci_goproxy" GOSUMDB="$ci_gosumdb" COREPACK_HOME="$ci_cache_root/corepack" npm_config_cache="$ci_cache_root/npm" NPM_CONFIG_CACHE="$ci_cache_root/npm" pnpm_config_store_dir="$ci_cache_root/pnpm-store" GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 NETRC=/dev/null XDG_CONFIG_HOME=/var/empty XDG_CACHE_HOME="$ci_cache_root/cache" XDG_DATA_HOME="$script_dir/../.tools/local-ci-state/data" XDG_STATE_HOME="$script_dir/../.tools/local-ci-state/state" GOENV=off GOFLAGS=-modcacherw LC_ALL=C /bin/sh "$0" "$@"
fi
if [ "$local_ci_mode" = full ] && [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" != 1 ]; then
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
    echo "local-ci: release fixture"; ./scripts/test-release.sh
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
