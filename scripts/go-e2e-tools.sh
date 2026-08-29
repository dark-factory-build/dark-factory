#!/bin/sh

go_e2e_resolve_tool() {
    go_e2e_tool_name=$1
    go_e2e_tool_path=${2-}
    if [ -z "$go_e2e_tool_path" ]; then
        go_e2e_tool_path=$(command -v "$go_e2e_tool_name" 2>/dev/null || true)
    fi
    case "$go_e2e_tool_path" in
        /*) ;;
        *) echo "go-e2e: $go_e2e_tool_name executable is not absolute" >&2; return 1 ;;
    esac
    [ -f "$go_e2e_tool_path" ] && [ -x "$go_e2e_tool_path" ] || {
        echo "go-e2e: $go_e2e_tool_name executable is unavailable: $go_e2e_tool_path" >&2
        return 1
    }
    printf '%s\n' "$go_e2e_tool_path"
}

# The parent gate has already captured and identity-checked these paths. Pass
# them as explicit command inputs so the bounded PATH remains intentionally
# free of Homebrew or other mutable tool directories.
go_gate_e2e_stage() {
    go_gate_e2e_timeout=$1
    shift
    go_gate_stage "$go_gate_e2e_timeout" "$go_gate_env" \
        "DARK_FACTORY_E2E_GO=$go_gate_go" \
        "DARK_FACTORY_E2E_NODE=$go_gate_node" \
        "DARK_FACTORY_E2E_COREPACK=$go_gate_corepack" \
        "$@"
}
