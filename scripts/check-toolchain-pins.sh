#!/bin/sh
set -eu

check_pin() {
    file=$1
    expected=$2
    description=$3
    if ! grep -Fq "$expected" "$file"; then
        echo "$file does not use $description" >&2
        exit 1
    fi
}

check_pin scripts/local-ci.sh "go-ci-owned.sh" "the authoritative Go gate stage"

go_version=$(sed -n 's/^go \([0-9][0-9.]*\)$/\1/p' go.mod)
case "$go_version" in
    *.*.*) ;;
    *) echo "could not read the exact Go version from go.mod" >&2; exit 1 ;;
esac
check_pin .github/workflows/release.yml "GOTOOLCHAIN=go$go_version" "runtime Go $go_version"
check_pin .github/workflows/release.yml "GOOS=darwin GOARCH=arm64" "the exact Darwin arm64 release target"
check_pin .github/workflows/release.yml "GOOS=darwin GOARCH=amd64" "the exact Darwin amd64 release target"

# The Rust runtime workspace is deleted. These guards keep its toolchain from
# reappearing through an unreviewed edit. The control-plane job is deliberately
# out of scope: that Worker is a separate, still-Rust, still-deployed package
# with its own gate, so its rustup step is correct and must survive.
if grep -Eq 'rustup|cargo[[:space:]]+\+' .github/workflows/release.yml; then
    echo ".github/workflows/release.yml retains the deleted Rust toolchain" >&2
    exit 1
fi
# Scan the WHOLE workflow with only the control-plane job's own block removed:
# a window ending at that job would let anything appended after it — the
# natural place a new job lands — escape the guard entirely.
runtime_jobs=$(awk '
    /^  control-plane:$/ { skip = 1; next }
    skip && /^  [^ #]/  { skip = 0 }
    !skip               { print }
' .github/workflows/ci.yml)
if printf '%s\n' "$runtime_jobs" | grep -Eq 'rustup|cargo[[:space:]]+\+'; then
    echo ".github/workflows/ci.yml runs the deleted Rust toolchain outside control-plane" >&2
    exit 1
fi
if [ -e Cargo.toml ] || [ -e Cargo.lock ] || [ -d crates ]; then
    echo "the deleted Rust runtime workspace has reappeared" >&2
    exit 1
fi
