#!/bin/sh
set -eu

export CLOUDFLARE_ACCOUNT_ID=account
export CLOUDFLARE_API_TOKEN=test-token
export PRODUCTION_HOSTNAME=maintainer.darkfactory.build
export CLOUDFLARE_ACCESS_POLICY_TEST=1

. "$(dirname "$0")/check-cloudflare-access-policy.sh"

curl() {
    url=
    for argument do
        case ${argument} in
            https://*) url=${argument} ;;
        esac
    done
    case ${url} in
        *access/apps?per_page=100\&page=1)
            printf '%s\n' '{"success":true,"result":[{"id":"app","domain":"maintainer.darkfactory.build/mcp"}],"result_info":{"page":1,"total_pages":2}}' ;;
        *access/apps?per_page=100\&page=2)
            printf '%s\n' '{"success":true,"result":[{"id":"other","domain":"maintainer.darkfactory.build/mcp"}],"result_info":{"page":2,"total_pages":2}}' ;;
        *)
            echo "unexpected URL: ${url}" >&2
            return 1 ;;
    esac
}

if CLOUDFLARE_ACCESS_POLICY_TEST=1 main >/dev/null 2>&1; then
    echo 'later-page duplicate was accepted' >&2
    exit 1
fi

curl() {
    url=
    for argument do
        case ${argument} in
            https://*) url=${argument} ;;
        esac
    done
    case ${url} in
        *access/apps?per_page=100\&page=1)
            printf '%s\n' '{"success":true,"result":[{"id":"app","domain":"maintainer.darkfactory.build/mcp"}],"result_info":{"page":1,"total_pages":2}}' ;;
        *access/apps?per_page=100\&page=2)
            printf '%s\n' '{"success":true,"result_info":{"page":2,"total_pages":2}}' ;;
        *)
            echo "unexpected URL: ${url}" >&2
            return 1 ;;
    esac
}

if CLOUDFLARE_ACCESS_POLICY_TEST=1 main >/dev/null 2>&1; then
    echo 'missing page result was accepted' >&2
    exit 1
fi

curl() {
    url=
    for argument do
        case ${argument} in
            https://*) url=${argument} ;;
        esac
    done
    case ${url} in
        *access/apps?per_page=100\&page=1)
            printf '%s\n' '{"success":true,"result":[{"id":"app","domain":"maintainer.darkfactory.build/mcp"}],"result_info":{"page":1,"total_pages":1}}' ;;
        *access/apps/app/policies?per_page=100\&page=1)
            printf '%s\n' '{"success":true,"result":[{"decision":"allow","include":[{"email":{"email":"operator@example.com"}}],"exclude":[],"require":[]},{"decision":"non_identity","include":[{"service_token":{"token_id":"0123456789abcdef0123456789abcdef.access"}}],"exclude":[],"require":[]}],"result_info":{"page":1,"total_pages":1}}' ;;
        *)
            echo "unexpected URL: ${url}" >&2
            return 1 ;;
    esac
}

if ! CLOUDFLARE_ACCESS_POLICY_TEST=1 main >/dev/null 2>&1; then
    echo 'valid Access policy shape was rejected' >&2
    exit 1
fi

# The exact shape and one extra allow policy for everyone: a drift that must fail.
curl() {
    url=
    for argument do
        case ${argument} in
            https://*) url=${argument} ;;
        esac
    done
    case ${url} in
        *access/apps?per_page=100\&page=1)
            printf '%s\n' '{"success":true,"result":[{"id":"app","domain":"maintainer.darkfactory.build/mcp"}],"result_info":{"page":1,"total_pages":1}}' ;;
        *access/apps/app/policies?per_page=100\&page=1)
            printf '%s\n' '{"success":true,"result":[{"decision":"allow","include":[{"email":{"email":"operator@example.com"}}],"exclude":[],"require":[]},{"decision":"allow","include":[{"everyone":{}}],"exclude":[],"require":[]},{"decision":"non_identity","include":[{"service_token":{"token_id":"0123456789abcdef0123456789abcdef.access"}}],"exclude":[],"require":[]}],"result_info":{"page":1,"total_pages":1}}' ;;
        *)
            echo "unexpected URL: ${url}" >&2
            return 1 ;;
    esac
}

if CLOUDFLARE_ACCESS_POLICY_TEST=1 main >/dev/null 2>&1; then
    echo 'extra allow policy was accepted' >&2
    exit 1
fi

echo 'Cloudflare Access pagination fixture passed'
