//go:build darwin

package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

const runtimeParentLockName = ".runtime.lock"

var errRuntimeBusy = errors.New("daemon: runtime ownership busy")

// RuntimeParent is the one concrete managed-runtime namespace capability.
// Its durable lock inode is created once during daemon initialization; each
// mutating operation independently reopens and locks that exact inode.
type RuntimeParent struct {
	mu           sync.Mutex
	path         string
	dir          *os.File
	identity     directoryIdentity
	lockIdentity runner.FileIdentity
}

func CreateRuntimeParent(parent *os.File) (_ *RuntimeParent, resultErr error) {
	return createRuntimeParent(parent, nil, unix.Fsync)
}

func createRuntimeParent(parent *os.File, afterCreate func(), syncDirectory func(int) error) (_ *RuntimeParent, resultErr error) {
	path, identity, err := validateRuntimeParentDescriptor(parent)
	if err != nil {
		return nil, err
	}
	if syncDirectory == nil {
		syncDirectory = unix.Fsync
	}
	fd, err := unix.Openat(int(parent.Fd()), runtimeParentLockName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, invalidContract(err)
	}
	created := os.NewFile(uintptr(fd), "runtime-parent-lock-init")
	cleanup := false
	var createdStat unix.Stat_t
	if err := unix.Fstat(fd, &createdStat); err != nil {
		created.Close()
		return nil, invalidContract(err)
	}
	defer func() {
		created.Close()
		if cleanup {
			if cleanupErr := cleanupCreatedRuntimeLock(int(parent.Fd()), createdStat); cleanupErr != nil {
				resultErr = retainedContract(errors.Join(resultErr, cleanupErr))
			}
		}
	}()
	lockIdentity, err := inspectRuntimeLock(fd)
	if err != nil {
		return nil, err
	}
	cleanup = true
	if err := syncDirectory(fd); err != nil {
		return nil, invalidContract(err)
	}
	if err := syncDirectory(int(parent.Fd())); err != nil {
		return nil, invalidContract(err)
	}
	if afterCreate != nil {
		afterCreate()
	}
	if named, err := inspectNamedRuntimeLock(int(parent.Fd())); err != nil || named != lockIdentity {
		return nil, invalidContract(err)
	}
	opened, err := openRuntimeParent(path, identity, lockIdentity)
	if err != nil {
		return nil, err
	}
	cleanup = false
	return opened, nil
}

func OpenRuntimeParent(parent *os.File) (*RuntimeParent, error) {
	path, identity, err := validateRuntimeParentDescriptor(parent)
	if err != nil {
		return nil, err
	}
	lockIdentity, err := inspectNamedRuntimeLock(int(parent.Fd()))
	if err != nil {
		return nil, err
	}
	return openRuntimeParent(path, identity, lockIdentity)
}

func openRuntimeParent(path string, identity directoryIdentity, lockIdentity runner.FileIdentity) (*RuntimeParent, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, invalidContract(err)
	}
	dir := os.NewFile(uintptr(fd), "runtime-parent")
	actual, inspectErr := inspectPrivateDirectory(fd)
	if inspectErr != nil || actual != identity || verifyNamedDirectory(path, identity) != nil {
		dir.Close()
		return nil, invalidContract(inspectErr)
	}
	return &RuntimeParent{path: path, dir: dir, identity: identity, lockIdentity: lockIdentity}, nil
}

func validateRuntimeParentDescriptor(parent *os.File) (string, directoryIdentity, error) {
	if parent == nil {
		return "", directoryIdentity{}, invalidContract(nil)
	}
	path, err := descriptorPath(parent)
	if err != nil || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", directoryIdentity{}, invalidContract(err)
	}
	identity, err := inspectPrivateDirectory(int(parent.Fd()))
	if err != nil || verifyNamedDirectory(path, identity) != nil {
		return "", directoryIdentity{}, invalidContract(err)
	}
	return path, identity, nil
}

func inspectRuntimeLock(fd int) (runner.FileIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return runner.FileIdentity{}, invalidContract(err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || stat.Size != 0 || stat.Dev == 0 || stat.Ino == 0 {
		return runner.FileIdentity{}, invalidContract(nil)
	}
	return runner.FileIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, nil
}

func inspectNamedRuntimeLock(parentFD int) (runner.FileIdentity, error) {
	fd, err := unix.Openat(parentFD, runtimeParentLockName, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return runner.FileIdentity{}, invalidContract(err)
	}
	identity, inspectErr := inspectRuntimeLock(fd)
	closeErr := unix.Close(fd)
	if inspectErr != nil || closeErr != nil {
		return runner.FileIdentity{}, invalidContract(errors.Join(inspectErr, closeErr))
	}
	return identity, nil
}

func cleanupCreatedRuntimeLock(parentFD int, created unix.Stat_t) error {
	var named unix.Stat_t
	if err := unix.Fstatat(parentFD, runtimeParentLockName, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if !sameFileObject(created, named) || named.Mode&unix.S_IFMT != unix.S_IFREG || named.Mode&0o7777 != 0o600 || named.Uid != uint32(os.Geteuid()) || named.Nlink != 1 || named.Size != 0 {
		return errRetainedRuntime
	}
	if err := unix.Unlinkat(parentFD, runtimeParentLockName, 0); err != nil {
		return err
	}
	return unix.Fsync(parentFD)
}

type runtimeParentOperation struct {
	dir  *os.File
	lock *os.File
}

func (parent *RuntimeParent) begin() (*runtimeParentOperation, error) {
	if parent == nil {
		return nil, invalidContract(nil)
	}
	parent.mu.Lock()
	if parent.dir == nil {
		parent.mu.Unlock()
		return nil, invalidContract(nil)
	}
	path, identity, lockIdentity := parent.path, parent.identity, parent.lockIdentity
	parent.mu.Unlock()
	dirFD, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, invalidContract(err)
	}
	dir := os.NewFile(uintptr(dirFD), "runtime-parent-operation")
	fail := func(cause error) (*runtimeParentOperation, error) {
		dir.Close()
		return nil, invalidContract(cause)
	}
	actual, err := inspectPrivateDirectory(dirFD)
	if err != nil || actual != identity || verifyNamedDirectory(path, identity) != nil {
		return fail(err)
	}
	lockFD, err := unix.Openat(dirFD, runtimeParentLockName, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return fail(err)
	}
	lock := os.NewFile(uintptr(lockFD), "runtime-parent-operation-lock")
	gotLock, err := inspectRuntimeLock(lockFD)
	if err != nil || gotLock != lockIdentity {
		lock.Close()
		return fail(err)
	}
	if err := unix.Flock(lockFD, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		dir.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errRuntimeBusy
		}
		return nil, invalidContract(err)
	}
	if got, err := inspectRuntimeLock(lockFD); err != nil || got != lockIdentity {
		unix.Flock(lockFD, unix.LOCK_UN)
		lock.Close()
		dir.Close()
		return nil, invalidContract(err)
	}
	// This advisory lock serializes cooperating Dark Factory owners. A hostile
	// same-EUID process that deliberately ignores it remains outside the threat
	// boundary documented by SECURITY.md.
	namedLock, namedErr := inspectNamedRuntimeLock(dirFD)
	if namedErr != nil || namedLock != lockIdentity || verifyNamedDirectory(path, identity) != nil {
		unix.Flock(lockFD, unix.LOCK_UN)
		lock.Close()
		dir.Close()
		return nil, invalidContract(namedErr)
	}
	return &runtimeParentOperation{dir: dir, lock: lock}, nil
}

func (operation *runtimeParentOperation) takeDirectory() *os.File {
	dir := operation.dir
	operation.dir = nil
	return dir
}

func (operation *runtimeParentOperation) Close() error {
	if operation == nil {
		return nil
	}
	var unlockErr, lockErr, dirErr error
	if operation.lock != nil {
		unlockErr = unix.Flock(int(operation.lock.Fd()), unix.LOCK_UN)
		lockErr = operation.lock.Close()
		operation.lock = nil
	}
	if operation.dir != nil {
		dirErr = operation.dir.Close()
		operation.dir = nil
	}
	return errors.Join(unlockErr, lockErr, dirErr)
}

func (parent *RuntimeParent) Close() error {
	if parent == nil {
		return nil
	}
	parent.mu.Lock()
	defer parent.mu.Unlock()
	if parent.dir == nil {
		return nil
	}
	err := parent.dir.Close()
	parent.dir = nil
	return err
}
