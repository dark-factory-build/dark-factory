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

    /usr/bin/ruby --disable-gems -e '
seconds = Integer(ARGV.shift)
abort "missing command" if ARGV.empty?
ready_r, ready_w = IO.pipe
go_r, go_w = IO.pipe
pid = fork do
  ready_r.close
  go_w.close
  begin
    Process.setpgid(0, 0)
    ready_w.write("R")
    go_r.read(1) == "G" or exit 125
    ready_w.close
    go_r.close
    exec(*ARGV)
  rescue SystemCallError
    exit 127
  end
end
ready_w.close
go_r.close
exit 125 unless ready_r.read(1) == "R"
begin
  Process.setpgid(pid, pid)
  go_w.write("G")
rescue SystemCallError
  Process.kill("TERM", pid) rescue nil
  Process.wait(pid)
  exit 125
end
ready_r.close
go_w.close
timed_out = false
term_sent = false
stop = lambda do |_signal|
  timed_out = true
  if !term_sent
    Process.kill("TERM", -pid) rescue nil
    term_sent = true
    Thread.new { sleep 1; Process.kill("KILL", -pid) rescue nil }
  else
    Process.kill("KILL", -pid) rescue nil
  end
end
Signal.trap("ALRM", &stop)
Signal.trap("TERM", &stop)
Signal.trap("HUP", &stop)
Signal.trap("INT", &stop)
timer = Thread.new { sleep seconds; Process.kill("ALRM", Process.pid) rescue nil }
_, status = Process.waitpid2(pid)
timer.kill
begin
  Process.kill(0, -pid)
  group_live = true
rescue Errno::ESRCH
  exit(timed_out ? 124 : (status.exited? ? status.exitstatus : 128 + status.termsig))
else
  Process.kill("TERM", -pid) rescue nil
  10.times do
    sleep 0.1
    begin
      Process.kill(0, -pid)
    rescue Errno::ESRCH
      exit 125 if timed_out || group_live
      exit(status.exited? ? status.exitstatus : 128 + status.termsig)
    end
  end
  Process.kill("KILL", -pid) rescue nil
  exit 125
end
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
