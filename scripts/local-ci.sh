#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
test "$#" -eq 0 || {
    echo "usage: scripts/local-ci.sh" >&2
    exit 2
}
if [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" != 1 ]; then
    exec "$script_dir/with-local-ci-lease.sh" "$script_dir/local-ci.sh"
fi

# Keep lease diagnostics attributable to the invoking live task, then make
# every gate child independent of that task's runtime and identity overrides.
# shellcheck source=scripts/local-ci-environment.sh
. "$script_dir/local-ci-environment.sh"

./scripts/test-local-ci-lease.sh
./scripts/test-local-ci-lease-mutations.sh
./scripts/check-toolchain-pins.sh
# These fixtures exercise the macOS release/publisher contract. Linux archive
# and installer support belongs to #142/#143, after the Go daemon reaches
# Linux at all.
./scripts/test-prepare-release-source.sh
./scripts/test-publish-release.sh
./scripts/test-package-release.sh

# Measure after the repository lease is held, so another linked worktree
# cannot begin a broad gate between this read-only preflight and our compile.
./scripts/test-local-ci-environment.sh
./scripts/test-new-worktree.sh

# The shared shell-fixture gate is platform-neutral. Keep this seam explicit
# so a future platform split cannot silently omit a core check.
./scripts/test-github-step-summary.sh
./scripts/test-verify-adversarial-review.sh
./scripts/test-inline-chokepoint.sh
./scripts/test-go-e2e-tools.sh
# The deleted Linux job was the only caller of both fixtures below. One
# asserts the `required` aggregate's shape against the publisher; the other is
# the primary guard for the Rust deletion itself — that the retired scripts
# stay deleted, that no CI job or aggregate dependency comes back, and that
# this gate keeps every fixture. A job deletion is exactly what silently
# orphans them, so the gate runs both.
./scripts/test-repository-settings.sh
./scripts/test-local-ci-mode.sh

# The authoritative source gate is the Go gate: gofmt, go vet, the full serial
# and race suites, the TypeScript client proof, and the browser, daemon and
# service E2Es, all inside the isolated stage environment. The lease held above
# covers the whole gate, so the owned stage runs directly instead of
# re-entering scripts/go-ci.sh.
/bin/sh "$script_dir/go-ci-owned.sh"
git diff --check
