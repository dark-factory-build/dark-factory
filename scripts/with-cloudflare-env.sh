#!/bin/sh
set -eu

case "$*" in
    'dns status'|'dns publish-app') ;;
    *)
        echo "usage: scripts/with-cloudflare-env.sh dns status" >&2
        echo "       scripts/with-cloudflare-env.sh dns publish-app" >&2
        exit 2
        ;;
esac

case "$0" in
    */*) cd -P -- "${0%/*}" ;;
    *)
        echo "cloudflare-admin: invoke the helper by path" >&2
        exit 1
        ;;
esac

exec /usr/bin/env -i \
    PATH=/usr/bin:/bin \
    LANG=C \
    LC_ALL=C \
    DARK_FACTORY_CLOUDFLARE_CLEAN_LAUNCH=reviewed-v1 \
    /bin/sh ./cloudflare-env-clean.sh "$@"
