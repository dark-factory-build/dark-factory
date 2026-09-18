//go:build !darwin

package kernel

import "golang.org/x/sys/unix"

const databaseAncestorOpenFlag = unix.O_RDONLY
