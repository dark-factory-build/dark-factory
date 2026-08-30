#!/bin/sh
set -eu

[ "$#" -eq 0 ] || { echo "usage: go-ci.sh" >&2; exit 64; }
# This bootstrap runs before the isolated stage environment exists.
script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
export PATH=/opt/homebrew/bin:/usr/bin:/bin
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_COUNT=0
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR \
    GIT_CONFIG_PARAMETERS GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0 GIT_CONFIG_KEY_1 \
    GIT_CONFIG_VALUE_1 GIT_CONFIG_KEY_2 GIT_CONFIG_VALUE_2 GIT_CONFIG_KEY_3 \
    GIT_CONFIG_VALUE_3 GIT_CONFIG_KEY_4 GIT_CONFIG_VALUE_4 GIT_CONFIG_KEY_5 \
    GIT_CONFIG_VALUE_5 GIT_CONFIG_KEY_6 GIT_CONFIG_VALUE_6 GIT_CONFIG_KEY_7 \
    GIT_CONFIG_VALUE_7 GIT_CONFIG_KEY_8 GIT_CONFIG_VALUE_8 GIT_CONFIG_KEY_9 \
    GIT_CONFIG_VALUE_9
exec "$script_dir/with-local-ci-lease.sh" /bin/sh "$script_dir/go-ci-owned.sh" "$@"
