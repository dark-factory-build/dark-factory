package runner

import (
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func unlinkExactScratch(dir *os.File, name string, opened unix.Stat_t) error {
	var named unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err == nil && named.Dev == opened.Dev && named.Ino == opened.Ino && named.Mode&unix.S_IFMT == unix.S_IFREG {
		if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
			return err
		}
		return unix.Fsync(int(dir.Fd()))
	} else if err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	fd, err := unix.Openat(int(dir.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	scan := os.NewFile(uintptr(fd), "terminal-scratch-cleanup")
	defer scan.Close()
	entries, err := scan.ReadDir(17)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > 16 {
		return ErrUnresolved
	}
	found := ""
	for _, entry := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Dev == opened.Dev && stat.Ino == opened.Ino && stat.Mode&unix.S_IFMT == unix.S_IFREG {
			if found != "" {
				return ErrUnresolved
			}
			found = entry.Name()
		}
	}
	if found == "" {
		return ErrUnresolved
	}
	if err := unix.Unlinkat(int(dir.Fd()), found, 0); err != nil {
		return err
	}
	return unix.Fsync(int(dir.Fd()))
}

func writeAll(fd int, p []byte) error {
	for len(p) > 0 {
		n, err := unix.Write(fd, p)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
