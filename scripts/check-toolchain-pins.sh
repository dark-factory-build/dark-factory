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

# The Rust workspace is deleted. These guards keep its toolchain from
# reappearing in the release or gate paths through an unreviewed edit.
for workflow in .github/workflows/release.yml .github/workflows/ci.yml; do
    if grep -Eq 'rustup|cargo[[:space:]]+\+' "$workflow"; then
        echo "$workflow retains the deleted Rust toolchain" >&2
        exit 1
    fi
done
if [ -e Cargo.toml ] || [ -e Cargo.lock ] || [ -d crates ]; then
    echo "the deleted Rust runtime workspace has reappeared" >&2
    exit 1
fi
