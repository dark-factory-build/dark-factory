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
/bin/cp -R "$repository_root/cmd/cloudflare-admin" "$fixture/cmd/"
/bin/cp -R "$repository_root/internal/cloudflareadmin" "$fixture/internal/"
/bin/chmod 755 "$fixture/scripts/with-cloudflare-env.sh"
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
    "$helper" dns status >"$temporary/missing.log" 2>&1; then
    fail "helper reached success without a credential file"
fi
/usr/bin/grep -F 'read Cloudflare credentials' "$temporary/missing.log" >/dev/null \
    || fail "committed helper source did not build and reach its credential boundary"
[ ! -e "$temporary/poison-ran" ] || fail "helper used the ambient PATH during its build"

/usr/bin/printf '\n// dirty helper source\n' >>"$fixture/internal/cloudflareadmin/dns.go"
if "$helper" dns status >"$temporary/dirty.log" 2>&1; then
    fail "helper admitted mutable worktree source"
fi
/usr/bin/grep -F 'helper source differs from the reviewed commit' "$temporary/dirty.log" >/dev/null \
    || fail "mutable-source refusal was not explicit"

# The assertion intentionally matches the literal shell variables.
# shellcheck disable=SC2016
/usr/bin/grep -F 'archive --format=tar --output="$archive" "$reviewed_commit"' \
    "$fixture/scripts/with-cloudflare-env.sh" >/dev/null \
    || fail "helper archive was not pinned to the captured commit"
/usr/bin/grep -F 'verify_reviewed_tree' "$fixture/scripts/with-cloudflare-env.sh" >/dev/null \
    || fail "helper did not re-verify the worktree binding"

echo "Cloudflare environment tests passed"
