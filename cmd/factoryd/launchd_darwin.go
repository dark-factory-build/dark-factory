//go:build darwin

package main

import (
	"os"
	"sync"
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
// trial child inherits it from its supervisor as fd 3. It is nil outside
// launchd, where the browser runtime binds the address itself.
var browserSocket = sync.OnceValue(func() *os.File {
	if os.Getenv(trialEnv) != "" {
		if _, err := unix.FcntlInt(3, unix.F_GETFD, 0); err != nil {
			return nil
		}
		return os.NewFile(3, "browser")
	}
	name, _ := syscall.BytePtrFromString("Browser")
	var fds *int32
	var count uintptr
	if result, _, _ := syscall_syscall(libc_launch_activate_socket_trampoline_addr, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&fds)), uintptr(unsafe.Pointer(&count))); result != 0 || count != 1 {
		return nil
	}
	return os.NewFile(uintptr(*fds), "browser")
})
