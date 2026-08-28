//go:build darwin

package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"golang.org/x/sys/unix"
)

const (
	operationalMaxDatabaseBytes = 256 << 20
	operationalMaxWALBytes      = 272 << 20
	operationalMinSHMBytes      = 32768
	operationalMaxSHMBytes      = 4 << 20
)

type operationalHomeState struct {
	homePath string
	parent   *homeParent
	home     *os.File
	lock     *os.File
	root     identity
	lockID   identity
	closed   bool
}

type operationalSource struct {
	name string
	file *os.File
	stat unix.Stat_t
}

func openOperationalHome(ctx context.Context, home string) (_ *OperationalHome, resultErr error) {
	parentPath, base, err := splitHome(home)
	if err != nil {
		return nil, err
	}
	parent, err := openParent(parentPath)
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			resultErr = errors.Join(resultErr, parent.close())
		}
	}()
	if err := parent.recheck(); err != nil {
		return nil, err
	}
	stage := "." + base + stageSuffix
	if err := rejectIfPresent(parent.file, stage, "staging path"); err != nil {
		return nil, err
	}
	homeFile, homeStat, err := openDirectoryMember(parent.file, base)
	if err != nil {
		return nil, fmt.Errorf("open operational home: %w", err)
	}
	var lockFile *os.File
	var lockStat unix.Stat_t
	locked := false
	defer func() {
		if cleanup {
			if locked {
				resultErr = errors.Join(resultErr, unix.Flock(int(lockFile.Fd()), unix.LOCK_UN))
			}
			if lockFile != nil {
				resultErr = errors.Join(resultErr, lockFile.Close())
			}
			resultErr = errors.Join(resultErr, homeFile.Close())
		}
	}()
	if err := recheckBinding(parent.file, base, homeStat); err != nil {
		return nil, err
	}

	lockFile, lockStat, err = openOperationalLock(homeFile)
	if err != nil {
		return nil, fmt.Errorf("open operational home lock: %w", err)
	}
	if err := unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errors.Join(ErrBusy, err)
		}
		return nil, fmt.Errorf("acquire operational home lock: %w", err)
	}
	locked = true
	if err := recheckOperationalIdentity(parent, base, homeFile, homeStat, lockFile, lockStat); err != nil {
		return nil, err
	}
	if err := inspectOperationalHome(ctx, homeFile); err != nil {
		return nil, err
	}
	if err := recheckOperationalIdentity(parent, base, homeFile, homeStat, lockFile, lockStat); err != nil {
		return nil, err
	}
	if err := parent.recheck(); err != nil {
		return nil, err
	}
	cleanup = false
	return &OperationalHome{state: operationalHomeState{
		homePath: home,
		parent:   parent,
		home:     homeFile,
		lock:     lockFile,
		root:     toIdentity(homeStat),
		lockID:   toIdentity(lockStat),
	}}, nil
}

func (state *operationalHomeState) pathMember(name string) string {
	return filepath.Join(state.homePath, name)
}

func (state *operationalHomeState) path(name string) string {
	if state == nil || state.homePath == "" {
		return ""
	}
	return state.pathMember(name)
}

func (state *operationalHomeState) close() error {
	if state == nil || state.closed {
		return nil
	}
	state.closed = true
	identityErr := recheckOperationalIdentityByState(state)
	var result error
	if identityErr != nil {
		result = errors.Join(result, errors.Join(ErrUncertain, identityErr))
	}
	// Close every retained descriptor even when identity is no longer
	// resolvable. The lock is deliberately released after all other handles.
	if state.home != nil {
		result = errors.Join(result, state.home.Close())
		state.home = nil
	}
	if state.parent != nil {
		result = errors.Join(result, state.parent.close())
		state.parent = nil
	}
	if state.lock != nil {
		if err := unix.Flock(int(state.lock.Fd()), unix.LOCK_UN); err != nil {
			result = errors.Join(result, errors.Join(ErrUncertain, fmt.Errorf("release operational home lock: %w", err)))
		}
		if err := state.lock.Close(); err != nil {
			result = errors.Join(result, errors.Join(ErrUncertain, fmt.Errorf("close operational home lock: %w", err)))
		}
		state.lock = nil
	}
	return result
}

func recheckOperationalIdentityByState(state *operationalHomeState) error {
	if state.parent == nil || state.home == nil || state.lock == nil {
		return fmt.Errorf("operational home descriptors are unavailable")
	}
	if err := state.parent.recheck(); err != nil {
		return err
	}
	if err := exactDirectory(state.home, false); err != nil {
		return err
	}
	if err := sameFileIdentity(state.root, state.home); err != nil {
		return err
	}
	if err := recheckIdentityBinding(state.parent.file, filepath.Base(state.homePath), state.root); err != nil {
		return err
	}
	if err := sameFileIdentity(state.lockID, state.lock); err != nil {
		return err
	}
	return recheckIdentityBinding(state.home, lockName, state.lockID)
}

func recheckOperationalIdentity(parent *homeParent, name string, home *os.File, homeStat unix.Stat_t, lock *os.File, lockStat unix.Stat_t) error {
	if err := parent.recheck(); err != nil {
		return err
	}
	if err := exactDirectory(home, false); err != nil {
		return err
	}
	if err := sameFileObjectIdentity(toIdentity(homeStat), home); err != nil {
		return err
	}
	if err := recheckBinding(parent.file, name, homeStat); err != nil {
		return err
	}
	if err := sameFileObjectIdentity(toIdentity(lockStat), lock); err != nil {
		return err
	}
	return recheckBinding(home, lockName, lockStat)
}

func sameFileIdentity(expected identity, file *os.File) error {
	var stat unix.Stat_t
	if file == nil {
		return fmt.Errorf("operational home descriptor is unavailable")
	}
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	return sameIdentities(expected, toIdentity(stat))
}

func sameFileObjectIdentity(expected identity, file *os.File) error {
	var stat unix.Stat_t
	if file == nil {
		return fmt.Errorf("operational home descriptor is unavailable")
	}
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	return sameObjectIdentity(expected, toIdentity(stat))
}

func recheckIdentityBinding(parent *os.File, name string, expected identity) error {
	var current unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return fmt.Errorf("recheck operational home member %s: %w", name, err)
	}
	if uint64(current.Dev) != expected.dev || uint64(current.Ino) != expected.ino {
		return fmt.Errorf("%w: operational home member %s identity changed", ErrInvalidHome, name)
	}
	return nil
}

func inspectOperationalHome(ctx context.Context, home *os.File) error {
	names, err := home.Readdirnames(memberCount + 3)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("enumerate operational home: %w", err)
	}
	if len(names) < memberCount || len(names) > memberCount+2 {
		return fmt.Errorf("%w: operational home census has %d entries", ErrInvalidHome, len(names))
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			return fmt.Errorf("%w: duplicate operational home entry", ErrInvalidHome)
		}
		seen[name] = true
	}
	for _, name := range []string{formatName, databaseName, tokenName, lockName, runtimesName, changesName} {
		if !seen[name] {
			return fmt.Errorf("%w: missing operational home member %s", ErrInvalidHome, name)
		}
	}
	if err := inspectFile(ctx, home, formatName, []byte(formatBytes)); err != nil {
		return err
	}
	if err := inspectToken(home); err != nil {
		return err
	}
	for _, name := range []string{runtimesName, changesName} {
		directory, _, err := openDirectoryMember(home, name)
		if err != nil {
			return fmt.Errorf("inspect operational home %s: %w", name, err)
		}
		if err := directory.Close(); err != nil {
			return fmt.Errorf("close operational home %s: %w", name, err)
		}
	}
	return inspectOperationalDatabase(ctx, home, seen[databaseName+"-wal"], seen[databaseName+"-shm"])
}

func openOperationalLock(home *os.File) (*os.File, unix.Stat_t, error) {
	fd, err := unix.Openat(int(home.Fd()), lockName, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, unix.Stat_t{}, err
	}
	file := os.NewFile(uintptr(fd), lockName)
	if file == nil {
		_ = unix.Close(fd)
		return nil, unix.Stat_t{}, errors.New("invalid operational home lock descriptor")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, unix.Stat_t{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || !exactMode(uint32(stat.Mode), 0o600) || !exactOwner(uint32(stat.Uid)) || stat.Nlink != 1 || stat.Size != 0 {
		_ = file.Close()
		return nil, unix.Stat_t{}, fmt.Errorf("%w: operational home lock is not exact owner-only regular 0600", ErrInvalidHome)
	}
	if err := recheckBinding(home, lockName, stat); err != nil {
		_ = file.Close()
		return nil, unix.Stat_t{}, err
	}
	return file, stat, nil
}

func inspectOperationalDatabase(ctx context.Context, home *os.File, walPresent, shmPresent bool) error {
	if walPresent != shmPresent {
		return fmt.Errorf("%w: operational SQLite WAL sidecars are incomplete", ErrInvalidHome)
	}
	sources := make([]operationalSource, 0, 3)
	for _, item := range []struct {
		name string
		min  int64
		max  int64
	}{
		{databaseName, 100, operationalMaxDatabaseBytes},
		{databaseName + "-wal", 0, operationalMaxWALBytes},
		{databaseName + "-shm", operationalMinSHMBytes, operationalMaxSHMBytes},
	} {
		if item.name != databaseName && !walPresent {
			continue
		}
		file, stat, err := openMember(home, item.name)
		if err != nil {
			return fmt.Errorf("inspect operational SQLite %s: %w", item.name, err)
		}
		if stat.Size < item.min || stat.Size > item.max {
			file.Close()
			return fmt.Errorf("%w: operational SQLite %s size is outside bounds", ErrInvalidHome, item.name)
		}
		sources = append(sources, operationalSource{name: item.name, file: file, stat: stat})
	}
	defer func() {
		for _, source := range sources {
			_ = source.file.Close()
		}
	}()
	if err := copyAndValidateOperationalDatabase(ctx, sources); err != nil {
		return err
	}
	for _, source := range sources {
		if err := recheckOperationalSource(home, source); err != nil {
			return err
		}
	}
	return nil
}

func recheckOperationalSource(home *os.File, source operationalSource) error {
	var current unix.Stat_t
	if err := unix.Fstat(int(source.file.Fd()), &current); err != nil {
		return fmt.Errorf("recheck operational SQLite %s: %w", source.name, err)
	}
	if toIdentity(current) != toIdentity(source.stat) {
		return fmt.Errorf("%w: operational SQLite %s changed during validation", ErrInvalidHome, source.name)
	}
	return recheckBinding(home, source.name, source.stat)
}

func copyAndValidateOperationalDatabase(ctx context.Context, sources []operationalSource) error {
	// kernel.Open intentionally rejects symlinked ancestors; /tmp is a
	// symlink on Darwin, so keep this disposable copy below its real parent.
	directory, err := os.MkdirTemp("/private/tmp", "dark-factory-operational-")
	if err != nil {
		return fmt.Errorf("create operational SQLite scratch: %w", err)
	}
	defer os.RemoveAll(directory)
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure operational SQLite scratch: %w", err)
	}
	databasePath := filepath.Join(directory, databaseName)
	for _, source := range sources {
		if err := copyOperationalSource(ctx, source, filepath.Join(directory, source.name)); err != nil {
			return err
		}
	}
	store, err := kernel.Open(ctx, databasePath)
	if err != nil {
		return fmt.Errorf("validate operational SQLite database: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("close operational SQLite validation store: %w", err)
	}
	return nil
}

func copyOperationalSource(ctx context.Context, source operationalSource, target string) error {
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create operational SQLite scratch member %s: %w", source.name, err)
	}
	closeFile := func(cause error) error { return errors.Join(cause, file.Close()) }
	buffer := make([]byte, 128<<10)
	for offset := int64(0); offset < source.stat.Size; {
		if err := ctx.Err(); err != nil {
			return closeFile(err)
		}
		want := int64(len(buffer))
		if remain := source.stat.Size - offset; want > remain {
			want = remain
		}
		read, readErr := source.file.ReadAt(buffer[:int(want)], offset)
		if read != int(want) {
			return closeFile(errors.Join(io.ErrUnexpectedEOF, readErr))
		}
		if readErr != nil {
			return closeFile(readErr)
		}
		written, writeErr := file.Write(buffer[:read])
		if written != read || writeErr != nil {
			return closeFile(errors.Join(io.ErrShortWrite, writeErr))
		}
		offset += int64(read)
	}
	if err := file.Sync(); err != nil {
		return closeFile(err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	return nil
}
