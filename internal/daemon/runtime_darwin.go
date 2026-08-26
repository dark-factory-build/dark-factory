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

type Runtime struct {
	mu             sync.Mutex
	path           string
	basename       string
	dir            *os.File
	directory      directoryIdentity
	parent         *os.File
	parentPath     string
	parentIdentity directoryIdentity
	identity       runner.FileIdentity
}

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

func CreateRuntime(parent *os.File, basename string) (*Runtime, error) {
	return createRuntime(parent, basename, nil, nil, nil)
}

func createRuntime(parent *os.File, basename string, beforeCreate, afterOpen func(), syncDirectory func(int) error) (_ *Runtime, resultErr error) {
	if parent == nil || !validBasename(basename) {
		return nil, invalidContract(nil)
	}
	if syncDirectory == nil {
		syncDirectory = unix.Fsync
	}
	parentPath, err := descriptorPath(parent)
	if err != nil || !filepath.IsAbs(parentPath) || filepath.Clean(parentPath) != parentPath {
		return nil, invalidContract(err)
	}
	wantParent, err := inspectPrivateDirectory(int(parent.Fd()))
	if err != nil || verifyNamedDirectory(parentPath, wantParent) != nil {
		return nil, invalidContract(err)
	}
	parentFD, err := unix.FcntlInt(parent.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, invalidContract(err)
	}
	ownedParent := os.NewFile(uintptr(parentFD), "attempt-runtime-parent")
	keepParent := false
	defer func() {
		if !keepParent {
			_ = ownedParent.Close()
		}
	}()
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
		parent: ownedParent, parentPath: parentPath, parentIdentity: wantParent, identity: created.fileIdentity(),
	}
	if err := runtime.verifyAuthority(); err != nil {
		return nil, invalidContract(err)
	}
	cleanupCreated = false
	keepDir = true
	keepParent = true
	return runtime, nil
}

func (runtime *Runtime) Path() (string, error) {
	if runtime == nil {
		return "", invalidContract(nil)
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if err := runtime.verifyAuthority(); err != nil {
		return "", invalidContract(err)
	}
	return runtime.path, nil
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
	if err := runtime.verifyAuthority(); err != nil {
		return nil, invalidContract(err)
	}
	fd, err := unix.FcntlInt(runtime.dir.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, invalidContract(err)
	}
	duplicate := os.NewFile(uintptr(fd), "attempt-runtime-duplicate")
	if _, err := inspectExpectedDirectory(fd, runtime.identity); err != nil {
		duplicate.Close()
		return nil, invalidContract(err)
	}
	if err := runtime.verifyAuthority(); err != nil {
		duplicate.Close()
		return nil, invalidContract(err)
	}
	return duplicate, nil
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
	if runtime == nil || runtime.dir == nil || runtime.parent == nil || !validBasename(runtime.basename) || runtime.path != filepath.Join(runtime.parentPath, runtime.basename) {
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
	return verifyNamedDirectory(runtime.path, runtime.directory)
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
	if runtime.dir == nil && runtime.parent == nil {
		return nil
	}
	var dirErr, parentErr error
	if runtime.dir != nil {
		dirErr = runtime.dir.Close()
		runtime.dir = nil
	}
	if runtime.parent != nil {
		parentErr = runtime.parent.Close()
		runtime.parent = nil
	}
	return errors.Join(dirErr, parentErr)
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
