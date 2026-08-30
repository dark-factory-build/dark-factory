#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
local_gate="$repository_root/scripts/local-ci.sh"
ordinary_gate="$repository_root/scripts/go-check.sh"
process_gate="$repository_root/scripts/go-ci-owned.sh"
process_entry="$repository_root/scripts/go-ci.sh"
fail() { echo "local-ci shape test failed: $*" >&2; exit 1; }

set +e
usage_output=$(/bin/sh "$local_gate" obsolete-mode 2>&1)
usage_status=$?
set -e
[ "$usage_status" -eq 2 ] || fail "local-ci accepted an argument"
[ "$usage_output" = "usage: scripts/local-ci.sh" ] || fail "local-ci usage error changed"

ordinary_line=$(grep -n -F 'echo "local-ci: ordinary source gate"' "$local_gate" | cut -d: -f1)
process_line=$(grep -n -F 'echo "local-ci: process-sensitive gate"' "$local_gate" | cut -d: -f1)
service_line=$(grep -n -F 'echo "local-ci: service and release gate"' "$local_gate" | cut -d: -f1)
[ -n "$ordinary_line" ] && [ "$ordinary_line" -lt "$process_line" ] && [ "$process_line" -lt "$service_line" ] \
    || fail "ordinary, process, and service gates are not ordered"

for invocation in './scripts/go-check.sh' '/bin/sh "$script_dir/go-ci-owned.sh"' './scripts/go-service-e2e.sh'; do
    [ "$(grep -Fc "$invocation" "$local_gate")" -eq 1 ] || fail "gate invocation is not unique: $invocation"
done
[ "$(grep -Fc './scripts/test-local-ci-lease.sh' "$local_gate")" -eq 1 ] \
    || fail "local-CI lease proof is not unique"
[ "$(grep -Fc './scripts/test-go-gates.sh' "$local_gate")" -eq 1 ] \
    || fail "fault fixtures are not in the gate"

grep -Fq 'go vet ./...' "$ordinary_gate" || fail "ordinary vet is missing"
grep -Fq 'go test -short -timeout=20m' "$ordinary_gate" || fail "ordinary tests are missing"
grep -Fq 'corepack pnpm run check' "$ordinary_gate" || fail "TypeScript check is missing"
grep -Fq 'git diff --check' "$ordinary_gate" || fail "diff check is missing"
if grep -Eq 'go build|-count=1|-p[[:space:]]+1' "$ordinary_gate"; then
    fail "ordinary gate rebuilds or disables Go caching/parallelism"
fi

grep -Fq 'go test -short -timeout=20m -count=1' "$process_gate" \
    || fail "process tests are not uncached"
if grep -Eq -- '-p[[:space:]]+1' "$process_gate"; then
    fail "process packages are globally serial"
fi
grep -Fq 'with-local-ci-lease.sh' "$process_entry" || fail "process entry lost the repository lease"
[ ! -e "$repository_root/scripts/go-fast-stage.sh" ] || fail "obsolete fast-stage helper survives"

node -e '
const scripts = require(process.argv[1]).scripts;
if ((scripts.check.match(/packages:build/g) || []).length !== 1) process.exit(1);
if (!scripts.check.includes("dark-factory-dev typecheck") || !scripts.check.includes("node --test")) process.exit(1);
' "$repository_root/web/package.json" || fail "TypeScript check does not build once before typecheck and tests"

/bin/sh -n "$local_gate" "$ordinary_gate" "$process_gate" "$process_entry" \
    "$repository_root/scripts/go-gate-environment.sh" "$repository_root/scripts/test-go-gates.sh" "$0"
echo "local-ci shape tests passed"
