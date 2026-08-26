//go:build darwin

package change

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	maxGitSelectionOutput = 4 << 10
	maxGitTreeOutput      = int(MaxEntryCount+1) * (MaxRelativePathBytes + 128)
	maxGitBlobHeader      = 256
	maxGitStderrBytes     = 64 << 10
	maxGitConfigBytes     = 1 << 20
	maxGitExecutableBytes = 64 << 20
	gitTerminateGrace     = 250 * time.Millisecond
	gitPipeDrainGrace     = time.Second
)

type gitCommandSpec struct {
	program    string
	repository string
	home       string
	arguments  []string
	operation  string
	hook       gitProcessHook
}

type gitCapture struct {
	output   []byte
	exitCode int
}

type gitStreamResult struct {
	data     []byte
	overflow bool
	err      error
}

// SelectGit resolves revision once, validates the complete recursive tree
// metadata, and returns without reading any blob contents.
func SelectGit(ctx context.Context, gitExecutable, repositoryRoot, revision string, expected RepositoryIdentity) (Selection, error) {
	return selectGit(ctx, gitExecutable, repositoryRoot, revision, expected, nil)
}

func selectGit(ctx context.Context, gitExecutable, repositoryRoot, revision string, expected RepositoryIdentity, hook gitProcessHook) (Selection, error) {
	if err := validateRevision(revision); err != nil {
		return Selection{}, err
	}
	repository, err := checkpointRepository(repositoryRoot, expected)
	if err != nil {
		return Selection{}, err
	}
	gitIdentity, err := checkpointGitExecutable(gitExecutable)
	if err != nil {
		return Selection{}, err
	}
	home, err := newGitHome()
	if err != nil {
		return Selection{}, err
	}
	defer cleanupGitHome(home)
	spec := gitCommandSpec{program: gitExecutable, repository: repositoryRoot, home: home, hook: hook}

	spec.operation = "partial-clone policy check"
	spec.arguments = []string{"-C", repositoryRoot, "config", "--local", "--null", "--get-regexp", `^(extensions\.partialclone|remote\..*\.promisor)$`}
	partial, err := runGitCapture(ctx, spec, maxGitSelectionOutput)
	if err != nil {
		return Selection{}, err
	}
	if err := verifyGitAuthority(repositoryRoot, repository, gitExecutable, gitIdentity); err != nil {
		return Selection{}, err
	}
	if !(partial.exitCode == 1 && len(partial.output) == 0) {
		if partial.exitCode == 0 || len(partial.output) != 0 {
			return Selection{}, &ValidationError{Reason: "partial-clone repositories are forbidden"}
		}
		return Selection{}, newGitError(gitFailureProcess)
	}

	spec.operation = "commit selection"
	spec.arguments = []string{"-C", repositoryRoot, "rev-parse", "--show-toplevel", "--show-object-format", "--verify", "--end-of-options", revision + "^{commit}"}
	resolved, err := runGitCapture(ctx, spec, maxGitSelectionOutput)
	if err != nil {
		return Selection{}, err
	}
	if resolved.exitCode != 0 {
		return Selection{}, newGitError(gitFailureProcess)
	}
	if err := verifyGitAuthority(repositoryRoot, repository, gitExecutable, gitIdentity); err != nil {
		return Selection{}, err
	}
	format, base, err := parseSelectionOutput(repositoryRoot, resolved.output)
	if err != nil {
		return Selection{}, err
	}

	spec.operation = "tree selection"
	spec.arguments = []string{"-C", repositoryRoot, "ls-tree", "-rz", "-l", "--full-tree", "--no-abbrev", base.Hex()}
	tree, err := runGitCapture(ctx, spec, maxGitTreeOutput)
	if err != nil {
		return Selection{}, err
	}
	if tree.exitCode != 0 {
		return Selection{}, newGitError(gitFailureProcess)
	}
	if err := verifyGitAuthority(repositoryRoot, repository, gitExecutable, gitIdentity); err != nil {
		return Selection{}, err
	}
	manifest, err := parseGitTree(format, base, tree.output)
	if err != nil {
		return Selection{}, err
	}
	return Selection{
		repositoryRoot: repositoryRoot, repositoryIdentity: repository.root, repository: repository,
		gitExecutable: gitExecutable, gitIdentity: gitIdentity,
		format: format, base: base, manifest: manifest,
	}, nil
}

func validateRevision(revision string) error {
	if revision == "" || len(revision) > MaxComponentBytes || !utf8.ValidString(revision) || strings.IndexByte(revision, 0) >= 0 {
		return &ValidationError{Reason: "revision policy value is invalid"}
	}
	for _, character := range revision {
		if character < 0x21 || character == 0x7f {
			return &ValidationError{Reason: "revision policy value contains control or whitespace"}
		}
	}
	return nil
}

func parseSelectionOutput(repositoryRoot string, output []byte) (ObjectFormat, ObjectID, error) {
	if !bytes.HasSuffix(output, []byte{'\n'}) {
		return 0, ObjectID{}, newGitError(gitFailureProtocol)
	}
	lines := bytes.Split(output[:len(output)-1], []byte{'\n'})
	if len(lines) != 3 || !bytes.Equal(lines[0], []byte(repositoryRoot)) {
		return 0, ObjectID{}, newGitError(gitFailureProtocol)
	}
	format, err := NewObjectFormat(string(lines[1]))
	if err != nil {
		return 0, ObjectID{}, newGitError(gitFailureProtocol)
	}
	base, err := parseGitOID(format, lines[2])
	if err != nil {
		return 0, ObjectID{}, err
	}
	return format, base, nil
}

func parseGitTree(format ObjectFormat, base ObjectID, output []byte) (Manifest, error) {
	if len(output) == 0 {
		return NewManifest(format, base, nil)
	}
	if output[len(output)-1] != 0 {
		return Manifest{}, newGitError(gitFailureProtocol)
	}
	records := bytes.Split(output[:len(output)-1], []byte{0})
	entries := make([]Entry, 0, min(len(records), int(MaxEntryCount)))
	for _, record := range records {
		metadata, path, found := bytes.Cut(record, []byte{'\t'})
		if !found || len(path) == 0 {
			return Manifest{}, newGitError(gitFailureProtocol)
		}
		fields := bytes.Fields(metadata)
		if len(fields) != 4 || len(fields[0]) == 0 || !bytes.Equal(fields[1], []byte("blob")) {
			return Manifest{}, &ValidationError{Reason: "Git tree contains a non-regular entry"}
		}
		mode := string(fields[0])
		if mode != "100644" && mode != "100755" {
			return Manifest{}, &ValidationError{Reason: "Git tree contains a symlink, gitlink, or unsupported mode"}
		}
		oid, err := parseGitOID(format, fields[2])
		if err != nil {
			return Manifest{}, err
		}
		size, err := strconv.ParseUint(string(fields[3]), 10, 64)
		if err != nil || strconv.FormatUint(size, 10) != string(fields[3]) {
			return Manifest{}, newGitError(gitFailureProtocol)
		}
		entry, err := NewEntry(path, mode, size, oid)
		if err != nil {
			return Manifest{}, err
		}
		entries = append(entries, entry)
		if uint64(len(entries)) > MaxEntryCount {
			return Manifest{}, &LimitError{Reason: "tree file count exceeded"}
		}
	}
	return NewManifest(format, base, entries)
}

func parseGitOID(format ObjectFormat, encoded []byte) (ObjectID, error) {
	if len(encoded) != format.OIDLength()*2 || string(encoded) != strings.ToLower(string(encoded)) {
		return ObjectID{}, newGitError(gitFailureProtocol)
	}
	raw := make([]byte, format.OIDLength())
	if _, err := hex.Decode(raw, encoded); err != nil {
		return ObjectID{}, newGitError(gitFailureProtocol)
	}
	return NewObjectID(format, raw)
}

func checkpointRepository(path string, expected RepositoryIdentity) (repositoryCheckpoint, error) {
	if !expected.valid() || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return repositoryCheckpoint{}, &ValidationError{Reason: "repository root and identity must be canonical"}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return repositoryCheckpoint{}, &ValidationError{Reason: "repository root contains a symlink or alias"}
	}
	rootFD, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return repositoryCheckpoint{}, newGitError(gitFailurePrivateIO)
	}
	defer unix.Close(rootFD)
	rootStat, err := fstatGit(rootFD)
	if err != nil || rootStat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return repositoryCheckpoint{}, &ValidationError{Reason: "repository root is not an exact directory"}
	}
	root, err := NewRepositoryIdentity(uint64(rootStat.Dev), rootStat.Ino)
	if err != nil || root != expected {
		return repositoryCheckpoint{}, &ValidationError{Reason: "repository root identity differs"}
	}
	gitFD, gitIdentity, err := openGitAdminDirectory(rootFD, ".git")
	if err != nil {
		return repositoryCheckpoint{}, &ValidationError{Reason: "only a primary non-bare Git worktree is supported"}
	}
	defer unix.Close(gitFD)
	configIdentity, config, err := readGitAdminFile(gitFD, "config", maxGitConfigBytes)
	if err != nil {
		return repositoryCheckpoint{}, &ValidationError{Reason: "Git config must be one bounded regular file"}
	}
	if configHasInclude(config) {
		return repositoryCheckpoint{}, &ValidationError{Reason: "Git config includes are forbidden"}
	}
	objectsFD, objectsIdentity, err := openGitAdminDirectory(gitFD, "objects")
	if err != nil {
		return repositoryCheckpoint{}, &ValidationError{Reason: "Git object directory must be exact and local"}
	}
	defer unix.Close(objectsFD)
	if err := rejectGitAdminEntry(objectsFD, "info", "alternates"); err != nil {
		return repositoryCheckpoint{}, err
	}
	if err := rejectGitAdminEntry(gitFD, "info", "grafts"); err != nil {
		return repositoryCheckpoint{}, err
	}
	if err := rejectDirectGitAdminEntry(gitFD, "commondir"); err != nil {
		return repositoryCheckpoint{}, err
	}
	return repositoryCheckpoint{root: root, git: gitIdentity, config: configIdentity, objects: objectsIdentity}, nil
}

func verifyRepository(path string, expected repositoryCheckpoint) error {
	actual, err := checkpointRepository(path, expected.root)
	if err != nil {
		return err
	}
	if actual != expected {
		return &ValidationError{Reason: "Git administrative identity differs"}
	}
	return nil
}

func openGitAdminDirectory(parentFD int, name string) (int, gitAdminIdentity, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, gitAdminIdentity{}, err
	}
	stat, err := fstatGit(fd)
	if err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Ino == 0 {
		unix.Close(fd)
		return -1, gitAdminIdentity{}, errors.New("invalid Git admin directory")
	}
	return fd, gitAdminIdentity{device: uint64(stat.Dev), inode: stat.Ino, uid: stat.Uid, mode: uint32(stat.Mode)}, nil
}

func readGitAdminFile(parentFD int, name string, maximum int64) (gitAdminIdentity, []byte, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return gitAdminIdentity{}, nil, err
	}
	file := os.NewFile(uintptr(fd), "")
	defer file.Close()
	before, err := fstatGit(fd)
	if err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Size < 0 || before.Size > maximum || before.Ino == 0 {
		return gitAdminIdentity{}, nil, errors.New("invalid Git admin file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(data)) > maximum {
		return gitAdminIdentity{}, nil, errors.New("bounded Git admin read failed")
	}
	after, err := fstatGit(fd)
	if err != nil || !sameGitStat(before, after) || int64(len(data)) != before.Size {
		return gitAdminIdentity{}, nil, errors.New("Git admin file changed during read")
	}
	digest := sha256.Sum256(data)
	return gitAdminFromStat(before, digest), data, nil
}

func rejectGitAdminEntry(parentFD int, directory, name string) error {
	fd, _, err := openGitAdminDirectory(parentFD, directory)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return &ValidationError{Reason: "Git administrative path is not local"}
	}
	defer unix.Close(fd)
	return rejectDirectGitAdminEntry(fd, name)
}

func rejectDirectGitAdminEntry(parentFD int, name string) error {
	var stat unix.Stat_t
	err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return newGitError(gitFailurePrivateIO)
	}
	return &ValidationError{Reason: "Git external object or administration indirection is forbidden"}
}

func configHasInclude(config []byte) bool {
	if bytes.IndexByte(config, 0) >= 0 {
		return true
	}
	for _, line := range bytes.Split(config, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) < 3 || line[0] == '#' || line[0] == ';' || line[0] != '[' {
			continue
		}
		end := bytes.IndexByte(line, ']')
		if end < 0 {
			continue
		}
		header := strings.ToLower(strings.TrimSpace(string(line[1:end])))
		if header == "include" || strings.HasPrefix(header, "includeif ") || strings.HasPrefix(header, "includeif\t") {
			return true
		}
	}
	return false
}

func fstatGit(fd int) (unix.Stat_t, error) {
	var stat unix.Stat_t
	err := unix.Fstat(fd, &stat)
	return stat, err
}

func gitAdminFromStat(stat unix.Stat_t, digest [32]byte) gitAdminIdentity {
	return gitAdminIdentity{
		device: uint64(stat.Dev), inode: stat.Ino, uid: stat.Uid, mode: uint32(stat.Mode), size: stat.Size,
		modifiedNS: stat.Mtim.Sec*1e9 + stat.Mtim.Nsec,
		changedNS:  stat.Ctim.Sec*1e9 + stat.Ctim.Nsec, digest: digest,
	}
}

func sameGitStat(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Uid == right.Uid && left.Mode == right.Mode &&
		left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func repositoryIdentityOf(info os.FileInfo) (RepositoryIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return RepositoryIdentity{}, errors.New("repository stat identity is unavailable")
	}
	return NewRepositoryIdentity(uint64(stat.Dev), stat.Ino)
}

func validateGitExecutable(path string) (gitFileIdentity, error) {
	return checkpointGitExecutable(path)
}

func checkpointGitExecutable(path string) (gitFileIdentity, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return gitFileIdentity{}, &ValidationError{Reason: "Git executable must be canonical and absolute"}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return gitFileIdentity{}, &ValidationError{Reason: "Git executable contains a symlink or alias"}
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return gitFileIdentity{}, newGitError(gitFailurePrivateIO)
	}
	file := os.NewFile(uintptr(fd), "")
	defer file.Close()
	before, err := fstatGit(fd)
	if err != nil || before.Mode&unix.S_IFMT != unix.S_IFREG || before.Mode&0o111 == 0 ||
		before.Mode&(unix.S_ISUID|unix.S_ISGID) != 0 || before.Mode&0o022 != 0 ||
		(before.Uid != 0 && before.Uid != uint32(unix.Geteuid())) ||
		before.Size <= 0 || before.Size > maxGitExecutableBytes || before.Ino == 0 {
		return gitFileIdentity{}, &ValidationError{Reason: "Git executable metadata is unsafe"}
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, maxGitExecutableBytes+1))
	after, statErr := fstatGit(fd)
	if err != nil || statErr != nil || written != before.Size || !sameGitStat(before, after) {
		return gitFileIdentity{}, newGitError(gitFailurePrivateIO)
	}
	var digest [32]byte
	copy(digest[:], hasher.Sum(nil))
	return gitFileIdentity{
		device: uint64(before.Dev), inode: before.Ino, uid: before.Uid, mode: uint32(before.Mode), size: before.Size,
		modifiedNS: before.Mtim.Sec*1e9 + before.Mtim.Nsec,
		changedNS:  before.Ctim.Sec*1e9 + before.Ctim.Nsec, digest: digest,
	}, nil
}

func verifyGitAuthority(repository string, expected repositoryCheckpoint, executable string, executableIdentity gitFileIdentity) error {
	if err := verifyRepository(repository, expected); err != nil {
		return err
	}
	actual, err := checkpointGitExecutable(executable)
	if err != nil {
		return err
	}
	if actual != executableIdentity {
		return &ValidationError{Reason: "Git executable identity differs"}
	}
	return nil
}

func newGitHome() (string, error) {
	home, err := os.MkdirTemp("", "dark-factory-git-home-")
	if err != nil {
		return "", newGitError(gitFailurePrivateIO)
	}
	if err := os.Chmod(home, 0o700); err != nil {
		_ = cleanupGitHome(home)
		return "", newGitError(gitFailurePrivateIO)
	}
	return home, nil
}

func cleanupGitHome(home string) error {
	return os.RemoveAll(home)
}

func gitEnvironment(home, repository string) []string {
	return []string{
		"HOME=" + home,
		"TMPDIR=" + home,
		"LC_ALL=C",
		"LANG=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_PROTOCOL_FROM_USER=0",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
		"GIT_CEILING_DIRECTORIES=" + filepath.Dir(repository),
	}
}

func (s gitCommandSpec) command() *exec.Cmd {
	command := exec.Command(s.program, s.arguments...)
	command.Dir = s.repository
	command.Env = gitEnvironment(s.home, s.repository)
	return command
}

type gitChild struct {
	command *exec.Cmd
	pid     int
	pgid    int
	kq      int
	exit    <-chan error
	stdin   *os.File
	stdout  *os.File
	stderr  *os.File
	hook    gitProcessHook
	waited  bool
	groupOK bool
}

type gitReap struct {
	waitErr      error
	contextError error
	cleanup      bool
	observerErr  error
}

func startGitChild(spec gitCommandSpec, withInput bool) (*gitChild, error) {
	command := spec.command()
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, newGitError(gitFailureProcess)
	}
	unix.CloseOnExec(kq)
	var childStdin *os.File
	var stdin *os.File
	if withInput {
		childStdin, stdin, err = os.Pipe()
		if err != nil {
			unix.Close(kq)
			return nil, newGitError(gitFailurePrivateIO)
		}
		command.Stdin = childStdin
	}
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		closeGitFiles(childStdin, stdin)
		unix.Close(kq)
		return nil, newGitError(gitFailurePrivateIO)
	}
	stderr, childStderr, err := os.Pipe()
	if err != nil {
		closeGitFiles(childStdin, stdin, stdout, childStdout)
		unix.Close(kq)
		return nil, newGitError(gitFailurePrivateIO)
	}
	command.Stdout, command.Stderr = childStdout, childStderr
	if err := command.Start(); err != nil {
		closeGitFiles(childStdin, stdin, stdout, childStdout, stderr, childStderr)
		unix.Close(kq)
		return nil, newGitError(gitFailureProcess)
	}
	if spec.hook != nil {
		spec.hook(gitProcessStarted)
	}
	child := &gitChild{
		command: command, pid: command.Process.Pid, pgid: unix.Getpgrp(), kq: kq,
		stdin: stdin, stdout: stdout, stderr: stderr, hook: spec.hook, groupOK: true,
	}
	if closeGitFiles(childStdin, childStdout, childStderr) != nil {
		return nil, child.failStart()
	}
	if !child.checkGroup(false) {
		return nil, child.failStart()
	}
	if err := registerGitExit(kq, child.pid); err != nil {
		return nil, child.failStart()
	}
	exit := make(chan error, 1)
	child.exit = exit
	go func() { exit <- waitGitExit(kq, child.pid) }()
	return child, nil
}

func (c *gitChild) failStart() error {
	if c.command.Process != nil {
		_ = c.command.Process.Signal(os.Kill)
		if c.hook != nil {
			c.hook(gitProcessKilled)
		}
		_ = c.command.Wait()
		if c.hook != nil {
			c.hook(gitProcessWaited)
		}
		c.waited = true
	}
	closeGitFiles(c.stdin, c.stdout, c.stderr)
	_ = unix.Close(c.kq)
	return newGitCleanupError(gitFailureProcess)
}

func closeGitFiles(files ...*os.File) error {
	var closeErr error
	for _, file := range files {
		if file != nil {
			if err := file.Close(); err != nil && closeErr == nil {
				closeErr = err
			}
		}
	}
	return closeErr
}

func registerGitExit(kq, pid int) error {
	change := unix.Kevent_t{Ident: uint64(pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT | unix.EV_RECEIPT, Fflags: unix.NOTE_EXIT}
	receipts := make([]unix.Kevent_t, 1)
	n, err := unix.Kevent(kq, []unix.Kevent_t{change}, receipts, nil)
	if err != nil || n != 1 || receipts[0].Flags&unix.EV_ERROR == 0 || receipts[0].Data != 0 {
		return errors.New("Git NOTE_EXIT registration failed")
	}
	return nil
}

func waitGitExit(kq, pid int) error {
	for {
		events := make([]unix.Kevent_t, 1)
		n, err := unix.Kevent(kq, nil, events, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n != 1 {
			return errors.New("Git NOTE_EXIT observation failed")
		}
		event := events[0]
		if event.Ident != uint64(pid) || event.Filter != unix.EVFILT_PROC || event.Fflags&unix.NOTE_EXIT == 0 || event.Flags&unix.EV_ERROR != 0 {
			return errors.New("unexpected Git NOTE_EXIT event")
		}
		return nil
	}
}

func (c *gitChild) signal(signal unix.Signal) {
	if c.waited {
		panic("Git child signal after Wait")
	}
	c.checkGroup(false)
	_ = unix.Kill(c.pid, signal)
	if c.hook != nil {
		if signal == unix.SIGTERM {
			c.hook(gitProcessTermed)
		} else {
			c.hook(gitProcessKilled)
		}
	}
}

func (c *gitChild) checkGroup(exitObserved bool) bool {
	actual, err := unix.Getpgid(c.pid)
	if exitObserved && errors.Is(err, unix.ESRCH) {
		return c.groupOK
	}
	if err != nil || actual != c.pgid {
		c.groupOK = false
	}
	return c.groupOK
}

func (c *gitChild) reap(ctx context.Context, terminate bool) gitReap {
	if c.waited {
		return gitReap{observerErr: errors.New("Git child waited more than once"), cleanup: true}
	}
	result := gitReap{cleanup: terminate}
	termSent := terminate
	var timer *time.Timer
	var timerChannel <-chan time.Time
	if terminate {
		c.signal(unix.SIGTERM)
		timer = time.NewTimer(gitTerminateGrace)
		timerChannel = timer.C
	}
	contextDone := ctx.Done()
	var observed error
	for {
		select {
		case observed = <-c.exit:
			goto observedExit
		case <-contextDone:
			result.contextError = ctx.Err()
			result.cleanup = true
			contextDone = nil
			if !termSent {
				termSent = true
				c.signal(unix.SIGTERM)
				timer = time.NewTimer(gitTerminateGrace)
				timerChannel = timer.C
			}
		case <-timerChannel:
			c.signal(unix.SIGKILL)
			timerChannel = nil
		}
	}

observedExit:
	if timer != nil {
		timer.Stop()
	}
	_ = unix.Close(c.kq)
	if observed != nil {
		result.observerErr = observed
		result.cleanup = true
		c.signal(unix.SIGKILL)
	}
	if !c.checkGroup(true) {
		result.cleanup = true
	}
	result.waitErr = c.command.Wait()
	c.waited = true
	if c.hook != nil {
		c.hook(gitProcessWaited)
	}
	return result
}

func (c *gitChild) reapObserved(observed error) gitReap {
	if c.waited {
		return gitReap{observerErr: errors.New("Git child waited more than once"), cleanup: true}
	}
	_ = unix.Close(c.kq)
	result := gitReap{observerErr: observed, cleanup: observed != nil}
	if observed != nil {
		c.signal(unix.SIGKILL)
	}
	if !c.checkGroup(true) {
		result.cleanup = true
	}
	result.waitErr = c.command.Wait()
	c.waited = true
	if c.hook != nil {
		c.hook(gitProcessWaited)
	}
	return result
}

func runGitCapture(ctx context.Context, spec gitCommandSpec, maximum int) (gitCapture, error) {
	if err := ctx.Err(); err != nil {
		return gitCapture{}, newGitContextError(err, false)
	}
	child, err := startGitChild(spec, false)
	if err != nil {
		return gitCapture{}, err
	}
	readContext, cancelRead := context.WithCancelCause(ctx)
	defer cancelRead(nil)
	stdoutChannel := make(chan gitStreamResult, 1)
	stderrChannel := make(chan gitStreamResult, 1)
	go func() {
		result := readGitCapture(child.stdout, maximum)
		if result.err != nil || result.overflow {
			cancelRead(errors.New("bounded Git stdout failed"))
		}
		stdoutChannel <- result
	}()
	go func() {
		result := readGitDiscard(child.stderr, maxGitStderrBytes)
		if result.err != nil || result.overflow {
			cancelRead(errors.New("bounded Git stderr failed"))
		}
		stderrChannel <- result
	}()
	reaped := child.reap(readContext, false)
	stdoutResult, stdoutDrained := collectGitStream(stdoutChannel, child.stdout)
	stderrResult, stderrDrained := collectGitStream(stderrChannel, child.stderr)
	closeErr := closeGitFiles(child.stdout, child.stderr)
	if reaped.contextError != nil && (errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return gitCapture{}, newGitContextError(ctx.Err(), true)
	}
	if !stdoutDrained || !stderrDrained || closeErr != nil || reaped.cleanup || reaped.observerErr != nil {
		return gitCapture{}, newGitCleanupError(gitFailurePrivateIO)
	}
	if stdoutResult.overflow {
		return gitCapture{}, &LimitError{Reason: "Git metadata output exceeded"}
	}
	if stdoutResult.err != nil || stderrResult.err != nil || stderrResult.overflow || (reaped.waitErr != nil && !isExitError(reaped.waitErr)) {
		return gitCapture{}, newGitError(gitFailurePrivateIO)
	}
	exitCode := 0
	if reaped.waitErr != nil {
		exitCode = reaped.waitErr.(*exec.ExitError).ExitCode()
	}
	return gitCapture{output: stdoutResult.data, exitCode: exitCode}, nil
}

func collectGitStream(channel <-chan gitStreamResult, file *os.File) (gitStreamResult, bool) {
	timer := time.NewTimer(gitPipeDrainGrace)
	defer timer.Stop()
	select {
	case result := <-channel:
		return result, true
	case <-timer.C:
		_ = file.Close()
		return <-channel, false
	}
}

func readGitDiscard(reader io.Reader, maximum int64) gitStreamResult {
	written, err := io.Copy(io.Discard, io.LimitReader(reader, maximum+1))
	return gitStreamResult{overflow: written > maximum, err: err}
}

func readGitCapture(reader io.Reader, maximum int) gitStreamResult {
	data := make([]byte, 0, min(maximum, 64<<10))
	buffer := make([]byte, 32<<10)
	for {
		n, err := reader.Read(buffer)
		if n > 0 {
			if len(data) > maximum-n {
				return gitStreamResult{overflow: true}
			}
			data = append(data, buffer[:n]...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return gitStreamResult{data: data}
			}
			return gitStreamResult{err: err}
		}
	}
}

func isExitError(err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError)
}

// GitBlobs owns one exact ordered git cat-file --batch child.
type GitBlobs struct {
	mu sync.Mutex

	selection Selection
	home      string
	child     *gitChild
	stdin     *os.File
	stdoutFD  *os.File
	stdout    *bufio.Reader
	stderrFD  *os.File
	stderr    <-chan gitStreamResult
	next      int
	closed    bool
	stderrSet bool
	stderrVal gitStreamResult
}

// OpenGitBlobs starts one bounded exact-object reader after rechecking the
// repository and executable identities captured by SelectGit.
func OpenGitBlobs(ctx context.Context, gitExecutable, repositoryRoot string, selection Selection) (*GitBlobs, error) {
	return openGitBlobs(ctx, gitExecutable, repositoryRoot, selection, nil)
}

func openGitBlobs(ctx context.Context, gitExecutable, repositoryRoot string, selection Selection, hook gitProcessHook) (*GitBlobs, error) {
	if err := ctx.Err(); err != nil {
		return nil, newGitContextError(err, false)
	}
	if !selection.valid() || repositoryRoot != selection.repositoryRoot || gitExecutable != selection.gitExecutable {
		return nil, &ValidationError{Reason: "blob reader does not match the immutable selection"}
	}
	if err := verifyGitAuthority(repositoryRoot, selection.repository, gitExecutable, selection.gitIdentity); err != nil {
		return nil, err
	}
	home, err := newGitHome()
	if err != nil {
		return nil, err
	}
	spec := gitCommandSpec{
		program: gitExecutable, repository: repositoryRoot, home: home,
		arguments: []string{"-C", repositoryRoot, "cat-file", "--batch"}, operation: "blob reader", hook: hook,
	}
	child, err := startGitChild(spec, true)
	if err != nil {
		_ = cleanupGitHome(home)
		return nil, err
	}
	stderrChannel := make(chan gitStreamResult, 1)
	go func() { stderrChannel <- readGitDiscard(child.stderr, maxGitStderrBytes) }()
	blobs := &GitBlobs{
		selection: selection, home: home, child: child, stdin: child.stdin,
		stdoutFD: child.stdout, stdout: bufio.NewReaderSize(child.stdout, maxGitBlobHeader+1),
		stderrFD: child.stderr, stderr: stderrChannel,
	}
	if err := verifyGitAuthority(repositoryRoot, selection.repository, gitExecutable, selection.gitIdentity); err != nil {
		_ = blobs.abortLocked()
		return nil, newGitCleanupError(gitFailureProcess)
	}
	return blobs, nil
}

// Read returns the next exact selected blob. Calls must follow Manifest entry
// order; arbitrary repository objects are never accepted.
func (b *GitBlobs) Read(ctx context.Context, requested ObjectID) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, &LifecycleError{Reason: "blob read after close"}
	}
	if b.next >= len(b.selection.manifest.entries) || !requested.equal(b.selection.manifest.entries[b.next].oid) {
		_ = b.abortLocked()
		return nil, newGitCleanupError(gitFailureProtocol)
	}
	if err := ctx.Err(); err != nil {
		_ = b.abortLocked()
		return nil, newGitContextError(err, true)
	}
	if err := verifyGitAuthority(b.selection.repositoryRoot, b.selection.repository, b.selection.gitExecutable, b.selection.gitIdentity); err != nil {
		_ = b.abortLocked()
		return nil, newGitCleanupError(gitFailureProcess)
	}
	expected := b.selection.manifest.entries[b.next]
	result := make(chan gitStreamResult, 1)
	go func() { result <- b.readOne(ctx, expected) }()
	select {
	case response := <-result:
		if response.err != nil || response.overflow {
			_ = b.abortLocked()
			return nil, newGitCleanupError(gitFailureProtocol)
		}
		if err := verifyGitAuthority(b.selection.repositoryRoot, b.selection.repository, b.selection.gitExecutable, b.selection.gitIdentity); err != nil {
			_ = b.abortLocked()
			return nil, newGitCleanupError(gitFailureProcess)
		}
		b.next++
		return response.data, nil
	case stderrResult := <-b.stderr:
		b.stderrSet, b.stderrVal = true, stderrResult
		_ = b.abortLocked()
		<-result
		return nil, newGitCleanupError(gitFailurePrivateIO)
	case observed := <-b.child.exit:
		_ = b.finishObservedLocked(observed)
		<-result
		return nil, newGitCleanupError(gitFailureProcess)
	case <-ctx.Done():
		_ = b.abortLocked()
		<-result
		return nil, newGitContextError(ctx.Err(), true)
	}
}

func (b *GitBlobs) readOne(ctx context.Context, expected Entry) gitStreamResult {
	if _, err := io.WriteString(b.stdin, expected.oid.Hex()+"\n"); err != nil {
		return gitStreamResult{err: err}
	}
	header, err := b.stdout.ReadSlice('\n')
	if err != nil || len(header) > maxGitBlobHeader || len(header) == 0 {
		return gitStreamResult{err: errors.New("blob header framing")}
	}
	fields := bytes.Fields(header[:len(header)-1])
	if len(fields) != 3 || !bytes.Equal(fields[0], []byte(expected.oid.Hex())) || !bytes.Equal(fields[1], []byte("blob")) {
		return gitStreamResult{err: errors.New("blob header identity or type")}
	}
	size, err := strconv.ParseUint(string(fields[2]), 10, 64)
	if err != nil || strconv.FormatUint(size, 10) != string(fields[2]) || size != expected.size || size > MaxBlobBytes {
		return gitStreamResult{err: errors.New("blob header size")}
	}
	data := make([]byte, int(size))
	if _, err := io.ReadFull(b.stdout, data); err != nil {
		return gitStreamResult{err: err}
	}
	delimiter, err := b.stdout.ReadByte()
	if err != nil || delimiter != '\n' {
		return gitStreamResult{err: errors.New("blob delimiter")}
	}
	actual, err := hashBlobContext(ctx, expected.oid.format, data, nil)
	if err != nil || !actual.equal(expected.oid) {
		return gitStreamResult{err: errors.New("blob object hash mismatch")}
	}
	return gitStreamResult{data: data}
}

// Close closes stdin, waits once for the exact child, drains private stderr,
// and removes the private Git HOME.
func (b *GitBlobs) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return &LifecycleError{Reason: "close blob reader more than once"}
	}
	if b.next != len(b.selection.manifest.entries) {
		return b.abortLocked()
	}
	b.closed = true
	stdinErr := b.stdin.Close()
	closeContext, cancel := context.WithTimeout(context.Background(), gitPipeDrainGrace)
	reaped := b.child.reap(closeContext, false)
	cancel()
	trailingChannel := make(chan gitStreamResult, 1)
	go func() {
		_, err := b.stdout.ReadByte()
		if errors.Is(err, io.EOF) {
			trailingChannel <- gitStreamResult{}
			return
		}
		trailingChannel <- gitStreamResult{err: errors.New("trailing batch output")}
	}()
	trailing, stdoutDrained := collectGitStream(trailingChannel, b.stdoutFD)
	stderrResult, stderrDrained := b.collectStderr()
	pipeErr := closeGitFiles(b.stdoutFD, b.stderrFD)
	homeErr := cleanupGitHome(b.home)
	if reaped.cleanup || reaped.observerErr != nil || !stdoutDrained || !stderrDrained {
		return newGitCleanupError(gitFailureProcess)
	}
	if trailing.err != nil {
		return newGitError(gitFailureProtocol)
	}
	if stdinErr != nil || pipeErr != nil || stderrResult.err != nil || stderrResult.overflow || homeErr != nil || reaped.waitErr != nil {
		return newGitError(gitFailurePrivateIO)
	}
	return nil
}

// Abort terminates and waits once for the exact child without exposing output.
func (b *GitBlobs) Abort() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return &LifecycleError{Reason: "abort blob reader after close"}
	}
	return b.abortLocked()
}

func (b *GitBlobs) abortLocked() error {
	if b.closed {
		return nil
	}
	b.closed = true
	_ = b.stdin.Close()
	reaped := b.child.reap(context.Background(), true)
	_ = closeGitFiles(b.stdoutFD, b.stderrFD)
	stderrResult, _ := b.collectStderr()
	homeErr := cleanupGitHome(b.home)
	_ = reaped
	_ = stderrResult
	_ = homeErr
	return newGitCleanupError(gitFailureProcess)
}

func (b *GitBlobs) finishObservedLocked(observed error) error {
	b.closed = true
	_ = b.stdin.Close()
	_ = b.child.reapObserved(observed)
	_ = closeGitFiles(b.stdoutFD, b.stderrFD)
	_, _ = b.collectStderr()
	_ = cleanupGitHome(b.home)
	return newGitCleanupError(gitFailureProcess)
}

func (b *GitBlobs) collectStderr() (gitStreamResult, bool) {
	if b.stderrSet {
		return b.stderrVal, true
	}
	b.stderrVal, b.stderrSet = collectGitStream(b.stderr, b.stderrFD)
	return b.stderrVal, b.stderrSet
}
