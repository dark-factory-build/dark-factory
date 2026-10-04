#!/bin/sh
# scripts/release.sh against a fixture checkout with fake wrangler, curl,
# npm, and sleep on PATH. Nothing here reaches Cloudflare or the network.
set -eu

repository_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd -P)
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-release-test.XXXXXX")
trap 'rm -rf "$temporary"' EXIT
fail() { echo "release test failed: $*" >&2; exit 1; }

fixture=$temporary/repository
bin=$temporary/bin
mkdir -p "$fixture/scripts" "$fixture/control-plane/scripts" "$bin"
cp "$repository_root/scripts/release.sh" "$fixture/scripts/"
# shellcheck disable=SC2016
printf '#!/bin/sh\n[ -z "${FAKE_RACE_BUILD:-}" ] || echo other-id >"$FAKE_LIVE"\n[ -z "${FAKE_BUILD_FAIL:-}" ]\n' >"$fixture/control-plane/scripts/local-ci.sh"
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
case "$1 $2" in
    "deployments status")
        [ -z "${FAKE_AUTH_FAIL:-}" ] || { echo "not authenticated" >&2; exit 1; }
        printf '{"versions":[{"version_id":"%s","percentage":100}]}\n' "$(cat "$FAKE_LIVE")" ;;
    "versions view") printf '{"annotations":{"workers/tag":"%s"}}\n' "$FAKE_LIVE_TAG" ;;
    "versions upload") echo "Worker Version ID: new-id" ;;
    "versions deploy") [ -z "${FAKE_DEPLOY_FAIL:-}" ] || exit 1; echo "${3%@100%}" >"$FAKE_LIVE" ;;
    *) exit 9 ;;
esac
EOF
cat >"$bin/curl" <<'EOF'
#!/bin/sh
case "$*" in
    *healthz*) printf 200 ;;
    *readyz*) [ -z "${FAKE_RACE_AFTER:-}" ] || echo other-id >"$FAKE_LIVE"
        [ -n "${FAKE_NOT_READY:-}" ] && echo '{"status":"degraded"}' \
        || echo '{"status":"ready","maintainer_operations":"mcp_installation_bound_operator_and_headless"}' ;;
esac
EOF
printf '#!/bin/sh\n' >"$bin/npm"
printf '#!/bin/sh\n' >"$bin/sleep"
chmod 755 "$bin"/*

run() {
    : >"$temporary/log"
    echo old-id >"$temporary/live"
    (cd "$fixture" && env PATH="$bin:$PATH" FAKE_LOG="$temporary/log" FAKE_LIVE="$temporary/live" "$@" \
        ./scripts/release.sh "$commit" >"$temporary/out" 2>&1)
}
deploys() { grep -c '^versions deploy' "$temporary/log" || true; }

run FAKE_LIVE_TAG="$tag" || fail "same tree did not succeed"
grep -q '^versions upload' "$temporary/log" && fail "same tree uploaded"
[ "$(deploys)" = 0 ] || fail "same tree deployed"

run FAKE_LIVE_TAG=cp-old || fail "new tree did not succeed: $(cat "$temporary/out")"
grep -Fxq "versions upload --name dark-factory-control-plane --tag $tag --message commit $commit" \
    "$temporary/log" || fail "upload was not tagged with the tree"
grep -q '^versions deploy new-id@100% ' "$temporary/log" || fail "new version not deployed"
[ "$(deploys)" = 1 ] || fail "new tree deployed more than once"

if run FAKE_LIVE_TAG=cp-old FAKE_NOT_READY=1; then fail "readiness failure succeeded"; fi
grep -q '^versions deploy old-id@100% ' "$temporary/log" || fail "previous version not redeployed"
grep -Fxq 'release: control-plane health_failed rolled back to old-id' "$temporary/out" \
    || fail "untyped readiness failure: $(cat "$temporary/out")"

if run FAKE_LIVE_TAG=cp-old FAKE_AUTH_FAIL=1; then fail "auth failure succeeded"; fi
grep -q '^release: control-plane auth_failed' "$temporary/out" || fail "untyped auth failure"
[ "$(deploys)" = 0 ] || fail "auth failure deployed"

if run FAKE_LIVE_TAG=cp-old FAKE_BUILD_FAIL=1; then fail "build failure succeeded"; fi
grep -Fxq 'release: control-plane build_failed' "$temporary/out" || fail "untyped build failure"
[ "$(deploys)" = 0 ] || fail "build failure deployed"

if run FAKE_LIVE_TAG=cp-old FAKE_RACE_BUILD=1; then fail "concurrent deploy during build succeeded"; fi
grep -q '^release: control-plane concurrent_deploy' "$temporary/out" || fail "untyped concurrent deploy"
[ "$(deploys)" = 0 ] || fail "promoted over a concurrent deploy"

if run FAKE_LIVE_TAG=cp-old FAKE_NOT_READY=1 FAKE_RACE_AFTER=1; then fail "readiness failure succeeded"; fi
grep -Fxq 'release: control-plane health_failed rollback_skipped: live changed to other-id' \
    "$temporary/out" || fail "untyped skipped rollback: $(cat "$temporary/out")"
[ "$(deploys)" = 1 ] || fail "rolled back over a concurrent deploy"
[ "$(cat "$temporary/live")" = other-id ] || fail "concurrent deploy was overwritten"

if run FAKE_LIVE_TAG=cp-old FAKE_RACE_AFTER=1; then fail "replaced version reported live"; fi
grep -Fxq 'release: control-plane concurrent_deploy after promote (live is other-id)' \
    "$temporary/out" || fail "untyped replacement during polling: $(cat "$temporary/out")"
[ "$(deploys)" = 1 ] || fail "rolled back over a replacement during polling"

if run FAKE_LIVE_TAG=cp-old FAKE_DEPLOY_FAIL=1; then fail "failed deploy succeeded"; fi
grep -Fxq 'release: control-plane deploy_failed (previous old-id still live)' \
    "$temporary/out" || fail "untyped failed deploy: $(cat "$temporary/out")"
[ "$(deploys)" = 1 ] || fail "failed deploy was rolled back"

echo "release step fixtures passed"
