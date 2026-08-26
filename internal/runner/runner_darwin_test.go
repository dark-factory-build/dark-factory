//go:build darwin

package runner

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--exec-gate" {
		if err := RunExecGate(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(70)
		}
		os.Exit(0)
	}
	beforeFD := fdCensus()
	beforeG := runtime.NumGoroutine()
	code := m.Run()
	afterFD := fdCensus()
	afterG := runtime.NumGoroutine()
	if code == 0 && !sameCensus(beforeFD, afterFD) {
		fmt.Fprintf(os.Stderr, "runner FD leak before=%v after=%v\n", beforeFD, afterFD)
		code = 1
	}
	if code == 0 && afterG > beforeG {
		fmt.Fprintf(os.Stderr, "runner goroutine leak before=%d after=%d\n", beforeG, afterG)
		code = 1
	}
	os.Exit(code)
}

func fdCensus() map[int]FileIdentity {
	out := map[int]FileIdentity{}
	entries, _ := os.ReadDir("/dev/fd")
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		var s unix.Stat_t
		if unix.Fstat(fd, &s) == nil {
			out[fd] = FileIdentity{Device: uint64(s.Dev), Inode: s.Ino}
		}
	}
	return out
}
func sameCensus(a, b map[int]FileIdentity) bool {
	if len(a) != len(b) {
		return false
	}
	for fd, id := range a {
		if b[fd] != id {
			return false
		}
	}
	return true
}

type fixture struct {
	t     *testing.T
	root  string
	cwd   string
	dir   *os.File
	lease *GateLease
	child *OwnedChild
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(root, "work")
	if err := os.Mkdir(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	lease, _, err := CreateGateLease(dir, "activate")
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, root: root, cwd: cwd, dir: dir, lease: lease}
	t.Cleanup(func() {
		if f.child != nil {
			_ = f.child.Close()
		}
		_ = lease.Close()
		_ = dir.Close()
	})
	return f
}
func (f *fixture) start(target string, args []string, input []byte, stdout *os.File) *OwnedChild {
	f.t.Helper()
	spec, err := PrepareExecSpec(ExecSpec{Target: target, Args: args, Env: []string{"PATH=/usr/bin:/bin", "LANG=C"}, Cwd: f.cwd, Stdin: input, Stdout: stdout, Stderr: stdout})
	if err != nil {
		f.t.Fatal(err)
	}
	gate, err := os.Executable()
	if err != nil {
		f.t.Fatal(err)
	}
	child, err := StartBlocked(f.lease, gate, spec, false)
	if err != nil {
		var diagnostic []byte
		if stdout != nil {
			_ = stdout.Sync()
			_, _ = stdout.Seek(0, 0)
			diagnostic, _ = io.ReadAll(stdout)
		}
		f.t.Fatalf("StartBlocked: %v; gate stderr=%q", err, diagnostic)
	}
	f.child = child
	return child
}
func outputFile(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}
func waitFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("witness %s absent", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBlockedActivateExecutesOnceWithExactIdentityAndInput(t *testing.T) {
	f := newFixture(t)
	effect := filepath.Join(f.root, "effect")
	stdinCopy := filepath.Join(f.root, "stdin")
	out := outputFile(t, filepath.Join(f.root, "output"))
	child := f.start("/bin/sh", []string{"-c", fmt.Sprintf("printf x >> %q; cat > %q; printf '%%s' $$", effect, stdinCopy)}, []byte("one-input"), out)
	id := child.Identity()
	if !id.Valid() || id.PID != id.PGID {
		t.Fatalf("bad ready identity %+v", id)
	}
	if got := ObserveProcessGroup(id); got.Presence != Present {
		t.Fatalf("ready observation %+v", got)
	}
	if _, err := os.Stat(effect); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("provider effect before activation")
	}
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	if _, err := child.Activate(); !errors.Is(err, ErrState) {
		t.Fatalf("duplicate activation=%v", err)
	}
	exit, err := child.FinishAfterExit(4 * time.Second)
	if err != nil || exit.Code != 0 {
		t.Fatalf("exit=%+v err=%v", exit, err)
	}
	if got, _ := os.ReadFile(effect); string(got) != "x" {
		t.Fatalf("effect=%q", got)
	}
	if got, _ := os.ReadFile(stdinCopy); string(got) != "one-input" {
		t.Fatalf("stdin=%q", got)
	}
	_ = out.Sync()
	_, _ = out.Seek(0, 0)
	body, _ := io.ReadAll(out)
	fields := string(body)
	if fields != fmt.Sprintf("%d", id.PID) {
		t.Fatalf("target identity %q want %d", fields, id.PID)
	}
	if got := ObserveProcess(id); got.Presence != Absent {
		t.Fatalf("post-Wait=%+v", got)
	}
}

func TestLeashEOFAbortsWithoutProviderEffect(t *testing.T) {
	f := newFixture(t)
	effect := filepath.Join(f.root, "effect")
	out := outputFile(t, filepath.Join(f.root, "out"))
	child := f.start("/bin/sh", []string{"-c", "echo bad > " + effect}, nil, out)
	if err := child.Abort(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(effect); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("aborted provider ran")
	}
}

func TestFrozenExecutableRejectsReplacementAndMutation(t *testing.T) {
	for _, mutation := range []string{"replace", "bytes", "mode", "remove"} {
		t.Run(mutation, func(t *testing.T) {
			f := newFixture(t)
			target := filepath.Join(f.root, "shell")
			copyNative(t, "/bin/sh", target)
			effect := filepath.Join(f.root, "effect")
			out := outputFile(t, filepath.Join(f.root, "out"))
			child := f.start(target, []string{"-c", "echo bad > " + effect}, nil, out)
			switch mutation {
			case "replace":
				replacement := filepath.Join(f.root, "replacement")
				copyNative(t, "/bin/sh", replacement)
				if err := os.Rename(replacement, target); err != nil {
					t.Fatal(err)
				}
			case "bytes":
				file, err := os.OpenFile(target, os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = file.Write([]byte{0})
				_ = file.Close()
			case "mode":
				if err := os.Chmod(target, 0o700); err != nil {
					t.Fatal(err)
				}
			case "remove":
				if err := os.Remove(target); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := child.Activate(); err != nil {
				t.Fatal(err)
			}
			exit, err := child.FinishAfterExit(4 * time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if exit.LaunchErr == "" || exit.Code == 0 {
				t.Fatalf("mutation executed: %+v", exit)
			}
			if _, err := os.Stat(effect); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("mutated target ran")
			}
		})
	}
}

func TestResolvedInstallationSymlinkDoesNotRetarget(t *testing.T) {
	f := newFixture(t)
	link := filepath.Join(f.root, "current")
	if err := os.Symlink("/bin/sh", link); err != nil {
		t.Fatal(err)
	}
	effect := filepath.Join(f.root, "effect")
	out := outputFile(t, filepath.Join(f.root, "out"))
	child := f.start(link, []string{"-c", "echo old > " + effect}, nil, out)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/bin/echo", link); err != nil {
		t.Fatal(err)
	}
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	exit, err := child.FinishAfterExit(4 * time.Second)
	if err != nil || exit.Code != 0 {
		t.Fatalf("%+v %v", exit, err)
	}
	body, _ := os.ReadFile(effect)
	if string(body) != "old\n" {
		t.Fatalf("effect=%q", body)
	}
}

func TestTerminateOwnsTERMKillAndWait(t *testing.T) {
	f := newFixture(t)
	ready := filepath.Join(f.root, "ready")
	out := outputFile(t, filepath.Join(f.root, "out"))
	child := f.start("/bin/sh", []string{"-c", "trap '' TERM; echo ready > " + ready + "; while :; do sleep 1; done"}, nil, out)
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	waitFile(t, ready)
	exit, err := child.Terminate(100 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if exit.Signal != int(syscall.SIGKILL) {
		t.Fatalf("exit=%+v", exit)
	}
	if got := ObserveProcess(child.Identity()); got.Presence != Absent {
		t.Fatalf("cleanup=%+v", got)
	}
}

func TestLeaderExitWithDescendantRequiresOwnedCleanup(t *testing.T) {
	f := newFixture(t)
	descPath := filepath.Join(f.root, "desc")
	out := outputFile(t, filepath.Join(f.root, "out"))
	child := f.start("/bin/sh", []string{"-c", "sleep 30 & echo $! > " + descPath + "; exit 0"}, nil, out)
	if _, err := child.Activate(); err != nil {
		t.Fatal(err)
	}
	waitFile(t, descPath)
	if _, err := child.waitForExit(4 * time.Second); err != nil {
		t.Fatal(err)
	}
	obs := ObserveProcessGroup(child.Identity())
	if obs.Presence != Present || len(obs.Members) < 2 {
		t.Fatalf("leader exit mistaken for group absence: %+v", obs)
	}
	if _, err := child.Terminate(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got := unix.Kill(-child.Identity().PGID, 0); !errors.Is(got, unix.ESRCH) {
		t.Fatalf("group remains: %v", got)
	}
}

func TestObservationFailsClosed(t *testing.T) {
	bad := []Identity{{}, {PID: 1, PGID: 1, Birth: Birth{Seconds: 1}}, {PID: 2, PGID: 2, Birth: Birth{}}}
	for _, id := range bad {
		if got := ObserveProcess(id); got.Presence != Unknown {
			t.Fatalf("malformed %+v => %+v", id, got)
		}
	}
	self, err := readIdentity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	reused := self
	reused.Birth.Microseconds++
	if got := ObserveProcess(reused); got.Presence != Reused {
		t.Fatalf("reuse=%+v", got)
	}
}

func copyNative(t *testing.T, from, to string) {
	t.Helper()
	source, err := canonical(from)
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}
