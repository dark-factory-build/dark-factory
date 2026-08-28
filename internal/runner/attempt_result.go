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
	"strings"

	"golang.org/x/sys/unix"
)

const (
	AttemptResultSpoolName = "attempt-result.json"
	maxAttemptResultBytes  = 1024
	resultProofBytes       = 32
)

// ResultProof is a per-run capability known only to the daemon and trusted
// outer attempt runner. Store persists SHA-256(ResultProof), never this secret.
type ResultProof struct {
	value [resultProofBytes]byte
}

func NewResultProof(value [resultProofBytes]byte) (ResultProof, error) {
	proof := ResultProof{value: value}
	if !validResultProof(proof) {
		return ResultProof{}, ErrIdentity
	}
	return proof, nil
}

const resultProofRedaction = "runner.ResultProof([redacted])"

func (ResultProof) String() string   { return resultProofRedaction }
func (ResultProof) GoString() string { return resultProofRedaction }

// Format ignores every verb, flag, width and precision so no diagnostic form
// can fall through to the private proof byte representation.
func (ResultProof) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, resultProofRedaction)
}

func validResultProof(proof ResultProof) bool { return proof.value != ([resultProofBytes]byte{}) }

func decodeResultProof(value string) (ResultProof, error) {
	var proof ResultProof
	if len(value) != hex.EncodedLen(len(proof.value)) || value != strings.ToLower(value) {
		return proof, ErrIdentity
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(proof.value) {
		return proof, errors.Join(ErrIdentity, err)
	}
	copy(proof.value[:], decoded)
	if !validResultProof(proof) {
		return ResultProof{}, ErrIdentity
	}
	return proof, nil
}

func encodeResultProof(proof ResultProof) (string, error) {
	if !validResultProof(proof) {
		return "", ErrIdentity
	}
	return hex.EncodeToString(proof.value[:]), nil
}

type AttemptResultKind string

const (
	AttemptResultInnerUnregisteredConverged AttemptResultKind = "inner_unregistered_converged"
	AttemptResultInnerConverged             AttemptResultKind = "inner_converged"
)

// AttemptResult is the closed value authenticated from attempt-result.json.
// Its fields remain private so callers cannot weaken the canonical parser.
type AttemptResult struct {
	attemptID string
	kind      AttemptResultKind
	proof     ResultProof
	process   Identity
	code      int
	signal    int
}

func (result AttemptResult) AttemptID() string       { return result.attemptID }
func (result AttemptResult) Kind() AttemptResultKind { return result.kind }

// String, GoString and Format render every public field and never the proof.
// fmt cannot invoke methods on unexported fields, so the enclosing value must
// redact rather than rely on the ResultProof methods.
func (result AttemptResult) String() string {
	return fmt.Sprintf("runner.AttemptResult{attempt_id:%q kind:%q process:%d:%d code:%d signal:%d}", result.attemptID, result.kind, result.process.PID, result.process.PGID, result.code, result.signal)
}
func (result AttemptResult) GoString() string { return result.String() }
func (result AttemptResult) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, result.String())
}
func (result AttemptResult) Process() (Identity, bool) {
	return result.process, result.kind == AttemptResultInnerConverged
}
func (result AttemptResult) Code() (int, bool) {
	return result.code, result.kind == AttemptResultInnerConverged && result.code >= 0 && result.signal == 0
}
func (result AttemptResult) Signal() (int, bool) {
	return result.signal, result.kind == AttemptResultInnerConverged && result.code == -1 && result.signal > 0
}

// AttemptResultNotice is the complete non-authoritative live notification.
// Result bytes are always reopened from the exact runtime directory.
type AttemptResultNotice struct {
	Identity FileIdentity
	Digest   string
}

// AttemptResultRecord binds parsed bytes and the inner-activation census to
// one exact no-replace inode. Only accessors are exposed across packages.
type AttemptResultRecord struct {
	result         AttemptResult
	notice         AttemptResultNotice
	innerActivated bool
}

func (record *AttemptResultRecord) Result() AttemptResult {
	if record == nil {
		return AttemptResult{}
	}
	result := record.result
	result.proof = ResultProof{}
	return result
}
func (record *AttemptResultRecord) Notice() AttemptResultNotice {
	if record == nil {
		return AttemptResultNotice{}
	}
	return record.notice
}
func (record *AttemptResultRecord) InnerActivated() bool {
	return record != nil && record.innerActivated
}
func (record *AttemptResultRecord) ProofDigest() [sha256.Size]byte {
	if record == nil {
		return [sha256.Size]byte{}
	}
	return sha256.Sum256(record.result.proof.value[:])
}

func (record *AttemptResultRecord) String() string {
	if record == nil {
		return "runner.AttemptResultRecord(<nil>)"
	}
	return fmt.Sprintf("runner.AttemptResultRecord{attempt_id:%q kind:%q file:%d:%d digest:%q proof_digest:%x}", record.result.attemptID, record.result.kind, record.notice.Identity.Device, record.notice.Identity.Inode, record.notice.Digest, record.ProofDigest())
}

func (record *AttemptResultRecord) GoString() string { return record.String() }

// Format on the value receiver covers dereferenced records, whose unexported
// fields fmt would otherwise print raw.
func (record AttemptResultRecord) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, (&record).String())
}

type attemptResultWire struct {
	Version   int                    `json:"version"`
	AttemptID string                 `json:"attempt_id"`
	Kind      AttemptResultKind      `json:"kind"`
	Proof     string                 `json:"proof"`
	Process   *Identity              `json:"process,omitempty"`
	Exit      *attemptResultExitWire `json:"exit,omitempty"`
}

type attemptResultExitWire struct {
	Code   *int `json:"code,omitempty"`
	Signal *int `json:"signal,omitempty"`
}

func innerUnregisteredConvergedResult(attemptID string, proof ResultProof) (AttemptResult, error) {
	result := AttemptResult{attemptID: attemptID, kind: AttemptResultInnerUnregisteredConverged, proof: proof, code: -1}
	if !validAttemptResult(result) {
		return AttemptResult{}, ErrIdentity
	}
	return result, nil
}

func innerConvergedResult(attemptID string, proof ResultProof, process Identity, exit Exit) (AttemptResult, error) {
	result := AttemptResult{attemptID: attemptID, kind: AttemptResultInnerConverged, proof: proof, process: process, code: exit.Code, signal: exit.Signal}
	if !validAttemptResult(result) {
		return AttemptResult{}, ErrIdentity
	}
	return result, nil
}

func validAttemptResult(result AttemptResult) bool {
	if validateAttemptName(result.attemptID, 256) != nil || !validResultProof(result.proof) {
		return false
	}
	switch result.kind {
	case AttemptResultInnerUnregisteredConverged:
		return result.process == (Identity{}) && result.code == -1 && result.signal == 0
	case AttemptResultInnerConverged:
		return result.process.Valid() && (result.code >= 0 && result.signal == 0 || result.code == -1 && result.signal > 0)
	default:
		return false
	}
}

func canonicalAttemptResult(result AttemptResult) ([]byte, error) {
	if !validAttemptResult(result) {
		return nil, ErrIdentity
	}
	proof, err := encodeResultProof(result.proof)
	if err != nil {
		return nil, err
	}
	wire := attemptResultWire{Version: 1, AttemptID: result.attemptID, Kind: result.kind, Proof: proof}
	if result.kind == AttemptResultInnerConverged {
		wire.Process = &result.process
		wire.Exit = &attemptResultExitWire{}
		if result.code >= 0 {
			code := result.code
			wire.Exit.Code = &code
		} else {
			signal := result.signal
			wire.Exit.Signal = &signal
		}
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || len(body) > maxAttemptResultBytes {
		return nil, ErrIdentity
	}
	return body, nil
}

func decodeCanonicalAttemptResult(body []byte) (AttemptResult, error) {
	if len(body) == 0 || len(body) > maxAttemptResultBytes {
		return AttemptResult{}, ErrIdentity
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var wire attemptResultWire
	if err := decoder.Decode(&wire); err != nil {
		return AttemptResult{}, ErrIdentity
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return AttemptResult{}, ErrIdentity
	}
	if wire.Version != 1 {
		return AttemptResult{}, ErrIdentity
	}
	proof, err := decodeResultProof(wire.Proof)
	if err != nil {
		return AttemptResult{}, err
	}
	result := AttemptResult{attemptID: wire.AttemptID, kind: wire.Kind, proof: proof, code: -1}
	switch wire.Kind {
	case AttemptResultInnerUnregisteredConverged:
		if wire.Process != nil || wire.Exit != nil {
			return AttemptResult{}, ErrIdentity
		}
	case AttemptResultInnerConverged:
		if wire.Process == nil || wire.Exit == nil || (wire.Exit.Code == nil) == (wire.Exit.Signal == nil) {
			return AttemptResult{}, ErrIdentity
		}
		result.process = *wire.Process
		if wire.Exit.Code != nil {
			result.code = *wire.Exit.Code
		} else {
			result.signal = *wire.Exit.Signal
		}
	default:
		return AttemptResult{}, ErrIdentity
	}
	canonical, err := canonicalAttemptResult(result)
	if err != nil || !bytes.Equal(body, canonical) {
		return AttemptResult{}, ErrIdentity
	}
	return result, nil
}

func publishAttemptResult(dir *os.File, result AttemptResult) (*AttemptResultRecord, error) {
	return publishAttemptResultWithWrite(dir, result, func(fd int, body []byte) error { return writeAll(fd, body) })
}

func publishAttemptResultWithWrite(dir *os.File, result AttemptResult, write func(int, []byte) error) (_ *AttemptResultRecord, resultErr error) {
	body, err := canonicalAttemptResult(result)
	if err != nil || write == nil {
		return nil, errors.Join(ErrIdentity, err)
	}
	directory, err := validatePrivateDirectory(dir)
	if err != nil {
		return nil, err
	}
	innerActivated, err := inspectAttemptMarkers(dir, directory.FileIdentity.Device)
	if err != nil || result.kind == AttemptResultInnerUnregisteredConverged && innerActivated {
		return nil, errors.Join(ErrIdentity, err)
	}
	fd, err := unix.Openat(int(dir.Fd()), AttemptResultSpoolName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if errors.Is(err, unix.EEXIST) {
		return nil, ErrConflict
	}
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "attempt-result-publisher")
	fileOpen := true
	defer func() {
		if fileOpen {
			resultErr = errors.Join(resultErr, file.Close())
		}
	}()
	var created unix.Stat_t
	if err := unix.Fstat(fd, &created); err != nil || !validAttemptResultFile(created, directory.FileIdentity.Device, 0) || created.Size != 0 {
		return nil, errors.Join(ErrIdentity, err)
	}
	if err := write(fd, body); err != nil {
		return nil, err
	}
	if err := unix.Fsync(fd); err != nil {
		return nil, err
	}
	if err := verifyAttemptResultDescriptorAndName(file, dir, created, int64(len(body))); err != nil {
		return nil, err
	}
	if err := unix.Fsync(int(dir.Fd())); err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	fileOpen = false
	digest := sha256.Sum256(body)
	want := AttemptResultNotice{Identity: FileIdentity{Device: uint64(created.Dev), Inode: created.Ino}, Digest: hex.EncodeToString(digest[:])}
	record, err := loadAttemptResult(dir, result.attemptID, &want, false)
	if err != nil || record.result != result || record.innerActivated != innerActivated {
		return nil, errors.Join(ErrIdentity, err)
	}
	return record, nil
}

// AuthenticateAttemptResult promotes complete canonical residue to durable
// evidence before returning it. A notice narrows live observation but is never
// trusted as result content; nil is the ordinary lost-notice/recovery path.
func AuthenticateAttemptResult(dir *os.File, attemptID string, notice *AttemptResultNotice) (*AttemptResultRecord, error) {
	return loadAttemptResult(dir, attemptID, notice, true)
}

func loadAttemptResult(dir *os.File, attemptID string, notice *AttemptResultNotice, promote bool) (result *AttemptResultRecord, resultErr error) {
	if validateAttemptName(attemptID, 256) != nil {
		return nil, ErrIdentity
	}
	directory, err := validatePrivateDirectory(dir)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(dir.Fd()), AttemptResultSpoolName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "attempt-result-consumer")
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !validAttemptResultFile(opened, directory.FileIdentity.Device, 0) || opened.Size <= 0 || opened.Size > maxAttemptResultBytes {
		return nil, errors.Join(ErrIdentity, err)
	}
	parsed, body, digest, err := readAttemptResultFile(file)
	if err != nil || parsed.attemptID != attemptID {
		return nil, errors.Join(ErrIdentity, err)
	}
	gotNotice := AttemptResultNotice{Identity: FileIdentity{Device: uint64(opened.Dev), Inode: opened.Ino}, Digest: hex.EncodeToString(digest[:])}
	if notice != nil && *notice != gotNotice {
		return nil, ErrIdentity
	}
	innerActivated, err := inspectAttemptMarkers(dir, directory.FileIdentity.Device)
	if err != nil || parsed.kind == AttemptResultInnerUnregisteredConverged && innerActivated {
		return nil, errors.Join(ErrIdentity, err)
	}
	if promote {
		if err := unix.Fsync(fd); err != nil {
			return nil, err
		}
	}
	if err := revalidateAttemptResult(file, dir, opened, parsed, body, directory.FileIdentity.Device, innerActivated); err != nil {
		return nil, err
	}
	if promote {
		if err := unix.Fsync(int(dir.Fd())); err != nil {
			return nil, err
		}
		if err := revalidateAttemptResult(file, dir, opened, parsed, body, directory.FileIdentity.Device, innerActivated); err != nil {
			return nil, err
		}
	}
	return &AttemptResultRecord{result: parsed, notice: gotNotice, innerActivated: innerActivated}, nil
}

func readAttemptResultFile(file *os.File) (AttemptResult, []byte, [sha256.Size]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return AttemptResult{}, nil, [sha256.Size]byte{}, err
	}
	body, err := io.ReadAll(io.LimitReader(file, maxAttemptResultBytes+1))
	if err != nil || len(body) > maxAttemptResultBytes {
		return AttemptResult{}, nil, [sha256.Size]byte{}, errors.Join(ErrIdentity, err)
	}
	result, err := decodeCanonicalAttemptResult(body)
	return result, body, sha256.Sum256(body), err
}

func revalidateAttemptResult(file, dir *os.File, opened unix.Stat_t, parsed AttemptResult, body []byte, device uint64, innerActivated bool) error {
	if err := verifyAttemptResultDescriptorAndName(file, dir, opened, int64(len(body))); err != nil {
		return err
	}
	again, againBody, _, err := readAttemptResultFile(file)
	if err != nil || again != parsed || !bytes.Equal(againBody, body) {
		return errors.Join(ErrIdentity, err)
	}
	marker, err := inspectAttemptMarkers(dir, device)
	if err != nil || marker != innerActivated {
		return errors.Join(ErrIdentity, err)
	}
	return nil
}

func inspectAttemptMarkers(dir *os.File, device uint64) (bool, error) {
	present := func(name string, required bool) (bool, error) {
		var stat unix.Stat_t
		err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if errors.Is(err, unix.ENOENT) && !required {
			return false, nil
		}
		if err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o7777 != 0o600 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || stat.Size != 0 || uint64(stat.Dev) != device || stat.Ino == 0 {
			return false, errors.Join(ErrIdentity, err)
		}
		return true, nil
	}
	if _, err := present(OuterActivationMarkerName, true); err != nil {
		return false, err
	}
	for _, name := range []string{GateConfigScratchName, GateStdinScratchName, TerminalSpoolName, TerminalScratchName} {
		var stat unix.Stat_t
		err := unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if !errors.Is(err, unix.ENOENT) {
			return false, errors.Join(ErrIdentity, err)
		}
	}
	return present(InnerActivationMarkerName, false)
}

func validAttemptResultFile(stat unix.Stat_t, device uint64, exactSize int64) bool {
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && stat.Mode&0o7777 == 0o600 && stat.Uid == uint32(os.Geteuid()) && stat.Nlink == 1 && uint64(stat.Dev) == device && stat.Ino != 0 && (exactSize == 0 || stat.Size == exactSize)
}

func verifyAttemptResultDescriptorAndName(file, dir *os.File, expected unix.Stat_t, exactSize int64) error {
	var current, named unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &current); err != nil {
		return errors.Join(ErrIdentity, err)
	}
	if err := unix.Fstatat(int(dir.Fd()), AttemptResultSpoolName, &named, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return errors.Join(ErrIdentity, err)
	}
	if !validAttemptResultFile(current, uint64(expected.Dev), exactSize) || !validAttemptResultFile(named, uint64(expected.Dev), exactSize) || current.Dev != expected.Dev || current.Ino != expected.Ino || named.Dev != current.Dev || named.Ino != current.Ino {
		return ErrIdentity
	}
	return nil
}

var testAfterAttemptResultProof func()
var testCloseAttemptResultRemoval func(*os.File) error

func closeAttemptResultRemoval(file *os.File) error {
	if testCloseAttemptResultRemoval != nil {
		return testCloseAttemptResultRemoval(file)
	}
	return file.Close()
}

// RemoveAttemptResult removes only the exact authenticated record. Store
// authorization is deliberately a preceding daemon operation, not a boolean
// accepted by this filesystem primitive.
func RemoveAttemptResult(dir *os.File, want *AttemptResultRecord) (resultErr error) {
	if want == nil {
		return ErrIdentity
	}
	got, file, opened, err := openAttemptResultForRemoval(dir, want)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, closeAttemptResultRemoval(file)) }()
	if got.result != want.result || got.notice != want.notice || got.innerActivated != want.innerActivated {
		return ErrIdentity
	}
	if testAfterAttemptResultProof != nil {
		testAfterAttemptResultProof()
	}
	if err := verifyAttemptResultDescriptorAndName(file, dir, opened, opened.Size); err != nil {
		return err
	}
	if err := unix.Unlinkat(int(dir.Fd()), AttemptResultSpoolName, 0); err != nil {
		return err
	}
	return FinishAttemptResultRemoval(dir)
}

func openAttemptResultForRemoval(dir *os.File, want *AttemptResultRecord) (*AttemptResultRecord, *os.File, unix.Stat_t, error) {
	directory, err := validatePrivateDirectory(dir)
	if err != nil {
		return nil, nil, unix.Stat_t{}, err
	}
	fd, err := unix.Openat(int(dir.Fd()), AttemptResultSpoolName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, unix.Stat_t{}, err
	}
	file := os.NewFile(uintptr(fd), "attempt-result-removal")
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil || !validAttemptResultFile(opened, directory.FileIdentity.Device, 0) {
		return nil, nil, unix.Stat_t{}, errors.Join(ErrIdentity, err, closeAttemptResultRemoval(file))
	}
	parsed, body, digest, err := readAttemptResultFile(file)
	notice := AttemptResultNotice{Identity: FileIdentity{Device: uint64(opened.Dev), Inode: opened.Ino}, Digest: hex.EncodeToString(digest[:])}
	marker, markerErr := inspectAttemptMarkers(dir, directory.FileIdentity.Device)
	if err != nil || markerErr != nil || parsed.attemptID != want.result.attemptID || notice != want.notice || parsed != want.result || marker != want.innerActivated || verifyAttemptResultDescriptorAndName(file, dir, opened, int64(len(body))) != nil {
		return nil, nil, unix.Stat_t{}, errors.Join(ErrIdentity, err, markerErr, closeAttemptResultRemoval(file))
	}
	return &AttemptResultRecord{result: parsed, notice: notice, innerActivated: marker}, file, opened, nil
}

// FinishAttemptResultRemoval durably promotes an already-unlinked result to a
// stable absence postcondition. Callers must first obtain Store authorization.
func FinishAttemptResultRemoval(dir *os.File) error {
	directory, err := validatePrivateDirectory(dir)
	if err != nil {
		return err
	}
	if err := requireAttemptResultAbsent(dir); err != nil {
		return err
	}
	if err := unix.Fsync(int(dir.Fd())); err != nil {
		return err
	}
	if _, err := validatePrivateDirectory(dir); err != nil {
		return err
	}
	if err := requireAttemptResultAbsent(dir); err != nil {
		return err
	}
	inner, err := inspectAttemptMarkers(dir, directory.FileIdentity.Device)
	if err != nil {
		return err
	}
	_ = inner
	return nil
}

func requireAttemptResultAbsent(dir *os.File) error {
	var stat unix.Stat_t
	err := unix.Fstatat(int(dir.Fd()), AttemptResultSpoolName, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return errors.Join(ErrIdentity, err, fmt.Errorf("runner: attempt result remains present"))
}
