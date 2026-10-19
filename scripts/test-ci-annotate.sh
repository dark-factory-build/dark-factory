#!/bin/sh
set -eu

script=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/ci-annotate.sh
temporary=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-ci-annotate-test.XXXXXX")
trap 'rm -rf "$temporary"' EXIT HUP INT TERM
# Node spec reporter, Go test and compile, Rust and TAP failures in one log: the
# ✖ line repeats in the failing-tests section with its assertion beneath it.
cat >"$temporary/gate.log" <<'EOF'
local-ci: ordinary source gate
go-check: TypeScript install, build, and tests
✖ StrictMode selected-agent composition owns one real terminal surface (0.9ms)
✔ unrelated passing test (0.1ms)
ℹ fail 1

✖ failing tests:

test at probe.test.mjs:3:1
✖ StrictMode selected-agent composition owns one real terminal surface (0.9ms)
  AssertionError [ERR_ASSERTION]: expected one terminal surface

  2 !== 1

      at TestContext.<anonymous> (file:///web/probe.test.mjs:4:10)
    code: 'ERR_ASSERTION',
--- FAIL: TestSurface (0.00s)
    surface_test.go:12: got 2 surfaces
./surface.go:3:4: undefined: y
error[E0308]: mismatched types
not ok 2 - tap case
  error: 'tap detail 100%'
EOF
expected='::error title=Failing stage::go-check: TypeScript install, build, and tests
::error title=Failure::✖ StrictMode selected-agent composition owns one real terminal surface (0.9ms)
::error title=Failure::AssertionError [ERR_ASSERTION]: expected one terminal surface
::error title=Failure::--- FAIL: TestSurface (0.00s)
::error title=Failure::./surface.go:3:4: undefined: y
::error title=Failure::error[E0308]: mismatched types
::error title=Failure::not ok 2 - tap case
::error title=Failure::error: '"'"'tap detail 100%25'"'"''
[ "$("$script" "$temporary/gate.log")" = "$expected" ] || { echo "ci-annotate test failed:" >&2; "$script" "$temporary/gate.log" >&2; exit 1; }
# Output stays bounded at ten distinct failures.
for i in 1 2 3 4 5 6 7 8 9 10 11 12; do echo "✖ case $i (1ms)"; echo "  Error: boom $i"; done >"$temporary/many.log"
[ "$("$script" "$temporary/many.log" | wc -l | tr -d ' ')" = 10 ] || { echo "ci-annotate bound test failed:" >&2; "$script" "$temporary/many.log" >&2; exit 1; }
