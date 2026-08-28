#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
temporary=$(/usr/bin/mktemp -d /private/tmp/dark-factory-go-gates-test.XXXXXXXX)
fail() { echo "go gates test failed: $*" >&2; exit 1; }
test_cleanup() { go_gate_environment_cleanup 2>/dev/null || true; /bin/rm -rf "$temporary"; }
trap test_cleanup EXIT HUP INT TERM

# Source the internal functions so fake commands can be passed explicitly.
# The executable gates never consume this test-only command path.
. "$repository_root/scripts/go-gate-environment.sh"
go_gate_environment_setup || fail "environment setup failed"
go_gate_test_root=$go_gate_root
go_gate_test_log="$temporary/stage.log"
fake="$temporary/fake-stage"
cat >"$fake" <<'EOF'
#!/bin/sh
set -eu
log=$1
printf 'HOME=%s PATH=%s SECRET=%s TMPDIR=%s\n' "${HOME-unset}" "$PATH" "${DARK_FACTORY_SECRET-unset}" "$TMPDIR" >>"$log"
case "${2-}" in
    fail) exit 41 ;;
    timeout)
        trap '' TERM
        (
            trap '' TERM
            (trap '' TERM; sleep 30) & grandchild=$!
            printf '%s\n' "$grandchild" >"$4"
            wait
        ) & child=$!
        printf '%s\n' "$child" >"$3"
        wait
        ;;
    swap) root=${TMPDIR%/tmp}; /bin/mv "$root" "$root.original"; /bin/mkdir "$root"; /bin/chmod 700 "$root"; for d in tmp cache modcache corepack npm-cache; do /bin/mkdir "$root/$d"; done ;;
esac
EOF
/bin/chmod 700 "$fake"

export DARK_FACTORY_SECRET=should-not-cross HOME="$temporary/home"
go_gate_stage 5 "$fake" "$go_gate_test_log" || fail "whitelist fixture failed"
/usr/bin/grep -F "HOME=$go_gate_root/home" "$go_gate_test_log" >/dev/null || fail "HOME was not confined to owned root"
/usr/bin/grep -F 'SECRET=unset' "$go_gate_test_log" >/dev/null || fail "ambient secret crossed env boundary"
/usr/bin/grep -F "PATH=$go_gate_node_bin_dir:/usr/bin:/bin" "$go_gate_test_log" >/dev/null || fail "PATH was not fixed"
/usr/bin/grep -F "TMPDIR=$go_gate_root/tmp" "$go_gate_test_log" >/dev/null || fail "TMPDIR was not controlled"

# Captured absolute tools remain authoritative after the caller's PATH is
# replaced. The same hostile path must not select a second Node/Corepack pair.
export PATH="$temporary/fake-path"

# Exercise the shared fast-stage composition through test-local explicit tool
# arguments. The production entrypoints have no environment override for
# these paths.
fast_log="$temporary/fast.log"
fast_mode="$temporary/fast.mode"
fast_go="$temporary/go"
fast_gofmt="$temporary/gofmt"
fast_git="$temporary/git"
fast_xargs="$temporary/xargs"
fast_corepack="$temporary/corepack"
/bin/cat >"$fast_go" <<EOF
#!/bin/sh
printf 'go %s GOPROXY=%s GOSUMDB=%s\n' "\$*" "\${GOPROXY-}" "\${GOSUMDB-}" >>"$fast_log"
case "\$1 \${2-}" in
    version*) printf 'go version go1.27.0 darwin/arm64\n' ;;
    mod\\ download) test "\$(/bin/cat "$fast_mode" 2>/dev/null)" != download-fail ;;
    mod\\ verify) test "\$(/bin/cat "$fast_mode" 2>/dev/null)" != verify-fail ;;
    vet\\ ./...) test "\$(/bin/cat "$fast_mode" 2>/dev/null)" != vet-fail ;;
    test*) test "\$(/bin/cat "$fast_mode" 2>/dev/null)" != test-fail ;;
esac
EOF
/bin/cat >"$fast_gofmt" <<EOF
#!/bin/sh
printf 'gofmt\n' >>"$fast_log"
case "\$(/bin/cat "$fast_mode" 2>/dev/null)" in
    format-fail) exit 46 ;;
    format-output) printf 'go-bad.go\n' ;;
esac
EOF
/bin/cat >"$fast_git" <<EOF
#!/bin/sh
case "\$1" in
    ls-files) printf 'go.mod\\0' ;;
    diff) : ;;
esac
EOF
/bin/cat >"$fast_xargs" <<EOF
#!/bin/sh
exec "$fast_gofmt" "\$@"
EOF
fast_typescript_template="$temporary/fast-typescript-template"
/bin/mkdir -p "$fast_typescript_template/bin" "$fast_typescript_template/lib"
printf '%s\n' '{"name":"typescript","version":"5.8.3"}' >"$fast_typescript_template/package.json"
printf '%s\n' 'typescript fixture compiler' >"$fast_typescript_template/bin/tsc"
printf '%s\n' 'typescript fixture library' >"$fast_typescript_template/lib/typescript.js"
/bin/chmod 755 "$fast_typescript_template" "$fast_typescript_template/bin" "$fast_typescript_template/lib"
/bin/chmod 644 "$fast_typescript_template/package.json" "$fast_typescript_template/lib/typescript.js"
/bin/chmod 755 "$fast_typescript_template/bin/tsc"
/bin/cat >"$fast_corepack" <<EOF
#!/bin/sh
printf 'corepack %s NETWORK=%s INTEGRITY=%s UMASK=%s\n' "\$*" "\${COREPACK_ENABLE_NETWORK-}" "\${COREPACK_INTEGRITY_KEYS-unset}" "\$(umask)" >>"$fast_log"
if [ "\${1-}" = pnpm ] && [ "\${2-}" = install ]; then
    /bin/mkdir -p node_modules/.pnpm
    /bin/mkdir -p node_modules/.pnpm/typescript@5.8.3/node_modules
    /bin/cp -R "$fast_typescript_template" node_modules/.pnpm/typescript@5.8.3/node_modules/typescript
    /bin/ln -s .pnpm/typescript@5.8.3/node_modules/typescript node_modules/typescript
    : >node_modules/.modules.yaml
fi
case "\$(/bin/cat "$fast_mode" 2>/dev/null)" in
    bad-tree) /bin/chmod 600 node_modules/.pnpm/typescript@5.8.3/node_modules/typescript/package.json ;;
    corepack-fail) exit 45 ;;
esac
EOF
for fast_tool in "$fast_go" "$fast_gofmt" "$fast_git" "$fast_xargs" "$fast_corepack"; do /bin/chmod 700 "$fast_tool"; done
. "$repository_root/scripts/go-fast-stage.sh"
go_gate_go=$fast_go; go_gate_go_identity=$(go_gate_stat "$fast_go")
go_gate_go_hash=$(go_gate_hash "$fast_go")
go_gate_gofmt=$fast_gofmt; go_gate_gofmt_identity=$(go_gate_stat "$fast_gofmt")
go_gate_gofmt_hash=$(go_gate_hash "$fast_gofmt")
go_gate_git=$fast_git; go_gate_git_identity=$(go_gate_stat "$fast_git")
go_gate_xargs=$fast_xargs; go_gate_xargs_identity=$(go_gate_stat "$fast_xargs")
go_gate_corepack=$fast_corepack; go_gate_corepack_identity=$(go_gate_stat "$fast_corepack")
go_gate_corepack_hash=$(go_gate_hash "$fast_corepack")
# The production helper invokes Corepack through its pinned Node binary. The
# fixture intentionally exercises the direct fake-Corepack branch.
go_gate_package_manager_direct=1
: >"$fast_mode"
fast_repository_root="$temporary/fast-repository"
/bin/mkdir -p "$fast_repository_root/web/scripts"
/bin/cp "$repository_root/web/scripts/package-artifacts.mjs" "$fast_repository_root/web/scripts/package-artifacts.mjs"
fast_typescript_digest=$("$go_gate_node" --input-type=module --eval 'const { toolTreeDigest } = await import("./web/scripts/package-artifacts.mjs"); process.stdout.write(`${toolTreeDigest(process.argv[1])}\n`);' "$fast_typescript_template")
fast_reviewed_typescript_digest=$("$go_gate_node" --input-type=module --eval 'const { readFileSync } = await import("node:fs"); process.stdout.write(`${JSON.parse(readFileSync(process.argv[1], "utf8")).typescript.treeSha512}\n`);' "$repository_root/web/toolchain-integrity.json")
/usr/bin/sed "s/$fast_reviewed_typescript_digest/$fast_typescript_digest/" "$repository_root/web/toolchain-integrity.json" >"$fast_repository_root/web/toolchain-integrity.json"
fast_real_repository_root=$repository_root
repository_root=$fast_repository_root
go_gate_fast_stage || { repository_root=$fast_real_repository_root; fail "fast stage fixture failed"; }
repository_root=$fast_real_repository_root
/usr/bin/grep -F 'go mod download GOPROXY=https://proxy.golang.org' "$fast_log" >/dev/null || fail "download did not use network stage"
/usr/bin/grep -F 'go mod verify GOPROXY=off' "$fast_log" >/dev/null || fail "verify was not offline"
/usr/bin/grep -F 'corepack pnpm install --frozen-lockfile --ignore-scripts NETWORK=1' "$fast_log" >/dev/null || fail "install was not the sole package network stage"
/usr/bin/grep -F 'corepack pnpm install --frozen-lockfile --ignore-scripts NETWORK=1 INTEGRITY=unset UMASK=0022' "$fast_log" >/dev/null || fail "install child umask was not 0022"
/usr/bin/grep -F 'corepack pnpm run test NETWORK=0' "$fast_log" >/dev/null || fail "package tests were not offline"
/usr/bin/grep -F 'INTEGRITY=unset' "$fast_log" >/dev/null || fail "Corepack signature verification was disabled"
printf '%s\n' bad-tree >"$fast_mode"
if go_gate_fast_stage; then fail "fast stage accepted a changed TypeScript tree"; fi
: >"$fast_mode"

# The web install deliberately uses a process-local umask. This fixture
# exercises the helper directly so nested calls and interrupted package
# manager commands cannot leak a relaxed mode back to the gate owner.
web_install_fixture="$temporary/web-install-fixture"
/bin/cat >"$web_install_fixture" <<'EOF'
#!/bin/sh
set -eu
. "$1"
. "$2"
log=$3
repository_root=$4
tree="$repository_root/web/node_modules"
mode=success
depth=0
go_gate_environment_setup
trap 'go_gate_environment_cleanup || true' EXIT
go_gate_package_manager_stage() {
    printf '%s %s\n' "$mode" "$(umask)" >>"$log"
    case "$mode" in
        success)
            /bin/mkdir -p "$tree/.pnpm"
            : >"$tree/.modules.yaml"
            : >"$tree/file"
            /bin/chmod 755 "$tree" "$tree/.pnpm"
            /bin/chmod 644 "$tree/.modules.yaml" "$tree/file"
            return 0
            ;;
        fail) return 43 ;;
        nested)
            /bin/mkdir -p "$tree/.pnpm"
            : >"$tree/.modules.yaml"
            if [ "$depth" -eq 0 ]; then
                depth=1
                ( go_gate_environment_setup; go_gate_web_install )
            fi
            /bin/chmod 755 "$tree" "$tree/.pnpm"
            /bin/chmod 644 "$tree/.modules.yaml"
            return 0
            ;;
        normalize)
            /bin/rm -rf -- "$tree"
            /bin/mkdir -p "$tree/.pnpm"
            : >"$tree/.modules.yaml"
            /bin/chmod 755 "$tree" "$tree/.pnpm"
            /bin/chmod 644 "$tree/.modules.yaml"
            return 0
            ;;
        rm-fail)
            /bin/mkdir -p "$tree/.pnpm"
            : >"$tree/.modules.yaml"
            /bin/chmod 755 "$tree" "$tree/.pnpm"
            /bin/chmod 644 "$tree/.modules.yaml"
            return 43
            ;;
        hup) /usr/bin/perl -e 'kill "HUP", $$' ;;
        int) /usr/bin/perl -e 'kill "INT", $$' ;;
        term) /usr/bin/perl -e 'kill "TERM", $$' ;;
        term-blocking) while :; do :; done ;;
        *) return 44 ;;
    esac
}
assert_umask() {
    [ "$(umask)" = "$1" ] || exit 45
}
assert_tree() {
    [ ! -L "$tree" ] && [ -d "$tree" ] || exit 46
    [ -f "$tree/.modules.yaml" ] && [ -d "$tree/.pnpm" ] || exit 47
}

/bin/mkdir -p "$repository_root/web"
for caller_umask in 0000 0027 0077; do
    umask "$caller_umask"
    mode=success
    go_gate_web_install
    assert_tree
    assert_umask "$caller_umask"
done

umask 0077
mode=nested
depth=0
go_gate_web_install
assert_tree
assert_umask 0077
[ "$(/usr/bin/grep -c '^nested 0077$' "$log")" -eq 2 ] || exit 46

umask 0027
mode=fail
if go_gate_web_install; then exit 47; else result=$?; fi
[ "$result" -eq 43 ] || exit 48
assert_umask 0027

umask 0077
mode=success
go_gate_web_install
mode=fail
if go_gate_web_install; then exit 49; else result=$?; fi
[ "$result" -eq 43 ] || exit 50
mode=success
go_gate_web_install
assert_umask 0077

umask 0077
mode=rm-fail
go_gate_web_remove() { return 37; }
if go_gate_web_install; then exit 59; else result=$?; fi
[ "$result" -eq 1 ] || exit 60
go_gate_web_remove() { /bin/rm -rf -- "$1"; }
go_gate_web_remove "$go_gate_root/web-node-modules-discard"
go_gate_web_remove "$go_gate_root/web-node-modules-old"

umask 0077
( mode=success; go_gate_web_install )
assert_umask 0077

umask 0077
mode=normalize
/bin/chmod 700 "$tree"
: >"$tree/.modules.yaml"
/bin/chmod 700 "$tree/.pnpm"
: >"$tree/file"
/bin/chmod 600 "$tree/file"
/bin/chmod 600 "$tree/.modules.yaml"
go_gate_web_install
assert_tree
[ "$(/usr/bin/stat -f '%Lp' "$tree")" = 755 ] || exit 51
[ "$(/usr/bin/stat -f '%Lp' "$tree/.modules.yaml")" = 644 ] || exit 52

for signal in hup int term; do
    umask 0077
    mode=$signal
    if go_gate_web_install 2>/dev/null; then exit 53; else result=$?; fi
    case "$signal" in
        hup) [ "$result" -eq 129 ] || exit 54 ;;
        int) [ "$result" -eq 130 ] || exit 55 ;;
        term) [ "$result" -eq 143 ] || exit 56 ;;
    esac
    assert_umask 0077
done

umask 0077
mode=term-blocking
go_gate_web_install &
web_install_pid=$!
/bin/sleep 1
/bin/kill -TERM "$web_install_pid"
if wait "$web_install_pid"; then exit 57; else result=$?; fi
[ "$result" -eq 143 ] || exit 58
assert_umask 0077
EOF
/bin/chmod 700 "$web_install_fixture"
web_install_log="$temporary/web-install.log"
web_install_root="$temporary/web-install-root"
/bin/mkdir -p "$web_install_root/web"
export PATH="$go_gate_node_bin_dir:/usr/bin:/bin"
if ! /bin/sh "$web_install_fixture" "$repository_root/scripts/go-fast-stage.sh" "$repository_root/scripts/go-gate-environment.sh" "$web_install_log" "$web_install_root"; then
    fail "web install umask fixture failed"
fi

# A permanent replacement of the exact scratch root must fail before the
# package-manager-owned source tree is moved. The external sentinel proves a
# symlinked root cannot redirect quarantine or discard operations.
scratch_attack_fixture="$temporary/scratch-attack-fixture"
/bin/cat >"$scratch_attack_fixture" <<'EOF'
#!/bin/sh
set -eu
script_root=$1
repository_root=$2
external_root=$3
. "$script_root/scripts/go-gate-environment.sh"
. "$script_root/scripts/go-fast-stage.sh"
go_gate_environment_setup
trap 'go_gate_environment_cleanup || true' EXIT
tree="$repository_root/web/node_modules"
/bin/mkdir -p "$tree/.pnpm"
: >"$tree/.modules.yaml"
source_identity=$(go_gate_stat "$tree")
: >"$external_root/sentinel"
original_root="$go_gate_root.original"
/bin/mv "$go_gate_root" "$original_root"
/bin/ln -s "$external_root" "$go_gate_root"
if go_gate_web_install; then exit 41; fi
[ "$(go_gate_stat "$tree")" = "$source_identity" ] || exit 42
[ -f "$tree/.modules.yaml" ] && [ -d "$tree/.pnpm" ] || exit 43
[ -f "$external_root/sentinel" ] || exit 44
[ ! -e "$external_root/web-node-modules-old" ] && [ ! -e "$external_root/web-node-modules-discard" ] || exit 45
/bin/rm -f "$go_gate_root"
/bin/mv "$original_root" "$go_gate_root"
[ -d "$tree" ] || exit 46
/bin/rm -rf -- "$tree"
/bin/ln -s "$external_root" "$tree"
if go_gate_web_install; then exit 47; fi
[ -L "$tree" ] || exit 48
[ -f "$external_root/sentinel" ] || exit 49
/bin/rm -f "$tree"
go_gate_environment_cleanup
EOF
/bin/chmod 700 "$scratch_attack_fixture"
scratch_attack_repository="$temporary/scratch-attack-repository"
scratch_attack_external="$temporary/scratch-attack-external"
/bin/mkdir -p "$scratch_attack_repository/web" "$scratch_attack_external"
/bin/sh "$scratch_attack_fixture" "$repository_root" "$scratch_attack_repository" "$scratch_attack_external" \
    || fail "scratch root replacement fixture failed"

printf '# mutation\n' >>"$fast_go"
if go_gate_before_stage; then fail "in-place Go mutation was accepted"; fi
go_gate_go_hash=$(go_gate_hash "$fast_go")
printf '# mutation\n' >>"$fast_corepack"
if go_gate_before_stage; then fail "in-place Corepack mutation was accepted"; fi
go_gate_corepack_hash=$(go_gate_hash "$fast_corepack")
# A pre-existing output symlink must be rejected before shell redirection can
# write outside the owned root.
outside="$temporary/outside"
printf 'sentinel\n' >"$outside"
/bin/rm -f "$go_gate_root/go-files"
/bin/ln -s "$outside" "$go_gate_root/go-files"
if go_gate_fast_stage; then fail "external output symlink passed"; fi
/usr/bin/grep -F 'sentinel' "$outside" >/dev/null || fail "outside sentinel changed"
/bin/rm -f "$go_gate_root/go-files"
printf '%s\n' format-fail >"$fast_mode"
if go_gate_fast_stage; then fail "formatter failure passed"; fi
printf '%s\n' verify-fail >"$fast_mode"
if go_gate_fast_stage; then fail "verification failure passed"; fi

go_gate_go=/opt/homebrew/bin/go
go_gate_gofmt=/opt/homebrew/bin/gofmt
go_gate_git=/usr/bin/git
go_gate_xargs=/usr/bin/xargs
go_gate_corepack=/opt/homebrew/bin/corepack
go_gate_go_identity=$(go_gate_stat "$go_gate_go")
go_gate_gofmt_identity=$(go_gate_stat "$go_gate_gofmt")
go_gate_git_identity=$(go_gate_stat "$go_gate_git")
go_gate_xargs_identity=$(go_gate_stat "$go_gate_xargs")
go_gate_corepack_identity=$(go_gate_stat "$go_gate_corepack")
go_gate_go_hash=$(go_gate_hash "$go_gate_go")
go_gate_gofmt_hash=$(go_gate_hash "$go_gate_gofmt")
go_gate_corepack_hash=$(go_gate_hash "$go_gate_corepack")

if go_gate_stage 5 "$fake" "$go_gate_test_log" fail; then
    fail "child failure passed"
else
    failure_status=$?
fi
[ "$failure_status" -eq 41 ] || fail "child failure status was lost"
pid_file="$temporary/descendant.pid"
grandchild_pid_file="$temporary/grandchild.pid"
if go_gate_stage 1 "$fake" "$go_gate_test_log" timeout "$pid_file" "$grandchild_pid_file"; then
    fail "timeout passed"
else
    timeout_status=$?
fi
[ "$timeout_status" -eq 124 ] || fail "timeout returned $timeout_status"
[ -s "$pid_file" ] || fail "timeout descendant was not created"
timeout_pid=$(/bin/cat "$pid_file")
if /bin/kill -0 "$timeout_pid" 2>/dev/null; then fail "timeout descendant survived"; fi
[ -s "$grandchild_pid_file" ] || fail "timeout grandchild was not created"
timeout_grandchild=$(/bin/cat "$grandchild_pid_file")
if /bin/kill -0 "$timeout_grandchild" 2>/dev/null; then fail "timeout grandchild survived"; fi

# A direct child that exits 0 while a same-group descendant survives is still
# a failed stage; the owner must terminate the whole group.
natural_pid="$temporary/natural.pid"
if go_gate_stage 5 /bin/sh -c "perl -e '\$p=fork(); if (!\$p) { \$SIG{TERM}=\"IGNORE\"; \$SIG{HUP}=\"IGNORE\"; sleep 30; exit 0; } print \"\$p\\n\"; exit 0' >'$natural_pid'"; then
    fail "surviving descendant passed"
else
    natural_status=$?
fi
[ "$natural_status" -eq 125 ] || fail "surviving descendant returned $natural_status"
[ -s "$natural_pid" ] || fail "natural descendant was not created"
natural_child=$(/bin/cat "$natural_pid")
if /bin/kill -0 "$natural_child" 2>/dev/null; then fail "natural descendant survived"; fi

# Model the post-reap process-group state machine. Once a census is unknown,
# only ESRCH can prove absence; a reused/live PGID must not regain signal
# authority, and live-owned cleanup must stop at a later unknown census.
if ! /usr/bin/perl - <<'EOF'
use strict;
use warnings;

sub stop_model {
    my @states = @_;
    my @signals;
    my $state = shift @states // "unknown";
    if ($state eq "unknown") {
        for (1..10) {
            $state = shift @states // "unknown";
            return (1, \@signals) if $state eq "gone";
            return (0, \@signals) if $state eq "live";
        }
        return (0, \@signals);
    }
    return (1, \@signals) if $state eq "gone";
    return (0, \@signals) if $state eq "unknown";
    push @signals, "TERM";
    for (1..10) {
        $state = shift @states // "unknown";
        return (2, \@signals) if $state eq "gone";
        return (0, \@signals) if $state eq "unknown";
    }
    push @signals, "KILL";
    for (1..10) {
        $state = shift @states // "unknown";
        return (2, \@signals) if $state eq "gone";
        return (0, \@signals) if $state eq "unknown";
    }
    return (0, \@signals);
}

sub check {
    my ($name, $want_result, $want_signals, @states) = @_;
    my ($result, $signals) = stop_model(@states);
    my $actual_signals = join ",", @$signals;
    my $expected_signals = join ",", @$want_signals;
    die "$name: result=$result expected=$want_result\n"
        unless $result == $want_result;
    die "$name: signals=$actual_signals expected=$expected_signals\n"
        unless $actual_signals eq $expected_signals;
}

check("persistent EPERM", 0, [], "unknown");
check("EPERM to ESRCH", 1, [], "unknown", "gone");
check("EPERM to live reused PGID", 0, [], "unknown", "live");
check("malformed/reused PGID", 0, [], "unknown", "unknown", "live");
check("leader exit first", 1, [], "unknown", "gone");
check("live-owned cleanup to ESRCH", 2, ["TERM"], "live", "gone");
check("live-owned TERM then KILL to ESRCH", 2, ["TERM", "KILL"],
    "live", ("live") x 10, "gone");
check("live to unknown", 0, ["TERM"], "live", "unknown");
print "group state models passed\n";
EOF
then
    fail "group state model failed"
fi

: >"$go_gate_test_log"
if go_gate_stage 5 "$fake" "$go_gate_test_log" swap; then fail "root replacement passed"; fi
if go_gate_validate_identity; then fail "replacement retained original identity"; fi
[ -d "$go_gate_root" ] && [ -d "$go_gate_root.original" ] || fail "root replacement evidence lost"
if go_gate_environment_cleanup; then fail "cleanup accepted a replaced root"; fi
[ -d "$go_gate_root" ] && [ -d "$go_gate_root.original" ] || fail "cleanup deleted a replacement"
/bin/rm -rf "$go_gate_root" "$go_gate_root.original"
go_gate_owned=0

# Matching caller variables cannot select a root or inject a cache.
export DARK_FACTORY_GO_GATE_ROOT=/tmp DARK_FACTORY_GO_GATE_NONCE=forged
go_gate_environment_cleanup || true
export PATH="$go_gate_node_bin_dir:/usr/bin:/bin"
go_gate_environment_setup || fail "second environment setup failed"
second_root=$go_gate_root
case "$second_root" in /private/tmp/dark-factory-go.*) ;; *) fail "root escaped canonical prefix" ;; esac
[ "$second_root" != "$go_gate_test_root" ] || fail "root was reused"
go_gate_environment_cleanup || fail "cleanup failed"

# Cleanup owns the exact root even when a stage leaves restrictive cache
# entries behind. Exercise both a nested read-only directory and file.
go_gate_environment_setup || fail "read-only cleanup setup failed"
readonly_root="$go_gate_root/cache/readonly/child"
/bin/mkdir -p "$readonly_root"
: >"$readonly_root/cache-entry"
/bin/chmod 0500 "$go_gate_root/cache/readonly" "$readonly_root"
/bin/chmod 0400 "$readonly_root/cache-entry"
readonly_gate_root=$go_gate_root
go_gate_environment_cleanup || fail "read-only cleanup failed"
[ ! -e "$readonly_gate_root" ] || fail "read-only cleanup retained its root"

# A top-level TERM must join the active supervisor before cleanup. The fixture
# writes its child PID only after the bounded owner has started the stage.
signal_fixture="$temporary/signal-fixture.sh"
signal_marker="$temporary/signal-active"
signal_root_file="$temporary/signal-root"
signal_wrapper_file="$temporary/signal-wrapper"
signal_child_file="$temporary/signal-child"
signal_joined_file="$temporary/signal-joined"
signal_cleaned_file="$temporary/signal-cleaned"
signal_fake_bin="$temporary/signal-bin"
signal_fake_corepack="$signal_fake_bin/corepack"
signal_repository_root="$temporary/signal-repository"
/bin/mkdir -p "$signal_fake_bin" "$signal_repository_root/web"
/bin/cat >"$signal_fake_corepack" <<EOF
#!/bin/sh
printf '%s %s\n' "\$\$" "\$(/bin/ps -o pgid= -p \$\$ | /usr/bin/tr -d ' ')" >"$signal_child_file"
printf active >"$signal_marker"
exec /usr/bin/perl -e 'select undef, undef, undef, 30 while 1'
EOF
/bin/chmod 700 "$signal_fake_corepack"
/bin/cat >"$signal_fixture" <<'EOF'
#!/bin/sh
set -eu
environment_script=$1
fast_stage_script=$2
signal_marker=$3
signal_root_file=$4
signal_wrapper_file=$5
signal_child_file=$6
signal_joined_file=$7
signal_cleaned_file=$8
signal_fake_corepack=$9
repository_root=${10}
. "$environment_script"
. "$fast_stage_script"
go_gate_environment_setup
go_gate_package_manager_direct=1
go_gate_corepack=$signal_fake_corepack
go_gate_corepack_identity=$(go_gate_stat "$signal_fake_corepack")
go_gate_corepack_hash=$(go_gate_hash "$signal_fake_corepack")
printf '%s\n' "$go_gate_root" >"$signal_root_file"
printf '%s\n' "$$" >"$signal_wrapper_file"
go_gate_signal() {
    signal=$1
    trap - EXIT HUP INT TERM
    if [ -n "${go_gate_supervisor_pid-}" ]; then
        /bin/kill -TERM "$go_gate_supervisor_pid" 2>/dev/null || true
        wait "$go_gate_supervisor_pid" 2>/dev/null || true
    fi
    : >"$signal_joined_file"
    go_gate_environment_cleanup || true
    : >"$signal_cleaned_file"
    exit $((128 + signal))
}
trap 'go_gate_signal 15' TERM
go_gate_web_install
EOF
/bin/chmod 700 "$signal_fixture"
PATH="$signal_fake_bin:$PATH" /bin/sh "$signal_fixture" "$repository_root/scripts/go-gate-environment.sh" "$repository_root/scripts/go-fast-stage.sh" "$signal_marker" "$signal_root_file" "$signal_wrapper_file" "$signal_child_file" "$signal_joined_file" "$signal_cleaned_file" "$signal_fake_corepack" "$signal_repository_root" &
signal_fixture_pid=$!
signal_attempts=0
while [ ! -f "$signal_marker" ] && [ "$signal_attempts" -lt 100 ]; do
    /bin/sleep 0.01
    signal_attempts=$((signal_attempts + 1))
done
[ -f "$signal_marker" ] || fail "signal fixture did not start its stage"
signal_wrapper_pid=$(/bin/cat "$signal_wrapper_file")
signal_child_record=$(/bin/cat "$signal_child_file")
signal_child_pid=$(/usr/bin/awk '{print $1}' <<EOF
$signal_child_record
EOF
)
signal_child_pgid=$(/usr/bin/awk '{print $2}' <<EOF
$signal_child_record
EOF
)
[ "$signal_wrapper_pid" = "$signal_fixture_pid" ] || fail "signal fixture wrapper identity changed"
[ -n "$signal_child_pid" ] && [ "$signal_child_pid" = "$signal_child_pgid" ] || fail "signal fixture child group identity changed"
/bin/kill -TERM "$signal_fixture_pid"
signal_join_attempts=0
while /bin/kill -0 "$signal_fixture_pid" 2>/dev/null; do
    signal_join_attempts=$((signal_join_attempts + 1))
    if [ "$signal_join_attempts" -ge 100 ]; then
        /bin/kill -KILL "$signal_fixture_pid" 2>/dev/null || true
        break
    fi
    /bin/sleep 0.01
done
if wait "$signal_fixture_pid"; then signal_status=0; else signal_status=$?; fi
[ "$signal_status" -eq 143 ] || fail "TERM fixture returned $signal_status"
[ -f "$signal_joined_file" ] || fail "TERM fixture did not join its supervisor"
[ -f "$signal_cleaned_file" ] || fail "TERM fixture did not finish cleanup"
signal_survivor=0
if /bin/kill -0 "$signal_child_pid" 2>/dev/null; then
    /bin/kill -KILL "$signal_child_pid" 2>/dev/null || true
    signal_survivor=1
fi
if /bin/kill -0 -"$signal_child_pgid" 2>/dev/null; then
    /bin/kill -KILL -"$signal_child_pgid" 2>/dev/null || true
    signal_survivor=1
fi
[ "$signal_survivor" -eq 0 ] || fail "TERM fixture left an exact child or process-group survivor"
[ ! -e "$(/bin/cat "$signal_root_file")" ] || fail "TERM fixture cleaned up before joining supervisor"

# Exercise the supported helper with the real cached pnpm in a disposable web
# workspace. A mode-corrupted TypeScript tree must be reconstructed from the
# cache; no candidate or repository path is modified by this perturbation.
real_web_install_fixture="$temporary/real-web-install-fixture"
/bin/cat >"$real_web_install_fixture" <<'EOF'
#!/bin/sh
set -eu
repository_root=$1
script_root=$2
initial_digest_file=$3
wrong_digest_file=$4
rebuilt_digest_file=$5
second_log=$6
. "$script_root/scripts/go-gate-environment.sh"
. "$script_root/scripts/go-fast-stage.sh"
go_gate_environment_setup
trap 'go_gate_environment_cleanup || true' EXIT
CDPATH= cd -- "$repository_root/web"
export COREPACK_ENABLE_NETWORK=1 npm_config_offline=false NPM_CONFIG_OFFLINE=false
go_gate_web_install
real_tsc_package=$(/bin/realpath "$repository_root/web/node_modules/typescript/package.json")
real_tsc_root=$(/usr/bin/dirname "$real_tsc_package")
real_initial_digest=$("$go_gate_node" --input-type=module --eval 'const { toolTreeDigest } = await import(process.argv[2]); process.stdout.write(`${toolTreeDigest(process.argv[3])}\n`);' /dev/null "$script_root/web/scripts/package-artifacts.mjs" "$real_tsc_root")
printf '%s\n' "$real_initial_digest" >"$initial_digest_file"
/usr/bin/find "$real_tsc_root" -type d -exec /bin/chmod 700 {} +
/usr/bin/find "$real_tsc_root" -type f -exec /bin/chmod 600 {} +
/usr/bin/find "$real_tsc_root/bin" -type f -exec /bin/chmod 755 {} +
real_wrong_digest=$("$go_gate_node" --input-type=module --eval 'const { toolTreeDigest } = await import(process.argv[2]); process.stdout.write(`${toolTreeDigest(process.argv[3])}\n`);' /dev/null "$script_root/web/scripts/package-artifacts.mjs" "$real_tsc_root")
printf '%s\n' "$real_wrong_digest" >"$wrong_digest_file"
export COREPACK_ENABLE_NETWORK=0 npm_config_offline=true NPM_CONFIG_OFFLINE=true
go_gate_web_install >"$second_log" 2>&1
real_rebuilt_digest=$("$go_gate_node" --input-type=module --eval 'const { toolTreeDigest } = await import(process.argv[2]); process.stdout.write(`${toolTreeDigest(process.argv[3])}\n`);' /dev/null "$script_root/web/scripts/package-artifacts.mjs" "$real_tsc_root")
printf '%s\n' "$real_rebuilt_digest" >"$rebuilt_digest_file"
/usr/bin/grep -F 'downloaded 0' "$second_log" >/dev/null
if /usr/bin/grep -Eq 'downloaded [1-9]' "$second_log"; then exit 61; fi
EOF
/bin/chmod 700 "$real_web_install_fixture"
/bin/mkdir -p "$temporary/real-web-install-root/web/packages/ui" "$temporary/real-web-install-root/web/packages/client" "$temporary/real-web-install-root/web/apps/dev"
/bin/cp "$repository_root/web/package.json" "$temporary/real-web-install-root/web/package.json"
/bin/cp "$repository_root/web/pnpm-lock.yaml" "$temporary/real-web-install-root/web/pnpm-lock.yaml"
/bin/cp "$repository_root/web/pnpm-workspace.yaml" "$temporary/real-web-install-root/web/pnpm-workspace.yaml"
for real_workspace in packages/ui packages/client apps/dev; do
    /bin/cp "$repository_root/web/$real_workspace/package.json" "$temporary/real-web-install-root/web/$real_workspace/package.json"
done
/bin/sh "$real_web_install_fixture" "$temporary/real-web-install-root" "$repository_root" \
    "$temporary/real-initial-digest" "$temporary/real-wrong-digest" "$temporary/real-rebuilt-digest" "$temporary/real-second-install.log"

real_node_modules_root=$(/bin/realpath "$temporary/real-web-install-root/web/node_modules") || fail "real install node_modules realpath failed"
[ -n "$real_node_modules_root" ] && [ "$real_node_modules_root" = "$temporary/real-web-install-root/web/node_modules" ] || fail "real install node_modules escaped its root"
real_tsc_package=$(/bin/realpath "$temporary/real-web-install-root/web/node_modules/typescript/package.json") || fail "real install TypeScript realpath failed"
[ -n "$real_tsc_package" ] || fail "real install TypeScript realpath was empty"
case "$real_tsc_package" in
    "$real_node_modules_root"/*) ;;
    *) fail "real install TypeScript escaped node_modules" ;;
esac
real_tsc_root=$(/usr/bin/dirname "$real_tsc_package")
[ -n "$real_tsc_root" ] && [ -d "$real_tsc_root" ] || fail "real install TypeScript root is missing"
case "$real_tsc_root" in
    "$real_node_modules_root"/*) ;;
    *) fail "real install TypeScript root escaped node_modules" ;;
esac
real_reviewed_digest=$(/bin/cat "$repository_root/web/toolchain-integrity.json" | /usr/bin/awk -F'"' '/"treeSha512"/ { count++; if (count == 2) print $4 }')
[ -n "$real_reviewed_digest" ] || fail "reviewed TypeScript digest was not read"
real_initial_digest=$(/bin/cat "$temporary/real-initial-digest")
[ "$real_initial_digest" = "$real_reviewed_digest" ] || fail "fresh real pnpm install did not match reviewed TypeScript tree"
real_wrong_digest=$(/bin/cat "$temporary/real-wrong-digest")
[ "$real_wrong_digest" != "$real_reviewed_digest" ] || fail "mode perturbation did not change the reviewed digest"
real_rebuilt_digest=$(/bin/cat "$temporary/real-rebuilt-digest")
[ "$real_rebuilt_digest" = "$real_reviewed_digest" ] || fail "real cached pnpm reconstruction did not restore reviewed TypeScript tree"
[ "$(/usr/bin/stat -f '%Lp' "$real_tsc_root")" = 755 ] || fail "reconstructed TypeScript root mode is not 0755"
[ "$(/usr/bin/stat -f '%Lp' "$real_tsc_root/package.json")" = 644 ] || fail "reconstructed TypeScript package mode is not 0644"
[ "$(/usr/bin/stat -f '%Lp' "$real_tsc_root/bin/tsc")" = 755 ] || fail "reconstructed TypeScript compiler mode is not 0755"
[ "$(/usr/bin/stat -f '%Lp' "$real_tsc_root/lib/typescript.js")" = 644 ] || fail "reconstructed TypeScript library mode is not 0644"

# Run the same helper against the checked-out web tree before the offline
# artifact proof. This covers an absent or already-correct candidate without
# using the disposable perturbation as a substitute for public bytes.
real_web_install_once_fixture="$temporary/real-web-install-once-fixture"
/bin/cat >"$real_web_install_once_fixture" <<'EOF'
#!/bin/sh
set -eu
repository_root=$1
script_root=$2
. "$script_root/scripts/go-gate-environment.sh"
. "$script_root/scripts/go-fast-stage.sh"
go_gate_environment_setup
trap 'go_gate_environment_cleanup || true' EXIT
CDPATH= cd -- "$repository_root/web"
export COREPACK_ENABLE_NETWORK=1 npm_config_offline=false NPM_CONFIG_OFFLINE=false
go_gate_web_install
EOF
/bin/chmod 700 "$real_web_install_once_fixture"
/bin/sh "$real_web_install_once_fixture" "$repository_root" "$repository_root"
real_artifact_output="$temporary/real-artifacts"
COREPACK_ENABLE_NETWORK=0 "$repository_root/web/scripts/package-artifacts" pack --output "$real_artifact_output" >/dev/null
COREPACK_ENABLE_NETWORK=0 "$repository_root/web/scripts/package-artifacts" verify --output "$real_artifact_output" >/dev/null

/usr/bin/grep -F 'go_gate_go=/opt/homebrew/bin/go' "$repository_root/scripts/go-gate-environment.sh" >/dev/null || fail "Go path is not fixed"
/usr/bin/grep -F 'command -v node' "$repository_root/scripts/go-gate-environment.sh" >/dev/null || fail "Node was not captured before isolation"
/usr/bin/grep -F 'command -v corepack' "$repository_root/scripts/go-gate-environment.sh" >/dev/null || fail "Corepack was not captured before isolation"
/usr/bin/grep -F '/usr/bin/dirname' "$repository_root/scripts/go-ci.sh" >/dev/null || fail "go-ci bootstrap uses ambient dirname"
/usr/bin/grep -F 'go_gate_supervisor_pid' "$repository_root/scripts/go-ci-owned.sh" >/dev/null || fail "go-ci signal join is missing"
/usr/bin/grep -F '$SIG{TERM}' "$repository_root/scripts/go-gate-environment.sh" >/dev/null || fail "supervisor TERM handler is missing"
/usr/bin/grep -F 'if (!setpgid(0, 0))' "$repository_root/scripts/go-gate-environment.sh" >/dev/null || fail "child setpgid errors are not checked"
/usr/bin/grep -F 'if (!setpgid($pid, $pid))' "$repository_root/scripts/go-gate-environment.sh" >/dev/null || fail "parent setpgid errors are not checked"
/usr/bin/grep -F 'my $group_clean = stop_group();' "$repository_root/scripts/go-gate-environment.sh" >/dev/null || fail "post-exit group census is missing"
/usr/bin/grep -F '. "$script_dir/go-fast-stage.sh"' "$repository_root/scripts/go-ci-owned.sh" >/dev/null || fail "go-ci does not share fast stage"
if /usr/bin/grep -F 'go-check.sh' "$repository_root/scripts/go-ci.sh" >/dev/null; then fail "go-ci still spawns a second gate"; fi
if /usr/bin/grep -F 'DARK_FACTORY_LOCAL_CI_LEASE_HELD' "$repository_root/scripts/go-ci.sh" >/dev/null; then fail "go-ci still exposes lease bypass"; fi
/usr/bin/grep -F 'exec "$script_dir/with-local-ci-lease.sh" /bin/sh "$script_dir/go-ci-owned.sh"' "$repository_root/scripts/go-ci.sh" >/dev/null || fail "go-ci does not invoke the kernel lease"
[ -x "$repository_root/scripts/go-ci.sh" ] || fail "official go-ci lost executable mode"
[ -x "$repository_root/scripts/go-ci-owned.sh" ] || fail "owned go-ci body lost executable mode"
if /usr/bin/grep -F 'internal/processcontract' "$repository_root/scripts/go-ci.sh" >/dev/null; then fail "go-ci duplicated full package proof"; fi
/usr/bin/grep -F 'final system census remains a cutover-only gate' "$repository_root/scripts/go-ci-owned.sh" >/dev/null || fail "pending cutover evidence is not explicit"

for web_script in typecheck build test; do
    /usr/bin/grep -F "\"$web_script\": \"corepack pnpm " "$repository_root/web/package.json" >/dev/null \
        || fail "web $web_script does not use the fixed Corepack invocation"
done
if /usr/bin/grep -E '"(typecheck|build|test)": "pnpm ' "$repository_root/web/package.json" >/dev/null; then
    fail "web scripts depend on an ambient pnpm executable"
fi
if /usr/bin/grep -F 'execFileSync("pnpm"' "$repository_root/web/packages/ui/test/packed-consumer.test.mjs" >/dev/null; then
    fail "packed UI test depends on an ambient pnpm executable"
fi
/usr/bin/grep -F 'execFileSync("corepack", ["pnpm", "pack"' "$repository_root/web/packages/ui/test/packed-consumer.test.mjs" >/dev/null \
    || fail "packed UI test does not use the fixed Corepack invocation"

/bin/sh -n "$repository_root/scripts/go-check.sh" "$repository_root/scripts/go-ci.sh" \
    "$repository_root/scripts/go-fast-stage.sh" "$repository_root/scripts/go-gate-environment.sh" "$0"
echo "go gate tests passed"
