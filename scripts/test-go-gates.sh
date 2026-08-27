#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-go-gates-test.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM

fail() {
    echo "go gates test failed: $*" >&2
    [ -f "${log-}" ] && cat "$log" >&2
    exit 1
}

fake_bin="$temporary/bin"
mkdir "$fake_bin"
log="$temporary/commands.log"

cat >"$fake_bin/go" <<'EOF'
#!/bin/sh
set -eu
printf 'go %s GOPROXY=%s COREPACK_ENABLE_NETWORK=%s TMPDIR=%s GOTMPDIR=%s GOCACHE=%s GOMODCACHE=%s HOME=%s\n' \
    "$*" "${GOPROXY-}" "${COREPACK_ENABLE_NETWORK-}" "${TMPDIR-}" \
    "${GOTMPDIR-}" "${GOCACHE-}" "${GOMODCACHE-}" "${HOME-}" >>"$FAKE_GO_LOG"
case "$1 ${2-}" in
    version*) printf 'go version go%s darwin/arm64\n' "${FAKE_GO_VERSION-1.27.0}"; exit 0 ;;
    mod\ download)
        [ "${FAKE_GO_FAIL-}" != download ] || exit 41
        exit 0 ;;
    mod\ verify)
        [ "${FAKE_GO_FAIL-}" != verify ] || exit 42
        exit 0 ;;
    vet\ ./...)
        [ "${FAKE_GO_FAIL-}" != vet ] || exit 43
        exit 0 ;;
    test*)
        [ "${FAKE_GO_FAIL-}" != test ] || exit 44
        exit 0 ;;
    *) exit 0 ;;
esac
EOF
chmod +x "$fake_bin/go"

cat >"$fake_bin/gofmt" <<'EOF'
#!/bin/sh
set -eu
printf 'gofmt %s\n' "$*" >>"$FAKE_GO_LOG"
if [ "${FAKE_GO_FAIL-}" = format ]; then
    printf 'internal/fake_format.go\n'
fi
exit 0
EOF
chmod +x "$fake_bin/gofmt"

cat >"$fake_bin/corepack" <<'EOF'
#!/bin/sh
set -eu
printf 'corepack %s NETWORK=%s COREPACK_HOME=%s npm_config_cache=%s\n' \
    "$*" "${COREPACK_ENABLE_NETWORK-}" "${COREPACK_HOME-}" \
    "${npm_config_cache-}" >>"$FAKE_GO_LOG"
[ "${FAKE_GO_FAIL-}" != corepack ] || exit 45
exit 0
EOF
chmod +x "$fake_bin/corepack"

run_check() {
    : >"$log"
    env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" "$@"
}

assert_absent() {
    [ ! -e "$1" ] && [ ! -L "$1" ] || fail "unexpected path exists: $1"
}

if "$repository_root/scripts/go-check.sh" unexpected >"$temporary/args.out" 2>"$temporary/args.err"; then
    fail "go-check accepted an unexpected argument"
fi
grep -F 'usage: go-check.sh' "$temporary/args.err" >/dev/null \
    || fail "go-check argument rejection had no usage message"
if DARK_FACTORY_GO_GATE_ROOT=/tmp \
    "$repository_root/scripts/go-check.sh" >"$temporary/path.out" 2>"$temporary/path.err"; then
    fail "go-check accepted a non-canonical scratch root"
fi
grep -Eq 'scratch root (escaped /private/tmp|is not physically canonical)' "$temporary/path.err" \
    || fail "non-canonical scratch root was not rejected"

# A successful run has the explicit network stage first, then no-network test
# stages, and cleans its canonical scratch root.
run_check env \
    DARK_FACTORY_HOME=/live/home \
    DARK_FACTORY_SOCKET=/live/socket \
    DARK_FACTORY_RUN=live-run \
    DARK_FACTORY_AGENT_DIR=/live/agent \
    "$repository_root/scripts/go-check.sh" 2>"$temporary/success.err" \
    || fail "successful go-check failed"
grep -F 'go mod download GOPROXY=https://proxy.golang.org,direct' "$log" >/dev/null \
    || fail "module download was not explicit"
grep -F 'go vet ./... GOPROXY=off' "$log" >/dev/null \
    || fail "vet did not disable module network access"
grep -F 'go test -count=1 ./internal/browserprotocol ./internal/sqlitecontract GOPROXY=off' "$log" >/dev/null \
    || fail "focused tests did not use the controlled Go environment"
grep -F 'corepack pnpm run typecheck NETWORK=0' "$log" >/dev/null \
    || fail "TypeScript typecheck did not disable network access"
grep -F 'corepack pnpm run test NETWORK=0' "$log" >/dev/null \
    || fail "TypeScript tests did not disable network access"
grep -F 'TMPDIR=/private/tmp/dark-factory-go.' "$log" >/dev/null \
    || fail "scratch directory was not canonical"
grep -F 'GOTMPDIR=/private/tmp/dark-factory-go.' "$log" >/dev/null \
    || fail "GOTMPDIR was not controlled"
if grep -Eq 'DARK_FACTORY_(HOME|SOCKET|RUN|AGENT_DIR)=' "$log"; then
    fail "live Dark Factory override reached a child"
fi
success_tmp=$(sed -n 's/.*TMPDIR=\([^ ]*\).*/\1/p' "$log" | head -1)
success_root=${success_tmp%/tmp}
assert_absent "$success_root"

# Toolchain mismatch fails before dependency access.
: >"$log"
if env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" FAKE_GO_VERSION=1.27.1 \
    "$repository_root/scripts/go-check.sh" >"$temporary/version.out" 2>"$temporary/version.err"; then
    fail "toolchain mismatch unexpectedly passed"
fi
grep -F 'expected Go 1.27.0' "$temporary/version.err" >/dev/null \
    || fail "toolchain mismatch was not diagnosed"
if grep -Eq 'mod download|mod verify|go vet|go test|corepack' "$log"; then
    fail "toolchain mismatch reached a later stage"
fi

# A failed dependency stage stops vet, tests and TypeScript.
: >"$log"
if env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" FAKE_GO_FAIL=verify \
    "$repository_root/scripts/go-check.sh" >"$temporary/failure.out" 2>"$temporary/failure.err"; then
    fail "failed module verification unexpectedly passed"
fi
grep -F 'go mod verify' "$log" >/dev/null || fail "verification was not attempted"
if grep -Eq 'go vet|go test|corepack' "$log"; then
    fail "a later stage ran after module verification failed"
fi
failure_tmp=$(sed -n 's/.*TMPDIR=\([^ ]*\).*/\1/p' "$log" | head -1)
[ -n "$failure_tmp" ] || fail "failed run did not expose its scratch root"
assert_absent "${failure_tmp%/tmp}"

# Read-only format violations stop before vet.
: >"$log"
if env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" FAKE_GO_FAIL=format \
    "$repository_root/scripts/go-check.sh" >"$temporary/format.out" 2>"$temporary/format.err"; then
    fail "format violation unexpectedly passed"
fi
grep -F 'gofmt required' "$temporary/format.err" >/dev/null \
    || fail "format violation was not diagnosed"
if grep -Fq 'go vet' "$log"; then
    fail "vet ran after format failure"
fi

# With the shared lease marker already present, go-ci does not recursively
# acquire it. It invokes go-check once, then exactly one full and one race pass.
: >"$log"
if ! env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" DARK_FACTORY_LOCAL_CI_LEASE_HELD=1 \
    "$repository_root/scripts/go-ci.sh" >"$temporary/ci.out" 2>"$temporary/ci.err"; then
    fail "held-lease go-ci failed"
fi
[ "$(grep -c '^go version ' "$log")" -eq 1 ] || fail "go-ci did not call go-check exactly once"
[ "$(grep -c '^go test -count=1 -p 1 ./\.\.\.' "$log")" -eq 1 ] \
    || fail "go-ci full tests were not run exactly once"
[ "$(grep -c '^go test -race -count=1 -p 1 ./\.\.\.' "$log")" -eq 1 ] \
    || fail "go-ci race tests were not run exactly once"

sh -n "$repository_root/scripts/go-check.sh" "$repository_root/scripts/go-ci.sh" "$0"
echo "go gate tests passed"
