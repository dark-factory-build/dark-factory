#!/bin/sh

# Shared only by go-check.sh and go-ci.sh. The caller owns the cleanup trap;
# this file creates/validates the per-invocation environment and exports its
# paths. The nonce prevents an arbitrary inherited root from becoming a cache
# or cleanup authority for nested execution.

go_gate_scrub_environment() {
    unset DARK_FACTORY_HOME DARK_FACTORY_SOCKET DARK_FACTORY_PROJECT \
        DARK_FACTORY_AGENT DARK_FACTORY_SESSION DARK_FACTORY_SESSION_TOKEN_FILE \
        DARK_FACTORY_AGENT_DIR DARK_FACTORY_FACTORYCTL DARK_FACTORY_TASK \
        DARK_FACTORY_RUN DARK_FACTORY_ATTEMPT_TOKEN DARK_FACTORY_ATTEMPT_TOKEN_FILE \
        DARK_FACTORY_OPERATOR_TOKEN DARK_FACTORY_OPERATOR_TOKEN_FILE \
        DARK_FACTORY_RUN_ID DARK_FACTORY_TASK_ID DARK_FACTORY_AGENT_ID \
        ANTHROPIC_API_KEY OPENAI_API_KEY CLAUDE_API_KEY CLAUDE_CODE_OAUTH_TOKEN \
        CODEX_API_KEY CLAUDE_CONFIG_DIR CODEX_HOME \
        GITHUB_TOKEN GH_TOKEN GH_ENTERPRISE_TOKEN GITHUB_ENTERPRISE_TOKEN \
        SSH_AUTH_SOCK SSH_AGENT_PID SSH_ASKPASS GIT_ASKPASS GIT_SSH_COMMAND GIT_SSH \
        GIT_CREDENTIAL_HELPER GIT_SSL_CERT GIT_SSL_KEY GIT_SSL_CAINFO \
        GIT_CONFIG_GLOBAL GIT_CONFIG_SYSTEM GIT_CONFIG_NOSYSTEM GIT_TERMINAL_PROMPT \
        GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR GIT_CONFIG_COUNT \
        GIT_CONFIG_PARAMETERS GIT_CONFIG_KEY_0 GIT_CONFIG_VALUE_0 GIT_EXTERNAL_DIFF \
        GIT_DIFF_OPTS GIT_PAGER GIT_EDITOR GIT_SEQUENCE_EDITOR GIT_TRACE GIT_TRACE_PACKET \
        GIT_TRACE_PERFORMANCE GIT_TRACE_SETUP GIT_TRACE_CURL GIT_CURL_VERBOSE \
        GH_CONFIG_DIR GITHUB_CONFIG_DIR \
        NPM_TOKEN NODE_AUTH_TOKEN AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY \
        AWS_SESSION_TOKEN AWS_PROFILE AWS_SHARED_CREDENTIALS_FILE AWS_CONFIG_FILE \
        GOAUTH GOPRIVATE GONOSUMDB GONOPROXY GOINSECURE GOVCS GOFLAGS \
        GO111MODULE GOAMD64 GOARM64 GOFIPS140 GODEBUG GOEXPERIMENT NETRC \
        HTTP_PROXY HTTPS_PROXY ALL_PROXY NO_PROXY \
        npm_config_proxy npm_config_https_proxy npm_config_userconfig NPM_CONFIG_USERCONFIG \
        npm_config_cache NPM_CONFIG_CACHE npm_config_store_dir NPM_CONFIG_STORE_DIR \
        npm_config_registry NPM_CONFIG_REGISTRY npm_config_globalconfig NPM_CONFIG_GLOBALCONFIG \
        npm_config_prefix NPM_CONFIG_PREFIX npm_config_node_gyp NPM_CONFIG_NODE_GYP \
        COREPACK_DEFAULT_TO_LATEST COREPACK_INTEGRITY_KEYS NODE_OPTIONS NODE_EXTRA_CA_CERTS \
        NODE_PATH NODE_DEBUG DYLD_INSERT_LIBRARIES DYLD_LIBRARY_PATH DYLD_FRAMEWORK_PATH \
        LD_PRELOAD LD_LIBRARY_PATH LIBRARY_PATH CPATH C_INCLUDE_PATH CPLUS_INCLUDE_PATH \
        NPM_CONFIG_WORKSPACE
}

go_gate_stat() {
    stat -f '%d:%i:%u:%Lp' "$1" 2>/dev/null
}

go_gate_validate_identity() {
    [ -n "${go_gate_root-}" ] && [ ! -L "$go_gate_root" ] \
        && [ -d "$go_gate_root" ] || return 1
    [ "$(CDPATH= cd -P -- "$go_gate_root" && pwd -P)" = "$go_gate_root" ] || return 1
    [ "$(go_gate_stat "$go_gate_root")" = "$go_gate_root_identity" ] || return 1
    [ "$(id -u)" = "${go_gate_root_uid-}" ] || return 1
    [ "$(stat -f '%Lp' "$go_gate_root" 2>/dev/null)" = 700 ] || return 1
    [ -n "${go_gate_marker-}" ] && [ -n "${go_gate_marker_identity-}" ] || return 0
    [ ! -L "$go_gate_marker" ] && [ -f "$go_gate_marker" ] || return 1
    [ "$(go_gate_stat "$go_gate_marker")" = "$go_gate_marker_identity" ] || return 1
    [ "$(awk -v nonce="$go_gate_nonce" \
        'NR == 1 { value=$0 } NR > 1 { invalid=1 } END { if (invalid || value != nonce) exit 1; print value }' \
        "$go_gate_marker" 2>/dev/null)" = "$go_gate_nonce" ] || return 1
}

go_gate_validate_directories() {
    for go_gate_directory in tmp cache modcache corepack npm-cache; do
        go_gate_directory_path="$go_gate_root/$go_gate_directory"
        [ ! -L "$go_gate_directory_path" ] && [ -d "$go_gate_directory_path" ] || return 1
        [ "$(CDPATH= cd -P -- "$go_gate_directory_path" 2>/dev/null && pwd -P)" = "$go_gate_directory_path" ] || return 1
        case "$go_gate_directory" in
            tmp) go_gate_expected_identity=${go_gate_tmp_identity-} ;;
            cache) go_gate_expected_identity=${go_gate_cache_identity-} ;;
            modcache) go_gate_expected_identity=${go_gate_modcache_identity-} ;;
            corepack) go_gate_expected_identity=${go_gate_corepack_identity-} ;;
            npm-cache) go_gate_expected_identity=${go_gate_npm_cache_identity-} ;;
            *) return 1 ;;
        esac
        [ "$(go_gate_stat "$go_gate_directory_path")" = "$go_gate_expected_identity" ] || return 1
        [ "$(stat -f '%Lp' "$go_gate_directory_path" 2>/dev/null)" = 700 ] || return 1
    done
}

go_gate_before_stage() {
    go_gate_validate_identity && go_gate_validate_directories || {
        echo "go gate: scratch identity changed before stage" >&2
        return 1
    }
}

go_gate_run_bounded() {
    go_gate_timeout_seconds=$1
    shift
    command -v perl >/dev/null 2>&1 || {
        echo "go gate: perl is required for bounded stages" >&2
        return 1
    }
    perl -e '
use strict;
use warnings;
use POSIX qw(setpgid WIFEXITED WEXITSTATUS WIFSIGNALED WTERMSIG);

my $seconds = shift @ARGV;
die "invalid timeout\n" unless defined $seconds && $seconds =~ /^\d+$/ && $seconds > 0;
die "missing command\n" unless @ARGV;
my $pid = fork();
die "fork failed: $!\n" unless defined $pid;
if ($pid == 0) {
    setpgid(0, 0);
    exec @ARGV;
    exit 127;
}
setpgid($pid, $pid);
my $timed_out = 0;
my $term_sent = 0;
$SIG{ALRM} = sub {
    $timed_out = 1;
    if (!$term_sent) {
        kill "TERM", -$pid;
        $term_sent = 1;
        alarm 1;
    } else {
        kill "KILL", -$pid;
        alarm 1;
    }
};
alarm $seconds;
waitpid($pid, 0);
alarm 0;
if ($timed_out) {
    waitpid($pid, 0);
    exit 124;
}
my $status = $?;
my $exit_code = WIFEXITED($status) ? WEXITSTATUS($status) : 128 + WTERMSIG($status);
exit $exit_code;
' "$go_gate_timeout_seconds" "$@"
}

go_gate_stage() {
    go_gate_before_stage || return 1
    go_gate_stage_status=0
    if go_gate_run_bounded "$@"; then
        go_gate_stage_status=0
    else
        go_gate_stage_status=$?
    fi
    go_gate_before_stage || return 1
    return "$go_gate_stage_status"
}

go_gate_environment_setup() {
    if [ -n "${DARK_FACTORY_GO_GATE_ROOT-}" ]; then
        go_gate_root=$DARK_FACTORY_GO_GATE_ROOT
        go_gate_owned=0
        go_gate_nonce=${DARK_FACTORY_GO_GATE_NONCE-}
        [ -n "$go_gate_nonce" ] || {
            echo "go gate: caller-provided root has no provenance nonce" >&2
            return 1
        }
    else
        umask 077
        go_gate_root=$(mktemp -d /private/tmp/dark-factory-go.XXXXXXXX) || return 1
        go_gate_owned=1
        go_gate_nonce=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n') || return 1
        [ -n "$go_gate_nonce" ] || return 1
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
    go_gate_root_uid=$(id -u)
    go_gate_root_identity=$(go_gate_stat "$go_gate_root") || return 1
    go_gate_mode=$(stat -f '%Lp' "$go_gate_root" 2>/dev/null || true)
    [ "$go_gate_mode" = 700 ] || {
        echo "go gate: scratch root is not mode 0700" >&2
        return 1
    }
    go_gate_marker="$go_gate_root/.provenance"
    if [ "$go_gate_owned" -eq 1 ]; then
        (set -C; printf '%s\n' "$go_gate_nonce" >"$go_gate_marker") || return 1
        chmod 600 "$go_gate_marker" || return 1
    fi
    [ ! -L "$go_gate_marker" ] && [ -f "$go_gate_marker" ] || {
        echo "go gate: provenance marker is not a regular file" >&2
        return 1
    }
    go_gate_marker_identity=$(go_gate_stat "$go_gate_marker") || return 1
    go_gate_marker_mode=$(stat -f '%Lp' "$go_gate_marker" 2>/dev/null || true)
    [ "$go_gate_marker_mode" = 600 ] || {
        echo "go gate: provenance marker is not mode 0600" >&2
        return 1
    }
    [ "$(cat "$go_gate_marker")" = "$go_gate_nonce" ] || {
        echo "go gate: provenance marker does not match caller" >&2
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
        case "$go_gate_directory" in
            tmp) go_gate_tmp_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            cache) go_gate_cache_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            modcache) go_gate_modcache_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            corepack) go_gate_corepack_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            npm-cache) go_gate_npm_cache_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            *) return 1 ;;
        esac
    done
    go_gate_validate_identity && go_gate_validate_directories || {
        echo "go gate: scratch identity changed during setup" >&2
        return 1
    }
    export TMPDIR="$go_gate_root/tmp"
    export GOTMPDIR="$go_gate_root/tmp"
    export GOCACHE="$go_gate_root/cache"
    export GOMODCACHE="$go_gate_root/modcache"
    export GOPROXY=off
    export GOSUMDB=off
    export GOENV=off
    export GOWORK=off
    export LC_ALL=C
    export LANG=C
    export COREPACK_HOME="$go_gate_root/corepack"
    export npm_config_cache="$go_gate_root/npm-cache"
    export NPM_CONFIG_CACHE="$go_gate_root/npm-cache"
    go_gate_scrub_environment
    # /dev/null is an immutable, descriptor-stable negative config. Keeping
    # these paths outside the scratch tree avoids creating more cleanup
    # authority or another path-swap surface.
    export GIT_CONFIG_GLOBAL=/dev/null
    export GIT_CONFIG_SYSTEM=/dev/null
    export GIT_CONFIG_NOSYSTEM=1
    export NETRC=/dev/null
    export GOAUTH=off
    export GOFLAGS=
    export GOINSECURE=
    export GOVCS='*:off'
    export npm_config_userconfig=/dev/null
    export NPM_CONFIG_USERCONFIG=/dev/null
    export npm_config_globalconfig=/dev/null
    export NPM_CONFIG_GLOBALCONFIG=/dev/null
    export npm_config_cache="$go_gate_root/npm-cache"
    export NPM_CONFIG_CACHE="$go_gate_root/npm-cache"
    export npm_config_registry=https://registry.npmjs.org/
    export NPM_CONFIG_REGISTRY=https://registry.npmjs.org/
    export DARK_FACTORY_GO_GATE_ROOT="$go_gate_root"
    export DARK_FACTORY_GO_GATE_NONCE="$go_gate_nonce"
}

go_gate_environment_cleanup() {
    if [ "${go_gate_owned-0}" -eq 1 ] && [ -n "${go_gate_root-}" ] \
        && [ -d "$go_gate_root" ]; then
        if ! go_gate_validate_identity || ! go_gate_validate_directories; then
            echo "go gate: refusing cleanup after scratch identity/provenance changed: $go_gate_root" >&2
            return 1
        fi
        go_gate_cleanup_parent=$(mktemp -d /private/tmp/dark-factory-go-cleanup.XXXXXXXX) || {
            echo "go gate: cannot create cleanup quarantine" >&2
            return 1
        }
        chmod 700 "$go_gate_cleanup_parent" || return 1
        go_gate_cleanup_target="$go_gate_cleanup_parent/root"
        if ! /bin/mv "$go_gate_root" "$go_gate_cleanup_target"; then
            /bin/rmdir "$go_gate_cleanup_parent" 2>/dev/null || true
            echo "go gate: refusing cleanup after scratch path changed" >&2
            return 1
        fi
        if [ ! -d "$go_gate_cleanup_target" ] || [ -L "$go_gate_cleanup_target" ] \
            || [ "$(go_gate_stat "$go_gate_cleanup_target")" != "$go_gate_root_identity" ]; then
            echo "go gate: refusing cleanup of a replaced scratch object: $go_gate_cleanup_target" >&2
            return 1
        fi
        /bin/rm -rf -- "$go_gate_cleanup_target" || {
            echo "go gate: cleanup quarantine removal failed: $go_gate_cleanup_target" >&2
            return 1
        }
        /bin/rmdir "$go_gate_cleanup_parent" 2>/dev/null || true
    fi
}
