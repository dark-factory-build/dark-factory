package api

import "golang.org/x/sys/unix"

// Darwin O_SEARCH (sys/fcntl.h) is not yet exported by x/sys/unix.
const ancestorOpenFlag = 0x40000000 | unix.O_DIRECTORY
