#!/bin/sh
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

temporary=$(/usr/bin/mktemp -d "/tmp/dark-factory-wrangler-env.XXXXXX")
trap '/bin/rm -rf -- "$temporary"' EXIT HUP INT TERM
/bin/mkdir -m 700 "$temporary/home" "$temporary/tmp"

status=0
/usr/bin/env -i \
    PATH=/usr/bin:/bin \
    HOME="$temporary/home" \
    TMPDIR="$temporary/tmp" \
    CI="${CI:-}" \
    NO_COLOR=1 \
    CLOUDFLARE_LOAD_DEV_VARS_FROM_DOT_ENV=false \
    DARK_FACTORY_WRANGLER_PREBUILT=1 \
    WRANGLER_SEND_METRICS=false \
    "$node_executable" "$wrangler_script" "$@" || status=$?
exit "$status"
