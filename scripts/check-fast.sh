#!/bin/sh
set -eu

[ "$#" -eq 0 ] || { echo "usage: scripts/check-fast.sh" >&2; exit 2; }
script_dir=$(CDPATH= cd -- "$(/usr/bin/dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
CDPATH= cd -- "$repository_root"
. "$script_dir/local-ci-environment.sh"

go=${DF_CI_GO-}
[ -n "$go" ] || {
    echo "check-fast: Go is unavailable; add Go's bin directory to factoryd --tool-path (and its install root to --toolchain-read-roots)" >&2
    exit 1
}

export GOTOOLCHAIN=local
required_go_series=$(awk '
    $1 == "go" { count++; parts=split($2, version, "."); if (parts == 3) series="go" version[1] "." version[2] }
    END { if (count != 1 || series == "") exit 1; print series }
' go.mod)
actual_go=$("$go" env GOVERSION)
case "$actual_go" in
    "$required_go_series".[0-9]*) actual_patch=${actual_go#"$required_go_series".} ;;
    *) actual_patch= ;;
esac
case "${actual_patch-}" in *[!0-9]*|'')
    echo "check-fast: expected $required_go_series.x, got $actual_go" >&2
    exit 1
    ;;
esac

echo "check-fast: download and verify Go modules"
"$go" mod download
"$go" mod verify

check_fast_tmp=$(mktemp -d "${TMPDIR:-/tmp}/dark-factory-check-fast.XXXXXX")
trap 'rm -rf "$check_fast_tmp"' EXIT HUP INT TERM

check_format() {
    echo "check-fast: gofmt"
    gofmt_output=$(git ls-files -z -- '*.go' | xargs -0 "$(dirname "$go")/gofmt" -l)
    [ -z "$gofmt_output" ] || {
        printf '%s\n' "$gofmt_output" >&2
        echo "check-fast: gofmt required" >&2
        return 1
    }
}

check_lint() {
    echo "check-fast: go vet ./..."
    "$go" vet ./...
}

check_tests() {
    echo "check-fast: ordinary Go tests"
    "$go" test -short -timeout=20m \
        ./cmd/cloudflare-admin \
        ./internal/browserprotocol \
        ./internal/cloudflareadmin \
        ./internal/provider \
        ./internal/topology
}

check_format >"$check_fast_tmp/fmt" 2>&1 & fmt_pid=$!
check_lint >"$check_fast_tmp/lint" 2>&1 & lint_pid=$!
check_tests >"$check_fast_tmp/tests" 2>&1 & tests_pid=$!

check_fast_status=0
for check_fast_name in fmt lint tests; do
    eval "check_fast_pid=\${${check_fast_name}_pid}"
    if ! wait "$check_fast_pid"; then
        check_fast_status=1
    fi
    cat "$check_fast_tmp/$check_fast_name"
done

[ "$check_fast_status" -eq 0 ] || exit "$check_fast_status"
echo "check-fast: PASS"
