#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
gate="$repository_root/scripts/local-ci.sh"
contributing="$repository_root/CONTRIBUTING.md"
workflow_doc="$repository_root/docs/development/WORKFLOW.md"
ci="$repository_root/.github/workflows/ci.yml"
stale_control_plane_workflow="$repository_root/control-plane/.github/workflows/ci.yml"

fail() {
    echo "local-ci shape test failed: $*" >&2
    exit 1
}

# The gate is single-mode: the Rust workspace it once branched for is deleted,
# so a mode argument must be refused rather than silently ignored.
grep -Fq 'usage: scripts/local-ci.sh' "$gate" || fail "gate lost its usage guard"
set +e
usage_output=$("$gate" obsolete-mode 2>&1)
usage_status=$?
set -e
[ "$usage_status" -eq 2 ] || fail "gate accepted an obsolete mode argument"
[ "$usage_output" = "usage: scripts/local-ci.sh" ] \
    || fail "gate did not reject an argument with the exact usage error"
if grep -Eq 'legacy-rust|linux-source' "$gate"; then
    fail "gate still branches on a retired Rust mode"
fi
if grep -Fq 'cargo ' "$gate"; then
    fail "gate still runs cargo"
fi

# This is a text-shape guard, not an execution guard: it catches a deleted or
# commented-out invocation, but an `if false; then ... fi` wrapper would still
# match. Tightened to the invocation form so a bare mention cannot satisfy it.
grep -Fq '/bin/sh "$script_dir/go-ci-owned.sh"' "$gate" \
    || fail "gate lost the authoritative Go gate invocation line"
grep -Fq 'git diff --check' "$gate" || fail "gate lost its diff check"

for fixture in test-local-ci-lease.sh test-local-ci-lease-mutations.sh \
    check-toolchain-pins.sh test-prepare-release-source.sh \
    test-publish-release.sh test-package-release.sh \
    test-local-ci-environment.sh test-new-worktree.sh \
    test-github-step-summary.sh test-verify-adversarial-review.sh \
    test-inline-chokepoint.sh test-go-e2e-tools.sh test-cloudflare-env.sh \
    test-repository-settings.sh \
    test-local-ci-mode.sh; do
    grep -Fq "./scripts/$fixture" "$gate" || fail "gate lost fixture $fixture"
done

# Every retired Rust script must be gone, not merely unreferenced, so a future
# edit cannot resurrect a stage whose subject no longer exists.
for retired in macos-contributor-smoke.sh test-macos-contributor-smoke.sh \
    macos-smoke-daemon-owner.pl linux-contributor-smoke.sh \
    test-linux-contributor-smoke.sh macos-launchd-release-proof.sh \
    test-macos-launchd-release-proof.sh check-build-headroom.sh \
    test-build-headroom.sh; do
    [ ! -e "$repository_root/scripts/$retired" ] \
        || fail "retired Rust-era script survives: $retired"
done
[ ! -e "$repository_root/launchd/com.dark-factory.factoryd.plist.template" ] \
    || fail "retired Rust launchd template survives"
if grep -Eq 'TUI|shared Rust operation|factory-tui|area:tui' \
    "$repository_root/.github/ISSUE_TEMPLATE/bug_report.yml" \
    "$repository_root/.github/ISSUE_TEMPLATE/feature_request.yml"; then
    fail "issue templates still direct contributors to the retired Rust/TUI product"
fi

e2e_tool_fixture_count=$(grep -Ec '^[[:space:]]*\./scripts/test-go-e2e-tools\.sh[[:space:]]*$' "$gate" || true)
[ "$e2e_tool_fixture_count" -eq 1 ] \
    || fail "gate must run the Go E2E tool fixture exactly once"
e2e_tool_fixture_line=$(grep -n -E '^[[:space:]]*\./scripts/test-go-e2e-tools\.sh[[:space:]]*$' "$gate" \
    | head -1 | cut -d: -f1)
owned_gate_line=$(grep -n -F '/bin/sh "$script_dir/go-ci-owned.sh"' "$gate" \
    | head -1 | cut -d: -f1)
[ -n "$e2e_tool_fixture_line" ] || fail "gate lost the Go E2E tool fixture"
[ -n "$owned_gate_line" ] && [ "$e2e_tool_fixture_line" -lt "$owned_gate_line" ] \
    || fail "Go E2E tool fixture does not run before the heavy Go gate"

# CI must not keep a job whose only subject was the deleted workspace, and the
# required aggregate must not name one: a dangling dependency blocks every PR.
if grep -Eq '^  (legacy-rust|linux):' "$ci"; then
    fail "CI still defines a job for the deleted Rust workspace"
fi
if grep -Eq 'needs\.(legacy-rust|linux)\.result|needs: \[.*(legacy-rust|linux)' "$ci"; then
    fail "the required aggregate still depends on a deleted job"
fi
# The Rust-toolchain-absence scan lives in check-toolchain-pins.sh, which the
# gate runs, so it is not duplicated here: two copies of one guard drift, and
# the pins script is the toolchain authority.

control_plane_job=$(sed -n '/^  control-plane:/,/^  [a-z]/p' "$ci")
control_line_of() {
    printf '%s\n' "$control_plane_job" | grep -n -F "$1" | head -1 | cut -d: -f1
}
node_runtime_line=$(control_line_of 'name: Verify the supported Node runtime')
control_gate_line=$(control_line_of 'run: ./control-plane/scripts/local-ci.sh')
[ -n "$node_runtime_line" ] && [ -n "$control_gate_line" ] \
    || fail "hosted control-plane CI lost its Node runtime check or gate"
[ "$node_runtime_line" -lt "$control_gate_line" ] \
    || fail "hosted control-plane CI runs its gate before checking Node"
[ ! -e "$stale_control_plane_workflow" ] \
    || fail "control-plane gate remains hidden in an undiscovered nested workflow"

grep -Fxq './scripts/local-ci.sh' "$contributing" \
    || fail "CONTRIBUTING lost the gate command"
if grep -Eq 'linux-source|linux-contributor-smoke' "$contributing"; then
    fail "CONTRIBUTING still documents a retired Rust gate mode"
fi
grep -Fq 'there is no Linux runtime lane yet' "$workflow_doc" \
    || fail "workflow does not state the current Darwin-only CI boundary"
if grep -Fq 'Linux source-only lane' "$workflow_doc"; then
    fail "workflow still claims the retired Linux source lane"
fi

sh -n "$gate"
echo "local-ci shape tests passed"
