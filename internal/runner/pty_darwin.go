//go:build darwin

package runner

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// openPTY deliberately uses the Darwin kernel's /dev/ptmx interface instead
// of adding a PTY library. Both descriptors are CLOEXEC; os/exec makes the
// slave its 0/1/2 descriptors for the blocked gate, while the parent retains
// only the master.
func openPTY() (master, slave *os.File, err error) {
	masterFD, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	master = os.NewFile(uintptr(masterFD), "pty-master")
	closeMaster := true
	defer func() {
		if closeMaster {
			_ = master.Close()
		}
	}()
	if err := unix.SetNonblock(masterFD, true); err != nil {
		return nil, nil, fmt.Errorf("runner: nonblocking pty master: %w", err)
	}
	if err := ptyIoctl(masterFD, unix.TIOCPTYGRANT, nil); err != nil {
		return nil, nil, fmt.Errorf("runner: grant pty: %w", err)
	}
	if err := ptyIoctl(masterFD, unix.TIOCPTYUNLK, nil); err != nil {
		return nil, nil, fmt.Errorf("runner: unlock pty: %w", err)
	}
	var name [256]byte
	if err := ptyIoctl(masterFD, unix.TIOCPTYGNAME, unsafe.Pointer(&name[0])); err != nil {
		return nil, nil, fmt.Errorf("runner: pty name: %w", err)
	}
	path := strings.TrimRight(string(name[:]), "\x00")
	if path == "" || !strings.HasPrefix(path, "/dev/") {
		return nil, nil, ErrIdentity
	}
	slaveFD, err := unix.Open(path, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	slave = os.NewFile(uintptr(slaveFD), "pty-slave")
	closeMaster = false
	return master, slave, nil
}

func ptyIoctl(fd int, request uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), request, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// ResizePTY changes the window size of this live owner's PTY. The PTY master
// is an authority-bearing capability: recovered process identities and
// callers holding only a numeric descriptor cannot reach this operation.
// Callers serialize it with other terminal operations at the daemon boundary.
func (c *OwnedChild) ResizePTY(columns, rows int) error {
	if c == nil || c.ptyMaster == nil || c.state != stateActivated || c.exitObserved {
		return ErrState
	}
	if columns < 1 || columns > maxPTYDimension || rows < 1 || rows > maxPTYDimension {
		return ErrState
	}
	if err := c.refreshExit(); err != nil {
		return err
	}
	if c.exitObserved {
		return ErrState
	}
	winsize := unix.Winsize{Col: uint16(columns), Row: uint16(rows)}
	return unix.IoctlSetWinsize(int(c.ptyMaster.Fd()), unix.TIOCSWINSZ, &winsize)
}

func validatePTYDescriptors() error {
	for _, fd := range []int{0, 1, 2} {
		if _, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err != nil {
			return fmt.Errorf("runner: fd %d is not a tty: %w", fd, err)
		}
	}
	sid, err := unix.Getsid(0)
	if err != nil || sid != os.Getpid() {
		return fmt.Errorf("runner: pty session: sid=%d err=%v", sid, err)
	}
	pgrp, err := unix.IoctlGetInt(0, unix.TIOCGPGRP)
	if err != nil || pgrp != os.Getpid() {
		return fmt.Errorf("runner: controlling tty pgrp=%d err=%v", pgrp, err)
	}
	return nil
}
