#!/bin/sh
# The review gate decides every merge, so its failure modes are the point.
# A gate that cannot say NO is worse than no gate: it reports green forever
# and nobody notices until something bad merges.
set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
verify=$repository_root/scripts/verify-adversarial-review.sh
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-review-gate.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM

head=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
other=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
# The App's numeric bot user id, which is what the projection emits and the
# gate binds to. A login can be renamed; this cannot.
app=319516570
tab=$(printf '\t')

# commit_id, state, author, flattened body -- exactly what the workflow's
# `gh api --jq ... | @tsv` projection emits.
record() {
    printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4"
}

reviews=$temporary/reviews
summary=$temporary/summary

expect_pass() {
    label=$1
    : >"$summary"
    if ! GITHUB_STEP_SUMMARY=$summary DF_REVIEW_HEAD_SHA=$head DF_REVIEW_REVIEWS=$reviews \
        "$verify" >"$temporary/out" 2>&1; then
        echo "FAIL: expected pass: $label" >&2
        cat "$temporary/out" >&2
        exit 1
    fi
}

expect_fail() {
    label=$1
    : >"$summary"
    if GITHUB_STEP_SUMMARY=$summary DF_REVIEW_HEAD_SHA=$head DF_REVIEW_REVIEWS=$reviews \
        "$verify" >"$temporary/out" 2>&1; then
        echo "FAIL: expected failure: $label" >&2
        cat "$temporary/out" >&2
        exit 1
    fi
}

assert_stderr() {
    grep -F -- "$1" "$temporary/out" >/dev/null || {
        echo "FAIL: expected message: $1" >&2
        cat "$temporary/out" >&2
        exit 1
    }
}

assert_summary() {
    grep -F -- "$1" "$summary" >/dev/null || {
        echo "FAIL: summary missing: $1" >&2
        cat "$summary" >&2
        exit 1
    }
}

# An ALLOW at the exact head is the only thing that passes.
record "$head" COMMENTED "$app" \
    "Tried the exact-head path and could not break it. Dark-Factory-Review: allow $head" >"$reviews"
expect_pass 'allow at head'
assert_summary '**ALLOWED**'

# No reviews at all. The default answer is NO.
: >"$reviews"
expect_fail 'no reviews'
assert_summary '**NO VERDICT**'

# A verdict against a different head reviewed code that is no longer proposed.
record "$other" COMMENTED "$app" "Dark-Factory-Review: allow $other" >"$reviews"
expect_fail 'allow at a stale head'

# A body that names this head but is recorded against another commit must not
# pass: `commit_id` and the rendered line have to agree.
record "$other" COMMENTED "$app" "Dark-Factory-Review: allow $head" >"$reviews"
expect_fail 'allow whose commit_id is a different head'

# ...and the reverse.
record "$head" COMMENTED "$app" "Dark-Factory-Review: allow $other" >"$reviews"
expect_fail 'allow whose rendered head is a different commit'

# A COMMENT decides nothing. Findings that were later fixed are not an ALLOW.
record "$head" COMMENTED "$app" "Three findings. Dark-Factory-Review: note $head" >"$reviews"
expect_fail 'note is not an allow'

# A blocking verdict blocks even when an ALLOW also stands at the same head,
# so a second opinion can never launder the first one away. Clearing it means
# pushing a fix, which moves the head and orphans both.
{
    record "$head" COMMENTED "$app" "Dark-Factory-Review: allow $head"
    record "$head" COMMENTED "$app" "Reaps nothing on failure. Dark-Factory-Review: block $head"
} >"$reviews"
expect_fail 'block outranks a co-existing allow'
assert_summary '**BLOCKED**'

# ...and clears only by pushing. A block belongs to the head it was recorded
# against, so the fix that moves the head orphans it exactly as it orphans an
# ALLOW. Without this, "cleared by pushing the fix" is a claim rather than a
# tested property -- and the failure it would hide is the worse direction of
# the two: a pull request no push could ever make mergeable again.
{
    record "$other" COMMENTED "$app" "Reaps nothing on failure. Dark-Factory-Review: block $other"
    record "$head" COMMENTED "$app" "Reap fixed and re-read. Dark-Factory-Review: allow $head"
} >"$reviews"
expect_pass 'a block at the previous head does not block the new one'
assert_summary '**ALLOWED**'

# GitHub's own blocking state blocks even with no verdict line.
record "$head" CHANGES_REQUESTED "$app" "no verdict line here" >"$reviews"
expect_fail 'CHANGES_REQUESTED blocks without a line'
# It must BLOCK, not merely fail for want of an ALLOW: those are different
# states and only one of them says a reviewer looked and said no.
assert_summary '**BLOCKED**'

# The same review alongside an ALLOW still blocks.
{
    record "$head" COMMENTED "$app" "Dark-Factory-Review: allow $head"
    record "$head" CHANGES_REQUESTED "$app" "no verdict line here"
} >"$reviews"
expect_fail 'CHANGES_REQUESTED outranks an allow'
assert_summary '**BLOCKED**'

# A review with no body at all is a real shape: GitHub returns `body: null`
# and the projection emits a trailing tab, so the record has an empty fourth
# field. It must neither crash the loop nor mask a valid verdict beside it.
{
    record "$head" COMMENTED "$app" ""
    record "$head" COMMENTED "$app" "Dark-Factory-Review: allow $head"
} >"$reviews"
expect_pass 'an empty-bodied review does not mask a valid verdict'
record "$head" COMMENTED "$app" "" >"$reviews"
expect_fail 'an empty-bodied review is not itself a verdict'

# Only the App's verdict counts. Anyone can type the line into a review by
# hand; the author check is what stops that being a merge gate bypass.
record "$head" COMMENTED 109233175 "Dark-Factory-Review: allow $head" >"$reviews"
expect_fail 'a real human account id does not count'
record "$head" COMMENTED 3195165 "Dark-Factory-Review: allow $head" >"$reviews"
expect_fail 'a truncated author id does not count'
record "$head" COMMENTED 3195165700 "Dark-Factory-Review: allow $head" >"$reviews"
expect_fail 'a lengthened author id does not count'
record "$head" COMMENTED " $app" "Dark-Factory-Review: allow $head" >"$reviews"
expect_fail 'a padded author id does not count'
record "$head" COMMENTED 'dark-factory-maintainer[bot]' "Dark-Factory-Review: allow $head" >"$reviews"
expect_fail 'the login is not the identity the gate binds to'

# A final record with no trailing newline must still be read. `while read`
# returns non-zero on an unterminated last line, so without the `|| [ -n ... ]`
# guard the loop body never runs for it -- silently dropping a blocking
# verdict, the one direction this gate must never fail in.
{
    printf '%s\t%s\t%s\t%s\n' "$head" COMMENTED "$app" "Dark-Factory-Review: allow $head"
    printf '%s\t%s\t%s\t%s' "$head" COMMENTED "$app" "Dark-Factory-Review: block $head"
} >"$reviews"
expect_fail 'an unterminated final record is still read'
assert_summary '**BLOCKED**'

# A record with too FEW fields is as malformed as one with too many, and the
# shortfall shifts the body into a field the gate does not read.
printf '%s\t%s\t%s\n' "$head" COMMENTED "$app" >"$reviews"
expect_fail 'a three-field record'
assert_stderr 'malformed review record'
printf '%s\t%s\n' "$head" COMMENTED >"$reviews"
expect_fail 'a two-field record'
assert_stderr 'malformed review record'

# A blank line is skipped, not read as a malformed record. Unreachable through
# `@tsv`, but the promise is "anything else in the file is a projection bug",
# and until this fixture existed the behaviour was unpinned in both directions.
{
    printf '\n'
    record "$head" COMMENTED "$app" "Dark-Factory-Review: allow $head"
    printf '\n'
} >"$reviews"
expect_pass 'blank lines are skipped'

# A malformed projection must fail closed rather than be read as a verdict.
# An embedded tab means the body was not flattened, so the fields after it are
# not the fields this gate thinks they are. The record below carries an
# otherwise-valid ALLOW, so a gate that ignored the malformation would pass it.
printf '%s\t%s\t%s\t%s\t%s\n' \
    "$head" COMMENTED "$app" "Dark-Factory-Review: allow $head" "leftover" >"$reviews"
expect_fail 'malformed record'
assert_stderr 'malformed review record'

# A head that is not a commit sha is a caller bug, and the reviews below would
# satisfy a gate that took it at face value.
record not-a-sha COMMENTED "$app" "Dark-Factory-Review: allow not-a-sha" >"$reviews"
if DF_REVIEW_HEAD_SHA=not-a-sha DF_REVIEW_REVIEWS=$reviews "$verify" >"$temporary/out" 2>&1; then
    echo 'FAIL: a bad head sha must not pass' >&2
    exit 1
fi
grep -Fq 'is not a commit sha' "$temporary/out" || {
    echo 'FAIL: a bad head sha failed for the wrong reason' >&2
    cat "$temporary/out" >&2
    exit 1
}
if DF_REVIEW_HEAD_SHA=$head DF_REVIEW_REVIEWS=$temporary/absent "$verify" \
    >"$temporary/out" 2>&1; then
    echo 'FAIL: a missing reviews file must not pass' >&2
    exit 1
fi
# It must say so. Falling through to a redirection error still fails the job,
# but tells whoever reads the log nothing about why the gate could not decide.
assert_stderr 'must name a readable file'

# Control bytes in a review body must not reach the run summary intact: a
# body is agent-authored text arriving back from GitHub.
record "$head" COMMENTED "$app" \
    "$(printf 'carriage\033[2Ktrick')  Dark-Factory-Review: block $head" >"$reviews"
expect_fail 'control bytes are neutralised'
assert_summary 'carriage?[2Ktrick'

# A review body is bounded before it reaches the summary. GITHUB_STEP_SUMMARY
# is capped, and a body can be 16000 characters; several of those would push
# the verdict itself out of the file that has to explain the red check.
long=$(awk 'BEGIN { while (i++ < 4200) printf "x" }')
record "$head" COMMENTED "$app" "${long}TAIL Dark-Factory-Review: block $head" >"$reviews"
expect_fail 'a long body is bounded'
assert_summary '**BLOCKED**'
if grep -Fq 'TAIL' "$summary"; then
    echo 'FAIL: review body reached the summary unbounded' >&2
    exit 1
fi

# The reviewer's findings reach the run summary. A red check that only says
# "review failed" cannot be acted on.
record "$head" COMMENTED "$app" \
    "Finding: <script>alert(1)</script> & the launch path. Dark-Factory-Review: block $head" >"$reviews"
expect_fail 'findings are rendered'
assert_summary 'the launch path'
assert_summary '&lt;script&gt;'
# `&` must be escaped first, or `&lt;` would itself be re-escaped wrongly and a
# literal `&` in a finding could open an entity in the rendered summary.
assert_summary '&amp; the launch path'
if grep -F '<script>alert' "$summary" >/dev/null; then
    echo 'FAIL: review body reached the summary unescaped' >&2
    exit 1
fi

# Both gating events name the pull request in GITHUB_REF.
assert_pull_number() {
    actual=$(GITHUB_REF=$1 "$verify" --pull-number)
    [ "$actual" = "$2" ] || {
        echo "FAIL: $1 -> $actual, expected $2" >&2
        exit 1
    }
}
assert_pull_number "refs/heads/gh-readonly-queue/main/pr-325-f64d7d6457938b771ac55390d010d185dbddef1f" 325
assert_pull_number "refs/heads/gh-readonly-queue/release/v1/pr-7-$head" 7
# A base branch whose own name contains `/pr-`. This is the only fixture that
# distinguishes rightmost (`##`) from leftmost (`#`) stripping, and getting it
# wrong reads a DIFFERENT pull request's verdict while merging this one -- at
# the sole enforcement point.
assert_pull_number "refs/heads/gh-readonly-queue/release/pr-2-hotfix/pr-341-$head" 341

# A pull-request ref must NOT resolve: this gate runs only on `merge_group`,
# so seeing one would mean the trigger was widened without the enforcement
# being rethought.
for bad in refs/heads/main '' refs/pull/330/merge refs/pull/abc/merge \
    refs/heads/gh-readonly-queue/main/pr--abc \
    refs/heads/gh-readonly-queue/main/pr-0-abc; do
    if GITHUB_REF=$bad "$verify" --pull-number >/dev/null 2>&1; then
        echo "FAIL: ref must not resolve: $bad" >&2
        exit 1
    fi
done

# The App must render the verdict line this gate reads. If the two ever
# disagree the gate silently stops recognising real verdicts, which reads as
# "nobody has reviewed anything" on every pull request at once.
app=$repository_root/control-plane/src/github_app.rs
grep -Fq 'const REVIEW_VERDICT_PREFIX: &str = "Dark-Factory-Review:";' "$app"
for verdict in allow note block; do
    grep -Fq "=> \"$verdict\"," "$app" || {
        echo "FAIL: the App renders no '$verdict' verdict" >&2
        exit 1
    }
    grep -Fq "Dark-Factory-Review: $verdict " "$verify" || {
        echo "FAIL: the gate does not read the '$verdict' verdict" >&2
        exit 1
    }
done
# And it must be able to record all three. Every verdict is submitted as one
# GitHub review event, because the App authors the pull requests it reviews and
# GitHub refuses a self-review that takes a side. Mapping any verdict onto a
# GitHub review state instead makes it unpostable on every reviewable pull
# request -- which is how block precedence above came to guard an empty set.
grep -Fq 'const REVIEW_EVENT: &str = "COMMENT";' "$app" || {
    echo 'FAIL: the App does not record every verdict as a COMMENT review' >&2
    exit 1
}

# The workflow has to run this gate, and run the default branch's copy of it.
# Running the pull request's own copy would let a change weaken the reviewer
# about to judge it -- `scripts/` is publishable by the maintainer App.
workflow=$repository_root/.github/workflows/ci.yml
grep -Fq 'scripts/verify-adversarial-review.sh' "$workflow"
grep -Fq 'refs/heads/${DEFAULT_BRANCH}' "$workflow"
# Enforcement lives in the merge queue. On a pull request a verdict recorded
# after the run cannot re-trigger it, so a gate there could never go green.
grep -Fq "if: github.event_name == 'merge_group'" "$workflow"
# The projection must emit the App's numeric id, which is what this gate binds
# to. Emitting the login instead would make every verdict invisible at once.
grep -Fq '(.user.id | tostring)' "$workflow"
grep -Fq "APP_USER_ID=319516570" "$verify"
echo "adversarial review gate passed its failure modes"
