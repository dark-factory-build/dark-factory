#!/bin/sh
# Opt-in proof that a release replacement and rollback work against a real,
# randomized, disposable launchd job.
#
# Containment is NOT owned by this script. The job and its private root are
# declared in a durable receipt before anything is bootstrapped, so a kill of
# the target, the fixture, or this coordinator leaves a resumable record. This
# script only resumes that ledger before and after the fixture; a run killed
# between the two is contained by the next invocation, not by a trap or a
# background verifier that would die with its parent.
#
# The installed com.dark-factory.factoryd job and ~/.dark-factory are always
# out of scope and are independently observed before and after.
set -eu

launchctl_bin=${DARK_FACTORY_LAUNCHD_LAUNCHCTL:-/bin/launchctl}
command_timeout=${DARK_FACTORY_LAUNCHD_COMMAND_TIMEOUT:-3}
case "$launchctl_bin" in /*) ;; *) echo 'launchctl path must be absolute' >&2; exit 2 ;; esac
test -x "$launchctl_bin" || { echo "launchctl is not executable: $launchctl_bin" >&2; exit 2; }
case "$command_timeout" in *[!0-9]* | 0) echo 'command timeout must be positive' >&2; exit 2 ;; esac

# Retain and reap the exact direct command child. Status 124 is an internal
# timeout, never a launchctl absence classification.
run_bounded() {
    /usr/bin/perl -MTime::HiRes=time,sleep -MPOSIX=:sys_wait_h -e '
        my ($timeout, @command) = @ARGV;
        my $child = fork();
        defined $child or exit 125;
        if ($child == 0) {
            exec {$command[0]} @command;
            exit 127;
        }
        my $deadline = time() + $timeout;
        while (1) {
            my $waited = waitpid($child, WNOHANG);
            if ($waited == $child) {
                exit(($? & 127) ? 128 + ($? & 127) : $? >> 8);
            }
            exit 125 if $waited == -1;
            last if time() >= $deadline;
            sleep 0.02;
        }
        kill "TERM", $child;
        my $term_deadline = time() + 0.25;
        while (time() < $term_deadline) {
            exit 124 if waitpid($child, WNOHANG) == $child;
            sleep 0.02;
        }
        kill "KILL", $child;
        waitpid($child, 0);
        exit 124;
    ' "$command_timeout" "$@"
}

# Returns 0 only for a present service, 3 only for launchctl's documented
# not-found status, and 4 for every operational or classification failure.
launchctl_print_state() {
    print_service=$1
    print_output=$2
    print_error="$print_output.error"
    print_status=0
    run_bounded "$launchctl_bin" print "$print_service" \
        >"$print_output" 2>"$print_error" || print_status=$?
    if test "$print_status" -eq 0; then
        rm -f "$print_error"
        return 0
    fi
    classification_status=0
    classification=$(run_bounded "$launchctl_bin" error "$print_status" \
        2>>"$print_error") || classification_status=$?
    if test "$print_status" -eq 113 \
        && test "$classification_status" -eq 0 \
        && test "$classification" = '113: Could not find specified service'; then
        rm -f "$print_output" "$print_error"
        return 3
    fi
    echo "launchctl print failed for $print_service (status $print_status)" >&2
    test ! -s "$print_error" || sed 's/^/launchctl: /' "$print_error" >&2
    return 4
}

if test "${DARK_FACTORY_LAUNCHD_PROBE_META_TEST:-0}" = 1; then
    case "${1:-}" in
        --print-state) launchctl_print_state "$2" "$3"; exit $? ;;
        *) echo 'unknown launchd probe meta-test' >&2; exit 2 ;;
    esac
fi

mode=${1:-run}
test "$#" -le 1 || {
    echo 'usage: scripts/macos-launchd-release-proof.sh [--fail-after-second]' >&2
    exit 2
}
case "$mode" in
    run) fail_after_second=0 ;;
    --fail-after-second) fail_after_second=1 ;;
    *) echo "unknown launchd proof mode: $mode" >&2; exit 2 ;;
esac

test "$(uname -s)" = Darwin || {
    echo 'disposable launchd release proof requires macOS' >&2
    exit 2
}

repository_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
target_root=${CARGO_TARGET_DIR:-"$repository_root/target"}
source_dir="$target_root/debug"
for binary in factoryd factory-runner factoryctl factory-tui; do
    test -x "$source_dir/$binary" || {
        echo "missing source-built $source_dir/$binary" >&2
        exit 1
    }
done
source_dir=$(CDPATH='' cd -- "$source_dir" && pwd -P)

operator_home=${HOME:?HOME must identify the operator before fixture isolation}
host_cargo_home=${CARGO_HOME:-"$operator_home/.cargo"}
host_rustup_home=${RUSTUP_HOME:-"$operator_home/.rustup"}
cargo=$(command -v cargo)
cargo_bin=$(dirname "$cargo")
safe_path="$cargo_bin:/usr/bin:/bin:/usr/sbin:/sbin"
for provider in claude codex; do
    if PATH="$safe_path" command -v "$provider" >/dev/null 2>&1; then
        echo "$provider must be absent from the launchd fixture PATH" >&2
        exit 1
    fi
done

# The ledger deliberately outlives one run: it is how a later coordinator
# finds and finalizes a job whose creator was killed.
ledger=${DARK_FACTORY_LAUNCHD_GATE_LEDGER:-"${TMPDIR:-/tmp}/dark-factory-launchd-gate"}
mkdir -p "$ledger"
chmod 700 "$ledger"

uid=$(/usr/bin/id -u)
domain="gui/$uid"
live_service="$domain/com.dark-factory.factoryd"
live_plist="$operator_home/Library/LaunchAgents/com.dark-factory.factoryd.plist"

# The gate owns this entire root, including the fixture's evidence. Nothing
# here is removed by this script, so a kill cannot strand a half-cleaned tree.
root=$(mktemp -d /tmp/df-launchd-release.XXXXXX)
chmod 700 "$root"
mkdir "$root/user-home" "$root/factory-home" "$root/tmp" "$root/evidence"
chmod 700 "$root/user-home" "$root/factory-home" "$root/tmp" "$root/evidence"
suffix=$(basename "$root" | tr -cd '[:alnum:]')
label="com.dark-factory.fixture.$suffix"
# Snapshots must outlive the root the gate deletes.
observations=$(mktemp -d /tmp/df-launchd-observed.XXXXXX)
chmod 700 "$observations"

snapshot_live_install() {
    destination=$1
    live_status=0
    launchctl_print_state "$live_service" "$destination.launchctl" || live_status=$?
    case "$live_status" in
        0)
            # The pid is deliberately excluded: the operator's own daemon may
            # legitimately restart mid-fixture. Remaining loaded from the same
            # plist and program is what proves this fixture never touched it.
            {
                echo loaded
                sed -n -e '/^[[:space:]]*path = /p' \
                    -e '/^[[:space:]]*program = /p' "$destination.launchctl"
            } >"$destination.job"
            ;;
        3) echo absent >"$destination.job" ;;
        *) return 1 ;;
    esac
    if test -L "$live_plist"; then
        printf 'symlink %s\n' "$(readlink "$live_plist")" >"$destination.plist"
    elif test -f "$live_plist"; then
        /usr/bin/stat -f 'file %d:%i %Sp %z' "$live_plist" >"$destination.plist"
        /usr/bin/shasum -a 256 "$live_plist" >>"$destination.plist"
    elif test -e "$live_plist"; then
        /usr/bin/stat -f 'other %d:%i %HT %Sp %z' "$live_plist" >"$destination.plist"
    else
        echo absent >"$destination.plist"
    fi
    rm -f "$destination.launchctl"
}

fixture_cargo() {
    /usr/bin/env \
        HOME="$root/user-home" \
        DARK_FACTORY_HOME="$root/factory-home" \
        DARK_FACTORY_LAUNCHD_RELEASE_PROOF=1 \
        DARK_FACTORY_LAUNCHD_FIXTURE_ROOT="$root" \
        DARK_FACTORY_LAUNCHD_EVIDENCE="$root/evidence" \
        DARK_FACTORY_LAUNCHD_GATE_LEDGER="$ledger" \
        DARK_FACTORY_LAUNCHD_LAUNCHCTL="$launchctl_bin" \
        DARK_FACTORY_LAUNCHD_LABEL="$label" \
        DARK_FACTORY_LAUNCHD_SOURCE_DIR="$source_dir" \
        DARK_FACTORY_LAUNCHD_SAFE_PATH="$safe_path" \
        DARK_FACTORY_LAUNCHD_FAIL_AFTER_SECOND="$fail_after_second" \
        CARGO_HOME="$host_cargo_home" \
        RUSTUP_HOME="$host_rustup_home" \
        PATH="$safe_path" \
        "$cargo" +1.88.0 test --locked -p factoryctl --test launchd_release \
            "$1" -- --ignored --exact --test-threads=1
}

snapshot_live_install "$observations/live-before" || {
    echo "could not observe the installed launchd job" >&2
    exit 1
}

# Contain anything an earlier killed coordinator left behind before adding to
# the ledger. A failure here is visible and stops the run.
fixture_cargo disposable_launchd_jobs_are_resumed || {
    echo 'could not finalize launchd jobs left by an earlier run' >&2
    exit 1
}

test_status=0
fixture_cargo disposable_launchd_release_replacement || test_status=$?

# Read the evidence while the root still exists. `cargo test` exits 0 when a
# filter matches nothing, so these markers are what prove the phases ran.
if test "$test_status" -eq 0; then
    for marker in first-live second-live crash-observed rollback-live; do
        test -f "$root/evidence/$marker" || {
            echo "launchd proof omitted $marker" >&2
            test_status=1
        }
    done
fi
# Keep the daemon logs of a failed run; the root itself is the gate's to remove.
if test "$test_status" -ne 0 && test -d "$root/factory-home/logs"; then
    cp -R "$root/factory-home/logs" "$observations/factory-logs" 2>/dev/null || true
    echo "preserved failed-run logs: $observations/factory-logs" >&2
fi

# The one finalization authority. Success and hard death converge here.
resume_status=0
fixture_cargo disposable_launchd_jobs_are_resumed || resume_status=$?
# A root is only ours to remove when it was never declared, and that needs two
# independent facts: no claim marker, and no receipt naming this run's label.
# The marker alone is not enough — removing a root is not atomic, so a
# finalization that unlinked the marker and then failed leaves a root the gate
# deliberately retained looking exactly like one that was never declared.
if test -e "$root"; then
    if test -e "$root/.dark-factory-launchd-gate" || test -e "$ledger/$label.json"; then
        echo "launchd gate did not remove the private root it claimed: $root" >&2
        resume_status=1
    else
        rm -rf -- "$root"
        echo 'the fixture failed before declaring its job; no launchd job was loaded' >&2
    fi
fi

snapshot_live_install "$observations/live-after" || resume_status=1
cmp -s "$observations/live-before.job" "$observations/live-after.job" || {
    echo 'the installed launchd job changed during the fixture' >&2
    resume_status=1
}
cmp -s "$observations/live-before.plist" "$observations/live-after.plist" || {
    echo 'the installed launchd plist changed during the fixture' >&2
    resume_status=1
}

if test "$resume_status" -ne 0; then
    echo "preserving launchd observations: $observations" >&2
    test "$test_status" -ne 0 || test_status=1
elif test "$test_status" -eq 0; then
    rm -rf -- "$observations"
    echo 'disposable launchd release proof passed with exact external teardown'
fi
exit "$test_status"
