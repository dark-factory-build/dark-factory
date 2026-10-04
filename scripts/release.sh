#!/bin/sh
# Deploy the control-plane Worker at <commit> with the operator's Wrangler
# login, rolling back if it does not come up ready. Contract: factoryd's
# release lane runs releases one at a time and is the only supported writer of
# this Worker; out-of-band deploys are unsupported.
set -eu
hostname=maintainer.darkfactory.build
export CLOUDFLARE_ACCOUNT_ID=008dfc7e9e8ab2c6028b3aee30ee3f49 CLOUDFLARE_LOAD_DEV_VARS_FROM_DOT_ENV=false
unset CLOUDFLARE_API_TOKEN
fail() { echo "release: control-plane $1" >&2; exit 1; }

test "$#" = 1 && test "$(git rev-parse HEAD)" = "$(git rev-parse --verify "$1^{commit}")" \
    && test -z "$(git status --porcelain -- control-plane)" || fail "needs a clean checkout at <commit>"
cd "$(git rev-parse --show-toplevel)/control-plane"
PATH="$PWD/.tools/bin:$PWD/node_modules/.bin:$PATH"
RUSTUP_TOOLCHAIN=1.88.0 ./scripts/local-ci.sh >&2 || fail build_failed
wrangler deployments status >/dev/null || fail auth_failed
DARK_FACTORY_WRANGLER_PREBUILT=1 wrangler deploy --tag "cp-$(git rev-parse HEAD:control-plane)" \
    --message "commit $1" >&2 || fail deploy_failed

for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
    test "$(curl -sS -o /dev/null -w '%{http_code}' --max-time 15 "https://$hostname/healthz")" = 200 \
        && curl -sS --max-time 30 "https://$hostname/readyz" \
            | grep -Fq '"maintainer_operations":"mcp_installation_bound_operator_and_headless"' \
        && exit 0
    test "$attempt" = 12 || sleep 10
done
wrangler rollback --yes --message "health check failed for commit $1" >&2 || fail "health_failed rollback_failed"
fail "health_failed rolled back"
