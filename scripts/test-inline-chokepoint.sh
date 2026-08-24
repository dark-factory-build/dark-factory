#!/bin/sh
# Drive the merge-queue chokepoint assertion that ships in `.github/workflows/ci.yml`.
#
# That assertion lives inline rather than in a script on purpose: `scripts/` is
# publishable by the maintainer App, so a helper there would be the
# weaker-protected file guarding the stronger-protected one. The cost of that
# choice is that the assertion is not, by itself, executable by a test.
#
# This closes the gap without moving it. The step's script is extracted from
# the workflow **verbatim** and run against fixtures with a stubbed `gh`, so
# what is exercised is the shipped artifact rather than a copy that could
# drift from it. This file being App-publishable does not matter: neutering it
# weakens only the test, never the assertion.
set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
workflow=$repository_root/.github/workflows/ci.yml
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-chokepoint.XXXXXX")
trap 'rm -rf "$temporary"' EXIT
trap 'rm -rf "$temporary"; exit 130' HUP INT TERM

# Everything indented under this step's `run: |`, with that indentation removed.
awk '
    /^      - name: Require the merge queue to still be the only path to this branch$/ { step = 1; next }
    step && /^        run: \|$/ { body = 1; next }
    body && /^          / { sub(/^          /, ""); print; next }
    body && /^ *$/ { print ""; next }
    body { exit }
' "$workflow" >"$temporary/assertion.sh"

[ -s "$temporary/assertion.sh" ] || {
    echo 'FAIL: could not extract the chokepoint step from the workflow' >&2
    exit 1
}
grep -q 'require rule:merge_queue' "$temporary/assertion.sh" || {
    echo 'FAIL: the extracted script is not the chokepoint assertion' >&2
    cat "$temporary/assertion.sh" >&2
    exit 1
}

# A `gh` that prints whatever facts the case under test wants.
mkdir -p "$temporary/bin"
cat >"$temporary/bin/gh" <<'STUB'
#!/bin/sh
[ "${DF_STUB_FAIL:-}" = 1 ] && exit 1
cat "$DF_STUB_FACTS"
STUB
chmod +x "$temporary/bin/gh"

run_case() {
    DF_STUB_FACTS=$temporary/facts \
    PATH="$temporary/bin:$PATH" \
    GITHUB_REPOSITORY=dark-factory-build/dark-factory \
    BASE_BRANCH=main \
    RUNNER_TEMP=$temporary \
        bash "$temporary/assertion.sh" >"$temporary/out" 2>&1
}

# The projection the step runs, as it reads against this repository's live
# `main` rules today. `integration:15368:required` is the binding fact: the
# required context is reportable only by GitHub Actions (integration 15368).
live_facts() {
    printf '%s\n' rule:deletion rule:non_fast_forward rule:required_linear_history \
        rule:required_status_checks context:required integration:15368:required \
        rule:merge_queue grouping:ALLGREEN rule:pull_request >"$temporary/facts"
}

# The live ruleset, as observed on this repository, must pass.
live_facts
run_case || { echo 'FAIL: the live ruleset must pass' >&2; cat "$temporary/out" >&2; exit 1; }

# Each of the five required facts must fail closed on its own.
for missing in rule:merge_queue grouping:ALLGREEN rule:required_status_checks \
    context:required integration:15368:required; do
    live_facts
    grep -vx "$missing" "$temporary/facts" >"$temporary/facts.tmp"
    mv "$temporary/facts.tmp" "$temporary/facts"
    if run_case; then
        echo "FAIL: a missing '$missing' must fail the step" >&2
        exit 1
    fi
    grep -Fq "'$missing' is not active" "$temporary/out" || {
        echo "FAIL: wrong reason reported for a missing '$missing'" >&2
        cat "$temporary/out" >&2
        exit 1
    }
done

# HEADGREEN is the configuration in which only the last entry of a group is
# gated, so unreviewed entries ahead of it would merge unchecked.
live_facts
sed 's/^grouping:ALLGREEN$/grouping:HEADGREEN/' "$temporary/facts" >"$temporary/facts.tmp"
mv "$temporary/facts.tmp" "$temporary/facts"
if run_case; then echo 'FAIL: HEADGREEN must fail the step' >&2; exit 1; fi

# A required check that is not the aggregate the review gate reports through.
live_facts
sed 's/^context:required$/context:checks/' "$temporary/facts" >"$temporary/facts.tmp"
mv "$temporary/facts.tmp" "$temporary/facts"
if run_case; then echo 'FAIL: a different required context must fail' >&2; exit 1; fi

# `-x` must reject a superstring rather than matching it.
live_facts
sed 's/^context:required$/context:not-required-really/' "$temporary/facts" >"$temporary/facts.tmp"
mv "$temporary/facts.tmp" "$temporary/facts"
if run_case; then echo 'FAIL: a superstring context must not satisfy the check' >&2; exit 1; fi

# The required context present but unbound: `required_status_checks` entries
# carry no `integration_id`, so `tostring` projects `null` and any installed
# integration could report the aggregate green. This is the ruleset the other
# four facts all pass through unchanged (#375).
live_facts
sed 's/^integration:15368:required$/integration:null:required/' "$temporary/facts" >"$temporary/facts.tmp"
mv "$temporary/facts.tmp" "$temporary/facts"
if run_case; then echo 'FAIL: an unbound required context must fail the step' >&2; exit 1; fi

# Bound, but to some other integration.
live_facts
sed 's/^integration:15368:required$/integration:99999:required/' "$temporary/facts" >"$temporary/facts.tmp"
mv "$temporary/facts.tmp" "$temporary/facts"
if run_case; then echo 'FAIL: a foreign integration binding must fail the step' >&2; exit 1; fi

# The binding on a DIFFERENT required context must not stand in for it. This
# is why the fact names the context: against a bare `integration:15368` this
# ruleset -- `required` unbound, some other context bound -- reads as green.
live_facts
sed 's/^integration:15368:required$/integration:15368:control-plane/' "$temporary/facts" >"$temporary/facts.tmp"
mv "$temporary/facts.tmp" "$temporary/facts"
if run_case; then echo "FAIL: another context's binding must not satisfy the check" >&2; exit 1; fi

# No rules at all is the most severe form, not a pass.
: >"$temporary/facts"
if run_case; then echo 'FAIL: an empty ruleset must fail' >&2; exit 1; fi

# A failing `gh` must abort rather than read as "no rules".
live_facts
# In a subshell: a `VAR=x func` prefix assignment persists in the current
# shell for a *function* (unlike for an external command), so setting it
# inline would leave the stub failing for every later case and turn this file
# into a test that passes for the wrong reason.
if (DF_STUB_FAIL=1; export DF_STUB_FAIL; run_case); then
    echo 'FAIL: a failing gh must fail the step' >&2
    exit 1
fi

# `grep -q` closes the pipe on its first match, so a piped projection under
# `pipefail` returns 141 once the data exceeds the 64KB pipe buffer -- reporting
# an ACTIVE rule as absent. Latent at today's ~190 bytes, so this is the only
# thing standing between the assertion and a silent regression to it.
live_facts
awk 'BEGIN { for (i = 0; i < 6000; i++) print "context:filler" i }' >>"$temporary/facts"
run_case || {
    echo 'FAIL: a large ruleset must not break the check (SIGPIPE regression)' >&2
    cat "$temporary/out" >&2
    exit 1
}

echo "inline chokepoint assertion passed its failure modes"
