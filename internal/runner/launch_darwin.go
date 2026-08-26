//go:build darwin

package runner

import (
	"crypto/sha256"
	"debug/macho"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

func PrepareExecSpec(spec ExecSpec) (*LaunchSpec, error) {
	if len(spec.Stdin) > maxInputBytes {
		return nil, fmt.Errorf("runner: stdin too large")
	}
	if len(spec.Args) > 128 || len(spec.Env) > 128 {
		return nil, fmt.Errorf("runner: argv/env too large")
	}
	for _, s := range append(append([]string{}, spec.Args...), spec.Env...) {
		if len(s) > 8192 || strings.IndexByte(s, 0) >= 0 {
			return nil, fmt.Errorf("runner: invalid argv/env")
		}
	}
	for _, e := range spec.Env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) != 2 || !allowedEnv(parts[0]) {
			return nil, fmt.Errorf("runner: environment key %q not allowed", parts[0])
		}
	}
	target, err := commitExecutable(spec.Target)
	if err != nil {
		return nil, err
	}
	cwd, err := commitDirectory(spec.Cwd)
	if err != nil {
		return nil, err
	}
	argv := append([]string{target.Path}, spec.Args...)
	return &LaunchSpec{commit: launchCommitment{Executable: target, Cwd: cwd, Argv: argv, Env: append([]string{}, spec.Env...)}, stdin: append([]byte{}, spec.Stdin...), stdout: spec.Stdout, stderr: spec.Stderr}, nil
}

func allowedEnv(k string) bool {
	switch k {
	case "HOME", "PATH", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TERM", "SHELL", "USER", "LOGNAME", "DARK_FACTORY_ATTEMPT_TOKEN_FILE", "GIT_CEILING_DIRECTORIES", "GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT":
		return true
	}
	return false
}

func canonical(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("runner: empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("runner: non-absolute path")
	}
	return resolved, nil
}

func commitExecutable(path string) (fileCommitment, error) {
	path, err := canonical(path)
	if err != nil {
		return fileCommitment{}, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fileCommitment{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	c, err := commitOpen(f, path, true)
	if err != nil {
		return fileCommitment{}, err
	}
	if err := validateMachO(f); err != nil {
		return fileCommitment{}, err
	}
	return c, nil
}

func commitDirectory(path string) (fileCommitment, error) {
	path, err := canonical(path)
	if err != nil {
		return fileCommitment{}, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fileCommitment{}, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	return commitOpen(f, path, false)
}

func commitOpen(f *os.File, path string, executable bool) (fileCommitment, error) {
	var a, b unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &a); err != nil {
		return fileCommitment{}, err
	}
	if executable {
		if a.Mode&unix.S_IFMT != unix.S_IFREG || a.Mode&0o111 == 0 || a.Mode&(unix.S_ISUID|unix.S_ISGID) != 0 || a.Size < 1 || a.Size > maxTargetBytes {
			return fileCommitment{}, fmt.Errorf("runner: unsafe executable")
		}
	} else if a.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fileCommitment{}, fmt.Errorf("runner: cwd is not directory")
	}
	h := sha256.New()
	if executable {
		if _, err := io.Copy(h, io.LimitReader(f, maxTargetBytes+1)); err != nil {
			return fileCommitment{}, err
		}
	}
	if err := unix.Fstat(int(f.Fd()), &b); err != nil {
		return fileCommitment{}, err
	}
	if statKey(a) != statKey(b) {
		return fileCommitment{}, ErrIdentity
	}
	return fileCommitment{Path: path, FileIdentity: FileIdentity{Device: uint64(a.Dev), Inode: a.Ino}, UID: a.Uid, GID: a.Gid, Mode: uint32(a.Mode), Size: a.Size, MtimeSec: a.Mtim.Sec, MtimeNsec: a.Mtim.Nsec, CtimeSec: a.Ctim.Sec, CtimeNsec: a.Ctim.Nsec, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func statKey(s unix.Stat_t) string {
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d:%d:%d", s.Dev, s.Ino, s.Uid, s.Gid, s.Mode, s.Size, s.Mtim.Sec, s.Mtim.Nsec, s.Ctim.Nsec)
}

func validateMachO(f *os.File) error {
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	want := macho.CpuArm64
	if runtime.GOARCH == "amd64" {
		want = macho.CpuAmd64
	}
	if fat, err := macho.NewFatFile(f); err == nil {
		defer fat.Close()
		for _, a := range fat.Arches {
			if a.Cpu == want {
				return nil
			}
		}
		return fmt.Errorf("runner: no native Mach-O slice")
	}
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	m, err := macho.NewFile(f)
	if err != nil {
		return fmt.Errorf("runner: target is not Mach-O: %w", err)
	}
	defer m.Close()
	if m.Cpu != want {
		return fmt.Errorf("runner: wrong Mach-O architecture")
	}
	return nil
}

func verifyCommit(c fileCommitment, executable bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if !executable {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Open(c.Path, flags, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), c.Path)
	got, err := commitOpen(f, c.Path, executable)
	if err != nil {
		f.Close()
		return nil, err
	}
	if got != c {
		f.Close()
		return nil, ErrIdentity
	}
	if executable {
		if err := validateMachO(f); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

func statIdentity(f *os.File) (FileIdentity, error) {
	var s unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &s); err != nil {
		return FileIdentity{}, err
	}
	return FileIdentity{Device: uint64(s.Dev), Inode: s.Ino}, nil
}

func sameNamedIdentity(path string, want FileIdentity) error {
	var s unix.Stat_t
	if err := unix.Lstat(path, &s); err != nil {
		return err
	}
	if s.Mode&unix.S_IFMT != unix.S_IFREG || uint64(s.Dev) != want.Device || s.Ino != want.Inode {
		return ErrIdentity
	}
	return nil
}
