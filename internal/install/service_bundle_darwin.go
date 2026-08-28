//go:build darwin

package install

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
	"golang.org/x/sys/unix"
)

func serviceBundleComponentNames() [3]string {
	return [3]string{"factoryctl", "factoryd", "factory-runner"}
}

type serviceBundleMember struct {
	file   *os.File
	stat   unix.Stat_t
	digest [sha256.Size]byte
}

type serviceBundleState struct {
	mu        sync.Mutex
	closed    bool
	directory *serviceDirectory
	members   map[string]serviceBundleMember
	expected  buildinfo.Identity
}

func openServiceBundle(factoryctlPath string, expected buildinfo.Identity) (_ *ServiceBundle, resultErr error) {
	if !expected.Release() || !validServicePath(factoryctlPath) || filepath.Base(factoryctlPath) != "factoryctl" {
		return nil, fmt.Errorf("%w: invalid request", ErrServiceBundle)
	}
	directory, err := openServiceDirectory(filepath.Dir(factoryctlPath))
	if err != nil {
		return nil, errors.Join(ErrServiceBundle, err)
	}
	components := serviceBundleComponentNames()
	state := &serviceBundleState{directory: directory, members: make(map[string]serviceBundleMember, len(components)), expected: expected}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, state.close())
		}
	}()
	parent := directory.files[len(directory.files)-1]
	for _, component := range components {
		fd, openErr := unix.Openat(int(parent.Fd()), component, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return nil, errors.Join(ErrServiceBundle, openErr)
		}
		file := os.NewFile(uintptr(fd), component)
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			_ = file.Close()
			return nil, errors.Join(ErrServiceBundle, err)
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o755 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || stat.Size <= 0 || stat.Size > buildinfo.MaxBinaryBytes {
			_ = file.Close()
			return nil, fmt.Errorf("%w: %s metadata", ErrServiceBundle, component)
		}
		if _, err := buildinfo.InspectReleaseArtifact(file, component, expected); err != nil {
			_ = file.Close()
			return nil, errors.Join(ErrServiceBundle, err)
		}
		digest, err := digestServiceMember(file, stat.Size)
		if err != nil {
			_ = file.Close()
			return nil, errors.Join(ErrServiceBundle, err)
		}
		state.members[component] = serviceBundleMember{file: file, stat: stat, digest: digest}
	}
	if err := state.recheck(); err != nil {
		return nil, err
	}
	return &ServiceBundle{state: state}, nil
}

func (state *serviceBundleState) recheck() error {
	if err := state.directory.recheck(); err != nil {
		return errors.Join(ErrServiceBundle, err)
	}
	parent := state.directory.files[len(state.directory.files)-1]
	for _, component := range serviceBundleComponentNames() {
		member, present := state.members[component]
		if !present || member.file == nil {
			return fmt.Errorf("%w: missing %s", ErrServiceBundle, component)
		}
		var descriptor, binding unix.Stat_t
		if err := unix.Fstat(int(member.file.Fd()), &descriptor); err != nil || !sameServiceFileStat(member.stat, descriptor) {
			return fmt.Errorf("%w: %s descriptor changed", ErrServiceBundle, component)
		}
		if err := unix.Fstatat(int(parent.Fd()), component, &binding, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameServiceFileStat(member.stat, binding) {
			return fmt.Errorf("%w: %s binding changed", ErrServiceBundle, component)
		}
		digest, err := digestServiceMember(member.file, member.stat.Size)
		if err != nil || digest != member.digest {
			return fmt.Errorf("%w: %s bytes changed", ErrServiceBundle, component)
		}
	}
	return nil
}

func (state *serviceBundleState) snapshot(parent *os.File, component string) (resultErr error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return ErrClosed
	}
	member, present := state.members[component]
	if !present || member.file == nil || !validSnapshotParent(parent) {
		return fmt.Errorf("%w: invalid snapshot request", ErrServiceBundle)
	}
	if err := state.recheck(); err != nil {
		return err
	}
	fd, err := unix.Openat(int(parent.Fd()), component, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create service snapshot: %w", err)
	}
	output := os.NewFile(uintptr(fd), component)
	keep := false
	var created unix.Stat_t
	if err := unix.Fstat(fd, &created); err != nil {
		_ = output.Close()
		return err
	}
	defer func() {
		if closeErr := output.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, ErrUncertain, closeErr)
		}
		if !keep {
			resultErr = errors.Join(resultErr, removeServiceSnapshot(parent, component, created))
		}
	}()
	if _, err := member.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.CopyN(output, member.file, member.stat.Size); err != nil {
		return fmt.Errorf("copy service snapshot: %w", err)
	}
	var trailing [1]byte
	if count, err := member.file.Read(trailing[:]); count != 0 || err != io.EOF {
		if err == nil {
			err = errors.New("source grew while copying")
		}
		return fmt.Errorf("copy service snapshot: %w", err)
	}
	if err := state.recheck(); err != nil {
		return err
	}
	if err := unix.Fchmod(fd, 0o755); err != nil {
		return fmt.Errorf("secure service snapshot: %w", err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("sync service snapshot: %w", err)
	}
	if _, err := buildinfo.InspectReleaseArtifact(output, component, state.expected); err != nil {
		return errors.Join(ErrServiceBundle, err)
	}
	if err := unix.Fsync(int(parent.Fd())); err != nil {
		return fmt.Errorf("sync service snapshot parent: %w", err)
	}
	var final, binding unix.Stat_t
	if err := unix.Fstat(fd, &final); err != nil || final.Mode&0o7777 != 0o755 || final.Dev != created.Dev || final.Ino != created.Ino {
		return fmt.Errorf("%w: snapshot identity changed", ErrServiceBundle)
	}
	if err := unix.Fstatat(int(parent.Fd()), component, &binding, unix.AT_SYMLINK_NOFOLLOW); err != nil || !sameServiceFileStat(final, binding) {
		return fmt.Errorf("%w: snapshot binding changed", ErrServiceBundle)
	}
	keep = true
	return nil
}

func digestServiceMember(file *os.File, size int64) ([sha256.Size]byte, error) {
	if file == nil || size <= 0 || size > buildinfo.MaxBinaryBytes {
		return [sha256.Size]byte{}, errors.New("invalid service member digest request")
	}
	hash := sha256.New()
	if _, err := io.CopyN(hash, io.NewSectionReader(file, 0, size), size); err != nil {
		return [sha256.Size]byte{}, err
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func sameServiceFileStat(left, right unix.Stat_t) bool {
	return sameServiceStat(left, right) && left.Nlink == right.Nlink && left.Size == right.Size && left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func validSnapshotParent(parent *os.File) bool {
	if parent == nil {
		return false
	}
	var stat unix.Stat_t
	return unix.Fstat(int(parent.Fd()), &stat) == nil && stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Mode&0o7777 == 0o700 && stat.Uid == uint32(os.Geteuid()) && stat.Nlink >= 2
}

func removeServiceSnapshot(parent *os.File, component string, expected unix.Stat_t) error {
	var binding unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), component, &binding, unix.AT_SYMLINK_NOFOLLOW); errors.Is(err, unix.ENOENT) {
		return nil
	} else if err != nil || binding.Dev != expected.Dev || binding.Ino != expected.Ino {
		return ErrUncertain
	}
	if err := unix.Unlinkat(int(parent.Fd()), component, 0); err != nil {
		return errors.Join(ErrUncertain, err)
	}
	if err := unix.Fsync(int(parent.Fd())); err != nil {
		return errors.Join(ErrUncertain, err)
	}
	return nil
}

func (state *serviceBundleState) close() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil
	}
	state.closed = true
	var result error
	for _, component := range serviceBundleComponentNames() {
		member := state.members[component]
		if member.file != nil {
			result = errors.Join(result, member.file.Close())
		}
	}
	if state.directory != nil {
		result = errors.Join(result, state.directory.close())
	}
	return result
}
