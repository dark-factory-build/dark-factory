#!/bin/sh
set -eu

repository_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
boundary=$repository_root/scripts/local-ci-environment.sh
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-local-ci-environment.XXXXXX")
temporary_root=$(CDPATH= cd -- "$temporary" && pwd -P)
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
unset DF_CI_CACHE_ROOT

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
DARK_FACTORY_TASK
DARK_FACTORY_RUN'

child_environment=$temporary/child.env
(
    for name in $live_names; do
        export "$name=hostile live value; \$(must remain data)"
    done
    export DARK_FACTORY_LOCAL_CI_TEST_SENTINEL=preserved-local-ci-test-seam
    export DARK_FACTORY_LOCAL_CI_DIRECTORY=/private/fixture/.git/dark-factory-local-ci
    export DARK_FACTORY_FACTORYCTL=/private/fixture/factoryctl
    export DARK_FACTORY_LOCAL_CI_LEASE_HELD=1
    export DARK_FACTORY_LOCAL_CI_TEST_PAUSE_AFTER_LOCKF=/hostile/pause
    export CARGO_TARGET_DIR=/intentional/build-target
    export RUSTUP_TOOLCHAIN=1.88.0
    export CODEX_HOME=/intentional/provider-home
    export OPENAI_API_KEY=preserved-fake-provider-credential
    export DARK_FACTORY_UPDATE_URL=https://fixture.invalid/manifest.json
    export HOME="$temporary/host-home"
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
    'DARK_FACTORY_LOCAL_CI_DIRECTORY=/private/fixture/.git/dark-factory-local-ci' \
    'DARK_FACTORY_FACTORYCTL=/private/fixture/factoryctl' \
    'DARK_FACTORY_LOCAL_CI_TEST_PAUSE_AFTER_LOCKF=/hostile/pause'
do
    grep -F -x "$expected" "$child_environment" >/dev/null \
        || fail "intentional input was removed: ${expected%%=*}"
done

for forbidden in \
    'CARGO_TARGET_DIR=' 'RUSTUP_TOOLCHAIN=' 'CODEX_HOME=' \
    'OPENAI_API_KEY=' 'DARK_FACTORY_UPDATE_URL=' \
    'npm_config_userconfig=/intentional/npmrc' 'COREPACK_HOME=/intentional/corepack' \
    'npm_config_globalconfig=/intentional/global-npmrc' 'NETRC=/intentional/netrc' \
    'GOPROXY=https://fixture.invalid/proxy'; do
    if grep -F "$forbidden" "$child_environment" >/dev/null; then
        fail "hostile environment crossed boundary: ${forbidden%%=*}"
    fi
done

grep -F -x 'GOFLAGS=-modcacherw' "$child_environment" >/dev/null \
    || fail "Go module cache directories would prevent worktree cleanup"
grep -F -x 'HOME=/var/empty' "$child_environment" >/dev/null \
    || fail "safe isolated HOME was not installed"
grep -F -x "DF_CI_CACHE_ROOT=$temporary_root/host-home/Library/Caches/dark-factory/local-ci/trusted" "$child_environment" >/dev/null \
    || fail "external cache root was not installed"
grep -F -x "GOCACHE=$temporary_root/host-home/Library/Caches/dark-factory/local-ci/trusted/go-build" "$child_environment" >/dev/null \
    || fail "safe isolated Go build cache was not installed"
grep -F -x "GOMODCACHE=$temporary_root/host-home/Library/Caches/dark-factory/local-ci/trusted/go-mod" "$child_environment" >/dev/null \
    || fail "safe isolated Go module cache was not installed"
grep -F -x 'NPM_CONFIG_USERCONFIG=/var/empty/.npmrc' "$child_environment" >/dev/null \
    || fail "safe npm config was not installed"
grep -F -x 'NPM_CONFIG_GLOBALCONFIG=/var/empty/.npmrc-global' "$child_environment" >/dev/null \
    || fail "safe global npm config was not installed"
grep -F -x "NPM_CONFIG_CACHE=$temporary_root/host-home/Library/Caches/dark-factory/local-ci/trusted/npm" "$child_environment" >/dev/null \
    || fail "reusable npm cache was not installed"
grep -F -x "pnpm_config_store_dir=$temporary_root/host-home/Library/Caches/dark-factory/local-ci/trusted/pnpm-store" "$child_environment" >/dev/null \
    || fail "reusable pnpm store was not installed"
grep -F -x 'NETRC=/dev/null' "$child_environment" >/dev/null \
    || fail "safe netrc boundary was not installed"
grep -F -x 'XDG_CONFIG_HOME=/var/empty' "$child_environment" >/dev/null \
    || fail "host config was not isolated"
grep -F -x "XDG_DATA_HOME=$repository_root/.tools/local-ci-state/data" "$child_environment" >/dev/null \
    || fail "safe XDG data directory was not installed"
grep -F -x "XDG_STATE_HOME=$repository_root/.tools/local-ci-state/state" "$child_environment" >/dev/null \
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

cache_root_one=$(CDPATH='' cd -- "$fixture" && HOME="$temporary/fixture-home" /bin/sh ./scripts/entry.sh)
fixture_root=$(CDPATH='' cd -- "$fixture" && pwd -P)
[ "$cache_root_one" = "$temporary_root/fixture-home/Library/Caches/dark-factory/local-ci/trusted" ] || fail "direct cache root changed"

/bin/rm -rf "$fixture/.tools"
/bin/mkdir -p "$fixture/.tools"
/bin/ln -s "$fixture/.tools-target" "$fixture/.tools/local-ci-state"
set +e
state_output=$(CDPATH='' cd -- "$fixture" && HOME="$temporary/fixture-home" /bin/sh ./scripts/entry.sh 2>&1)
state_status=$?
set -e
[ "$state_status" -ne 0 ] || fail "symlink private state root was accepted"
printf '%s\n' "$state_output" | grep -F 'refusing unsafe .tools/local-ci-state path' >/dev/null \
    || fail "symlink private state refusal was unclear: $state_output"
/bin/rm -rf "$fixture/.tools"

/bin/mkdir -p "$temporary/explicit-cache"
explicit_root="$temporary_root/explicit-cache/root"
/bin/mkdir "$temporary/explicit-cache/root"
/bin/chmod 750 "$temporary/explicit-cache/root"
explicit_output=$(CDPATH='' cd -- "$fixture" && HOME="$temporary/fixture-home" DF_CI_CACHE_ROOT="$explicit_root" /bin/sh ./scripts/entry.sh)
[ "$explicit_output" = "$explicit_root" ] || fail "explicit cache root was discarded"
[ "$(/usr/bin/stat -f '%Lp' "$explicit_root")" = 750 ] || fail "existing cache-root permissions were changed"
for invalid_root in / /// relative "$fixture_root/cache" "$fixture_root/../cache"; do
    if (CDPATH='' cd -- "$fixture" && DF_CI_CACHE_ROOT="$invalid_root" /bin/sh ./scripts/entry.sh) >"$temporary/invalid.out" 2>&1; then
        fail "unsafe cache root was accepted: $invalid_root"
    fi
done
if (CDPATH='' cd -- "$fixture" && HOME= /bin/sh ./scripts/entry.sh) >"$temporary/invalid.out" 2>&1; then
    fail "missing HOME silently selected a shared cache"
fi
/bin/rm -rf "$temporary/explicit-cache/root"
/bin/ln -s "$fixture/.tools-target" "$temporary/explicit-cache/root"
set +e
symlink_output=$(CDPATH='' cd -- "$fixture" && HOME="$temporary/fixture-home" DF_CI_CACHE_ROOT="$temporary/explicit-cache/root" /bin/sh ./scripts/entry.sh 2>&1)
symlink_status=$?
set -e
[ "$symlink_status" -ne 0 ] || fail "symlink cache root was accepted"
printf '%s\n' "$symlink_output" | grep -F 'refusing unsafe cache root path' >/dev/null \
    || fail "symlink cache root refusal was unclear: $symlink_output"
/bin/rm "$temporary/explicit-cache/root"
/bin/mkdir "$temporary/explicit-cache/root"
/bin/ln -s "$fixture/.tools-target" "$temporary/explicit-cache/root/go-build"
set +e
symlink_output=$(CDPATH='' cd -- "$fixture" && HOME="$temporary/fixture-home" DF_CI_CACHE_ROOT="$temporary/explicit-cache/root" /bin/sh ./scripts/entry.sh 2>&1)
symlink_status=$?
set -e
[ "$symlink_status" -ne 0 ] || fail "symlink cache child was accepted"
printf '%s\n' "$symlink_output" | grep -F 'refusing unsafe cache/go-build path' >/dev/null \
    || fail "symlink cache child refusal was unclear: $symlink_output"
[ "$(/usr/bin/find "$temporary/explicit-cache/root" -mindepth 1 -maxdepth 1 | /usr/bin/wc -l | /usr/bin/tr -d ' ')" -eq 1 ] \
    || fail "symlink cache child caused partial cache writes"

/bin/rm -rf "$temporary/explicit-cache/root"
/bin/mkdir "$temporary/explicit-cache/root"
: >"$temporary/explicit-cache/root/go-mod"
set +e
file_output=$(CDPATH='' cd -- "$fixture" && HOME="$temporary/fixture-home" DF_CI_CACHE_ROOT="$temporary/explicit-cache/root" /bin/sh ./scripts/entry.sh 2>&1)
file_status=$?
set -e
[ "$file_status" -ne 0 ] || fail "regular-file cache child was accepted"
printf '%s\n' "$file_output" | grep -F 'refusing unsafe cache/go-mod path' >/dev/null \
    || fail "regular-file cache child refusal was unclear: $file_output"
[ "$(/usr/bin/find "$temporary/explicit-cache/root" -mindepth 1 -maxdepth 1 | /usr/bin/wc -l | /usr/bin/tr -d ' ')" -eq 1 ] \
    || fail "regular-file cache child caused partial cache writes"

sh -n "$boundary" "$repository_root/scripts/local-ci.sh"
echo "local-ci environment tests passed"
