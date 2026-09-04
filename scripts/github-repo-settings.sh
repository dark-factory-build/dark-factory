#!/bin/sh
# Apply this repository's GitHub configuration: labels, merge settings, the
# `main` branch ruleset, and security features. Idempotent — run it again
# after changing anything below. Needs `gh` authenticated as a repository
# admin. Rulesets and the security features need a public repository (or
# GitHub Pro); while the repository is private those steps report the 403
# and the script exits non-zero so the gap is visible.
set -u

repository=$(gh repo view --json nameWithOwner --jq .nameWithOwner) || exit 1
failed=0
tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
step() { printf '\n== %s\n' "$1"; }
try() {
    if "$@"; then
        return 0
    fi
    echo "   FAILED: $*" >&2
    failed=1
    return 1
}

step "labels"
while IFS='|' read -r name color description; do
    try gh label create "$name" --color "$color" --description "$description" --force >/dev/null
done <<'LABELS'
known-issue|B60205|Imported from the known-issues triage; the smallest fix is in the body
area:daemon|1D76DB|factoryd: attempts, dispatch, store, hooks, supervision
area:cli|1D76DB|factoryctl
area:console|1D76DB|loopback web console, browser protocol, client, and UI
area:providers|1D76DB|claude/codex/shell adapters and their generated config
area:docs|0075CA|README, ARCHITECTURE, docs/
area:ci|5319E7|CI, release, toolchain, scripts
state:queued|D4C5F9|Accepted work has not started
state:in-progress|1D76DB|Work is actively being executed
state:blocked|B60205|Work needs a bounded resolution before it can continue
state:review|FBCA04|A change is awaiting independent review or recheck
state:release-ready|0E8A16|Exact integration or release preconditions are satisfied
size:S|C2E0C6|A focused change; hours
size:M|FBCA04|A day or two; touches more than one package or a load-bearing path
size:L|E99695|Needs design or an upstream change first
decision|D4C5F9|Needs the maintainer to decide, not (only) code
security|D93F0B|Widens or narrows what an attempt, external caller, or PR can reach
LABELS

step "merge settings (linear history: squash or rebase only; delete merged branches)"
try gh repo edit "$repository" --enable-squash-merge --enable-rebase-merge \
    --enable-merge-commit=false --delete-branch-on-merge

# One ruleset protects main with no bypass. Every change needs a pull request,
# resolved threads, the merge queue, and the `required` aggregate from GitHub
# Actions (integration 15368). The Maintainer's exact-head ALLOW is the review
# gate for ordinary changes. Native CODEOWNER review is reserved for the few
# paths that can rewrite that gate; the App cannot approve its own pull request.
# Currency is the queue's job, not a strict up-to-date check. Nobody pushes to
# main.
ruleset_id() {
    ruleset_ids=$(gh api "repos/$repository/rulesets" --jq ".[] | select(.name==\"$1\") | .id" 2>/dev/null) || {
        echo "   FAILED: cannot list rulesets" >&2
        return 1
    }
    ruleset_count=$(printf '%s\n' "$ruleset_ids" | awk 'NF { count++ } END { print count + 0 }')
    [ "$ruleset_count" -le 1 ] || {
        echo "   FAILED: duplicate ruleset name: $1" >&2
        return 1
    }
    case "$ruleset_ids" in
        '') ;;
        *[!0-9]*) echo "   FAILED: invalid ruleset id for $1" >&2; return 1 ;;
    esac
    printf '%s' "$ruleset_ids"
}

apply_ruleset() {
    printf '%s' "$2" > "$tmp"
    existing=$(ruleset_id "$1") || { failed=1; return; }
    if [ -n "$existing" ]; then
        try gh api -X PUT "repos/$repository/rulesets/$existing" --input "$tmp" >/dev/null
    else
        try gh api -X POST "repos/$repository/rulesets" --input "$tmp" >/dev/null
    fi
}

delete_ruleset() {
    existing=$(ruleset_id "$1") || { failed=1; return; }
    if [ -n "$existing" ]; then
        try gh api -X DELETE "repos/$repository/rulesets/$existing" >/dev/null
    fi
}

# The condition names `refs/heads/main` rather than `~DEFAULT_BRANCH`, and that
# is load-bearing: GitHub refuses a merge queue rule whose ref condition is not
# an exact name ("Wildcard ref names are not supported when merge queue is
# enabled"), and it counts `~DEFAULT_BRANCH` as non-exact. On the real ruleset
# that refusal arrives as `Invalid rule 'merge_queue': ` with an empty reason,
# so the cause is only visible if you happen to try a wildcard and read the
# better error. Renaming the default branch now needs this line changed too.
#
# A merge queue also requires an ORGANIZATION-owned repository. The same rule
# with the same parameters is rejected on a user-owned repository, again with
# an empty reason. That is why this repository moved to `dark-factory-build`.
#
# `strict_required_status_checks_policy` is deliberately FALSE, and the merge
# queue is why. Strict demands every open pull request be rebased onto main and
# fully re-tested after any merge, so with more than one branch in flight each
# merge invalidates the next: update-branch, wait a full CI run, merge, repeat.
# The queue gives the same guarantee without the serialisation — it tests
# main + everything ahead in the queue + this entry, and merges only that exact
# combination — so strict would add a per-merge re-run for a property the queue
# already establishes. GitHub also expects strict off when a queue is enabled.
step "ruleset: main-protect (pull request + required aggregate + merge queue + immutable linear history; no bypass)"
if apply_ruleset main-protect '{
  "name": "main-protect",
  "target": "branch",
  "enforcement": "active",
  "bypass_actors": [],
  "conditions": { "ref_name": { "include": ["refs/heads/main"], "exclude": [] } },
  "rules": [
    { "type": "deletion" },
    { "type": "non_fast_forward" },
    { "type": "required_linear_history" },
    { "type": "merge_queue", "parameters": {
        "merge_method": "SQUASH",
        "grouping_strategy": "ALLGREEN",
        "max_entries_to_build": 5,
        "max_entries_to_merge": 5,
        "min_entries_to_merge": 1,
        "min_entries_to_merge_wait_minutes": 0,
        "check_response_timeout_minutes": 60 } },
    { "type": "required_status_checks", "parameters": {
        "strict_required_status_checks_policy": false,
        "required_status_checks": [ { "context": "required", "integration_id": 15368 } ] } },
    { "type": "pull_request", "parameters": {
        "required_approving_review_count": 0,
        "dismiss_stale_reviews_on_push": true,
        "require_code_owner_review": true,
        "require_last_push_approval": false,
        "required_review_thread_resolution": true } }
  ]
}'; then
    # Delete only after the replacement rule is live. Otherwise an API failure
    # could remove the repository's sole pull-request rule.
    step "ruleset: remove obsolete main-review"
    delete_ruleset main-review
else
    echo "   SKIPPED: obsolete main-review remains until main-protect succeeds" >&2
fi

# `main-review` existed only to carry an admin bypass around GitHub's native
# approval rule. The unified no-bypass ruleset supersedes it.

step "security: private vulnerability reporting, dependabot alerts, secret scanning + push protection"
try gh api -X PUT "repos/$repository/private-vulnerability-reporting" >/dev/null
try gh api -X PUT "repos/$repository/vulnerability-alerts" >/dev/null
printf '%s' '{"security_and_analysis":{"secret_scanning":{"status":"enabled"},"secret_scanning_push_protection":{"status":"enabled"}}}' > "$tmp"
try gh api -X PATCH "repos/$repository" --input "$tmp" >/dev/null

step "actions: workflows from every outside contributor's fork PR need approval before they run"
printf '%s' '{"approval_policy":"all_external_contributors"}' > "$tmp"
try gh api -X PUT "repos/$repository/actions/permissions/fork-pr-contributor-approval" --input "$tmp" >/dev/null

if [ "$failed" -ne 0 ]; then
    printf '\nsome steps failed (a private repository on a free plan cannot use rulesets or the security features: flip it public and re-run)\n' >&2
    exit 1
fi
printf '\nall settings applied to %s\n' "$repository"
