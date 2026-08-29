#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
mode=${1:-macos}
test "$#" -le 1 || {
    echo "usage: scripts/local-ci.sh [macos|--legacy-rust|--linux-source]" >&2
    exit 2
}
case "$mode" in
    macos | --legacy-rust)
        if [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" != 1 ]; then
            exec "$script_dir/with-local-ci-lease.sh" "$script_dir/local-ci.sh" "$mode"
        fi
        ;;
    --linux-source) ;;
    *)
        echo "unknown local-ci mode: $mode" >&2
        exit 2
        ;;
esac

# Keep lease diagnostics attributable to the invoking live task, then make
# every gate child independent of that task's runtime and identity overrides.
# shellcheck source=scripts/local-ci-environment.sh
. "$script_dir/local-ci-environment.sh"

case "$mode" in
    macos)
        ./scripts/test-local-ci-lease.sh
        ./scripts/test-local-ci-lease-mutations.sh
        ./scripts/check-toolchain-pins.sh
        # These fixtures exercise the macOS release/publisher contract. They
        # are intentionally outside the Linux source-preview gate: Linux
        # archive and installer support belongs to #142/#143.
        ./scripts/test-prepare-release-source.sh
        ./scripts/test-publish-release.sh
        ./scripts/test-package-release.sh
        ./scripts/test-macos-launchd-release-proof.sh
        ;;
    --legacy-rust)
        # The retired Rust runtime keeps its exact pre-cutover gate until the
        # deletion lands. Same fixture list as the macOS mode above so the
        # legacy job proves the release contract it still ships under.
        ./scripts/test-local-ci-lease.sh
        ./scripts/test-local-ci-lease-mutations.sh
        ./scripts/check-toolchain-pins.sh
        ./scripts/test-prepare-release-source.sh
        ./scripts/test-publish-release.sh
        ./scripts/test-package-release.sh
        ./scripts/test-macos-launchd-release-proof.sh
        ;;
    --linux-source)
        ./scripts/check-toolchain-pins.sh
        ;;
esac

# Measure after the macOS repository lease is held, so another linked worktree
# cannot begin a broad gate between this read-only preflight and our compile.
./scripts/test-local-ci-environment.sh
./scripts/test-new-worktree.sh
./scripts/check-build-headroom.sh
./scripts/test-build-headroom.sh

# The shared shell-fixture gate is platform-neutral. Keep this seam explicit
# so a platform mode cannot silently omit a core check.
./scripts/test-github-step-summary.sh
./scripts/test-verify-adversarial-review.sh
./scripts/test-inline-chokepoint.sh

case "$mode" in
    macos)
        # The authoritative source gate is the Go gate: gofmt, go vet, the
        # full serial and race suites, the TypeScript client proof, and the
        # browser and daemon E2Es, all inside the isolated stage environment.
        # The lease held above covers the whole gate, so the owned stage runs
        # directly instead of re-entering scripts/go-ci.sh.
        /bin/sh "$script_dir/go-ci-owned.sh"
        ;;
    --legacy-rust | --linux-source)
        # The retired Rust workspace stays green until its deletion; Linux
        # remains on this source preview until #142/#143 bring the Go daemon
        # to Linux.
        cargo +1.88.0 fmt --all -- --check
        cargo +1.88.0 clippy --locked --workspace --all-targets --all-features -- -D warnings
        cargo +1.88.0 test --locked --workspace -- --test-threads=1
        ;;
esac
git diff --check
