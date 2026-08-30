package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const maxTerminalBytes = 32 << 10

type TerminalRecord struct {
	Terminal Terminal
	Identity FileIdentity
	Digest   string
}

func PublishTerminal(dir *os.File, basename string, terminal Terminal) (_ *TerminalRecord, resultErr error) {
	return publishTerminal(dir, basename, terminal, nil)
}

func publishTerminal(dir *os.File, basename string, terminal Terminal, afterOpen func(int)) (_ *TerminalRecord, resultErr error) {
	if err := validateTerminalName(dir, basename); err != nil {
		return nil, err
	}
	if err := validateTerminal(terminal); err != nil {
		return nil, fmt.Errorf("runner: invalid terminal")
	}
	body, err := json.Marshal(terminal)
	if err != nil {
		return nil, err
	}
	if len(body)+1 > maxTerminalBytes {
		return nil, fmt.Errorf("runner: terminal too large")
	}
	body = append(body, '\n')
	tmp := TerminalScratchName
	fd, err := unix.Openat(int(dir.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	if afterOpen != nil {
		afterOpen(fd)
	}
	var created unix.Stat_t
	if err := unix.Fstat(fd, &created); err != nil {
		_ = unix.Close(fd)
		return nil, errors.Join(ErrUnresolved, err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			if cleanupErr := unlinkExactScratch(dir, tmp, created); cleanupErr != nil {
				resultErr = errors.Join(resultErr, ErrUnresolved, cleanupErr)
			}
		}
		if closeErr := unix.Close(fd); closeErr != nil {
			resultErr = errors.Join(resultErr, closeErr)
		}
	}()
	if !validTerminalFile(created, 0) || created.Size != 0 {
		return nil, ErrIdentity
	}
	if err := writeAll(fd, body); err != nil {
		return nil, err
	}
	if err := unix.Fsync(fd); err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if !validTerminalFile(st, int64(len(body))) {
		return nil, ErrIdentity
	}
	var named unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), tmp, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil || named.Dev != st.Dev || named.Ino != st.Ino || !validTerminalFile(named, int64(len(body))) {
		return nil, errors.Join(ErrIdentity, err)
	}
	if err := publishNoReplace(int(dir.Fd()), tmp, basename); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return nil, ErrConflict
		}
		return nil, err
	}
	cleanup = false
	if err := unix.Fsync(int(dir.Fd())); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	return &TerminalRecord{Terminal: terminal, Identity: FileIdentity{Device: uint64(st.Dev), Inode: st.Ino}, Digest: hex.EncodeToString(digest[:])}, nil
}

func LoadTerminal(dir *os.File, basename string) (result *TerminalRecord, resultErr error) {
	record, f, _, err := loadTerminalFile(dir, basename)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			result = nil
			resultErr = errors.Join(resultErr, closeErr)
		}
	}()
	return record, nil
}

// testAfterTerminalContentProof is a package-test-only seam. Production
// callers rely on the runtime lifetime lease as the cooperative sole-writer
// boundary; the final descriptor/name comparison still detects a replacement
// made before that comparison. It is not an atomic-unlink claim against a
// hostile same-EUID writer after the final check.
var testAfterTerminalContentProof func()

func AcknowledgeTerminal(dir *os.File, basename string, want *TerminalRecord, storeCommitted bool) (resultErr error) {
	if !storeCommitted {
		return ErrState
	}
	if want == nil {
		return ErrIdentity
	}
	got, file, opened, err := loadTerminalFile(dir, basename)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, closeErr)
		}
	}()
	if got.Digest != want.Digest || got.Identity != want.Identity || got.Terminal != want.Terminal {
		return ErrIdentity
	}
	if testAfterTerminalContentProof != nil {
		testAfterTerminalContentProof()
	}
	if err := verifyTerminalDescriptorAndName(file, dir, basename, opened); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(dir.Fd()), basename, 0); err != nil {
		return err
	}
	return unix.Fsync(int(dir.Fd()))
}

func loadTerminalFile(dir *os.File, basename string) (record *TerminalRecord, file *os.File, opened unix.Stat_t, resultErr error) {
	if err := validateTerminalName(dir, basename); err != nil {
		return nil, nil, unix.Stat_t{}, err
	}
	fd, err := unix.Openat(int(dir.Fd()), basename, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, unix.Stat_t{}, err
	}
	f := os.NewFile(uintptr(fd), "terminal")
	keep := false
	defer func() {
		if !keep {
			resultErr = errors.Join(resultErr, f.Close())
		}
	}()
	if err := unix.Fstat(fd, &opened); err != nil {
		return nil, nil, unix.Stat_t{}, err
	}
	if !validTerminalFile(opened, 0) || opened.Size <= 0 || opened.Size > maxTerminalBytes {
		return nil, nil, unix.Stat_t{}, ErrIdentity
	}
	body, err := io.ReadAll(io.LimitReader(f, maxTerminalBytes+1))
	if err != nil {
		return nil, nil, unix.Stat_t{}, err
	}
	if len(body) > maxTerminalBytes {
		return nil, nil, unix.Stat_t{}, ErrIdentity
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, nil, unix.Stat_t{}, err
	}
	if !sameTerminalFileStat(opened, after) {
		return nil, nil, unix.Stat_t{}, ErrIdentity
	}
	terminal, err := decodeCanonicalTerminal(body)
	if err != nil {
		return nil, nil, unix.Stat_t{}, err
	}
	digest := sha256.Sum256(body)
	record = &TerminalRecord{Terminal: terminal, Identity: FileIdentity{Device: uint64(after.Dev), Inode: after.Ino}, Digest: hex.EncodeToString(digest[:])}
	if err := verifyTerminalDescriptorAndName(f, dir, basename, after); err != nil {
		return nil, nil, unix.Stat_t{}, err
	}
	keep = true
	return record, f, after, nil
}

func decodeCanonicalTerminal(body []byte) (Terminal, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var terminal Terminal
	if err := decoder.Decode(&terminal); err != nil {
		return Terminal{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Terminal{}, fmt.Errorf("runner: trailing terminal")
	}
	if err := validateTerminal(terminal); err != nil {
		return Terminal{}, err
	}
	canonical, err := json.Marshal(terminal)
	if err != nil {
		return Terminal{}, err
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(body, canonical) {
		return Terminal{}, ErrIdentity
	}
	return terminal, nil
}

func verifyTerminalDescriptorAndName(file *os.File, dir *os.File, basename string, expected unix.Stat_t) error {
	if err := validateTerminalName(dir, basename); err != nil {
		return err
	}
	var current unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &current); err != nil {
		return errors.Join(ErrIdentity, err)
	}
	var named unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), basename, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return errors.Join(ErrIdentity, err)
	}
	if !sameTerminalFileAuthority(expected, current, named) {
		return ErrIdentity
	}
	return nil
}

func sameTerminalFileAuthority(expected, current, named unix.Stat_t) bool {
	return sameTerminalFileStat(current, expected) && sameTerminalFileStat(named, current)
}

func validateTerminalName(dir *os.File, name string) error {
	if dir == nil || name != TerminalSpoolName || filepath.Base(name) != name {
		return ErrIdentity
	}
	_, err := validatePrivateDirectory(dir)
	return err
}

func validateTerminal(terminal Terminal) error {
	codeExit := terminal.Exit.Code >= 0 && terminal.Exit.Signal == 0
	signalExit := terminal.Exit.Code == -1 && terminal.Exit.Signal > 0
	if terminal.AttemptID == "" || len(terminal.AttemptID) > 256 || !terminal.Process.Valid() || len(terminal.Message) > 8192 || !codeExit && !signalExit {
		return ErrIdentity
	}
	return nil
}

func validTerminalFile(stat unix.Stat_t, exactSize int64) bool {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || stat.Dev == 0 || stat.Ino == 0 {
		return false
	}
	return exactSize == 0 || stat.Size == exactSize
}

func unlinkExactScratch(dir *os.File, name string, opened unix.Stat_t) error {
	var named unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err == nil && named.Dev == opened.Dev && named.Ino == opened.Ino && named.Mode&unix.S_IFMT == unix.S_IFREG {
		if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil {
			return err
		}
		return unix.Fsync(int(dir.Fd()))
	} else if err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	fd, err := unix.Openat(int(dir.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	scan := os.NewFile(uintptr(fd), "terminal-scratch-cleanup")
	defer scan.Close()
	entries, err := scan.ReadDir(17)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > 16 {
		return ErrUnresolved
	}
	found := ""
	for _, entry := range entries {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(dir.Fd()), entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		if stat.Dev == opened.Dev && stat.Ino == opened.Ino && stat.Mode&unix.S_IFMT == unix.S_IFREG {
			if found != "" {
				return ErrUnresolved
			}
			found = entry.Name()
		}
	}
	if found == "" {
		return ErrUnresolved
	}
	if err := unix.Unlinkat(int(dir.Fd()), found, 0); err != nil {
		return err
	}
	return unix.Fsync(int(dir.Fd()))
}

func writeAll(fd int, p []byte) error {
	for len(p) > 0 {
		n, err := unix.Write(fd, p)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
func sameTerminalFileStat(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Uid == right.Uid && left.Gid == right.Gid && left.Mode == right.Mode && left.Nlink == right.Nlink && left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}
