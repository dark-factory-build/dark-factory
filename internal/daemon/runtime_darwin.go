//go:build darwin

package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"github.com/dark-factory-build/dark-factory/internal/runner"
	"golang.org/x/sys/unix"
)

const (
	workerConfigName   = "change-worker.config"
	cleanupSearchLimit = 128
)

type RuntimeLeasePresence uint8

const (
	RuntimeLeaseHeld RuntimeLeasePresence = iota + 1
	RuntimeLeaseAvailable
)

type Runtime struct {
	mu             sync.Mutex
	path           string
	basename       string
	dir            *os.File
	directory      directoryIdentity
	parent         *os.File
	parentPath     string
	parentIdentity directoryIdentity
	parentLock     runner.FileIdentity
	identity       runner.FileIdentity
	home           directoryIdentity
	temp           directoryIdentity
	lifetime       *os.File
	lifetimeID     runner.FileIdentity
}

// RuntimeBinding is the non-forgeable per-run filesystem capability. Every
// locator method revalidates the retained root and exact fixed child identities;
// it is deliberately distinct from the daemon-global API socket authority.
type RuntimeBinding struct{ runtime *Runtime }

type PrivateFile struct {
	runtime  *Runtime
	name     string
	identity runner.FileIdentity
	size     int64
}

func (file PrivateFile) Path() (string, error) {
	if file.runtime == nil || !validBasename(file.name) || file.identity.Device == 0 || file.identity.Inode == 0 || file.size <= 0 {
		return "", invalidContract(nil)
	}
	file.runtime.mu.Lock()
	defer file.runtime.mu.Unlock()
	if err := file.runtime.verifyAuthority(); err != nil {
		return "", invalidContract(err)
	}
	if err := verifyPrivateFileAuthority(file.runtime, file.name, file.identity, file.size); err != nil {
		return "", invalidContract(err)
	}
	return filepath.Join(file.runtime.path, file.name), nil
}

func (file PrivateFile) Identity() runner.FileIdentity { return file.identity }
func (PrivateFile) String() string                     { return "private runtime file" }
func (PrivateFile) GoString() string                   { return "daemon.PrivateFile{private}" }

func CreateRuntime(parent *RuntimeParent, basename string) (*Runtime, error) {
	return createRuntime(parent, basename, nil, nil, nil)
}

// AdoptRuntime recovers only a declared runtime whose creation died before
// Binding could be durably registered. The named root may be empty or contain
// empty, exact home/tmp children; any later-phase effect is not adoptable.
func AdoptRuntime(parent *RuntimeParent, basename string) (*Runtime, error) {
	if parent == nil || !validBasename(basename) {
		return nil, invalidContract(nil)
	}
	operation, err := parent.begin()
	if err != nil {
		return nil, err
	}
	defer operation.Close()
	parentFD := int(operation.dir.Fd())
	created, err := inspectNamedPrivateDirectory(parentFD, basename)
	if err != nil {
		return nil, invalidContract(err)
	}
	fd, err := unix.Openat(parentFD, basename, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, invalidContract(err)
	}
	dir := os.NewFile(uintptr(fd), "adopted-attempt-runtime")
	keepDir := false
	defer func() {
		if !keepDir {
			dir.Close()
		}
	}()
	lifetime, lifetimeID, err := adoptRuntimeLayout(fd, created, unix.Fsync)
	if err != nil {
		return nil, err
	}
	keepLifetime := false
	defer func() {
		if !keepLifetime {
			lifetime.Close()
		}
	}()
	home, temp, err := inspectRuntimeLayout(fd, created)
	if err != nil {
		return nil, invalidContract(err)
	}
	if err := unix.Fsync(fd); err != nil {
		return nil, invalidContract(err)
	}
	if err := verifyNamedChild(parentFD, basename, created); err != nil {
		return nil, invalidContract(err)
	}
	if err := unix.Fsync(parentFD); err != nil {
		return nil, invalidContract(err)
	}
	runtime := &Runtime{
		path: filepath.Join(parent.path, basename), basename: basename, dir: dir, directory: created,
		parent: operation.dir, parentPath: parent.path, parentIdentity: parent.identity,
		parentLock: parent.lockIdentity, identity: created.fileIdentity(), home: home, temp: temp,
		lifetime: lifetime, lifetimeID: lifetimeID,
	}
	if err := runtime.verifyAuthority(); err != nil {
		return nil, invalidContract(err)
	}
	keepDir = true
	keepLifetime = true
	operation.takeDirectory()
	return runtime, nil
}

func createRuntime(parent *RuntimeParent, basename string, beforeCreate, afterOpen func(), syncDirectory func(int) error) (_ *Runtime, resultErr error) {
	if parent == nil || !validBasename(basename) {
		return nil, invalidContract(nil)
	}
	if syncDirectory == nil {
		syncDirectory = unix.Fsync
	}
	operation, err := parent.begin()
	if err != nil {
		return nil, err
	}
	defer operation.Close()
	ownedParent := operation.dir
	parentFD := int(ownedParent.Fd())
	parentPath, wantParent, lockIdentity := parent.path, parent.identity, parent.lockIdentity
	if err := verifyDirectoryDescriptor(ownedParent, parentPath, wantParent); err != nil {
		return nil, invalidContract(err)
	}
	var present unix.Stat_t
	err = unix.Fstatat(parentFD, basename, &present, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil || !errors.Is(err, unix.ENOENT) {
		return nil, invalidContract(err)
	}
	if beforeCreate != nil {
		beforeCreate()
	}
	if err := verifyDirectoryDescriptor(ownedParent, parentPath, wantParent); err != nil {
		return nil, invalidContract(err)
	}
	if err := unix.Mkdirat(parentFD, basename, 0o700); err != nil {
		return nil, invalidContract(err)
	}
	created, err := inspectNamedPrivateDirectory(parentFD, basename)
	if err != nil {
		return nil, retainedContract(err)
	}
	createdName := basename
	cleanupCreated := true
	defer func() {
		if !cleanupCreated {
			return
		}
		cleanupErr := cleanupCreatedDirectory(parentFD, createdName, created, syncDirectory)
		if cleanupErr != nil {
			resultErr = retainedContract(errors.Join(resultErr, cleanupErr))
		}
	}()
	fd, err := unix.Openat(parentFD, basename, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, invalidContract(err)
	}
	dir := os.NewFile(uintptr(fd), "attempt-runtime")
	keepDir := false
	defer func() {
		if !keepDir {
			_ = dir.Close()
		}
	}()
	if afterOpen != nil {
		afterOpen()
	}
	opened, openedErr := inspectPrivateDirectory(fd)
	namedErr := verifyNamedChild(parentFD, basename, created)
	parentErr := verifyDirectoryDescriptor(ownedParent, parentPath, wantParent)
	if openedErr != nil || opened != created || namedErr != nil || parentErr != nil {
		return nil, invalidContract(errors.Join(openedErr, namedErr, parentErr))
	}
	lifetime, lifetimeID, err := createRuntimeLayout(fd, syncDirectory)
	if err != nil {
		return nil, err
	}
	keepLifetime := false
	defer func() {
		if !keepLifetime {
			lifetime.Close()
		}
	}()
	home, temp, err := inspectRuntimeLayout(fd, created)
	if err != nil {
		return nil, invalidContract(err)
	}
	if err := syncDirectory(fd); err != nil {
		return nil, invalidContract(err)
	}
	if err := verifyNamedChild(parentFD, basename, created); err != nil {
		return nil, invalidContract(err)
	}
	if err := syncDirectory(parentFD); err != nil {
		return nil, invalidContract(err)
	}
	runtime := &Runtime{
		path: filepath.Join(parentPath, basename), basename: basename, dir: dir, directory: created,
		parent: ownedParent, parentPath: parentPath, parentIdentity: wantParent, parentLock: lockIdentity,
		identity: created.fileIdentity(), home: home, temp: temp, lifetime: lifetime, lifetimeID: lifetimeID,
	}
	if err := runtime.verifyAuthority(); err != nil {
		return nil, invalidContract(err)
	}
	cleanupCreated = false
	keepDir = true
	keepLifetime = true
	operation.takeDirectory()
	return runtime, nil
}

func (runtime *Runtime) Binding() (*RuntimeBinding, error) {
	if runtime == nil {
		return nil, invalidContract(nil)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if err := runtime.verifyAuthority(); err != nil {
		return nil, invalidContract(err)
	}
	return &RuntimeBinding{runtime: runtime}, nil
}

func (binding *RuntimeBinding) Values() (string, runner.FileIdentity, error) {
	if binding == nil || binding.runtime == nil {
		return "", runner.FileIdentity{}, invalidContract(nil)
	}
	binding.runtime.mu.Lock()
	defer binding.runtime.mu.Unlock()
	if err := binding.runtime.verifyAuthority(); err != nil {
		return "", runner.FileIdentity{}, invalidContract(err)
	}
	return binding.runtime.path, binding.runtime.identity, nil
}

func (binding *RuntimeBinding) ProviderHome() (string, error) {
	return binding.fixedDirectory(runtimeHomeName)
}

func (binding *RuntimeBinding) ProviderTemp() (string, error) {
	return binding.fixedDirectory(runtimeTempName)
}

func (binding *RuntimeBinding) AttemptTokenPath() (string, error) {
	return binding.fixedFile(attemptTokenName)
}

func (binding *RuntimeBinding) WorkerConfigPath() (string, error) {
	return binding.fixedFile(workerConfigName)
}

func (binding *RuntimeBinding) fixedDirectory(name string) (string, error) {
	if binding == nil || binding.runtime == nil {
		return "", invalidContract(nil)
	}
	binding.runtime.mu.Lock()
	defer binding.runtime.mu.Unlock()
	if err := binding.runtime.verifyAuthority(); err != nil {
		return "", invalidContract(err)
	}
	if name != runtimeHomeName && name != runtimeTempName {
		return "", invalidContract(nil)
	}
	return filepath.Join(binding.runtime.path, name), nil
}

func (binding *RuntimeBinding) fixedFile(name string) (string, error) {
	if binding == nil || binding.runtime == nil || name != attemptTokenName && name != workerConfigName {
		return "", invalidContract(nil)
	}
	binding.runtime.mu.Lock()
	defer binding.runtime.mu.Unlock()
	if err := binding.runtime.verifyAuthority(); err != nil {
		return "", invalidContract(err)
	}
	return filepath.Join(binding.runtime.path, name), nil
}

// DuplicateRunnerFiles transfers the exact private directory needed by the
// wrapper and a least-privilege empty regular lifetime file. The returned
// files share the Runtime's lifetime lock open-file description.
func (runtime *Runtime) DuplicateRunnerFiles() (*os.File, *os.File, error) {
	if runtime == nil {
		return nil, nil, invalidContract(nil)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if err := runtime.verifyAuthority(); err != nil {
		return nil, nil, invalidContract(err)
	}
	fd, err := unix.FcntlInt(runtime.dir.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, nil, invalidContract(err)
	}
	duplicate := os.NewFile(uintptr(fd), "attempt-runtime-duplicate")
	if _, err := inspectExpectedDirectory(fd, runtime.identity); err != nil {
		duplicate.Close()
		return nil, nil, invalidContract(err)
	}
	lifetimeFD, err := unix.FcntlInt(runtime.lifetime.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		duplicate.Close()
		return nil, nil, invalidContract(err)
	}
	lifetime := os.NewFile(uintptr(lifetimeFD), "runtime-lifetime-duplicate")
	if err := runtime.verifyAuthority(); err != nil {
		duplicate.Close()
		lifetime.Close()
		return nil, nil, invalidContract(err)
	}
	return duplicate, lifetime, nil
}

// ObserveRuntimeLifetime tests the exact runtime-root lock using a fresh open
// file description. Held means a cooperating live owner still retains the
// inherited lifetime lease. Available is positive evidence that no such owner
// does. Errors, replacement, and missing authorities are never absence.
func ObserveRuntimeLifetime(parent *RuntimeParent, basename string, expected runner.FileIdentity) (RuntimeLeasePresence, error) {
	if parent == nil || !validBasename(basename) || expected.Device == 0 || expected.Inode == 0 {
		return 0, invalidContract(nil)
	}
	operation, err := parent.begin()
	if err != nil {
		return 0, err
	}
	defer operation.Close()
	fd, err := unix.Openat(int(operation.dir.Fd()), basename, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return 0, invalidContract(err)
	}
	root := os.NewFile(uintptr(fd), "runtime-lifetime-observation")
	defer root.Close()
	identity, err := inspectExpectedDirectory(fd, expected)
	if err != nil || verifyNamedChild(int(operation.dir.Fd()), basename, identity) != nil {
		return 0, invalidContract(err)
	}
	lease, _, err := openRuntimeLifetime(fd, identity.device, nil)
	if errors.Is(err, errRuntimeBusy) {
		return RuntimeLeaseHeld, nil
	}
	if err != nil {
		return 0, err
	}
	defer lease.Close()
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return RuntimeLeaseHeld, nil
		}
		return 0, invalidContract(err)
	}
	defer unix.Flock(int(lease.Fd()), unix.LOCK_UN)
	if _, err := inspectExpectedDirectory(fd, expected); err != nil || verifyNamedChild(int(operation.dir.Fd()), basename, identity) != nil {
		return 0, invalidContract(err)
	}
	return RuntimeLeaseAvailable, nil
}

func (runtime *Runtime) PublishAttemptToken(ctx context.Context, token [32]byte) (PrivateFile, error) {
	return runtime.publish(ctx, attemptTokenName, token[:], len(token), nil, nil)
}

func (runtime *Runtime) PublishWorkerConfig(ctx context.Context, config workerConfig) (PrivateFile, error) {
	if runtime == nil {
		return PrivateFile{}, invalidContract(nil)
	}
	runtime.mu.Lock()
	bound := runtime.dir != nil && config.RuntimePath == runtime.path && config.RuntimeIdentity == runtime.identity
	runtime.mu.Unlock()
	if !bound {
		return PrivateFile{}, invalidContract(nil)
	}
	encoded, err := encodeWorkerConfig(config)
	if err != nil {
		return PrivateFile{}, invalidContract(nil)
	}
	return runtime.publish(ctx, workerConfigName, encoded, workerConfigLimit, nil, nil)
}

func (runtime *Runtime) publish(ctx context.Context, name string, value []byte, limit int, write privateWrite, syncDirectory func(int) error) (_ PrivateFile, resultErr error) {
	if runtime == nil || ctx == nil || !validBasename(name) || len(value) == 0 || len(value) > limit {
		return PrivateFile{}, invalidContract(nil)
	}
	if syncDirectory == nil {
		syncDirectory = unix.Fsync
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	if err := runtime.verifyAuthority(); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	fd, err := unix.Openat(int(runtime.dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0o600)
	if err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	defer unix.Close(fd)
	var created unix.Stat_t
	if err := unix.Fstat(fd, &created); err != nil || !validCleanupPrivateFile(created) {
		return PrivateFile{}, retainedContract(err)
	}
	cleanupCreated := true
	defer func() {
		if !cleanupCreated {
			return
		}
		cleanupErr := cleanupCreatedFile(int(runtime.dir.Fd()), name, created, syncDirectory)
		if cleanupErr != nil {
			resultErr = retainedContract(errors.Join(resultErr, cleanupErr))
		}
	}()
	if !validPrivateFile(created, 0) {
		return PrivateFile{}, invalidContract(nil)
	}
	if err := runtime.verifyAuthority(); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	if write == nil {
		write = unix.Write
	}
	if err := writePrivate(ctx, fd, value, write); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	if err := runtime.verifyAuthority(); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	if err := syncDirectory(fd); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	if err := runtime.verifyAuthority(); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !validPrivateFile(opened, int64(len(value))) || !sameFileObject(created, opened) {
		return PrivateFile{}, invalidContract(err)
	}
	identity := runner.FileIdentity{Device: uint64(opened.Dev), Inode: opened.Ino}
	if err := verifyPrivateFileAuthority(runtime, name, identity, opened.Size); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	if err := syncDirectory(int(runtime.dir.Fd())); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	if err := runtime.verifyAuthority(); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	if err := verifyPrivateFileAuthority(runtime, name, identity, opened.Size); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	cleanupCreated = false
	return PrivateFile{runtime: runtime, name: name, identity: identity, size: opened.Size}, nil
}

func (runtime *Runtime) verifyAuthority() error {
	if runtime == nil || runtime.dir == nil || runtime.lifetime == nil || runtime.parent == nil || !validBasename(runtime.basename) || runtime.path != filepath.Join(runtime.parentPath, runtime.basename) {
		return errInvalidContract
	}
	opened, err := inspectPrivateDirectory(int(runtime.dir.Fd()))
	if err != nil || opened != runtime.directory || opened.fileIdentity() != runtime.identity {
		return errInvalidContract
	}
	if err := verifyDirectoryDescriptor(runtime.parent, runtime.parentPath, runtime.parentIdentity); err != nil {
		return errInvalidContract
	}
	if err := verifyNamedChild(int(runtime.parent.Fd()), runtime.basename, runtime.directory); err != nil {
		return errInvalidContract
	}
	if err := verifyNamedDirectory(runtime.path, runtime.directory); err != nil {
		return err
	}
	if err := verifyRuntimeLayoutDirectory(runtime, runtimeHomeName, runtime.home); err != nil {
		return err
	}
	if err := verifyRuntimeLayoutDirectory(runtime, runtimeTempName, runtime.temp); err != nil {
		return err
	}
	var lifetime, namedLifetime unix.Stat_t
	if err := unix.Fstat(int(runtime.lifetime.Fd()), &lifetime); err != nil || !validRuntimeLifetime(lifetime, runtime.directory.device) || (runner.FileIdentity{Device: uint64(lifetime.Dev), Inode: lifetime.Ino}) != runtime.lifetimeID {
		return errInvalidContract
	}
	if err := unix.Fstatat(int(runtime.dir.Fd()), runner.RuntimeLifetimeLeaseName, &namedLifetime, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameFileObject(lifetime, namedLifetime) || !validRuntimeLifetime(namedLifetime, runtime.directory.device) {
		return errInvalidContract
	}
	if err := unix.Flock(int(runtime.lifetime.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errInvalidContract
	}
	lock, err := inspectNamedRuntimeLock(int(runtime.parent.Fd()))
	if err != nil || lock != runtime.parentLock {
		return errInvalidContract
	}
	return nil
}

func verifyRuntimeLayoutDirectory(runtime *Runtime, name string, expected directoryIdentity) error {
	if expected.device != runtime.directory.device {
		return errInvalidContract
	}
	fd, err := unix.Openat(int(runtime.dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return err
	}
	actual, inspectErr := inspectPrivateDirectory(fd)
	closeErr := unix.Close(fd)
	if inspectErr != nil || closeErr != nil || actual != expected {
		return errors.Join(errInvalidContract, inspectErr, closeErr)
	}
	if err := verifyNamedChild(int(runtime.dir.Fd()), name, expected); err != nil {
		return err
	}
	return verifyNamedDirectory(filepath.Join(runtime.path, name), expected)
}

func verifyPrivateFileAuthority(runtime *Runtime, name string, identity runner.FileIdentity, size int64) error {
	var named unix.Stat_t
	if err := unix.Fstatat(int(runtime.dir.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || !validPrivateFile(named, size) || uint64(named.Dev) != identity.Device || named.Ino != identity.Inode {
		return errInvalidContract
	}
	var absolute unix.Stat_t
	if err := unix.Lstat(filepath.Join(runtime.path, name), &absolute); err != nil || !samePrivateFile(named, absolute) {
		return errInvalidContract
	}
	return nil
}

func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.dir == nil && runtime.lifetime == nil && runtime.parent == nil {
		return nil
	}
	var dirErr, lifetimeErr, parentErr error
	if runtime.lifetime != nil {
		lifetimeErr = runtime.lifetime.Close()
		runtime.lifetime = nil
	}
	if runtime.dir != nil {
		dirErr = runtime.dir.Close()
		runtime.dir = nil
	}
	if runtime.parent != nil {
		parentErr = runtime.parent.Close()
		runtime.parent = nil
	}
	return errors.Join(dirErr, lifetimeErr, parentErr)
}

type privateWrite func(int, []byte) (int, error)

func writePrivate(ctx context.Context, fd int, value []byte, write privateWrite) error {
	for len(value) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		written, err := write(fd, value)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if written <= 0 || written > len(value) {
			return io.ErrShortWrite
		}
		value = value[written:]
	}
	return nil
}

func cleanupCreatedDirectory(parentFD int, preferred string, identity directoryIdentity, syncDirectory func(int) error) error {
	name, err := locateExactEntry(parentFD, preferred, identity.device, identity.inode, true)
	if err != nil {
		return err
	}
	if name == "" {
		return errRetainedRuntime
	}
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return err
	}
	directory := os.NewFile(uintptr(fd), "failed-attempt-runtime")
	if err := cleanupRuntimeLayout(fd, identity, syncDirectory); err != nil {
		directory.Close()
		return err
	}
	entries, readErr := directory.ReadDir(1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || len(entries) != 0 || closeErr != nil {
		return errRetainedRuntime
	}
	if err := verifyNamedChild(parentFD, name, identity); err != nil {
		return errRetainedRuntime
	}
	if err := unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR); err != nil {
		return err
	}
	return syncDirectory(parentFD)
}

func createRuntimeLayout(rootFD int, syncDirectory func(int) error) (*os.File, runner.FileIdentity, error) {
	root, err := inspectPrivateDirectory(rootFD)
	if err != nil {
		return nil, runner.FileIdentity{}, invalidContract(err)
	}
	for _, name := range []string{runtimeHomeName, runtimeTempName} {
		if err := createRuntimeLayoutChild(rootFD, name, root, syncDirectory); err != nil {
			return nil, runner.FileIdentity{}, err
		}
	}
	return createRuntimeLifetime(rootFD, syncDirectory)
}

func inspectRuntimeLayout(rootFD int, root directoryIdentity) (directoryIdentity, directoryIdentity, error) {
	identities := make([]directoryIdentity, 0, 2)
	for _, name := range []string{runtimeHomeName, runtimeTempName} {
		fd, err := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
		if err != nil {
			return directoryIdentity{}, directoryIdentity{}, err
		}
		identity, inspectErr := inspectPrivateDirectory(fd)
		closeErr := unix.Close(fd)
		if inspectErr != nil || closeErr != nil || identity.device != root.device {
			return directoryIdentity{}, directoryIdentity{}, errors.Join(inspectErr, closeErr, errInvalidContract)
		}
		identities = append(identities, identity)
	}
	return identities[0], identities[1], nil
}

func adoptRuntimeLayout(rootFD int, root directoryIdentity, syncDirectory func(int) error) (*os.File, runner.FileIdentity, error) {
	entries, more, err := readRuntimeEntries(rootFD, 3)
	if err != nil || more {
		return nil, runner.FileIdentity{}, invalidContract(err)
	}
	homePresent := false
	tempPresent := false
	lifetimePresent := false
	for _, name := range entries {
		if name == runner.RuntimeLifetimeLeaseName {
			var named unix.Stat_t
			if err := unix.Fstatat(rootFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || !validRuntimeLifetime(named, root.device) {
				return nil, runner.FileIdentity{}, invalidContract(err)
			}
			lifetimePresent = true
			continue
		}
		if name != runtimeHomeName && name != runtimeTempName {
			return nil, runner.FileIdentity{}, invalidContract(nil)
		}
		if name == runtimeHomeName {
			homePresent = true
		} else {
			tempPresent = true
		}
		var named unix.Stat_t
		if err := unix.Fstatat(rootFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || !validRuntimeOrdinaryDirectory(named, root.device, true) {
			return nil, runner.FileIdentity{}, invalidContract(err)
		}
		fd, err := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
		if err != nil {
			return nil, runner.FileIdentity{}, invalidContract(err)
		}
		children, more, readErr := readRuntimeEntries(fd, 1)
		closeErr := unix.Close(fd)
		if readErr != nil || more || len(children) != 0 || closeErr != nil {
			return nil, runner.FileIdentity{}, invalidContract(errors.Join(readErr, closeErr))
		}
	}
	for _, name := range []string{runtimeHomeName, runtimeTempName} {
		present := name == runtimeHomeName && homePresent || name == runtimeTempName && tempPresent
		if !present {
			if err := createRuntimeLayoutChild(rootFD, name, root, syncDirectory); err != nil {
				return nil, runner.FileIdentity{}, err
			}
		}
	}
	if !lifetimePresent {
		return createRuntimeLifetime(rootFD, syncDirectory)
	}
	return openRuntimeLifetime(rootFD, root.device, nil)
}

func createRuntimeLifetime(rootFD int, syncDirectory func(int) error) (_ *os.File, _ runner.FileIdentity, resultErr error) {
	fd, err := unix.Openat(rootFD, runner.RuntimeLifetimeLeaseName, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, runner.FileIdentity{}, invalidContract(err)
	}
	file := os.NewFile(uintptr(fd), "runtime-lifetime")
	keep := false
	var created unix.Stat_t
	defer func() {
		if !keep {
			_ = file.Close()
			if created.Dev != 0 && created.Ino != 0 {
				if cleanupErr := cleanupCreatedFile(rootFD, runner.RuntimeLifetimeLeaseName, created, syncDirectory); cleanupErr != nil {
					resultErr = retainedContract(errors.Join(resultErr, cleanupErr))
				}
			}
		}
	}()
	root, err := inspectPrivateDirectory(rootFD)
	statErr := unix.Fstat(fd, &created)
	if err != nil || statErr != nil || !validRuntimeLifetime(created, root.device) {
		return nil, runner.FileIdentity{}, invalidContract(errors.Join(err, statErr))
	}
	if err := unix.Fsync(fd); err != nil {
		return nil, runner.FileIdentity{}, invalidContract(err)
	}
	if err := syncDirectory(rootFD); err != nil {
		return nil, runner.FileIdentity{}, invalidContract(err)
	}
	identity := runner.FileIdentity{Device: uint64(created.Dev), Inode: created.Ino}
	opened, openedID, err := openRuntimeLifetime(rootFD, root.device, &identity)
	if err != nil {
		return nil, runner.FileIdentity{}, err
	}
	_ = file.Close()
	file = opened
	keep = true
	return file, openedID, nil
}

func openRuntimeLifetime(rootFD int, device uint64, expected *runner.FileIdentity) (*os.File, runner.FileIdentity, error) {
	fd, err := unix.Openat(rootFD, runner.RuntimeLifetimeLeaseName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, runner.FileIdentity{}, invalidContract(err)
	}
	file := os.NewFile(uintptr(fd), "runtime-lifetime")
	var opened, named unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || unix.Fstatat(rootFD, runner.RuntimeLifetimeLeaseName, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || !validRuntimeLifetime(opened, device) || !sameFileObject(opened, named) || !validRuntimeLifetime(named, device) {
		file.Close()
		return nil, runner.FileIdentity{}, invalidContract(err)
	}
	identity := runner.FileIdentity{Device: uint64(opened.Dev), Inode: opened.Ino}
	if expected != nil && identity != *expected {
		file.Close()
		return nil, runner.FileIdentity{}, invalidContract(nil)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, runner.FileIdentity{}, errRuntimeBusy
		}
		return nil, runner.FileIdentity{}, invalidContract(err)
	}
	var recheck, namedRecheck unix.Stat_t
	if err := unix.Fstat(fd, &recheck); err != nil || unix.Fstatat(rootFD, runner.RuntimeLifetimeLeaseName, &namedRecheck, unix.AT_SYMLINK_NOFOLLOW) != nil || !validRuntimeLifetime(recheck, device) || !validRuntimeLifetime(namedRecheck, device) || !sameFileObject(opened, recheck) || !sameFileObject(opened, namedRecheck) {
		file.Close()
		return nil, runner.FileIdentity{}, invalidContract(err)
	}
	return file, identity, nil
}

func validRuntimeLifetime(stat unix.Stat_t, device uint64) bool {
	return validPrivateFile(stat, 0) && uint64(stat.Dev) == device
}

func createRuntimeLayoutChild(rootFD int, name string, root directoryIdentity, syncDirectory func(int) error) error {
	if err := unix.Mkdirat(rootFD, name, 0o700); err != nil {
		return invalidContract(err)
	}
	fd, err := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return invalidContract(err)
	}
	child, inspectErr := inspectPrivateDirectory(fd)
	syncErr := syncDirectory(fd)
	closeErr := unix.Close(fd)
	if inspectErr != nil || child.device != root.device || syncErr != nil || closeErr != nil {
		return invalidContract(errors.Join(inspectErr, syncErr, closeErr))
	}
	return nil
}

func cleanupRuntimeLayout(rootFD int, root directoryIdentity, syncDirectory func(int) error) error {
	mutated := false
	for _, name := range []string{runtimeTempName, runtimeHomeName} {
		var named unix.Stat_t
		err := unix.Fstatat(rootFD, name, &named, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return err
		}
		identity := directoryIdentity{device: uint64(named.Dev), inode: named.Ino, uid: named.Uid, mode: uint32(named.Mode)}
		if identity.device != root.device || identity.uid != uint32(os.Geteuid()) || identity.mode&uint32(unix.S_IFMT) != uint32(unix.S_IFDIR) || identity.mode&0o7777 != 0o700 || named.Nlink == 0 {
			return errRetainedRuntime
		}
		fd, err := unix.Openat(rootFD, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
		if err != nil {
			return err
		}
		directory := os.NewFile(uintptr(fd), "failed-runtime-layout")
		entries, readErr := directory.ReadDir(1)
		closeErr := directory.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) || len(entries) != 0 || closeErr != nil {
			return errRetainedRuntime
		}
		if err := verifyNamedChild(rootFD, name, identity); err != nil {
			return errRetainedRuntime
		}
		if err := unix.Unlinkat(rootFD, name, unix.AT_REMOVEDIR); err != nil {
			return err
		}
		mutated = true
	}
	var lifetime unix.Stat_t
	if err := unix.Fstatat(rootFD, runner.RuntimeLifetimeLeaseName, &lifetime, unix.AT_SYMLINK_NOFOLLOW); err == nil {
		if !validRuntimeLifetime(lifetime, root.device) {
			return errRetainedRuntime
		}
		if err := unix.Unlinkat(rootFD, runner.RuntimeLifetimeLeaseName, 0); err != nil {
			return err
		}
		mutated = true
	} else if !errors.Is(err, unix.ENOENT) {
		return err
	}
	if mutated {
		return syncDirectory(rootFD)
	}
	return nil
}

func cleanupCreatedFile(directoryFD int, preferred string, identity unix.Stat_t, syncDirectory func(int) error) error {
	name, err := locateExactEntry(directoryFD, preferred, uint64(identity.Dev), identity.Ino, false)
	if err != nil {
		return err
	}
	if name == "" {
		return errRetainedRuntime
	}
	var named unix.Stat_t
	if err := unix.Fstatat(directoryFD, name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || !samePrivateFileAuthority(identity, named) || !validCleanupPrivateFile(named) {
		return errRetainedRuntime
	}
	if err := unix.Unlinkat(directoryFD, name, 0); err != nil {
		return err
	}
	return syncDirectory(directoryFD)
}

func locateExactEntry(parentFD int, preferred string, device uint64, inode uint64, directory bool) (string, error) {
	var named unix.Stat_t
	if err := unix.Fstatat(parentFD, preferred, &named, unix.AT_SYMLINK_NOFOLLOW); err == nil && statMatchesKindIdentity(named, device, inode, directory) {
		return preferred, nil
	} else if err != nil && !errors.Is(err, unix.ENOENT) {
		return "", err
	}
	fd, err := unix.Openat(parentFD, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return "", err
	}
	scan := os.NewFile(uintptr(fd), "bounded-runtime-cleanup")
	defer scan.Close()
	entries, err := scan.ReadDir(cleanupSearchLimit + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if len(entries) > cleanupSearchLimit {
		return "", errRetainedRuntime
	}
	found := ""
	for _, entry := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(parentFD, entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return "", err
		}
		if statMatchesKindIdentity(stat, device, inode, directory) {
			if found != "" {
				return "", errRetainedRuntime
			}
			found = entry.Name()
		}
	}
	return found, nil
}

func statMatchesKindIdentity(stat unix.Stat_t, device uint64, inode uint64, directory bool) bool {
	wantType := uint32(unix.S_IFREG)
	if directory {
		wantType = uint32(unix.S_IFDIR)
	}
	return uint64(stat.Dev) == device && stat.Ino == inode && uint32(stat.Mode)&uint32(unix.S_IFMT) == wantType
}

type directoryIdentity struct {
	device uint64
	inode  uint64
	uid    uint32
	mode   uint32
}

func (identity directoryIdentity) fileIdentity() runner.FileIdentity {
	return runner.FileIdentity{Device: identity.device, Inode: identity.inode}
}

func inspectPrivateDirectory(fd int) (directoryIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return directoryIdentity{}, err
	}
	identity := directoryIdentity{device: uint64(stat.Dev), inode: stat.Ino, uid: stat.Uid, mode: uint32(stat.Mode)}
	if identity.device == 0 || identity.inode == 0 || identity.uid != uint32(os.Geteuid()) || identity.mode&uint32(unix.S_IFMT) != uint32(unix.S_IFDIR) || identity.mode&0o7777 != 0o700 || stat.Nlink == 0 {
		return directoryIdentity{}, errInvalidContract
	}
	return identity, nil
}

func inspectNamedPrivateDirectory(parentFD int, name string) (directoryIdentity, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return directoryIdentity{}, err
	}
	identity := directoryIdentity{device: uint64(stat.Dev), inode: stat.Ino, uid: stat.Uid, mode: uint32(stat.Mode)}
	if identity.device == 0 || identity.inode == 0 || identity.uid != uint32(os.Geteuid()) || identity.mode&uint32(unix.S_IFMT) != uint32(unix.S_IFDIR) || identity.mode&0o7777 != 0o700 || stat.Nlink == 0 {
		return directoryIdentity{}, errInvalidContract
	}
	return identity, nil
}

func inspectExpectedDirectory(fd int, expected runner.FileIdentity) (directoryIdentity, error) {
	identity, err := inspectPrivateDirectory(fd)
	if err != nil || identity.fileIdentity() != expected {
		return directoryIdentity{}, errInvalidContract
	}
	return identity, nil
}

func verifyDirectoryDescriptor(parent *os.File, path string, expected directoryIdentity) error {
	actual, err := inspectPrivateDirectory(int(parent.Fd()))
	if err != nil || actual != expected {
		return errInvalidContract
	}
	return verifyNamedDirectory(path, expected)
}

func verifyNamedDirectory(path string, expected directoryIdentity) error {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return err
	}
	actual := directoryIdentity{device: uint64(stat.Dev), inode: stat.Ino, uid: stat.Uid, mode: uint32(stat.Mode)}
	if actual != expected {
		return errInvalidContract
	}
	return nil
}

func verifyNamedChild(parentFD int, name string, expected directoryIdentity) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	actual := directoryIdentity{device: uint64(stat.Dev), inode: stat.Ino, uid: stat.Uid, mode: uint32(stat.Mode)}
	if actual != expected {
		return errInvalidContract
	}
	return nil
}

func validPrivateFile(stat unix.Stat_t, size int64) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&0o7777 == 0o600 && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1 && stat.Dev != 0 && stat.Ino != 0 && stat.Size == size
}

func validCleanupPrivateFile(stat unix.Stat_t) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1 && stat.Dev != 0 && stat.Ino != 0
}

func sameFileObject(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino
}

func samePrivateFileAuthority(opened, named unix.Stat_t) bool {
	return sameFileObject(opened, named) && opened.Uid == named.Uid && opened.Gid == named.Gid && opened.Mode == named.Mode && opened.Nlink == named.Nlink
}

func samePrivateFile(opened, named unix.Stat_t) bool {
	return validPrivateFile(named, opened.Size) && samePrivateFileAuthority(opened, named) && opened.Size == named.Size
}

func validBasename(name string) bool {
	return name != "" && name != "." && name != ".." && filepath.Base(name) == name && len(name) <= 255
}

func descriptorPath(file *os.File) (string, error) {
	buffer := make([]byte, 1024)
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, file.Fd(), unix.F_GETPATH, uintptr(unsafe.Pointer(&buffer[0])))
	if errno != 0 {
		return "", errno
	}
	length := 0
	for length < len(buffer) && buffer[length] != 0 {
		length++
	}
	if length == 0 {
		return "", errInvalidContract
	}
	return string(buffer[:length]), nil
}
