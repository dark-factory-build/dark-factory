#!/bin/sh
set -eu

repository_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
boundary=$repository_root/scripts/local-ci-environment.sh
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-local-ci-environment.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM

fail() {
    echo "local-ci environment test failed: $*" >&2
    exit 1
}

live_names='DARK_FACTORY_HOME
DARK_FACTORY_SOCKET
DARK_FACTORY_PROJECT
DARK_FACTORY_AGENT
DARK_FACTORY_SESSION
DARK_FACTORY_SESSION_TOKEN_FILE
DARK_FACTORY_AGENT_DIR
DARK_FACTORY_FACTORYCTL
DARK_FACTORY_TASK
DARK_FACTORY_RUN'

child_environment=$temporary/child.env
(
    for name in $live_names; do
        export "$name=hostile live value; \$(must remain data)"
    done
    export DARK_FACTORY_LOCAL_CI_TEST_SENTINEL=preserved-local-ci-test-seam
    export DARK_FACTORY_LOCAL_CI_LEASE_HELD=1
    export DARK_FACTORY_LOCAL_CI_TEST_PAUSE_AFTER_LOCKF=/hostile/pause
    export CARGO_TARGET_DIR=/intentional/build-target
    export RUSTUP_TOOLCHAIN=1.88.0
    export CODEX_HOME=/intentional/provider-home
    export OPENAI_API_KEY=preserved-fake-provider-credential
    export DARK_FACTORY_UPDATE_URL=https://fixture.invalid/manifest.json
    export HOME=/intentional/home
    export npm_config_userconfig=/intentional/npmrc
    export npm_config_globalconfig=/intentional/global-npmrc
    export NETRC=/intentional/netrc
    export COREPACK_HOME=/intentional/corepack
    export GOPROXY=https://fixture.invalid/proxy
    # shellcheck source=scripts/local-ci-environment.sh
    . "$boundary"
    env
) >"$child_environment"

for name in $live_names; do
    if grep -q "^$name=" "$child_environment"; then
        fail "$name reached a child gate command"
    fi
done

for expected in \
    'DARK_FACTORY_LOCAL_CI_TEST_SENTINEL=preserved-local-ci-test-seam' \
    'DARK_FACTORY_LOCAL_CI_LEASE_HELD=1' \
    'DARK_FACTORY_LOCAL_CI_TEST_PAUSE_AFTER_LOCKF=/hostile/pause'
do
    grep -F -x "$expected" "$child_environment" >/dev/null \
        || fail "intentional input was removed: ${expected%%=*}"
done

for forbidden in \
    'CARGO_TARGET_DIR=' 'RUSTUP_TOOLCHAIN=' 'CODEX_HOME=' \
    'OPENAI_API_KEY=' 'DARK_FACTORY_UPDATE_URL=' 'HOME=/intentional/home' \
    'npm_config_userconfig=/intentional/npmrc' 'COREPACK_HOME=/intentional/corepack' \
    'npm_config_globalconfig=/intentional/global-npmrc' 'NETRC=/intentional/netrc' \
    'GOPROXY=https://fixture.invalid/proxy'; do
    if grep -F "$forbidden" "$child_environment" >/dev/null; then
        fail "hostile environment crossed boundary: ${forbidden%%=*}"
    fi
done

grep -F -x 'HOME=/var/empty' "$child_environment" >/dev/null \
    || fail "safe isolated HOME was not installed"
grep -F -x "DF_CI_CACHE_ROOT=$repository_root/.tools/local-ci" "$child_environment" >/dev/null \
    || fail "worktree-local cache root was not installed"
grep -F -x "GOCACHE=$repository_root/.tools/local-ci/go-build" "$child_environment" >/dev/null \
    || fail "safe isolated Go build cache was not installed"
grep -F -x "GOMODCACHE=$repository_root/.tools/local-ci/go-mod" "$child_environment" >/dev/null \
    || fail "safe isolated Go module cache was not installed"
grep -F -x 'NPM_CONFIG_USERCONFIG=/var/empty/.npmrc' "$child_environment" >/dev/null \
    || fail "safe npm config was not installed"
grep -F -x 'NPM_CONFIG_GLOBALCONFIG=/var/empty/.npmrc-global' "$child_environment" >/dev/null \
    || fail "safe global npm config was not installed"
grep -F -x 'NETRC=/dev/null' "$child_environment" >/dev/null \
    || fail "safe netrc boundary was not installed"
grep -F -x 'XDG_CONFIG_HOME=/var/empty' "$child_environment" >/dev/null \
    || fail "host config was not isolated"
grep -F -x "XDG_DATA_HOME=$repository_root/.tools/local-ci/data" "$child_environment" >/dev/null \
    || fail "safe XDG data directory was not installed"
grep -F -x "XDG_STATE_HOME=$repository_root/.tools/local-ci/state" "$child_environment" >/dev/null \
    || fail "safe XDG state directory was not installed"

fixture=$temporary/fixture
/bin/mkdir -p "$fixture/scripts" "$fixture/.tools-target"
/bin/cp "$boundary" "$fixture/scripts/local-ci-environment.sh"
/bin/cat >"$fixture/scripts/entry.sh" <<'EOF'
#!/bin/sh
set -eu
boundary=$(CDPATH= cd -- "$(dirname "$0")" && pwd)/local-ci-environment.sh
. "$boundary"
. "$boundary"
printf '%s\n' "$DF_CI_CACHE_ROOT"
EOF
/bin/chmod 755 "$fixture/scripts/entry.sh"

cache_root_one=$(CDPATH='' cd -- "$fixture" && /bin/sh ./scripts/entry.sh)
cache_root_two=$cache_root_one
fixture_root=$(CDPATH='' cd -- "$fixture" && pwd -P)
[ "$cache_root_one" = "$fixture_root/.tools/local-ci" ] || fail "direct cache root changed"
[ "$cache_root_two" = "$cache_root_one" ] || fail "nested cache root changed"

/bin/rm -rf "$fixture/.tools"
/bin/ln -s "$fixture/.tools-target" "$fixture/.tools"
set +e
symlink_output=$(CDPATH='' cd -- "$fixture" && /bin/sh ./scripts/entry.sh 2>&1)
symlink_status=$?
set -e
[ "$symlink_status" -ne 0 ] || fail "symlink .tools path was accepted"
printf '%s\n' "$symlink_output" | grep -F 'refusing unsafe .tools path' >/dev/null \
    || fail "symlink .tools refusal was unclear: $symlink_output"
/bin/rm "$fixture/.tools"
/bin/mkdir "$fixture/.tools"
/bin/ln -s "$fixture/.tools-target" "$fixture/.tools/local-ci"
set +e
symlink_output=$(CDPATH='' cd -- "$fixture" && /bin/sh ./scripts/entry.sh 2>&1)
symlink_status=$?
set -e
[ "$symlink_status" -ne 0 ] || fail "symlink cache path was accepted"
printf '%s\n' "$symlink_output" | grep -F 'refusing unsafe .tools/local-ci path' >/dev/null \
    || fail "symlink cache refusal was unclear: $symlink_output"

/bin/rm -rf "$fixture/.tools"
/bin/mkdir -p "$fixture/.tools/local-ci"
/bin/ln -s "$fixture/.tools-target" "$fixture/.tools/local-ci/go-build"
set +e
symlink_output=$(CDPATH='' cd -- "$fixture" && /bin/sh ./scripts/entry.sh 2>&1)
symlink_status=$?
set -e
[ "$symlink_status" -ne 0 ] || fail "symlink cache child was accepted"
printf '%s\n' "$symlink_output" | grep -F 'refusing unsafe .tools/local-ci/go-build path' >/dev/null \
    || fail "symlink cache child refusal was unclear: $symlink_output"
[ "$(/usr/bin/find "$fixture/.tools/local-ci" -mindepth 1 -maxdepth 1 | /usr/bin/wc -l | /usr/bin/tr -d ' ')" -eq 1 ] \
    || fail "symlink cache child caused partial cache writes"

/bin/rm -rf "$fixture/.tools"
/bin/mkdir -p "$fixture/.tools/local-ci"
: >"$fixture/.tools/local-ci/go-mod"
set +e
file_output=$(CDPATH='' cd -- "$fixture" && /bin/sh ./scripts/entry.sh 2>&1)
file_status=$?
set -e
[ "$file_status" -ne 0 ] || fail "regular-file cache child was accepted"
printf '%s\n' "$file_output" | grep -F 'refusing unsafe .tools/local-ci/go-mod path' >/dev/null \
    || fail "regular-file cache child refusal was unclear: $file_output"
[ "$(/usr/bin/find "$fixture/.tools/local-ci" -mindepth 1 -maxdepth 1 | /usr/bin/wc -l | /usr/bin/tr -d ' ')" -eq 1 ] \
    || fail "regular-file cache child caused partial cache writes"

sh -n "$boundary" "$repository_root/scripts/local-ci.sh"
echo "local-ci environment tests passed"
