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

// privateTemp is the short TMPDIR the Change worker gives a run, named from its
// runtime directory. Worker sandboxes deny the shared /private/tmp (a grant
// beneath it still applies), and the runtime's own tmp is too deep for a test
// fixture's Unix socket to fit the sun_path budget. A runtime not named by a
// run id (a test's) has none.
func privateTemp(runtime string) string {
	name := filepath.Base(runtime)
	if len(name) != 32 || strings.Trim(name, "0123456789abcdef") != "" {
		return ""
	}
	return "/private/tmp/df-" + name[:8]
}

const privateTempOwnerName = ".df-run"

// ClaimPrivateTemp makes runtime's short TMPDIR and records the runtime's full
// name in it, or returns "" when the name is taken (a run whose id shares its
// first 8 characters, or one that left it behind).
func ClaimPrivateTemp(runtime string) string {
	short := privateTemp(runtime)
	if short == "" || os.Mkdir(short, 0o700) != nil {
		return ""
	}
	if os.WriteFile(filepath.Join(short, privateTempOwnerName), []byte(filepath.Base(runtime)), 0o600) != nil {
		_ = os.RemoveAll(short)
		return ""
	}
	return short
}

// ownedPrivateTemp is runtime's short TMPDIR only when ClaimPrivateTemp made
// it for this runtime, so a run never sweeps or removes another run's.
func ownedPrivateTemp(runtime string) string {
	short := privateTemp(runtime)
	if owner, err := os.ReadFile(filepath.Join(short, privateTempOwnerName)); short == "" || err != nil || string(owner) != filepath.Base(runtime) {
		return ""
	}
	return short
}

// killRunStragglers kills every process of this user whose TMPDIR is inside
// the run's private runtime root or its owned short TMPDIR, then removes the
// latter: descendants that left the provider's group and session (setsid,
// double fork) and now live on under launchd (#1403). Only the run's own
// processes are given those paths. ARCHITECTURE.md records why this
// numeric-PID sweep stays Darwin-only. Every error is unresolved: cleanup was
// not proved, so no result may be published.
func killRunStragglers(runtime *os.File) error {
	root, err := fdPath(runtime)
	short := ownedPrivateTemp(root)
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
