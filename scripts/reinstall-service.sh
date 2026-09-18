#!/bin/sh
set -eu

usage() {
    echo "usage: scripts/reinstall-service.sh [--home PATH] [--prepare | --install-prepared] <commit-sha>" >&2
    echo "  builds factoryctl, factoryd and factory-runner from a clean detached worktree at the" >&2
    echo "  commit, backs up the selected factory store, reinstalls the service from the" >&2
    echo "  new build, and prints service, web and remote status plus the new PRAGMA user_version" >&2
}

sha=
runtime_home="$HOME/.dark-factory"
prepare=0
install_prepared=0
while [ "$#" -gt 0 ]; do
    case "$1" in
        --home) [ "$#" -ge 2 ] || { usage; exit 1; }; runtime_home=$2; shift 2 ;;
        --prepare) prepare=1; shift ;;
        --install-prepared) install_prepared=1; shift ;;
        -h|--help) usage; exit 0 ;;
        *) [ -z "$sha" ] || { usage; exit 1; }; sha=$1; shift ;;
    esac
done
[ "$prepare" = 0 ] || [ "$install_prepared" = 0 ] || { usage; exit 1; }
case "$sha" in
    "" | *[!0-9a-f]*) usage; exit 1 ;;
esac
[ "${#sha}" -eq 40 ] || { usage; exit 1; }
case "$runtime_home" in /*) ;; *) echo "factory home must be absolute" >&2; exit 1 ;; esac
[ -d "$runtime_home" ] && [ ! -L "$runtime_home" ] \
    && [ "$(stat -f '%u' "$runtime_home")" = "$(id -u)" ] \
    || { echo "factory home must be an operator-owned directory" >&2; exit 1; }

repository_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
db="$runtime_home/factory.sqlite3"
socket="$runtime_home/runtimes/factory.sock"
worktree="$repository_root/.worktrees/build-$sha"
bin="$repository_root/.worktrees/bin-$sha"
# The receipt is the service manager's durable record of the exact plist it
# installed. Decode its JSON string before passing it back to factoryctl: Go's
# encoder escapes HTML-significant bytes in a valid custom origin.
receipt_field() {
    perl -MJSON::PP -0777 -e '
        my $field = shift;
        my $receipt = JSON::PP::decode_json(<>);
        die "invalid service receipt\n" unless ref $receipt eq "HASH";
        my $value = $receipt->{$field} // "";
        die "invalid service receipt field\n" if ref $value || $value =~ /[\x00-\x1f\x7f]/;
        print $value;
    ' "$1" "$runtime_home.service/receipt"
}
service_receipt_hash=$(shasum -a 256 "$runtime_home.service/receipt")
label=$(receipt_field label)
[ -n "$label" ] || { echo "service receipt has no label" >&2; exit 1; }
plist_path=$(receipt_field plist_path)
case "$plist_path" in
    /*/"$label.plist") plist_dir=$(dirname "$plist_path") ;;
    *) echo "service receipt has no matching absolute plist path" >&2; exit 1 ;;
esac
relay_origin=$(receipt_field relay_origin)
tool_path=$(receipt_field tool_path)
toolchain_read_roots=$(receipt_field toolchain_read_roots)
development_browser_address=$(receipt_field development_browser_address)

# Dispatch must stay off while the script builds and replaces the service, so
# the supervisor cannot admit work after this count is read.
refuse_dispatch_enabled() {
    dispatch=$(sqlite3 "$db" "SELECT dispatch_enabled FROM factory WHERE singleton = 1")
    [ "$dispatch" = 0 ] || { echo "refusing: dispatch is enabled; keep it off through reinstall and wait for work to drain" >&2; exit 1; }
}

# Checked before the build and again right before the uninstall that would
# kill work that was still draining while the build was running. A run that is
# still running behind its runner's takeover endpoint is adopted by the next
# daemon and survives the restart, so it does not block; every other
# non-terminal run — including an older runner with no endpoint — still does.
refuse_active_runs() {
    blocking=0
    for row in $(sqlite3 "$db" "SELECT phase || ':' || lower(hex(id)) FROM runs WHERE phase <> 'terminal'"); do
        phase=${row%%:*}
        run=${row#*:}
        if [ "$phase" = running ] && [ -S "$runtime_home/runtimes/$run/takeover.sock" ]; then
            echo "adoptable: run $run keeps running across the restart" >&2
            continue
        fi
        echo "blocking: run $run is $phase and cannot be adopted" >&2
        blocking=$((blocking + 1))
    done
    [ "$blocking" = 0 ] || { echo "refusing: $blocking non-adoptable non-terminal run(s) in $db" >&2; exit 1; }
}

# A connect, as the service e2e probes: nc -z cannot scan Unix sockets on macOS.
listening() {
    perl -MIO::Socket::UNIX -e 'exit !IO::Socket::UNIX->new(Peer => shift)' "$socket" 2>/dev/null
}
# factoryd holds an exclusive flock on home.lock until its store is closed.
# Unlike an argv census, the lock answers whether the previous owner left.
home_released() {
    perl -MFcntl=:flock -e 'open my $lock, "+<", shift or exit 1; flock $lock, LOCK_EX | LOCK_NB or exit 1' \
        "$runtime_home/home.lock"
}
previous_left() {
    ! listening && home_released
}
# Polls the predicate for up to 60 x 1s, then hands over to the timeout handler.
await() {
    waited=0
    until "$1"; do
        [ "$waited" -lt 60 ] || "$2"
        sleep 1
        waited=$((waited + 1))
    done
}
uninstall_stalled() {
    echo "the previous factoryd has not left $runtime_home within 60s; the service is uninstalled" >&2
    listening && echo "$socket still accepts connections" >&2
    home_released || echo "$runtime_home/home.lock remains held" >&2
    echo "rerun once the previous daemon has exited" >&2
    exit 1
}
install_stalled() {
    echo "factoryd did not listen on $socket within 60s" >&2
    echo "check 'factoryctl service status --home $runtime_home --label $label --plist-dir $plist_dir' and the daemon log; after a failed migration" >&2
    echo "run 'factoryctl service uninstall --home $runtime_home --label $label --plist-dir $plist_dir', confirm $runtime_home/home.lock is free, then restore $backup/factory.sqlite3 over $db, remove $db-wal and $db-shm, and reinstall the previous bin-*" >&2
    exit 1
}

[ -f "$db" ] || { echo "no store at $db" >&2; exit 1; }
if [ "$prepare" = 0 ]; then
    refuse_dispatch_enabled
    refuse_active_runs
fi
# The old verifier profile was under the strict runtime-home census. Only
# inspect it when an old entry or unsafe home path exists, so normal installs
# retain no Node dependency.
legacy_profile="$runtime_home/verification-browser"
if [ "$prepare" = 0 ] && { [ -e "$legacy_profile" ] || [ -L "$legacy_profile" ]; }; then
    [ "$runtime_home" = "$HOME/.dark-factory" ] || { echo "legacy browser profile needs migration before custom-home reinstall" >&2; exit 1; }
    node "$repository_root/scripts/verification-profile.mjs"
fi

if [ "$prepare" = 0 ]; then
    if [ "$install_prepared" = 0 ]; then
        "$repository_root/scripts/reinstall-service.sh" --home "$runtime_home" --prepare "$sha"
    fi
else
    # Serialize compiler work only; release the gate before callers drain workers.
    if [ "${DARK_FACTORY_LOCAL_CI_LEASE_HELD-}" != 1 ]; then
        (cd "$repository_root" && exec ./scripts/with-local-ci-lease.sh \
            ./scripts/reinstall-service.sh --home "$runtime_home" --prepare "$sha")
        exit $?
    fi
    git -C "$repository_root" fetch -q origin
    # Configured hooks must not run where the live service is built.
    [ -d "$worktree" ] || git -C "$repository_root" -c core.hooksPath=/dev/null \
        worktree add -q --detach "$worktree" "$sha"
    [ -z "$(git -C "$worktree" status --porcelain=v1 --untracked-files=all)" ] \
        || { echo "worktree not clean: $worktree" >&2; exit 1; }
    [ "$(git -C "$worktree" rev-parse HEAD)" = "$sha" ] \
        || { echo "worktree not at $sha: $worktree" >&2; exit 1; }

    # The compiler pinned the way the release workflow pins it, from the go.mod
    # being built: the vcs.* checks below prove the source, not the toolchain.
    # A missing go.mod reads as no version and takes the refusal below.
    go_version=$(sed -n 's/^go \([0-9][0-9.]*\)$/\1/p' "$worktree/go.mod" 2>/dev/null || :)
    case "$go_version" in
        *.*.*) ;;
        *) echo "could not read the exact Go version from $worktree/go.mod" >&2; exit 1 ;;
    esac
    export GOTOOLCHAIN="go$go_version" GOENV=off GOAUTH=off

    version=$(cat "$worktree/VERSION")
    target=$(go env GOOS)/$(go env GOARCH)
    receipt=$(cd "$worktree" && go run ./internal/buildinfo/cmd/release-artifact receipt "$version" "$sha" "$target")
    mkdir -p "$bin"
    for cmd in factoryctl factoryd factory-runner; do
        (cd "$worktree" && go build -trimpath -buildvcs=true -ldflags "-X github.com/dark-factory-build/dark-factory/internal/buildinfo.receipt=$receipt" -o "$bin/$cmd" "./cmd/$cmd")
        go version -m "$bin/$cmd" | grep -q "vcs.revision=$sha" \
            || { echo "$cmd not built from $sha" >&2; exit 1; }
        go version -m "$bin/$cmd" | grep -q "vcs.modified=false" \
            || { echo "$cmd built from a modified tree" >&2; exit 1; }
        "$bin/$cmd" --build-identity | perl -MJSON::PP -0777 -e '
            my $expected = shift;
            my $identity = JSON::PP::decode_json(<>);
            die "invalid build receipt\n" unless $identity->{release}
                && join("|", @{$identity}{qw(version source target build_id)}) eq $expected;
        ' "$receipt" || { echo "$cmd build receipt mismatch" >&2; exit 1; }
    done

    exit 0
fi


# The install phase never compiles or holds the shared build gate. Accept only
# the exact prepared clean source and a coherent installed-release identity.
[ "$(git -C "$worktree" rev-parse HEAD)" = "$sha" ] \
    && [ -z "$(git -C "$worktree" status --porcelain=v1 --untracked-files=all)" ] \
    || { echo "prepared source is missing, modified, or at another revision" >&2; exit 1; }
version=$(cat "$worktree/VERSION")
target=$(go env GOOS)/$(go env GOARCH)
prepared_identity=
for cmd in factoryctl factoryd factory-runner; do
    metadata=$(go version -m "$bin/$cmd")
    echo "$metadata" | grep -q "vcs.revision=$sha" \
        && echo "$metadata" | grep -q 'vcs.modified=false' \
        || { echo "$cmd prepared source identity mismatch" >&2; exit 1; }
    identity=$("$bin/$cmd" --build-identity | perl -MJSON::PP -0777 -e '
        my ($version, $source, $target) = @ARGV; @ARGV = ();
        my $identity = JSON::PP::decode_json(<>);
        die "invalid prepared build receipt\n" unless $identity->{release}
            && $identity->{version} eq $version && $identity->{source} eq $source
            && $identity->{target} eq $target && length($identity->{build_id});
        print join("|", @{$identity}{qw(version source target build_id)});
    ' "$version" "$sha" "$target")
    [ -z "$prepared_identity" ] || [ "$prepared_identity" = "$identity" ] \
        || { echo "prepared binaries have different build receipts" >&2; exit 1; }
    prepared_identity=$identity
done

# Private, unique directories prevent same-revision factory backups colliding.
backup=$(umask 077 && mkdir -p "$HOME/.dark-factory-backups" && \
    mktemp -d "$HOME/.dark-factory-backups/$(date -u +%Y%m%dT%H%M%S)-$sha.XXXXXX")
(umask 077 && sqlite3 "$db" ".backup $backup/factory.sqlite3")
echo "backup: $backup (user_version $(sqlite3 "$backup/factory.sqlite3" 'PRAGMA user_version'))"

refuse_dispatch_enabled
refuse_active_runs
[ "$(shasum -a 256 "$runtime_home.service/receipt")" = "$service_receipt_hash" ] \
    || { echo "service settings changed during preparation; retry from the current receipt" >&2; exit 1; }
"$bin/factoryctl" service uninstall --home "$runtime_home" --label "$label" --plist-dir "$plist_dir"
# bootout returns once launchd forgets the job; factoryd unlinks its socket
# before it closes the store and releases the home flock. A socket file that
# nothing answers on is stale, and factoryd removes it on its next start.
await previous_left uninstall_stalled
set -- service install --home "$runtime_home" --label "$label" --plist-dir "$plist_dir"
[ -z "$relay_origin" ] || set -- "$@" --relay-origin "$relay_origin"
[ -z "$tool_path" ] || set -- "$@" --tool-path "$tool_path"
[ -z "$toolchain_read_roots" ] || set -- "$@" --toolchain-read-roots "$toolchain_read_roots"
[ -z "$development_browser_address" ] || set -- "$@" --development-browser-address "$development_browser_address"
"$bin/factoryctl" "$@"
# launchd returns from bootstrap before factoryd listens, and factoryd opens
# (and migrates) the store before it listens, so the socket accepting means
# the migration finished. Bounded: a daemon that dies on a failed migration
# never listens.
await listening install_stalled
"$bin/factoryctl" service status --home "$runtime_home" --label "$label" --plist-dir "$plist_dir"
export DARK_FACTORY_SOCKET="$socket"
export DARK_FACTORY_OPERATOR_TOKEN_FILE="$runtime_home/operator.token"
"$bin/factoryctl" web status
"$bin/factoryctl" remote status
echo "user_version now: $(sqlite3 "$db" 'PRAGMA user_version')"
echo "binaries: $bin (keep the previous bin-* for rollback; after a failed migration restore $backup/factory.sqlite3 over $db and remove $db-wal and $db-shm)"
