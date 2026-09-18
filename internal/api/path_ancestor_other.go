//go:build !darwin

package api

import "golang.org/x/sys/unix"

const ancestorOpenFlag = unix.O_RDONLY
