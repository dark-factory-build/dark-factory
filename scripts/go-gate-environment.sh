#!/bin/sh

# Shared by the Go gates. Entry points always create the root themselves;
# there is intentionally no caller-selected root or provenance nonce. The
# boundary trusts the caller's captured system tools and excludes malicious
# concurrent same-UID pathname races; hashes detect ordinary in-place mutation.

go_gate_stat() { /usr/bin/stat -f '%d:%i:%u:%Lp' "$1" 2>/dev/null; }

go_gate_validate_tool() {
    [ -x "$1" ] && [ -f "$1" ] || return 1
    [ "$(go_gate_stat "$1")" = "$2" ] || return 1
}

go_gate_validate_tools() {
    go_gate_validate_tool "$go_gate_go" "$go_gate_go_identity" \
        && go_gate_validate_tool "$go_gate_gofmt" "$go_gate_gofmt_identity" \
        && go_gate_validate_tool "$go_gate_node" "$go_gate_node_identity" \
        && go_gate_validate_tool "$go_gate_git" "$go_gate_git_identity" \
        && go_gate_validate_tool "$go_gate_xargs" "$go_gate_xargs_identity" \
        && go_gate_validate_tool "$go_gate_perl" "$go_gate_perl_identity" \
        && go_gate_validate_tool "$go_gate_corepack" "$go_gate_corepack_identity" \
        && go_gate_validate_tool "$go_gate_env" "$go_gate_env_identity" \
        && go_gate_validate_tool "$go_gate_shasum" "$go_gate_shasum_identity" \
        && [ "$(go_gate_hash "$go_gate_go")" = "$go_gate_go_hash" ] \
        && [ "$(go_gate_hash "$go_gate_gofmt")" = "$go_gate_gofmt_hash" ] \
        && [ "$(go_gate_hash "$go_gate_node")" = "$go_gate_node_hash" ] \
        && [ "$(go_gate_hash "$go_gate_corepack")" = "$go_gate_corepack_hash" ]
}

go_gate_hash() { /usr/bin/shasum -a 256 "$1" | /usr/bin/awk '{print $1}'; }

go_gate_package_manager_stage() {
    go_gate_package_manager_timeout=$1
    shift
    if [ "${go_gate_package_manager_direct-0}" -eq 1 ]; then
        go_gate_stage "$go_gate_package_manager_timeout" "$go_gate_corepack" "$@"
    else
        go_gate_stage "$go_gate_package_manager_timeout" "$go_gate_node" "$go_gate_corepack" "$@"
    fi
}

go_gate_validate_identity() {
    [ -n "${go_gate_root-}" ] && [ ! -L "$go_gate_root" ] \
        && [ -d "$go_gate_root" ] || return 1
    [ "$(CDPATH= cd -P -- "$go_gate_root" && pwd -P)" = "$go_gate_root" ] || return 1
    [ "$(go_gate_stat "$go_gate_root")" = "$go_gate_root_identity" ] || return 1
    [ "$(/usr/bin/id -u)" = "$go_gate_root_uid" ] || return 1
    [ "$(/usr/bin/stat -f '%Lp' "$go_gate_root" 2>/dev/null)" = 700 ] || return 1
}

go_gate_validate_directories() {
    for go_gate_directory in tmp cache modcache corepack npm-cache home; do
        go_gate_directory_path="$go_gate_root/$go_gate_directory"
        [ ! -L "$go_gate_directory_path" ] && [ -d "$go_gate_directory_path" ] || return 1
        [ "$(CDPATH= cd -P -- "$go_gate_directory_path" 2>/dev/null && pwd -P)" = "$go_gate_directory_path" ] || return 1
        case "$go_gate_directory" in
            tmp) go_gate_expected_identity=$go_gate_tmp_identity ;;
            cache) go_gate_expected_identity=$go_gate_cache_identity ;;
            modcache) go_gate_expected_identity=$go_gate_modcache_identity ;;
            corepack) go_gate_expected_identity=$go_gate_corepack_dir_identity ;;
            npm-cache) go_gate_expected_identity=$go_gate_npm_cache_identity ;;
            home) go_gate_expected_identity=$go_gate_home_identity ;;
            *) return 1 ;;
        esac
        [ "$(go_gate_stat "$go_gate_directory_path")" = "$go_gate_expected_identity" ] || return 1
        [ "$(/usr/bin/stat -f '%Lp' "$go_gate_directory_path" 2>/dev/null)" = 700 ] || return 1
    done
}

go_gate_before_stage() {
    go_gate_validate_identity && go_gate_validate_directories && go_gate_validate_tools || {
        echo "go gate: scratch or fixed-tool identity changed before stage" >&2
        return 1
    }
}

# Every external stage is run in this explicit whitelist environment. HOME and
# all credentials/configuration controls are absent, while tool paths remain
# fixed in the trusted parent and are not selected through PATH. SIGKILL cannot
# be trapped; a killed gate may retain its 0700 root for the external census.
go_gate_run_bounded() {
    go_gate_timeout_seconds=$1
    shift
    [ "$#" -gt 0 ] || return 64
    "$go_gate_env" -i \
        "PATH=$go_gate_node_bin_dir:/usr/bin:/bin" \
        "HOME=$go_gate_root/home" \
        "TMPDIR=$go_gate_root/tmp" "GOTMPDIR=$go_gate_root/tmp" \
        "GOCACHE=$go_gate_root/cache" "GOMODCACHE=$go_gate_root/modcache" \
        "COREPACK_HOME=$go_gate_root/corepack" \
        "npm_config_cache=$go_gate_root/npm-cache" "NPM_CONFIG_CACHE=$go_gate_root/npm-cache" \
        "GOPROXY=${GOPROXY-off}" "GOSUMDB=${GOSUMDB-off}" \
        "GOENV=off" "GOWORK=off" "GOTOOLCHAIN=${GOTOOLCHAIN-local}" \
        "GOAUTH=off" "GOFLAGS=" "GOINSECURE=" "GOVCS=*:off" \
        "GOPRIVATE=" "GONOSUMDB=" "GONOPROXY=" \
        "LC_ALL=C" "LANG=C" \
        "COREPACK_ENABLE_NETWORK=${COREPACK_ENABLE_NETWORK-0}" \
        "COREPACK_DEFAULT_TO_LATEST=0" \
        "CI=true" \
        "NODE_OPTIONS=" "NETRC=/dev/null" \
        "GIT_CONFIG_GLOBAL=/dev/null" "GIT_CONFIG_SYSTEM=/dev/null" \
        "GIT_CONFIG_NOSYSTEM=1" "GIT_TERMINAL_PROMPT=0" "GIT_PAGER=cat" \
        "npm_config_userconfig=$go_gate_root/home/npm-user-config" "NPM_CONFIG_USERCONFIG=$go_gate_root/home/npm-user-config" \
        "npm_config_globalconfig=/dev/null" "NPM_CONFIG_GLOBALCONFIG=/dev/null" \
        "npm_config_registry=https://registry.npmjs.org/" \
        "NPM_CONFIG_REGISTRY=https://registry.npmjs.org/" \
        "npm_config_offline=${npm_config_offline-false}" \
        "NPM_CONFIG_OFFLINE=${npm_config_offline-false}" \
        "$go_gate_perl" -e '
use strict;
use warnings;
use POSIX qw(setpgid WIFEXITED WEXITSTATUS WIFSIGNALED WTERMSIG);
use Time::HiRes qw(usleep);

my $seconds = shift @ARGV;
die "invalid timeout\n" unless defined $seconds && $seconds =~ /^\d+$/ && $seconds > 0;
die "missing command\n" unless @ARGV;
pipe(my $ready_r, my $ready_w) or die "pipe: $!\n";
pipe(my $go_r, my $go_w) or die "pipe: $!\n";
my $pid = fork();
die "fork failed: $!\n" unless defined $pid;
if ($pid == 0) {
    close $ready_r; close $go_w;
    if (!setpgid(0, 0)) { syswrite($ready_w, "E"); exit 125; }
    syswrite($ready_w, "R") or exit 125;
    my $ack = "";
    sysread($go_r, $ack, 1) == 1 or exit 125;
    close $ready_w; close $go_r;
    exec @ARGV;
    exit 127;
}
close $ready_w; close $go_r;
my $ready = "";
if (sysread($ready_r, $ready, 1) != 1 || $ready ne "R") {
    kill "TERM", $pid;
    waitpid($pid, 0);
    exit 125;
}
if (!setpgid($pid, $pid)) {
    kill "TERM", $pid;
    waitpid($pid, 0);
    exit 125;
}
syswrite($go_w, "G") == 1 or do { kill "TERM", -$pid; waitpid($pid, 0); exit 125; };
close $ready_r; close $go_w;
my $timed_out = 0;
my $term_sent = 0;
sub request_stop {
    $timed_out = 1;
    if (!$term_sent) { kill "TERM", -$pid; $term_sent = 1; alarm 1; }
    else { kill "KILL", -$pid; alarm 1; }
}
$SIG{ALRM} = \&request_stop;
$SIG{TERM} = \&request_stop;
$SIG{HUP} = \&request_stop;
$SIG{INT} = \&request_stop;
alarm $seconds;
waitpid($pid, 0);
alarm 0;
my $status = $?;
sub group_state {
    return "live" if kill 0, -$pid;
    return "gone" if $!{ESRCH};
    return "unknown";
}
sub stop_group {
    # After the leader is reaped, an unknown census loses numeric PGID
    # authority: the identifier may already belong to an unrelated group.
    # Observe only until ESRCH proves absence; never signal after uncertainty.
    my $state = group_state();
    if ($state eq "unknown") {
        for (1..10) {
            usleep(100_000);
            $state = group_state();
            return 1 if $state eq "gone";
            return 0 if $state eq "live";
        }
        return 0;
    }
    return 1 if $state eq "gone";
    return 0 if $state eq "unknown";
    # A positive live census is the signal-authority linearization point.
    # Signals are allowed only until a later census becomes uncertain.
    unless (kill "TERM", -$pid) {
        return 1 if $!{ESRCH};
        return 0;
    }
    for (1..10) {
        usleep(100_000);
        $state = group_state();
        return 2 if $state eq "gone";
        return 0 if $state eq "unknown";
    }
    unless (kill "KILL", -$pid) {
        return 1 if $!{ESRCH};
        return 0;
    }
    for (1..10) {
        usleep(100_000);
        $state = group_state();
        return 2 if $state eq "gone";
        return 0 if $state eq "unknown";
    }
    return 0;
}
my $group_clean = stop_group();
exit 125 if $group_clean == 0;
exit 124 if $timed_out;
exit 125 if $group_clean == 2;
exit (WIFEXITED($status) ? WEXITSTATUS($status) : 128 + WTERMSIG($status));
' "$go_gate_timeout_seconds" "$@" &
    go_gate_supervisor_pid=$!
    if wait "$go_gate_supervisor_pid"; then
        go_gate_stage_status=0
    else
        go_gate_stage_status=$?
    fi
    go_gate_supervisor_pid=
    return "$go_gate_stage_status"
}

go_gate_stage() {
    go_gate_before_stage || return 1
    go_gate_stage_status=0
    if go_gate_run_bounded "$@"; then go_gate_stage_status=0; else go_gate_stage_status=$?; fi
    go_gate_before_stage || return 1
    return "$go_gate_stage_status"
}

go_gate_environment_setup() {
    umask 077
    go_gate_owned=0
    go_gate_setup_complete=0
    go_gate_root=$(/usr/bin/mktemp -d /private/tmp/dark-factory-go.XXXXXXXX) || return 1
    go_gate_owned=1
    go_gate_root_uid=$(/usr/bin/id -u)
    go_gate_root_identity=$(go_gate_stat "$go_gate_root") || return 1
    [ "$(/usr/bin/stat -f '%Lp' "$go_gate_root" 2>/dev/null)" = 700 ] || return 1
    case "$go_gate_root" in /private/tmp/dark-factory-go.*) ;; *) return 1 ;; esac

    # Capture the caller's installed Node/Corepack pair once, before entering
    # the isolated stage environment. Canonical paths and content hashes then
    # prevent ordinary in-place replacement or PATH drift between stages.
    go_gate_node=$(/bin/realpath "$(command -v node)") || return 1
    go_gate_corepack=$(/bin/realpath "$(command -v corepack)") || return 1
    case "$go_gate_node:$go_gate_corepack" in /*:/*) ;; *) return 1 ;; esac
    go_gate_node_bin_dir=${go_gate_node%/*}
    go_gate_go=/opt/homebrew/bin/go
    go_gate_gofmt=/opt/homebrew/bin/gofmt
    go_gate_git=/usr/bin/git
    go_gate_xargs=/usr/bin/xargs
    go_gate_perl=/usr/bin/perl
    go_gate_env=/usr/bin/env
    go_gate_shasum=/usr/bin/shasum
    go_gate_go_identity=$(go_gate_stat "$go_gate_go") || return 1
    go_gate_gofmt_identity=$(go_gate_stat "$go_gate_gofmt") || return 1
    go_gate_node_identity=$(go_gate_stat "$go_gate_node") || return 1
    go_gate_git_identity=$(go_gate_stat "$go_gate_git") || return 1
    go_gate_xargs_identity=$(go_gate_stat "$go_gate_xargs") || return 1
    go_gate_perl_identity=$(go_gate_stat "$go_gate_perl") || return 1
    go_gate_corepack_identity=$(go_gate_stat "$go_gate_corepack") || return 1
    go_gate_env_identity=$(go_gate_stat "$go_gate_env") || return 1
    go_gate_shasum_identity=$(go_gate_stat "$go_gate_shasum") || return 1
    go_gate_go_hash=$(go_gate_hash "$go_gate_go") || return 1
    go_gate_gofmt_hash=$(go_gate_hash "$go_gate_gofmt") || return 1
    go_gate_node_hash=$(go_gate_hash "$go_gate_node") || return 1
    go_gate_corepack_hash=$(go_gate_hash "$go_gate_corepack") || return 1
    go_gate_validate_tools || { echo "go gate: fixed tool is unavailable" >&2; return 1; }

    for go_gate_directory in tmp cache modcache corepack npm-cache home; do
        /bin/mkdir "$go_gate_root/$go_gate_directory" || return 1
        /bin/chmod 700 "$go_gate_root/$go_gate_directory" || return 1
        case "$go_gate_directory" in
            tmp) go_gate_tmp_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            cache) go_gate_cache_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            modcache) go_gate_modcache_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            corepack) go_gate_corepack_dir_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            npm-cache) go_gate_npm_cache_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            home) go_gate_home_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
        esac
    done
    : >"$go_gate_root/home/npm-user-config"
    /bin/chmod 600 "$go_gate_root/home/npm-user-config"
    go_gate_setup_complete=1
    go_gate_validate_identity && go_gate_validate_directories || return 1
    export TMPDIR="$go_gate_root/tmp" GOTMPDIR="$go_gate_root/tmp"
    export GOCACHE="$go_gate_root/cache" GOMODCACHE="$go_gate_root/modcache"
    export COREPACK_HOME="$go_gate_root/corepack" npm_config_cache="$go_gate_root/npm-cache"
    export NPM_CONFIG_CACHE="$go_gate_root/npm-cache" GOPROXY=off GOSUMDB=off
    export GOENV=off GOWORK=off GOTOOLCHAIN=local GOAUTH=off GOFLAGS=
    export GOINSECURE= GOVCS='*:off' GOPRIVATE= GONOSUMDB= GONOPROXY=
    export LC_ALL=C LANG=C COREPACK_ENABLE_NETWORK=0
}

go_gate_environment_cleanup() {
    [ "${go_gate_owned-0}" -eq 1 ] && [ -n "${go_gate_root-}" ] \
        && [ -d "$go_gate_root" ] || return 0
    go_gate_validate_identity || {
        echo "go gate: refusing cleanup after scratch identity changed: $go_gate_root" >&2
        return 1
    }
    if [ "${go_gate_setup_complete-0}" -eq 1 ] && ! go_gate_validate_directories; then
        echo "go gate: refusing cleanup after scratch directory changed: $go_gate_root" >&2
        return 1
    fi
    go_gate_cleanup_parent=$(/usr/bin/mktemp -d /private/tmp/dark-factory-go-cleanup.XXXXXXXX) || return 1
    /bin/chmod 700 "$go_gate_cleanup_parent" || return 1
    go_gate_cleanup_target="$go_gate_cleanup_parent/root"
    if ! /bin/mv "$go_gate_root" "$go_gate_cleanup_target"; then
        /bin/rmdir "$go_gate_cleanup_parent" 2>/dev/null || true
        echo "go gate: refusing cleanup after scratch path changed" >&2
        return 1
    fi
    # The replacement can only be moved to quarantine; it is never removed
    # unless the quarantined inode is the original gate root.
    if [ ! -d "$go_gate_cleanup_target" ] || [ -L "$go_gate_cleanup_target" ] \
        || [ "$(go_gate_stat "$go_gate_cleanup_target")" != "$go_gate_root_identity" ]; then
        echo "go gate: retaining replaced scratch object: $go_gate_cleanup_target" >&2
        return 1
    fi
    # Test stages may leave read-only cache entries. This is still the exact
    # inode validated above, so restore owner permissions before removing it.
    /usr/bin/find "$go_gate_cleanup_target" -type d -exec /bin/chmod u+rwx {} + || return 1
    /usr/bin/find "$go_gate_cleanup_target" -type f -exec /bin/chmod u+rw {} + || return 1
    /bin/rm -rf -- "$go_gate_cleanup_target" || return 1
    /bin/rmdir "$go_gate_cleanup_parent" 2>/dev/null || true
}
