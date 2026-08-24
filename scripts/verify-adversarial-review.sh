#!/bin/sh
# Decide whether AGENTS.md rule 2 is satisfied at one exact pull request head.
#
# The review itself happens elsewhere: an agent that did not write the change
# reads the diff and records its verdict through the maintainer App, which
# writes a `Dark-Factory-Review:` line the caller cannot forge and pins the
# review to `commit_id`. This script is the part GitHub can see. It reads the
# projected reviews, decides, and fails the job when rule 2 is not met.
#
# Deciding here rather than in a jq expression or a workflow `run:` block is
# deliberate: this is the logic that gates every merge, so it lives somewhere
# a test can drive it. The workflow only projects GitHub's JSON into TSV and
# passes it in.
set -eu

app_login=${DF_REVIEW_APP_LOGIN:-dark-factory-maintainer[bot]}

usage() {
    echo "usage: verify-adversarial-review.sh [--pull-number]" >&2
    exit 64
}

# Both gating events name the pull request in `GITHUB_REF`, so neither the
# workflow nor this script has to branch on the event:
#   pull_request  refs/pull/<n>/merge
#   merge_group   refs/heads/gh-readonly-queue/<base>/pr-<n>-<base sha>
#
# The queue form is a GitHub contract with no other source of truth -- the
# queue's synthetic head commit belongs to no pull request, so it cannot be
# resolved through the API -- which is exactly why it is parsed in one tested
# place instead of inline in YAML.
pull_number_from_ref() {
    case ${1:-} in
        refs/pull/*/merge)
            number=${1#refs/pull/}
            number=${number%/merge}
            ;;
        refs/heads/gh-readonly-queue/*/pr-*-*)
            number=${1##*/pr-}
            number=${number%%-*}
            ;;
        *)
            echo "verify-adversarial-review: cannot name a pull request from ref: ${1:-<unset>}" >&2
            return 1
            ;;
    esac
    # A ref that parsed to something that is not a positive integer must fail
    # closed rather than send a junk number to the API and read the answer as
    # "no verdict".
    # `0*` rejects both zero and a leading-zero number: neither names a pull
    # request, and both would otherwise be sent to the API and come back as
    # "no verdict" rather than as an error.
    case $number in
        '' | 0* | *[!0-9]*)
            echo "verify-adversarial-review: ref does not carry a pull number: $1" >&2
            return 1
            ;;
    esac
    printf '%s\n' "$number"
}

if [ $# -gt 0 ]; then
    [ "$1" = --pull-number ] && [ $# -eq 1 ] || usage
    pull_number_from_ref "${GITHUB_REF:-}"
    exit $?
fi

head_sha=${DF_REVIEW_HEAD_SHA:-}
reviews=${DF_REVIEW_REVIEWS:-}

case $head_sha in
    *[!0-9a-f]* | '') head_sha='' ;;
    *) [ "${#head_sha}" -eq 40 ] || head_sha='' ;;
esac
if [ -z "$head_sha" ]; then
    echo "verify-adversarial-review: DF_REVIEW_HEAD_SHA is not a commit sha" >&2
    exit 1
fi
if [ -z "$reviews" ] || [ ! -f "$reviews" ]; then
    echo "verify-adversarial-review: DF_REVIEW_REVIEWS must name a readable file" >&2
    exit 1
fi

bounded_html() {
    # Review bodies are agent-authored text arriving from GitHub. Make control
    # characters inert, bound the length, then escape HTML metacharacters --
    # the same treatment scripts/github-step-summary.sh gives workflow context.
    printf '%s' "${1:-}" |
        LC_ALL=C tr '\000-\037\177' '?' |
        LC_ALL=C cut -c 1-4000 |
        sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'
}

summary() {
    [ -n "${GITHUB_STEP_SUMMARY:-}" ] || return 0
    printf '%s\n' "$1" >>"$GITHUB_STEP_SUMMARY"
}

# One review per line: commit id, state, author login, flattened body.
# Anything else in the file is a projection bug and must not be read as "no
# blocking verdict".
allowed=0
blocked=0
considered=0
findings=$(mktemp "${TMPDIR:-/tmp}/df-review-findings.XXXXXX")
trap 'rm -f "$findings"' EXIT HUP INT TERM

while IFS="$(printf '\t')" read -r commit_id state author body extra; do
    [ -n "$commit_id" ] || continue
    if [ -n "${extra:-}" ]; then
        echo "verify-adversarial-review: malformed review record" >&2
        exit 1
    fi
    # A verdict counts only from the App, and only against this exact commit.
    # A human's review is not an agent's adversarial review, and a verdict
    # against an earlier head reviewed code that is no longer proposed.
    [ "$author" = "$app_login" ] || continue
    [ "$commit_id" = "$head_sha" ] || continue
    considered=$((considered + 1))

    verdict=none
    case $body in
        *"Dark-Factory-Review: allow $head_sha"*) verdict=allow ;;
        *"Dark-Factory-Review: block $head_sha"*) verdict=block ;;
        *"Dark-Factory-Review: note $head_sha"*) verdict=note ;;
    esac
    # CHANGES_REQUESTED is GitHub's own blocking state. Honour it even when the
    # verdict line is absent, so a review recorded before this line existed, or
    # by any other route, still blocks.
    if [ "$state" = CHANGES_REQUESTED ]; then
        verdict=block
    fi

    case $verdict in
        allow) allowed=$((allowed + 1)) ;;
        block) blocked=$((blocked + 1)) ;;
    esac

    {
        printf '<details><summary><code>%s</code> — <code>%s</code></summary>\n\n' \
            "$(bounded_html "$verdict")" "$(bounded_html "$state")"
        printf '<pre>%s</pre>\n\n</details>\n\n' "$(bounded_html "$body")"
    } >>"$findings"
done <"$reviews"

summary '### Adversarial review (AGENTS.md rule 2)'
summary ''
summary "- Head: <code>$(bounded_html "$head_sha")</code>"
summary "- Verdicts at this head: <code>$considered</code>"
summary ''
if [ "$considered" -gt 0 ] && [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
    cat "$findings" >>"$GITHUB_STEP_SUMMARY"
fi

if [ "$blocked" -gt 0 ]; then
    summary '**BLOCKED** — a reviewer recorded a blocking defect at this head.'
    summary 'Push the fix; the new head needs a fresh verdict.'
    echo "verify-adversarial-review: $blocked blocking verdict(s) at $head_sha" >&2
    exit 1
fi
if [ "$allowed" -eq 0 ]; then
    summary '**NO VERDICT** — no reviewer has recorded an ALLOW at this head.'
    summary ''
    summary 'A reviewer that did not write the change reviews the diff and records:'
    summary '`submit_pull_request_review` with `event: ALLOW` and this exact `head_sha`.'
    echo "verify-adversarial-review: no ALLOW verdict at $head_sha" >&2
    exit 1
fi

summary "**ALLOWED** — $allowed verdict(s) at this head, none blocking."
echo "verify-adversarial-review: $allowed allowing verdict(s) at $head_sha"
