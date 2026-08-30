#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
temporary=$(/usr/bin/mktemp -d /private/tmp/dark-factory-go-gates.XXXXXXXX)
trap '/bin/rm -rf -- "$temporary"' EXIT HUP INT TERM
fail() { echo "go gate fault fixtures failed: $*" >&2; exit 1; }

ordinary="$temporary/ordinary"
/bin/mkdir -p "$ordinary/scripts" "$ordinary/web" "$ordinary/bin"
/bin/cp "$repository_root/scripts/go-check.sh" "$ordinary/scripts/go-check.sh"
/bin/cp "$repository_root/scripts/local-ci-environment.sh" "$ordinary/scripts/local-ci-environment.sh"
printf '%s\n' 'module example.invalid/fault' '' 'go 1.27.0' >"$ordinary/go.mod"
printf '%s\n' 'package fixture' >"$ordinary/fixture.go"

/bin/cat >"$ordinary/bin/go" <<'EOF'
#!/bin/sh
set -eu
case "$1:${2-}" in
    env:GOVERSION) printf '%s\n' go1.27.0 ;;
    mod:download|mod:verify) ;;
    vet:./...)
        [ "${DF_GATE_FAULT-}" != vet ] || { echo 'fixture vet failure' >&2; exit 1; }
        ;;
    test:*)
        [ "${DF_GATE_FAULT-}" != go-test ] || { echo 'fixture Go test failure' >&2; exit 1; }
        ;;
    *) echo "unexpected fake go command: $*" >&2; exit 1 ;;
esac
EOF
/bin/cat >"$ordinary/bin/git" <<'EOF'
#!/bin/sh
set -eu
case "$1" in
    ls-files) printf 'fixture.go\0' ;;
    diff) ;;
    *) echo "unexpected fake git command: $*" >&2; exit 1 ;;
esac
EOF
/bin/cat >"$ordinary/bin/gofmt" <<'EOF'
#!/bin/sh
case "${DF_GATE_FAULT-}" in
    malformed-go) echo 'malformed Go source' >&2; exit 2 ;;
    gofmt) printf '%s\n' fixture.go ;;
esac
EOF
/bin/cat >"$ordinary/bin/corepack" <<'EOF'
#!/bin/sh
case "${DF_GATE_FAULT-}" in
    ts-type) echo 'fixture TypeScript type error' >&2; exit 1 ;;
    ts-test) echo 'fixture TypeScript test failure' >&2; exit 1 ;;
esac
EOF
/bin/chmod 755 "$ordinary/scripts/go-check.sh" "$ordinary/bin/go" "$ordinary/bin/git" \
    "$ordinary/bin/gofmt" "$ordinary/bin/corepack"

run_ordinary() {
    ordinary_mode=$1
    set +e
    ordinary_output=$(CDPATH= cd -- "$ordinary" && \
        DF_GATE_FAULT="$ordinary_mode" PATH="$ordinary/bin:/usr/bin:/bin" \
        /bin/sh ./scripts/go-check.sh 2>&1)
    ordinary_status=$?
    set -e
}
run_ordinary ''
[ "$ordinary_status" -eq 0 ] || fail "ordinary success fixture failed: $ordinary_output"
for ordinary_case in \
    'malformed-go:malformed Go source' \
    'gofmt:gofmt required' \
    'vet:fixture vet failure' \
    'go-test:fixture Go test failure' \
    'ts-type:fixture TypeScript type error' \
    'ts-test:fixture TypeScript test failure'; do
    ordinary_mode=${ordinary_case%%:*}
    ordinary_want=${ordinary_case#*:}
    run_ordinary "$ordinary_mode"
    [ "$ordinary_status" -ne 0 ] || fail "$ordinary_mode fault passed"
    printf '%s\n' "$ordinary_output" | /usr/bin/grep -F "$ordinary_want" >/dev/null \
        || fail "$ordinary_mode fault was unclear: $ordinary_output"
done

process="$temporary/process"
/bin/mkdir -p "$process/scripts" "$process/bin"
/bin/cp "$repository_root/scripts/go-ci-owned.sh" "$process/scripts/go-ci-owned.sh"
/bin/cp "$repository_root/scripts/go-gate-environment.sh" "$process/scripts/go-gate-environment.sh"
/bin/cp "$repository_root/scripts/local-ci-environment.sh" "$process/scripts/local-ci-environment.sh"
/bin/cp "$ordinary/bin/go" "$process/bin/go"
for process_script in go-browser-e2e.sh go-daemon-e2e.sh; do
    printf '%s\n' '#!/bin/sh' 'exit 0' >"$process/scripts/$process_script"
done
/bin/chmod 755 "$process/scripts/"*.sh "$process/bin/go"
set +e
process_output=$(CDPATH= cd -- "$process" && DF_GATE_FAULT=go-test \
    PATH="$process/bin:/usr/bin:/bin" /bin/sh ./scripts/go-ci-owned.sh 2>&1)
process_status=$?
set -e
[ "$process_status" -ne 0 ] || fail "failing process test passed"
printf '%s\n' "$process_output" | /usr/bin/grep -F 'fixture Go test failure' >/dev/null \
    || fail "process failure was unclear: $process_output"

. "$repository_root/scripts/go-gate-environment.sh"
leaker="$temporary/leaker"
leaker_pid_file="$temporary/leaker.pid"
/bin/cat >"$leaker" <<'EOF'
#!/bin/sh
/bin/sleep 30 &
printf '%s\n' "$!" >"$1"
exit 0
EOF
/bin/chmod 755 "$leaker"
set +e
go_gate_stage 5 "$leaker" "$leaker_pid_file"
leaker_status=$?
set -e
[ "$leaker_status" -eq 125 ] || fail "leaked descendant returned $leaker_status, want 125"
leaker_pid=$(/bin/cat "$leaker_pid_file")
if /bin/kill -0 "$leaker_pid" 2>/dev/null; then
    fail "process supervisor left descendant $leaker_pid alive"
fi

local_fixture="$temporary/local"
/bin/mkdir -p "$local_fixture/scripts"
/bin/cp "$repository_root/scripts/local-ci.sh" "$local_fixture/scripts/local-ci.sh"
/bin/cp "$repository_root/scripts/local-ci-environment.sh" "$local_fixture/scripts/local-ci-environment.sh"
/bin/cat >"$local_fixture/scripts/stub" <<'EOF'
#!/bin/sh
name=$(/usr/bin/basename "$0")
case "${DF_GATE_FAULT-}:$name" in
    release:test-package-release.sh) echo 'fixture release proof failure' >&2; exit 1 ;;
    service:go-service-e2e.sh) echo 'fixture service proof failure' >&2; exit 1 ;;
esac
EOF
/bin/chmod 755 "$local_fixture/scripts/local-ci.sh" "$local_fixture/scripts/stub"
for local_child in \
    check-toolchain-pins.sh test-local-ci-environment.sh test-new-worktree.sh \
    test-github-step-summary.sh test-verify-adversarial-review.sh test-inline-chokepoint.sh \
    test-cloudflare-env.sh test-bootstrap-maintainer-v2.sh test-repository-settings.sh \
    test-local-ci-mode.sh test-go-gates.sh go-check.sh test-local-ci-lease.sh \
    test-local-ci-lease-mutations.sh test-go-e2e-tools.sh go-ci-owned.sh \
    test-prepare-release-source.sh test-publish-release.sh test-package-release.sh \
    go-service-e2e.sh; do
    /bin/ln -s stub "$local_fixture/scripts/$local_child"
done

run_local_fault() {
    local_mode=$1
    set +e
    local_output=$(CDPATH= cd -- "$local_fixture" && DARK_FACTORY_LOCAL_CI_LEASE_HELD=1 \
        DF_GATE_FAULT="$local_mode" /bin/sh ./scripts/local-ci.sh 2>&1)
    local_status=$?
    set -e
}
run_local_fault release
[ "$local_status" -ne 0 ] || fail "failing release proof passed"
printf '%s\n' "$local_output" | /usr/bin/grep -F 'fixture release proof failure' >/dev/null \
    || fail "release failure was unclear: $local_output"
run_local_fault service
[ "$local_status" -ne 0 ] || fail "failing service proof passed"
printf '%s\n' "$local_output" | /usr/bin/grep -F 'fixture service proof failure' >/dev/null \
    || fail "service failure was unclear: $local_output"

echo "go gate fault fixtures passed"
