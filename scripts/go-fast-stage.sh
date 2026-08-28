#!/bin/sh

# Sourced by go-check.sh and go-ci.sh after one shared environment setup.
go_gate_web_scratch_fence() {
    go_gate_validate_identity && go_gate_validate_directories || {
        echo "go-check: web dependency scratch identity changed" >&2
        return 1
    }
}

go_gate_web_tree_move() {
    go_gate_web_tree_source=$1
    go_gate_web_tree_expected=${3-}
    go_gate_web_scratch_fence || return 1
    go_gate_web_tree_destination=$2
    if [ -L "$go_gate_web_tree_source" ]; then
        echo "go-check: refusing symlink web dependency tree: $go_gate_web_tree_source" >&2
        return 1
    fi
    if [ ! -e "$go_gate_web_tree_source" ]; then
        [ -z "$go_gate_web_tree_expected" ] || {
            echo "go-check: web dependency tree disappeared" >&2
            return 1
        }
        return 0
    fi
    [ -d "$go_gate_web_tree_source" ] || {
        echo "go-check: web dependency tree is not a directory" >&2
        return 1
    }
    go_gate_web_tree_identity=$(go_gate_stat "$go_gate_web_tree_source") || return 1
    [ -z "$go_gate_web_tree_expected" ] || [ "$go_gate_web_tree_identity" = "$go_gate_web_tree_expected" ] || {
        echo "go-check: web dependency tree identity changed" >&2
        return 1
    }
    [ ! -e "$go_gate_web_tree_destination" ] && [ ! -L "$go_gate_web_tree_destination" ] || {
        echo "go-check: web dependency quarantine already exists" >&2
        return 1
    }
    go_gate_web_scratch_fence || return 1
    /bin/mv "$go_gate_web_tree_source" "$go_gate_web_tree_destination" || return 1
    go_gate_web_scratch_fence || return 1
    [ ! -e "$go_gate_web_tree_source" ] && [ ! -L "$go_gate_web_tree_source" ] || return 1
    [ "$(go_gate_stat "$go_gate_web_tree_destination")" = "$go_gate_web_tree_identity" ] || return 1
}

go_gate_web_tree_discard() {
    go_gate_web_tree_source=$1
    go_gate_web_tree_expected=$2
    go_gate_web_scratch_fence || return 1
    go_gate_web_tree_discard_path="$go_gate_root/web-node-modules-discard"
    go_gate_web_tree_move "$go_gate_web_tree_source" "$go_gate_web_tree_discard_path" "$go_gate_web_tree_expected" || return 1
    go_gate_web_scratch_fence || return 1
    /bin/rm -rf -- "$go_gate_web_tree_discard_path"
    go_gate_web_scratch_fence || return 1
}

go_gate_web_install() (
    umask 022
    go_gate_web_install_path="$repository_root/web/node_modules"
    go_gate_web_install_parent="$repository_root/web"
    go_gate_web_scratch_fence || exit 1
    go_gate_web_install_old="$go_gate_root/web-node-modules-old"
    [ ! -L "$go_gate_web_install_parent" ] && [ -d "$go_gate_web_install_parent" ] || exit 1
    go_gate_web_install_parent_identity=$(go_gate_stat "$go_gate_web_install_parent") || exit 1
    go_gate_web_install_old_identity=
    if [ -L "$go_gate_web_install_path" ]; then
        echo "go-check: refusing symlink web dependency tree: $go_gate_web_install_path" >&2
        exit 1
    fi
    if [ -e "$go_gate_web_install_path" ]; then
        [ -d "$go_gate_web_install_path" ] || {
            echo "go-check: web dependency tree is not a directory" >&2
            exit 1
        }
        [ ! -L "$go_gate_web_install_path/.modules.yaml" ] && [ -f "$go_gate_web_install_path/.modules.yaml" ] || {
            echo "go-check: existing web dependency tree is not package-manager-owned" >&2
            exit 1
        }
        [ ! -L "$go_gate_web_install_path/.pnpm" ] && [ -d "$go_gate_web_install_path/.pnpm" ] || {
            echo "go-check: existing web virtual store is invalid" >&2
            exit 1
        }
        go_gate_web_install_old_identity=$(go_gate_stat "$go_gate_web_install_path") || exit 1
        go_gate_web_tree_move "$go_gate_web_install_path" "$go_gate_web_install_old" "$go_gate_web_install_old_identity" || exit 1
    fi

    if go_gate_package_manager_stage 600 pnpm install --frozen-lockfile --ignore-scripts; then
        go_gate_web_install_status=0
    else
        go_gate_web_install_status=$?
    fi
    if [ "$go_gate_web_install_status" -ne 0 ]; then
        go_gate_web_install_cleanup=0
        if [ -e "$go_gate_web_install_path" ] || [ -L "$go_gate_web_install_path" ]; then
            go_gate_web_install_partial_identity=$(go_gate_stat "$go_gate_web_install_path" 2>/dev/null || true)
            if [ -n "$go_gate_web_install_partial_identity" ]; then
                go_gate_web_tree_discard "$go_gate_web_install_path" "$go_gate_web_install_partial_identity" || go_gate_web_install_cleanup=1
            else
                go_gate_web_install_cleanup=1
            fi
        fi
        if [ -n "$go_gate_web_install_old_identity" ]; then
            go_gate_web_tree_discard "$go_gate_web_install_old" "$go_gate_web_install_old_identity" || go_gate_web_install_cleanup=1
        fi
        [ "$go_gate_web_install_cleanup" -eq 0 ] || return 1
        return "$go_gate_web_install_status"
    fi
    if [ "$(go_gate_stat "$go_gate_web_install_parent" 2>/dev/null || true)" != "$go_gate_web_install_parent_identity" ]; then
        echo "go-check: web dependency parent identity changed" >&2
        return 1
    fi
    if [ -L "$go_gate_web_install_path" ] || [ ! -d "$go_gate_web_install_path" ] || [ -L "$go_gate_web_install_path/.modules.yaml" ] || [ ! -f "$go_gate_web_install_path/.modules.yaml" ] || [ -L "$go_gate_web_install_path/.pnpm" ] || [ ! -d "$go_gate_web_install_path/.pnpm" ]; then
        echo "go-check: package manager produced an invalid web dependency tree" >&2
        go_gate_web_install_cleanup=0
        go_gate_web_install_partial_identity=$(go_gate_stat "$go_gate_web_install_path" 2>/dev/null || true)
        if [ -n "$go_gate_web_install_partial_identity" ]; then
            go_gate_web_tree_discard "$go_gate_web_install_path" "$go_gate_web_install_partial_identity" || go_gate_web_install_cleanup=1
        fi
        if [ -n "$go_gate_web_install_old_identity" ]; then
            go_gate_web_tree_discard "$go_gate_web_install_old" "$go_gate_web_install_old_identity" || go_gate_web_install_cleanup=1
        fi
        [ "$go_gate_web_install_cleanup" -eq 0 ] || return 1
        return 1
    fi
    if [ -n "$go_gate_web_install_old_identity" ]; then
        go_gate_web_tree_discard "$go_gate_web_install_old" "$go_gate_web_install_old_identity" || return 1
    fi
    go_gate_web_install_partial_identity=$(go_gate_stat "$go_gate_web_install_path") || return 1
    [ -n "$go_gate_web_install_partial_identity" ]
)

go_gate_web_verify_toolchain() {
    go_gate_web_node_modules="$repository_root/web/node_modules"
    [ ! -L "$go_gate_web_node_modules" ] && [ -d "$go_gate_web_node_modules" ] || {
        echo "go-check: web dependency tree is not a regular directory" >&2
        return 1
    }
    go_gate_web_node_modules_real=$(/bin/realpath "$go_gate_web_node_modules") || return 1
    [ "$go_gate_web_node_modules_real" = "$go_gate_web_node_modules" ] || {
        echo "go-check: web dependency tree path is not canonical" >&2
        return 1
    }
    go_gate_web_typescript_path="$go_gate_web_node_modules/typescript"
    go_gate_web_typescript_root=$(/bin/realpath "$go_gate_web_typescript_path") || return 1
    case "$go_gate_web_typescript_root" in
        "$go_gate_web_node_modules_real"/*) ;;
        *) echo "go-check: TypeScript resolves outside the installed web dependency tree" >&2; return 1 ;;
    esac
    [ -d "$go_gate_web_typescript_root" ] && [ ! -L "$go_gate_web_typescript_root" ] || {
        echo "go-check: TypeScript tool tree is not a regular directory" >&2
        return 1
    }
    go_gate_web_toolchain_output="$go_gate_root/web-typescript-tree"
    go_gate_stage_to_file "$go_gate_web_toolchain_output" 120 "$go_gate_node" --input-type=module --eval '
const { readFileSync } = await import("node:fs");
const { toolTreeDigest } = await import(process.argv[2]);
const reviewed = JSON.parse(readFileSync(process.argv[3], "utf8"));
const actual = toolTreeDigest(process.argv[4]);
if (actual !== reviewed.typescript.treeSha512) throw new Error("installed TypeScript content differs from reviewed toolchain");
process.stdout.write(`${actual}\n`);
' /dev/null "$repository_root/web/scripts/package-artifacts.mjs" "$repository_root/web/toolchain-integrity.json" "$go_gate_web_typescript_root" || return 1
}

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
        go_gate_web_install
    ) || return
    go_gate_web_verify_toolchain || return
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
