package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const maxTerminalBytes = 32 << 10

type TerminalRecord struct {
	Terminal Terminal
	Identity FileIdentity
	Digest   string
}

func PublishTerminal(dir *os.File, basename string, terminal Terminal) (_ *TerminalRecord, resultErr error) {
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
	var created unix.Stat_t
	if err := unix.Fstat(fd, &created); err != nil || !validTerminalFile(created, 0) || created.Size != 0 {
		_ = unix.Close(fd)
		return nil, errors.Join(ErrIdentity, err)
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

func LoadTerminal(dir *os.File, basename string) (*TerminalRecord, error) {
	if err := validateTerminalName(dir, basename); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(dir.Fd()), basename, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "terminal")
	defer f.Close()
	var a, b unix.Stat_t
	if err := unix.Fstat(fd, &a); err != nil {
		return nil, err
	}
	if !validTerminalFile(a, 0) || a.Size <= 0 || a.Size > maxTerminalBytes {
		return nil, ErrIdentity
	}
	body, err := io.ReadAll(io.LimitReader(f, maxTerminalBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxTerminalBytes {
		return nil, ErrIdentity
	}
	if err := unix.Fstat(fd, &b); err != nil {
		return nil, err
	}
	if statSpool(a) != statSpool(b) {
		return nil, ErrIdentity
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	var terminal Terminal
	if err := dec.Decode(&terminal); err != nil {
		return nil, err
	}
	var trailing json.RawMessage
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("runner: trailing terminal")
	}
	if err := validateTerminal(terminal); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(body)
	return &TerminalRecord{Terminal: terminal, Identity: FileIdentity{Device: uint64(a.Dev), Inode: a.Ino}, Digest: hex.EncodeToString(digest[:])}, nil
}

func AcknowledgeTerminal(dir *os.File, basename string, want *TerminalRecord, storeCommitted bool) error {
	if !storeCommitted {
		return ErrState
	}
	if want == nil {
		return ErrIdentity
	}
	got, err := LoadTerminal(dir, basename)
	if err != nil {
		return err
	}
	if got.Digest != want.Digest || got.Identity != want.Identity || got.Terminal != want.Terminal {
		return ErrIdentity
	}
	if err := validateTerminalName(dir, basename); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(dir.Fd()), basename, 0); err != nil {
		return err
	}
	return unix.Fsync(int(dir.Fd()))
}

func validateTerminalName(dir *os.File, name string) error {
	if dir == nil || name != TerminalSpoolName || filepath.Base(name) != name {
		return ErrIdentity
	}
	_, err := validatePrivateDirectory(dir)
	return err
}

func validateTerminal(terminal Terminal) error {
	if terminal.AttemptID == "" || len(terminal.AttemptID) > 256 || !terminal.Process.Valid() || len(terminal.Message) > 8192 {
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
	if err := unix.Fstatat(int(dir.Fd()), name, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if named.Dev != opened.Dev || named.Ino != opened.Ino || named.Mode&unix.S_IFMT != unix.S_IFREG || named.Uid != uint32(os.Geteuid()) || named.Nlink != 1 {
		return ErrIdentity
	}
	return unix.Unlinkat(int(dir.Fd()), name, 0)
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
func statSpool(s unix.Stat_t) string {
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d:%d:%d:%d:%d", s.Dev, s.Ino, s.Uid, s.Gid, s.Mode, s.Nlink, s.Size, s.Mtim.Sec, s.Mtim.Nsec, s.Ctim.Sec, s.Ctim.Nsec)
}
