#!/bin/sh
set -eu

[ "$#" -eq 0 ] || {
    echo "usage: go-check.sh" >&2
    exit 64
}

script_dir=$(CDPATH= cd -- "$(dirname "$0")" && pwd -P)
repository_root=$(CDPATH= cd -- "$script_dir/.." && pwd -P)
CDPATH= cd -- "$repository_root"

# shellcheck source=scripts/local-ci-environment.sh
. "$script_dir/local-ci-environment.sh"
# shellcheck source=scripts/go-gate-environment.sh
. "$script_dir/go-gate-environment.sh"
if ! go_gate_environment_setup; then
    go_gate_environment_cleanup || true
    exit 1
fi

go_gate_status=0
go_gate_cleanup() {
    go_gate_status=$?
    trap - EXIT HUP INT TERM
    go_gate_environment_cleanup || go_gate_status=1
    exit "$go_gate_status"
}
go_gate_signal() {
    go_gate_signal_number=$1
    trap - EXIT HUP INT TERM
    go_gate_environment_cleanup || true
    exit $((128 + go_gate_signal_number))
}
trap go_gate_cleanup EXIT
trap 'go_gate_signal 1' HUP
trap 'go_gate_signal 2' INT
trap 'go_gate_signal 15' TERM

export GOTOOLCHAIN=local
go_gate_expected_version=$(awk '
    $1 == "go" { count++; value=$2 }
    END { if (count != 1 || value !~ /^[0-9]+\.[0-9]+\.[0-9]+$/) exit 1; print value }
' go.mod) || {
    echo "go-check: go.mod must contain exactly one fully pinned go directive" >&2
    exit 1
}
go_gate_actual_line=$(go_gate_stage 30 go version)
go_gate_actual_version=$(printf '%s\n' "$go_gate_actual_line" \
    | awk '$1 == "go" && $2 == "version" && $3 ~ /^go[0-9]+\.[0-9]+\.[0-9]+$/ { sub(/^go/, "", $3); print $3 }')
[ "$go_gate_actual_version" = "$go_gate_expected_version" ] || {
    echo "go-check: expected Go $go_gate_expected_version, got: $go_gate_actual_line" >&2
    exit 1
}

# Network is permitted only in these explicit dependency stages. The module
# cache and Corepack home survive into go-ci's full tests when it owns the root.
export GOPROXY='https://proxy.golang.org'
export GOSUMDB=sum.golang.org
go_gate_stage 600 go mod download
export GOPROXY=off
export GOSUMDB=off
go_gate_stage 120 go mod verify

go_gate_go_files="$go_gate_root/go-files"
go_gate_stage 120 git ls-files -z -- '*.go' >"$go_gate_go_files"
[ -s "$go_gate_go_files" ] || {
    echo "go-check: no tracked Go files" >&2
    exit 1
}
# Read-only formatting check; never rewrite a contributor's work.
# Keep the formatter's exit status and output separate. A formatter failure
# must not become a false-green empty pipeline.
if ! go_gate_stage 120 xargs -0 gofmt -l -- <"$go_gate_go_files" >"$go_gate_root/gofmt.out"; then
    echo "go-check: gofmt failed" >&2
    exit 1
fi
[ ! -s "$go_gate_root/gofmt.out" ] || {
    echo "go-check: gofmt required for the files listed above" >&2
    exit 1
}

echo "go-check: go vet ./..."
GOPROXY=off GOSUMDB=off go_gate_stage 600 go vet ./...

echo "go-check: focused pure-package tests"
export GOTOOLCHAIN=local
go_gate_stage 900 go test -timeout=5m -count=1 ./internal/kernel ./internal/browserprotocol ./internal/sqlitecontract

command -v corepack >/dev/null 2>&1 || {
    echo "go-check: corepack is required for the TypeScript client gate" >&2
    exit 1
}
echo "go-check: frozen TypeScript dependency install"
(
    cd "$repository_root/web"
    export COREPACK_ENABLE_NETWORK=1
    go_gate_stage 600 corepack pnpm install --frozen-lockfile --ignore-scripts
)
echo "go-check: TypeScript client typecheck and tests"
(
    cd "$repository_root/web"
    export COREPACK_ENABLE_NETWORK=0
    go_gate_stage 600 corepack pnpm --offline run typecheck
    go_gate_stage 600 corepack pnpm --offline run test
)

echo "go-check: git diff --check"
go_gate_stage 120 git diff --check
echo "go-check: PASS"
