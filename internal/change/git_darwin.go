//go:build darwin

package change

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/macho"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const (
	maxGitSelectionOutput = 4 << 10
	maxGitListOutput      = 4 << 20
	maxGitStatusOutput    = 1 << 20
	maxGitStderrBytes     = 64 << 10
	maxGitConfigBytes     = 1 << 20
	maxGitExecutableBytes = 64 << 20
	maxRevisionBytes      = 4096
	gitLockRetries        = 5
	gitLockRetryDelay     = 200 * time.Millisecond
	gitTerminateGrace     = 250 * time.Millisecond
	gitPipeDrainGrace     = time.Second
)

type gitCommandSpec struct {
	program     string
	repository  string
	home        string
	arguments   []string
	environment []string
	hook        gitProcessHook
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

// SelectGit resolves revision once to the exact commit a Change is made
// from, refreshing a configured upstream first, without touching the
// checkout.
func SelectGit(ctx context.Context, gitExecutable, repositoryRoot, revision string, expected RepositoryIdentity) (Selection, error) {
	return selectGitWithTrust(ctx, gitExecutable, repositoryRoot, revision, expected, nil, true)
}

// SelectRegisteredGit retains the registered Git administration and origin
// boundary across the host/worker handoff before source selection or refresh.
func SelectRegisteredGit(ctx context.Context, gitExecutable, repositoryRoot, revision string, expected RepositorySourceIdentity) (Selection, error) {
	return selectGitWithTrust(ctx, gitExecutable, repositoryRoot, revision, expected.Root, nil, true, expected)
}

// VerifyRepositoryRoot rechecks the exact repository-root identity without
// resolving a revision or selecting source content.
func VerifyRepositoryRoot(repositoryRoot string, expected RepositoryIdentity) error {
	rootFD, _, err := openRepositoryRoot(repositoryRoot, expected)
	if err == nil {
		err = unix.Close(rootFD)
	}
	return err
}

// selectGit is the package-private native-process fixture seam. Public callers
// can enter only through SelectGit's root-owned Developer-toolchain check.
func selectGit(ctx context.Context, gitExecutable, repositoryRoot, revision string, expected RepositoryIdentity, hook gitProcessHook) (Selection, error) {
	return selectGitWithTrust(ctx, gitExecutable, repositoryRoot, revision, expected, hook, false)
}

func selectGitWithTrust(ctx context.Context, gitExecutable, repositoryRoot, revision string, expected RepositoryIdentity, hook gitProcessHook, trusted bool, bound ...RepositorySourceIdentity) (Selection, error) {
	if err := validateRevision(revision); err != nil {
		return Selection{}, err
	}
	repository, err := checkpointRepository(repositoryRoot, expected)
	if err != nil {
		return Selection{}, err
	}
	gitIdentity, err := checkpointExpectedGitExecutable(gitExecutable, trusted)
	if err != nil {
		return Selection{}, err
	}
	home, err := newGitHome()
	if err != nil {
		return Selection{}, err
	}
	defer cleanupGitHome(home)
	if len(bound) > 0 {
		authority := gitAuthority{repositoryRoot: repositoryRoot, repository: repository, gitExecutable: gitExecutable, gitIdentity: gitIdentity, home: home, hook: hook}
		actual, err := authority.sourceIdentity(ctx)
		expectedSource := bound[0]
		if err != nil {
			return Selection{}, err
		}
		if !expectedSource.Valid() || actual.Root != expectedSource.Root || actual.Git != expectedSource.Git || actual.OriginDigest != expectedSource.OriginDigest {
			return Selection{}, &ValidationError{Reason: "registered checkout identity changed"}
		}
	}
	spec := gitCommandSpec{program: gitExecutable, repository: repositoryRoot, home: home, hook: hook}

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

	revision, err = refreshTrackingRevision(ctx, spec, revision, func() error {
		return verifyGitAuthority(repositoryRoot, repository, gitExecutable, gitIdentity)
	})
	if err != nil {
		return Selection{}, err
	}

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
	return Selection{
		repositoryRoot: repositoryRoot, repository: repository,
		gitExecutable: gitExecutable, gitIdentity: gitIdentity,
		format: format, base: base,
	}, nil
}

// refreshTrackingRevision runs only for a fresh Change, before its commit is
// pinned. Retained Changes and explicit local revisions never refresh source.
func refreshTrackingRevision(ctx context.Context, spec gitCommandSpec, revision string, verify func() error) (string, error) {
	run := func(arguments ...string) ([]byte, error) {
		spec.arguments = append([]string{"-C", spec.repository}, arguments...)
		result, err := runGitCapture(ctx, spec, maxGitSelectionOutput)
		if err != nil {
			return nil, err
		}
		if err := verify(); err != nil {
			return nil, err
		}
		if result.exitCode != 0 {
			return nil, &ValidationError{Reason: "source refresh failed; configured remote source was not selected"}
		}
		return result.output, nil
	}
	var remote, branch string
	if revision == "HEAD" {
		// Empty upstream means a deliberately local project, including detached
		// HEAD. This does not fall back when an actual configured fetch fails.
		head, err := run("rev-parse", "--symbolic-full-name", "HEAD")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(string(head)) == "HEAD" {
			return revision, nil
		}
		revision = strings.TrimSpace(string(head))
		output, err := run("for-each-ref", "--count=1", "--format=%(upstream) %(upstream:remotename) %(upstream:remoteref)", revision)
		if err != nil {
			return "", err
		}
		fields := strings.Fields(string(output))
		if len(fields) == 0 {
			// Git also prints an empty upstream for broken tracking config.
			// Only genuinely unconfigured branches may use their local source.
			for _, field := range []string{"remote", "merge"} {
				value, err := run("config", "--default", "", "--get", "branch."+strings.TrimPrefix(revision, "refs/heads/")+"."+field)
				if err != nil {
					return "", err
				}
				if len(bytes.TrimSpace(value)) != 0 {
					return "", &ValidationError{Reason: "configured source upstream is invalid"}
				}
			}
			return revision, nil
		}
		if len(fields) != 3 {
			return "", &ValidationError{Reason: "configured source upstream is invalid"}
		}
		revision, remote, branch = fields[0], fields[1], fields[2]
	} else if suffix, ok := strings.CutPrefix(revision, "refs/remotes/"); ok {
		var found bool
		remote, branch, found = strings.Cut(suffix, "/")
		if !found || remote == "" || branch == "" {
			return "", &ValidationError{Reason: "configured remote source is invalid"}
		}
		branch = "refs/heads/" + branch
	}
	if remote == "" || remote == "." {
		return revision, nil
	}
	if strings.HasPrefix(remote, "-") || !strings.HasPrefix(branch, "refs/heads/") {
		return "", &ValidationError{Reason: "configured remote source is invalid"}
	}
	// Pin the remote observation before fetching objects. No shared ref or
	// FETCH_HEAD is written, so simultaneous fresh starts cannot race a ref
	// lock or rewrite an operator's branch through a custom fetch mapping.
	observed, err := run("ls-remote", "--exit-code", "--refs", remote, branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(observed))
	if len(fields) != 2 || fields[1] != branch {
		return "", &ValidationError{Reason: "configured remote source was not observed exactly"}
	}
	commit, err := hex.DecodeString(fields[0])
	if err != nil || (len(commit) != 20 && len(commit) != 32) {
		return "", &ValidationError{Reason: "configured remote source commit is invalid"}
	}
	_, err = run("-c", "core.hooksPath=/dev/null", "fetch", "--quiet", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", "--no-auto-maintenance", "--refmap=", remote, fields[0])
	return fields[0], err
}

func validateRevision(revision string) error {
	if revision == "" || len(revision) > maxRevisionBytes || !utf8.ValidString(revision) || strings.IndexByte(revision, 0) >= 0 {
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
	rootFD, rootStat, err := openRepositoryRoot(path, expected)
	if err != nil {
		return repositoryCheckpoint{}, err
	}
	defer unix.Close(rootFD)
	root := expected
	gitFD, gitIdentity, err := openGitAdminDirectory(rootFD, ".git")
	if err != nil {
		return repositoryCheckpoint{}, &ValidationError{Reason: "only a primary non-bare Git worktree is supported"}
	}
	defer unix.Close(gitFD)
	if gitIdentity.device != uint64(rootStat.Dev) || gitIdentity.uid != rootStat.Uid {
		return repositoryCheckpoint{}, &ValidationError{Reason: "Git administration must remain on the repository filesystem"}
	}
	if err := rejectDirectGitAdminEntry(gitFD, "config.worktree"); err != nil {
		return repositoryCheckpoint{}, err
	}
	configIdentity, config, err := readGitAdminFile(gitFD, "config", maxGitConfigBytes)
	if err != nil {
		return repositoryCheckpoint{}, &ValidationError{Reason: "Git config must be one bounded regular file"}
	}
	if configIdentity.device != uint64(rootStat.Dev) || configIdentity.uid != rootStat.Uid {
		return repositoryCheckpoint{}, &ValidationError{Reason: "Git config authority differs from the repository"}
	}
	if !validLocalGitConfig(config) {
		return repositoryCheckpoint{}, &ValidationError{Reason: "Git config syntax or authority is unsupported"}
	}
	objectsFD, objectsIdentity, err := openGitAdminDirectory(gitFD, "objects")
	if err != nil {
		return repositoryCheckpoint{}, &ValidationError{Reason: "Git object directory must be exact and local"}
	}
	defer unix.Close(objectsFD)
	if objectsIdentity.device != uint64(rootStat.Dev) || objectsIdentity.uid != rootStat.Uid {
		return repositoryCheckpoint{}, &ValidationError{Reason: "Git object store must remain on the repository filesystem"}
	}
	if err := rejectGitAdminEntry(gitFD, "info", "grafts"); err != nil {
		return repositoryCheckpoint{}, err
	}
	if err := rejectDirectGitAdminEntry(gitFD, "commondir"); err != nil {
		return repositoryCheckpoint{}, err
	}
	for _, name := range []string{"alternates", "http-alternates"} {
		if err := rejectGitAdminEntry(objectsFD, "info", name); err != nil {
			return repositoryCheckpoint{}, err
		}
	}
	return repositoryCheckpoint{
		root: root, git: gitIdentity, config: configIdentity, objects: objectsIdentity,
	}, nil
}

// openRepositoryRoot verifies only the durable repository-root authority. It
// deliberately does not inspect Git administration, so retained Change retry
// can proceed when the live .git tree or Git executable is unavailable.
func openRepositoryRoot(path string, expected RepositoryIdentity) (int, unix.Stat_t, error) {
	if !expected.valid() || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, unix.Stat_t{}, &ValidationError{Reason: "repository root and identity must be canonical"}
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return -1, unix.Stat_t{}, &ValidationError{Reason: "repository root contains a symlink or alias"}
	}
	rootFD, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, unix.Stat_t{}, newGitError(gitFailurePrivateIO)
	}
	rootStat, err := fstatGit(rootFD)
	if err != nil || rootStat.Uid != uint32(unix.Geteuid()) || !safeGitMode(rootStat.Mode, gitModeDirectory) {
		_ = unix.Close(rootFD)
		return -1, unix.Stat_t{}, &ValidationError{Reason: "repository root must be an owned safe directory"}
	}
	root, err := NewRepositoryIdentity(uint64(rootStat.Dev), rootStat.Ino)
	if err != nil || root != expected {
		_ = unix.Close(rootFD)
		return -1, unix.Stat_t{}, &ValidationError{Reason: "repository root identity differs"}
	}
	return rootFD, rootStat, nil
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
	if err != nil || !safeGitMode(stat.Mode, gitModeDirectory) || stat.Ino == 0 {
		unix.Close(fd)
		return -1, gitAdminIdentity{}, errors.New("invalid Git admin directory")
	}
	return fd, gitAdminIdentity{device: uint64(stat.Dev), inode: stat.Ino, uid: stat.Uid, mode: uint32(stat.Mode)}, nil
}

func readGitAdminFile(parentFD int, name string, maximum int64) (gitAdminIdentity, []byte, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return gitAdminIdentity{}, nil, err
	}
	file := os.NewFile(uintptr(fd), "")
	defer file.Close()
	before, err := fstatGit(fd)
	if err != nil || !safeGitMode(before.Mode, gitModeFile) || before.Size < 0 || before.Size > maximum || before.Ino == 0 ||
		before.Nlink != 1 {
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

type gitModeKind byte

const (
	gitModeDirectory gitModeKind = iota + 1
	gitModeFile
	gitModeExecutable
)

// safeGitMode is the single mode authority for repository inputs. Nothing Git
// may consume can be group/world writable or carry special permission bits.
func safeGitMode(mode uint16, kind gitModeKind) bool {
	if mode&0o022 != 0 || mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return false
	}
	switch kind {
	case gitModeDirectory:
		return mode&unix.S_IFMT == unix.S_IFDIR
	case gitModeFile:
		return mode&unix.S_IFMT == unix.S_IFREG
	case gitModeExecutable:
		return mode&unix.S_IFMT == unix.S_IFREG && mode&0o111 != 0
	default:
		return false
	}
}

func validLocalGitConfig(config []byte) bool {
	for index, character := range config {
		if character >= utf8.RuneSelf || character == 0x7f || character == 0 || character < 0x20 && character != '\t' && character != '\n' && character != '\r' {
			return false
		}
		if character == '\r' && (index+1 == len(config) || config[index+1] != '\n') {
			return false
		}
	}
	section := ""
	for _, rawLine := range bytes.Split(config, []byte{'\n'}) {
		rawLine = bytes.TrimSuffix(rawLine, []byte{'\r'})
		line := strings.TrimSpace(string(rawLine))
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			if len(line) < 3 || line[len(line)-1] != ']' {
				return false
			}
			fields := strings.Fields(strings.TrimSpace(line[1 : len(line)-1]))
			if len(fields) == 0 {
				return false
			}
			section = strings.ToLower(fields[0])
			if section == "include" || strings.HasPrefix(section, "include.") || section == "includeif" || strings.HasPrefix(section, "includeif.") {
				return false
			}
			continue
		}
		if section == "" {
			return false
		}
		key, _, _ := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" || key == "include" || strings.HasPrefix(key, "include.") || key == "includeif" || strings.HasPrefix(key, "includeif.") {
			return false
		}
		for _, character := range key {
			if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-') {
				return false
			}
		}
	}
	return true
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
		left.Nlink == right.Nlink && left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func repositoryIdentityOf(info os.FileInfo) (RepositoryIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return RepositoryIdentity{}, errors.New("repository stat identity is unavailable")
	}
	return NewRepositoryIdentity(uint64(stat.Dev), stat.Ino)
}

func checkpointGitExecutable(path string) (gitFileIdentity, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "git" || path == "/usr/bin/git" {
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
	return inspectGitExecutable(file, false)
}

func checkpointTrustedGitExecutable(path string) (gitFileIdentity, error) {
	if !TrustedDeveloperGitPath(path) {
		return gitFileIdentity{}, &ValidationError{Reason: "Git executable is outside the trusted Developer toolchain"}
	}
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	directoryFD, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return gitFileIdentity{}, newGitError(gitFailurePrivateIO)
	}
	defer func() { _ = unix.Close(directoryFD) }()
	root, err := fstatGit(directoryFD)
	if err != nil || root.Uid != 0 || !safeGitMode(root.Mode, gitModeDirectory) {
		return gitFileIdentity{}, &ValidationError{Reason: "Git Developer toolchain authority is unsafe"}
	}
	for _, component := range components[:len(components)-1] {
		nextFD, openErr := unix.Openat(directoryFD, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return gitFileIdentity{}, &ValidationError{Reason: "Git Developer toolchain path is not exact"}
		}
		stat, statErr := fstatGit(nextFD)
		if statErr != nil || stat.Uid != 0 || stat.Ino == 0 || !safeGitMode(stat.Mode, gitModeDirectory) {
			_ = unix.Close(nextFD)
			return gitFileIdentity{}, &ValidationError{Reason: "Git Developer toolchain authority is unsafe"}
		}
		_ = unix.Close(directoryFD)
		directoryFD = nextFD
	}
	fd, err := unix.Openat(directoryFD, components[len(components)-1], unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return gitFileIdentity{}, &ValidationError{Reason: "Git executable is not exact"}
	}
	file := os.NewFile(uintptr(fd), "")
	defer file.Close()
	return inspectGitExecutable(file, true)
}

func inspectGitExecutable(file *os.File, trusted bool) (gitFileIdentity, error) {
	fd := int(file.Fd())
	before, err := fstatGit(fd)
	if err != nil || !safeGitMode(before.Mode, gitModeExecutable) ||
		(trusted && before.Uid != 0) || (!trusted && before.Uid != 0 && before.Uid != uint32(unix.Geteuid())) ||
		before.Size <= 0 || before.Size > maxGitExecutableBytes || before.Ino == 0 || before.Nlink != 1 {
		return gitFileIdentity{}, &ValidationError{Reason: "Git executable metadata is unsafe"}
	}
	if !isNativeGitMachO(file) {
		return gitFileIdentity{}, &ValidationError{Reason: "Git executable is not a native binary for this host"}
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
		trusted: trusted,
		device:  uint64(before.Dev), inode: before.Ino, uid: before.Uid, mode: uint32(before.Mode), size: before.Size,
		modifiedNS: before.Mtim.Sec*1e9 + before.Mtim.Nsec,
		changedNS:  before.Ctim.Sec*1e9 + before.Ctim.Nsec, digest: digest,
	}, nil
}

func checkpointExpectedGitExecutable(path string, trusted bool) (gitFileIdentity, error) {
	if trusted {
		return checkpointTrustedGitExecutable(path)
	}
	return checkpointGitExecutable(path)
}

func isNativeGitMachO(file *os.File) bool {
	wanted := macho.CpuArm64
	if runtime.GOARCH == "amd64" {
		wanted = macho.CpuAmd64
	} else if runtime.GOARCH != "arm64" {
		return false
	}
	fat, err := macho.NewFatFile(file)
	if err == nil {
		for _, architecture := range fat.Arches {
			if architecture.Cpu == wanted && architecture.Type == macho.TypeExec {
				return true
			}
		}
		return false
	}
	if !errors.Is(err, macho.ErrNotFat) {
		return false
	}
	thin, err := macho.NewFile(file)
	return err == nil && thin.Cpu == wanted && thin.Type == macho.TypeExec
}

func verifyGitAuthority(repository string, expected repositoryCheckpoint, executable string, executableIdentity gitFileIdentity) error {
	if err := verifyRepository(repository, expected); err != nil {
		return err
	}
	actual, err := checkpointExpectedGitExecutable(executable, executableIdentity.trusted)
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
	command.Env = append(gitEnvironment(s.home, s.repository), s.environment...)
	return command
}

type gitChild struct {
	command *exec.Cmd
	pid     int
	pgid    int
	kq      int
	exit    <-chan error
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

func startGitChild(spec gitCommandSpec) (*gitChild, error) {
	command := spec.command()
	pgid := unix.Getpgrp()
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: pgid}
	kq, err := unix.Kqueue()
	if err != nil {
		return nil, newGitError(gitFailureProcess)
	}
	unix.CloseOnExec(kq)
	stdout, childStdout, err := os.Pipe()
	if err != nil {
		unix.Close(kq)
		return nil, newGitError(gitFailurePrivateIO)
	}
	stderr, childStderr, err := os.Pipe()
	if err != nil {
		closeGitFiles(stdout, childStdout)
		unix.Close(kq)
		return nil, newGitError(gitFailurePrivateIO)
	}
	command.Stdout, command.Stderr = childStdout, childStderr
	if err := command.Start(); err != nil {
		closeGitFiles(stdout, childStdout, stderr, childStderr)
		unix.Close(kq)
		return nil, newGitError(gitFailureProcess)
	}
	if spec.hook != nil {
		spec.hook(gitProcessStarted)
	}
	child := &gitChild{
		command: command, pid: command.Process.Pid, pgid: pgid, kq: kq,
		stdout: stdout, stderr: stderr, hook: spec.hook, groupOK: true,
	}
	if closeGitFiles(childStdout, childStderr) != nil {
		return nil, child.failStart()
	}
	exit := make(chan error, 1)
	child.exit = exit
	switch err := registerGitExit(kq, child.pid); {
	case err == nil:
		go func() { exit <- waitGitExit(kq, child.pid) }()
	case errors.Is(err, unix.ESRCH):
		exit <- nil
	default:
		return nil, child.failStart()
	}
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
	closeGitFiles(c.stdout, c.stderr)
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
	if err != nil {
		return err
	}
	if n != 1 || receipts[0].Flags&unix.EV_ERROR == 0 {
		return errors.New("Git NOTE_EXIT registration failed")
	}
	if receipts[0].Data != 0 {
		return unix.Errno(receipts[0].Data)
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

func runGitCapture(ctx context.Context, spec gitCommandSpec, maximum int) (gitCapture, error) {
	if err := ctx.Err(); err != nil {
		return gitCapture{}, newGitContextError(err, false)
	}
	child, err := startGitChild(spec)
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
	data, err := io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
	if len(data) > maximum {
		return gitStreamResult{overflow: true}
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return gitStreamResult{err: err}
	}
	return gitStreamResult{data: data}
}

func isExitError(err error) bool {
	var exitError *exec.ExitError
	return errors.As(err, &exitError)
}

// gitAuthority is the repository and executable identity every Git
// operation on a Change is checked against before and after each process.
type gitAuthority struct {
	repositoryRoot string
	repository     repositoryCheckpoint
	gitExecutable  string
	gitIdentity    gitFileIdentity
	home           string
	hook           gitProcessHook
}

func openGitAuthority(gitExecutable, repositoryRoot string, expected RepositoryIdentity, hook gitProcessHook, trusted bool) (*gitAuthority, error) {
	repository, err := checkpointRepository(repositoryRoot, expected)
	if err != nil {
		return nil, err
	}
	gitIdentity, err := checkpointExpectedGitExecutable(gitExecutable, trusted)
	if err != nil {
		return nil, err
	}
	home, err := newGitHome()
	if err != nil {
		return nil, err
	}
	return &gitAuthority{repositoryRoot: repositoryRoot, repository: repository, gitExecutable: gitExecutable, gitIdentity: gitIdentity, home: home, hook: hook}, nil
}

func (a *gitAuthority) close() { _ = cleanupGitHome(a.home) }

// run runs one Git command in the repository and rechecks the repository
// and executable identity after it exits, so a replaced repository or Git
// cannot pass an earlier check.
func (a *gitAuthority) run(ctx context.Context, maximum int, arguments ...string) (gitCapture, error) {
	return a.runWithEnvironment(ctx, maximum, nil, arguments...)
}

func (a *gitAuthority) runWithEnvironment(ctx context.Context, maximum int, environment []string, arguments ...string) (gitCapture, error) {
	if err := verifyGitAuthority(a.repositoryRoot, a.repository, a.gitExecutable, a.gitIdentity); err != nil {
		return gitCapture{}, err
	}
	spec := gitCommandSpec{program: a.gitExecutable, repository: a.repositoryRoot, home: a.home, hook: a.hook, arguments: arguments, environment: environment}
	result, err := runGitCapture(ctx, spec, maximum)
	if err != nil {
		return gitCapture{}, err
	}
	if err := verifyGitAuthority(a.repositoryRoot, a.repository, a.gitExecutable, a.gitIdentity); err != nil {
		return gitCapture{}, err
	}
	return result, nil
}

// succeed runs one Git command that must exit zero.
func (a *gitAuthority) succeed(ctx context.Context, maximum int, arguments ...string) ([]byte, error) {
	result, err := a.run(ctx, maximum, arguments...)
	if err != nil {
		return nil, err
	}
	if result.exitCode != 0 {
		return nil, newGitError(gitFailureProcess)
	}
	return result.output, nil
}

// rewriteConfig runs one Git command that must exit zero and may rewrite
// the local config file in place.
func (a *gitAuthority) rewriteConfig(ctx context.Context, maximum int, arguments ...string) error {
	spec := gitCommandSpec{program: a.gitExecutable, repository: a.repositoryRoot, home: a.home, hook: a.hook, arguments: arguments}
	result, err := runGitCapture(ctx, spec, maximum)
	if err != nil {
		return err
	}
	if err := a.refresh(); err != nil {
		return err
	}
	if result.exitCode != 0 {
		return newGitError(gitFailureProcess)
	}
	return nil
}

// AddWorktree makes the Change's worktree: one linked worktree of the
// selected repository at path, on its own branch, checked out at the
// selected base. A leftover of an earlier attempt that never reached a
// provider (the same branch still at the base, at this path or registered
// for it) is removed and remade; anything else at the path or on the branch
// is refused, never replaced.
func AddWorktree(ctx context.Context, selection Selection, path, branch string) (WorktreeFacts, error) {
	return addWorktree(ctx, selection, path, branch, nil, true)
}

// GitDirectoryForChange returns the deterministic private administration for
// a Change worktree. It lives below the project's Git directory, not below
// the worker-readable worktree parent.
func GitDirectoryForChange(repositoryRoot, path string) string {
	return filepath.Join(repositoryRoot, ".git", "dark-factory-changes", filepath.Base(path), ".git")
}

func preparePrivateGitParent(repositoryRoot, path string) error {
	parent := filepath.Dir(GitDirectoryForChange(repositoryRoot, path))
	for _, directory := range []string{filepath.Dir(parent), parent} {
		if err := os.Mkdir(directory, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
		fd, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
		if err != nil {
			return &ValidationError{Reason: "private Git parent is not local"}
		}
		stat, statErr := fstatGit(fd)
		_ = unix.Close(fd)
		if statErr != nil || stat.Uid != uint32(os.Geteuid()) || !safeGitMode(stat.Mode, gitModeDirectory) {
			return &ValidationError{Reason: "private Git parent is unsafe"}
		}
	}
	return nil
}

func validatePrivateGitAdmin(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return &ValidationError{Reason: "private Change Git administration contains a symlink"}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return &ValidationError{Reason: "private Change Git administration is unavailable"}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(unix.Geteuid()) || !safeGitMode(uint16(stat.Mode), gitModeDirectory) {
		return &ValidationError{Reason: "private Change Git administration is unsafe"}
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &ValidationError{Reason: "private Change Git administration is unavailable"}
	}
	defer unix.Close(fd)
	_, config, readErr := readGitAdminFile(fd, "config", maxGitConfigBytes)
	if readErr != nil || !validLocalGitConfig(config) {
		return &ValidationError{Reason: "private Change Git config is unsafe"}
	}
	// A worker can edit its config. Daemon reads must never execute its
	// filters, monitors, helpers, hooks, or storage extensions.
	section := ""
	for _, raw := range strings.Split(strings.ToLower(string(config)), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		key, _, _ := strings.Cut(line, "=")
		switch section + "." + strings.TrimSpace(key) {
		case "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.ignorecase", "core.precomposeunicode", "core.symlinks", "core.quotepath", "core.autocrlf", "core.eol", "core.safecrlf", "extensions.objectformat", "user.name", "user.email":
		default:
			return &ValidationError{Reason: "private Change Git config grants unsupported authority"}
		}
	}
	if err := rejectDirectGitAdminEntry(fd, "commondir"); err != nil {
		return err
	}
	if err := rejectDirectGitAdminEntry(fd, "config.worktree"); err != nil {
		return err
	}
	objectsFD, _, err := openGitAdminDirectory(fd, "objects")
	if err != nil {
		return &ValidationError{Reason: "private Change Git objects are unavailable"}
	}
	defer unix.Close(objectsFD)
	for _, name := range []string{"alternates", "http-alternates"} {
		if err := rejectGitAdminEntry(objectsFD, "info", name); err != nil {
			return err
		}
	}
	if err := rejectGitAdminEntry(fd, "info", "grafts"); err != nil {
		return err
	}
	if err := rejectGitAdminEntry(fd, "refs", "replace"); err != nil {
		return err
	}
	return nil
}

func privateGitfileTarget(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", &ValidationError{Reason: "private Change Gitfile is not regular"}
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", newGitError(gitFailurePrivateIO)
	}
	file := os.NewFile(uintptr(fd), "")
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return "", newGitError(gitFailurePrivateIO)
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir: ") || strings.ContainsAny(line, "\r\n") {
		return "", &ValidationError{Reason: "private Change Gitfile syntax is invalid"}
	}
	target := strings.TrimPrefix(line, "gitdir: ")
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return "", &ValidationError{Reason: "private Change Gitfile target is not canonical"}
	}
	return target, nil
}

// AddPrivateWorktree creates a linked worktree backed by a private bare Git
// administration without changing the project's refs or worktree registry.
func AddPrivateWorktree(ctx context.Context, selection Selection, path, branch string) (WorktreeFacts, error) {
	if !selection.valid() || !validWorktreeBranch(branch) {
		return WorktreeFacts{}, &ValidationError{Reason: "worktree selection or branch is invalid"}
	}
	if err := validateWorktreePath(path); err != nil {
		return WorktreeFacts{}, err
	}
	authority, err := openGitAuthority(selection.gitExecutable, selection.repositoryRoot, selection.repository.root, nil, true)
	if err != nil {
		return WorktreeFacts{}, err
	}
	defer authority.close()
	if _, err := os.Lstat(path); err == nil {
		facts, inspectErr := authority.inspectWorktree(ctx, path)
		if inspectErr == nil && facts.GitDirectory() == GitDirectoryForChange(selection.repositoryRoot, path) && facts.Branch() == branch && facts.Head().equal(selection.base) && !facts.Dirty() {
			return facts, nil
		}
		return WorktreeFacts{}, &ValidationError{Reason: "private Change path is already taken"}
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorktreeFacts{}, newGitError(gitFailurePrivateIO)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return WorktreeFacts{}, newGitError(gitFailurePrivateIO)
	}
	admin := GitDirectoryForChange(selection.repositoryRoot, path)
	if filepath.Base(path) == "." || filepath.Base(path) == ".." {
		return WorktreeFacts{}, &ValidationError{Reason: "private Change path has no stable identity"}
	}
	if info, err := os.Lstat(admin); err == nil && !info.IsDir() {
		return WorktreeFacts{}, &ValidationError{Reason: "private Change Git administration is not a directory"}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return WorktreeFacts{}, newGitError(gitFailurePrivateIO)
	}
	if err := preparePrivateGitParent(selection.repositoryRoot, path); err != nil {
		return WorktreeFacts{}, err
	}
	if _, err := os.Stat(filepath.Join(admin, "config")); err == nil {
		if err := validatePrivateGitAdmin(admin); err != nil {
			return WorktreeFacts{}, err
		}
		if err := authority.removeUnusedPrivateWorktree(ctx, admin, path, branch, selection.base); err != nil {
			return WorktreeFacts{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorktreeFacts{}, newGitError(gitFailurePrivateIO)
	}
	if _, err := authority.succeed(ctx, maxGitSelectionOutput, "init", "--bare", "--object-format="+selection.format.Name(), admin); err != nil {
		return WorktreeFacts{}, err
	}
	if err := validatePrivateGitAdmin(admin); err != nil {
		return WorktreeFacts{}, err
	}
	if _, err := authority.succeed(ctx, maxGitSelectionOutput, "-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=always", "--git-dir", admin, "fetch", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance", "--no-recurse-submodules", selection.repositoryRoot, selection.base.Hex()); err != nil {
		return WorktreeFacts{}, newGitError(gitFailureProcess)
	}
	if err := validatePrivateGitAdmin(admin); err != nil {
		return WorktreeFacts{}, err
	}
	worktreeArgs := []string{"-c", "core.hooksPath=/dev/null", "--git-dir", admin, "worktree", "add", "--quiet"}
	branchTip, branchErr := authority.run(ctx, maxGitSelectionOutput, "-c", "core.hooksPath=/dev/null", "--git-dir", admin, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if branchErr != nil {
		return WorktreeFacts{}, branchErr
	}
	if branchTip.exitCode == 0 {
		tip, parseErr := parseGitOID(selection.format, bytes.TrimSpace(branchTip.output))
		if parseErr != nil || !tip.equal(selection.base) {
			return WorktreeFacts{}, &ValidationError{Reason: "private Change branch already exists at another commit"}
		}
		worktreeArgs = append(worktreeArgs, path, branch)
	} else {
		worktreeArgs = append(worktreeArgs, "-b", branch, path, selection.base.Hex())
	}
	if _, err := authority.succeed(ctx, maxGitSelectionOutput, worktreeArgs...); err != nil {
		return WorktreeFacts{}, newGitError(gitFailureProcess)
	}
	return inspectWorktree(ctx, selection.gitExecutable, selection.repositoryRoot, selection.repository.root, path, nil, true)
}

func addWorktree(ctx context.Context, selection Selection, path, branch string, hook gitProcessHook, trusted bool) (WorktreeFacts, error) {
	if !selection.valid() || !validWorktreeBranch(branch) {
		return WorktreeFacts{}, &ValidationError{Reason: "worktree selection or branch is invalid"}
	}
	if err := validateWorktreePath(path); err != nil {
		return WorktreeFacts{}, err
	}
	authority, err := openGitAuthority(selection.gitExecutable, selection.repositoryRoot, selection.repository.root, hook, trusted)
	if err != nil {
		return WorktreeFacts{}, err
	}
	defer authority.close()
	if err := authority.removeUnusedWorktree(ctx, path, branch, selection.base); err != nil {
		return WorktreeFacts{}, err
	}
	if _, err := os.Lstat(path); err == nil {
		return WorktreeFacts{}, &ValidationError{Reason: "Change path is taken by something that is not its worktree"}
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorktreeFacts{}, newGitError(gitFailurePrivateIO)
	}
	if err := authority.addWorktree(ctx, path, branch, selection.base, false); err != nil {
		return WorktreeFacts{}, err
	}
	return authority.verifyWorktree(ctx, path, branch, selection.base)
}

// InspectWorktree verifies that path is a linked worktree of the repository
// and reports its head, branch and cleanliness.
func InspectWorktree(ctx context.Context, gitExecutable, repositoryRoot string, expected RepositoryIdentity, path string) (WorktreeFacts, error) {
	return inspectWorktree(ctx, gitExecutable, repositoryRoot, expected, path, nil, true)
}

func inspectWorktree(ctx context.Context, gitExecutable, repositoryRoot string, expected RepositoryIdentity, path string, hook gitProcessHook, trusted bool) (WorktreeFacts, error) {
	if err := validateWorktreePath(path); err != nil {
		return WorktreeFacts{}, err
	}
	authority, err := openGitAuthority(gitExecutable, repositoryRoot, expected, hook, trusted)
	if err != nil {
		return WorktreeFacts{}, err
	}
	defer authority.close()
	return authority.inspectWorktree(ctx, path)
}

// DescendsFrom reports whether the worktree's head descends from base: the
// Change's recorded base is an ancestor of the work on its branch.
func DescendsFrom(ctx context.Context, gitExecutable, repositoryRoot string, expected RepositoryIdentity, path string, base ObjectID) (bool, error) {
	if err := validateWorktreePath(path); err != nil {
		return false, err
	}
	if !base.format.valid() {
		return false, &ValidationError{Reason: "worktree base is invalid"}
	}
	authority, err := openGitAuthority(gitExecutable, repositoryRoot, expected, nil, true)
	if err != nil {
		return false, err
	}
	defer authority.close()
	if _, err := authority.inspectWorktree(ctx, path); err != nil {
		return false, err
	}
	result, err := authority.run(ctx, maxGitSelectionOutput, "-C", path, "merge-base", "--is-ancestor", base.Hex(), "HEAD")
	if err != nil {
		return false, err
	}
	switch result.exitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, newGitError(gitFailureProcess)
	}
}

// AdoptWorktree turns a Git-free tree at path, published before managed
// worktrees from the commit base, into the Change's worktree: the same
// files, now on the Change's own branch at base, with the worker's edits
// showing as uncommitted work. It is idempotent across a crash at any step
// and refuses a path that is already a worktree of something else.
func AdoptWorktree(ctx context.Context, gitExecutable, repositoryRoot string, expected RepositoryIdentity, path, branch string, base ObjectID) (WorktreeFacts, error) {
	return adoptWorktree(ctx, gitExecutable, repositoryRoot, expected, path, branch, base, nil, true)
}

func adoptWorktree(ctx context.Context, gitExecutable, repositoryRoot string, expected RepositoryIdentity, path, branch string, base ObjectID, hook gitProcessHook, trusted bool) (WorktreeFacts, error) {
	if !validWorktreeBranch(branch) || !base.format.valid() {
		return WorktreeFacts{}, &ValidationError{Reason: "worktree branch or base is invalid"}
	}
	if err := validateWorktreePath(path); err != nil {
		return WorktreeFacts{}, err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() {
		return WorktreeFacts{}, &ValidationError{Reason: "the Change tree to adopt is not a directory"}
	}
	authority, err := openGitAuthority(gitExecutable, repositoryRoot, expected, hook, trusted)
	if err != nil {
		return WorktreeFacts{}, err
	}
	defer authority.close()
	gitFile := filepath.Join(path, ".git")
	if _, err := os.Lstat(gitFile); err == nil {
		// Already adopted, or someone else's repository: the facts decide.
		return authority.verifyWorktree(ctx, path, branch, base)
	} else if !errors.Is(err, os.ErrNotExist) {
		return WorktreeFacts{}, newGitError(gitFailurePrivateIO)
	}
	// The gitfile is minted in a sibling directory with no checkout, then
	// moved into the tree, so the tree's files are never written by Git.
	stage := path + ".adopt"
	stageGitFile := filepath.Join(stage, ".git")
	if _, err := os.Lstat(stageGitFile); errors.Is(err, os.ErrNotExist) {
		if err := os.Remove(stage); err != nil && !errors.Is(err, os.ErrNotExist) {
			return WorktreeFacts{}, &ValidationError{Reason: "adoption staging directory is not empty"}
		}
		if err := authority.addWorktree(ctx, stage, branch, base, true); err != nil {
			return WorktreeFacts{}, err
		}
	} else if err != nil {
		return WorktreeFacts{}, newGitError(gitFailurePrivateIO)
	}
	if err := os.Rename(stageGitFile, gitFile); err != nil {
		return WorktreeFacts{}, newGitError(gitFailurePrivateIO)
	}
	if err := os.Remove(stage); err != nil && !errors.Is(err, os.ErrNotExist) {
		return WorktreeFacts{}, newGitError(gitFailurePrivateIO)
	}
	if _, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", repositoryRoot, "worktree", "repair", path); err != nil {
		return WorktreeFacts{}, err
	}
	// A mixed reset fills the index from HEAD and leaves every file alone.
	if _, err := authority.succeed(ctx, maxGitSelectionOutput, "-C", path, "reset", "-q", "--"); err != nil {
		return WorktreeFacts{}, err
	}
	return authority.verifyWorktree(ctx, path, branch, base)
}

// addWorktree runs git worktree add, creating the branch at base or checking
// out an existing branch that already sits at base; a branch at any other
// commit belongs to work this Change may not replace. Concurrent attempts
// in one repository contend for Git's own locks, so a refused add is
// retried a few times before it is a failure.
func (a *gitAuthority) addWorktree(ctx context.Context, path, branch string, base ObjectID, staging bool) error {
	arguments := []string{"-C", a.repositoryRoot, "-c", "core.hooksPath=/dev/null", "worktree", "add", "--quiet"}
	if staging {
		arguments = append(arguments, "--no-checkout", "--force")
	}
	tip, exists, err := a.branchTip(ctx, base.format, branch)
	if err != nil {
		return err
	}
	if exists {
		if !tip.equal(base) {
			return &ValidationError{Reason: "the Change branch already exists at another commit"}
		}
		arguments = append(arguments, path, branch)
	} else {
		arguments = append(arguments, "-b", branch, path, base.Hex())
	}
	// ponytail: fixed retry on any nonzero exit; a per-cause classification
	// would need Git's stderr, which stays private.
	for attempt := 0; ; attempt++ {
		result, err := a.run(ctx, maxGitSelectionOutput, arguments...)
		if err != nil {
			return err
		}
		if result.exitCode == 0 {
			return nil
		}
		if attempt == gitLockRetries {
			return newGitError(gitFailureProcess)
		}
		if _, err := os.Lstat(path); err == nil {
			return newGitError(gitFailureProcess)
		}
		select {
		case <-ctx.Done():
			return newGitContextError(ctx.Err(), false)
		case <-time.After(gitLockRetryDelay):
		}
	}
}

// removeUnusedWorktree removes a registration of path, or of the Change's
// branch, that an earlier attempt left before any provider ran: it must
// still be the Change's branch at the Change's base. Nothing else is touched.
func (a *gitAuthority) removeUnusedWorktree(ctx context.Context, path, branch string, base ObjectID) error {
	registrations, err := a.worktrees(ctx)
	if err != nil {
		return err
	}
	registered, ok := registrations[path]
	if !ok {
		for _, candidate := range registrations {
			if candidate.branch == "refs/heads/"+branch {
				registered, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		return nil
	}
	if registered.branch != "refs/heads/"+branch || registered.head != base.Hex() {
		return &ValidationError{Reason: "the Change path or branch is registered to a worktree that is not its unused one"}
	}
	if _, err := os.Lstat(registered.path); err == nil {
		facts, err := a.inspectWorktree(ctx, registered.path)
		if err != nil || !facts.head.equal(base) || facts.branch != branch || facts.dirty {
			return errors.Join(&ValidationError{Reason: "the Change path holds a worktree with work in it"}, err)
		}
	}
	if err := a.rewriteConfig(ctx, maxGitSelectionOutput, "-C", a.repositoryRoot, "worktree", "remove", "--force", registered.path); err != nil {
		return err
	}
	tip, exists, err := a.branchTip(ctx, base.format, branch)
	if err != nil {
		return err
	}
	if exists && tip.equal(base) {
		return a.rewriteConfig(ctx, maxGitSelectionOutput, "-C", a.repositoryRoot, "branch", "--quiet", "-D", branch)
	}
	return nil
}

// removeUnusedPrivateWorktree removes only the private registration left by a
// deleted pre-provider worktree. Its index must still equal HEAD, so staged
// work is never discarded; all refs and objects remain in the private admin.
func (a *gitAuthority) removeUnusedPrivateWorktree(ctx context.Context, admin, path, branch string, base ObjectID) error {
	// Git permits registration names that are unrelated to the worktree
	// basename. Only the deterministic name made by AddPrivateWorktree is
	// eligible for recovery; an alternate registration is left untouched and
	// the subsequent native add fails closed.
	registration := filepath.Join(admin, "worktrees", filepath.Base(path))
	if info, err := os.Lstat(registration); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil || !info.IsDir() {
		return &ValidationError{Reason: "private Change registration is unavailable"}
	}
	if err := validatePrivateWorktreeRegistration(registration, path, admin, &base); err != nil {
		return err
	}
	registrations, err := a.privateWorktrees(ctx, admin)
	if err != nil {
		return err
	}
	registered, ok := registrations[path]
	for candidatePath, candidate := range registrations {
		if candidate.branch == "refs/heads/"+branch && candidatePath != path {
			return &ValidationError{Reason: "private Change branch is registered to another worktree"}
		}
	}
	if !ok {
		return &ValidationError{Reason: "private Change registration is not recognized by Git"}
	}
	if registered.branch != "refs/heads/"+branch || registered.head != base.Hex() {
		return &ValidationError{Reason: "private Change registration is not its unused one"}
	}
	if registered.path != path {
		return &ValidationError{Reason: "private Change registration path is not exact"}
	}
	index, err := a.run(ctx, maxGitSelectionOutput, "--git-dir", registration, "diff", "--cached", "--quiet", "--no-ext-diff", "--no-textconv", base.Hex(), "--")
	if err != nil {
		return err
	}
	if index.exitCode == 1 {
		return &ValidationError{Reason: "private Change registration has staged work"}
	}
	if index.exitCode != 0 {
		return newGitError(gitFailureProcess)
	}
	// Do not remove a path that appeared after the initial admission check.
	if _, err := os.Lstat(path); err == nil {
		return &ValidationError{Reason: "private Change path was recreated during recovery"}
	} else if !errors.Is(err, os.ErrNotExist) {
		return newGitError(gitFailurePrivateIO)
	}
	if _, err := a.succeed(ctx, maxGitSelectionOutput, "--git-dir", admin, "worktree", "remove", "--force", path); err != nil {
		return err
	}
	return nil
}

// validatePrivateWorktreeRegistration proves the exact worker-editable
// registration before Git is allowed to interpret it. Unknown entries are
// refused because Git operation state in this directory must not be silently
// discarded by worktree remove --force.
func validatePrivateWorktreeRegistration(registration, path, admin string, expectedBase *ObjectID) error {
	adminFD, err := unix.Open(admin, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &ValidationError{Reason: "private Change Git administration is unavailable"}
	}
	defer unix.Close(adminFD)
	worktreesFD, _, err := openGitAdminDirectory(adminFD, "worktrees")
	if err != nil {
		return &ValidationError{Reason: "private Change registrations are unavailable"}
	}
	defer unix.Close(worktreesFD)
	regFD, err := unix.Openat(worktreesFD, filepath.Base(registration), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &ValidationError{Reason: "private Change registration is unavailable"}
	}
	regFile := os.NewFile(uintptr(regFD), "")
	defer regFile.Close()
	regStat, err := fstatGit(regFD)
	if err != nil || regStat.Uid != uint32(unix.Geteuid()) || !safeGitMode(regStat.Mode, gitModeDirectory) {
		return &ValidationError{Reason: "private Change registration is unsafe"}
	}
	if _, _, err := readGitAdminFile(regFD, "HEAD", maxGitSelectionOutput); err != nil {
		return &ValidationError{Reason: "private Change registration HEAD is unsafe"}
	}
	_, gitdir, err := readGitAdminFile(regFD, "gitdir", maxGitSelectionOutput)
	if err != nil || strings.TrimSpace(string(gitdir)) != filepath.Join(path, ".git") {
		return &ValidationError{Reason: "private Change registration points elsewhere"}
	}
	relativeAdmin, relErr := filepath.Rel(registration, admin)
	if relErr != nil {
		return &ValidationError{Reason: "private Change common directory redirects"}
	}
	_, commondir, err := readGitAdminFile(regFD, "commondir", maxGitSelectionOutput)
	if err != nil || strings.TrimSpace(string(commondir)) != relativeAdmin {
		return &ValidationError{Reason: "private Change common directory redirects"}
	}
	// Reading the index with O_NOFOLLOW both rejects a missing index and
	// prevents a worker-created alias from becoming Git input.
	if _, _, err := readGitAdminFile(regFD, "index", maxGitListOutput); err != nil {
		return &ValidationError{Reason: "private Change registration index is unsafe"}
	}
	if err := rejectDirectGitAdminEntry(regFD, "config.worktree"); err != nil {
		return err
	}
	if expectedBase == nil {
		return nil // Inspection preserves split-index and in-progress operation state.
	}
	entries, err := regFile.Readdirnames(-1)
	if err != nil {
		return &ValidationError{Reason: "private Change registration cannot be inspected"}
	}
	for _, name := range entries {
		switch name {
		case "HEAD", "commondir", "gitdir", "index", "logs":
		case "ORIG_HEAD":
			_, data, err := readGitAdminFile(regFD, name, maxGitSelectionOutput)
			if err != nil || strings.TrimSpace(string(data)) != expectedBase.Hex() {
				return &ValidationError{Reason: "private Change registration retains another original head"}
			}
		case "refs":
			refsFD, _, err := openGitAdminDirectory(regFD, name)
			if err != nil {
				return err
			}
			refs := os.NewFile(uintptr(refsFD), "")
			names, readErr := refs.Readdirnames(-1)
			closeErr := refs.Close()
			if readErr != nil || closeErr != nil || len(names) != 0 {
				return &ValidationError{Reason: "private Change registration retains worktree refs"}
			}
		default:
			return &ValidationError{Reason: "private Change registration contains unknown state"}
		}
	}
	logsFD, _, logsErr := openGitAdminDirectory(regFD, "logs")
	if errors.Is(logsErr, unix.ENOENT) {
		return nil
	}
	if logsErr != nil {
		return &ValidationError{Reason: "private Change registration logs are unsafe"}
	}
	logs := os.NewFile(uintptr(logsFD), "")
	defer logs.Close()
	logEntries, err := logs.Readdirnames(-1)
	if err != nil {
		return &ValidationError{Reason: "private Change registration logs cannot be inspected"}
	}
	for _, name := range logEntries {
		if name != "HEAD" {
			return &ValidationError{Reason: "private Change registration contains unknown log state"}
		}
		_, logData, err := readGitAdminFile(logsFD, name, maxGitListOutput)
		if err != nil || expectedBase != nil && !validPrivateWorktreeReflog(logData, *expectedBase) {
			return &ValidationError{Reason: "private Change registration log is unsafe"}
		}
	}
	return nil
}

func validPrivateWorktreeReflog(data []byte, base ObjectID) bool {
	zero := strings.Repeat("0", len(base.Hex()))
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || (fields[0] != zero && fields[0] != base.Hex()) || (fields[1] != zero && fields[1] != base.Hex()) {
			return false
		}
	}
	return true
}

type worktreeRegistration struct {
	path, head, branch string
}

// worktrees parses git worktree list --porcelain into one record per path.
func (a *gitAuthority) worktrees(ctx context.Context) (map[string]worktreeRegistration, error) {
	output, err := a.succeed(ctx, maxGitListOutput, "-C", a.repositoryRoot, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(output), nil
}

func (a *gitAuthority) privateWorktrees(ctx context.Context, admin string) (map[string]worktreeRegistration, error) {
	output, err := a.succeed(ctx, maxGitListOutput, "--git-dir", admin, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktrees(output), nil
}

func parseWorktrees(output []byte) map[string]worktreeRegistration {
	result := make(map[string]worktreeRegistration)
	var current worktreeRegistration
	for _, line := range strings.Split(string(output), "\n") {
		switch {
		case strings.HasPrefix(line, "worktree "):
			current = worktreeRegistration{path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "HEAD "):
			current.head = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			current.branch = strings.TrimPrefix(line, "branch ")
		case line == "":
			if current.path != "" {
				result[current.path] = current
			}
			current = worktreeRegistration{}
		}
	}
	return result
}

func (a *gitAuthority) branchTip(ctx context.Context, format ObjectFormat, branch string) (ObjectID, bool, error) {
	result, err := a.run(ctx, maxGitSelectionOutput, "-C", a.repositoryRoot, "rev-parse", "--verify", "--quiet", "--end-of-options", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return ObjectID{}, false, err
	}
	if result.exitCode != 0 {
		return ObjectID{}, false, nil
	}
	tip, err := parseGitOID(format, bytes.TrimSpace(result.output))
	if err != nil {
		return ObjectID{}, false, err
	}
	return tip, true, nil
}

func (a *gitAuthority) verifyWorktree(ctx context.Context, path, branch string, base ObjectID) (WorktreeFacts, error) {
	facts, err := a.inspectWorktree(ctx, path)
	if err != nil {
		return WorktreeFacts{}, err
	}
	if facts.branch != branch || !facts.head.equal(base) {
		return WorktreeFacts{}, &ValidationError{Reason: fmt.Sprintf("the Change worktree is on %q at %s, not on %q at %s", facts.branch, facts.head.Hex(), branch, base.Hex())}
	}
	return facts, nil
}

// refresh re-reads the repository checkpoint after a Git command that
// rewrites the local config file in place (branch deletion, worktree
// removal): the same root, administration, object store and config bytes
// under a fresh config inode is Git's own rewrite, anything else is not.
func (a *gitAuthority) refresh() error {
	fresh, err := checkpointRepository(a.repositoryRoot, a.repository.root)
	if err != nil {
		return err
	}
	if fresh.root != a.repository.root || fresh.git != a.repository.git || fresh.objects != a.repository.objects || fresh.config.digest != a.repository.config.digest || fresh.config.size != a.repository.config.size || fresh.config.uid != a.repository.config.uid || fresh.config.mode != a.repository.config.mode {
		return &ValidationError{Reason: "Git administrative identity differs"}
	}
	a.repository = fresh
	return nil
}

// inspectWorktree proves path is a linked worktree of this repository, then
// reads its head, branch and cleanliness.
func (a *gitAuthority) inspectWorktree(ctx context.Context, path string) (WorktreeFacts, error) {
	gitEntry, err := os.Lstat(filepath.Join(path, ".git"))
	if err != nil || !gitEntry.Mode().IsRegular() {
		return WorktreeFacts{}, &ValidationError{Reason: "the Change path is not a Git worktree"}
	}
	gitfileTarget := ""
	if gitEntry.Mode().IsRegular() {
		gitfileTarget, err = privateGitfileTarget(filepath.Join(path, ".git"))
		if err != nil {
			return WorktreeFacts{}, err
		}
		private := GitDirectoryForChange(a.repositoryRoot, path)
		canonical := filepath.Join(a.repositoryRoot, ".git")
		canonicalWorktree := filepath.Join(canonical, "worktrees")
		privateWorktree := filepath.Join(private, "worktrees")
		if filepath.Dir(gitfileTarget) != canonicalWorktree && filepath.Dir(gitfileTarget) != privateWorktree {
			return WorktreeFacts{}, &ValidationError{Reason: "the Change Gitfile target is not authorized"}
		}
		if filepath.Dir(gitfileTarget) == privateWorktree {
			if err := validatePrivateGitAdmin(private); err != nil {
				return WorktreeFacts{}, err
			}
		}
		resolvedTarget, targetErr := filepath.EvalSymlinks(gitfileTarget)
		if targetErr != nil || resolvedTarget != gitfileTarget {
			return WorktreeFacts{}, &ValidationError{Reason: "the Change Gitfile registration is not local"}
		}
		if filepath.Dir(gitfileTarget) == privateWorktree {
			if err := validatePrivateWorktreeRegistration(gitfileTarget, path, private, nil); err != nil {
				return WorktreeFacts{}, err
			}
		} else {
			registrationFD, openErr := unix.Open(gitfileTarget, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW_ANY|unix.O_CLOEXEC, 0)
			if openErr != nil {
				return WorktreeFacts{}, &ValidationError{Reason: "the Change Gitfile registration is unsafe"}
			}
			defer unix.Close(registrationFD)
			_, data, readErr := readGitAdminFile(registrationFD, "gitdir", 4096)
			if readErr != nil || strings.TrimSpace(string(data)) != filepath.Join(path, ".git") {
				return WorktreeFacts{}, &ValidationError{Reason: "the Change Gitfile registration points elsewhere"}
			}
			if err := rejectDirectGitAdminEntry(registrationFD, "config.worktree"); err != nil {
				return WorktreeFacts{}, err
			}
			if _, commonData, commonErr := readGitAdminFile(registrationFD, "commondir", maxGitSelectionOutput); commonErr == nil {
				commonPath := strings.TrimSpace(string(commonData))
				resolvedCommon, resolveErr := filepath.EvalSymlinks(filepath.Clean(filepath.Join(gitfileTarget, commonPath)))
				if resolveErr != nil || resolvedCommon != canonical {
					return WorktreeFacts{}, &ValidationError{Reason: "the Change Git common directory redirects"}
				}
			} else {
				return WorktreeFacts{}, &ValidationError{Reason: "the Change Git common directory is unsafe"}
			}
		}
	}
	layout, err := a.succeed(ctx, maxGitSelectionOutput, "-C", path, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--show-object-format")
	if err != nil {
		return WorktreeFacts{}, err
	}
	lines := strings.Split(strings.TrimSuffix(string(layout), "\n"), "\n")
	if len(lines) != 3 || lines[0] != path {
		return WorktreeFacts{}, &ValidationError{Reason: "the Change path is not a worktree of the project repository"}
	}
	common := filepath.Clean(lines[1])
	canonical := filepath.Join(a.repositoryRoot, ".git")
	private := GitDirectoryForChange(a.repositoryRoot, path)
	if common != canonical && common != private {
		return WorktreeFacts{}, &ValidationError{Reason: "the Change Git directory is not private"}
	}
	if common == private {
		if !gitEntry.Mode().IsRegular() || filepath.Dir(gitfileTarget) != filepath.Join(private, "worktrees") {
			return WorktreeFacts{}, &ValidationError{Reason: "the private Change must be a linked worktree"}
		}
		info, statErr := os.Lstat(common)
		statOK := false
		var stat *syscall.Stat_t
		if statErr == nil {
			stat, statOK = info.Sys().(*syscall.Stat_t)
		}
		if statErr != nil || !info.IsDir() || !statOK || stat.Uid != uint32(unix.Geteuid()) || info.Mode().Perm()&0o022 != 0 {
			return WorktreeFacts{}, &ValidationError{Reason: "the private Change Git directory is unsafe"}
		}
	}
	format, err := NewObjectFormat(lines[2])
	if err != nil {
		return WorktreeFacts{}, newGitError(gitFailureProtocol)
	}
	headOutput, err := a.succeed(ctx, maxGitSelectionOutput, "-C", path, "rev-parse", "--verify", "--end-of-options", "HEAD^{commit}")
	if err != nil {
		return WorktreeFacts{}, err
	}
	head, err := parseGitOID(format, bytes.TrimSpace(headOutput))
	if err != nil {
		return WorktreeFacts{}, err
	}
	symbolic, err := a.run(ctx, maxGitSelectionOutput, "-C", path, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return WorktreeFacts{}, err
	}
	var branch string
	if symbolic.exitCode == 0 {
		ref := strings.TrimSpace(string(symbolic.output))
		if !strings.HasPrefix(ref, "refs/heads/") {
			return WorktreeFacts{}, newGitError(gitFailureProtocol)
		}
		branch = strings.TrimPrefix(ref, "refs/heads/")
	} else if symbolic.exitCode != 1 {
		return WorktreeFacts{}, newGitError(gitFailureProcess)
	}
	status, err := a.run(ctx, maxGitStatusOutput, "-C", path, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=all")
	dirty := true
	var limit *LimitError
	if errors.As(err, &limit) {
		err = nil
	} else if err == nil {
		if status.exitCode != 0 {
			return WorktreeFacts{}, newGitError(gitFailureProcess)
		}
		dirty = len(status.output) != 0
	}
	if err != nil {
		return WorktreeFacts{}, err
	}
	return WorktreeFacts{head: head, branch: branch, dirty: dirty, gitDirectory: common}, nil
}

func validateWorktreePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" || strings.EqualFold(filepath.Base(path), ".git") {
		return &ValidationError{Reason: "worktree path must be canonical and absolute"}
	}
	return nil
}

func validWorktreeBranch(branch string) bool {
	if !strings.HasPrefix(branch, "factory/") || len(branch) > maxRevisionBytes {
		return false
	}
	for _, character := range branch {
		if !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '/' || character == '-') {
			return false
		}
	}
	return !strings.HasSuffix(branch, "/") && !strings.Contains(branch, "//")
}
