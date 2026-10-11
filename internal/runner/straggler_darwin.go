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

// PrivateTemp is the short TMPDIR the Change worker gives a run, named from its
// runtime directory. Worker sandboxes deny the shared /private/tmp (a grant
// beneath it still applies), and the runtime's own tmp is too deep for a test
// fixture's Unix socket to fit the sun_path budget. A runtime not named by a
// run id (a test's) has none, so unrelated runtimes never share one.
// ponytail: two live runs whose ids share 8 hex characters (about one in four
// billion) share this name, and the later run's sweep would reach the earlier
// run's processes; widen the name if run counts ever make that plausible.
func PrivateTemp(runtime string) string {
	name := filepath.Base(runtime)
	if len(name) != 32 || strings.Trim(name, "0123456789abcdef") != "" {
		return ""
	}
	return "/private/tmp/df-" + name[:8]
}

// killRunStragglers kills every process of this user whose TMPDIR is inside
// the run's private runtime root or PrivateTemp, then removes the latter:
// descendants that left the provider's group and session (setsid, double
// fork) and now live on under launchd (#1403). Only the run's own processes
// are given those paths. ARCHITECTURE.md records why this numeric-PID sweep stays Darwin-only. Every
// error is unresolved: cleanup was not proved, so no result may be published.
func killRunStragglers(runtime *os.File) error {
	root, err := fdPath(runtime)
	short := PrivateTemp(root)
	roots := []string{root}
	if short != "" {
		roots = append(roots, short)
	}
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
			if pid != os.Getpid() && process.Proc.P_stat != darwinZombieState && environmentNames(pid, roots) {
				found = true
				_ = unix.Kill(pid, unix.SIGKILL)
			}
		}
		if err == nil && !found {
			// Best effort: what a failed removal leaves is the system's
			// periodic temporary-file cleanup's.
			if short != "" {
				_ = os.RemoveAll(short)
			}
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err == nil {
		err = fmt.Errorf("run processes outlived the run")
	}
	return fmt.Errorf("%w: run-process sweep: %v", ErrUnresolved, err)
}

// environmentNames reports whether pid's exec-time TMPDIR resolves inside one of roots. kern.procargs2 is argc, the exec path and its NUL
// padding, argc arguments, then the environment up to an empty string.
func environmentNames(pid int, roots []string) bool {
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
		for _, root := range roots {
			if name == "TMPDIR" && strings.Contains(value, "/"+filepath.Base(root)) {
				if real, err := filepath.EvalSymlinks(value); err == nil && (real == root || strings.HasPrefix(real, root+"/")) {
					return true
				}
			}
		}
	}
}
