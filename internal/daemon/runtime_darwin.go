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
	workerConfigName = "change-worker.config"
)

type Runtime struct {
	mu       sync.Mutex
	path     string
	dir      *os.File
	identity runner.FileIdentity
}

type PrivateFile struct {
	path     string
	identity runner.FileIdentity
}

func (file PrivateFile) Path() string                  { return file.path }
func (file PrivateFile) Identity() runner.FileIdentity { return file.identity }
func (PrivateFile) String() string                     { return "private runtime file" }
func (PrivateFile) GoString() string                   { return "daemon.PrivateFile{private}" }

func CreateRuntime(parent *os.File, basename string) (*Runtime, error) {
	return createRuntime(parent, basename, nil, nil)
}

func createRuntime(parent *os.File, basename string, beforeCreate, afterOpen func()) (*Runtime, error) {
	if parent == nil || !validBasename(basename) {
		return nil, invalidContract(nil)
	}
	parentPath, err := descriptorPath(parent)
	if err != nil || !filepath.IsAbs(parentPath) || filepath.Clean(parentPath) != parentPath {
		return nil, invalidContract(err)
	}
	wantParent, err := inspectPrivateDirectory(int(parent.Fd()))
	if err != nil || verifyNamedDirectory(parentPath, wantParent) != nil {
		return nil, invalidContract(err)
	}
	var present unix.Stat_t
	err = unix.Fstatat(int(parent.Fd()), basename, &present, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil || !errors.Is(err, unix.ENOENT) {
		return nil, invalidContract(err)
	}
	if beforeCreate != nil {
		beforeCreate()
	}
	if err := verifyDirectoryDescriptor(parent, parentPath, wantParent); err != nil {
		return nil, invalidContract(err)
	}
	if err := unix.Mkdirat(int(parent.Fd()), basename, 0o700); err != nil {
		return nil, invalidContract(err)
	}
	fd, err := unix.Openat(int(parent.Fd()), basename, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0)
	if err != nil {
		return nil, invalidContract(err)
	}
	dir := os.NewFile(uintptr(fd), "attempt-runtime")
	if afterOpen != nil {
		afterOpen()
	}
	child, childErr := inspectPrivateDirectory(fd)
	namedErr := verifyNamedChild(int(parent.Fd()), basename, child)
	parentErr := verifyDirectoryDescriptor(parent, parentPath, wantParent)
	if childErr != nil || namedErr != nil || parentErr != nil {
		dir.Close()
		return nil, invalidContract(errors.Join(childErr, namedErr, parentErr))
	}
	if err := unix.Fsync(fd); err != nil {
		dir.Close()
		return nil, invalidContract(err)
	}
	if err := unix.Fsync(int(parent.Fd())); err != nil {
		dir.Close()
		return nil, invalidContract(err)
	}
	path := filepath.Join(parentPath, basename)
	return &Runtime{path: path, dir: dir, identity: child.fileIdentity()}, nil
}

func (runtime *Runtime) Path() string {
	if runtime == nil {
		return ""
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.path
}

func (runtime *Runtime) Identity() runner.FileIdentity {
	if runtime == nil {
		return runner.FileIdentity{}
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.identity
}

// DuplicateDirectory transfers one independently closable descriptor to a
// runner call without transferring the Runtime's own cleanup authority.
func (runtime *Runtime) DuplicateDirectory() (*os.File, error) {
	if runtime == nil {
		return nil, invalidContract(nil)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.dir == nil {
		return nil, invalidContract(nil)
	}
	if _, err := inspectExpectedDirectory(int(runtime.dir.Fd()), runtime.identity); err != nil {
		return nil, invalidContract(err)
	}
	fd, err := unix.FcntlInt(runtime.dir.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, invalidContract(err)
	}
	return os.NewFile(uintptr(fd), "attempt-runtime-duplicate"), nil
}

func (runtime *Runtime) PublishAttemptToken(ctx context.Context, token [32]byte) (PrivateFile, error) {
	return runtime.publish(ctx, attemptTokenName, token[:], len(token), nil)
}

func (runtime *Runtime) PublishWorkerConfig(ctx context.Context, config workerConfig) (PrivateFile, error) {
	encoded, err := encodeWorkerConfig(config)
	if err != nil {
		return PrivateFile{}, invalidContract(nil)
	}
	return runtime.publish(ctx, workerConfigName, encoded, workerConfigLimit, nil)
}

func (runtime *Runtime) publish(ctx context.Context, name string, value []byte, limit int, write privateWrite) (PrivateFile, error) {
	if runtime == nil || ctx == nil || !validBasename(name) || len(value) == 0 || len(value) > limit {
		return PrivateFile{}, invalidContract(nil)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.dir == nil {
		return PrivateFile{}, invalidContract(nil)
	}
	if err := ctx.Err(); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	if _, err := inspectExpectedDirectory(int(runtime.dir.Fd()), runtime.identity); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	fd, err := unix.Openat(int(runtime.dir.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW_ANY, 0o600)
	if err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	created := true
	defer func() {
		if created {
			unlinkExact(int(runtime.dir.Fd()), name, fd)
		}
		_ = unix.Close(fd)
	}()
	if write == nil {
		write = unix.Write
	}
	if err := writePrivate(ctx, fd, value, write); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	if err := unix.Fsync(fd); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !validPrivateFile(opened, int64(len(value))) {
		return PrivateFile{}, invalidContract(err)
	}
	var named unix.Stat_t
	if err := unix.Fstatat(int(runtime.dir.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || !samePrivateFile(opened, named) {
		return PrivateFile{}, invalidContract(err)
	}
	if _, err := inspectExpectedDirectory(int(runtime.dir.Fd()), runtime.identity); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	if err := unix.Fsync(int(runtime.dir.Fd())); err != nil {
		return PrivateFile{}, invalidContract(err)
	}
	created = false
	return PrivateFile{path: filepath.Join(runtime.path, name), identity: runner.FileIdentity{Device: uint64(opened.Dev), Inode: opened.Ino}}, nil
}

func (runtime *Runtime) Close() error {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.dir == nil {
		return nil
	}
	err := runtime.dir.Close()
	runtime.dir = nil
	return err
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

func unlinkExact(parentFD int, name string, openedFD int) {
	var opened, named unix.Stat_t
	if unix.Fstat(openedFD, &opened) == nil && unix.Fstatat(parentFD, name, &named, unix.AT_SYMLINK_NOFOLLOW) == nil &&
		opened.Dev == named.Dev && opened.Ino == named.Ino {
		_ = unix.Unlinkat(parentFD, name, 0)
		_ = unix.Fsync(parentFD)
	}
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

func samePrivateFile(opened, named unix.Stat_t) bool {
	return validPrivateFile(named, opened.Size) && opened.Dev == named.Dev && opened.Ino == named.Ino && opened.Uid == named.Uid && opened.Gid == named.Gid && opened.Mode == named.Mode && opened.Nlink == named.Nlink && opened.Size == named.Size
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
