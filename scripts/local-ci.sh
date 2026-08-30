#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
[ "$#" -eq 0 ] || { echo "usage: scripts/local-ci.sh" >&2; exit 2; }
if [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" != 1 ]; then
    exec "$script_dir/with-local-ci-lease.sh" "$script_dir/local-ci.sh"
fi
. "$script_dir/local-ci-environment.sh"

echo "local-ci: repository contract fixtures"
./scripts/check-toolchain-pins.sh
./scripts/test-local-ci-environment.sh
./scripts/test-new-worktree.sh
./scripts/test-github-step-summary.sh
./scripts/test-verify-adversarial-review.sh
./scripts/test-inline-chokepoint.sh
./scripts/test-cloudflare-env.sh
./scripts/test-bootstrap-maintainer-v2.sh
./scripts/test-repository-settings.sh
./scripts/test-local-ci-mode.sh
./scripts/test-go-gates.sh

echo "local-ci: ordinary source gate"
./scripts/go-check.sh

echo "local-ci: process-sensitive gate"
./scripts/test-local-ci-lease.sh
./scripts/test-local-ci-lease-mutations.sh
./scripts/test-go-e2e-tools.sh
/bin/sh "$script_dir/go-ci-owned.sh"

echo "local-ci: service and release gate"
./scripts/test-prepare-release-source.sh
./scripts/test-publish-release.sh
./scripts/test-package-release.sh
./scripts/go-service-e2e.sh
echo "local-ci: PASS"
