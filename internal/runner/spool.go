package runner

import (
	"crypto/rand"
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

func PublishTerminal(dir *os.File, basename string, terminal Terminal) (*TerminalRecord, error) {
	if err := validateTerminalName(dir, basename); err != nil {
		return nil, err
	}
	if terminal.AttemptID == "" || len(terminal.AttemptID) > 256 || !terminal.Process.Valid() || len(terminal.Message) > 8192 {
		return nil, fmt.Errorf("runner: invalid terminal")
	}
	body, err := json.Marshal(terminal)
	if err != nil {
		return nil, err
	}
	if len(body) > maxTerminalBytes {
		return nil, fmt.Errorf("runner: terminal too large")
	}
	body = append(body, '\n')
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	tmp := "." + basename + "." + hex.EncodeToString(nonce[:]) + ".tmp"
	fd, err := unix.Openat(int(dir.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		_ = unix.Close(fd)
		if cleanup {
			_ = unix.Unlinkat(int(dir.Fd()), tmp, 0)
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
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0o777 != 0o600 || st.Nlink != 1 {
		return nil, ErrIdentity
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
	defer unix.Close(fd)
	var a, b unix.Stat_t
	if err := unix.Fstat(fd, &a); err != nil {
		return nil, err
	}
	if a.Mode&unix.S_IFMT != unix.S_IFREG || a.Mode&0o777 != 0o600 || a.Nlink != 1 || a.Size <= 0 || a.Size > maxTerminalBytes {
		return nil, ErrIdentity
	}
	f := os.NewFile(uintptr(fd), "terminal")
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
	if terminal.AttemptID == "" || !terminal.Process.Valid() {
		return nil, ErrIdentity
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
	if got.Digest != want.Digest || got.Identity != want.Identity || got.Terminal.AttemptID != want.Terminal.AttemptID {
		return ErrIdentity
	}
	if err := unix.Unlinkat(int(dir.Fd()), basename, 0); err != nil {
		return err
	}
	return unix.Fsync(int(dir.Fd()))
}

func validateTerminalName(dir *os.File, name string) error {
	if dir == nil || name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return ErrIdentity
	}
	var st unix.Stat_t
	if err := unix.Fstat(int(dir.Fd()), &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return ErrIdentity
	}
	return nil
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
	return fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d", s.Dev, s.Ino, s.Mode, s.Size, s.Mtim.Sec, s.Mtim.Nsec, s.Ctim.Nsec)
}
