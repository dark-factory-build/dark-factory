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
    go_gate_version_output="$go_gate_root/go-version"
    go_gate_stage_to_file "$go_gate_version_output" 30 "$go_gate_go" version || return
    go_gate_actual_line=$(/bin/cat "$go_gate_version_output")
    go_gate_actual_version=$(/usr/bin/printf '%s\n' "$go_gate_actual_line" \
        | /usr/bin/awk '$1 == "go" && $2 == "version" && $3 ~ /^go[0-9]+\.[0-9]+\.[0-9]+$/ { sub(/^go/, "", $3); print $3 }')
    [ "$go_gate_actual_version" = "$go_gate_expected_version" ] || {
        echo "go-check: expected Go $go_gate_expected_version, got: $go_gate_actual_line" >&2
        return 1
    }

    go_gate_node_version_output="$go_gate_root/node-version"
    go_gate_stage_to_file "$go_gate_node_version_output" 30 "$go_gate_node" --version || return
    go_gate_node_version=$(/bin/cat "$go_gate_node_version_output")
    case "$go_gate_node_version" in
        v22.*|v23.*|v24.*|v25.*|v26.*) ;;
        *) echo "go-check: Node 22+ is required for pnpm 11, got: $go_gate_node_version" >&2; return 1 ;;
    esac

    export GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org
    go_gate_stage 600 "$go_gate_go" mod download || return
    export GOPROXY=off GOSUMDB=off
    go_gate_stage 120 "$go_gate_go" mod verify || return

    go_gate_go_files="$go_gate_root/go-files"
    go_gate_prepare_output "$go_gate_go_files" || return
    go_gate_stage_to_file "$go_gate_go_files" 120 "$go_gate_git" ls-files -z -- '*.go' || return
    [ -s "$go_gate_go_files" ] || { echo "go-check: no tracked Go files" >&2; return 1; }
    go_gate_format_output="$go_gate_root/gofmt.out"
    go_gate_prepare_output "$go_gate_format_output" || return
    if ! go_gate_stage_from_file "$go_gate_format_output" "$go_gate_go_files" 120 "$go_gate_xargs" -0 "$go_gate_gofmt" -l --; then
        echo "go-check: gofmt failed" >&2
        return 1
    fi
    [ ! -s "$go_gate_format_output" ] || { echo "go-check: gofmt required" >&2; return 1; }

    echo "go-check: go vet ./..."
    go_gate_stage 600 "$go_gate_go" vet ./... || return
    echo "go-check: focused pure-package tests"
    go_gate_stage 900 "$go_gate_go" test -timeout=5m -count=1 ./internal/kernel ./internal/browserprotocol ./internal/sqlitecontract || return

    [ -x "$go_gate_corepack" ] || { echo "go-check: fixed corepack is unavailable" >&2; return 1; }
    echo "go-check: frozen TypeScript dependency install"
    (
        cd "$repository_root/web" || exit
        export COREPACK_ENABLE_NETWORK=1
        export npm_config_offline=false NPM_CONFIG_OFFLINE=false
        go_gate_package_manager_stage 600 pnpm install --frozen-lockfile --ignore-scripts
    ) || return
    echo "go-check: TypeScript client typecheck and tests"
    (
        cd "$repository_root/web" || exit
        export COREPACK_ENABLE_NETWORK=0
        export npm_config_offline=true NPM_CONFIG_OFFLINE=true
        go_gate_package_manager_stage 600 pnpm run typecheck || exit
        go_gate_package_manager_stage 600 pnpm run test
    ) || return

    echo "go-check: git diff --check"
    go_gate_stage 120 "$go_gate_git" diff --check
}

go_gate_stage_to_file() {
    go_gate_output=$1
    shift
    go_gate_prepare_output "$go_gate_output" || return 1
    go_gate_before_stage || return 1
    go_gate_stage_status=0
    if go_gate_run_bounded "$@" >"$go_gate_output"; then
        go_gate_stage_status=0
    else
        go_gate_stage_status=$?
    fi
    go_gate_before_stage || return 1
    return "$go_gate_stage_status"
}

go_gate_stage_from_file() {
    go_gate_output=$1
    go_gate_input=$2
    shift 2
    go_gate_prepare_output "$go_gate_output" || return 1
    go_gate_before_stage || return 1
    go_gate_stage_status=0
    if go_gate_run_bounded "$@" <"$go_gate_input" >"$go_gate_output"; then
        go_gate_stage_status=0
    else
        go_gate_stage_status=$?
    fi
    go_gate_before_stage || return 1
    return "$go_gate_stage_status"
}

go_gate_prepare_output() {
    go_gate_before_stage || return 1
    if [ -L "$1" ]; then
        echo "go gate: refusing symlink output path: $1" >&2
        return 1
    fi
    [ ! -e "$1" ] || /bin/rm -f -- "$1"
}
