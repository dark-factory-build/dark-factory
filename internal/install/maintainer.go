package install

import "io"

const maintainerCredentialName = "maintainer.json"
const maintainerCredentialStage = "maintainer.staging"
const maxMaintainerCredentialBytes = 4096

// ReadMaintainerCredential reads the fixed private connection record through
// the same retained home authority and owner-only file checks as the API token.
// An absent record returns nil. These bytes must never enter operator replies.
func (home *OperationalHome) ReadMaintainerCredential() ([]byte, error) {
	if home == nil || home.state == nil {
		return nil, ErrClosed
	}
	return home.state.readMaintainerCredential()
}

// WriteMaintainerCredential atomically replaces the fixed connection record.
// A disconnected record is retained so an offline revocation can be retried.
func (home *OperationalHome) WriteMaintainerCredential(data []byte) error {
	if home == nil || home.state == nil {
		return ErrClosed
	}
	if len(data) == 0 || len(data) > maxMaintainerCredentialBytes {
		return ErrInvalidHome
	}
	return home.state.writeMaintainerCredential(data)
}

// LockLegacyController fences the existing host controller while the first
// customer credential becomes durable. The caller closes it after saving.
func (home *OperationalHome) LockLegacyController() (io.Closer, error) {
	if home == nil || home.state == nil {
		return nil, ErrClosed
	}
	return home.state.lockLegacyController()
}
