#!/bin/sh
# Verify the Maintainer review contract at one exact pull request head.
#
# The review itself happens elsewhere: an agent that did not write the change
# reads the diff and records its verdict through the maintainer App, which
# writes a `Dark-Factory-Review:` line the caller cannot forge and pins the
# review to `commit_id`. This script is the part GitHub can see.
#
# Deciding here rather than in a jq expression or a workflow `run:` block is
# deliberate: this is the logic that gates every merge, so it lives somewhere
# a test can drive it. The workflow only projects GitHub's JSON into TSV.
#
# It runs in the merge queue and nowhere else. Recording a verdict fires no
# workflow event, so a pull-request-time run could never turn green after the
# reviewer acted -- it would have failed before the verdict existed, and the
# only way to re-run it is to push, which moves the head and orphans the
# verdict. The queue re-reads the verdict at merge time instead, which is the
# one moment it has to be true.
set -eu

# The App's numeric bot user id, not its login. `docs/development/GITHUB_APP.md`
# calls `dark-factory-maintainer[bot]` a "target bot identity ... subject to
# GitHub name availability": a rename would make every verdict invisible and
# this gate would report "nobody has reviewed anything" on every pull request
# at once, which reads like a review failure rather than a misconfiguration.
# The id is stable, and it is the same discipline as `integration_id` in the
# ruleset. Deliberately not overridable: the workflow that supplies this
# script's inputs comes from the pull request under review, so an override
# would let a change choose who counts as its own reviewer.
APP_USER_ID=319516570

usage() {
    echo "usage: verify-adversarial-review.sh [--pull-number]" >&2
    exit 64
}

tab=$(printf '\t')

# The merge queue names the entry's pull request in `GITHUB_REF`:
#
#   refs/heads/gh-readonly-queue/<base>/pr-<n>-<base sha>
#
# That is the only source of truth for it. The queue's head commit is a
# synthetic merge commit belonging to no pull request, so `commits/{sha}/pulls`
# cannot resolve it -- which is why the format is parsed in one tested place
# rather than inline in YAML.
#
# Only the queue form is accepted. This gate runs on `merge_group` and nothing
# else, so a `refs/pull/<n>/merge` ref reaching it would mean the trigger had
# been widened without the enforcement being rethought, and failing closed is
# the right answer to that.
pull_number_from_ref() {
    case ${1:-} in
        refs/heads/gh-readonly-queue/*/pr-*-*)
            number=${1##*/pr-}
            number=${number%%-*}
            ;;
        *)
            echo "verify-adversarial-review: cannot name a pull request from ref: ${1:-<unset>}" >&2
            return 1
            ;;
    esac
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
    # Review bodies are agent-authored text arriving back from GitHub. Make
    # control characters inert, bound the length, then escape HTML
    # metacharacters -- the same treatment scripts/github-step-summary.sh
    # gives workflow context. Not shared with it: that script bounds to 96
    # characters for a one-line field, and a findings body needs 4000.
    printf '%s' "${1:-}" |
        LC_ALL=C tr '\000-\037\177' '?' |
        LC_ALL=C cut -c 1-4000 |
        sed -e 's/&/\&amp;/g' -e 's/</\&lt;/g' -e 's/>/\&gt;/g'
}

summary() {
    [ -n "${GITHUB_STEP_SUMMARY:-}" ] || return 0
    printf '%s\n' "$1" >>"$GITHUB_STEP_SUMMARY"
}

# Split one record into exactly four tab-separated fields, preserving empty
# ones. Deliberately NOT `IFS=<tab> read -r a b c d`: tab is an IFS whitespace
# character, so that form collapses runs of tabs and silently shifts an empty
# field out of existence -- a five-field record with an empty body would be
# re-read as a well-formed four-field one, and its fifth field would be
# trusted as the review body.
split_record() {
    rest=$1
    # One arity check: exactly three separators, so a short record cannot
    # shift the body into a field this gate does not read, and a long one
    # cannot hide a fifth. Written as a single `case` rather than a guard
    # before each field because per-field guards mask each other -- removing
    # any one of them changes nothing, which means none of them is tested.
    case $rest in
        *"$tab"*"$tab"*"$tab"*"$tab"*) return 1 ;;
        *"$tab"*"$tab"*"$tab"*) ;;
        *) return 1 ;;
    esac
    field_commit=${rest%%"$tab"*}
    rest=${rest#*"$tab"}
    field_state=${rest%%"$tab"*}
    rest=${rest#*"$tab"}
    field_author=${rest%%"$tab"*}
    field_body=${rest#*"$tab"}
    return 0
}

allowed=0
blocked=0
considered=0
findings=$(mktemp "${TMPDIR:-/tmp}/df-review-findings.XXXXXX")
trap 'rm -f "$findings"' EXIT
trap 'rm -f "$findings"; exit 130' HUP INT TERM

# `|| [ -n "$line" ]` keeps the final record when the file has no trailing
# newline. Without it `read` returns non-zero and the loop body never runs for
# that record -- which would silently drop a blocking verdict, the one
# direction this gate must never fail in.
while IFS= read -r line || [ -n "$line" ]; do
    [ -n "$line" ] || continue
    if ! split_record "$line"; then
        echo "verify-adversarial-review: malformed review record" >&2
        exit 1
    fi
    # A verdict counts only from the App, and only against this exact commit.
    # A human's review is not an agent's adversarial review, and a verdict
    # against an earlier head reviewed code that is no longer proposed.
    [ "$field_author" = "$APP_USER_ID" ] || continue
    [ "$field_commit" = "$head_sha" ] || continue
    considered=$((considered + 1))

    verdict=none
    case $field_body in
        *"Dark-Factory-Review: allow $head_sha"*) verdict=allow ;;
        *"Dark-Factory-Review: block $head_sha"*) verdict=block ;;
        *"Dark-Factory-Review: note $head_sha"*) verdict=note ;;
    esac
    # CHANGES_REQUESTED is GitHub's own blocking state. Honour it even when the
    # verdict line is absent, so a review recorded before this line existed, or
    # by any other route, still blocks.
    if [ "$field_state" = CHANGES_REQUESTED ]; then
        verdict=block
    fi

    case $verdict in
        allow) allowed=$((allowed + 1)) ;;
        block) blocked=$((blocked + 1)) ;;
    esac

    {
        printf '<details><summary><code>%s</code> — <code>%s</code></summary>\n\n' \
            "$(bounded_html "$verdict")" "$(bounded_html "$field_state")"
        printf '<pre>%s</pre>\n\n</details>\n\n' "$(bounded_html "$field_body")"
    } >>"$findings"
done <"$reviews"

summary '### Adversarial review'
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
