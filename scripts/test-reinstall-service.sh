#!/bin/sh
set -eu

# The reinstall script replaces the live service. Everything here runs against
# a fixture repository, a temporary HOME and fake go, sqlite3 and factoryctl
# binaries, so no case builds, backs up, or touches launchd.

repository_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
# Under /private/tmp like the package smoke: the fake daemon's socket path
# must fit a Unix socket address, which a deep TMPDIR does not.
temporary=$(mktemp -d /private/tmp/dark-factory-reinstall-service-test.XXXXXX)
trap 'kill $(cat "$temporary/pids" 2>/dev/null) 2>/dev/null || true; rm -rf "$temporary"' EXIT
trap 'exit 1' HUP INT TERM

fail() {
    echo "reinstall-service test failed: $*" >&2
    exit 1
}

# The default macOS umask: the fixture store and anything the script creates
# without guarding against it are world-readable.
umask 022
# Compiler execution is fake; no host CI lease is acquired by these fixtures.
export DARK_FACTORY_LOCAL_CI_LEASE_HELD=1
test_repository=$temporary/repository
fake_bin=$temporary/fake-bin
fake_home=$temporary/home
mkdir -p "$test_repository/scripts" "$fake_bin" "$fake_home/.dark-factory"
cp "$repository_root/scripts/reinstall-service.sh" "$test_repository/scripts/reinstall-service.sh"
cp "$repository_root/scripts/verification-profile.mjs" "$test_repository/scripts/verification-profile.mjs"
for controller_asset in \
    cold-review.sh \
    factory-autonomy.py \
    factory-delivery.py \
    factory-intake.py \
    factory-publication.py \
    factory-release.py \
    factory-review-intake.py \
    go-gate-environment.sh \
    verify-adversarial-review.sh \
    supervision.md
do
    cp "$repository_root/scripts/$controller_asset" "$test_repository/scripts/$controller_asset"
done

git -C "$test_repository" init -q -b main
git -C "$test_repository" config user.name fixture
git -C "$test_repository" config user.email fixture@example.invalid
printf 'fixture\n' >"$test_repository/README.md"
printf 'module fixture\n\ngo 1.2.3\n' >"$test_repository/go.mod"
printf '0.3.5\n' >"$test_repository/VERSION"
git -C "$test_repository" add README.md go.mod VERSION scripts
git -C "$test_repository" commit -q -m fixture
git clone -q --bare "$test_repository" "$temporary/origin.git"
git -C "$test_repository" remote add origin "$temporary/origin.git"
sha=$(git -C "$test_repository" rev-parse HEAD)
# A hook the repository configures, as test-new-worktree.sh: it must not run
# when the script adds its worktree.
mkdir -p "$temporary/configured-hooks"
printf '#!/bin/sh\n: >"%s"\n' "$temporary/post-checkout-ran" >"$temporary/configured-hooks/post-checkout"
chmod 700 "$temporary/configured-hooks/post-checkout"
git -C "$test_repository" config core.hooksPath "$temporary/configured-hooks"

printf 'live store\n' >"$fake_home/.dark-factory/factory.sqlite3"
printf '\n' >"$fake_home/.dark-factory/home.lock"
mkdir -p "$fake_home/.dark-factory.service"
printf '{"label":"com.dark-factory.fixture","plist_path":"/private/tmp/fixture-plists/com.dark-factory.fixture.plist","relay_origin":"wss://relay.example"}\n' >"$fake_home/.dark-factory.service/receipt"
printf '0\n' >"$temporary/dispatch-enabled"
: >"$temporary/active-runs"
: >"$temporary/pids"

# go build writes a stub that records the build tree's HEAD, its supplied VCS
# metadata and the toolchain pins it was given, and execs the fake factoryctl;
# go version -m prints the stub so the vcs.* checks see it. With
# DARK_FACTORY_TEST_ADMIT_DURING_BUILD set, the build also admits a run, the
# way the supervisor can while a real build takes minutes.
cat >"$fake_bin/go" <<'FAKE'
#!/bin/sh
set -eu
case "$1" in
    build)
        printf '%s\n' "$*" >>"$DARK_FACTORY_TEST_GO_LOG"
        out=""
        while [ $# -gt 0 ]; do
            [ "$1" = -o ] && out=$2
            shift
        done
        printf '#!/bin/sh\n# vcs.revision=%s\n# vcs.modified=%s\n# pins GOTOOLCHAIN=%s GOENV=%s GOAUTH=%s\nexec factoryctl "$@"\n' \
            "${DARK_FACTORY_TEST_VCS_REVISION-$(git rev-parse HEAD)}" "${DARK_FACTORY_TEST_VCS_MODIFIED-false}" \
            "${GOTOOLCHAIN-unset}" "${GOENV-unset}" "${GOAUTH-unset}" >"$out"
        chmod 755 "$out"
        [ -z "${DARK_FACTORY_TEST_ADMIT_DURING_BUILD-}" ] || printf 'admitted:%s\n' "$DARK_FACTORY_TEST_RUN" >"$DARK_FACTORY_TEST_ACTIVE_RUNS"
        [ -z "${DARK_FACTORY_TEST_ENABLE_DISPATCH_DURING_BUILD-}" ] || printf '1\n' >"$DARK_FACTORY_TEST_DISPATCH_ENABLED"
        ;;
    env) case "$2" in GOOS) echo darwin ;; GOARCH) echo arm64 ;; esac ;;
    run) printf '%s|%s|%s|fixture-id\n' "$4" "$5" "$6" ;;
    version) cat "$3" ;;
    *) exit 1 ;;
esac
FAKE
cat >"$fake_bin/sqlite3" <<'FAKE'
#!/bin/sh
set -eu
case "$2" in
    "SELECT dispatch_enabled FROM factory WHERE singleton = 1") cat "$DARK_FACTORY_TEST_DISPATCH_ENABLED" ;;
    "SELECT phase || ':' || lower(hex(id)) FROM runs WHERE phase <> 'terminal'") cat "$DARK_FACTORY_TEST_ACTIVE_RUNS" ;;
    ".backup "*)
        cp "$1" "${2#.backup }"
        # The modes while the store is being copied, before any later chmod.
        stat -f %Lp "$HOME/.dark-factory-backups" "$(dirname "${2#.backup }")" "${2#.backup }" \
            >"$DARK_FACTORY_TEST_BACKUP_MODES"
        ;;
    "PRAGMA user_version") echo 7 ;;
    *) exit 1 ;;
esac
FAKE
# The daemon's socket lifetime: service install returns at once, and only
# after a real /bin/sleep (the store opening and migrating) does the listener
# remove a stale socket, take the home lock and bind, the way launchd returns
# before factoryd does; service uninstall stops it and removes the socket. web
# status and remote status dial the socket the real client is pointed at. Two
# knobs select an unclean exit: DARK_FACTORY_TEST_UNINSTALL_LEAVES=stale keeps
# the socket file with nothing answering (a SIGKILLed daemon), =listening keeps
# the listener itself, =lock keeps just the lifetime home lock;
# DARK_FACTORY_TEST_INSTALL_DEAD installs a daemon that never listens.
cat >"$fake_bin/factoryctl" <<'FAKE'
#!/bin/sh
set -eu
if [ "${1-}" = --build-identity ]; then
    printf '{"version":"0.3.5","source":"%s","target":"darwin/arm64","build_id":"%s","release":true}\n' "$DARK_FACTORY_TEST_SOURCE" "${DARK_FACTORY_TEST_BUILD_ID-fixture-id}"
    exit 0
fi
echo "$*" >>"$DARK_FACTORY_TEST_FACTORYCTL_LOG"
factory_home=$HOME/.dark-factory
previous=
for argument in "$@"; do
    [ "$previous" != --home ] || factory_home=$argument
    previous=$argument
done
runtimes=$factory_home/runtimes
stop() {
    kill $(cat "$DARK_FACTORY_TEST_PIDS") 2>/dev/null || true
    : >"$DARK_FACTORY_TEST_PIDS"
}
case "$1 $2" in
    "service uninstall")
        [ ! -e "$factory_home/verification-browser" ] || exit 1
        case "${DARK_FACTORY_TEST_UNINSTALL_LEAVES-}" in
            stale) stop ;;
            listening) ;;
            lock)
                stop
                rm -f "$runtimes/factory.sock"
                perl -MFcntl=:flock -e 'open my $lock, "+<", shift or die "$!\\n"; flock $lock, LOCK_EX or die "$!\\n"; sleep 1000' \
                    "$factory_home/home.lock" &
                echo $! >>"$DARK_FACTORY_TEST_PIDS"
                ;;
            *) stop; rm -f "$runtimes/factory.sock" ;;
        esac
        ;;
    "service install")
        previous=""
        for argument in "$@"; do
            if [ "$previous" = "--relay-origin" ] && [ -z "$argument" ]; then
                exit 64
            fi
            previous=$argument
        done
        mkdir -p "$runtimes"
        [ -n "${DARK_FACTORY_TEST_INSTALL_DEAD-}" ] || {
            (cd "$runtimes" && /bin/sleep 0.2 && rm -f factory.sock && exec perl -MFcntl=:flock -MSocket -MIO::Socket::UNIX -e 'open my $lock, "+<", shift or die "$!\\n"; flock $lock, LOCK_EX or die "$!\\n"; my $s = IO::Socket::UNIX->new(Type => SOCK_STREAM, Local => "factory.sock", Listen => 1) or die "$!\\n"; while (my $c = $s->accept) { close $c }' "$factory_home/home.lock") &
            echo $! >>"$DARK_FACTORY_TEST_PIDS"
        }
        ;;
    "web status" | "remote status")
        perl -MIO::Socket::UNIX -e 'exit !IO::Socket::UNIX->new(Peer => shift)' "$DARK_FACTORY_SOCKET"
        ;;
esac
FAKE
# The script's bounded waits poll 60 times with sleep between. A no-op sleep
# makes a timeout case take about a second (macOS stretches short real sleeps
# to well over 100 ms), and the probe each poll spawns still outlasts the fake
# listener's 200 ms of startup.
printf '#!/bin/sh\n' >"$fake_bin/sleep"
chmod 755 "$fake_bin"/*
export PATH="$fake_bin:$PATH" HOME="$fake_home"
export DARK_FACTORY_TEST_ACTIVE_RUNS="$temporary/active-runs"
# One fixture run id, the 32 lowercase hex characters a runtime directory is
# named after.
export DARK_FACTORY_TEST_RUN=0123456789abcdef0123456789abcdef
export DARK_FACTORY_TEST_DISPATCH_ENABLED="$temporary/dispatch-enabled"
export DARK_FACTORY_TEST_BACKUP_MODES="$temporary/backup-modes"
export DARK_FACTORY_TEST_FACTORYCTL_LOG="$temporary/factoryctl.log"
export DARK_FACTORY_TEST_GO_LOG="$temporary/go.log"
export DARK_FACTORY_TEST_SOURCE="$sha"
export DARK_FACTORY_TEST_PIDS="$temporary/pids"
script=$test_repository/scripts/reinstall-service.sh

backups() {
    [ -d "$fake_home/.dark-factory-backups" ] || { echo 0; return; }
    find "$fake_home/.dark-factory-backups" -name factory.sqlite3 | wc -l | tr -d ' '
}
untouched() {
    [ "$(backups)" = 0 ] || fail "$1: backup taken"
    [ ! -e "$DARK_FACTORY_TEST_FACTORYCTL_LOG" ] || fail "$1: factoryctl invoked"
}
not_installed() {
    grep -q '^service install' "$DARK_FACTORY_TEST_FACTORYCTL_LOG" && fail "$1: service installed anyway"
    rm "$DARK_FACTORY_TEST_FACTORYCTL_LOG"
}
no_service_change() {
    [ ! -e "$DARK_FACTORY_TEST_FACTORYCTL_LOG" ] || fail "$1: factoryctl invoked"
}
# A real takeover.sock in the given runtime directory. It is bound under a
# short path and moved into place: sockaddr_un's 104-byte sun_path cannot
# reach this fixture's own temporary tree.
fake_takeover_socket() {
    mkdir -p "$1"
    stage=$(mktemp -d /private/tmp/df-sock.XXXXXX)
    perl -MIO::Socket::UNIX -e 'IO::Socket::UNIX->new(Local => shift, Listen => 1) or die' "$stage/s"
    mv "$stage/s" "$1/takeover.sock"
    rmdir "$stage"
}

"$script" >/dev/null 2>"$temporary/stderr" && fail "no argument accepted"
grep -q '^usage:' "$temporary/stderr" || fail "no argument: usage not printed"
"$script" not-a-sha >/dev/null 2>"$temporary/stderr" && fail "bad argument accepted"
grep -q '^usage:' "$temporary/stderr" || fail "bad argument: usage not printed"
[ ! -e "$test_repository/.worktrees" ] || fail "usage error created a worktree"
untouched "usage error"

printf '1\n' >"$temporary/dispatch-enabled"
"$script" "$sha" >/dev/null 2>"$temporary/stderr" && fail "dispatch enabled accepted"
grep -q 'dispatch is enabled' "$temporary/stderr" || fail "dispatch enabled: wrong refusal"
[ ! -e "$test_repository/.worktrees" ] || fail "dispatch enabled created a worktree"
untouched "dispatch enabled"
printf '0\n' >"$temporary/dispatch-enabled"

printf 'running:%s\n' "$DARK_FACTORY_TEST_RUN" >"$temporary/active-runs"
"$script" "$sha" >/dev/null 2>"$temporary/stderr" && fail "active run accepted"
grep -q 'non-terminal run' "$temporary/stderr" || fail "active run: wrong refusal"
[ ! -e "$test_repository/.worktrees" ] || fail "active run created a worktree"
untouched "active run"

# The same running run, still publishing its runner's takeover endpoint, is
# adopted by the next daemon: it does not have to drain.
adoptable_runtime="$fake_home/.dark-factory/runtimes/$DARK_FACTORY_TEST_RUN"
fake_takeover_socket "$adoptable_runtime"
"$script" "$sha" >/dev/null 2>"$temporary/stderr" \
    || fail "adoptable running run refused: $(cat "$temporary/stderr")"
grep -q "adoptable: run $DARK_FACTORY_TEST_RUN" "$temporary/stderr" || fail "adoptable run: not reported"
rm -rf "$fake_home/.dark-factory/runtimes" "$DARK_FACTORY_TEST_FACTORYCTL_LOG" "$fake_home/.dark-factory-backups"

# A finalizing run behind the same endpoint still blocks: only a running one
# is adoptable.
printf 'finalizing:%s\n' "$DARK_FACTORY_TEST_RUN" >"$temporary/active-runs"
fake_takeover_socket "$adoptable_runtime"
"$script" "$sha" >/dev/null 2>"$temporary/stderr" && fail "finalizing run with an endpoint accepted"
grep -q 'non-terminal run' "$temporary/stderr" || fail "finalizing run: wrong refusal"
rm -rf "$fake_home/.dark-factory/runtimes"
: >"$temporary/active-runs"

DARK_FACTORY_TEST_ENABLE_DISPATCH_DURING_BUILD=1 "$script" "$sha" >/dev/null 2>"$temporary/stderr" \
    && fail "dispatch enabled during build accepted"
grep -q 'dispatch is enabled' "$temporary/stderr" || fail "dispatch enabled during build: wrong refusal"
[ ! -e "$DARK_FACTORY_TEST_FACTORYCTL_LOG" ] || fail "dispatch enabled during build: service uninstalled"
printf '0\n' >"$temporary/dispatch-enabled"

DARK_FACTORY_TEST_ADMIT_DURING_BUILD=1 "$script" "$sha" >/dev/null 2>"$temporary/stderr" \
    && fail "run admitted during the build accepted"
grep -q 'non-terminal run' "$temporary/stderr" || fail "run admitted during the build: wrong refusal"
[ ! -e "$DARK_FACTORY_TEST_FACTORYCTL_LOG" ] || fail "run admitted during the build: service uninstalled"
: >"$temporary/active-runs"
rm -rf "$fake_home/.dark-factory-backups"

# A legacy browser session must leave the strict runtime home before the
# service lifecycle starts, without changing its contents.
legacy_profile="$fake_home/.dark-factory/verification-browser"
mkdir -p "$legacy_profile"
printf 'paired session\n' >"$legacy_profile/session"
DARK_FACTORY_TEST_INSTALL_DEAD=1 "$script" "$sha" >"$temporary/stdout" 2>"$temporary/stderr" \
    && fail "legacy browser profile accepted a dead service"
grep -q 'did not listen' "$temporary/stderr" || fail "legacy browser profile: reinstall did not reach service lifecycle"
[ ! -e "$legacy_profile" ] || fail "legacy browser profile remained in runtime home"
grep -qx 'paired session' "$fake_home/.dark-factory-verification-browser/session" \
    || fail "legacy browser profile was not preserved"
rm "$DARK_FACTORY_TEST_FACTORYCTL_LOG"
rm -rf "$fake_home/.dark-factory-backups"

# A symlinked runtime home must be refused before a service lifecycle action.
unsafe_home="$temporary/unsafe-home"
mkdir -p "$unsafe_home/real-home"
printf 'live store\n' >"$unsafe_home/real-home/factory.sqlite3"
mkdir -p "$unsafe_home/real-home/verification-browser" "$unsafe_home/.dark-factory-verification-browser"
printf 'legacy session\n' >"$unsafe_home/real-home/verification-browser/session"
printf 'current session\n' >"$unsafe_home/.dark-factory-verification-browser/session"
ln -s real-home "$unsafe_home/.dark-factory"
HOME="$unsafe_home" "$script" "$sha" >/dev/null 2>"$temporary/stderr" && fail "symlinked runtime home accepted"
grep -q 'operator-owned directory' "$temporary/stderr" || fail "symlinked runtime home: wrong refusal"
grep -qx 'legacy session' "$unsafe_home/real-home/verification-browser/session" \
    || fail "symlinked runtime home: legacy profile changed"
grep -qx 'current session' "$unsafe_home/.dark-factory-verification-browser/session" \
    || fail "symlinked runtime home: current profile changed"
no_service_change "symlinked runtime home"

"$script" "$sha" >"$temporary/stdout" || fail "clean reinstall exited non-zero"
backup=$(find "$fake_home/.dark-factory-backups" -name factory.sqlite3)
case "$backup" in
    "$fake_home/.dark-factory-backups/"[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]T[0-9][0-9][0-9][0-9][0-9][0-9]-"$sha".??????/factory.sqlite3) ;;
    *) fail "unexpected backup path: $backup" ;;
esac
cmp -s "$backup" "$fake_home/.dark-factory/factory.sqlite3" || fail "backup content differs"
[ ! -e "$temporary/post-checkout-ran" ] || fail "configured post-checkout hook executed"
printf '700\n700\n600\n' | cmp -s - "$DARK_FACTORY_TEST_BACKUP_MODES" \
    || fail "backup modes while the store was copied: $(tr '\n' ' ' <"$DARK_FACTORY_TEST_BACKUP_MODES")"
for cmd in factoryctl factoryd factory-runner; do
    [ -x "$test_repository/.worktrees/bin-$sha/$cmd" ] || fail "$cmd not built"
    grep -q '^# pins GOTOOLCHAIN=go1.2.3 GOENV=off GOAUTH=off$' "$test_repository/.worktrees/bin-$sha/$cmd" \
        || fail "$cmd not built with the go.mod toolchain pinned: $(grep '^# pins' "$test_repository/.worktrees/bin-$sha/$cmd")"
done
for controller_asset in \
    cold-review.sh \
    factory-autonomy.py \
    factory-delivery.py \
    factory-intake.py \
    factory-publication.py \
    factory-release.py \
    factory-review-intake.py \
    go-gate-environment.sh \
    verify-adversarial-review.sh \
    supervision.md
do
    installed="$test_repository/.worktrees/bin-$sha/libexec/dark-factory/$controller_asset"
    [ -x "$installed" ] && cmp -s "$installed" "$test_repository/.worktrees/build-$sha/scripts/$controller_asset" \
        || fail "controller asset was not prepared with factoryctl: $controller_asset"
done
builds=$(wc -l <"$DARK_FACTORY_TEST_GO_LOG" | tr -d ' ')
[ "$builds" -gt 0 ] && [ "$(grep -F -c -- '-buildvcs=true' "$DARK_FACTORY_TEST_GO_LOG")" = "$builds" ] \
    || fail "builds did not require VCS metadata: $(tr '\n' ';' <"$DARK_FACTORY_TEST_GO_LOG")"
printf '%s\n' \
    "service uninstall --home $fake_home/.dark-factory --label com.dark-factory.fixture --plist-dir /private/tmp/fixture-plists" \
    "service install --home $fake_home/.dark-factory --label com.dark-factory.fixture --plist-dir /private/tmp/fixture-plists --relay-origin wss://relay.example" \
    "service status --home $fake_home/.dark-factory --label com.dark-factory.fixture --plist-dir /private/tmp/fixture-plists" \
    "web status" \
    "remote status" >"$temporary/expected.log"
cmp -s "$temporary/expected.log" "$DARK_FACTORY_TEST_FACTORYCTL_LOG" \
    || fail "factoryctl calls: $(tr '\n' ';' <"$DARK_FACTORY_TEST_FACTORYCTL_LOG")"
grep -q '^user_version now: 7$' "$temporary/stdout" || fail "user_version not printed"
rm "$DARK_FACTORY_TEST_FACTORYCTL_LOG"

# An absent receipt member is a local-only install: factoryctl rejects an
# empty relay argument, so reinstall must omit the flag rather than pass "".
printf '{"label":"com.dark-factory.fixture","plist_path":"/private/tmp/fixture-plists/com.dark-factory.fixture.plist"}\n' >"$fake_home/.dark-factory.service/receipt"
"$script" "$sha" >"$temporary/stdout" || fail "local-only reinstall exited non-zero"
printf '%s\n' \
    "service uninstall --home $fake_home/.dark-factory --label com.dark-factory.fixture --plist-dir /private/tmp/fixture-plists" \
    "service install --home $fake_home/.dark-factory --label com.dark-factory.fixture --plist-dir /private/tmp/fixture-plists" \
    "service status --home $fake_home/.dark-factory --label com.dark-factory.fixture --plist-dir /private/tmp/fixture-plists" \
    "web status" \
    "remote status" >"$temporary/expected-local.log"
cmp -s "$temporary/expected-local.log" "$DARK_FACTORY_TEST_FACTORYCTL_LOG" \
    || fail "local-only relay: factoryctl calls: $(tr '\n' ';' <"$DARK_FACTORY_TEST_FACTORYCTL_LOG")"
rm "$DARK_FACTORY_TEST_FACTORYCTL_LOG"
# The receipt comes from Go's JSON encoder, which escapes HTML-significant
# bytes. Reinstall must restore the decoded, valid custom origin.
printf '%s\n' '{"label":"com.dark-factory.fixture","plist_path":"/private/tmp/fixture-plists/com.dark-factory.fixture.plist","relay_origin":"wss://relay\u0026.example"}' >"$fake_home/.dark-factory.service/receipt"
"$script" "$sha" >"$temporary/stdout" || fail "escaped relay origin reinstall exited non-zero"
printf '%s\n' \
    "service uninstall --home $fake_home/.dark-factory --label com.dark-factory.fixture --plist-dir /private/tmp/fixture-plists" \
    "service install --home $fake_home/.dark-factory --label com.dark-factory.fixture --plist-dir /private/tmp/fixture-plists --relay-origin wss://relay&.example" \
    "service status --home $fake_home/.dark-factory --label com.dark-factory.fixture --plist-dir /private/tmp/fixture-plists" \
    "web status" \
    "remote status" >"$temporary/expected-escaped.log"
cmp -s "$temporary/expected-escaped.log" "$DARK_FACTORY_TEST_FACTORYCTL_LOG" \
    || fail "escaped relay origin: factoryctl calls: $(tr '\n' ';' <"$DARK_FACTORY_TEST_FACTORYCTL_LOG")"
rm "$DARK_FACTORY_TEST_FACTORYCTL_LOG"
printf '{"label":"com.dark-factory.fixture","plist_path":"/private/tmp/fixture-plists/com.dark-factory.fixture.plist","relay_origin":"wss://relay.example"}\n' >"$fake_home/.dark-factory.service/receipt"

# VCS metadata is provenance, not just a build option: both refusal paths must
# stop before the backup or service change.
rm -rf "$fake_home/.dark-factory-backups"
DARK_FACTORY_TEST_VCS_REVISION=0000000000000000000000000000000000000000 "$script" "$sha" \
    >/dev/null 2>"$temporary/stderr" && fail "wrong VCS revision accepted"
grep -q "factoryctl not built from $sha" "$temporary/stderr" || fail "wrong VCS revision: wrong refusal"
untouched "wrong VCS revision"
no_service_change "wrong VCS revision"
DARK_FACTORY_TEST_VCS_MODIFIED=true "$script" "$sha" >/dev/null 2>"$temporary/stderr" \
    && fail "modified VCS build accepted"
grep -q 'factoryctl built from a modified tree' "$temporary/stderr" || fail "modified VCS build: wrong refusal"
untouched "modified VCS build"
no_service_change "modified VCS build"

# A socket file nothing answers on is stale, not a reason to stay uninstalled.
DARK_FACTORY_TEST_UNINSTALL_LEAVES=stale "$script" "$sha" >/dev/null 2>"$temporary/stderr" \
    || fail "stale socket after uninstall refused: $(cat "$temporary/stderr")"
cmp -s "$temporary/expected.log" "$DARK_FACTORY_TEST_FACTORYCTL_LOG" \
    || fail "stale socket: factoryctl calls: $(tr '\n' ';' <"$DARK_FACTORY_TEST_FACTORYCTL_LOG")"
rm "$DARK_FACTORY_TEST_FACTORYCTL_LOG"

# A listener still accepting is the previous daemon still owning the home.
DARK_FACTORY_TEST_UNINSTALL_LEAVES=listening "$script" "$sha" >/dev/null 2>"$temporary/stderr" \
    && fail "accepting socket after uninstall accepted"
grep -q 'has not left' "$temporary/stderr" || fail "accepting socket: wrong refusal"
grep -q 'still accepts' "$temporary/stderr" || fail "accepting socket: socket not named"
not_installed "accepting socket"

# So is a previous daemon that has closed its listener but still owns the home.
DARK_FACTORY_TEST_UNINSTALL_LEAVES=lock "$script" "$sha" >/dev/null 2>"$temporary/stderr" \
    && fail "held home lock after uninstall accepted"
grep -q 'has not left' "$temporary/stderr" || fail "held home lock: wrong refusal"
grep -q 'home.lock remains held' "$temporary/stderr" || fail "held home lock: lock not named"
not_installed "held home lock"

# A daemon that dies before listening (a failed migration) must time out with
# the restore instruction, not report success.
DARK_FACTORY_TEST_INSTALL_DEAD=1 "$script" "$sha" >/dev/null 2>"$temporary/stderr" \
    && fail "daemon that never listens accepted"
grep -q 'did not listen' "$temporary/stderr" || fail "daemon that never listens: wrong refusal"
grep -q "service uninstall --home $fake_home/.dark-factory --label com.dark-factory.fixture --plist-dir /private/tmp/fixture-plists.*home.lock is free" "$temporary/stderr" \
    || fail "daemon that never listens: safe stop instruction not printed"
grep -q "restore $fake_home/.dark-factory-backups/.*/factory.sqlite3 over .*factory.sqlite3-shm" "$temporary/stderr" \
    || fail "daemon that never listens: restore instruction not printed"
rm "$DARK_FACTORY_TEST_FACTORYCTL_LOG"

# Preparation can run alongside active work, without migrating profiles,
# backing up stores, or changing the service.
printf '1\n' >"$temporary/dispatch-enabled"
printf 'running:%s\n' "$DARK_FACTORY_TEST_RUN" >"$temporary/active-runs"
mkdir -p "$fake_home/.dark-factory/verification-browser"
before_backups=$(backups)
"$script" --prepare "$sha" >/dev/null 2>"$temporary/stderr" \
    || fail "preparation while active: $(cat "$temporary/stderr")"
[ -d "$fake_home/.dark-factory/verification-browser" ] || fail "preparation moved profile"
[ "$(backups)" = "$before_backups" ] || fail "preparation backed up store"
no_service_change "preparation"
rmdir "$fake_home/.dark-factory/verification-browser"
printf '0\n' >"$temporary/dispatch-enabled"
: >"$temporary/active-runs"

# The drained phase uses the prepared artifacts without compiler work, and
# preserves the configured service rather than targeting the default factory.
custom_home="$fake_home/other-factory"
mkdir -p "$custom_home" "$custom_home.service"
printf 'other store\n' >"$custom_home/factory.sqlite3"
printf '\n' >"$custom_home/home.lock"
printf '%s\n' '{"label":"com.dark-factory.other","plist_path":"/private/tmp/custom plists/com.dark-factory.other.plist","relay_origin":"wss://other.example","tool_path":"/tools with spaces:/bin","toolchain_read_roots":"/tool roots:/sdk","development_browser_address":"127.0.0.1:4173"}' >"$custom_home.service/receipt"
before_builds=$(wc -l <"$DARK_FACTORY_TEST_GO_LOG")
"$script" --home "$custom_home" --install-prepared "$sha" >/dev/null 2>"$temporary/stderr" \
    || fail "configured prepared installation: $(cat "$temporary/stderr")"
[ "$(wc -l <"$DARK_FACTORY_TEST_GO_LOG")" = "$before_builds" ] || fail "prepared install rebuilt"
grep -Fqx "service uninstall --home $custom_home --label com.dark-factory.other --plist-dir /private/tmp/custom plists" "$DARK_FACTORY_TEST_FACTORYCTL_LOG" || fail "wrong configured uninstall"
grep -Fqx "service install --home $custom_home --label com.dark-factory.other --plist-dir /private/tmp/custom plists --relay-origin wss://other.example --tool-path /tools with spaces:/bin --toolchain-read-roots /tool roots:/sdk --development-browser-address 127.0.0.1:4173" "$DARK_FACTORY_TEST_FACTORYCTL_LOG" || fail "configured settings lost"
rm "$DARK_FACTORY_TEST_FACTORYCTL_LOG"
# The operator can replace the recorded toolchain grant, e.g. to add Rust.
DARK_FACTORY_TOOL_PATH="/cargo/bin:/bin" DARK_FACTORY_TOOLCHAIN_READ_ROOTS="/cargo/bin:/rustup home" "$script" --home "$custom_home" --install-prepared "$sha" >/dev/null 2>"$temporary/stderr" \
    || fail "toolchain override installation: $(cat "$temporary/stderr")"
grep -Fq -- "--tool-path /cargo/bin:/bin --toolchain-read-roots /cargo/bin:/rustup home --development-browser-address" "$DARK_FACTORY_TEST_FACTORYCTL_LOG" || fail "toolchain override ignored"
rm "$DARK_FACTORY_TEST_FACTORYCTL_LOG"
DARK_FACTORY_TEST_BUILD_ID=wrong "$script" --prepare "$sha" >/dev/null 2>"$temporary/stderr" \
    && fail "incorrect build receipt accepted"
grep -q 'build receipt mismatch' "$temporary/stderr" || fail "wrong build receipt refusal"
no_service_change "build receipt"

rm -rf "$fake_home/.dark-factory-backups"
printf 'stray\n' >"$test_repository/.worktrees/build-$sha/stray"
"$script" "$sha" >/dev/null 2>"$temporary/stderr" && fail "dirty worktree accepted"
grep -q 'worktree not clean' "$temporary/stderr" || fail "dirty worktree: wrong refusal"
untouched "dirty worktree"

# Without an exact three-part Go version there is nothing to pin the toolchain to.
refuses_go_mod() {
    git -C "$test_repository" commit -q -am "$1"
    bad=$(git -C "$test_repository" rev-parse HEAD)
    "$script" "$bad" >/dev/null 2>"$temporary/stderr" && fail "$1 accepted"
    grep -q 'could not read the exact Go version' "$temporary/stderr" \
        || fail "$1: wrong refusal: $(cat "$temporary/stderr")"
    [ ! -e "$test_repository/.worktrees/bin-$bad" ] || fail "$1: built anyway"
    untouched "$1"
}
printf 'module fixture\n\ngo 1.2\n' >"$test_repository/go.mod"
refuses_go_mod "two-part go version"
git -C "$test_repository" rm -q go.mod
refuses_go_mod "missing go.mod"

echo "reinstall-service tests passed"
