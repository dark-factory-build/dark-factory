package kernel

import "golang.org/x/sys/unix"

// Darwin O_SEARCH (sys/fcntl.h) is not yet exported by x/sys/unix.
// Ancestors need identity and traversal, never directory listing permission.
const databaseAncestorOpenFlag = 0x40000000 | unix.O_DIRECTORY
