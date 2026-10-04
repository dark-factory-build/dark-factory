#!/bin/sh
# Release step for this repository: deploy the control-plane Worker at <commit>
# when its control-plane tree differs from the live version's tag. Run from a
# clean checkout at <commit>; Wrangler authenticates with the operator's own
# OAuth login under HOME. Any failure after promotion redeploys the version
# that was live before.
set -eu

worker=dark-factory-control-plane
hostname=maintainer.darkfactory.build
export CLOUDFLARE_ACCOUNT_ID=008dfc7e9e8ab2c6028b3aee30ee3f49
export CLOUDFLARE_LOAD_DEV_VARS_FROM_DOT_ENV=false NO_COLOR=1 WRANGLER_SEND_METRICS=false
unset CLOUDFLARE_API_TOKEN

fail() {
    echo "release: control-plane $1" >&2
    exit 1
}

test "$#" = 1 || { echo "usage: scripts/release.sh <commit>" >&2; exit 2; }
cd "$(git rev-parse --show-toplevel)"
commit=$(git rev-parse --verify "$1^{commit}") || fail checkout_mismatch
test "$(git rev-parse HEAD)" = "$commit" || fail checkout_mismatch
test -z "$(git status --porcelain -- control-plane)" || fail checkout_dirty
tag=cp-$(git rev-parse "$commit:control-plane")
cd control-plane
PATH="$PWD/.tools/bin:$PWD/node_modules/.bin:$PATH"
test -x node_modules/.bin/wrangler || npm ci --ignore-scripts >&2 || fail build_failed

json() { node -e 'let s="";process.stdin.on("data",c=>s+=c).on("end",()=>{try{const v=('"$1"')(JSON.parse(s));if(v)process.stdout.write(String(v))}catch{}})'; }
live() {
    wrangler deployments status --name "$worker" --json 2>/dev/null \
        | json 'd=>d.versions.length===1&&d.versions[0].percentage===100&&d.versions[0].version_id' || true
}
previous=$(live)
test -n "$previous" || fail "auth_failed (no single live version at 100%)"
live_tag=$(wrangler versions view "$previous" --name "$worker" --json 2>/dev/null \
    | json 'v=>v.annotations["workers/tag"]') || true
if test "$live_tag" = "$tag"; then
    echo "release: control-plane $tag already live as $previous"
    exit 0
fi

# The authoritative gate installs the pinned worker-build and builds the Worker.
RUSTUP_TOOLCHAIN=1.88.0 ./scripts/local-ci.sh >&2 || fail build_failed
upload=$(DARK_FACTORY_WRANGLER_PREBUILT=1 wrangler versions upload --name "$worker" \
    --tag "$tag" --message "commit $commit") || fail upload_failed
version=$(printf '%s\n' "$upload" | sed -n 's/^Worker Version ID: *//p' | tail -1)
test -n "$version" || fail upload_failed

# Another deployment may land while this one builds or verifies; never
# promote over it or roll it back.
rollback() {
    now=$(live)
    test "$now" != "$previous" || fail "$1 (previous $previous still live)"
    test "$now" = "$version" || fail "$1 rollback_skipped: live changed to ${now:-unknown}"
    if wrangler versions deploy "$previous@100%" --name "$worker" --yes \
        --message "roll back failed release of $commit" >&2; then
        fail "$1 rolled back to $previous"
    fi
    fail "$1 rollback_failed to $previous"
}
now=$(live)
test "$now" = "$previous" || fail "concurrent_deploy (live is ${now:-unknown}, not $previous)"
wrangler versions deploy "$version@100%" --name "$worker" --yes \
    --message "commit $commit" >&2 || rollback deploy_failed

# The headless label proves the inherited service-token binding survived.
for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
    health=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 "https://$hostname/healthz" || true)
    body=$(curl -sS --max-time 30 "https://$hostname/readyz" || true)
    if test "$health" = 200 \
        && printf '%s' "$body" | grep -Fq '"status":"ready"' \
        && printf '%s' "$body" | grep -Fq '"maintainer_operations":"mcp_installation_bound_operator_and_headless"'; then
        now=$(live)
        test "$now" = "$version" || fail "concurrent_deploy after promote (live is ${now:-unknown})"
        echo "release: control-plane $tag live as $version (previous $previous)"
        exit 0
    fi
    test "$attempt" = 12 || sleep 10
done
rollback health_failed
