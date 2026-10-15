package install

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// libSystem's launch_activate_socket, called as x/sys/unix calls libc.

//go:cgo_import_dynamic libc_launch_activate_socket launch_activate_socket "/usr/lib/libSystem.B.dylib"
//go:cgo_import_dynamic libc_free free "/usr/lib/libSystem.B.dylib"

var libc_launch_activate_socket_trampoline_addr uintptr
var libc_free_trampoline_addr uintptr

//go:linkname syscall_syscall syscall.syscall
func syscall_syscall(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

// LaunchActivateSocket returns the descriptors launchd bound for name in this
// job's plist: ESRCH outside launchd, ENOENT for a plist without it.
func LaunchActivateSocket(name string) ([]int, error) {
	key, err := unix.BytePtrFromString(name)
	if err != nil {
		return nil, err
	}
	var fds *int32
	var count uintptr
	result, _, _ := syscall_syscall(libc_launch_activate_socket_trampoline_addr, uintptr(unsafe.Pointer(key)), uintptr(unsafe.Pointer(&fds)), uintptr(unsafe.Pointer(&count)))
	if result != 0 {
		return nil, os.NewSyscallError("launch_activate_socket", syscall.Errno(result))
	}
	activated := make([]int, count)
	for index, fd := range unsafe.Slice(fds, count) {
		activated[index] = int(fd)
	}
	syscall_syscall(libc_free_trampoline_addr, uintptr(unsafe.Pointer(fds)), 0, 0)
	return activated, nil
}
