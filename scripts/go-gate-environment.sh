#!/bin/sh

# Shared by the Go gates. Entry points always create the root themselves;
# there is intentionally no caller-selected root or provenance nonce.

go_gate_stat() { /usr/bin/stat -f '%d:%i:%u:%Lp' "$1" 2>/dev/null; }

go_gate_validate_tool() {
    [ -x "$1" ] && [ -f "$1" ] || return 1
    [ "$(go_gate_stat "$1")" = "$2" ] || return 1
}

go_gate_validate_tools() {
    go_gate_validate_tool "$go_gate_go" "$go_gate_go_identity" \
        && go_gate_validate_tool "$go_gate_gofmt" "$go_gate_gofmt_identity" \
        && go_gate_validate_tool "$go_gate_git" "$go_gate_git_identity" \
        && go_gate_validate_tool "$go_gate_xargs" "$go_gate_xargs_identity" \
        && go_gate_validate_tool "$go_gate_perl" "$go_gate_perl_identity" \
        && go_gate_validate_tool "$go_gate_corepack" "$go_gate_corepack_identity" \
        && go_gate_validate_tool "$go_gate_env" "$go_gate_env_identity"
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
    for go_gate_directory in tmp cache modcache corepack npm-cache; do
        go_gate_directory_path="$go_gate_root/$go_gate_directory"
        [ ! -L "$go_gate_directory_path" ] && [ -d "$go_gate_directory_path" ] || return 1
        [ "$(CDPATH= cd -P -- "$go_gate_directory_path" 2>/dev/null && pwd -P)" = "$go_gate_directory_path" ] || return 1
        case "$go_gate_directory" in
            tmp) go_gate_expected_identity=$go_gate_tmp_identity ;;
            cache) go_gate_expected_identity=$go_gate_cache_identity ;;
            modcache) go_gate_expected_identity=$go_gate_modcache_identity ;;
            corepack) go_gate_expected_identity=$go_gate_corepack_dir_identity ;;
            npm-cache) go_gate_expected_identity=$go_gate_npm_cache_identity ;;
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
# fixed in the trusted parent and are not selected through PATH.
go_gate_run_bounded() {
    go_gate_timeout_seconds=$1
    shift
    [ "$#" -gt 0 ] || return 64
    "$go_gate_env" -i \
        "PATH=/opt/homebrew/bin:/usr/bin:/bin" \
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
        "COREPACK_DEFAULT_TO_LATEST=0" "COREPACK_INTEGRITY_KEYS=0" \
        "NODE_OPTIONS=" "NETRC=/dev/null" \
        "GIT_CONFIG_GLOBAL=/dev/null" "GIT_CONFIG_SYSTEM=/dev/null" \
        "GIT_CONFIG_NOSYSTEM=1" "GIT_TERMINAL_PROMPT=0" "GIT_PAGER=cat" \
        "npm_config_userconfig=/dev/null" "NPM_CONFIG_USERCONFIG=/dev/null" \
        "npm_config_globalconfig=/dev/null" "NPM_CONFIG_GLOBALCONFIG=/dev/null" \
        "npm_config_registry=https://registry.npmjs.org/" \
        "NPM_CONFIG_REGISTRY=https://registry.npmjs.org/" \
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
$SIG{ALRM} = sub {
    $timed_out = 1;
    if (!$term_sent) { kill "TERM", -$pid; $term_sent = 1; alarm 1; }
    else { kill "KILL", -$pid; alarm 1; }
};
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
    my $state = group_state();
    return 1 if $state eq "gone";
    return 0 if $state eq "unknown";
    return 0 unless kill "TERM", -$pid;
    for (1..10) { usleep(100_000); return 2 if group_state() eq "gone"; }
    return 0 unless kill "KILL", -$pid;
    for (1..10) { usleep(100_000); return 2 if group_state() eq "gone"; }
    return 0;
}
my $group_clean = stop_group();
exit 125 if $group_clean == 0 || $group_clean == 2;
exit 124 if $timed_out;
exit (WIFEXITED($status) ? WEXITSTATUS($status) : 128 + WTERMSIG($status));
' "$go_gate_timeout_seconds" "$@"
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

    # Fixed trusted locations; no PATH-selected fake can become a gate tool.
    go_gate_go=/private/tmp/dark-factory-go1.27.0/bin/go
    go_gate_gofmt=/private/tmp/dark-factory-go1.27.0/bin/gofmt
    go_gate_git=/usr/bin/git
    go_gate_xargs=/usr/bin/xargs
    go_gate_perl=/usr/bin/perl
    go_gate_corepack=/opt/homebrew/bin/corepack
    go_gate_env=/usr/bin/env
    go_gate_go_identity=$(go_gate_stat "$go_gate_go") || return 1
    go_gate_gofmt_identity=$(go_gate_stat "$go_gate_gofmt") || return 1
    go_gate_git_identity=$(go_gate_stat "$go_gate_git") || return 1
    go_gate_xargs_identity=$(go_gate_stat "$go_gate_xargs") || return 1
    go_gate_perl_identity=$(go_gate_stat "$go_gate_perl") || return 1
    go_gate_corepack_identity=$(go_gate_stat "$go_gate_corepack") || return 1
    go_gate_env_identity=$(go_gate_stat "$go_gate_env") || return 1
    go_gate_validate_tools || { echo "go gate: fixed tool is unavailable" >&2; return 1; }

    for go_gate_directory in tmp cache modcache corepack npm-cache; do
        /bin/mkdir "$go_gate_root/$go_gate_directory" || return 1
        /bin/chmod 700 "$go_gate_root/$go_gate_directory" || return 1
        case "$go_gate_directory" in
            tmp) go_gate_tmp_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            cache) go_gate_cache_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            modcache) go_gate_modcache_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            corepack) go_gate_corepack_dir_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
            npm-cache) go_gate_npm_cache_identity=$(go_gate_stat "$go_gate_root/$go_gate_directory") ;;
        esac
    done
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
    /bin/rm -rf -- "$go_gate_cleanup_target" || return 1
    /bin/rmdir "$go_gate_cleanup_parent" 2>/dev/null || true
}
