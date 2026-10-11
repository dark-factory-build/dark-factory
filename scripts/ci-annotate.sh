#!/bin/sh
# Turns a failed gate log into GitHub annotations: the last stage the gate
# announced, then the first distinct failing tests, compile and vet errors,
# timeouts and Rust panics or test failures.
log=$1
grep -E '^(local-ci|go-check|go-ci|control-plane local-ci): ' "$log" | tail -n 1 |
    sed -e 's/%/%25/g' -e 's/^/::error title=Failing stage::/'
grep -E '^[[:space:]]*(--- FAIL: |not ok |✖ )|^[^[:space:]]+\.go:[0-9]+(:[0-9]+)?: |^panic: test timed out|panicked at|^error(\[E[0-9]+\])?: |^test .* \.\.\. FAILED$' "$log" |
    sed -e 's/^[[:space:]]*//' | awk '$0 != "✖ failing tests:" && !seen[$0]++' | head -n 10 |
    sed -e 's/%/%25/g' -e 's/^/::error title=Failure::/'
exit 0
