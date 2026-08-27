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
    timeout) trap '' TERM; (trap '' TERM; sleep 30) & child=$!; printf '%s\n' "$child" >"$3"; wait ;;
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
/bin/cat >"$fast_corepack" <<EOF
#!/bin/sh
printf 'corepack %s NETWORK=%s INTEGRITY=%s\n' "\$*" "\${COREPACK_ENABLE_NETWORK-}" "\${COREPACK_INTEGRITY_KEYS-unset}" >>"$fast_log"
case "\$(/bin/cat "$fast_mode" 2>/dev/null)" in corepack-fail) exit 45 ;; esac
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
go_gate_fast_stage || fail "fast stage fixture failed"
/usr/bin/grep -F 'go mod download GOPROXY=https://proxy.golang.org' "$fast_log" >/dev/null || fail "download did not use network stage"
/usr/bin/grep -F 'go mod verify GOPROXY=off' "$fast_log" >/dev/null || fail "verify was not offline"
/usr/bin/grep -F 'corepack pnpm install --frozen-lockfile --ignore-scripts NETWORK=1' "$fast_log" >/dev/null || fail "install was not the sole package network stage"
/usr/bin/grep -F 'corepack pnpm run test NETWORK=0' "$fast_log" >/dev/null || fail "package tests were not offline"
/usr/bin/grep -F 'INTEGRITY=unset' "$fast_log" >/dev/null || fail "Corepack signature verification was disabled"
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
if go_gate_stage 1 "$fake" "$go_gate_test_log" timeout "$pid_file"; then
    fail "timeout passed"
else
    timeout_status=$?
fi
[ "$timeout_status" -eq 124 ] || fail "timeout returned $timeout_status"
[ -s "$pid_file" ] || fail "timeout descendant was not created"
timeout_pid=$(/bin/cat "$pid_file")
if /bin/kill -0 "$timeout_pid" 2>/dev/null; then fail "timeout descendant survived"; fi

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
signal_child_file="$temporary/signal-child"
/bin/cat >"$signal_fixture" <<'EOF'
#!/bin/sh
set -eu
. "$1"
go_gate_environment_setup
printf '%s\n' "$go_gate_root" >"$3"
go_gate_signal() {
    signal=$1
    trap - EXIT HUP INT TERM
    if [ -n "${go_gate_supervisor_pid-}" ]; then
        /bin/kill -TERM "$go_gate_supervisor_pid" 2>/dev/null || true
        wait "$go_gate_supervisor_pid" 2>/dev/null || true
    fi
    go_gate_environment_cleanup || true
    exit $((128 + signal))
}
trap 'go_gate_signal 15' TERM
go_gate_run_bounded 30 /bin/sh -c "printf active >'$2'; printf '%s\\n' \"\$\$\" >'$4'; /bin/sleep 30"
EOF
/bin/chmod 700 "$signal_fixture"
/bin/sh "$signal_fixture" "$repository_root/scripts/go-gate-environment.sh" "$signal_marker" "$signal_root_file" "$signal_child_file" &
signal_fixture_pid=$!
signal_attempts=0
while [ ! -f "$signal_marker" ] && [ "$signal_attempts" -lt 100 ]; do
    /bin/sleep 0.01
    signal_attempts=$((signal_attempts + 1))
done
[ -f "$signal_marker" ] || fail "signal fixture did not start its stage"
/bin/kill -TERM "$signal_fixture_pid"
if wait "$signal_fixture_pid"; then signal_status=0; else signal_status=$?; fi
[ "$signal_status" -eq 143 ] || fail "TERM fixture returned $signal_status"
signal_child_pid=$(/bin/cat "$signal_child_file")
if /bin/kill -0 "$signal_child_pid" 2>/dev/null; then fail "TERM fixture left its child alive"; fi
[ ! -e "$(/bin/cat "$signal_root_file")" ] || fail "TERM fixture cleaned up before joining supervisor"

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
