#!/bin/sh
# The chokepoint check exists because its absence is silent. A test that
# cannot distinguish "present" from "absent" would reproduce that exactly.
set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
verify=$repository_root/scripts/verify-merge-queue-chokepoint.sh
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-chokepoint.XXXXXX")
trap 'rm -rf "$temporary"' EXIT
trap 'rm -rf "$temporary"; exit 130' HUP INT TERM

rules=$temporary/rules
run() { DF_CHOKEPOINT_RULES=$rules "$verify" >"$temporary/out" 2>&1; }

# The live shape, as observed on this repository.
printf '%s\n' deletion merge_queue non_fast_forward pull_request \
    required_linear_history required_status_checks >"$rules"
run || { echo 'FAIL: the live rule set must pass' >&2; cat "$temporary/out" >&2; exit 1; }

# The failure this exists to catch: the queue rule lapses. `merge_group` stops
# firing, the review gate never runs, and `required` stays green.
printf '%s\n' deletion non_fast_forward pull_request \
    required_linear_history required_status_checks >"$rules"
if run; then echo 'FAIL: a missing merge_queue rule must fail' >&2; exit 1; fi
grep -Fq 'no active merge_queue rule' "$temporary/out" || {
    echo 'FAIL: wrong reason for a missing queue' >&2; cat "$temporary/out" >&2; exit 1; }

# Nothing requiring the aggregate is the same class of hole.
printf '%s\n' deletion merge_queue non_fast_forward >"$rules"
if run; then echo 'FAIL: a missing required_status_checks rule must fail' >&2; exit 1; fi
grep -Fq 'no active required_status_checks' "$temporary/out" || {
    echo 'FAIL: wrong reason for missing checks' >&2; cat "$temporary/out" >&2; exit 1; }

# No rules at all is the most severe form, not a pass.
: >"$rules"
if run; then echo 'FAIL: an empty rule set must fail' >&2; exit 1; fi

# A final line with no terminator is still a rule.
printf 'required_status_checks\nmerge_queue' >"$rules"
run || { echo 'FAIL: an unterminated final rule must still be read' >&2; exit 1; }

# A near-miss name must not satisfy it.
printf '%s\n' merge_queue_disabled required_status_checks >"$rules"
if run; then echo 'FAIL: a near-miss rule name must not count' >&2; exit 1; fi

if DF_CHOKEPOINT_RULES=$temporary/absent "$verify" >/dev/null 2>&1; then
    echo 'FAIL: a missing rules file must not pass' >&2; exit 1
fi

echo "merge queue chokepoint check passed its failure modes"
