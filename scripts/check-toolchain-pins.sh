#!/bin/sh
set -eu

workspace_version=$(sed -n 's/^rust-version = "\([^"]*\)"/\1/p' Cargo.toml)
case "$workspace_version" in
    *.*.*) toolchain_version=$workspace_version ;;
    *.*) toolchain_version="$workspace_version.0" ;;
    *) echo "could not read workspace rust-version from Cargo.toml" >&2; exit 1 ;;
esac

check_pin() {
    file=$1
    expected=$2
    description=$3
    if ! grep -Fq "$expected" "$file"; then
        echo "$file does not use $description" >&2
        exit 1
    fi
}

check_pin scripts/local-ci.sh "cargo +$toolchain_version fmt" "workspace Rust $toolchain_version"
check_pin scripts/local-ci.sh "cargo +$toolchain_version clippy" "workspace Rust $toolchain_version"
check_pin scripts/local-ci.sh "cargo +$toolchain_version test" "workspace Rust $toolchain_version"
check_pin .github/workflows/ci.yml "rustup toolchain install $toolchain_version" "workspace Rust $toolchain_version"

go_version=$(sed -n 's/^go \([0-9][0-9.]*\)$/\1/p' go.mod)
case "$go_version" in
    *.*.*) ;;
    *) echo "could not read the exact Go version from go.mod" >&2; exit 1 ;;
esac
check_pin .github/workflows/release.yml "GOTOOLCHAIN=go$go_version" "runtime Go $go_version"
check_pin .github/workflows/release.yml "GOOS=darwin GOARCH=arm64" "the exact Darwin arm64 release target"
check_pin .github/workflows/release.yml "GOOS=darwin GOARCH=amd64" "the exact Darwin amd64 release target"
if grep -Eq 'rustup|cargo[[:space:]]+\+' .github/workflows/release.yml; then
    echo ".github/workflows/release.yml retains the replaced Rust local-runtime build" >&2
    exit 1
fi
