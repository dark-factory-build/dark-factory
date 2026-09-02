//go:build darwin

package change

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRepositoryIdentityUsesExactStoreIntegerBounds(t *testing.T) {
	for _, invalid := range [][2]uint64{{0, 0}, {1 << 63, 1}, {0, 1 << 63}} {
		if _, err := NewRepositoryIdentity(invalid[0], invalid[1]); err == nil {
			t.Fatalf("invalid Store identity (%d,%d) accepted", invalid[0], invalid[1])
		}
	}
	for _, valid := range [][2]uint64{{0, 1}, {1<<63 - 1, 1<<63 - 1}} {
		identity, err := NewRepositoryIdentity(valid[0], valid[1])
		if err != nil || identity.Device() != valid[0] || identity.Inode() != valid[1] {
			t.Fatalf("valid Store identity (%d,%d) reconstructed as %+v: %v", valid[0], valid[1], identity, err)
		}
	}
}

func TestSelectGitUsesSealedMetadataOnlyBoundary(t *testing.T) {
	repository := fakeRepository(t)
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{0x11}, format.OIDLength()))
	blob := mustEntry(t, format, []byte("file"), "100644", []byte("secret"))
	logPath := filepath.Join(filepath.Dir(repository), "commands")
	environmentPath := filepath.Join(filepath.Dir(repository), "environment")
	blobWitness := filepath.Join(filepath.Dir(repository), "blob-read")
	os.Setenv("DARK_FACTORY_AMBIENT_SENTINEL", "must-not-cross")
	t.Cleanup(func() { os.Unsetenv("DARK_FACTORY_AMBIENT_SENTINEL") })
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$*" in
  *" config "*) /usr/bin/env > %q; exit 1 ;;
  *" rev-parse "*) printf '%%s\nsha1\n%%s\n' %q %q ;;
  *" ls-tree "*) printf '100644 blob %%s       6\tfile\0' %q ;;
  *" cat-file "*) : > %q; exit 91 ;;
  *) exit 92 ;;
esac
`, logPath, environmentPath, repository, base.Hex(), blob.oid.Hex(), blobWitness)
	git := writeFakeGit(t, script)
	selected, err := selectGit(context.Background(), git, repository, "refs/heads/main", mustRepositoryIdentity(t, repository), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !selected.Base().equal(base) || !manifestsEqual(selected.Manifest(), mustManifest(t, format, base, []Entry{blob})) {
		t.Fatal("fake metadata selection did not produce the exact manifest")
	}
	if _, err := os.Lstat(blobWitness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("selection crossed the blob boundary: %v", err)
	}
	commands := mustReadFile(t, logPath)
	if bytes.Contains(commands, []byte("cat-file")) || !bytes.Contains(commands, []byte("rev-parse")) || !bytes.Contains(commands, []byte("ls-tree")) {
		t.Fatalf("selection commands=%q", commands)
	}
	environment := mustReadFile(t, environmentPath)
	for _, required := range []string{
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0", "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1",
		"GIT_PROTOCOL_FROM_USER=0", "GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
	} {
		if !bytes.Contains(environment, []byte(required+"\n")) {
			t.Fatalf("sealed environment lacks %q: %q", required, environment)
		}
	}
	if bytes.Contains(environment, []byte("DARK_FACTORY_AMBIENT_SENTINEL")) {
		t.Fatal("ambient environment crossed the Git boundary")
	}
}

func TestGitBlobsValidatesExactOrderAndFramingWithoutLeakingOutput(t *testing.T) {
	cases := map[string]func(string) string{
		"wrong oid":       func(oid string) string { return strings.Repeat("0", len(oid)) + " blob 6\\nsecret\\n" },
		"wrong type":      func(oid string) string { return oid + " tree 6\\nsecret\\n" },
		"wrong size":      func(oid string) string { return oid + " blob 5\\nshort\\n" },
		"wrong blob hash": func(oid string) string { return oid + " blob 6\\nwrong!\\n" },
		"oversize":        func(oid string) string { return fmt.Sprintf("%s blob %d\\n", oid, MaxBlobBytes+1) },
		"truncated":       func(oid string) string { return oid + " blob 6\\nsec" },
		"delimiter":       func(oid string) string { return oid + " blob 6\\nsecretX" },
		"missing":         func(oid string) string { return oid + " missing\\n" },
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			repository := fakeRepository(t)
			selection, entry := fakeSelection(t, repository, "", []byte("secret"))
			responseText := response(entry.oid.Hex())
			script := fmt.Sprintf("#!/bin/sh\nIFS= read -r request || exit 2\nprintf '%%b' %q\n", responseText)
			git := writeFakeGit(t, script)
			selection.gitExecutable = git
			selection.gitIdentity = mustGitFileIdentity(t, git)
			blobs, err := openGitBlobs(context.Background(), git, repository, selection, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = blobs.Read(context.Background(), entry.oid)
			if err == nil {
				t.Fatal("malformed batch response accepted")
			}
			var protocol *GitError
			if !errors.As(err, &protocol) {
				t.Fatalf("error=%T %v, want GitError", err, err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("private output leaked: %v", err)
			}
		})
	}

	repository := fakeRepository(t)
	selection, first := fakeSelection(t, repository, "a", []byte("first"))
	format := selection.format
	second := mustEntry(t, format, []byte("b"), "100644", []byte("second"))
	selection.manifest = mustManifest(t, format, selection.base, []Entry{first, second})
	logPath := filepath.Join(filepath.Dir(repository), "requests")
	script := fmt.Sprintf(`#!/bin/sh
while IFS= read -r request; do
  printf '%%s\n' "$request" >> %q
  case "$request" in
    %s) printf '%%s blob 5\nfirst\n' "$request" ;;
    %s) printf '%%s blob 6\nsecond\n' "$request" ;;
    *) exit 3 ;;
  esac
done
`, logPath, first.oid.Hex(), second.oid.Hex())
	git := writeFakeGit(t, script)
	selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
	blobs, err := openGitBlobs(context.Background(), git, repository, selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.Read(context.Background(), second.oid); err == nil {
		t.Fatal("out-of-order object request accepted")
	}
	if got := mustReadFile(t, logPath); len(got) != 0 {
		t.Fatalf("out-of-order request reached Git: %q", got)
	}
}

func TestGitBlobsCloseRequiresExactProtocolEOF(t *testing.T) {
	repository := fakeRepository(t)
	selection, entry := fakeSelection(t, repository, "", []byte("secret"))
	script := `#!/bin/sh
IFS= read -r request || exit 2
printf '%s blob 6\nsecret\nTRAILING-PRIVATE-BYTES' "$request"
while IFS= read -r ignored; do :; done
`
	git := writeFakeGit(t, script)
	selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
	blobs, err := openGitBlobs(context.Background(), git, repository, selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := blobs.Read(context.Background(), entry.oid)
	if err != nil || string(data) != "secret" {
		t.Fatalf("read=%q err=%v", data, err)
	}
	err = blobs.Close()
	if err == nil || strings.Contains(err.Error(), "TRAILING-PRIVATE-BYTES") {
		t.Fatalf("trailing protocol output was accepted or leaked: %v", err)
	}
}

func TestGitBlobsBoundsAndRedactsStderrAndRejectsIncompleteClose(t *testing.T) {
	const sentinel = "PRIVATE-BLOB-TOKEN"
	repository := fakeRepository(t)
	selection, entry := fakeSelection(t, repository, "", []byte("secret"))
	script := fmt.Sprintf("#!/bin/sh\nIFS= read -r request || exit 2\nexec /usr/bin/perl -e 'print STDERR q(%s) x %d; while(1){}'\n", sentinel, maxGitStderrBytes)
	git := writeFakeGit(t, script)
	selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
	recorder := &gitEventRecorder{}
	blobs, err := openGitBlobs(context.Background(), git, repository, selection, recorder.record)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = blobs.Read(ctx, entry.oid)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stderr bound did not terminate the child: %v", err)
	}
	if strings.Contains(err.Error(), sentinel) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("private stderr/blob leaked: %v", err)
	}
	recorder.require(t, 1, 1, 0, 1)

	repository = fakeRepository(t)
	selection, _ = fakeSelection(t, repository, "", []byte("secret"))
	git = writeFakeGit(t, "#!/bin/sh\nwhile IFS= read -r request; do exit 4; done\n")
	selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
	blobs, err = openGitBlobs(context.Background(), git, repository, selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := blobs.Close(); err == nil {
		t.Fatal("incomplete blob source closed successfully")
	}
}

func TestGitBlobsReadsOnlySelectedObjectsInOrderAndWaitsOnce(t *testing.T) {
	repository := fakeRepository(t)
	selection, first := fakeSelection(t, repository, "a", []byte("first"))
	second := mustEntry(t, selection.format, []byte("b"), "100644", []byte("second"))
	selection.manifest = mustManifest(t, selection.format, selection.base, []Entry{first, second})
	logPath := filepath.Join(filepath.Dir(repository), "requests")
	pidPath := filepath.Join(filepath.Dir(repository), "pid")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s' "$$" > %q
while IFS= read -r request; do
  printf '%%s\n' "$request" >> %q
  case "$request" in
    %s) printf '%%s blob 5\nfirst\n' "$request" ;;
    %s) printf '%%s blob 6\nsecond\n' "$request" ;;
    *) exit 3 ;;
  esac
done
`, pidPath, logPath, first.oid.Hex(), second.oid.Hex())
	git := writeFakeGit(t, script)
	selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
	recorder := &gitEventRecorder{}
	blobs, err := openGitBlobs(context.Background(), git, repository, selection, recorder.record)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []struct {
		entry Entry
		data  string
	}{{first, "first"}, {second, "second"}} {
		data, err := blobs.Read(context.Background(), expected.entry.oid)
		if err != nil || string(data) != expected.data {
			t.Fatalf("read=%q err=%v", data, err)
		}
	}
	if err := blobs.Close(); err != nil {
		t.Fatal(err)
	}
	if got, want := string(mustReadFile(t, logPath)), first.oid.Hex()+"\n"+second.oid.Hex()+"\n"; got != want {
		t.Fatalf("requests=%q want=%q", got, want)
	}
	recorder.require(t, 1, 0, 0, 1)
	pid, err := strconv.Atoi(string(mustReadFile(t, pidPath)))
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("exact Git child %d remains observable after Close: %v", pid, err)
	}
}

func TestGitChildCancellationKillsAndWaitsExactlyOnce(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		repository := fakeRepository(t)
		ready := filepath.Join(filepath.Dir(repository), "ready")
		blocking := fmt.Sprintf("#!/bin/sh\nexec /usr/bin/perl -e '$SIG{TERM}=q(IGNORE); open(F,q(>%s)); print F q(x); close(F); while(1){}'\n", ready)
		git := writeFakeGit(t, blocking)
		recorder := &gitEventRecorder{}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := selectGit(ctx, git, repository, "HEAD", mustRepositoryIdentity(t, repository), recorder.record)
			result <- err
		}()
		waitForFile(t, ready)
		cancel()
		err := <-result
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
		recorder.require(t, 1, 1, 1, 1)
		recorder.requireOrder(t, gitProcessStarted, gitProcessTermed, gitProcessKilled, gitProcessWaited)
	})
	t.Run("blob", func(t *testing.T) {
		repository := fakeRepository(t)
		selection, entry := fakeSelection(t, repository, "", []byte("secret"))
		ready := filepath.Join(filepath.Dir(repository), "ready")
		script := fmt.Sprintf("#!/bin/sh\nIFS= read -r request || exit 2\nexec /usr/bin/perl -e '$SIG{TERM}=q(IGNORE); open(F,q(>%s)); print F q(x); close(F); while(1){}'\n", ready)
		git := writeFakeGit(t, script)
		selection.gitExecutable, selection.gitIdentity = git, mustGitFileIdentity(t, git)
		recorder := &gitEventRecorder{}
		blobs, err := openGitBlobs(context.Background(), git, repository, selection, recorder.record)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := blobs.Read(ctx, entry.oid)
			result <- err
		}()
		waitForFile(t, ready)
		cancel()
		err = <-result
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
		recorder.require(t, 1, 1, 1, 1)
		recorder.requireOrder(t, gitProcessStarted, gitProcessTermed, gitProcessKilled, gitProcessWaited)
	})
	t.Run("before start", func(t *testing.T) {
		repository := fakeRepository(t)
		git := writeFakeGit(t, "#!/bin/sh\nexit 90\n")
		recorder := &gitEventRecorder{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := selectGit(ctx, git, repository, "HEAD", mustRepositoryIdentity(t, repository), recorder.record)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
		recorder.require(t, 0, 0, 0, 0)
	})
}

func TestGitSignalsOnlyWhileExactLeaderIsUnreaped(t *testing.T) {
	repository := fakeRepository(t)
	ready := filepath.Join(filepath.Dir(repository), "unreaped-ready")
	git := writeFakeGit(t, fmt.Sprintf("#!/bin/sh\nexec /usr/bin/perl -e '$SIG{TERM}=q(IGNORE); open(F,q(>%s)); print F q(x); close(F); while(1){}'\n", ready))
	var child *gitChild
	var signalChecks, waitedChecks int
	hook := func(event gitProcessEvent) {
		switch event {
		case gitProcessTermed, gitProcessKilled:
			signalChecks++
			if child == nil {
				t.Fatal("signal hook ran before child ownership was returned")
			}
			// The hook runs after the signal syscall, and a child killed by
			// that very signal leaves its process group the moment it exits —
			// so a pgid probe races the kernel. The invariant is narrower:
			// the pid must not have been REAPED yet, and an unreaped pid
			// (live or zombie) answers signal 0 until Wait releases it.
			// Any error, not only ESRCH: a pid reaped and recycled to a
			// foreign owner answers EPERM, which must fail the witness too.
			if err := unix.Kill(child.pid, 0); err != nil {
				t.Fatalf("signal %s targeted a reaped/reusable pid %d: %v", event, child.pid, err)
			}
		case gitProcessWaited:
			waitedChecks++
			if err := unix.Kill(child.pid, 0); !errors.Is(err, unix.ESRCH) {
				t.Fatalf("Wait hook ran before exact pid %d disappeared: %v", child.pid, err)
			}
		}
	}
	var err error
	child, err = startGitChild(gitCommandSpec{program: git, repository: repository, home: filepath.Dir(repository), hook: hook}, false)
	if err != nil {
		t.Fatal(err)
	}
	waitForFile(t, ready)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reaped := child.reap(ctx, false)
	_ = closeGitFiles(child.stdout, child.stderr)
	if !errors.Is(reaped.contextError, context.Canceled) || !reaped.cleanup || signalChecks != 2 || waitedChecks != 1 {
		t.Fatalf("reap=%+v signalChecks=%d waitedChecks=%d", reaped, signalChecks, waitedChecks)
	}
}

func TestRegisteredWrapperGroupOwnsGitDescendantsUntilOuterCleanup(t *testing.T) {
	if os.Getenv("DARK_FACTORY_GIT_WRAPPER_HELPER") == "1" {
		repository := os.Getenv("DARK_FACTORY_GIT_WRAPPER_REPOSITORY")
		git := os.Getenv("DARK_FACTORY_GIT_WRAPPER_EXECUTABLE")
		resultPath := os.Getenv("DARK_FACTORY_GIT_WRAPPER_RESULT")
		readyPath := os.Getenv("DARK_FACTORY_GIT_WRAPPER_READY")
		home := os.Getenv("DARK_FACTORY_GIT_WRAPPER_HOME")
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() {
			_, err := runGitCapture(ctx, gitCommandSpec{program: git, repository: repository, home: home}, 1)
			result <- err
		}()
		waitForFile(t, readyPath)
		cancel()
		err := <-result
		var gitErr *GitError
		if !errors.As(err, &gitErr) || !gitErr.RequiresGroupCleanup() || !errors.Is(err, context.Canceled) {
			t.Fatalf("wrapper helper error=%T %v", err, err)
		}
		if err := os.WriteFile(resultPath, []byte("cleanup-required"), 0o600); err != nil {
			t.Fatal(err)
		}
		select {}
	}

	root := secureTempDir(t)
	repository := fakeRepository(t)
	readyPath := filepath.Join(root, "ready")
	leaderPath := filepath.Join(root, "leader")
	descendantPath := filepath.Join(root, "descendant")
	resultPath := filepath.Join(root, "result")
	home := filepath.Join(root, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/usr/bin/perl
use POSIX ();
$SIG{TERM} = 'IGNORE';
my $child = fork();
die "fork" unless defined $child;
if ($child == 0) {
  $SIG{TERM} = 'IGNORE';
  open(my $d, '>', %q) or die "descendant";
  print $d "$$ ", POSIX::getpgrp();
  close($d);
  while (1) { sleep 1; }
}
open(my $l, '>', %q) or die "leader";
print $l "$$ ", POSIX::getpgrp();
close($l);
open(my $r, '>', %q) or die "ready";
print $r "ready";
close($r);
while (1) { sleep 1; }
`, descendantPath, leaderPath, readyPath)
	git := writeFakeGit(t, script)
	command := exec.Command(os.Args[0], "-test.run=^TestRegisteredWrapperGroupOwnsGitDescendantsUntilOuterCleanup$")
	command.Env = append(os.Environ(),
		"DARK_FACTORY_GIT_WRAPPER_HELPER=1",
		"DARK_FACTORY_GIT_WRAPPER_REPOSITORY="+repository,
		"DARK_FACTORY_GIT_WRAPPER_EXECUTABLE="+git,
		"DARK_FACTORY_GIT_WRAPPER_RESULT="+resultPath,
		"DARK_FACTORY_GIT_WRAPPER_READY="+readyPath,
		"DARK_FACTORY_GIT_WRAPPER_HOME="+home,
	)
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	wrapperPID := command.Process.Pid
	wrapperCleaned := false
	t.Cleanup(func() {
		if !wrapperCleaned {
			_ = unix.Kill(-wrapperPID, unix.SIGKILL)
			_ = command.Wait()
		}
	})
	waitForFileUntil(t, resultPath, 5*time.Second)
	leaderPID, leaderPGID := readPIDAndPGID(t, leaderPath)
	descendantPID, descendantPGID := readPIDAndPGID(t, descendantPath)
	if leaderPGID != wrapperPID || descendantPGID != wrapperPID {
		t.Fatalf("registered wrapper pgid=%d leader=(%d,%d) descendant=(%d,%d)", wrapperPID, leaderPID, leaderPGID, descendantPID, descendantPGID)
	}
	if err := unix.Kill(leaderPID, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("direct Git leader was not synchronously reaped: %v", err)
	}
	if err := unix.Kill(descendantPID, 0); err != nil {
		t.Fatalf("descendant escaped before registered wrapper cleanup: %v", err)
	}
	providerWitness := filepath.Join(root, "provider-started")
	if _, err := os.Lstat(providerWitness); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("provider witness existed before registered group cleanup: %v", err)
	}
	if err := unix.Kill(-wrapperPID, unix.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("wrapper did not die by registered group cleanup")
	}
	wrapperCleaned = true
	waitForPIDGone(t, descendantPID)
	if err := os.WriteFile(providerWitness, []byte("provider may start"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGitChildInheritsCurrentRegisteredWrapperGroup(t *testing.T) {
	repository := fakeRepository(t)
	git := writeFakeGit(t, "#!/usr/bin/perl\n$SIG{TERM}=sub{exit 0}; while(1){select(undef,undef,undef,1)}\n")
	child, err := startGitChild(gitCommandSpec{program: git, repository: repository, home: filepath.Dir(repository)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := mustProcessGroup(t, child.pid); got != unix.Getpgrp() {
		t.Fatalf("Git child pgid=%d current registered wrapper pgid=%d", got, unix.Getpgrp())
	}
	reaped := child.reap(context.Background(), true)
	_ = closeGitFiles(child.stdout, child.stderr)
	if !reaped.cleanup || reaped.observerErr != nil {
		t.Fatalf("reap=%+v", reaped)
	}
}

func mustProcessGroup(t testing.TB, pid int) int {
	t.Helper()
	group, err := unix.Getpgid(pid)
	if err != nil {
		t.Fatal(err)
	}
	return group
}

func readPIDAndPGID(t testing.TB, path string) (int, int) {
	t.Helper()
	fields := strings.Fields(string(mustReadFile(t, path)))
	if len(fields) != 2 {
		t.Fatalf("invalid process witness %q", fields)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		t.Fatal(err)
	}
	pgid, err := strconv.Atoi(fields[1])
	if err != nil {
		t.Fatal(err)
	}
	return pid, pgid
}

func waitForPIDGone(t testing.TB, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := unix.Kill(pid, 0); errors.Is(err, unix.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process %d remains after registered group cleanup", pid)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForFile(t testing.TB, path string) {
	t.Helper()
	waitForFileUntil(t, path, 2*time.Second)
}

func waitForFileUntil(t testing.TB, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Lstat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for child witness %s", path)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSelectedLooseObjectRemovalFailsWithoutFallback(t *testing.T) {
	fixture := newLocalGitFixture(t, "sha1")
	selected, err := SelectGit(context.Background(), fixture.git, fixture.repository, "HEAD", fixture.identity)
	if err != nil {
		t.Fatal(err)
	}
	first := selected.manifest.entries[0]
	objectPath := filepath.Join(fixture.repository, ".git", "objects", first.oid.Hex()[:2], first.oid.Hex()[2:])
	quarantine := objectPath + ".quarantine"
	if err := os.Rename(objectPath, quarantine); err != nil {
		t.Fatalf("quarantine selected loose object: %v", err)
	}
	blobs, err := OpenGitBlobs(context.Background(), fixture.git, fixture.repository, selected)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blobs.Read(context.Background(), first.oid); err == nil {
		t.Fatal("missing selected object was read after selection")
	}
}

func TestGitStartFailureLeavesNoOwnedProcessDescriptorOrHome(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	repository := fakeRepository(t)
	git := filepath.Join(secureTempDir(t), "git")
	if err := os.WriteFile(git, []byte("not an executable image"), 0o700); err != nil {
		t.Fatal(err)
	}
	beforeFDs := descriptorCount(t)
	homesBefore, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "dark-factory-git-home-*"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &gitEventRecorder{}
	_, err = selectGit(context.Background(), git, repository, "HEAD", mustRepositoryIdentity(t, repository), recorder.record)
	if err == nil {
		t.Fatal("invalid executable image started")
	}
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("error=%T %v, want ValidationError", err, err)
	}
	recorder.require(t, 0, 0, 0, 0)
	if after := descriptorCount(t); after != beforeFDs {
		t.Fatalf("start failure leaked descriptors: before=%d after=%d", beforeFDs, after)
	}
	homesAfter, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "dark-factory-git-home-*"))
	if err != nil || len(homesAfter) != len(homesBefore) {
		t.Fatalf("start failure HOME census before=%v after=%v err=%v", homesBefore, homesAfter, err)
	}
}

type gitEventRecorder struct {
	mu     sync.Mutex
	events []gitProcessEvent
}

func (r *gitEventRecorder) record(event gitProcessEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *gitEventRecorder) require(t testing.TB, started, termed, killed, waited int) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	counts := make(map[gitProcessEvent]int)
	for _, event := range r.events {
		counts[event]++
	}
	if counts[gitProcessStarted] != started || counts[gitProcessTermed] != termed || counts[gitProcessKilled] != killed || counts[gitProcessWaited] != waited {
		t.Fatalf("process events=%v", r.events)
	}
}

func (r *gitEventRecorder) requireOrder(t testing.TB, want ...gitProcessEvent) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) != len(want) {
		t.Fatalf("process events=%v want=%v", r.events, want)
	}
	for index := range want {
		if r.events[index] != want[index] {
			t.Fatalf("process events=%v want=%v", r.events, want)
		}
	}
}

func fakeRepository(t testing.TB) string {
	t.Helper()
	repository := filepath.Join(secureTempDir(t), "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, ".git", "config"), []byte("[core]\n\trepositoryformatversion = 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repository, ".git", "objects"), 0o700); err != nil {
		t.Fatal(err)
	}
	return repository
}

func fakeSelection(t testing.TB, repository, path string, data []byte) (Selection, Entry) {
	t.Helper()
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{0x22}, format.OIDLength()))
	if path == "" {
		path = "file"
	}
	entry := mustEntry(t, format, []byte(path), "100644", data)
	repositoryIdentity := mustRepositoryIdentity(t, repository)
	repositoryCheckpoint, err := checkpointRepository(repository, repositoryIdentity)
	if err != nil {
		t.Fatal(err)
	}
	return Selection{
		repositoryRoot: repository, repository: repositoryCheckpoint,
		format: format, base: base, manifest: mustManifest(t, format, base, []Entry{entry}),
	}, entry
}

func writeFakeGit(t testing.TB, contents string) string {
	t.Helper()
	path := filepath.Join(secureTempDir(t), "git")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		t.Fatal(err)
	}
	if err := target.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".plan", []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustGitFileIdentity(t testing.TB, path string) gitFileIdentity {
	t.Helper()
	identity, err := checkpointGitExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func mustReadFile(t testing.TB, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	return data
}

func TestUnreapedZombieAnswersSignalZeroProbe(t *testing.T) {
	// Pins the Darwin semantics the unreaped-signal hook above relies on:
	// an exited, unreaped child still occupies its pid (signal 0 succeeds)
	// even though it left its process group at exit — so a pgid probe is
	// the wrong witness for "unreaped" while signal 0 is exact until Wait.
	command := exec.Command("/bin/sh", "-c", "exit 0")
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := command.Process.Pid
	deadline := time.Now().Add(5 * time.Second)
	for {
		proc, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
		if err == nil && proc.Proc.P_stat == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child never became an unreaped zombie")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := unix.Kill(pid, 0); err != nil {
		t.Fatalf("unreaped zombie refused signal 0: %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
		t.Fatalf("reaped pid still answered signal 0: %v", err)
	}
}
