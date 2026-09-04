#!/bin/sh
set -eu

if [ "${DARK_FACTORY_WRANGLER_PREBUILT:-}" = 1 ]; then
    [ -f build/worker/shim.mjs ] && [ ! -L build/worker/shim.mjs ] || {
        echo "Wrangler prebuilt worker output is missing or unsafe" >&2
        exit 1
    }
    exit 0
fi

[ -z "${DARK_FACTORY_WRANGLER_PREBUILT:-}" ] || {
    echo "invalid Wrangler prebuilt contract" >&2
    exit 1
}
exec worker-build --release
