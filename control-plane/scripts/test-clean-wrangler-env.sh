#!/bin/sh
set -eu

script_dir=$(CDPATH='' cd -- "$(dirname "$0")" && pwd)
helper="$script_dir/with-clean-wrangler-env.sh"
fixture=$(/usr/bin/mktemp -d "/tmp/dark-factory-wrangler-test.XXXXXX")
trap '/bin/rm -rf -- "$fixture"' EXIT HUP INT TERM

fail() {
    echo "clean Wrangler environment test failed: $*" >&2
    exit 1
}

/bin/cat >"$fixture/capture.sh" <<'EOF'
#!/bin/sh
/usr/bin/env
"$1"
EOF
/bin/chmod 700 "$fixture/capture.sh"
/bin/mkdir "$fixture/poison"
/bin/cat >"$fixture/poison/node" <<'EOF'
#!/bin/sh
echo POISON_PATH_EXECUTED
exit 91
EOF
/bin/chmod 700 "$fixture/poison/node"
/bin/cat >"$fixture/poison/worker-build" <<'EOF'
#!/bin/sh
echo PRODUCTION_WORKER_BUILD_FOUND
EOF
/bin/chmod 700 "$fixture/poison/worker-build"
/bin/mkdir -p "$fixture/project/build/worker"
/bin/cat >"$fixture/project/build/worker/shim.mjs" <<'EOF'
export default {};
EOF
build_helper="$script_dir/build-worker.sh"

output=$(cd "$fixture/project" && PATH="$fixture/poison" \
    HOME=/operator-home \
    TMPDIR=/operator-tmp \
    CI=fixture-ci \
    CLOUDFLARE_API_TOKEN=ambient-token-must-not-cross \
    CLOUDFLARE_ACCOUNT_ID=ambient-account-must-not-cross \
    WRANGLER_AUTH_TOKEN=ambient-oauth-must-not-cross \
    XDG_CONFIG_HOME=/operator-config \
    SSH_AUTH_SOCK=/operator-keychain-socket \
    NODE_OPTIONS=--require=/operator-loader \
    "$helper" /bin/sh "$fixture/capture.sh" "$build_helper") \
    || fail "clean wrapper did not run"

printf '%s\n' "$output" | grep -Fqx 'CI=fixture-ci' \
    || fail "required CI build variable was not retained"
printf '%s\n' "$output" | grep -Fqx 'PATH=/usr/bin:/bin' \
    || fail "child PATH was not fixed"
printf '%s\n' "$output" | grep -Fqx 'CLOUDFLARE_LOAD_DEV_VARS_FROM_DOT_ENV=false' \
    || fail "Wrangler dotenv loading was not disabled"
if printf '%s\n' "$output" | grep -Fq 'POISON_PATH_EXECUTED'; then
    fail "ambient PATH selected the child executable"
fi
if printf '%s\n' "$output" | grep -Fq 'PRODUCTION_WORKER_BUILD_FOUND'; then
    fail "clean Wrangler rebuilt instead of consuming the verified output"
fi
printf '%s\n' "$output" | grep -Fqx 'DARK_FACTORY_WRANGLER_PREBUILT=1' \
    || fail "the verified prebuilt contract was not retained"
printf '%s\n' "$output" | grep -Fqx 'WRANGLER_SEND_METRICS=false' \
    || fail "local Wrangler telemetry was not disabled"
if printf '%s\n' "$output" | grep -Fvx 'WRANGLER_SEND_METRICS=false' | grep -Eq \
    '^(CLOUDFLARE_API_TOKEN=|CLOUDFLARE_ACCOUNT_ID=|WRANGLER_|XDG_CONFIG_HOME=|SSH_AUTH_SOCK=|NODE_OPTIONS=|HOME=/operator-home|TMPDIR=/operator-tmp)'; then
    fail "ambient credential, keychain, loader, HOME, or temp state crossed the boundary"
fi
printf '%s\n' "$output" | grep -Eq '^HOME=/tmp/dark-factory-wrangler-env\.[^/]+/home$' \
    || fail "child did not receive an isolated HOME"

/usr/bin/touch "$fixture/.dev.vars"
if (cd "$fixture" && "$helper" /bin/sh "$fixture/capture.sh" "$build_helper" >"$fixture/rejected.log" 2>&1); then
    fail "wrapper admitted a local dev-vars file"
fi
/usr/bin/grep -Fq 'refuses local env or dev-vars files' "$fixture/rejected.log" \
    || fail "local dev-vars rejection was not explicit"

production_output=$(cd "$fixture/project" && PATH="$fixture/poison:/usr/bin:/bin" "$build_helper") \
    || fail "production build wrapper did not invoke worker-build"
printf '%s\n' "$production_output" | grep -Fqx 'PRODUCTION_WORKER_BUILD_FOUND' \
    || fail "production build wrapper did not invoke the pinned tool path"

echo "clean Wrangler environment tests passed"
