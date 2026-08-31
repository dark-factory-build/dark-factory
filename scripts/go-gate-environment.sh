#!/bin/sh

# Process-sensitive stages run in a dedicated process group. A stage passes
# only after that group is absent, so a test cannot hide a leaked descendant
# behind a successful parent exit. Ordinary source checks do not use this.
go_gate_run_bounded() {
    go_gate_timeout_seconds=$1
    shift
    [ "$#" -gt 0 ] || return 64
    case "$go_gate_timeout_seconds" in
        ''|*[!0-9]*|0) return 64 ;;
    esac

    /usr/bin/perl -e '
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
exit 125 if $group_clean == 0 || $group_clean == 2;
exit 124 if $timed_out;
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
    go_gate_run_bounded "$@"
}

# An interrupted caller still owns the bounded supervisor as a direct child.
# Join it before cleanup so the supervisor can tear down the stage process
# group, including descendants that ignore TERM.
go_gate_join_supervisor() {
    [ -n "${go_gate_supervisor_pid-}" ] || return 0
    /bin/kill -TERM "$go_gate_supervisor_pid" 2>/dev/null || true
    wait "$go_gate_supervisor_pid" 2>/dev/null || true
    go_gate_supervisor_pid=
}
