//go:build darwin

package main

import (
	"os"

	"github.com/dark-factory-build/dark-factory/internal/install"
	"golang.org/x/sys/unix"
)

// browserSocket is the console listener launchd binds for the job's whole
// load (the plist's Sockets "Browser"), so no other local process can take
// the paired console origin while factoryd restarts or after it crashes. A
// trial child inherits it from its supervisor as fd 3, read at init before
// boot opens anything there. It is close-on-exec so no worker inherits it,
// and nil outside launchd, where the browser runtime binds the address itself.
var browserSocket = func() *os.File {
	fd := 3
	if os.Getenv(trialEnv) == "" {
		fds, err := install.LaunchActivateSocket("Browser")
		if err != nil || len(fds) != 1 {
			return nil
		}
		fd = fds[0]
	}
	if kind, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE); err != nil || kind != unix.SOCK_STREAM {
		return nil
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), "browser")
}()
