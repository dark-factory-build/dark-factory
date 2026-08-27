#!/bin/sh

# Shared only by go-check.sh and go-ci.sh. The caller owns the cleanup trap;
# this file only creates/validates the per-invocation environment and exports
# its paths.

go_gate_environment_setup() {
    if [ -n "${DARK_FACTORY_GO_GATE_ROOT-}" ]; then
        go_gate_root=$DARK_FACTORY_GO_GATE_ROOT
        go_gate_owned=0
    else
        umask 077
        go_gate_root=$(mktemp -d /private/tmp/dark-factory-go.XXXXXXXX) || return 1
        go_gate_owned=1
    fi
    go_gate_physical_root=$(CDPATH= cd -P -- "$go_gate_root" 2>/dev/null && pwd -P) || {
        echo "go gate: cannot resolve scratch root" >&2
        return 1
    }
    [ "$go_gate_root" = "$go_gate_physical_root" ] || {
        echo "go gate: scratch root is not physically canonical" >&2
        return 1
    }
    case "$go_gate_root" in
        /private/tmp/dark-factory-go.*) ;;
        *) echo "go gate: scratch root escaped /private/tmp" >&2; return 1 ;;
    esac
    go_gate_mode=$(stat -f '%Lp' "$go_gate_root" 2>/dev/null || true)
    [ "$go_gate_mode" = 700 ] || {
        echo "go gate: scratch root is not mode 0700" >&2
        return 1
    }
    for go_gate_directory in tmp cache modcache corepack npm-cache; do
        if [ ! -d "$go_gate_root/$go_gate_directory" ]; then
            mkdir "$go_gate_root/$go_gate_directory" || return 1
            chmod 700 "$go_gate_root/$go_gate_directory" || return 1
        fi
        go_gate_directory_physical=$(CDPATH= cd -P -- "$go_gate_root/$go_gate_directory" \
            2>/dev/null && pwd -P) || return 1
        [ "$go_gate_root/$go_gate_directory" = "$go_gate_directory_physical" ] || {
            echo "go gate: scratch directory is not physically canonical: $go_gate_directory" >&2
            return 1
        }
        go_gate_directory_mode=$(stat -f '%Lp' "$go_gate_root/$go_gate_directory" 2>/dev/null || true)
        [ "$go_gate_directory_mode" = 700 ] || {
            echo "go gate: scratch directory is not mode 0700: $go_gate_directory" >&2
            return 1
        }
    done
    export TMPDIR="$go_gate_root/tmp"
    export GOTMPDIR="$go_gate_root/tmp"
    export GOCACHE="$go_gate_root/cache"
    export GOMODCACHE="$go_gate_root/modcache"
    export GOENV=off
    export GOWORK=off
    export LC_ALL=C
    export LANG=C
    export COREPACK_HOME="$go_gate_root/corepack"
    export npm_config_cache="$go_gate_root/npm-cache"
    export DARK_FACTORY_GO_GATE_ROOT="$go_gate_root"
}

go_gate_environment_cleanup() {
    if [ "${go_gate_owned-0}" -eq 1 ] && [ -n "${go_gate_root-}" ] \
        && [ -d "$go_gate_root" ]; then
        rm -rf -- "$go_gate_root"
    fi
}
