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

policies_are() {
    policies=$1
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
                printf '{"success":true,"result":%s,"result_info":{"page":1,"total_pages":1}}\n' "${policies}" ;;
            *)
                echo "unexpected URL: ${url}" >&2
                return 1 ;;
        esac
    }
}
operator='{"decision":"allow","include":[{"email":{"email":"operator@example.com"}}]}'
service='{"decision":"non_identity","include":[{"service_token":{"token_id":"0123456789abcdef0123456789abcdef.access"}}]}'

# Accepted: more named people, a second named token, and narrowing rules.
policies_are "[${operator},{\"decision\":\"allow\",\"include\":[{\"email\":{\"email\":\"a@example.com\"}},{\"email\":{\"email\":\"b@example.com\"}}],\"require\":[{\"auth_method\":{\"auth_method\":\"mfa\"}}]},${service},${service}]"
if ! CLOUDFLARE_ACCESS_POLICY_TEST=1 main >/dev/null 2>&1; then
    echo 'named principals with narrowing rules were rejected' >&2
    exit 1
fi

for widening in \
    "[${operator},{\"decision\":\"allow\",\"include\":[{\"email_domain\":{\"domain\":\"example.com\"}}]},${service}]" \
    "[${operator},{\"decision\":\"bypass\",\"include\":[{\"email\":{\"email\":\"x@example.com\"}}]},${service}]" \
    "[${operator},{\"decision\":\"non_identity\",\"include\":[{\"any_valid_service_token\":{}}]},${service}]" \
    "[{\"decision\":\"allow\",\"include\":[{\"email\":{\"email\":\"o@example.com\"}},{\"ip\":{\"ip\":\"0.0.0.0/0\"}}]},${service}]" \
    "[${operator}]" \
    "[${service}]"; do
    policies_are "${widening}"
    if CLOUDFLARE_ACCESS_POLICY_TEST=1 main >/dev/null 2>&1; then
        echo "widening or incomplete policy set was accepted: ${widening}" >&2
        exit 1
    fi
done

# A refusal names only rule kinds, never the principals.
policies_are "[${operator},{\"decision\":\"bypass\",\"include\":[{\"everyone\":{}}]},${service}]"
message=$(CLOUDFLARE_ACCESS_POLICY_TEST=1 main 2>&1 >/dev/null || true)
case ${message} in
    *'"decision":"bypass"'*'"everyone"'*) ;;
    *) echo "refusal did not describe the live shape: ${message}" >&2; exit 1 ;;
esac
case ${message} in
    *operator@example.com*|*0123456789abcdef*) echo "refusal leaked a principal: ${message}" >&2; exit 1 ;;
esac

echo 'Cloudflare Access pagination fixture passed'
