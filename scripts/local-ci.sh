#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
[ "$#" -le 2 ] || { echo "usage: scripts/local-ci.sh [--full|--runtime|--release|--ui|--warm] [daemon|packages|source]" >&2; exit 2; }
case "${1-}" in
    '') local_ci_mode=full ;;
    --full) local_ci_mode=full ;;
    --runtime) local_ci_mode=runtime ;;
    --release) local_ci_mode=release ;;
    --ui) local_ci_mode=ui ;;
    --warm) local_ci_mode=warm ;;
    *) echo "usage: scripts/local-ci.sh [--full|--runtime|--release|--ui|--warm] [daemon|packages|source]" >&2; exit 2 ;;
esac
# CI splits the full and runtime gates across parallel Macs. A shard runs one
# part of go-ci-owned.sh; "source" also runs everything outside it. No shard
# runs everything, as before.
local_ci_shard=${2-}
case "$local_ci_shard" in
    ''|daemon|packages|source) ;;
    *) echo "usage: scripts/local-ci.sh [--full|--runtime|--release|--ui|--warm] [daemon|packages|source]" >&2; exit 2 ;;
esac
in_source_shard() { [ "$local_ci_shard" != daemon ] && [ "$local_ci_shard" != packages ]; }
# Refuse outside Git before creating cache state; inherited Git locators must
# not turn another repository into the target of this preflight.
/usr/bin/env -i PATH=/usr/bin:/bin HOME=/dev/null TMPDIR=/tmp \
    GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null \
    GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_COUNT=0 \
    /usr/bin/git rev-parse --git-common-dir >/dev/null 2>&1 || {
    echo "local-ci: cannot resolve the git common directory" >&2
    exit 1
}
. "$script_dir/local-ci-environment.sh"
export DARK_FACTORY_LOCAL_CI=1

if { [ "$local_ci_mode" = full ] || [ "$local_ci_mode" = release ]; } \
    && [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" != 1 ]; then
    exec "$script_dir/with-local-ci-lease.sh" "$script_dir/local-ci.sh" "--$local_ci_mode" ${local_ci_shard:+"$local_ci_shard"}
fi

# CI's push-to-main job fills the compiler and package caches that merge
# queue runs restore: everything the source gate builds, plus test binaries.
if [ "$local_ci_mode" = warm ]; then
    ./scripts/go-check.sh
    # The binaries land in the unsaved cache/ child; only their compiled
    # packages in go-build matter.
    GOTOOLCHAIN=local "$DF_CI_GO" test -c -o "$XDG_CACHE_HOME/warm-test-binaries/" ./...
    echo "local-ci: PASS (warm)"
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
    ./scripts/test-local-ci-environment.sh
    ./scripts/test-new-worktree.sh
    ./scripts/test-publication-parents.sh
    python3 ./scripts/test-factory-browser.py
    ./scripts/test-github-step-summary.sh
    ./scripts/test-release.sh
    ./scripts/test-repository-settings.sh
    /bin/sh ./scripts/test-go-gates.sh
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
