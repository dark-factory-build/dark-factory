//go:build darwin || linux

package cloudflareadmin

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

type fileIdentity struct {
	device           uint64
	inode            uint64
	mode             uint32
	uid              uint32
	links            uint64
	size             int64
	modificationTime int64
	changeTime       int64
}

func readPrivateRegularFile(path string, maximum int64) ([]byte, error) {
	file, before, err := openRegularFile(path, 0o600, maximum, true)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	first, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(first)) > maximum {
		return nil, fmt.Errorf("file exceeds %d bytes", maximum)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	second, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(first, second) {
		return nil, fmt.Errorf("file changed while it was read")
	}
	if err := requireStableBinding(path, file, before); err != nil {
		return nil, err
	}
	return first, nil
}

func openRegularFile(path string, permissions uint32, maximum int64, singleLink bool) (*os.File, fileIdentity, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fileIdentity{}, fmt.Errorf("%s must not be a symlink", path)
		}
		return nil, fileIdentity{}, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fileIdentity{}, fmt.Errorf("open %s", path)
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); err != nil {
		file.Close()
		return nil, fileIdentity{}, err
	}
	identity := identityOf(stat)
	if uint32(stat.Mode)&syscall.S_IFMT != syscall.S_IFREG || identity.device == 0 || identity.inode == 0 {
		file.Close()
		return nil, fileIdentity{}, fmt.Errorf("%s must be a regular file", path)
	}
	if identity.uid != uint32(os.Geteuid()) || identity.mode&0o7777 != permissions {
		file.Close()
		return nil, fileIdentity{}, fmt.Errorf("%s must be owned by the current user with mode %04o", path, permissions)
	}
	if singleLink && identity.links != 1 {
		file.Close()
		return nil, fileIdentity{}, fmt.Errorf("%s must have exactly one link", path)
	}
	if identity.size <= 0 || identity.size > maximum {
		file.Close()
		return nil, fileIdentity{}, fmt.Errorf("%s has an invalid size", path)
	}
	return file, identity, nil
}

func requireStableBinding(path string, file *os.File, before fileIdentity) error {
	var after, named syscall.Stat_t
	if err := syscall.Fstat(int(file.Fd()), &after); err != nil {
		return err
	}
	if identityOf(after) != before {
		return fmt.Errorf("%s changed while it was open", path)
	}
	if err := syscall.Lstat(path, &named); err != nil {
		return err
	}
	if identityOf(named) != before {
		return fmt.Errorf("%s no longer names the opened file", path)
	}
	return nil
}

func identityOf(stat syscall.Stat_t) fileIdentity {
	modificationTime, changeTime := stableTimes(stat)
	return fileIdentity{
		device:           uint64(stat.Dev),
		inode:            stat.Ino,
		mode:             uint32(stat.Mode),
		uid:              stat.Uid,
		links:            uint64(stat.Nlink),
		size:             stat.Size,
		modificationTime: modificationTime,
		changeTime:       changeTime,
	}
}

func acquirePublishLock(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("open publish lock")
	}
	var opened, named syscall.Stat_t
	if err := syscall.Fstat(fd, &opened); err != nil {
		file.Close()
		return nil, err
	}
	identity := identityOf(opened)
	if uint32(opened.Mode)&syscall.S_IFMT != syscall.S_IFDIR || identity.uid != uint32(os.Geteuid()) || identity.mode&0o022 != 0 {
		file.Close()
		return nil, fmt.Errorf("git common directory must be a current-user directory not writable by group or others")
	}
	if err := syscall.Lstat(path, &named); err != nil || identityOf(named) != identity {
		file.Close()
		return nil, fmt.Errorf("publish lock path is not stable")
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another app DNS publish is already in progress")
		}
		return nil, err
	}
	return file, nil
}
