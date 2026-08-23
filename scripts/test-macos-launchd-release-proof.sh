#!/bin/sh
# Static and meta tests for the disposable launchd proof coordinator.
#
# Containment itself is proved by `cargo test -p factoryctl --test launchd_gate`
# against a fake launchctl on every platform. What is checked here is the part
# that cannot be: that this coordinator keeps the installed service out of
# scope, resumes the durable ledger around the fixture, and never becomes a
# second teardown authority of its own.
# shellcheck disable=SC2016 # grep patterns intentionally contain shell syntax
set -eu

repository_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
runner="$repository_root/scripts/macos-launchd-release-proof.sh"
test_source="$repository_root/crates/factoryctl/tests/launchd_release.rs"
gate_source="$repository_root/crates/factoryctl/tests/launchd_gate.rs"
workflow="$repository_root/.github/workflows/ci.yml"
gate="$repository_root/scripts/local-ci.sh"

fail() {
    echo "macOS launchd release proof static test failed: $*" >&2
    exit 1
}

meta_root=$(mktemp -d "${TMPDIR:-/tmp}/df-launchd-probes.XXXXXX")
trap 'rm -rf -- "$meta_root"' EXIT HUP INT TERM
fake_launchctl="$meta_root/launchctl"
printf '%s\n' \
    '#!/bin/sh' \
    'case "$1" in' \
    '  print) exit "${FAKE_PRINT_STATUS:-113}" ;;' \
    '  error) printf "%s\n" "${FAKE_ERROR_TEXT:-113: Could not find specified service}" ;;' \
    '  *) exit 64 ;;' \
    'esac' >"$fake_launchctl"
chmod 700 "$fake_launchctl"

sh -n "$runner"

# The installed service stays observable but untouchable.
grep -Fq "test \"\$classification\" = '113: Could not find specified service'" "$runner" \
    || fail 'launchctl absence is not tied to the documented classification'
grep -Fq 'label="com.dark-factory.fixture.$suffix"' "$runner" \
    || fail 'runner lost its randomized disposable label'
grep -Fq 'live_service="$domain/com.dark-factory.factoryd"' "$runner" \
    || fail 'coordinator no longer observes the installed label'
grep -Fq 'cmp -s "$observations/live-before.job" "$observations/live-after.job"' "$runner" \
    || fail 'installed job identity is not compared'
grep -Fq 'cmp -s "$observations/live-before.plist" "$observations/live-after.plist"' "$runner" \
    || fail 'installed plist identity is not compared'
grep -Fq 'DARK_FACTORY_LAUNCHD_SAFE_PATH' "$runner" \
    || fail 'provider-absent PATH is not explicit'

# Teardown authority belongs to the durable gate, not to this script. Only
# executable lines matter; the header comment necessarily names these verbs.
runner_code=$(grep -v '^[[:space:]]*#' "$runner")
if printf '%s\n' "$runner_code" | grep -Eq 'bootout|kickstart|bootstrap'; then
    fail 'the coordinator must never be a second launchd teardown authority'
fi
if printf '%s\n' "$runner_code" | grep -Fq 'mkfifo'; then
    fail 'containment regressed to a background verifier that dies with its parent'
fi
if printf '%s\n' "$runner_code" | grep -Eq 'trap .*(EXIT|TERM|HUP|INT)'; then
    fail 'containment regressed to a trap owned by the killed process'
fi
test "$(grep -c 'fixture_cargo disposable_launchd_jobs_are_resumed' "$runner")" -ge 2 \
    || fail 'the ledger is not resumed both before and after the fixture'
grep -Fq 'ledger=${DARK_FACTORY_LAUNCHD_GATE_LEDGER:-' "$runner" \
    || fail 'the ledger does not outlive one run'

# The script must never delete the gate-owned root itself.
if printf '%s\n' "$runner_code" | grep -Eq 'rm -rf.*\$root|rm -rf -- "\$root"'; then
    fail 'the coordinator deletes the gate-owned root instead of resuming it'
fi
grep -Fq 'test ! -e "$root" ||' "$runner" \
    || fail 'the coordinator does not verify the gate removed its root'
grep -Fq 'DARK_FACTORY_LAUNCHD_EVIDENCE' "$test_source" \
    || fail 'the fixture no longer records phase evidence'

meta_status=0
env DARK_FACTORY_LAUNCHD_PROBE_META_TEST=1 \
    DARK_FACTORY_LAUNCHD_LAUNCHCTL="$fake_launchctl" \
    "$runner" --print-state gui/0/missing "$meta_root/missing" || meta_status=$?
test "$meta_status" -eq 3 || fail 'documented launchctl not-found was not absence'

meta_status=0
env DARK_FACTORY_LAUNCHD_PROBE_META_TEST=1 \
    DARK_FACTORY_LAUNCHD_LAUNCHCTL="$fake_launchctl" \
    FAKE_ERROR_TEXT='113: injected ambiguous failure' \
    "$runner" --print-state gui/0/ambiguous "$meta_root/ambiguous" || meta_status=$?
test "$meta_status" -eq 4 || fail 'status 113 without exact classification became absence'

meta_status=0
env DARK_FACTORY_LAUNCHD_PROBE_META_TEST=1 \
    DARK_FACTORY_LAUNCHD_LAUNCHCTL="$fake_launchctl" \
    FAKE_PRINT_STATUS=77 FAKE_ERROR_TEXT='77: Operation not permitted' \
    "$runner" --print-state gui/0/denied "$meta_root/denied" || meta_status=$?
test "$meta_status" -eq 4 || fail 'an operational launchctl failure became absence'
test -f "$meta_root/denied.error" \
    || fail 'operational launchctl diagnostics were discarded'

# The real fixture stays opt-in and declares its job before it is loaded.
grep -Fq '#[ignore = "opt-in: loads a randomized disposable launchd job"]' "$test_source" \
    || fail 'real launchd test is not opt-in'
grep -Fq 'LaunchdTarget::new(' "$test_source" \
    || fail 'test no longer injects an explicit launchd identity'
grep -Fq 'rollback_after_health_failure_for(' "$test_source" \
    || fail 'test no longer exercises the targeted rollback seam'
grep -Fq 'LaunchdGateInvocation::open(' "$test_source" \
    || fail 'the disposable job is not declared in the durable ledger'
if grep -Fq 'invocation.finalize()' "$test_source"; then
    fail 'the fixture added a second teardown path that only success exercises'
fi

# The portable containment proofs must keep covering the dangerous cases.
for proof in \
    a_declared_job_is_resumable_before_it_is_ever_bootstrapped \
    a_resumed_receipt_boots_out_exactly_its_own_label \
    a_lost_bootout_response_does_not_send_a_second_teardown \
    a_sigkilled_coordinator_leaves_exactly_one_resumable_invocation \
    a_receipt_cannot_aim_cleanup_at_a_directory_the_gate_never_claimed \
    the_installed_label_is_refused_at_declaration_and_at_finalization
do
    grep -Fq "fn $proof(" "$gate_source" || fail "containment proof $proof was removed"
done

grep -Fq './scripts/test-macos-launchd-release-proof.sh' "$gate" \
    || fail 'local source gate lost the launchd safety checks'
macos_job=$(sed -n '/^  checks:/,/^  required:/p' "$workflow")
printf '%s\n' "$macos_job" | grep -Fq './scripts/macos-launchd-release-proof.sh' \
    || fail 'hosted macOS no longer runs the real launchd proof'
linux_job=$(sed -n '/^  linux:/,$p' "$workflow")
if printf '%s\n' "$linux_job" | grep -Fq './scripts/macos-launchd-release-proof.sh'; then
    fail 'Linux workflow invokes real launchctl proof'
fi

echo 'macOS launchd release proof static tests passed'
