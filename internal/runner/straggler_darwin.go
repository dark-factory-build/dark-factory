//go:build darwin

package runner

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// testStragglerPass replaces one sweep scan in package tests; production is nil.
var testStragglerPass func(pass int) (bool, error)

// killRunStragglers kills every process of this user whose TMPDIR is inside
// the run's private runtime root: descendants that left the
// provider's group and session (setsid, double fork) and now live on under
// launchd (#1403). Only the run's own processes are given those paths.
// ARCHITECTURE.md records why this numeric-PID sweep stays Darwin-only. Every
// error is unresolved: cleanup was not proved, so no result may be published.
func killRunStragglers(runtime *os.File) error {
	root, err := fdPath(runtime)
	for pass := 0; err == nil && pass < 100; pass++ {
		var processes []unix.KinfoProc
		found := false
		if testStragglerPass != nil {
			found, err = testStragglerPass(pass)
		} else {
			processes, err = unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
		}
		for _, process := range processes {
			pid := int(process.Proc.P_pid)
			if pid != os.Getpid() && process.Proc.P_stat != darwinZombieState && environmentNames(pid, root) {
				found = true
				_ = unix.Kill(pid, unix.SIGKILL)
			}
		}
		if err == nil && !found {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err == nil {
		err = fmt.Errorf("run processes outlived the run")
	}
	return fmt.Errorf("%w: run-process sweep: %v", ErrUnresolved, err)
}

// environmentNames reports whether pid's exec-time TMPDIR resolves inside root. kern.procargs2 is argc, the exec path and its NUL
// padding, argc arguments, then the environment up to an empty string.
func environmentNames(pid int, root string) bool {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(raw) < 4 {
		return false
	}
	argc := int(binary.LittleEndian.Uint32(raw))
	_, rest, _ := bytes.Cut(raw[4:], []byte{0})
	rest = bytes.TrimLeft(rest, "\x00")
	for ; argc > 0 && len(rest) > 0; argc-- {
		_, rest, _ = bytes.Cut(rest, []byte{0})
	}
	for {
		var entry []byte
		entry, rest, _ = bytes.Cut(rest, []byte{0})
		if len(entry) == 0 {
			return false
		}
		// Only TMPDIR: the run gives its provider a private one and every child
		// inherits it, while other variables naming run paths (the attempt
		// token file, for one) may sit in the environment of a process that
		// merely talks to the run, factoryd included.
		name, value, _ := strings.Cut(string(entry), "=")
		if name != "TMPDIR" || !strings.Contains(value, "/"+filepath.Base(root)) {
			continue
		}
		if real, err := filepath.EvalSymlinks(value); err == nil && (real == root || strings.HasPrefix(real, root+"/")) {
			return true
		}
	}
}
