#!/bin/sh
# scripts/release.sh against a fixture checkout with fake wrangler, curl and
# sleep on PATH. Nothing here reaches Cloudflare or the network.
set -eu

repository_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd -P)
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-release-test.XXXXXX")
trap 'rm -rf "$temporary"' EXIT
fail() { echo "release test failed: $*" >&2; exit 1; }

fixture=$temporary/repository
bin=$temporary/bin
mkdir -p "$fixture/scripts" "$fixture/control-plane/scripts" "$bin"
cp "$repository_root/scripts/release.sh" "$fixture/scripts/"
printf '#!/bin/sh\n' >"$fixture/control-plane/scripts/local-ci.sh"
chmod 755 "$fixture/control-plane/scripts/local-ci.sh"
git -C "$fixture" init -q
git -C "$fixture" add -A
git -C "$fixture" -c user.name=fixture -c user.email=fixture@example.invalid \
    -c commit.gpgsign=false commit -qm fixture
commit=$(git -C "$fixture" rev-parse HEAD)
tag=cp-$(git -C "$fixture" rev-parse HEAD:control-plane)

cat >"$bin/wrangler" <<'EOF'
#!/bin/sh
echo "$*" >>"$FAKE_LOG"
[ "$1" != deployments ] || [ -z "${FAKE_AUTH_FAIL:-}" ]
EOF
cat >"$bin/curl" <<'EOF'
#!/bin/sh
case "$*" in
    *healthz*) printf 200 ;;
    *readyz*) [ -n "${FAKE_NOT_READY:-}" ] && echo '{"status":"unavailable"}' \
        || echo '{"status":"ready","maintainer_operations":"mcp_installation_bound_operator_and_headless"}' ;;
esac
EOF
printf '#!/bin/sh\n' >"$bin/sleep"
chmod 755 "$bin"/*

run() {
    : >"$temporary/log"
    (cd "$fixture" && env PATH="$bin:$PATH" FAKE_LOG="$temporary/log" "$@" \
        ./scripts/release.sh "$commit" >"$temporary/out" 2>&1)
}

run || fail "deploy did not succeed: $(cat "$temporary/out")"
grep -Fxq "deploy --tag $tag --message commit $commit" "$temporary/log" || fail "deploy not tagged"
grep -q '^rollback' "$temporary/log" && fail "healthy deploy rolled back"

if run FAKE_NOT_READY=1; then fail "readiness failure succeeded"; fi
grep -q '^rollback --yes' "$temporary/log" || fail "readiness failure not rolled back"
grep -Fxq 'release: control-plane health_failed rolled back' "$temporary/out" || fail "untyped health failure"

if run FAKE_AUTH_FAIL=1; then fail "auth failure succeeded"; fi
grep -Fxq 'release: control-plane auth_failed' "$temporary/out" || fail "untyped auth failure"
grep -q '^deploy --' "$temporary/log" && fail "deployed without auth"

echo "release step fixtures passed"
