//go:build linux

package kernel

import "golang.org/x/sys/unix"

// Linux O_PATH permits identity checks and descriptor-relative traversal
// without granting directory listing access to the database path ancestors.
const databaseAncestorOpenFlag = unix.O_PATH | unix.O_DIRECTORY
