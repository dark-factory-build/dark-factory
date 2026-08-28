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

func kernelSelectionCheckpoint(selection changeworker.SelectionReport) (kernel.ChangeSelection, error) {
	if err := changeworker.ValidateSelectionReport(selection); err != nil || selection.EntryCount > math.MaxUint32 {
		return kernel.ChangeSelection{}, errInvalidContract
	}
	format, err := kernel.NewObjectFormat(selection.Format.Name())
	if err != nil {
		return kernel.ChangeSelection{}, errInvalidContract
	}
	commit, err := kernel.NewCommitID(format, selection.Base.Bytes())
	if err != nil {
		return kernel.ChangeSelection{}, errInvalidContract
	}
	digest, err := kernel.TreeDigestFromBytes(selection.Commitment.Bytes())
	if err != nil {
		return kernel.ChangeSelection{}, errInvalidContract
	}
	result, err := kernel.NewChangeSelection(format, commit, digest, uint32(selection.EntryCount), selection.BlobBytes)
	if err != nil {
		return kernel.ChangeSelection{}, errInvalidContract
	}
	return result, nil
}

func kernelStageIdentity(identity change.StageIdentity) (kernel.FileIdentity, error) {
	return changeFileIdentity(identity.Device(), identity.Inode())
}

func kernelAvailability(facts change.TreeFacts) (kernel.ChangeAvailability, error) {
	checkpoint := changeworker.PopulationReport{
		Identity: facts.Identity(), Commitment: facts.Commitment(),
		EntryCount: facts.EntryCount(), BlobBytes: facts.BlobBytes(),
	}
	return kernelAvailabilityCheckpoint(checkpoint)
}

func kernelAvailabilityCheckpoint(facts changeworker.PopulationReport) (kernel.ChangeAvailability, error) {
	if err := changeworker.ValidatePopulationReport(facts); err != nil || facts.EntryCount > math.MaxUint32 {
		return kernel.ChangeAvailability{}, errInvalidContract
	}
	digest, err := kernel.TreeDigestFromBytes(facts.Commitment.Bytes())
	if err != nil {
		return kernel.ChangeAvailability{}, errInvalidContract
	}
	source, err := changeFileIdentity(facts.Identity.Device(), facts.Identity.Inode())
	if err != nil {
		return kernel.ChangeAvailability{}, err
	}
	result, err := kernel.NewChangeAvailability(digest, uint32(facts.EntryCount), facts.BlobBytes, source)
	if err != nil {
		return kernel.ChangeAvailability{}, errInvalidContract
	}
	return result, nil
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

// inspectPublishedArguments reverses the durable facts needed by
// change.InspectPublished without recovering any path from process memory.
func inspectPublishedArguments(selection kernel.ChangeSelection, stage kernel.FileIdentity) (change.ObjectFormat, change.ObjectID, change.StageIdentity, error) {
	format, err := change.NewObjectFormat(selection.ObjectFormat().String())
	if err != nil {
		return 0, change.ObjectID{}, change.StageIdentity{}, errInvalidContract
	}
	base, err := change.NewObjectID(format, selection.Commit().Bytes())
	if err != nil {
		return 0, change.ObjectID{}, change.StageIdentity{}, errInvalidContract
	}
	if stage.Device() < 0 || stage.Inode() <= 0 {
		return 0, change.ObjectID{}, change.StageIdentity{}, errInvalidContract
	}
	identity, err := change.NewStageIdentity(uint64(stage.Device()), uint64(stage.Inode()))
	if err != nil {
		return 0, change.ObjectID{}, change.StageIdentity{}, errInvalidContract
	}
	return format, base, identity, nil
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
