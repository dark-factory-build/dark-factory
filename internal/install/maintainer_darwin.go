//go:build darwin

package install

import (
	"errors"
	"golang.org/x/sys/unix"
	"io"
)

func (state *operationalHomeState) readMaintainerCredential() ([]byte, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return nil, ErrClosed
	}
	if err := recheckOperationalCoreIdentityByState(state); err != nil {
		return nil, err
	}
	file, stat, err := openMember(state.home, maintainerCredentialName)
	if errors.Is(err, unix.ENOENT) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if stat.Size <= 0 || stat.Size > maxMaintainerCredentialBytes {
		return nil, ErrInvalidHome
	}
	data, err := io.ReadAll(io.LimitReader(file, maxMaintainerCredentialBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != stat.Size {
		return nil, ErrInvalidHome
	}
	if err := recheckIdentityBinding(state.home, maintainerCredentialName, toIdentity(stat)); err != nil {
		return nil, err
	}
	return data, nil
}

func (state *operationalHomeState) writeMaintainerCredential(data []byte) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed {
		return ErrClosed
	}
	if err := recheckOperationalCoreIdentityByState(state); err != nil {
		return err
	}
	// A crashed staging write is never used as authority. Validate its shape
	// before removing it; the home lease excludes another legitimate writer.
	for _, name := range []string{maintainerCredentialName, maintainerCredentialStage} {
		file, _, err := openMember(state.home, name)
		if errors.Is(err, unix.ENOENT) {
			continue
		}
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		if name == maintainerCredentialStage {
			if err := unix.Unlinkat(int(state.home.Fd()), name, 0); err != nil {
				return err
			}
		}
	}
	if err := writeMember(state.home, maintainerCredentialStage, data, phase("maintainer credential")); err != nil {
		return err
	}
	if err := unix.Renameat(int(state.home.Fd()), maintainerCredentialStage, int(state.home.Fd()), maintainerCredentialName); err != nil {
		return err
	}
	return syncFile(int(state.home.Fd()))
}
