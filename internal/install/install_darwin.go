//go:build darwin

package install

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"golang.org/x/sys/unix"
)

const (
	formatName       = "format"
	databaseName     = "factory.sqlite3"
	tokenName        = "operator.token"
	lockName         = "home.lock"
	lockAnchorName   = "home.lock.anchor"
	runtimesName     = "runtimes"
	changesName      = "changes"
	formatBytes      = "dark-factory-go-home-v1\n"
	stageSuffix      = ".dark-factory-go-v1.stage"
	memberCount      = 7
	maxNameSize      = 255
	maxHomeBytes     = 4096
	maxPathDepth     = 128
	maxDatabaseBytes = 8 << 20
)

type retainedDir struct {
	name string
	file *os.File
	stat unix.Stat_t
}

type homeParent struct {
	parts []retainedDir
	file  *os.File
}

type phase string

const (
	phaseAfterStageMkdir      phase = "after stage mkdir"
	phaseAfterStageParentSync phase = "after stage mkdir and parent sync"
	phaseBeforeFormatCreate   phase = "before format create"
	phaseBeforeDatabaseCreate phase = "before database create"
	phaseBeforeTokenCreate    phase = "before token create"
	phaseBeforeLockCreate     phase = "before lock create"
	phaseBeforeLockAnchor     phase = "before lock anchor create"
	phaseBeforeRuntimesMkdir  phase = "before runtimes mkdir"
	phaseBeforeChangesMkdir   phase = "before changes mkdir"
	phaseBeforeStageInspect   phase = "before stage inspect"
	phaseAfterStageInspect    phase = "after stage inspect"
	phaseBeforeStageSync      phase = "before stage sync"
	phaseAfterStageSync       phase = "after stage sync"
	phaseBeforeFinalStage     phase = "before final stage inspection"
	phaseBeforePublishRename  phase = "immediately before publish rename"
	phaseBeforeRename         phase = "before rename"
	phaseAfterRename          phase = "after rename"
	phaseAfterParentSync      phase = "after publish parent sync"
	phaseBeforeFinalProof     phase = "before final proof"
	phaseBeforeExistingSecond phase = "before existing home second scan"
	phaseBeforeDoctorSecond   phase = "before doctor second scan"
	phaseBeforeSnapshot       phase = "before snapshot"
)

// phaseHook is only used by package-local Darwin tests to schedule faults and
// replacements at filesystem boundaries. Normal production calls leave it nil.
var phaseHook func(phase) error

// syncDirectory is a deliberately tiny syscall seam. Package-local Darwin
// tests replace it to prove that directory fsync errors are observed; normal
// code always uses unix.Fsync.
var syncDirectory = unix.Fsync

// syncFile is a deliberately tiny syscall seam. Package-local Darwin tests
// replace it to prove that each regular member is fsynced and that errors are
// propagated; normal code always uses unix.Fsync.
var syncFile = unix.Fsync

func atPhase(point phase) error {
	if phaseHook == nil {
		return nil
	}
	return phaseHook(point)
}

type identity struct {
	dev   uint64
	ino   uint64
	mode  uint32
	uid   uint32
	nlink uint64
	size  int64
}

type memberSnapshot struct {
	identity
	digest [sha256.Size]byte
}

type ancestryIdentity struct {
	dev   uint64
	ino   uint64
	mode  uint32
	uid   uint32
	nlink uint64
}

type ancestryCommitment struct {
	name   string
	parent ancestryIdentity
	object ancestryIdentity
}

type treeSnapshot struct {
	root        identity
	rootBinding ancestryCommitment
	ancestors   []ancestryCommitment
	files       map[string]memberSnapshot
	directories map[string]identity
}

func initHome(ctx context.Context, home string) (result Result, resultErr error) {
	parentPath, base, err := splitHome(home)
	if err != nil {
		return Result{}, err
	}
	parent, err := openParent(parentPath)
	if err != nil {
		return Result{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, parent.close()) }()

	stage := "." + base + stageSuffix
	present, err := presentAt(parent.file, base)
	if err != nil {
		return Result{}, err
	}
	if present {
		if _, err := inspectStable(ctx, home, phaseBeforeExistingSecond); err != nil {
			return Result{}, err
		}
		return Result{State: Ready}, nil
	}
	if err := unix.Mkdirat(int(parent.file.Fd()), stage, 0o700); err != nil {
		return Result{}, fmt.Errorf("create staging home: %w", err)
	}
	stageFile, err := openDirectoryAt(parent.file, stage)
	if err != nil {
		return Result{}, fmt.Errorf("open staging home: %w", err)
	}
	defer stageFile.Close()
	var stageStat unix.Stat_t
	if err := unix.Fstat(int(stageFile.Fd()), &stageStat); err != nil {
		return Result{}, fmt.Errorf("inspect staging home: %w", err)
	}
	if err := atPhase(phaseAfterStageMkdir); err != nil {
		return Result{}, err
	}
	if err := parent.recheck(); err != nil {
		return Result{}, err
	}
	if err := recheckBinding(parent.file, stage, stageStat); err != nil {
		return Result{}, err
	}
	if err := syncDirectory(int(parent.file.Fd())); err != nil {
		return Result{}, fmt.Errorf("sync home parent after staging mkdir: %w", err)
	}
	if err := atPhase(phaseAfterStageParentSync); err != nil {
		return Result{}, err
	}
	if err := parent.recheck(); err != nil {
		return Result{}, err
	}
	if err := recheckBinding(parent.file, stage, stageStat); err != nil {
		return Result{}, err
	}

	// Everything after mkdir is deliberately left in the stage on failure.
	at, err := kernel.NewUnixMillis(time.Now().UnixMilli())
	if err != nil {
		return Result{}, fmt.Errorf("create bootstrap timestamp: %w", err)
	}
	image, err := kernel.NewDatabaseImage(ctx, kernel.FactoryConfig{}, at)
	if err != nil {
		return Result{}, fmt.Errorf("build fresh database image: %w", err)
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return Result{}, fmt.Errorf("generate operator token: %w", err)
	}
	if err := writeMember(stageFile, formatName, []byte(formatBytes), phaseBeforeFormatCreate); err != nil {
		return Result{}, err
	}
	if err := writeMember(stageFile, databaseName, image, phaseBeforeDatabaseCreate); err != nil {
		return Result{}, err
	}
	if err := writeMember(stageFile, tokenName, token[:], phaseBeforeTokenCreate); err != nil {
		return Result{}, err
	}
	if err := writeMember(stageFile, lockName, nil, phaseBeforeLockCreate); err != nil {
		return Result{}, err
	}
	if err := atPhase(phaseBeforeLockAnchor); err != nil {
		return Result{}, err
	}
	if err := unix.Linkat(int(stageFile.Fd()), lockName, int(stageFile.Fd()), lockAnchorName, 0); err != nil {
		return Result{}, fmt.Errorf("create home lock anchor: %w", err)
	}
	if err := verifyLockPair(stageFile); err != nil {
		return Result{}, err
	}
	for _, name := range []string{runtimesName, changesName} {
		point := phaseBeforeRuntimesMkdir
		if name == changesName {
			point = phaseBeforeChangesMkdir
		}
		if err := atPhase(point); err != nil {
			return Result{}, err
		}
		if err := unix.Mkdirat(int(stageFile.Fd()), name, 0o700); err != nil {
			return Result{}, fmt.Errorf("create staging %s directory: %w", name, err)
		}
		child, openErr := openDirectoryAt(stageFile, name)
		if openErr != nil {
			return Result{}, fmt.Errorf("open staging %s directory: %w", name, openErr)
		}
		if syncErr := syncDirectory(int(child.Fd())); syncErr != nil {
			_ = child.Close()
			return Result{}, fmt.Errorf("sync staging %s directory: %w", name, syncErr)
		}
		if closeErr := child.Close(); closeErr != nil {
			return Result{}, fmt.Errorf("close staging %s directory: %w", name, closeErr)
		}
		if err := atPhase(phase("after " + name + " directory sync")); err != nil {
			return Result{}, err
		}
	}
	if err := atPhase(phaseBeforeStageInspect); err != nil {
		return Result{}, err
	}
	firstSnapshot, err := inspectFD(ctx, stageFile)
	if err != nil {
		return Result{}, fmt.Errorf("validate staging home: %w", err)
	}
	if err := atPhase(phaseAfterStageInspect); err != nil {
		return Result{}, err
	}
	if err := atPhase(phaseBeforeStageSync); err != nil {
		return Result{}, err
	}
	if err := syncDirectory(int(stageFile.Fd())); err != nil {
		return Result{}, fmt.Errorf("sync staging home: %w", err)
	}
	if err := atPhase(phaseAfterStageSync); err != nil {
		return Result{}, err
	}
	if err := atPhase(phaseBeforeFinalStage); err != nil {
		return Result{}, err
	}
	if err := atPhase(phaseBeforeRename); err != nil {
		return Result{}, err
	}
	// Reopen the stage so the immediately preceding inspection starts at a
	// fresh directory offset and is not satisfied by an old descriptor view.
	second, secondStat, err := openDirectoryMember(parent.file, stage)
	if err != nil {
		return Result{}, fmt.Errorf("reopen staging home: %w", err)
	}
	secondSnapshot, inspectErr := inspectFD(ctx, second)
	closeErr := second.Close()
	if inspectErr != nil {
		return Result{}, fmt.Errorf("revalidate staging home: %w", inspectErr)
	}
	if closeErr != nil {
		return Result{}, fmt.Errorf("close staging home: %w", closeErr)
	}
	if err := sameObjectIdentity(toIdentity(stageStat), toIdentity(secondStat)); err != nil {
		return Result{}, fmt.Errorf("staging home identity changed: %w", err)
	}
	if err := sameSnapshot(firstSnapshot, secondSnapshot); err != nil {
		return Result{}, fmt.Errorf("staging home changed between inspections: %w", err)
	}
	secondSnapshot.ancestors, err = parent.commitments()
	if err != nil {
		return Result{}, err
	}
	secondSnapshot.rootBinding = ancestryCommitment{
		name:   base,
		parent: secondSnapshot.ancestors[len(secondSnapshot.ancestors)-1].object,
		object: ancestryFromIdentity(secondSnapshot.root),
	}
	if err := parent.recheck(); err != nil {
		return Result{}, err
	}
	if err := recheckBinding(parent.file, stage, stageStat); err != nil {
		return Result{}, err
	}
	if err := atPhase(phaseBeforePublishRename); err != nil {
		return Result{}, err
	}
	if err := parent.recheck(); err != nil {
		return Result{}, err
	}
	if err := recheckBinding(parent.file, stage, stageStat); err != nil {
		return Result{}, err
	}
	if err := unix.RenameatxNp(int(parent.file.Fd()), stage, int(parent.file.Fd()), base, unix.RENAME_EXCL); err != nil {
		return Result{}, fmt.Errorf("publish home: %w", err)
	}
	if err := atPhase(phaseAfterRename); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	if err := syncDirectory(int(parent.file.Fd())); err != nil {
		return Result{}, fmt.Errorf("%w: publish rename succeeded but parent sync failed", errors.Join(ErrUncertain, err))
	}
	if err := atPhase(phaseAfterParentSync); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	if err := atPhase(phaseBeforeFinalProof); err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrUncertain, err)
	}
	published, err := inspectPublished(ctx, home, stageStat)
	if err != nil {
		return Result{}, errors.Join(ErrUncertain, err)
	}
	if err := sameSnapshot(secondSnapshot, published); err != nil {
		return Result{}, errors.Join(ErrUncertain, err)
	}
	return Result{State: Published}, nil
}

func inspectHome(ctx context.Context, home string) (result Result, resultErr error) {
	if _, err := inspectStable(ctx, home, phaseBeforeDoctorSecond); err != nil {
		return Result{}, err
	}
	return Result{State: Ready}, nil
}

func inspectStable(ctx context.Context, home string, secondPhase phase) (treeSnapshot, error) {
	first, err := inspectFreshPath(ctx, home)
	if err != nil {
		return treeSnapshot{}, err
	}
	if err := atPhase(secondPhase); err != nil {
		return treeSnapshot{}, errors.Join(ErrUncertain, err)
	}
	second, err := inspectFreshPath(ctx, home)
	if err != nil {
		return treeSnapshot{}, errors.Join(ErrUncertain, err)
	}
	if err := sameSnapshot(first, second); err != nil {
		return treeSnapshot{}, errors.Join(ErrUncertain, err)
	}
	return second, nil
}

func splitHome(home string) (string, string, error) {
	if home == "" || len(home) > maxHomeBytes || !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return "", "", fmt.Errorf("%w: --home must be an absolute canonical path", ErrInvalidHome)
	}
	base := filepath.Base(home)
	if base == "." || base == string(filepath.Separator) || base == ".." || len(base) > maxNameSize || len(base)+len(stageSuffix)+1 > maxNameSize || strings.ContainsRune(base, 0) {
		return "", "", fmt.Errorf("%w: invalid home name", ErrInvalidHome)
	}
	return filepath.Dir(home), base, nil
}

func openParent(path string) (*homeParent, error) {
	if len(path) > maxHomeBytes || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("%w: parent path is not canonical", ErrInvalidHome)
	}
	rootFD, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	root := os.NewFile(uintptr(rootFD), "/")
	if root == nil {
		_ = unix.Close(rootFD)
		return nil, errors.New("open filesystem root: invalid descriptor")
	}
	var rootStat unix.Stat_t
	if err := unix.Fstat(rootFD, &rootStat); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("inspect filesystem root: %w", err)
	}
	p := &homeParent{parts: []retainedDir{{file: root, name: "", stat: rootStat}}, file: root}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if path == "/" {
		parts = nil
	}
	if len(parts) > maxPathDepth {
		_ = p.close()
		return nil, fmt.Errorf("%w: home path is too deep", ErrInvalidHome)
	}
	for _, name := range parts {
		if name == "" || name == "." || name == ".." || len(name) > maxNameSize {
			_ = p.close()
			return nil, fmt.Errorf("%w: invalid parent component", ErrInvalidHome)
		}
		fd, openErr := unix.Openat(int(p.file.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			_ = p.close()
			return nil, fmt.Errorf("open home parent component: %w", openErr)
		}
		file := os.NewFile(uintptr(fd), filepath.Join(path, name))
		if file == nil {
			_ = unix.Close(fd)
			_ = p.close()
			return nil, errors.New("open home parent component: invalid descriptor")
		}
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			_ = file.Close()
			_ = p.close()
			return nil, fmt.Errorf("inspect home parent component: %w", err)
		}
		p.parts = append(p.parts, retainedDir{name: name, file: file, stat: stat})
		p.file = file
	}
	if err := exactDirectory(p.file, true); err != nil {
		_ = p.close()
		return nil, fmt.Errorf("home parent is not private: %w", err)
	}
	return p, nil
}

func (p *homeParent) recheck() error {
	for i := range p.parts {
		var current unix.Stat_t
		if err := unix.Fstat(int(p.parts[i].file.Fd()), &current); err != nil {
			return fmt.Errorf("recheck home parent: %w", err)
		}
		if current.Dev != p.parts[i].stat.Dev || current.Ino != p.parts[i].stat.Ino {
			return fmt.Errorf("%w: home parent identity changed", ErrInvalidHome)
		}
		if i > 0 {
			var binding unix.Stat_t
			if err := unix.Fstatat(int(p.parts[i-1].file.Fd()), p.parts[i].name, &binding, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return fmt.Errorf("recheck home parent binding: %w", err)
			}
			if binding.Dev != p.parts[i].stat.Dev || binding.Ino != p.parts[i].stat.Ino {
				return fmt.Errorf("%w: home parent binding changed", ErrInvalidHome)
			}
		}
	}
	return exactDirectory(p.file, true)
}

func (p *homeParent) commitments() ([]ancestryCommitment, error) {
	if err := p.recheck(); err != nil {
		return nil, err
	}
	commitments := make([]ancestryCommitment, 0, len(p.parts))
	for i := range p.parts {
		var object unix.Stat_t
		if err := unix.Fstat(int(p.parts[i].file.Fd()), &object); err != nil {
			return nil, fmt.Errorf("snapshot home parent: %w", err)
		}
		commitment := ancestryCommitment{name: "/", object: toAncestryIdentity(object)}
		if i > 0 {
			var parent unix.Stat_t
			if err := unix.Fstat(int(p.parts[i-1].file.Fd()), &parent); err != nil {
				return nil, fmt.Errorf("snapshot home parent: %w", err)
			}
			var binding unix.Stat_t
			if err := unix.Fstatat(int(p.parts[i-1].file.Fd()), p.parts[i].name, &binding, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				return nil, fmt.Errorf("snapshot home parent binding: %w", err)
			}
			if binding.Dev != object.Dev || binding.Ino != object.Ino {
				return nil, fmt.Errorf("%w: home parent binding changed", ErrInvalidHome)
			}
			commitment = ancestryCommitment{
				name:   p.parts[i].name,
				parent: toAncestryIdentity(parent),
				object: toAncestryIdentity(object),
			}
		}
		commitments = append(commitments, commitment)
	}
	if err := p.recheck(); err != nil {
		return nil, err
	}
	return commitments, nil
}

func (p *homeParent) close() error {
	var err error
	for i := len(p.parts) - 1; i >= 0; i-- {
		err = errors.Join(err, p.parts[i].file.Close())
	}
	return err
}

func presentAt(parent *os.File, name string) (bool, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect home path: %w", err)
	}
	return true, nil
}

func rejectIfPresent(parent *os.File, name, kind string) error {
	present, err := presentAt(parent, name)
	if err != nil {
		return err
	}
	if present {
		return fmt.Errorf("%w: existing %s refused unchanged", ErrInvalidHome, kind)
	}
	return nil
}

func openDirectoryAt(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("invalid directory descriptor")
	}
	if err := exactDirectory(file, false); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func exactDirectory(file *os.File, parent bool) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR || !exactMode(uint32(stat.Mode), 0o700) || !exactOwner(uint32(stat.Uid)) {
		return fmt.Errorf("%w: directory must be owner-only 0700", ErrInvalidHome)
	}
	if !exactDirectoryLinkCount(uint64(stat.Nlink)) {
		return fmt.Errorf("%w: directory link count is invalid", ErrInvalidHome)
	}
	return nil
}

func writeMember(parent *os.File, name string, contents []byte, createPhase phase) error {
	if err := atPhase(createPhase); err != nil {
		return err
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create home member %s: %w", name, err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return errors.New("create home member: invalid descriptor")
	}
	defer file.Close()
	if err := atPhase(phase("after " + name + " create")); err != nil {
		return err
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		return fmt.Errorf("secure home member %s: %w", name, err)
	}
	if len(contents) > 0 {
		written, err := file.Write(contents)
		if err != nil {
			return fmt.Errorf("write home member %s: %w", name, err)
		}
		if written != len(contents) {
			return fmt.Errorf("write home member %s: %w", name, io.ErrShortWrite)
		}
	}
	if err := atPhase(phase("after " + name + " write")); err != nil {
		return err
	}
	if err := atPhase(phase("before " + name + " fsync")); err != nil {
		return err
	}
	if err := syncFile(fd); err != nil {
		return fmt.Errorf("sync home member %s: %w", name, err)
	}
	if err := atPhase(phase("after " + name + " fsync")); err != nil {
		return err
	}
	return nil
}

func inspectFreshPath(ctx context.Context, path string) (treeSnapshot, error) {
	parentPath, base, err := splitHome(path)
	if err != nil {
		return treeSnapshot{}, err
	}
	parent, err := openParent(parentPath)
	if err != nil {
		return treeSnapshot{}, err
	}
	defer parent.close()
	if err := parent.recheck(); err != nil {
		return treeSnapshot{}, err
	}
	stage := "." + base + stageSuffix
	if err := rejectIfPresent(parent.file, stage, "staging path"); err != nil {
		return treeSnapshot{}, err
	}
	snapshot, err := inspectBinding(ctx, parent.file, base)
	if err != nil {
		return treeSnapshot{}, fmt.Errorf("home is not an exact stopped Go home: %w", err)
	}
	if err := parent.recheck(); err != nil {
		return treeSnapshot{}, err
	}
	snapshot.ancestors, err = parent.commitments()
	if err != nil {
		return treeSnapshot{}, err
	}
	snapshot.rootBinding = ancestryCommitment{
		name:   base,
		parent: snapshot.ancestors[len(snapshot.ancestors)-1].object,
		object: ancestryFromIdentity(snapshot.root),
	}
	return snapshot, nil
}

func inspectPublished(ctx context.Context, path string, expected unix.Stat_t) (treeSnapshot, error) {
	snapshot, err := inspectFreshPath(ctx, path)
	if err != nil {
		return treeSnapshot{}, err
	}
	if err := sameObjectIdentity(toIdentity(expected), snapshot.root); err != nil {
		return treeSnapshot{}, fmt.Errorf("published home identity changed: %w", err)
	}
	return snapshot, nil
}

func inspectBinding(ctx context.Context, parent *os.File, name string) (treeSnapshot, error) {
	file, err := openDirectoryAt(parent, name)
	if err != nil {
		return treeSnapshot{}, err
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return treeSnapshot{}, err
	}
	var binding unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &binding, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return treeSnapshot{}, err
	}
	if stat.Dev != binding.Dev || stat.Ino != binding.Ino {
		return treeSnapshot{}, fmt.Errorf("%w: home identity changed", ErrInvalidHome)
	}
	snapshot, err := inspectFD(ctx, file)
	if err != nil {
		return treeSnapshot{}, err
	}
	if err := unix.Fstatat(int(parent.Fd()), name, &binding, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return treeSnapshot{}, err
	}
	if stat.Dev != binding.Dev || stat.Ino != binding.Ino {
		return treeSnapshot{}, fmt.Errorf("%w: home identity changed", ErrInvalidHome)
	}
	var finalStat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &finalStat); err != nil {
		return treeSnapshot{}, err
	}
	if finalStat.Dev != stat.Dev || finalStat.Ino != stat.Ino {
		return treeSnapshot{}, fmt.Errorf("%w: home identity changed", ErrInvalidHome)
	}
	if err := recheckBinding(parent, name, stat); err != nil {
		return treeSnapshot{}, err
	}
	snapshot.root = toIdentity(finalStat)
	return snapshot, nil
}

func inspectFD(ctx context.Context, home *os.File) (treeSnapshot, error) {
	if err := exactDirectory(home, false); err != nil {
		return treeSnapshot{}, err
	}
	names, err := home.Readdirnames(memberCount + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return treeSnapshot{}, fmt.Errorf("enumerate home: %w", err)
	}
	if len(names) != memberCount {
		return treeSnapshot{}, fmt.Errorf("%w: home census has %d entries", ErrInvalidHome, len(names))
	}
	seen := make(map[string]bool, memberCount)
	for _, name := range names {
		if seen[name] {
			return treeSnapshot{}, fmt.Errorf("%w: duplicate home entry", ErrInvalidHome)
		}
		seen[name] = true
	}
	for _, name := range []string{formatName, databaseName, tokenName} {
		if !seen[name] {
			return treeSnapshot{}, fmt.Errorf("%w: missing home member", ErrInvalidHome)
		}
	}
	if !seen[lockName] || !seen[lockAnchorName] {
		return treeSnapshot{}, fmt.Errorf("%w: missing home lock pair", ErrInvalidHome)
	}
	for _, name := range []string{runtimesName, changesName} {
		if !seen[name] {
			return treeSnapshot{}, fmt.Errorf("%w: missing home directory", ErrInvalidHome)
		}
	}
	if err := inspectFile(ctx, home, formatName, []byte(formatBytes)); err != nil {
		return treeSnapshot{}, err
	}
	if err := inspectDatabase(ctx, home); err != nil {
		return treeSnapshot{}, err
	}
	if err := inspectToken(home); err != nil {
		return treeSnapshot{}, err
	}
	if err := inspectLockPair(home); err != nil {
		return treeSnapshot{}, err
	}
	for _, name := range []string{runtimesName, changesName} {
		child, childStat, err := openDirectoryMember(home, name)
		if err != nil {
			return treeSnapshot{}, fmt.Errorf("inspect home %s: %w", name, err)
		}
		entries, readErr := child.Readdirnames(1)
		closeErr := child.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return treeSnapshot{}, errors.Join(fmt.Errorf("inspect home %s: %w", name, readErr), closeErr)
		}
		if len(entries) != 0 {
			return treeSnapshot{}, errors.Join(fmt.Errorf("%w: home %s directory is populated", ErrInvalidHome, name), closeErr)
		}
		if closeErr != nil {
			return treeSnapshot{}, closeErr
		}
		if err := recheckBinding(home, name, childStat); err != nil {
			return treeSnapshot{}, err
		}
	}
	if err := atPhase(phaseBeforeSnapshot); err != nil {
		return treeSnapshot{}, err
	}
	snapshot, err := snapshotFD(ctx, home)
	if err != nil {
		return treeSnapshot{}, err
	}
	return snapshot, nil
}

func openMember(parent *os.File, name string) (*os.File, unix.Stat_t, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, unix.Stat_t{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, unix.Stat_t{}, errors.New("invalid member descriptor")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, unix.Stat_t{}, err
	}
	var binding unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &binding, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = file.Close()
		return nil, unix.Stat_t{}, err
	}
	if stat.Dev != binding.Dev || stat.Ino != binding.Ino {
		_ = file.Close()
		return nil, unix.Stat_t{}, fmt.Errorf("%w: member identity changed", ErrInvalidHome)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || !exactMode(uint32(stat.Mode), 0o600) || !exactOwner(uint32(stat.Uid)) || stat.Nlink != 1 {
		_ = file.Close()
		return nil, unix.Stat_t{}, fmt.Errorf("%w: member %s is not exact owner-only regular 0600", ErrInvalidHome, name)
	}
	return file, stat, nil
}

func inspectFile(ctx context.Context, parent *os.File, name string, expected []byte) error {
	file, stat, err := openMember(parent, name)
	if err != nil {
		return fmt.Errorf("inspect home member %s: %w", name, err)
	}
	defer file.Close()
	if stat.Size != int64(len(expected)) {
		return fmt.Errorf("%w: home member %s has wrong length", ErrInvalidHome, name)
	}
	contents := make([]byte, int(stat.Size))
	if _, err := io.ReadFull(file, contents); err != nil {
		return fmt.Errorf("read home member %s: %w", name, err)
	}
	if !bytes.Equal(contents, expected) {
		return fmt.Errorf("%w: home member %s has wrong bytes", ErrInvalidHome, name)
	}
	if err := recheckBinding(parent, name, stat); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func inspectDatabase(ctx context.Context, parent *os.File) error {
	file, stat, err := openMember(parent, databaseName)
	if err != nil {
		return fmt.Errorf("inspect home database: %w", err)
	}
	defer file.Close()
	if stat.Size <= 0 || stat.Size > maxDatabaseBytes {
		return fmt.Errorf("%w: database image size is outside bounds", ErrInvalidHome)
	}
	if err := kernel.InspectPristine(ctx, file, stat.Size); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("inspect pristine database image: %w", err)
		}
		return errors.Join(ErrInvalidHome, fmt.Errorf("inspect pristine database image: %w", err))
	}
	if err := atPhase(phase("after database pristine proof")); err != nil {
		return err
	}
	if err := recheckBinding(parent, databaseName, stat); err != nil {
		return err
	}
	return nil
}

func inspectToken(parent *os.File) error {
	file, stat, err := openMember(parent, tokenName)
	if err != nil {
		return fmt.Errorf("inspect operator token: %w", err)
	}
	defer file.Close()
	if stat.Size != 32 {
		return fmt.Errorf("%w: operator token length is not 32", ErrInvalidHome)
	}
	var token [32]byte
	if _, err := io.ReadFull(file, token[:]); err != nil {
		return fmt.Errorf("read operator token: %w", err)
	}
	if bytes.Equal(token[:], make([]byte, len(token))) {
		return fmt.Errorf("%w: operator token is zero", ErrInvalidHome)
	}
	if err := recheckBinding(parent, tokenName, stat); err != nil {
		return err
	}
	return nil
}

func recheckBinding(parent *os.File, name string, expected unix.Stat_t) error {
	var current unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("recheck home member %s: %w", name, err)
	}
	if current.Dev != expected.Dev || current.Ino != expected.Ino {
		return fmt.Errorf("%w: home member %s identity changed", ErrInvalidHome, name)
	}
	return nil
}

func exactMode(mode, expected uint32) bool {
	return mode&0o7777 == expected
}

func exactOwner(uid uint32) bool {
	return uid == uint32(os.Geteuid())
}

func exactDirectoryLinkCount(nlink uint64) bool {
	return nlink >= 2
}

func openDirectoryMember(parent *os.File, name string) (*os.File, unix.Stat_t, error) {
	file, err := openDirectoryAt(parent, name)
	if err != nil {
		return nil, unix.Stat_t{}, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		_ = file.Close()
		return nil, unix.Stat_t{}, err
	}
	if err := recheckBinding(parent, name, stat); err != nil {
		_ = file.Close()
		return nil, unix.Stat_t{}, err
	}
	return file, stat, nil
}

func snapshotFD(ctx context.Context, home *os.File) (treeSnapshot, error) {
	var root unix.Stat_t
	if err := unix.Fstat(int(home.Fd()), &root); err != nil {
		return treeSnapshot{}, err
	}
	snapshot := treeSnapshot{
		root:        toIdentity(root),
		files:       make(map[string]memberSnapshot, 4),
		directories: make(map[string]identity, 2),
	}
	for _, name := range []string{formatName, databaseName, tokenName} {
		file, stat, err := openMember(home, name)
		if err != nil {
			return treeSnapshot{}, fmt.Errorf("snapshot home member %s: %w", name, err)
		}
		minimum, maximum := memberSizeBounds(name)
		digest, digestErr := digestMember(ctx, file, stat.Size, minimum, maximum)
		closeErr := file.Close()
		if digestErr != nil {
			return treeSnapshot{}, digestErr
		}
		if closeErr != nil {
			return treeSnapshot{}, closeErr
		}
		if err := recheckBinding(home, name, stat); err != nil {
			return treeSnapshot{}, err
		}
		snapshot.files[name] = memberSnapshot{identity: toIdentity(stat), digest: digest}
	}
	for _, name := range []string{lockName, lockAnchorName} {
		file, stat, err := openLockMember(home, name)
		if err != nil {
			return treeSnapshot{}, fmt.Errorf("snapshot home lock member %s: %w", name, err)
		}
		digest, digestErr := digestMember(ctx, file, stat.Size, 0, 0)
		closeErr := file.Close()
		if digestErr != nil {
			return treeSnapshot{}, digestErr
		}
		if closeErr != nil {
			return treeSnapshot{}, closeErr
		}
		if err := recheckBinding(home, name, stat); err != nil {
			return treeSnapshot{}, err
		}
		snapshot.files[name] = memberSnapshot{identity: toIdentity(stat), digest: digest}
	}
	for _, name := range []string{runtimesName, changesName} {
		directory, stat, err := openDirectoryMember(home, name)
		if err != nil {
			return treeSnapshot{}, fmt.Errorf("snapshot home directory %s: %w", name, err)
		}
		entries, readErr := directory.Readdirnames(1)
		closeErr := directory.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return treeSnapshot{}, readErr
		}
		if len(entries) != 0 {
			return treeSnapshot{}, fmt.Errorf("%w: home %s directory is populated", ErrInvalidHome, name)
		}
		if closeErr != nil {
			return treeSnapshot{}, closeErr
		}
		if err := recheckBinding(home, name, stat); err != nil {
			return treeSnapshot{}, err
		}
		snapshot.directories[name] = toIdentity(stat)
	}
	return snapshot, nil
}

func memberSizeBounds(name string) (int64, int64) {
	switch name {
	case formatName:
		return int64(len(formatBytes)), int64(len(formatBytes))
	case databaseName:
		return 1, maxDatabaseBytes
	case tokenName:
		return 32, 32
	case lockName:
		return 0, 0
	case lockAnchorName:
		return 0, 0
	default:
		return 0, 0
	}
}

func digestMember(ctx context.Context, file *os.File, size, minimum, maximum int64) ([sha256.Size]byte, error) {
	if size < minimum || size > maximum {
		return [sha256.Size]byte{}, fmt.Errorf("%w: member size is outside bounded snapshot", ErrInvalidHome)
	}
	var initial unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &initial); err != nil {
		return [sha256.Size]byte{}, err
	}
	if initial.Size != size || initial.Size < minimum || initial.Size > maximum {
		return [sha256.Size]byte{}, fmt.Errorf("%w: member changed before bounded snapshot", ErrInvalidHome)
	}
	hash := sha256.New()
	buffer := make([]byte, 128<<10)
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return [sha256.Size]byte{}, err
		}
		want := int64(len(buffer))
		if remaining := size - offset; want > remaining {
			want = remaining
		}
		read, err := file.ReadAt(buffer[:int(want)], offset)
		if read != int(want) {
			return [sha256.Size]byte{}, fmt.Errorf("read home member: %w", errors.Join(io.ErrUnexpectedEOF, err))
		}
		if err != nil {
			return [sha256.Size]byte{}, err
		}
		if _, err := hash.Write(buffer[:read]); err != nil {
			return [sha256.Size]byte{}, err
		}
		offset += int64(read)
	}
	var final unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &final); err != nil {
		return [sha256.Size]byte{}, err
	}
	if final.Dev != initial.Dev || final.Ino != initial.Ino || final.Size != initial.Size {
		return [sha256.Size]byte{}, fmt.Errorf("%w: member changed during bounded snapshot", ErrInvalidHome)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func toIdentity(stat unix.Stat_t) identity {
	return identity{dev: uint64(stat.Dev), ino: uint64(stat.Ino), mode: uint32(stat.Mode), uid: uint32(stat.Uid), nlink: uint64(stat.Nlink), size: stat.Size}
}

func sameIdentities(left, right identity) error {
	if left != right {
		return fmt.Errorf("%w: filesystem identity or metadata changed", ErrInvalidHome)
	}
	return nil
}

func sameObjectIdentity(left, right identity) error {
	if left.dev != right.dev || left.ino != right.ino {
		return fmt.Errorf("%w: filesystem object identity changed", ErrInvalidHome)
	}
	return nil
}

func sameSnapshot(left, right treeSnapshot) error {
	if err := sameIdentities(left.root, right.root); err != nil {
		return err
	}
	if left.rootBinding.name != right.rootBinding.name || !sameAncestryIdentity(left.rootBinding.parent, right.rootBinding.parent, true) || !sameAncestryIdentity(left.rootBinding.object, right.rootBinding.object, true) {
		return fmt.Errorf("%w: home path binding changed between inspections", ErrInvalidHome)
	}
	if len(left.ancestors) != len(right.ancestors) {
		return fmt.Errorf("%w: home ancestry changed between inspections", ErrInvalidHome)
	}
	for index := range left.ancestors {
		leftAncestor, rightAncestor := left.ancestors[index], right.ancestors[index]
		strictNlink := index == 0 || index == len(left.ancestors)-1
		if leftAncestor.name != rightAncestor.name || !sameAncestryIdentity(leftAncestor.parent, rightAncestor.parent, false) || !sameAncestryIdentity(leftAncestor.object, rightAncestor.object, strictNlink) {
			return fmt.Errorf("%w: home ancestry changed between inspections", ErrInvalidHome)
		}
	}
	for _, name := range []string{formatName, databaseName, tokenName, lockName, lockAnchorName} {
		if left.files[name] != right.files[name] {
			return fmt.Errorf("%w: home member %s changed between inspections", ErrInvalidHome, name)
		}
	}
	for _, name := range []string{runtimesName, changesName} {
		if err := sameIdentities(left.directories[name], right.directories[name]); err != nil {
			return fmt.Errorf("%w: home directory %s changed between inspections", ErrInvalidHome, name)
		}
	}
	return nil
}

func toAncestryIdentity(stat unix.Stat_t) ancestryIdentity {
	return ancestryIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino), mode: uint32(stat.Mode), uid: uint32(stat.Uid), nlink: uint64(stat.Nlink)}
}

func ancestryFromIdentity(value identity) ancestryIdentity {
	return ancestryIdentity{dev: value.dev, ino: value.ino, mode: value.mode, uid: value.uid, nlink: value.nlink}
}

func sameAncestryIdentity(left, right ancestryIdentity, strictNlink bool) bool {
	if left.dev != right.dev || left.ino != right.ino || left.mode != right.mode || left.uid != right.uid {
		return false
	}
	return !strictNlink || left.nlink == right.nlink
}
