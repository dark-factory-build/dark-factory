#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd -P)
script=$repository_root/scripts/check-fast.sh

/bin/sh -n "$script"
grep -F -x '[ "$#" -eq 0 ] || { echo "usage: scripts/check-fast.sh" >&2; exit 2; }' "$script" >/dev/null
grep -F 'check_format >"$check_fast_tmp/fmt" 2>&1 & fmt_pid=$!' "$script" >/dev/null
grep -F 'check_lint >"$check_fast_tmp/lint" 2>&1 & lint_pid=$!' "$script" >/dev/null
grep -F 'check_tests >"$check_fast_tmp/tests" 2>&1 & tests_pid=$!' "$script" >/dev/null
grep -F 'check-fast: PASS' "$script" >/dev/null

echo "check-fast tests passed"
