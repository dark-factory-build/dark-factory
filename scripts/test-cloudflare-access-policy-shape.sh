#!/bin/sh
set -eu

# The deployment gate concatenates every API page before counting matches.
# Keep a matching duplicate on page two so a first-page-only implementation
# fails this fixture instead of silently proving the wrong contract.
pages='[
  {"result":[{"domain":"maintainer.darkfactory.build/mcp"}],"result_info":{"page":1,"total_pages":2}},
  {"result":[{"domain":"maintainer.darkfactory.build/mcp"}],"result_info":{"page":2,"total_pages":2}}
]'
all="$(printf '%s' "${pages}" | jq -c '[.[].result[]]')"
if printf '%s' "${all}" | jq -e '[.[] | select(.domain == "maintainer.darkfactory.build/mcp")] | length == 1' >/dev/null; then
    echo 'later-page duplicate was accepted' >&2
    exit 1
fi

echo 'Cloudflare Access pagination fixture passed'
