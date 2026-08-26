//go:build darwin

package change

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
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
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	maxGitSelectionOutput = 4 << 10
	maxGitTreeOutput      = int(MaxEntryCount+1) * (MaxRelativePathBytes + 128)
	maxGitBlobHeader      = 256
	maxGitStderrBytes     = 64 << 10
	gitTerminateGrace     = 250 * time.Millisecond
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
	repositoryIdentity, err := verifyRepository(repositoryRoot, expected)
	if err != nil {
		return Selection{}, err
	}
	gitIdentity, err := validateGitExecutable(gitExecutable)
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
	if err := verifyGitAuthority(repositoryRoot, repositoryIdentity, gitExecutable, gitIdentity); err != nil {
		return Selection{}, err
	}
	if !(partial.exitCode == 1 && len(partial.output) == 0) {
		if partial.exitCode == 0 || len(partial.output) != 0 {
			return Selection{}, &ValidationError{Reason: "partial-clone repositories are forbidden"}
		}
		return Selection{}, &GitProcessError{Operation: spec.operation, Cause: fmt.Errorf("exit status %d", partial.exitCode)}
	}

	spec.operation = "commit selection"
	spec.arguments = []string{"-C", repositoryRoot, "rev-parse", "--show-toplevel", "--show-object-format", "--verify", "--end-of-options", revision + "^{commit}"}
	resolved, err := runGitCapture(ctx, spec, maxGitSelectionOutput)
	if err != nil {
		return Selection{}, err
	}
	if resolved.exitCode != 0 {
		return Selection{}, &GitProcessError{Operation: spec.operation, Cause: fmt.Errorf("exit status %d", resolved.exitCode)}
	}
	if err := verifyGitAuthority(repositoryRoot, repositoryIdentity, gitExecutable, gitIdentity); err != nil {
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
		return Selection{}, &GitProcessError{Operation: spec.operation, Cause: fmt.Errorf("exit status %d", tree.exitCode)}
	}
	if err := verifyGitAuthority(repositoryRoot, repositoryIdentity, gitExecutable, gitIdentity); err != nil {
		return Selection{}, err
	}
	manifest, err := parseGitTree(format, base, tree.output)
	if err != nil {
		return Selection{}, err
	}
	return Selection{
		repositoryRoot: repositoryRoot, repositoryIdentity: repositoryIdentity,
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
		return 0, ObjectID{}, &GitProtocolError{Reason: "selection framing"}
	}
	lines := bytes.Split(output[:len(output)-1], []byte{'\n'})
	if len(lines) != 3 || !bytes.Equal(lines[0], []byte(repositoryRoot)) {
		return 0, ObjectID{}, &GitProtocolError{Reason: "selection root"}
	}
	format, err := NewObjectFormat(string(lines[1]))
	if err != nil {
		return 0, ObjectID{}, err
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
		return Manifest{}, &GitProtocolError{Reason: "tree record framing"}
	}
	records := bytes.Split(output[:len(output)-1], []byte{0})
	entries := make([]Entry, 0, min(len(records), int(MaxEntryCount)))
	for _, record := range records {
		metadata, path, found := bytes.Cut(record, []byte{'\t'})
		if !found || len(path) == 0 {
			return Manifest{}, &GitProtocolError{Reason: "tree record fields"}
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
			return Manifest{}, &GitProtocolError{Reason: "tree blob size"}
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
		return ObjectID{}, &GitProtocolError{Reason: "object ID encoding"}
	}
	raw := make([]byte, format.OIDLength())
	if _, err := hex.Decode(raw, encoded); err != nil {
		return ObjectID{}, &GitProtocolError{Reason: "object ID encoding"}
	}
	return NewObjectID(format, raw)
}

func verifyRepository(path string, expected RepositoryIdentity) (RepositoryIdentity, error) {
	if !expected.valid() || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return RepositoryIdentity{}, &ValidationError{Reason: "repository root and identity must be canonical"}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return RepositoryIdentity{}, &ValidationError{Reason: "repository root contains a symlink or alias"}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return RepositoryIdentity{}, &ValidationError{Reason: "repository root is not an exact directory"}
	}
	identity, err := repositoryIdentityOf(info)
	if err != nil || identity != expected {
		return RepositoryIdentity{}, &ValidationError{Reason: "repository root identity differs"}
	}
	gitDirectory := filepath.Join(path, ".git")
	gitInfo, err := os.Lstat(gitDirectory)
	if err != nil || !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return RepositoryIdentity{}, &ValidationError{Reason: "only a primary non-bare Git worktree is supported"}
	}
	for _, forbidden := range []string{
		filepath.Join(gitDirectory, "objects", "info", "alternates"),
		filepath.Join(gitDirectory, "info", "grafts"),
	} {
		if _, err := os.Lstat(forbidden); err == nil {
			return RepositoryIdentity{}, &ValidationError{Reason: "Git alternates and grafts are forbidden"}
		} else if !errors.Is(err, os.ErrNotExist) {
			return RepositoryIdentity{}, &GitProcessError{Operation: "repository policy check", Cause: err}
		}
	}
	return identity, nil
}

func repositoryIdentityOf(info os.FileInfo) (RepositoryIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return RepositoryIdentity{}, errors.New("repository stat identity is unavailable")
	}
	return NewRepositoryIdentity(uint64(stat.Dev), stat.Ino)
}

func validateGitExecutable(path string) (gitFileIdentity, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return gitFileIdentity{}, &ValidationError{Reason: "Git executable must be canonical and absolute"}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return gitFileIdentity{}, &ValidationError{Reason: "Git executable contains a symlink or alias"}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 || info.Mode()&os.ModeSymlink != 0 {
		return gitFileIdentity{}, &ValidationError{Reason: "Git executable is not an exact executable file"}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 {
		return gitFileIdentity{}, &ValidationError{Reason: "Git executable identity is unavailable"}
	}
	return gitFileIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}

func verifyGitAuthority(repository string, expected RepositoryIdentity, executable string, executableIdentity gitFileIdentity) error {
	if _, err := verifyRepository(repository, expected); err != nil {
		return err
	}
	actual, err := validateGitExecutable(executable)
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
		return "", &GitProcessError{Operation: "private HOME creation", Cause: err}
	}
	if err := os.Chmod(home, 0o700); err != nil {
		_ = cleanupGitHome(home)
		return "", &GitProcessError{Operation: "private HOME creation", Cause: err}
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
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return command
}

func runGitCapture(ctx context.Context, spec gitCommandSpec, maximum int) (gitCapture, error) {
	if err := ctx.Err(); err != nil {
		return gitCapture{}, &GitProcessError{Operation: spec.operation, Cause: err}
	}
	command := spec.command()
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		return gitCapture{}, &GitProcessError{Operation: spec.operation, Cause: err}
	}
	stderr, childStderr, err := os.Pipe()
	if err != nil {
		stdout.Close()
		childStdout.Close()
		return gitCapture{}, &GitProcessError{Operation: spec.operation, Cause: err}
	}
	command.Stdout = childStdout
	command.Stderr = childStderr
	if err := command.Start(); err != nil {
		stdout.Close()
		childStdout.Close()
		stderr.Close()
		childStderr.Close()
		return gitCapture{}, &GitProcessError{Operation: spec.operation, Cause: err}
	}
	childStdout.Close()
	childStderr.Close()
	if spec.hook != nil {
		spec.hook(gitProcessStarted)
	}
	outChannel := make(chan gitStreamResult, 1)
	errChannel := make(chan gitStreamResult, 1)
	waitChannel := make(chan error, 1)
	go func() { outChannel <- readGitCapture(stdout, maximum) }()
	go func() { errChannel <- readGitDiscard(stderr, maxGitStderrBytes) }()
	go func() {
		waitErr := command.Wait()
		if spec.hook != nil {
			spec.hook(gitProcessWaited)
		}
		waitChannel <- waitErr
	}()

	var output gitStreamResult
	var stderrResult gitStreamResult
	var waitErr error
	var outputDone, stderrDone, waitDone bool
	var stopCause error
	contextDone := ctx.Done()
	for !outputDone || !stderrDone || !waitDone {
		select {
		case result := <-outChannel:
			output, outputDone = result, true
			if (result.overflow || result.err != nil) && stopCause == nil {
				stopCause = errors.New("bounded stdout read failed")
			}
		case stderrResult = <-errChannel:
			stderrDone = true
			if (stderrResult.err != nil || stderrResult.overflow) && stopCause == nil {
				stopCause = errors.New("private stderr drain failed")
			}
		case waitErr = <-waitChannel:
			waitDone = true
		case <-contextDone:
			if stopCause == nil {
				stopCause = ctx.Err()
			}
			contextDone = nil
		}
		if stopCause != nil && !waitDone {
			waitErr = stopGitProcess(command, waitChannel, spec.hook)
			waitDone = true
		}
	}
	if errors.Is(stopCause, context.Canceled) || errors.Is(stopCause, context.DeadlineExceeded) {
		return gitCapture{}, &GitProcessError{Operation: spec.operation, Cause: stopCause}
	}
	if output.overflow {
		return gitCapture{}, &LimitError{Reason: "Git metadata output exceeded"}
	}
	if output.err != nil || stderrResult.err != nil || stderrResult.overflow || (waitErr != nil && !isExitError(waitErr)) {
		return gitCapture{}, &GitProcessError{Operation: spec.operation, Cause: errors.New("bounded child I/O failed")}
	}
	exitCode := 0
	if waitErr != nil {
		exitCode = waitErr.(*exec.ExitError).ExitCode()
	}
	return gitCapture{output: output.data, exitCode: exitCode}, nil
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

func stopGitProcess(command *exec.Cmd, waitChannel <-chan error, hook gitProcessHook) error {
	if command.Process == nil {
		return errors.New("Git process is absent")
	}
	_ = unix.Kill(-command.Process.Pid, unix.SIGTERM)
	if hook != nil {
		hook(gitProcessTermed)
	}
	timer := time.NewTimer(gitTerminateGrace)
	defer timer.Stop()
	select {
	case err := <-waitChannel:
		return err
	case <-timer.C:
		_ = unix.Kill(-command.Process.Pid, unix.SIGKILL)
		if hook != nil {
			hook(gitProcessKilled)
		}
		return <-waitChannel
	}
}

// GitBlobs owns one exact ordered git cat-file --batch child.
type GitBlobs struct {
	mu sync.Mutex

	selection Selection
	home      string
	command   *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	wait      <-chan error
	stderr    <-chan gitStreamResult
	hook      gitProcessHook
	next      int
	closed    bool
	waited    bool
	waitErr   error
}

// OpenGitBlobs starts one bounded exact-object reader after rechecking the
// repository and executable identities captured by SelectGit.
func OpenGitBlobs(ctx context.Context, gitExecutable, repositoryRoot string, selection Selection) (*GitBlobs, error) {
	return openGitBlobs(ctx, gitExecutable, repositoryRoot, selection, nil)
}

func openGitBlobs(ctx context.Context, gitExecutable, repositoryRoot string, selection Selection, hook gitProcessHook) (*GitBlobs, error) {
	if err := ctx.Err(); err != nil {
		return nil, &GitProcessError{Operation: "blob reader start", Cause: err}
	}
	if !selection.valid() || repositoryRoot != selection.repositoryRoot || gitExecutable != selection.gitExecutable {
		return nil, &ValidationError{Reason: "blob reader does not match the immutable selection"}
	}
	if err := verifyGitAuthority(repositoryRoot, selection.repositoryIdentity, gitExecutable, selection.gitIdentity); err != nil {
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
	command := spec.command()
	childStdin, stdin, err := os.Pipe()
	if err != nil {
		_ = cleanupGitHome(home)
		return nil, &GitProcessError{Operation: spec.operation, Cause: err}
	}
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		childStdin.Close()
		stdin.Close()
		_ = cleanupGitHome(home)
		return nil, &GitProcessError{Operation: spec.operation, Cause: err}
	}
	stderr, childStderr, err := os.Pipe()
	if err != nil {
		childStdin.Close()
		stdin.Close()
		stdout.Close()
		childStdout.Close()
		_ = cleanupGitHome(home)
		return nil, &GitProcessError{Operation: spec.operation, Cause: err}
	}
	command.Stdin = childStdin
	command.Stdout = childStdout
	command.Stderr = childStderr
	if err := command.Start(); err != nil {
		childStdin.Close()
		stdin.Close()
		stdout.Close()
		childStdout.Close()
		stderr.Close()
		childStderr.Close()
		_ = cleanupGitHome(home)
		return nil, &GitProcessError{Operation: spec.operation, Cause: err}
	}
	childStdin.Close()
	childStdout.Close()
	childStderr.Close()
	if hook != nil {
		hook(gitProcessStarted)
	}
	waitChannel := make(chan error, 1)
	stderrChannel := make(chan gitStreamResult, 1)
	go func() {
		waitErr := command.Wait()
		if hook != nil {
			hook(gitProcessWaited)
		}
		waitChannel <- waitErr
	}()
	go func() { stderrChannel <- readGitDiscard(stderr, maxGitStderrBytes) }()
	blobs := &GitBlobs{
		selection: selection, home: home, command: command, stdin: stdin,
		stdout: bufio.NewReaderSize(stdout, maxGitBlobHeader+1), wait: waitChannel,
		stderr: stderrChannel, hook: hook,
	}
	if err := verifyGitAuthority(repositoryRoot, selection.repositoryIdentity, gitExecutable, selection.gitIdentity); err != nil {
		_ = blobs.abortLocked()
		return nil, err
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
		return nil, &ValidationError{Reason: "blob requests must follow the immutable manifest"}
	}
	if err := ctx.Err(); err != nil {
		_ = b.abortLocked()
		return nil, &GitProcessError{Operation: "blob read", Cause: err}
	}
	if err := verifyGitAuthority(b.selection.repositoryRoot, b.selection.repositoryIdentity, b.selection.gitExecutable, b.selection.gitIdentity); err != nil {
		_ = b.abortLocked()
		return nil, err
	}
	expected := b.selection.manifest.entries[b.next]
	result := make(chan gitStreamResult, 1)
	go func() { result <- b.readOne(expected) }()
	select {
	case response := <-result:
		if response.err != nil || response.overflow {
			_ = b.abortLocked()
			return nil, &GitProtocolError{Reason: "blob framing or selected object mismatch"}
		}
		if err := verifyGitAuthority(b.selection.repositoryRoot, b.selection.repositoryIdentity, b.selection.gitExecutable, b.selection.gitIdentity); err != nil {
			_ = b.abortLocked()
			return nil, err
		}
		b.next++
		return response.data, nil
	case stderrResult := <-b.stderr:
		_ = b.abortWithStderrLocked(stderrResult)
		<-result
		return nil, &GitProcessError{Operation: "blob reader stderr", Cause: errors.New("private stderr limit or read failure")}
	case <-ctx.Done():
		_ = b.abortLocked()
		<-result
		return nil, &GitProcessError{Operation: "blob read", Cause: ctx.Err()}
	}
}

func (b *GitBlobs) readOne(expected Entry) gitStreamResult {
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
		_ = b.abortLocked()
		return &LifecycleError{Reason: "close before every selected blob was read"}
	}
	b.closed = true
	stdinErr := b.stdin.Close()
	waitErr := b.waitForExit(false)
	stderrResult := <-b.stderr
	homeErr := cleanupGitHome(b.home)
	if stdinErr != nil || stderrResult.err != nil || stderrResult.overflow || homeErr != nil || (waitErr != nil && !isExitError(waitErr)) || (isExitError(waitErr) && waitErr.(*exec.ExitError).ExitCode() != 0) {
		return &GitProcessError{Operation: "blob reader close", Cause: errors.New("child close or private I/O failed")}
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
	waitErr := b.waitForExit(true)
	stderrResult := <-b.stderr
	homeErr := cleanupGitHome(b.home)
	if stderrResult.err != nil || stderrResult.overflow || homeErr != nil || (waitErr != nil && !isExitError(waitErr)) {
		return &GitProcessError{Operation: "blob reader abort", Cause: errors.New("child abort or private I/O failed")}
	}
	return nil
}

func (b *GitBlobs) abortWithStderrLocked(stderrResult gitStreamResult) error {
	if b.closed {
		return nil
	}
	b.closed = true
	_ = b.stdin.Close()
	waitErr := b.waitForExit(true)
	homeErr := cleanupGitHome(b.home)
	if stderrResult.err != nil || stderrResult.overflow || homeErr != nil || (waitErr != nil && !isExitError(waitErr)) {
		return &GitProcessError{Operation: "blob reader abort", Cause: errors.New("child abort or private I/O failed")}
	}
	return nil
}

func (b *GitBlobs) waitForExit(force bool) error {
	if b.waited {
		return b.waitErr
	}
	b.waited = true
	if force {
		b.waitErr = stopGitProcess(b.command, b.wait, b.hook)
		return b.waitErr
	}
	timer := time.NewTimer(gitTerminateGrace)
	defer timer.Stop()
	select {
	case b.waitErr = <-b.wait:
		return b.waitErr
	case <-timer.C:
		b.waitErr = stopGitProcess(b.command, b.wait, b.hook)
		return b.waitErr
	}
}
