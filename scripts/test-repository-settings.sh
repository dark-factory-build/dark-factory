#!/bin/sh
# Contract for the repository rules and the event-selective CI gate.
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
workflow="$repository_root/.github/workflows/ci.yml"
publisher="$repository_root/scripts/github-repo-settings.sh"
issue_importer="$repository_root/scripts/import-issues.sh"

require() {
    grep -Fq "$1" "$2" || {
        echo "missing repository contract: $1" >&2
        exit 1
    }
}

job_block() {
    awk -v key="$1" '
        $0 == "  " key ":" { found = 1 }
        found && $0 ~ /^  [^ #]/ && $0 != "  " key ":" { exit }
        found { print }
    ' "$workflow"
}

job_field() {
    job_block "$1" | awk -v field="$2" '
        $0 ~ "^    " field ": " { sub("^    " field ": ", ""); print }
    '
}

step_field() {
    job_block "$1" | awk -v label="$2" -v field="$3" '
        $0 == "      - name: " label { step = 1; next }
        step && /^      - name: / { exit }
        step && $0 ~ "^        " field ": " {
            sub("^        " field ": ", "")
            print
        }
    '
}

step_keys() {
    job_block "$1" | awk -v label="$2" '
        $0 == "      - name: " label { step = 1; print "name"; next }
        step && /^      - name: / { exit }
        step && /^        [^ ]/ {
            key = $0
            sub(/^        /, "", key)
            sub(/:.*/, "", key)
            print key
        }
    '
}

assert_job_field() {
    actual=$(job_field "$1" "$2")
    [ "$actual" = "$3" ] || {
        echo "wrong $2 for workflow job $1: ${actual:-(missing)}" >&2
        exit 1
    }
}

require_job() {
    job_block "$1" | grep -Fq "$2" || {
        echo "workflow job $1 is missing: $2" >&2
        exit 1
    }
}

# A pull request does only the cheap admission check. The exact combined tree
# receives its selected gates and exact-head review once, in the merge queue.
assert_job_field eligibility if "github.event_name == 'pull_request'"
require_job eligibility 'git diff --check "$BASE_SHA" "$GITHUB_SHA"'
full_events="github.event_name == 'merge_group' || github.event_name == 'workflow_dispatch'"
assert_job_field scope if "$full_events"
assert_job_field checks needs "scope"
assert_job_field checks if "needs.scope.result == 'success' && needs.scope.outputs.macos == 'true'"
assert_job_field control-plane needs "scope"
assert_job_field control-plane if "needs.scope.result == 'success' && needs.scope.outputs.control_plane == 'true'"
assert_job_field review if "github.event_name == 'merge_group'"
assert_job_field required if "always()"
assert_job_field required needs "[eligibility, scope, checks, control-plane, review]"
require_job scope 'BASE_SHA: ${{ github.event.merge_group.base_sha }}'
require_job scope 'git diff --check "$BASE_SHA" "$GITHUB_SHA"'
require_job scope 'git diff --name-only -z --no-renames --diff-filter=ACDMRTUXB "$BASE_SHA" "$GITHUB_SHA"'
expected_scope_keys=$(printf '%s\n' name id env run)
[ "$(step_keys scope 'Select fixed gates from the combined tree')" = "$expected_scope_keys" ] || {
    echo "combined-tree scope has unexpected step controls" >&2
    exit 1
}
require_job checks './scripts/local-ci.sh'
diagnostic_events="github.event_name == 'pull_request' || github.event_name == 'merge_group'"
[ "$(step_field required 'Confirm the live merge rules' if)" = "$diagnostic_events" ] || {
    echo "live merge-rule diagnostic is not bound to PR and queue events" >&2
    exit 1
}
expected_diagnostic_keys=$(printf '%s\n' name if env run)
[ "$(step_keys required 'Confirm the live merge rules')" = "$expected_diagnostic_keys" ] || {
    echo "live merge-rule diagnostic has unexpected step controls" >&2
    exit 1
}
source_condition="if: (github.event_name == 'pull_request' && needs.eligibility.result != 'success') || ((github.event_name == 'merge_group' || github.event_name == 'workflow_dispatch') && (needs.scope.result != 'success' || (needs.scope.outputs.macos == 'true' && needs.checks.result != 'success') || (needs.scope.outputs.control_plane == 'true' && needs.control-plane.result != 'success')))"
require_job required "$source_condition"
require_job required "needs.review.result != 'success' && (github.event_name == 'merge_group' || needs.review.result != 'skipped')"
require_job required 'rules/branches/${BASE_BRANCH}'
for fact in rule:merge_queue grouping:ALLGREEN rule:required_status_checks \
    context:required integration:15368:required; do
    require_job required "$fact"
done

# One no-bypass ruleset carries every main-branch rule. The queue has no
# artificial settling delay and its exact combined tree is the only full gate.
require 'apply_ruleset main-protect' "$publisher"
require 'delete_ruleset main-review' "$publisher"
require '"bypass_actors": []' "$publisher"
require '"type": "merge_queue"' "$publisher"
require '"grouping_strategy": "ALLGREEN"' "$publisher"
require '"min_entries_to_merge_wait_minutes": 0' "$publisher"
require '"context": "required", "integration_id": 15368' "$publisher"
require '"type": "pull_request"' "$publisher"
require '"required_approving_review_count": 0' "$publisher"
require '"require_code_owner_review": true' "$publisher"
require '"required_review_thread_resolution": true' "$publisher"

if grep -Fq '"min_entries_to_merge_wait_minutes": 5' "$publisher"; then
    echo "repository settings retain superseded merge ceremony" >&2
    exit 1
fi

require '/.github/workflows/' "$repository_root/.github/CODEOWNERS"
require '/.github/CODEOWNERS' "$repository_root/.github/CODEOWNERS"
require '/scripts/github-repo-settings.sh' "$repository_root/.github/CODEOWNERS"
require '/scripts/verify-adversarial-review.sh' "$repository_root/.github/CODEOWNERS"
if grep -Eq '^/(ARCHITECTURE|SECURITY|CLAUDE)\.md|^/\.github/[[:space:]]' "$repository_root/.github/CODEOWNERS"; then
    echo "CODEOWNERS still blocks ordinary documentation or all of .github" >&2
    exit 1
fi

require 'area:console|1D76DB|loopback web console, browser protocol, client, and UI' "$publisher"
require '"TUI") echo "area:console"' "$issue_importer"
if grep -Eq 'area:tui|factory-tui' "$publisher" "$issue_importer"; then
    echo "repository label surfaces still name the retired TUI" >&2
    exit 1
fi

# The settings migration must distinguish an absent legacy ruleset from an API
# failure. A failed list read may never be reported as successful cleanup.
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-settings.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
mkdir "$temporary/bin"
settings_log="$temporary/calls"
cat >"$temporary/bin/gh" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >>"$DF_SETTINGS_STUB_LOG"
if [ -n "${DF_MERGE_RULE_JSON-}" ]; then
    [ "${DF_MERGE_RULE_API_FAIL-}" != 1 ] || exit 1
    jq -r "$4" "$DF_MERGE_RULE_JSON"
elif [ "$1" = repo ] && [ "$2" = view ]; then
    printf '%s\n' dark-factory-build/dark-factory
elif [ "$1" = api ] && [ "$2" = repos/dark-factory-build/dark-factory/rulesets ] && [ "${3-}" = --jq ]; then
    [ "${DF_SETTINGS_STUB_LIST_FAIL-}" != 1 ] || exit 1
    if [ "${DF_SETTINGS_STUB_EXISTING-}" = 1 ]; then
        case "$*" in
            *main-protect*) printf '%s\n' 11 ;;
            *main-review*) printf '%s\n' 22 ;;
        esac
    fi
elif [ "$1" = api ] && [ "${2-}" = -X ] && { [ "${3-}" = PUT ] || [ "${3-}" = POST ]; }; then
    [ "${DF_SETTINGS_STUB_APPLY_FAIL-}" != 1 ] || exit 1
fi
STUB
chmod 755 "$temporary/bin/gh"

# Drive the exact CODEOWNED scope body. Documentation and control-plane-only
# changes skip macOS; source, workflow, mixed, and unknown changes fail closed.
awk '
    /^      - name: Select fixed gates from the combined tree$/ { step = 1; next }
    step && /^        run: \|$/ { body = 1; next }
    body && /^          / { sub(/^          /, ""); print; next }
    body && /^ *$/ { print ""; next }
    body { exit }
' "$workflow" >"$temporary/scope.sh"
cat >"$temporary/bin/git" <<'STUB'
#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$DF_SCOPE_GIT_LOG"
case "$1:${2-}" in
    init:|remote:add|fetch:*) ;;
    diff:--check) [ "${DF_SCOPE_DIFF_FAIL-}" != 1 ] ;;
    diff:--name-only)
        while IFS= read -r path; do [ -z "$path" ] || printf '%s\0' "$path"; done <"$DF_SCOPE_PATHS"
        ;;
    *) exit 1 ;;
esac
STUB
chmod 755 "$temporary/bin/git"
run_scope() (
    event=$1
    base=$2
    diff_fail=$3
    shift 3
    printf '%s\n' "$@" >"$temporary/scope-paths"
    : >"$temporary/scope-output"
    : >"$temporary/scope-git-log"
    export PATH="$temporary/bin:/usr/bin:/bin"
    export DF_SCOPE_PATHS="$temporary/scope-paths" DF_SCOPE_GIT_LOG="$temporary/scope-git-log"
    export GITHUB_EVENT_NAME="$event" BASE_SHA="$base"
    export GITHUB_SHA=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
    export GITHUB_REPOSITORY=dark-factory-build/dark-factory
    export GITHUB_OUTPUT="$temporary/scope-output" RUNNER_TEMP="$temporary"
    [ "$diff_fail" = false ] || export DF_SCOPE_DIFF_FAIL=1
    CDPATH= cd -- "$temporary"
    /bin/bash "$temporary/scope.sh"
)
scope_case() {
    want_macos=$1
    want_control=$2
    shift 2
    run_scope merge_group aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa false "$@"
    [ "$(sort "$temporary/scope-output" | tr '\n' ' ')" = \
        "control_plane=$want_control macos=$want_macos " ]
}
scope_case false false docs/install.md README.md
scope_case false true control-plane
scope_case false true control-plane/src/lib.rs
scope_case false true .github/workflows/deploy-control-plane.yml
scope_case true false internal/kernel/store.go
scope_case true true control-plane/src/lib.rs internal/kernel/store.go
scope_case true true .github/workflows/ci.yml
scope_case true true .gitignore
scope_case true true scripts/bootstrap-maintainer-v2.sh
scope_case true false unclassified-boundary
run_scope workflow_dispatch '' false
[ "$(sort "$temporary/scope-output" | tr '\n' ' ')" = \
    'control_plane=true macos=true ' ]
if run_scope merge_group bad false >/dev/null 2>&1; then
    echo "invalid scope commit passed" >&2
    exit 1
fi
if run_scope merge_group aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa true >/dev/null 2>&1; then
    echo "failing combined-tree diff check passed" >&2
    exit 1
fi

# Execute the actual inline merge-rule step, including its jq projection, from
# raw ruleset JSON. Every missing or misbound fact and an API failure must stop.
awk '
    /^      - name: Confirm the live merge rules$/ { step = 1; next }
    step && /^        run: \|$/ { body = 1; next }
    body && /^          / { sub(/^          /, ""); print; next }
    body && /^ *$/ { print ""; next }
    body { exit }
' "$workflow" >"$temporary/merge-rule-step.sh"
merge_rules_base="$temporary/merge-rules-base.json"
merge_rules="$temporary/merge-rules.json"
cat >"$merge_rules_base" <<'RULES'
[
  {"type":"merge_queue","parameters":{"grouping_strategy":"ALLGREEN"}},
  {"type":"required_status_checks","parameters":{"required_status_checks":[
    {"context":"required","integration_id":15368}
  ]}},
  {"type":"pull_request","parameters":{}}
]
RULES
run_merge_rule_step() (
    export DF_MERGE_RULE_JSON="$merge_rules"
    export DF_SETTINGS_STUB_LOG="$settings_log"
    export GITHUB_REPOSITORY=dark-factory-build/dark-factory
    export BASE_BRANCH=main
    export RUNNER_TEMP="$temporary"
    PATH="$temporary/bin:$PATH" bash "$temporary/merge-rule-step.sh" \
        >"$temporary/merge.out" 2>"$temporary/merge.err"
)
cp "$merge_rules_base" "$merge_rules"
run_merge_rule_step
expect_rule_failure() {
    label=$1
    filter=$2
    jq "$filter" "$merge_rules_base" >"$merge_rules"
    if run_merge_rule_step; then
        echo "invalid live merge rules passed: $label" >&2
        exit 1
    fi
}
expect_rule_failure no-queue 'map(select(.type != "merge_queue"))'
expect_rule_failure wrong-grouping 'map(if .type == "merge_queue" then .parameters.grouping_strategy = "HEADGREEN" else . end)'
expect_rule_failure no-required-rule 'map(select(.type != "required_status_checks"))'
expect_rule_failure wrong-context 'map(if .type == "required_status_checks" then .parameters.required_status_checks[0].context = "other" else . end)'
expect_rule_failure wrong-integration 'map(if .type == "required_status_checks" then .parameters.required_status_checks[0].integration_id = 42 else . end)'
expect_rule_failure misbound-integration 'map(if .type == "required_status_checks" then .parameters.required_status_checks = [{"context":"required","integration_id":null},{"context":"other","integration_id":15368}] else . end)'
cp "$merge_rules_base" "$merge_rules"
if (DF_MERGE_RULE_API_FAIL=1; export DF_MERGE_RULE_API_FAIL; run_merge_rule_step); then
    echo "merge-rule API failure passed" >&2
    exit 1
fi

DF_SETTINGS_STUB_LOG="$settings_log" PATH="$temporary/bin:$PATH" \
    "$publisher" >"$temporary/ok.out" 2>"$temporary/ok.err"
grep -Fq -- '-X POST repos/dark-factory-build/dark-factory/rulesets' "$settings_log"

: >"$settings_log"
DF_SETTINGS_STUB_EXISTING=1 DF_SETTINGS_STUB_LOG="$settings_log" \
    PATH="$temporary/bin:$PATH" "$publisher" >"$temporary/update.out" 2>"$temporary/update.err"
grep -Fq -- '-X PUT repos/dark-factory-build/dark-factory/rulesets/11' "$settings_log"
grep -Fq -- '-X DELETE repos/dark-factory-build/dark-factory/rulesets/22' "$settings_log"

if DF_SETTINGS_STUB_LIST_FAIL=1 DF_SETTINGS_STUB_LOG="$settings_log" \
    PATH="$temporary/bin:$PATH" "$publisher" >"$temporary/fail.out" 2>"$temporary/fail.err"; then
    echo "ruleset-list failure was reported as successful" >&2
    exit 1
fi
grep -Fq 'FAILED: cannot list rulesets' "$temporary/fail.err"

: >"$settings_log"
if DF_SETTINGS_STUB_EXISTING=1 DF_SETTINGS_STUB_APPLY_FAIL=1 \
    DF_SETTINGS_STUB_LOG="$settings_log" PATH="$temporary/bin:$PATH" \
    "$publisher" >"$temporary/apply-fail.out" 2>"$temporary/apply-fail.err"; then
    echo "ruleset update failure was reported as successful" >&2
    exit 1
fi
if grep -Fq -- '-X DELETE repos/dark-factory-build/dark-factory/rulesets/22' "$settings_log"; then
    echo "legacy pull-request rule was deleted after replacement failed" >&2
    exit 1
fi
grep -Fq 'SKIPPED: obsolete main-review remains' "$temporary/apply-fail.err"

echo "repository settings and event-selective CI contract passed"
