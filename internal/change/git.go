package change

import (
	"context"
	"errors"
)

// RepositoryIdentity is one immutable repository-root device/inode identity.
type RepositoryIdentity struct {
	device uint64
	inode  uint64
}

// NewRepositoryIdentity reconstructs an identity from signed SQLite INTEGERs.
func NewRepositoryIdentity(device, inode uint64) (RepositoryIdentity, error) {
	if device > maxStoreInteger || inode == 0 || inode > maxStoreInteger {
		return RepositoryIdentity{}, &ValidationError{Reason: "repository identity is not representable in Store"}
	}
	return RepositoryIdentity{device: device, inode: inode}, nil
}

// Device returns the repository root device number.
func (i RepositoryIdentity) Device() uint64 { return i.device }

// Inode returns the repository root inode number.
func (i RepositoryIdentity) Inode() uint64 { return i.inode }

// Equal reports exact device/inode equality.
func (i RepositoryIdentity) Equal(other RepositoryIdentity) bool { return i == other }

func (i RepositoryIdentity) valid() bool {
	return i.device <= maxStoreInteger && i.inode > 0 && i.inode <= maxStoreInteger
}

type gitFileIdentity struct {
	device     uint64
	inode      uint64
	uid        uint32
	mode       uint32
	size       int64
	modifiedNS int64
	changedNS  int64
	digest     [32]byte
}

type gitAdminIdentity struct {
	device     uint64
	inode      uint64
	uid        uint32
	mode       uint32
	size       int64
	modifiedNS int64
	changedNS  int64
	digest     [32]byte
}

type gitObjectStoreIdentity struct {
	entryCount uint32
	digest     [32]byte
}

type repositoryCheckpoint struct {
	root        RepositoryIdentity
	git         gitAdminIdentity
	config      gitAdminIdentity
	objects     gitAdminIdentity
	objectStore gitObjectStoreIdentity
}

// Selection is one immutable exact commit and its complete regular-file tree.
// Repository paths and Git process configuration remain private.
type Selection struct {
	repositoryRoot string
	repository     repositoryCheckpoint
	gitExecutable  string
	gitIdentity    gitFileIdentity
	format         ObjectFormat
	base           ObjectID
	manifest       Manifest
}

// RepositoryIdentity returns the exact selected repository-root identity.
func (s Selection) RepositoryIdentity() RepositoryIdentity { return s.repository.root }

// ObjectFormat returns the selected repository object format.
func (s Selection) ObjectFormat() ObjectFormat { return s.format }

// Base returns the exact selected commit object ID.
func (s Selection) Base() ObjectID { return s.base }

// Manifest returns the immutable selected tree manifest.
func (s Selection) Manifest() Manifest { return s.manifest }

// Commitment returns the base-bound selected tree commitment.
func (s Selection) Commitment() Commitment { return s.manifest.Commitment() }

// EntryCount returns selected regular files plus implied directories.
func (s Selection) EntryCount() uint64 { return s.manifest.EntryCount() }

// BlobBytes returns the exact sum of selected blob sizes.
func (s Selection) BlobBytes() uint64 { return s.manifest.BlobBytes() }

// String and GoString deliberately keep repository and executable locators out
// of logs while the immutable selection is passed between daemon-owned phases.
func (s Selection) String() string   { return "selected Git Change" }
func (s Selection) GoString() string { return "change.Selection{private}" }

func (s Selection) valid() bool {
	return s.repositoryRoot != "" && s.repository.root.valid() && s.gitExecutable != "" &&
		s.gitIdentity.inode != 0 &&
		s.format.valid() && s.base.format == s.format &&
		s.manifest.format == s.format && s.manifest.base.equal(s.base)
}

type gitFailure byte

const (
	gitFailureProcess gitFailure = iota + 1
	gitFailureProtocol
	gitFailurePrivateIO
)

// GitError is one closed, path-safe Git boundary failure. It intentionally
// exposes neither raw child errors nor repository, executable, protocol,
// stderr, or blob data.
type GitError struct {
	failure      gitFailure
	contextError error
	groupCleanup bool
}

func newGitError(failure gitFailure) *GitError { return &GitError{failure: failure} }

func newGitContextError(err error, groupCleanup bool) *GitError {
	contextError := context.Canceled
	if errors.Is(err, context.DeadlineExceeded) {
		contextError = context.DeadlineExceeded
	}
	return &GitError{failure: gitFailureProcess, contextError: contextError, groupCleanup: groupCleanup}
}

func newGitCleanupError(failure gitFailure) *GitError {
	return &GitError{failure: failure, groupCleanup: true}
}

func (e *GitError) Error() string {
	if e.groupCleanup {
		return "Git operation failed; registered wrapper-group cleanup is required"
	}
	if errors.Is(e.contextError, context.Canceled) {
		return "Git operation canceled"
	}
	if errors.Is(e.contextError, context.DeadlineExceeded) {
		return "Git operation deadline exceeded"
	}
	switch e.failure {
	case gitFailureProtocol:
		return "invalid Git protocol response"
	case gitFailurePrivateIO:
		return "Git private I/O failed"
	default:
		return "Git process failed"
	}
}

// GoString prevents diagnostic formatting from exposing private fields.
func (e *GitError) GoString() string { return e.Error() }

// Unwrap preserves only path-safe context cancellation classification.
func (e *GitError) Unwrap() error { return e.contextError }

// RequiresGroupCleanup reports that the registered source-wrapper owner must
// clean its already-recorded process group before any provider execution.
func (e *GitError) RequiresGroupCleanup() bool { return e.groupCleanup }

type gitProcessEvent string

const (
	gitProcessStarted gitProcessEvent = "started"
	gitProcessTermed  gitProcessEvent = "term"
	gitProcessKilled  gitProcessEvent = "kill"
	gitProcessWaited  gitProcessEvent = "waited"
)

type gitProcessHook func(gitProcessEvent)
