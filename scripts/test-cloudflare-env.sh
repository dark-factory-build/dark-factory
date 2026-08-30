#!/bin/sh
set -eu

repository_root=$(CDPATH='' cd -- "$(/usr/bin/dirname "$0")/.." && pwd -P)
temporary=$(/usr/bin/mktemp -d "/tmp/dark-factory-cloudflare-env-test.XXXXXX")
trap '/bin/rm -rf "$temporary"' EXIT HUP INT TERM

fail() {
    echo "Cloudflare environment test failed: $*" >&2
    exit 1
}

fixture="$temporary/repository"
/bin/mkdir -p "$fixture/scripts" "$fixture/cmd" "$fixture/internal"
/bin/cp "$repository_root/go.mod" "$repository_root/go.sum" "$fixture/"
/bin/cp "$repository_root/scripts/with-cloudflare-env.sh" "$fixture/scripts/"
/bin/cp "$repository_root/scripts/cloudflare-env-clean.sh" "$fixture/scripts/"
/bin/cp -R "$repository_root/cmd/cloudflare-admin" "$fixture/cmd/"
/bin/cp -R "$repository_root/internal/cloudflareadmin" "$fixture/internal/"
/bin/chmod 755 "$fixture/scripts/with-cloudflare-env.sh"

# Commit a fixture-only observer into the inner script. If the public launcher
# does not clear its caller's environment before starting that script, this
# records the canary without exposing any real credential.
observed_environment="$temporary/inner-environment"
/usr/bin/awk -v observed="$observed_environment" '
    NR == 2 { printf "/usr/bin/env >\"%s\"\n", observed }
    { print }
' "$fixture/scripts/cloudflare-env-clean.sh" >"$temporary/cloudflare-env-clean.sh"
/bin/mv "$temporary/cloudflare-env-clean.sh" "$fixture/scripts/cloudflare-env-clean.sh"
/usr/bin/git -C "$fixture" init -q
/usr/bin/git -C "$fixture" add go.mod go.sum scripts cmd internal
/usr/bin/git -C "$fixture" -c user.name=fixture -c user.email=fixture@example.invalid \
    commit -qm fixture

helper="$fixture/scripts/with-cloudflare-env.sh"
poison="$temporary/poison"
/bin/mkdir "$poison"
/bin/cat >"$poison/go" <<EOF
#!/bin/sh
touch "$temporary/poison-ran"
exit 99
EOF
/bin/chmod 700 "$poison/go"

if PATH="$poison" DARK_FACTORY_CLOUDFLARE_ENV_STAGE=clean \
    DARK_FACTORY_CLOUDFLARE_ENV_COMMAND=/usr/bin/false \
    DARK_FACTORY_CLOUDFLARE_CURL=/usr/bin/false \
    DARK_FACTORY_CLOUDFLARE_JQ=/usr/bin/false \
    "$helper" wrangler auth token >"$temporary/forbidden.log" 2>&1; then
    fail "helper admitted a forbidden Wrangler command"
fi
/usr/bin/grep -F 'usage: scripts/with-cloudflare-env.sh dns status' \
    "$temporary/forbidden.log" >/dev/null || fail "forbidden command did not fail with exact usage"
[ ! -e "$temporary/poison-ran" ] || fail "helper used the ambient PATH"

if PATH="$poison" DARK_FACTORY_CLOUDFLARE_ENV_STAGE=clean \
    DARK_FACTORY_CLOUDFLARE_ENV_COMMAND=/usr/bin/false \
    DARK_FACTORY_CLOUDFLARE_CURL=/usr/bin/false \
    DARK_FACTORY_CLOUDFLARE_JQ=/usr/bin/false \
    DARK_FACTORY_AMBIENT_SECRET_CANARY=must-not-cross \
    CLOUDFLARE_API_TOKEN=ambient-token-must-not-cross \
    CLOUDFLARE_ACCOUNT_ID=ambient-account-must-not-cross \
    "$helper" dns status >"$temporary/missing.log" 2>&1; then
    fail "helper reached success without a credential file"
fi
/usr/bin/grep -F 'read Cloudflare credentials' "$temporary/missing.log" >/dev/null \
    || fail "committed helper source did not build and reach its credential boundary"
[ ! -e "$temporary/poison-ran" ] || fail "helper used the ambient PATH during its build"
[ -f "$observed_environment" ] || fail "clean-environment observer did not run"
/usr/bin/grep -F 'DARK_FACTORY_CLOUDFLARE_CLEAN_LAUNCH=reviewed-v1' \
    "$observed_environment" >/dev/null || fail "launcher did not establish the clean boundary"
if /usr/bin/grep -Eq \
    '^(CLOUDFLARE_|DARK_FACTORY_AMBIENT_SECRET_CANARY=|DARK_FACTORY_CLOUDFLARE_ENV_)' \
    "$observed_environment"; then
    fail "ambient credential or test hook crossed the clean launcher"
fi

/usr/bin/printf '\n# dirty clean-environment helper\n' \
    >>"$fixture/scripts/cloudflare-env-clean.sh"
if "$helper" dns status >"$temporary/dirty-inner.log" 2>&1; then
    fail "helper admitted mutable clean-environment source"
fi
/usr/bin/grep -F 'helper source differs from the reviewed commit' \
    "$temporary/dirty-inner.log" >/dev/null \
    || fail "mutable clean-environment source refusal was not explicit"
/usr/bin/git -C "$fixture" show HEAD:scripts/cloudflare-env-clean.sh \
    >"$temporary/cloudflare-env-clean.sh"
/bin/mv "$temporary/cloudflare-env-clean.sh" "$fixture/scripts/cloudflare-env-clean.sh"

/usr/bin/printf '\n// dirty helper source\n' >>"$fixture/internal/cloudflareadmin/dns.go"
if "$helper" dns status >"$temporary/dirty.log" 2>&1; then
    fail "helper admitted mutable worktree source"
fi
/usr/bin/grep -F 'helper source differs from the reviewed commit' "$temporary/dirty.log" >/dev/null \
    || fail "mutable-source refusal was not explicit"
/usr/bin/git -C "$fixture" show HEAD:internal/cloudflareadmin/dns.go \
    >"$temporary/dns.go"
/bin/mv "$temporary/dns.go" "$fixture/internal/cloudflareadmin/dns.go"

# `git diff` is quiet for an untracked path. Prove the reviewed tree must
# contain the inner helper rather than accepting an untracked replacement.
/usr/bin/git -C "$fixture" rm --cached -q scripts/cloudflare-env-clean.sh
/usr/bin/git -C "$fixture" -c user.name=fixture -c user.email=fixture@example.invalid \
    commit -qm 'untrack clean helper'
if "$helper" dns status >"$temporary/untracked-inner.log" 2>&1; then
    fail "helper admitted an untracked clean-environment replacement"
fi
/usr/bin/grep -F 'clean-environment helper is not committed at the reviewed commit' \
    "$temporary/untracked-inner.log" >/dev/null \
    || fail "untracked clean-environment helper refusal was not explicit"

# The assertion intentionally matches the literal shell variables.
# shellcheck disable=SC2016
/usr/bin/grep -F 'archive --format=tar --output="$archive" "$reviewed_commit"' \
    "$fixture/scripts/cloudflare-env-clean.sh" >/dev/null \
    || fail "helper archive was not pinned to the captured commit"
/usr/bin/grep -F 'verify_reviewed_tree' "$fixture/scripts/cloudflare-env-clean.sh" >/dev/null \
    || fail "helper did not re-verify the worktree binding"

echo "Cloudflare environment tests passed"
