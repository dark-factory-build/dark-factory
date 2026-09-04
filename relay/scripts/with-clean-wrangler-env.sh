#!/bin/sh
# Runs Wrangler with the operator's ambient environment stripped: no Cloudflare
# credential, keychain socket, Node loader, HOME, or temp directory crosses the
# boundary. Copied from control-plane/scripts/ so the relay deploys independently.
set -eu

[ "$#" -ge 2 ] || {
    echo "usage: scripts/with-clean-wrangler-env.sh /absolute/node /absolute/wrangler.js [args...]" >&2
    exit 2
}
node_executable=$1
wrangler_script=$2
shift 2
for executable in "$node_executable" "$wrangler_script"; do
    case "$executable" in
        /*) ;;
        *)
            echo "clean Wrangler environment requires absolute executables" >&2
            exit 2
            ;;
    esac
    [ -f "$executable" ] && [ -x "$executable" ] || {
        echo "clean Wrangler environment requires regular executable files" >&2
        exit 2
    }
done

if /usr/bin/find . -maxdepth 1 \( -name '.env' -o -name '.env.*' -o -name '.dev.vars' -o -name '.dev.vars.*' \) -print -quit | /usr/bin/grep -q .; then
    echo "clean Wrangler environment refuses local env or dev-vars files" >&2
    exit 1
fi

temporary=$(/usr/bin/mktemp -d "/tmp/dark-factory-relay-env.XXXXXX")
child_pid=
# Invoked by the traps below.
# shellcheck disable=SC2329
cleanup() {
    status=$?
    trap - EXIT HUP INT TERM
    if [ -n "$child_pid" ]; then
        /bin/kill -TERM "$child_pid" 2>/dev/null || :
        wait "$child_pid" 2>/dev/null || :
    fi
    /bin/rm -rf -- "$temporary"
    exit "$status"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
/bin/mkdir -m 700 "$temporary/home" "$temporary/tmp"

/usr/bin/env -i \
    PATH=/usr/bin:/bin \
    HOME="$temporary/home" \
    TMPDIR="$temporary/tmp" \
    CI="${CI:-}" \
    NO_COLOR=1 \
    CLOUDFLARE_LOAD_DEV_VARS_FROM_DOT_ENV=false \
    WRANGLER_SEND_METRICS=false \
    "$node_executable" "$wrangler_script" "$@" &
child_pid=$!
if wait "$child_pid"; then
    status=0
else
    status=$?
fi
child_pid=
exit "$status"
