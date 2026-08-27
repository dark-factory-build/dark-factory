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
security_names_file="$temporary/security-names"
cat >"$security_names_file" <<'EOF'
DARK_FACTORY_HOME
DARK_FACTORY_SOCKET
DARK_FACTORY_PROJECT
DARK_FACTORY_AGENT
DARK_FACTORY_SESSION
DARK_FACTORY_SESSION_TOKEN_FILE
DARK_FACTORY_AGENT_DIR
DARK_FACTORY_FACTORYCTL
DARK_FACTORY_TASK
DARK_FACTORY_RUN
DARK_FACTORY_ATTEMPT_TOKEN
DARK_FACTORY_ATTEMPT_TOKEN_FILE
DARK_FACTORY_OPERATOR_TOKEN
DARK_FACTORY_OPERATOR_TOKEN_FILE
DARK_FACTORY_RUN_ID
DARK_FACTORY_TASK_ID
DARK_FACTORY_AGENT_ID
ANTHROPIC_API_KEY
OPENAI_API_KEY
CLAUDE_CONFIG_DIR
CODEX_HOME
GITHUB_TOKEN
GH_TOKEN
SSH_AUTH_SOCK
GIT_ASKPASS
GIT_SSH_COMMAND
GIT_SSH
GIT_CONFIG_GLOBAL
GIT_CONFIG_SYSTEM
GIT_CONFIG_NOSYSTEM
GIT_TERMINAL_PROMPT
NPM_TOKEN
NODE_AUTH_TOKEN
AWS_ACCESS_KEY_ID
AWS_SECRET_ACCESS_KEY
AWS_SESSION_TOKEN
CLAUDE_API_KEY
CLAUDE_CODE_OAUTH_TOKEN
CODEX_API_KEY
GH_ENTERPRISE_TOKEN
GITHUB_ENTERPRISE_TOKEN
SSH_AGENT_PID
SSH_ASKPASS
GIT_CREDENTIAL_HELPER
GIT_SSL_CERT
GIT_SSL_KEY
GIT_SSL_CAINFO
GIT_DIR
GIT_WORK_TREE
GIT_INDEX_FILE
GIT_COMMON_DIR
GIT_CONFIG_COUNT
GH_CONFIG_DIR
GITHUB_CONFIG_DIR
AWS_PROFILE
AWS_SHARED_CREDENTIALS_FILE
AWS_CONFIG_FILE
GOAUTH
GOPRIVATE
GONOSUMDB
GONOPROXY
NETRC
HTTP_PROXY
HTTPS_PROXY
ALL_PROXY
NO_PROXY
npm_config_proxy
npm_config_https_proxy
npm_config_userconfig
NPM_CONFIG_USERCONFIG
EOF

cat >"$fake_bin/go" <<'EOF'
#!/bin/sh
set -eu
printf 'go %s GOPROXY=%s GOSUMDB=%s GOTOOLCHAIN=%s TMPDIR=%s GOTMPDIR=%s GOCACHE=%s GOMODCACHE=%s\n' \
    "$*" "${GOPROXY-}" "${GOSUMDB-}" "${GOTOOLCHAIN-}" "${TMPDIR-}" \
    "${GOTMPDIR-}" "${GOCACHE-}" "${GOMODCACHE-}" >>"$FAKE_GO_LOG"
while IFS= read -r security_name; do
    eval "security_value=\${$security_name+x}"
    if [ -n "$security_value" ]; then
        printf 'env %s=present\n' "$security_name" >>"$FAKE_GO_LOG"
    else
        printf 'env %s=absent\n' "$security_name" >>"$FAKE_GO_LOG"
    fi
done <"$FAKE_SECURITY_NAMES"
case "$1 ${2-}" in
    version*) printf 'go version go%s darwin/arm64\n' "${FAKE_GO_VERSION-1.27.0}"; exit 0 ;;
    mod\ download)
        if [ "${FAKE_GO_SIGNAL-}" = 1 ]; then
            kill -TERM "$PPID"
            exit 143
        fi
        [ "${FAKE_GO_FAIL-}" != download ] || exit 41
        exit 0 ;;
    mod\ verify)
        if [ "${FAKE_GO_SWAP_ROOT-}" = 1 ]; then
            fake_root=${TMPDIR%/tmp}
            mv "$fake_root" "$fake_root.original"
            mkdir "$fake_root"
            chmod 700 "$fake_root"
        fi
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
while IFS= read -r security_name; do
    eval "security_value=\${$security_name+x}"
    if [ -n "$security_value" ]; then
        printf 'gofmt-env %s=present\n' "$security_name" >>"$FAKE_GO_LOG"
    else
        printf 'gofmt-env %s=absent\n' "$security_name" >>"$FAKE_GO_LOG"
    fi
done <"$FAKE_SECURITY_NAMES"
if [ "${FAKE_GO_FAIL-}" = format ]; then
    printf 'internal/fake_format.go\n'
fi
if [ "${FAKE_GO_FAIL-}" = format-error ]; then
    exit 46
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
while IFS= read -r security_name; do
    eval "security_value=\${$security_name+x}"
    if [ -n "$security_value" ]; then
        printf 'corepack-env %s=present\n' "$security_name" >>"$FAKE_GO_LOG"
    else
        printf 'corepack-env %s=absent\n' "$security_name" >>"$FAKE_GO_LOG"
    fi
done <"$FAKE_SECURITY_NAMES"
[ "${FAKE_GO_FAIL-}" != corepack ] || exit 45
exit 0
EOF
chmod +x "$fake_bin/corepack"

run_check() {
    : >"$log"
    env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" \
        FAKE_SECURITY_NAMES="$security_names_file" "$@"
}

assert_absent() {
    [ ! -e "$1" ] && [ ! -L "$1" ] || fail "unexpected path exists: $1"
}

if "$repository_root/scripts/go-check.sh" unexpected >"$temporary/args.out" 2>"$temporary/args.err"; then
    fail "go-check accepted an unexpected argument"
fi
grep -F 'usage: go-check.sh' "$temporary/args.err" >/dev/null \
    || fail "go-check argument rejection had no usage message"
if DARK_FACTORY_GO_GATE_ROOT=/tmp DARK_FACTORY_GO_GATE_NONCE=forged \
    "$repository_root/scripts/go-check.sh" >"$temporary/path.out" 2>"$temporary/path.err"; then
    fail "go-check accepted a non-canonical scratch root"
fi
grep -Eq 'scratch root (escaped /private/tmp|is not physically canonical)' "$temporary/path.err" \
    || fail "non-canonical scratch root was not rejected"

stale_root="/private/tmp/dark-factory-go.stale.$$"
mkdir "$stale_root"
chmod 700 "$stale_root"
printf 'actual-nonce\n' >"$stale_root/.provenance"
chmod 600 "$stale_root/.provenance"
if DARK_FACTORY_GO_GATE_ROOT="$stale_root" DARK_FACTORY_GO_GATE_NONCE=wrong-nonce \
    "$repository_root/scripts/go-check.sh" >"$temporary/stale.out" 2>"$temporary/stale.err"; then
    fail "tampered external root unexpectedly passed"
fi
grep -F 'provenance marker does not match caller' "$temporary/stale.err" >/dev/null \
    || fail "tampered external root was not diagnosed"
[ -d "$stale_root" ] || fail "tampered external root was deleted"
rm -rf "$stale_root"

security_assignments=
while IFS= read -r security_name; do
    security_assignments="$security_assignments $security_name=hostile-secret"
done <"$security_names_file"

# A successful run has the explicit network stage first, then no-network test
# stages, and cleans its canonical scratch root.
run_check env \
    GOTOOLCHAIN=auto \
    $security_assignments \
    "$repository_root/scripts/go-check.sh" 2>"$temporary/success.err" \
    || fail "successful go-check failed"
grep -F 'go mod download GOPROXY=https://proxy.golang.org' "$log" >/dev/null \
    || fail "module download was not explicit"
grep -F 'go vet ./... GOPROXY=off' "$log" >/dev/null \
    || fail "vet did not disable module network access"
grep -F 'go test -timeout=5m -count=1 ./internal/kernel ./internal/browserprotocol ./internal/sqlitecontract GOPROXY=off' "$log" >/dev/null \
    || fail "focused tests did not use the controlled Go environment"
grep -F 'corepack pnpm --offline run typecheck NETWORK=0' "$log" >/dev/null \
    || fail "TypeScript typecheck did not disable network access"
grep -F 'corepack pnpm --offline run test NETWORK=0' "$log" >/dev/null \
    || fail "TypeScript tests did not disable network access"
grep -F 'corepack pnpm install --frozen-lockfile --ignore-scripts NETWORK=1' "$log" >/dev/null \
    || fail "TypeScript dependency install was not the explicit network stage"
grep -F 'TMPDIR=/private/tmp/dark-factory-go.' "$log" >/dev/null \
    || fail "scratch directory was not canonical"
grep -F 'GOTMPDIR=/private/tmp/dark-factory-go.' "$log" >/dev/null \
    || fail "GOTMPDIR was not controlled"
grep -F 'go version ' "$log" | grep -F 'GOTOOLCHAIN=local' >/dev/null \
    || fail "hostile inherited GOTOOLCHAIN was not replaced before version check"
while IFS= read -r security_name; do
    grep -F "env $security_name=absent" "$log" >/dev/null \
        || fail "scrubbed environment reached Go child: $security_name"
done <"$security_names_file"
while IFS= read -r security_name; do
    grep -F "gofmt-env $security_name=absent" "$log" >/dev/null \
        || fail "scrubbed environment reached formatter child: $security_name"
    grep -F "corepack-env $security_name=absent" "$log" >/dev/null \
        || fail "scrubbed environment reached package-manager child: $security_name"
done <"$security_names_file"
download_line=$(grep -n 'go mod download' "$log" | cut -d: -f1)
verify_line=$(grep -n 'go mod verify' "$log" | cut -d: -f1)
format_line=$(grep -n '^gofmt ' "$log" | cut -d: -f1)
vet_line=$(grep -n 'go vet' "$log" | cut -d: -f1)
focused_line=$(grep -n 'go test -timeout=5m' "$log" | cut -d: -f1)
install_line=$(grep -n 'corepack pnpm install --frozen-lockfile --ignore-scripts' "$log" | cut -d: -f1)
typecheck_line=$(grep -n 'corepack pnpm --offline run typecheck' "$log" | cut -d: -f1)
[ "$download_line" -lt "$verify_line" ] && [ "$verify_line" -lt "$format_line" ] \
    && [ "$format_line" -lt "$vet_line" ] && [ "$vet_line" -lt "$focused_line" ] \
    && [ "$focused_line" -lt "$install_line" ] && [ "$install_line" -lt "$typecheck_line" ] \
    || fail "go-check stages ran out of order"
success_tmp=$(sed -n 's/.*TMPDIR=\([^ ]*\).*/\1/p' "$log" | head -1)
success_root=${success_tmp%/tmp}
assert_absent "$success_root"

# Toolchain mismatch fails before dependency access.
: >"$log"
if env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" \
    FAKE_SECURITY_NAMES="$security_names_file" FAKE_GO_VERSION=1.27.1 \
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
if env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" \
    FAKE_SECURITY_NAMES="$security_names_file" FAKE_GO_FAIL=verify \
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

# A signal during a child command invokes the same identity-checked cleanup.
: >"$log"
if env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" \
    FAKE_SECURITY_NAMES="$security_names_file" FAKE_GO_SIGNAL=1 \
    "$repository_root/scripts/go-check.sh" >"$temporary/signal.out" 2>"$temporary/signal.err"; then
    fail "signalled gate unexpectedly passed"
fi
signal_tmp=$(sed -n 's/.*TMPDIR=\([^ ]*\).*/\1/p' "$log" | head -1)
[ -n "$signal_tmp" ] || fail "signalled run did not expose its scratch root"
assert_absent "${signal_tmp%/tmp}"

# A replacement at the owned path must be preserved rather than recursively
# deleted by the cleanup trap.
: >"$log"
if env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" \
    FAKE_SECURITY_NAMES="$security_names_file" FAKE_GO_SWAP_ROOT=1 \
    "$repository_root/scripts/go-check.sh" >"$temporary/swap.out" 2>"$temporary/swap.err"; then
    fail "scratch path replacement unexpectedly passed"
fi
swap_tmp=$(sed -n 's/.*TMPDIR=\([^ ]*\).*/\1/p' "$log" | head -1)
swap_root=${swap_tmp%/tmp}
[ -d "$swap_root" ] || fail "replacement scratch root was deleted"
[ -d "$swap_root.original" ] || fail "original scratch root was not preserved"
rm -rf "$swap_root" "$swap_root.original"

# A formatter that exits nonzero without output must still stop the gate; this
# specifically kills the old `gofmt | tee` false-green mutation.
: >"$log"
if env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" \
    FAKE_SECURITY_NAMES="$security_names_file" FAKE_GO_FAIL=format-error \
    "$repository_root/scripts/go-check.sh" >"$temporary/format-error.out" 2>"$temporary/format-error.err"; then
    fail "nonzero formatter unexpectedly passed"
fi
grep -F 'gofmt failed' "$temporary/format-error.err" >/dev/null \
    || fail "nonzero formatter failure was not diagnosed"
if grep -Fq 'go vet' "$log"; then
    fail "vet ran after nonzero formatter failure"
fi

# Read-only format violations stop before vet.
: >"$log"
if env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" \
    FAKE_SECURITY_NAMES="$security_names_file" FAKE_GO_FAIL=format \
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
if ! env PATH="$fake_bin:$PATH" FAKE_GO_LOG="$log" \
    FAKE_SECURITY_NAMES="$security_names_file" DARK_FACTORY_LOCAL_CI_LEASE_HELD=1 \
    "$repository_root/scripts/go-ci.sh" >"$temporary/ci.out" 2>"$temporary/ci.err"; then
    fail "held-lease go-ci failed"
fi
[ "$(grep -c '^go version ' "$log")" -eq 1 ] || fail "go-ci did not call go-check exactly once"
[ "$(grep -c '^go test -timeout=20m -count=1 -p 1 ./\.\.\.' "$log")" -eq 1 ] \
    || fail "go-ci full tests were not run exactly once"
[ "$(grep -c '^go test -race -timeout=30m -count=1 -p 1 ./\.\.\.' "$log")" -eq 1 ] \
    || fail "go-ci race tests were not run exactly once"
grep -F 'go test -race -timeout=30m' "$log" >/dev/null \
    || fail "go-ci race tests lacked a timeout"
grep -F 'corepack pnpm --offline run typecheck NETWORK=0' "$log" >/dev/null \
    || fail "go-ci TypeScript tests lacked pnpm offline mode"

sh -n "$repository_root/scripts/go-check.sh" "$repository_root/scripts/go-ci.sh" "$0"
echo "go gate tests passed"
