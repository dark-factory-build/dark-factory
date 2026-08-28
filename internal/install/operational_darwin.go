//go:build darwin

package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

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
	mu       sync.Mutex
	homeName string
	parent   *homeParent
	home     *os.File
	lock     *os.File
	anchor   *os.File
	root     identity
	lockID   identity
	anchorID identity
	members  map[string]retainedMember
	closed   bool
}

type retainedMember struct {
	file      *os.File
	identity  identity
	directory bool
}

// operationalCloseHook is package-local test instrumentation. Production
// close has no hook and keeps the lease until all other descriptors are gone.
var operationalCloseHook func(string)

type operationalSource struct {
	name string
	file *os.File
	stat unix.Stat_t
}

func openOperationalHome(ctx context.Context, home string) (_ *OperationalHome, resultErr error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	var anchorFile *os.File
	var anchorStat unix.Stat_t
	var members map[string]retainedMember
	locked := false
	defer func() {
		if cleanup {
			if locked {
				resultErr = errors.Join(resultErr, unix.Flock(int(lockFile.Fd()), unix.LOCK_UN))
			}
			if anchorFile != nil {
				resultErr = errors.Join(resultErr, anchorFile.Close())
			}
			if lockFile != nil {
				resultErr = errors.Join(resultErr, lockFile.Close())
			}
			resultErr = errors.Join(resultErr, homeFile.Close())
		}
	}()
	defer func() {
		if cleanup {
			resultErr = errors.Join(resultErr, closeRetainedMembers(members))
		}
	}()
	if err := recheckBinding(parent.file, base, homeStat); err != nil {
		return nil, err
	}

	lockFile, lockStat, anchorFile, anchorStat, err = openOperationalLockPair(homeFile)
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
	if err := recheckOperationalIdentity(parent, base, homeFile, homeStat, lockFile, lockStat, anchorFile, anchorStat); err != nil {
		return nil, err
	}
	if err := inspectOperationalHome(ctx, homeFile); err != nil {
		return nil, err
	}
	members, err = retainOperationalMembers(homeFile)
	if err != nil {
		return nil, err
	}
	if err := recheckOperationalIdentity(parent, base, homeFile, homeStat, lockFile, lockStat, anchorFile, anchorStat); err != nil {
		return nil, err
	}
	if err := recheckOperationalCensus(parent, base, toIdentity(homeStat), members); err != nil {
		return nil, err
	}
	if err := parent.recheck(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cleanup = false
	return &OperationalHome{state: &operationalHomeState{
		homeName: base,
		parent:   parent,
		home:     homeFile,
		lock:     lockFile,
		anchor:   anchorFile,
		root:     toIdentity(homeStat),
		lockID:   toIdentity(lockStat),
		anchorID: toIdentity(anchorStat),
		members:  members,
	}}, nil
}

func (state *operationalHomeState) close() error {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
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
	for name, member := range state.members {
		if member.file != nil {
			result = errors.Join(result, member.file.Close())
			member.file = nil
			state.members[name] = member
		}
	}
	if state.home != nil {
		result = errors.Join(result, state.home.Close())
		state.home = nil
	}
	if state.anchor != nil {
		if err := state.anchor.Close(); err != nil {
			result = errors.Join(result, errors.Join(ErrUncertain, fmt.Errorf("close operational home lock anchor: %w", err)))
		}
		state.anchor = nil
	}
	if state.parent != nil {
		if err := state.parent.close(); err != nil {
			result = errors.Join(result, errors.Join(ErrUncertain, fmt.Errorf("close operational home ancestry: %w", err)))
		}
		state.parent = nil
	}
	if operationalCloseHook != nil {
		operationalCloseHook("before lock release")
	}
	// Unlocking and closing the lifetime descriptor are the final owned
	// effects. The anchor is only an identity binding; closing it above does
	// not release this descriptor's flock.
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

func (state *operationalHomeState) memberCapability(name string) (MemberCapability, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.parent == nil || state.home == nil {
		return MemberCapability{}, ErrClosed
	}
	if _, ok := state.members[name]; !ok || name == tokenName || name == lockName {
		return MemberCapability{}, fmt.Errorf("%w: member capability is not exposed", ErrInvalidHome)
	}
	return MemberCapability{state: state, name: name}, nil
}

func (state *operationalHomeState) openCapability(name string) (*os.File, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.parent == nil || state.home == nil {
		return nil, ErrClosed
	}
	member, ok := state.members[name]
	if !ok || name == tokenName || name == lockName {
		return nil, fmt.Errorf("%w: member capability is not exposed", ErrInvalidHome)
	}
	if err := recheckOperationalIdentityByState(state); err != nil {
		return nil, errors.Join(ErrUncertain, err)
	}
	var file *os.File
	var stat unix.Stat_t
	var err error
	if member.directory {
		file, stat, err = openDirectoryMember(state.home, name)
	} else {
		file, stat, err = openMember(state.home, name)
	}
	if err != nil {
		return nil, fmt.Errorf("open operational member capability %s: %w", name, err)
	}
	if err := sameMemberIdentity(member.identity, toIdentity(stat), member.directory); err != nil {
		_ = file.Close()
		return nil, errors.Join(ErrUncertain, fmt.Errorf("%w: operational member %s changed: %v", ErrInvalidHome, name, err))
	}
	return file, nil
}

func recheckOperationalIdentityByState(state *operationalHomeState) error {
	if state.parent == nil || state.home == nil || state.lock == nil || state.anchor == nil {
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
	if err := recheckIdentityBinding(state.parent.file, state.homeName, state.root); err != nil {
		return err
	}
	if err := sameFileIdentity(state.lockID, state.lock); err != nil {
		return err
	}
	if err := recheckIdentityBinding(state.home, lockName, state.lockID); err != nil {
		return err
	}
	if err := sameFileIdentity(state.anchorID, state.anchor); err != nil {
		return err
	}
	if err := recheckIdentityBinding(state.home, lockAnchorName, state.anchorID); err != nil {
		return err
	}
	if err := sameObjectIdentity(state.lockID, state.anchorID); err != nil {
		return fmt.Errorf("%w: operational home lock pair differs", ErrInvalidHome)
	}
	if err := state.recheckMembers(); err != nil {
		return err
	}
	return recheckOperationalCensus(state.parent, state.homeName, state.root, state.members)
}

func recheckOperationalIdentity(parent *homeParent, name string, home *os.File, homeStat unix.Stat_t, lock *os.File, lockStat unix.Stat_t, anchor *os.File, anchorStat unix.Stat_t) error {
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
	if err := recheckBinding(home, lockName, lockStat); err != nil {
		return err
	}
	if err := sameFileObjectIdentity(toIdentity(anchorStat), anchor); err != nil {
		return err
	}
	if err := recheckBinding(home, lockAnchorName, anchorStat); err != nil {
		return err
	}
	if lockStat.Dev != anchorStat.Dev || lockStat.Ino != anchorStat.Ino || lockStat.Nlink != 2 || anchorStat.Nlink != 2 {
		return fmt.Errorf("%w: operational home lock pair differs", ErrInvalidHome)
	}
	return nil
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

func sameMemberFileIdentity(expected identity, file *os.File, directory bool) error {
	var stat unix.Stat_t
	if file == nil {
		return fmt.Errorf("operational home descriptor is unavailable")
	}
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return err
	}
	return sameMemberIdentity(expected, toIdentity(stat), directory)
}

func sameMemberIdentity(expected, actual identity, directory bool) error {
	if expected.dev != actual.dev || expected.ino != actual.ino || expected.mode != actual.mode || expected.uid != actual.uid || expected.gid != actual.gid || (!directory && expected.nlink != actual.nlink) {
		return fmt.Errorf("%w: filesystem identity or metadata changed", ErrInvalidHome)
	}
	return nil
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
	if err := sameIdentityBinding(expected, toIdentity(current)); err != nil {
		return fmt.Errorf("%w: operational home member %s identity changed: %v", ErrInvalidHome, name, err)
	}
	return nil
}

func sameIdentityBinding(expected, actual identity) error {
	if expected.dev != actual.dev || expected.ino != actual.ino || expected.mode != actual.mode || expected.uid != actual.uid || expected.gid != actual.gid {
		return errors.New("retained filesystem binding changed")
	}
	if expected.mode&unix.S_IFMT == unix.S_IFREG && expected.nlink != actual.nlink {
		return errors.New("retained filesystem binding link count changed")
	}
	return nil
}

func inspectOperationalHome(ctx context.Context, home *os.File) error {
	names, err := readOperationalCensus(home)
	if err != nil {
		return err
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
	return inspectOperationalDatabase(ctx, home, names[databaseName+"-wal"], names[databaseName+"-shm"])
}

func readOperationalCensus(home *os.File) (map[string]bool, error) {
	names, err := home.Readdirnames(memberCount + 3)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("enumerate operational home: %w", err)
	}
	if len(names) < memberCount || len(names) > memberCount+2 {
		return nil, fmt.Errorf("%w: operational home census has %d entries", ErrInvalidHome, len(names))
	}
	seen := make(map[string]bool, len(names))
	allowed := map[string]bool{
		formatName: true, databaseName: true, tokenName: true, lockName: true,
		lockAnchorName: true,
		runtimesName:   true, changesName: true,
		databaseName + "-wal": true, databaseName + "-shm": true,
	}
	for _, name := range names {
		if seen[name] {
			return nil, fmt.Errorf("%w: duplicate operational home entry", ErrInvalidHome)
		}
		if !allowed[name] {
			return nil, fmt.Errorf("%w: unknown operational home entry %s", ErrInvalidHome, name)
		}
		seen[name] = true
	}
	for _, name := range []string{formatName, databaseName, tokenName, lockName, lockAnchorName, runtimesName, changesName} {
		if !seen[name] {
			return nil, fmt.Errorf("%w: missing operational home member %s", ErrInvalidHome, name)
		}
	}
	if seen[databaseName+"-wal"] != seen[databaseName+"-shm"] {
		return nil, fmt.Errorf("%w: operational SQLite WAL sidecars are incomplete", ErrInvalidHome)
	}
	return seen, nil
}

func openOperationalLockPair(home *os.File) (*os.File, unix.Stat_t, *os.File, unix.Stat_t, error) {
	// Keeping both names bound to one exact two-link inode defeats replacement
	// of either pathname. Replacing both names with a new pair concurrently is
	// a stronger same-EUID namespace attack outside this home contract.
	lock, lockStat, err := openLockMemberRW(home, lockName)
	if err != nil {
		return nil, unix.Stat_t{}, nil, unix.Stat_t{}, err
	}
	anchor, anchorStat, err := openLockMemberRW(home, lockAnchorName)
	if err != nil {
		_ = lock.Close()
		return nil, unix.Stat_t{}, nil, unix.Stat_t{}, err
	}
	if lockStat.Dev != anchorStat.Dev || lockStat.Ino != anchorStat.Ino || lockStat.Nlink != 2 || anchorStat.Nlink != 2 {
		_ = anchor.Close()
		_ = lock.Close()
		return nil, unix.Stat_t{}, nil, unix.Stat_t{}, fmt.Errorf("%w: operational home lock pair differs", ErrInvalidHome)
	}
	return lock, lockStat, anchor, anchorStat, nil
}

func openLockMemberRW(home *os.File, name string) (*os.File, unix.Stat_t, error) {
	fd, err := unix.Openat(int(home.Fd()), name, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, unix.Stat_t{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, unix.Stat_t{}, errors.New("invalid operational home lock descriptor")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, unix.Stat_t{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || !exactMode(uint32(stat.Mode), 0o600) || !exactOwner(uint32(stat.Uid)) || stat.Nlink != 2 || stat.Size != 0 {
		_ = file.Close()
		return nil, unix.Stat_t{}, fmt.Errorf("%w: operational home lock member is not exact owner-only regular 0600", ErrInvalidHome)
	}
	if err := recheckBinding(home, name, stat); err != nil {
		_ = file.Close()
		return nil, unix.Stat_t{}, err
	}
	return file, stat, nil
}

func openLockMember(home *os.File, name string) (*os.File, unix.Stat_t, error) {
	fd, err := unix.Openat(int(home.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, unix.Stat_t{}, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, unix.Stat_t{}, errors.New("invalid operational home lock descriptor")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, unix.Stat_t{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || !exactMode(uint32(stat.Mode), 0o600) || !exactOwner(uint32(stat.Uid)) || stat.Nlink != 2 || stat.Size != 0 {
		_ = file.Close()
		return nil, unix.Stat_t{}, fmt.Errorf("%w: home lock member is not exact owner-only regular 0600", ErrInvalidHome)
	}
	if err := recheckBinding(home, name, stat); err != nil {
		_ = file.Close()
		return nil, unix.Stat_t{}, err
	}
	return file, stat, nil
}

func inspectLockPair(home *os.File) error {
	lock, lockStat, err := openLockMember(home, lockName)
	if err != nil {
		return err
	}
	anchor, anchorStat, anchorErr := openLockMember(home, lockAnchorName)
	lockErr := lock.Close()
	if anchorErr != nil {
		return errors.Join(anchorErr, lockErr)
	}
	anchorCloseErr := anchor.Close()
	if lockStat.Dev != anchorStat.Dev || lockStat.Ino != anchorStat.Ino || lockStat.Nlink != 2 || anchorStat.Nlink != 2 {
		return errors.Join(fmt.Errorf("%w: home lock pair differs", ErrInvalidHome), lockErr, anchorCloseErr)
	}
	return errors.Join(lockErr, anchorCloseErr)
}

func verifyLockPair(home *os.File) error {
	return inspectLockPair(home)
}

func retainOperationalMembers(home *os.File) (map[string]retainedMember, error) {
	members := make(map[string]retainedMember, 6)
	for _, name := range []string{formatName, databaseName, tokenName} {
		file, stat, err := openMember(home, name)
		if err != nil {
			closeRetainedMembers(members)
			return nil, fmt.Errorf("retain operational home member %s: %w", name, err)
		}
		members[name] = retainedMember{file: file, identity: toIdentity(stat)}
	}
	for _, name := range []string{runtimesName, changesName} {
		file, stat, err := openDirectoryMember(home, name)
		if err != nil {
			closeRetainedMembers(members)
			return nil, fmt.Errorf("retain operational home directory %s: %w", name, err)
		}
		members[name] = retainedMember{file: file, identity: toIdentity(stat), directory: true}
	}
	for _, name := range []string{databaseName + "-wal", databaseName + "-shm"} {
		present, err := presentAt(home, name)
		if err != nil {
			closeRetainedMembers(members)
			return nil, err
		}
		if !present {
			continue
		}
		file, stat, err := openMember(home, name)
		if err != nil {
			closeRetainedMembers(members)
			return nil, fmt.Errorf("retain operational SQLite sidecar %s: %w", name, err)
		}
		minimum, maximum := operationalSidecarBounds(name)
		if stat.Size < minimum || stat.Size > maximum {
			_ = file.Close()
			closeRetainedMembers(members)
			return nil, fmt.Errorf("%w: operational SQLite sidecar %s size is outside bounds", ErrInvalidHome, name)
		}
		members[name] = retainedMember{file: file, identity: toIdentity(stat)}
	}
	if (members[databaseName+"-wal"].file == nil) != (members[databaseName+"-shm"].file == nil) {
		closeRetainedMembers(members)
		return nil, fmt.Errorf("%w: operational SQLite sidecars are incomplete", ErrInvalidHome)
	}
	return members, nil
}

func closeRetainedMembers(members map[string]retainedMember) error {
	var result error
	for name, member := range members {
		if member.file != nil {
			result = errors.Join(result, member.file.Close())
			member.file = nil
			members[name] = member
		}
	}
	return result
}

func (state *operationalHomeState) recheckMembers() error {
	for name, member := range state.members {
		if member.file == nil {
			return fmt.Errorf("%w: retained operational member %s is closed", ErrUncertain, name)
		}
		if member.directory {
			if err := exactDirectory(member.file, false); err != nil {
				return err
			}
		}
		if err := sameMemberFileIdentity(member.identity, member.file, member.directory); err != nil {
			return err
		}
		if err := recheckIdentityBinding(state.home, name, member.identity); err != nil {
			return err
		}
	}
	return nil
}

func recheckOperationalCensus(parent *homeParent, name string, expected identity, members map[string]retainedMember) error {
	root, stat, err := openDirectoryMember(parent.file, name)
	if err != nil {
		return fmt.Errorf("recheck operational home: %w", err)
	}
	seen, censusErr := readOperationalCensus(root)
	if censusErr != nil {
		return errors.Join(censusErr, root.Close())
	}
	if err := sameObjectIdentity(expected, toIdentity(stat)); err != nil {
		return errors.Join(err, root.Close())
	}
	for _, name := range []string{formatName, databaseName, tokenName, runtimesName, changesName, lockName, lockAnchorName} {
		if !seen[name] {
			return errors.Join(fmt.Errorf("%w: operational member %s disappeared", ErrInvalidHome, name), root.Close())
		}
	}
	for _, name := range []string{databaseName + "-wal", databaseName + "-shm"} {
		_, retained := members[name]
		if retained != seen[name] {
			return errors.Join(fmt.Errorf("%w: operational SQLite sidecar %s presence changed", ErrUncertain, name), root.Close())
		}
		if seen[name] {
			file, sidecarStat, openErr := openMember(root, name)
			if openErr != nil {
				return errors.Join(openErr, root.Close())
			}
			minimum, maximum := operationalSidecarBounds(name)
			if sidecarStat.Size < minimum || sidecarStat.Size > maximum {
				_ = file.Close()
				return errors.Join(fmt.Errorf("%w: operational SQLite sidecar %s size is outside bounds", ErrInvalidHome, name), root.Close())
			}
			if member, retained := members[name]; retained {
				if err := sameMemberIdentity(member.identity, toIdentity(sidecarStat), false); err != nil {
					_ = file.Close()
					return errors.Join(fmt.Errorf("%w: operational SQLite sidecar %s changed: %v", ErrInvalidHome, name, err), root.Close())
				}
			}
			if closeErr := file.Close(); closeErr != nil {
				return errors.Join(closeErr, root.Close())
			}
		}
	}
	return root.Close()
}

func operationalSidecarBounds(name string) (int64, int64) {
	switch name {
	case databaseName + "-wal":
		return 0, operationalMaxWALBytes
	case databaseName + "-shm":
		return operationalMinSHMBytes, operationalMaxSHMBytes
	default:
		return 0, 0
	}
}

func inspectOperationalDatabase(ctx context.Context, home *os.File, walPresent, shmPresent bool) error {
	if walPresent != shmPresent {
		return fmt.Errorf("%w: operational SQLite WAL sidecars are incomplete", ErrInvalidHome)
	}
	sources := make([]operationalSource, 0, 3)
	for _, name := range []string{databaseName, databaseName + "-wal", databaseName + "-shm"} {
		if name != databaseName && !walPresent {
			continue
		}
		file, stat, err := openMember(home, name)
		if err != nil {
			return fmt.Errorf("inspect operational SQLite %s: %w", name, err)
		}
		minimum, maximum := int64(100), int64(operationalMaxDatabaseBytes)
		if name != databaseName {
			minimum, maximum = operationalSidecarBounds(name)
		}
		if stat.Size < minimum || stat.Size > maximum {
			file.Close()
			return fmt.Errorf("%w: operational SQLite %s size is outside bounds", ErrInvalidHome, name)
		}
		sources = append(sources, operationalSource{name: name, file: file, stat: stat})
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
