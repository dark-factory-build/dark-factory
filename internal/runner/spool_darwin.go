//go:build darwin

package runner

import "golang.org/x/sys/unix"

func publishNoReplace(dirfd int, from, to string) error {
	return unix.RenameatxNp(dirfd, from, dirfd, to, unix.RENAME_EXCL)
}
