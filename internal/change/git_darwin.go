//go:build darwin

package change

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"debug/macho"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
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
	maxGitObjectEntries   = 65_536
	gitTerminateGrace     = 250 * time.Millisecond
	gitPipeDrainGrace     = time.Second
)

type gitCommandSpec struct {
	program    string
	repository string
	home       string
	arguments  []string
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
	return selectGitWithTrust(ctx, gitExecutable, repositoryRoot, revision, expected, nil, true)
}

// VerifyRepositoryRoot rechecks the exact repository-root identity without
// resolving a revision or selecting source content.
func VerifyRepositoryRoot(repositoryRoot string, expected RepositoryIdentity) error {
	_, err := checkpointRepository(repositoryRoot, expected)
	return err
}

// selectGit is the package-private native-process fixture seam. Public callers
// can enter only through SelectGit's root-owned Developer-toolchain check.
func selectGit(ctx context.Context, gitExecutable, repositoryRoot, revision string, expected RepositoryIdentity, hook gitProcessHook) (Selection, error) {
	return selectGitWithTrust(ctx, gitExecutable, repositoryRoot, revision, expected, hook, false)
}

func selectGitWithTrust(ctx context.Context, gitExecutable, repositoryRoot, revision string, expected RepositoryIdentity, hook gitProcessHook, trusted bool) (Selection, error) {
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
		repositoryRoot: repositoryRoot, repository: repository,
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
	if err != nil || !safeGitMode(rootStat.Mode, gitModeDirectory) {
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
	objectStore, err := checkpointGitObjectStore(objectsFD, maxGitObjectEntries)
	if err != nil {
		return repositoryCheckpoint{}, err
	}
	return repositoryCheckpoint{
		root: root, git: gitIdentity, config: configIdentity, objects: objectsIdentity, objectStore: objectStore,
	}, nil
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
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
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

type gitObjectRecord struct {
	path string
	stat unix.Stat_t
}

type gitObjectScan struct {
	rootDevice uint64
	rootUID    uint32
	maximum    int
	count      int
	records    []gitObjectRecord
}

func checkpointGitObjectStore(objectsFD, maximum int) (gitObjectStoreIdentity, error) {
	root, err := fstatGit(objectsFD)
	if err != nil || !safeGitMode(root.Mode, gitModeDirectory) || root.Ino == 0 || maximum < 0 {
		return gitObjectStoreIdentity{}, &ValidationError{Reason: "Git object store root is invalid"}
	}
	scan := gitObjectScan{
		rootDevice: uint64(root.Dev), rootUID: root.Uid, maximum: maximum,
		records: make([]gitObjectRecord, 0, min(maximum+1, 1024)),
	}
	if err := scan.walk(objectsFD, "", root); err != nil {
		return gitObjectStoreIdentity{}, err
	}
	sort.Slice(scan.records, func(left, right int) bool { return scan.records[left].path < scan.records[right].path })
	hasher := sha256.New()
	var encoded [8]byte
	for _, record := range scan.records {
		binary.BigEndian.PutUint32(encoded[:4], uint32(len(record.path)))
		_, _ = hasher.Write(encoded[:4])
		_, _ = io.WriteString(hasher, record.path)
		for _, value := range []uint64{
			uint64(record.stat.Dev), record.stat.Ino, uint64(record.stat.Uid), uint64(record.stat.Mode),
			uint64(record.stat.Nlink), uint64(record.stat.Size), uint64(record.stat.Mtim.Sec),
			uint64(record.stat.Mtim.Nsec), uint64(record.stat.Ctim.Sec), uint64(record.stat.Ctim.Nsec),
		} {
			binary.BigEndian.PutUint64(encoded[:], value)
			_, _ = hasher.Write(encoded[:])
		}
	}
	var digest [32]byte
	copy(digest[:], hasher.Sum(nil))
	return gitObjectStoreIdentity{entryCount: uint32(scan.count), digest: digest}, nil
}

func (scan *gitObjectScan) walk(directoryFD int, relative string, expected unix.Stat_t) error {
	before, err := fstatGit(directoryFD)
	if err != nil || !sameGitStat(before, expected) || !scan.validDirectory(before) {
		return &ValidationError{Reason: "Git object-store directory identity differs"}
	}
	scan.records = append(scan.records, gitObjectRecord{path: relative, stat: before})
	names, err := scan.readNames(directoryFD)
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		isDirectory, allowed := validGitObjectPath(relative, name)
		if !allowed {
			return &ValidationError{Reason: "Git object store contains an unsupported path"}
		}
		var observed unix.Stat_t
		if err := unix.Fstatat(directoryFD, name, &observed, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return newGitError(gitFailurePrivateIO)
		}
		path := name
		if relative != "" {
			path = relative + "/" + name
		}
		if isDirectory {
			childFD, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return &ValidationError{Reason: "Git object-store directory indirection is forbidden"}
			}
			childStat, statErr := fstatGit(childFD)
			if statErr != nil || !sameGitStat(observed, childStat) || !scan.validDirectory(childStat) {
				unix.Close(childFD)
				return &ValidationError{Reason: "Git object-store directory authority differs"}
			}
			err = scan.walk(childFD, path, childStat)
			closeErr := unix.Close(childFD)
			if err != nil {
				return err
			}
			if closeErr != nil {
				return newGitError(gitFailurePrivateIO)
			}
			continue
		}
		if !scan.validFile(observed) {
			return &ValidationError{Reason: "Git object-store file authority differs"}
		}
		fileFD, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return &ValidationError{Reason: "Git object-store file indirection is forbidden"}
		}
		actual, statErr := fstatGit(fileFD)
		closeErr := unix.Close(fileFD)
		if statErr != nil || closeErr != nil || !sameGitStat(observed, actual) || !scan.validFile(actual) {
			return &ValidationError{Reason: "Git object-store file authority differs"}
		}
		scan.records = append(scan.records, gitObjectRecord{path: path, stat: actual})
	}
	after, err := fstatGit(directoryFD)
	if err != nil || !sameGitStat(before, after) {
		return &ValidationError{Reason: "Git object-store directory changed during inspection"}
	}
	return nil
}

func (scan *gitObjectScan) readNames(directoryFD int) ([]string, error) {
	duplicate, err := unix.Dup(directoryFD)
	if err != nil {
		return nil, newGitError(gitFailurePrivateIO)
	}
	unix.CloseOnExec(duplicate)
	directory := os.NewFile(uintptr(duplicate), "")
	defer directory.Close()
	names := make([]string, 0, 32)
	for {
		batch, err := directory.Readdirnames(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, newGitError(gitFailurePrivateIO)
		}
		for _, name := range batch {
			scan.count++
			if scan.count > scan.maximum {
				return nil, &LimitError{Reason: "Git object-store entry limit exceeded"}
			}
			names = append(names, name)
		}
		if errors.Is(err, io.EOF) {
			return names, nil
		}
	}
}

func (scan *gitObjectScan) validDirectory(stat unix.Stat_t) bool {
	return safeGitMode(stat.Mode, gitModeDirectory) && uint64(stat.Dev) == scan.rootDevice && stat.Uid == scan.rootUID &&
		stat.Ino != 0
}

func (scan *gitObjectScan) validFile(stat unix.Stat_t) bool {
	return safeGitMode(stat.Mode, gitModeFile) && uint64(stat.Dev) == scan.rootDevice && stat.Uid == scan.rootUID &&
		stat.Ino != 0 && stat.Nlink == 1 && stat.Size >= 0
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

func validGitObjectPath(parent, name string) (bool, bool) {
	if len(name) == 0 || len(name) > MaxComponentBytes || name == "." || name == ".." {
		return false, false
	}
	switch parent {
	case "":
		return true, name == "info" || name == "pack" || (len(name) == 2 && isLowerHex(name))
	case "info":
		if name == "commit-graphs" {
			return true, true
		}
		return false, name == "packs" || name == "commit-graph"
	case "info/commit-graphs":
		return false, name == "commit-graph-chain" || validGraphName(name)
	case "pack":
		return false, validPackName(name)
	default:
		if len(parent) == 2 && isLowerHex(parent) {
			return false, (len(name) == 38 || len(name) == 62) && isLowerHex(name)
		}
		return false, false
	}
}

func isLowerHex(value string) bool {
	for _, character := range []byte(value) {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return len(value) > 0
}

func validGraphName(name string) bool {
	if !strings.HasPrefix(name, "graph-") || !strings.HasSuffix(name, ".graph") {
		return false
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(name, "graph-"), ".graph")
	return (len(digest) == 40 || len(digest) == 64) && isLowerHex(digest)
}

func validPackName(name string) bool {
	if name == "multi-pack-index" {
		return true
	}
	if strings.HasPrefix(name, "multi-pack-index-") && strings.HasSuffix(name, ".bitmap") {
		digest := strings.TrimSuffix(strings.TrimPrefix(name, "multi-pack-index-"), ".bitmap")
		return (len(digest) == 40 || len(digest) == 64) && isLowerHex(digest)
	}
	if !strings.HasPrefix(name, "pack-") {
		return false
	}
	stem, extension, found := strings.Cut(strings.TrimPrefix(name, "pack-"), ".")
	if !found || (len(stem) != 40 && len(stem) != 64) || !isLowerHex(stem) {
		return false
	}
	switch extension {
	case "pack", "idx", "rev", "bitmap", "mtimes", "keep":
		return true
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
		if key == "" || key == "include" || strings.HasPrefix(key, "include.") || key == "includeif" || strings.HasPrefix(key, "includeif.") || section == "extensions" && key == "worktreeconfig" {
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
	if !validDeveloperGitPath(path) {
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

func validDeveloperGitPath(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Base(path) != "git" {
		return false
	}
	components := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(components) == 6 && components[0] == "Library" && components[1] == "Developer" && components[2] == "CommandLineTools" &&
		components[3] == "usr" && components[4] == "bin" && components[5] == "git" {
		return true
	}
	return len(components) == 7 && components[0] == "Applications" && strings.HasSuffix(components[1], ".app") &&
		components[2] == "Contents" && components[3] == "Developer" && components[4] == "usr" && components[5] == "bin" && components[6] == "git"
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
	if !selection.gitIdentity.trusted {
		return nil, &ValidationError{Reason: "blob reader selection lacks trusted Git authority"}
	}
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
		arguments: []string{"-C", repositoryRoot, "cat-file", "--batch"}, hook: hook,
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
