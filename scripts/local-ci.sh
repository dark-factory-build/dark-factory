#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
[ "$#" -le 2 ] || { echo "usage: scripts/local-ci.sh [--full|--runtime|--release|--ui|--warm] [daemon|packages|source] | --affected [BASE]" >&2; exit 2; }
case "${1-}" in
    '') local_ci_mode=full ;;
    --full) local_ci_mode=full ;;
    --runtime) local_ci_mode=runtime ;;
    --release) local_ci_mode=release ;;
    --ui) local_ci_mode=ui ;;
    --warm) local_ci_mode=warm ;;
    --affected) local_ci_mode=affected ;;
    *) echo "usage: scripts/local-ci.sh [--full|--runtime|--release|--ui|--warm] [daemon|packages|source] | --affected [BASE]" >&2; exit 2 ;;
esac
# CI splits the full and runtime gates across parallel Macs. A shard runs one
# part of go-ci-owned.sh; "source" also runs everything outside it. No shard
# runs everything, as before.
local_ci_shard=${2-}
affected_base=
if [ "$local_ci_mode" = affected ]; then
    affected_base=$local_ci_shard
    local_ci_shard=
fi
case "$local_ci_shard" in
    ''|daemon|packages|source) ;;
    *) echo "usage: scripts/local-ci.sh [--full|--runtime|--release|--ui|--warm] [daemon|packages|source] | --affected [BASE]" >&2; exit 2 ;;
esac
in_source_shard() { [ "$local_ci_shard" != daemon ] && [ "$local_ci_shard" != packages ]; }

# Establish the small, credential-free gate environment once. The caller's
# tool path and cache root are the only host-specific values that cross in.
if [ "${DARK_FACTORY_LOCAL_CI_ENV-}" != 1 ]; then
    git_common=$(/usr/bin/env -i PATH=/usr/bin:/bin HOME=/dev/null \
        /usr/bin/git rev-parse --git-common-dir 2>/dev/null) || {
        echo "local-ci: cannot resolve the git common directory" >&2
        exit 1
    }
    ci_path=${PATH-}
    ci_home=${HOME-}
    ci_tmpdir=${TMPDIR-/tmp}
    ci_cache_root=${DF_CI_CACHE_ROOT-}
    [ -n "$ci_cache_root" ] || ci_cache_root=${ci_home:-/var/empty}/Library/Caches/dark-factory/local-ci/trusted
    ci_go_module_cache=${DF_CI_GO_MODULE_CACHE-}
    [ -n "$ci_go_module_cache" ] || ci_go_module_cache="$ci_cache_root/go-mod"
    ci_go=${DF_CI_GO-}; [ -n "$ci_go" ] || ci_go=$(PATH="$ci_path" command -v go || true)
    ci_node=${DF_CI_NODE-}; [ -n "$ci_node" ] || ci_node=$(PATH="$ci_path" command -v node || true)
    ci_corepack=${DF_CI_COREPACK-}; [ -n "$ci_corepack" ] || ci_corepack=$(PATH="$ci_path" command -v corepack || true)
    [ -n "$ci_go" ] || { echo "local-ci: go is unavailable" >&2; exit 1; }
    [ -n "$ci_node" ] && [ -n "$ci_corepack" ] || { echo "local-ci: Node/Corepack is unavailable" >&2; exit 1; }
    /bin/mkdir -p "$ci_cache_root" "$script_dir/../.tools/local-ci-state/data" "$script_dir/../.tools/local-ci-state/state"
    exec /usr/bin/env -i \
        DARK_FACTORY_LOCAL_CI_ENV=1 DARK_FACTORY_LOCAL_CI=1 \
        DARK_FACTORY_LOCAL_CI_DIRECTORY="${DARK_FACTORY_LOCAL_CI_DIRECTORY-}" \
        DARK_FACTORY_LOCAL_CI_LEASE_HELD="${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" \
        PATH="$ci_path" HOME=/var/empty TMPDIR="$ci_tmpdir" \
        DF_CI_CACHE_ROOT="$ci_cache_root" DF_CI_GO="$ci_go" \
        DF_CI_NODE="$ci_node" DF_CI_COREPACK="$ci_corepack" \
        GOPATH="$ci_cache_root/go" GOCACHE="$ci_cache_root/go-build" \
        DF_CI_GO_MODULE_CACHE="$ci_go_module_cache" GOMODCACHE="$ci_go_module_cache" GOPROXY=off GOSUMDB=off \
        COREPACK_HOME="$ci_cache_root/corepack" \
        npm_config_cache="$ci_cache_root/npm" NPM_CONFIG_CACHE="$ci_cache_root/npm" \
        pnpm_config_store_dir="$ci_cache_root/pnpm-store" \
        GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 \
        NETRC=/dev/null XDG_CONFIG_HOME=/var/empty XDG_CACHE_HOME="$ci_cache_root/cache" \
        XDG_DATA_HOME="$script_dir/../.tools/local-ci-state/data" \
        XDG_STATE_HOME="$script_dir/../.tools/local-ci-state/state" \
        GOENV=off GOFLAGS=-modcacherw LC_ALL=C \
        /bin/sh "$0" "$@"
fi

if { [ "$local_ci_mode" = full ] || [ "$local_ci_mode" = release ]; } \
    && [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" != 1 ]; then
    exec "$script_dir/with-local-ci-lease.sh" "$script_dir/local-ci.sh" "--$local_ci_mode" ${local_ci_shard:+"$local_ci_shard"}
fi

# CI's push-to-main job fills the compiler, package, and cacheable test-result
# caches that merge-queue runs restore.
if [ "$local_ci_mode" = warm ]; then
    ./scripts/go-check.sh
    # Queue runs cannot save cache entries for other runs, so execute the
    # cacheable tests on main while the cache is saved.
    /bin/sh "$script_dir/go-ci-owned.sh" --cacheable
    echo "local-ci: PASS (warm)"
    exit 0
fi

# A pull request's own check (and a worker's pre-publish check): the source
# gate, then only the process tests that depend on what changed since BASE.
if [ "$local_ci_mode" = affected ]; then
    affected_base=$(git rev-parse --verify "${affected_base:-$(git merge-base origin/main HEAD)}^{commit}")
    ./scripts/go-check.sh
    if [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" = 1 ]; then
        /bin/sh "$script_dir/go-ci-owned.sh" --client-built --affected "$affected_base"
    else
        "$script_dir/with-local-ci-lease.sh" /bin/sh "$script_dir/go-ci-owned.sh" --client-built --affected "$affected_base"
    fi
    echo "local-ci: PASS (affected since $affected_base)"
    exit 0
fi

if [ "$local_ci_mode" = ui ]; then
    echo "local-ci: UI source and browser smoke gate"
    ./scripts/go-check.sh --ui
    if [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" = 1 ]; then
        ./scripts/go-e2e.sh browser --client-built
    else
        "$script_dir/with-local-ci-lease.sh" ./scripts/go-e2e.sh browser --client-built
    fi
    echo "local-ci: PASS (ui)"
    exit 0
fi

if [ "$local_ci_mode" = full ] && in_source_shard; then
    echo "local-ci: repository contract fixtures"
    ./scripts/check-toolchain-pins.sh
    ./scripts/test-with-local-ci-lease.sh
    ./scripts/test-new-worktree.sh
    ./scripts/test-publication-parents.sh
    python3 ./scripts/test-factory-browser.py
    ./scripts/test-github-step-summary.sh
    ./scripts/test-release.sh
    ./scripts/test-repository-settings.sh
fi

if [ "$local_ci_mode" = full ] || [ "$local_ci_mode" = runtime ]; then
    if in_source_shard; then
        echo "local-ci: ordinary source gate"
        ./scripts/go-check.sh
    fi

    echo "local-ci: process-sensitive gate"
    if in_source_shard; then
        ./scripts/test-go-e2e-tools.sh
    fi
    if [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" = 1 ]; then
        /bin/sh "$script_dir/go-ci-owned.sh" --client-built ${local_ci_shard:+"$local_ci_shard"}
    else
        "$script_dir/with-local-ci-lease.sh" /bin/sh "$script_dir/go-ci-owned.sh" --client-built ${local_ci_shard:+"$local_ci_shard"}
    fi
fi

if { [ "$local_ci_mode" = full ] || [ "$local_ci_mode" = release ]; } && in_source_shard; then
    echo "local-ci: release gate"
    if [ "$local_ci_mode" = release ]; then
        ./scripts/go-check.sh
    fi
    ./scripts/test-prepare-release-source.sh
    # The full gate already tests buildinfo in the process-sensitive packages.
    if [ "$local_ci_mode" = release ]; then
        GOTOOLCHAIN=local "$DF_CI_GO" test -count=1 ./internal/buildinfo/...
    fi
fi
echo "local-ci: PASS ($local_ci_mode${local_ci_shard:+ $local_ci_shard})"
