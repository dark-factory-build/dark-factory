#!/bin/sh
# Assert that the merge queue is still the only way into the default branch.
#
# The adversarial-review gate runs on `merge_group` and nowhere else, because a
# verdict recorded after a pull-request-time run can never re-trigger it. That
# makes the `merge_queue` rule the single chokepoint: if it lapses, GitHub
# stops emitting `merge_group`, the review gate never runs on any event, and
# `required` is green -- so rule 2 silently stops being enforced with no red
# check anywhere. A gate whose enforcement can disappear without a signal is
# worse than one that is merely absent, because it still reads as present.
#
# This turns that into a loud failure. It reads the *live* rules that apply to
# the branch, not the proposal in scripts/github-repo-settings.sh: the proposal
# says what an operator intended to apply, which is not evidence that it is
# applied -- and the two drifted once already.
set -eu

rules=${DF_CHOKEPOINT_RULES:-}
if [ -z "$rules" ] || [ ! -f "$rules" ]; then
    echo "verify-merge-queue-chokepoint: DF_CHOKEPOINT_RULES must name a readable file" >&2
    exit 1
fi

# One rule type per line, as `gh api .../rules/branches/<branch> --jq '.[].type'`
# emits them. An empty file means no rules apply at all, which is the most
# severe form of the failure this exists to catch.
found_queue=no
found_checks=no
while IFS= read -r type || [ -n "$type" ]; do
    case $type in
        merge_queue) found_queue=yes ;;
        required_status_checks) found_checks=yes ;;
    esac
done <"$rules"

if [ "$found_queue" != yes ]; then
    echo "verify-merge-queue-chokepoint: no active merge_queue rule on the base branch" >&2
    echo "  The adversarial-review gate runs only on merge_group. Without the queue" >&2
    echo "  it never runs, and nothing enforces AGENTS.md rule 2." >&2
    echo "  Re-apply scripts/github-repo-settings.sh." >&2
    exit 1
fi
if [ "$found_checks" != yes ]; then
    echo "verify-merge-queue-chokepoint: no active required_status_checks rule" >&2
    echo "  Nothing requires the aggregate that the review gate reports through." >&2
    exit 1
fi

echo "verify-merge-queue-chokepoint: merge queue and required checks are both active"
