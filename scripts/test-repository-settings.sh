#!/bin/sh
# Static regression for the no-live-mutation repository-settings proposal.
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
manifest="$repository_root/.github/repository-settings.yml"
workflow="$repository_root/.github/workflows/ci.yml"
publisher="$repository_root/scripts/github-repo-settings.sh"

grep -Fq 'repository: dark-factory-build/dark-factory' "$manifest"
grep -Fq 'context: required' "$manifest"
grep -Fq 'integration_id: 15368' "$manifest"
grep -Fq '  required:' "$workflow"
grep -Fq '    if: always()' "$workflow"
grep -Fq '    needs: [checks, linux, control-plane, review]' "$workflow"
# The condition is extracted from the step, not grepped from the file. A
# free-floating `grep` is satisfied by the string appearing in a comment, so it
# passes while the step's real `if:` has been changed to `false` -- a gate that
# is present, correctly named, body intact, and never fires.
# Bounded to the `required` job, not just to the step name. Unbounded, a decoy
# step carrying this exact name in a job that never runs satisfies both
# extractions while the real gate is neutered -- or deleted outright, which a
# uniqueness check would not catch either, since then the decoy is the only
# match.
verdict_if=$(awk '
    /^  required:$/ { job = 1; next }
    # The scan ends at the next two-space-indented line that is not a
    # comment. Every job key is such a line -- a job id may start with a
    # letter of either case or `_`, so the old `[a-z]` bound let a `_shim`
    # or `Zshim` decoy job sit past it -- and so, in principle, are exotic
    # legal continuations, which would stop the scan early: a spurious red,
    # never a silent pass. `#` is excluded because a comment line is never a
    # job key, and this file writes two-space comment blocks directly above
    # job keys (one sits right above `required:` itself), so one drifting
    # into the scanned window would otherwise red this test for no reason.
    # None is inside the window at this commit.
    job && /^  [^ #]/ { exit }
    job && /^      - name: Require a recorded adversarial review verdict$/ { step = 1; next }
    step && /^      - name: / { exit }
    step && /^        if: / { sub(/^        if: /, ""); print; exit }
' "$workflow")
# In the queue only `success` passes; elsewhere `skipped` passes too, because
# `review` runs on merge_group alone. Without the second arm the aggregate
# fails every pull request.
expected_if="needs.review.result != 'success' && (github.event_name == 'merge_group' || needs.review.result != 'skipped')"
if [ "$verdict_if" != "$expected_if" ]; then
    echo "the adversarial-review step does not carry the reviewed condition" >&2
    exit 1
fi

# Pinning the strings is not enough, and claiming otherwise was wrong: the
# condition can be present and correct while the step it guards has been
# changed to `exit 0`, or deleted with the `if:` left behind in a comment.
# Either leaves a gate that runs, prints its diagnostic, and passes. So the
# step body is extracted and checked for the exit that makes it a gate.
verdict_step=$(awk '
    /^  required:$/ { job = 1; next }
    # Same job-key bound as the first extraction above.
    job && /^  [^ #]/ { exit }
    job && /^      - name: Require a recorded adversarial review verdict$/ { step = 1; next }
    # Bounded to this step. Without this, a step whose `run:` is not a block
    # scalar leaves the scan running until the NEXT step`s `run: |` and
    # asserts against that body instead -- which passes, because the step
    # beside it also ends in `exit 1`. Found by mutating this file.
    step && !body && /^      - name: / { exit }
    step && /^        run: \|$/ { body = 1; next }
    body && /^          / { sub(/^          /, ""); print; next }
    body && /^ *$/ { print ""; next }
    body { exit }
' "$workflow")
# `grep -qx`, not a substring test. A substring is satisfied by `# exit 1`, by
# an `exit 1` nested under `if false; then`, and by `echo "exit 1"` -- each a
# step that runs, prints its diagnostic, and passes. Since awk has already
# stripped the body indent, requiring a whole line equal to `exit 1` also
# forces it to be unconditional at the top level.
printf '%s\n' "$verdict_step" | grep -qx 'exit 1' || {
    echo "the adversarial-review step does not fail the job" >&2
    exit 1
}
grep -Fq "if: needs.checks.result != 'success' || needs.linux.result != 'success' || needs.control-plane.result != 'success'" "$workflow"
grep -Fq '"context": "required"' "$publisher"
# The review gate runs only on `merge_group`, so these two are load-bearing for
# rule 2's enforcement and not merely for CI cost: without the queue the gate
# never runs, and under `HEADGREEN` only the last entry of a group is required,
# so unreviewed entries ahead of it would merge unchecked.
grep -Fq '"type": "merge_queue"' "$publisher"
grep -Fq '"grouping_strategy": "ALLGREEN"' "$publisher"
# The chokepoint assertion lives inline in the workflow on purpose: a helper
# under scripts/ is App-publishable, so it would be the weaker-protected file
# guarding the stronger-protected one. Assert all four facts are required.
for fact in rule:merge_queue grouping:ALLGREEN rule:required_status_checks context:required; do
    grep -Fq "require $fact" "$workflow" || {
        echo "the workflow does not require '$fact' to be live" >&2
        exit 1
    }
done
grep -Fq 'rules/branches/${BASE_BRANCH}' "$workflow"
if grep -Fq '"context": "checks"' "$publisher"; then
    echo "repository settings still require the macOS-only checks context" >&2
    exit 1
fi

echo "repository settings proposal passed static checks"
