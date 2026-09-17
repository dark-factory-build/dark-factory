#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
local_gate="$repository_root/scripts/local-ci.sh"
ordinary_gate="$repository_root/scripts/go-check.sh"
process_gate="$repository_root/scripts/go-ci-owned.sh"
process_entry="$repository_root/scripts/go-ci.sh"
service_gate="$repository_root/scripts/go-service-e2e.sh"
fail() { echo "local-ci shape test failed: $*" >&2; exit 1; }

grep -Fq '1:--ui) go_check_mode=ui' "$ordinary_gate" \
    || fail "Go gate has no narrow UI mode"
grep -Fq 'usage: scripts/go-check.sh [--ui]' "$ordinary_gate" \
    || fail "Go gate usage does not document UI mode"
grep -Fq 'local_ci_mode=ui' "$local_gate" || fail "local-CI has no UI mode"
grep -Fq './scripts/go-check.sh --ui' "$local_gate" || fail "UI mode lacks source checks"
grep -Fq 'with-local-ci-lease.sh" ./scripts/go-browser-e2e.sh' "$local_gate" \
    || fail "UI mode lacks the leased browser smoke"

set +e
usage_output=$(/bin/sh "$local_gate" obsolete-mode 2>&1)
usage_status=$?
set -e
[ "$usage_status" -eq 2 ] || fail "local-ci accepted an argument"
[ "$usage_output" = "usage: scripts/local-ci.sh [--full|--runtime|--release|--ui]" ] || fail "local-ci usage error changed"

grep -Fq './scripts/go-check.sh' "$local_gate" || fail "ordinary source gate is missing"
grep -Fq 'exec "$script_dir/with-local-ci-lease.sh" "$script_dir/local-ci.sh" --full' "$local_gate" \
    || fail "full gate lost its shared lease"
grep -Fq 'with-local-ci-lease.sh" /bin/sh "$script_dir/go-ci-owned.sh"' "$local_gate" \
    || fail "process gate lost the repository lease"
grep -Fq './scripts/test-prepare-release-source.sh' "$local_gate" \
    || fail "release mode lacks release fixtures"
if grep -Fq 'go-service-e2e.sh' "$local_gate"; then
    fail "focused service lifecycle proof returned to the routine gate"
fi
if grep -Eq 'test-local-ci-lease(-mutations)?\.sh' "$local_gate"; then
    fail "focused lease stress returned to the routine gate"
fi

grep -Fq 'go vet ./...' "$ordinary_gate" || fail "ordinary vet is missing"
grep -Fq 'go test -short -timeout=20m' "$ordinary_gate" || fail "ordinary tests are missing"
for web_command in \
    'pnpm --filter @dark-factory/client build' \
    'pnpm --filter @dark-factory/ui build' \
    'pnpm --filter dark-factory-dev typecheck'; do
    grep -Fq '"$DF_CI_NODE" "$DF_CI_COREPACK" '"$web_command" "$ordinary_gate" \
        || fail "TypeScript command is missing its validated toolchain: $web_command"
done
grep -Fq '"$DF_CI_NODE" --test packages/client/test/*.test.mjs packages/ui/test/*.test.mjs' \
    "$ordinary_gate" || fail "TypeScript tests are missing their validated Node"
if grep -Fq 'pnpm run check' "$ordinary_gate"; then
    fail "ordinary gate re-enters an ambient package-script toolchain"
fi
grep -Fq 'git diff --check' "$ordinary_gate" || fail "diff check is missing"
if grep -Eq 'go build|-count=1|-p[[:space:]]+1' "$ordinary_gate"; then
    fail "ordinary gate rebuilds or disables Go caching/parallelism"
fi

grep -Fq 'go test -short -timeout=20m -count=1' "$process_gate" \
    || fail "process tests are not uncached"
for isolated_package in ./internal/change ./internal/changeworker ./internal/daemon; do
    grep -Fq "go_gate_stage 1200 go test -short -timeout=20m -count=1 $isolated_package" \
        "$process_gate" || fail "process package lacks an isolated causal stage: $isolated_package"
    [ "$(grep -Ec "\\$isolated_package$" "$process_gate")" -eq 1 ] \
        || fail "isolated process package is not unique: $isolated_package"
done
for process_package in \
    './internal/buildinfo/...' \
    './internal/kernel' \
    './spikes/browser-connectivity'; do
    grep -Fq "$process_package" "$process_gate" \
        || fail "process-sensitive package is outside the bounded gate: $process_package"
    if grep -Fq "$process_package" "$ordinary_gate"; then
        fail "process-sensitive package remains in the ordinary gate: $process_package"
    fi
done
if grep -Eq -- '-p[[:space:]]+1' "$process_gate"; then
    fail "process packages are globally serial"
fi
grep -Fq 'with-local-ci-lease.sh' "$process_entry" || fail "process entry lost the repository lease"
grep -Fq 'exec "$script_dir/with-local-ci-lease.sh" "$script_dir/go-service-e2e.sh"' "$service_gate" \
    || fail "focused service gate lost the repository lease"
[ ! -e "$repository_root/scripts/go-fast-stage.sh" ] || fail "obsolete fast-stage helper survives"

node -e '
const scripts = require(process.argv[1]).scripts;
if ((scripts.check.match(/packages:build/g) || []).length !== 1) process.exit(1);
if (!scripts.check.includes("dark-factory-dev typecheck") || !scripts.check.includes("node --test")) process.exit(1);
' "$repository_root/web/package.json" || fail "TypeScript check does not build once before typecheck and tests"

/bin/sh -n "$local_gate" "$ordinary_gate" "$process_gate" "$process_entry" "$service_gate" \
    "$repository_root/scripts/go-gate-environment.sh" \
    "$repository_root/scripts/local-ci-lease.sh" \
    "$repository_root/scripts/with-local-ci-lease.sh" \
    "$repository_root/scripts/test-go-gates.sh" "$0"
echo "local-ci shape tests passed"
