package daemon

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

var (
	errInvalidContract = errors.New("daemon: invalid private contract")
	errRetainedRuntime = errors.New("daemon: private runtime effect retained")
	errUnsupported     = errors.New("daemon: unsupported platform")
)

var birthMagic = [8]byte{'D', 'F', 'B', 'I', 'R', 'T', 'H', 1}

// encodeBirth preserves the Darwin kinfo birth tuple. It is an encoding, not
// a hash: recovery must be able to reconstruct the exact runner observation.
func encodeBirth(birth runner.Birth) ([32]byte, error) {
	var encoded [32]byte
	if birth.Seconds <= 0 || birth.Microseconds < 0 || birth.Microseconds >= 1_000_000 {
		return encoded, errInvalidContract
	}
	copy(encoded[:8], birthMagic[:])
	binary.BigEndian.PutUint64(encoded[8:16], uint64(birth.Seconds))
	binary.BigEndian.PutUint32(encoded[16:20], uint32(birth.Microseconds))
	return encoded, nil
}

func decodeBirth(encoded [32]byte) (runner.Birth, error) {
	if !bytes.Equal(encoded[:8], birthMagic[:]) || !allZero(encoded[20:]) {
		return runner.Birth{}, errInvalidContract
	}
	seconds := binary.BigEndian.Uint64(encoded[8:16])
	microseconds := binary.BigEndian.Uint32(encoded[16:20])
	if seconds == 0 || seconds > math.MaxInt64 || microseconds >= 1_000_000 {
		return runner.Birth{}, errInvalidContract
	}
	return runner.Birth{Seconds: int64(seconds), Microseconds: int32(microseconds)}, nil
}

func allZero(value []byte) bool {
	for _, octet := range value {
		if octet != 0 {
			return false
		}
	}
	return true
}

func processResourceIdentity(identity runner.Identity) (kernel.ResourceIdentity, error) {
	if !identity.Valid() {
		return kernel.ResourceIdentity{}, errInvalidContract
	}
	encoded, err := encodeBirth(identity.Birth)
	if err != nil {
		return kernel.ResourceIdentity{}, err
	}
	birth, err := kernel.BirthDigestFromBytes(encoded[:])
	if err != nil {
		return kernel.ResourceIdentity{}, errInvalidContract
	}
	result, err := kernel.NewProcessResourceIdentity(int64(identity.PID), int64(identity.PGID), birth)
	if err != nil {
		return kernel.ResourceIdentity{}, errInvalidContract
	}
	return result, nil
}

func runnerIdentity(identity kernel.ResourceIdentity) (runner.Identity, error) {
	pid, pgid, birth, ok := identity.Process()
	if !ok || pid > math.MaxInt || pgid > math.MaxInt {
		return runner.Identity{}, errInvalidContract
	}
	var encoded [32]byte
	copy(encoded[:], birth.Bytes())
	decoded, err := decodeBirth(encoded)
	if err != nil {
		return runner.Identity{}, err
	}
	result := runner.Identity{PID: int(pid), PGID: int(pgid), Birth: decoded}
	if !result.Valid() {
		return runner.Identity{}, errInvalidContract
	}
	return result, nil
}

func pathResourceIdentity(identity runner.FileIdentity) (kernel.ResourceIdentity, error) {
	if identity.Device == 0 {
		return kernel.ResourceIdentity{}, errInvalidContract
	}
	device, inode, err := signedIdentity(identity.Device, identity.Inode)
	if err != nil {
		return kernel.ResourceIdentity{}, err
	}
	result, err := kernel.NewPathResourceIdentity(device, inode)
	if err != nil {
		return kernel.ResourceIdentity{}, errInvalidContract
	}
	return result, nil
}

func kernelFileIdentity(identity runner.FileIdentity) (kernel.FileIdentity, error) {
	if identity.Device == 0 {
		return kernel.FileIdentity{}, errInvalidContract
	}
	device, inode, err := signedIdentity(identity.Device, identity.Inode)
	if err != nil {
		return kernel.FileIdentity{}, err
	}
	result, err := kernel.NewFileIdentity(device, inode)
	if err != nil {
		return kernel.FileIdentity{}, errInvalidContract
	}
	return result, nil
}

func runnerFileIdentity(identity kernel.FileIdentity) (runner.FileIdentity, error) {
	if identity.Device() <= 0 || identity.Inode() <= 0 {
		return runner.FileIdentity{}, errInvalidContract
	}
	return runner.FileIdentity{Device: uint64(identity.Device()), Inode: uint64(identity.Inode())}, nil
}

func signedIdentity(device, inode uint64) (int64, int64, error) {
	if device > math.MaxInt64 || inode == 0 || inode > math.MaxInt64 {
		return 0, 0, errInvalidContract
	}
	return int64(device), int64(inode), nil
}

func attemptDigest(value api.AttemptDigest) (kernel.AttemptDigest, error) {
	bytes := value.Bytes()
	result, err := kernel.AttemptDigestFromBytes(bytes[:])
	if err != nil {
		return kernel.AttemptDigest{}, errInvalidContract
	}
	return result, nil
}

func kernelSelectionCheckpoint(result changeworker.Result, repository change.RepositoryIdentity) (kernel.ChangeSelection, error) {
	format, err := kernel.NewObjectFormat(result.Format.Name())
	if err != nil {
		return kernel.ChangeSelection{}, errInvalidContract
	}
	commit, err := kernel.NewCommitID(format, result.Base.Bytes())
	if err != nil {
		return kernel.ChangeSelection{}, errInvalidContract
	}
	repositoryFile, err := changeFileIdentity(repository.Device(), repository.Inode())
	if err != nil {
		return kernel.ChangeSelection{}, errInvalidContract
	}
	selection, err := kernel.NewChangeSelection(format, commit, repositoryFile)
	if err != nil {
		return kernel.ChangeSelection{}, errInvalidContract
	}
	return selection, nil
}

// retainedWorkerCheckpoint reverses the durable facts of an available
// Change into the worker's reopen contract: its base and, unless the Change
// is still a Git-free tree, the branch head the daemon last recorded.
func retainedWorkerCheckpoint(value kernel.Change) (*changeworker.Result, change.RepositoryIdentity, error) {
	if value.Phase != kernel.ChangeAvailable || value.Selection == nil {
		return nil, change.RepositoryIdentity{}, errInvalidContract
	}
	format, base, err := changeCommit(value.Selection.Commit())
	if err != nil {
		return nil, change.RepositoryIdentity{}, err
	}
	result := &changeworker.Result{Format: format, Base: base}
	if value.HeadCommit != nil {
		_, head, err := changeCommit(*value.HeadCommit)
		if err != nil {
			return nil, change.RepositoryIdentity{}, err
		}
		result.Head = &head
	}
	repository, err := changeRepositoryIdentity(value.Selection.RepositoryIdentity())
	if err != nil {
		return nil, change.RepositoryIdentity{}, err
	}
	return result, repository, nil
}

func changeCommit(commit kernel.CommitID) (change.ObjectFormat, change.ObjectID, error) {
	format, err := change.NewObjectFormat(commit.Format().String())
	if err != nil {
		return 0, change.ObjectID{}, errInvalidContract
	}
	id, err := change.NewObjectID(format, commit.Bytes())
	if err != nil {
		return 0, change.ObjectID{}, errInvalidContract
	}
	return format, id, nil
}

func kernelCommit(id change.ObjectID) (kernel.CommitID, error) {
	format, err := kernel.NewObjectFormat(id.Format().Name())
	if err != nil {
		return kernel.CommitID{}, errInvalidContract
	}
	commit, err := kernel.NewCommitID(format, id.Bytes())
	if err != nil {
		return kernel.CommitID{}, errInvalidContract
	}
	return commit, nil
}

func changeRepositoryIdentity(identity kernel.FileIdentity) (change.RepositoryIdentity, error) {
	repository, err := change.NewRepositoryIdentity(uint64(identity.Device()), uint64(identity.Inode()))
	if err != nil {
		return change.RepositoryIdentity{}, errInvalidContract
	}
	return repository, nil
}

func changeFileIdentity(device, inode uint64) (kernel.FileIdentity, error) {
	signedDevice, signedInode, err := signedIdentity(device, inode)
	if err != nil {
		return kernel.FileIdentity{}, err
	}
	result, err := kernel.NewFileIdentity(signedDevice, signedInode)
	if err != nil {
		return kernel.FileIdentity{}, errInvalidContract
	}
	return result, nil
}

func (e contractError) Error() string    { return "daemon: invalid private contract" }
func (e contractError) String() string   { return e.Error() }
func (e contractError) GoString() string { return e.Error() }

type contractError struct{ cause error }

func invalidContract(cause error) error {
	if cause == nil {
		cause = errInvalidContract
	}
	return contractError{cause: cause}
}

func (e contractError) Unwrap() error { return e.cause }

func (e contractError) Is(target error) bool {
	return target == errInvalidContract || errors.Is(e.cause, target)
}

type retainedContractError struct{ cause error }

func (retainedContractError) Error() string    { return "daemon: private runtime effect retained" }
func (e retainedContractError) String() string { return e.Error() }
func (e retainedContractError) GoString() string {
	return e.Error()
}
func (e retainedContractError) Unwrap() error { return e.cause }
func (e retainedContractError) Is(target error) bool {
	return target == errRetainedRuntime || target == errInvalidContract || errors.Is(e.cause, target)
}

func retainedContract(cause error) error {
	if cause == nil {
		cause = errInvalidContract
	}
	return retainedContractError{cause: cause}
}
