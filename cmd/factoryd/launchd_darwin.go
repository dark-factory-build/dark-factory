//go:build darwin

package main

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

//go:cgo_import_dynamic libc_launch_activate_socket launch_activate_socket "/usr/lib/libSystem.B.dylib"

var libc_launch_activate_socket_trampoline_addr uintptr

//go:linkname syscall_syscall syscall.syscall
func syscall_syscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

// browserSocket is the console listener launchd binds for the job's whole
// load (the plist's Sockets "Browser"), so no other local process can take
// the paired console origin while factoryd restarts or after it crashes. A
// trial child inherits it from its supervisor as fd 3, read at init before
// boot opens anything there. It is close-on-exec so no worker inherits it,
// and nil outside launchd, where the browser runtime binds the address itself.
var browserSocket = func() *os.File {
	fd := 3
	if os.Getenv(trialEnv) == "" {
		name, _ := syscall.BytePtrFromString("Browser")
		var fds *int32
		var count uintptr
		if result, _, _ := syscall_syscall(libc_launch_activate_socket_trampoline_addr, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&fds)), uintptr(unsafe.Pointer(&count))); result != 0 || count != 1 {
			return nil
		}
		fd = int(*fds)
	}
	if kind, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE); err != nil || kind != unix.SOCK_STREAM {
		return nil
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), "browser")
}()
