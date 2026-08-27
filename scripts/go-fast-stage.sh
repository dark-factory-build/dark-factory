#!/bin/sh

# Sourced by go-check.sh and go-ci.sh after one shared environment setup.
go_gate_fast_stage() {
    go_gate_expected_version=$(/usr/bin/awk '
        $1 == "go" { count++; value=$2 }
        END { if (count != 1 || value !~ /^[0-9]+\.[0-9]+\.[0-9]+$/) exit 1; print value }
    ' go.mod) || {
        echo "go-check: go.mod must contain exactly one fully pinned go directive" >&2
        return 1
    }
    go_gate_actual_line=$(go_gate_stage 30 "$go_gate_go" version)
    go_gate_actual_version=$(printf '%s\n' "$go_gate_actual_line" \
        | /usr/bin/awk '$1 == "go" && $2 == "version" && $3 ~ /^go[0-9]+\.[0-9]+\.[0-9]+$/ { sub(/^go/, "", $3); print $3 }')
    [ "$go_gate_actual_version" = "$go_gate_expected_version" ] || {
        echo "go-check: expected Go $go_gate_expected_version, got: $go_gate_actual_line" >&2
        return 1
    }

    export GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org
    go_gate_stage 600 "$go_gate_go" mod download || return
    export GOPROXY=off GOSUMDB=off
    go_gate_stage 120 "$go_gate_go" mod verify || return

    go_gate_go_files="$go_gate_root/go-files"
    go_gate_stage 120 "$go_gate_git" ls-files -z -- '*.go' >"$go_gate_go_files" || return
    [ -s "$go_gate_go_files" ] || { echo "go-check: no tracked Go files" >&2; return 1; }
    if ! go_gate_stage 120 "$go_gate_xargs" -0 "$go_gate_gofmt" -l -- <"$go_gate_go_files" >"$go_gate_root/gofmt.out"; then
        echo "go-check: gofmt failed" >&2
        return 1
    fi
    [ ! -s "$go_gate_root/gofmt.out" ] || { echo "go-check: gofmt required" >&2; return 1; }

    echo "go-check: go vet ./..."
    go_gate_stage 600 "$go_gate_go" vet ./... || return
    echo "go-check: focused pure-package tests"
    go_gate_stage 900 "$go_gate_go" test -timeout=5m -count=1 ./internal/kernel ./internal/browserprotocol ./internal/sqlitecontract || return

    [ -x "$go_gate_corepack" ] || { echo "go-check: fixed corepack is unavailable" >&2; return 1; }
    echo "go-check: frozen TypeScript dependency install"
    (
        cd "$repository_root/web" || exit
        export COREPACK_ENABLE_NETWORK=1
        go_gate_stage 600 "$go_gate_corepack" pnpm install --frozen-lockfile --ignore-scripts
    ) || return
    echo "go-check: TypeScript client typecheck and tests"
    (
        cd "$repository_root/web" || exit
        export COREPACK_ENABLE_NETWORK=0
        go_gate_stage 600 "$go_gate_corepack" pnpm --offline run typecheck || exit
        go_gate_stage 600 "$go_gate_corepack" pnpm --offline run test
    ) || return

    echo "go-check: git diff --check"
    go_gate_stage 120 "$go_gate_git" diff --check
}
