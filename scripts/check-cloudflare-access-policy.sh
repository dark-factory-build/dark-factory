#!/bin/sh
set -eu

read_all_pages() {
    path=$1
    page=1
    all='[]'
    while :; do
        response="$(curl -sS --fail-with-body --max-time 30 \
            -H "Authorization: Bearer ${CLOUDFLARE_API_TOKEN}" \
            "${CLOUDFLARE_ACCESS_API}${path}&page=${page}")"
        printf '%s' "${response}" | jq -e --argjson expected_page "${page}" '
            .success == true
            and (.result_info.page? == $expected_page)
            and ((.result_info.total_pages? // 0) >= $expected_page)' >/dev/null
        result="$(printf '%s' "${response}" | jq -ce '.result // []')"
        all="$(jq -cn --argjson all "${all}" --argjson result "${result}" '$all + $result')"
        total_pages="$(printf '%s' "${response}" | jq -er '.result_info.total_pages')"
        [ "${page}" -ge "${total_pages}" ] && break
        page=$((page + 1))
    done
    printf '%s' "${all}"
}

main() {
    api="https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}"
    CLOUDFLARE_ACCESS_API=${api}
    apps="$(read_all_pages '/access/apps?per_page=100')"
    app_id="$(printf '%s' "${apps}" | jq -er --arg domain "${PRODUCTION_HOSTNAME}/mcp" '
        [.[] | select(.domain == $domain)]
          | if length == 1 then .[0].id else error("expected exactly one /mcp Access application") end
    ')"
    policies="$(read_all_pages "/access/apps/${app_id}/policies?per_page=100")"
    printf '%s' "${policies}" | jq -e '
        def empty_lists: ((.exclude // []) | length == 0) and ((.require // []) | length == 0);
        def operator: .decision == "allow"
          and ((.include // []) | length == 1)
          and (.include[0].email.email? != null)
          and empty_lists;
        def service_auth: .decision == "non_identity"
          and ((.include // []) | length == 1)
          and (.include[0].service_token.token_id? != null)
          and empty_lists;
        ([.[] | select(operator)] | length == 1)
          and ([.[] | select(service_auth)] | length == 1)
          or error("expected one operator allow policy and one service-auth policy")
    '
}

if [ "${CLOUDFLARE_ACCESS_POLICY_TEST-}" != 1 ]; then
    CLOUDFLARE_ACCESS_API="${CLOUDFLARE_ACCESS_API-https://api.cloudflare.com/client/v4/accounts/${CLOUDFLARE_ACCOUNT_ID}}"
    main
fi
