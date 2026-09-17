package change

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// ObjectFormat is one closed Git object-format value.
type ObjectFormat byte

const (
	objectFormatSHA1 ObjectFormat = iota + 1
	objectFormatSHA256
)

// NewObjectFormat constructs the closed SHA-1/SHA-256 object-format value.
func NewObjectFormat(name string) (ObjectFormat, error) {
	switch name {
	case "sha1":
		return objectFormatSHA1, nil
	case "sha256":
		return objectFormatSHA256, nil
	default:
		return 0, &ValidationError{Reason: fmt.Sprintf("unsupported object format %q", name)}
	}
}

// Name returns the canonical Git object-format name.
func (f ObjectFormat) Name() string {
	switch f {
	case objectFormatSHA1:
		return "sha1"
	case objectFormatSHA256:
		return "sha256"
	default:
		return ""
	}
}

// OIDLength returns the exact raw object-ID length, or zero for an invalid format.
func (f ObjectFormat) OIDLength() int {
	switch f {
	case objectFormatSHA1:
		return sha1.Size
	case objectFormatSHA256:
		return sha256.Size
	default:
		return 0
	}
}

func (f ObjectFormat) valid() bool { return f == objectFormatSHA1 || f == objectFormatSHA256 }

// ObjectID is an immutable raw Git object ID.
type ObjectID struct {
	format ObjectFormat
	raw    [sha256.Size]byte
}

// NewObjectID validates and copies one raw Git object ID.
func NewObjectID(format ObjectFormat, raw []byte) (ObjectID, error) {
	if !format.valid() || len(raw) != format.OIDLength() {
		return ObjectID{}, &ValidationError{Reason: "object ID length does not match its format"}
	}
	var id ObjectID
	id.format = format
	copy(id.raw[:], raw)
	return id, nil
}

// Format returns the ID's object format.
func (id ObjectID) Format() ObjectFormat { return id.format }

// Bytes returns a copy of the raw object ID.
func (id ObjectID) Bytes() []byte { return bytes.Clone(id.raw[:id.format.OIDLength()]) }

// Hex returns the lowercase hexadecimal object ID.
func (id ObjectID) Hex() string { return hex.EncodeToString(id.raw[:id.format.OIDLength()]) }

// Equal reports exact object ID equality.
func (id ObjectID) Equal(other ObjectID) bool { return id.equal(other) }

func (id ObjectID) equal(other ObjectID) bool {
	return id.format == other.format && id.raw == other.raw
}

// BranchName is the branch a Change's worktree is checked out on, one per
// task incarnation, and the branch the Maintainer App publishes it to.
func BranchName(changeID string) string { return "factory/" + changeID[:min(12, len(changeID))] }

// WorktreeFacts are the observed facts of one Change worktree: the commit
// its HEAD names, the branch HEAD is on (empty when detached), and whether
// the work tree or index differs from that commit.
type WorktreeFacts struct {
	head   ObjectID
	branch string
	dirty  bool
}

// Head returns the commit the worktree's HEAD names.
func (f WorktreeFacts) Head() ObjectID { return f.head }

// Branch returns the short branch name HEAD is on, or empty when detached.
func (f WorktreeFacts) Branch() string { return f.branch }

// Dirty reports uncommitted or untracked work in the worktree.
func (f WorktreeFacts) Dirty() bool { return f.dirty }

// TrustedGitExecutable is the only Git installation whose complete path is
// rooted in system-owned directories on macOS. Xcode applications live below
// the group-writable /Applications directory and cannot provide this authority.
const TrustedGitExecutable = "/Library/Developer/CommandLineTools/usr/bin/git"

// TrustedDeveloperGitPath is the one Git-trust path predicate. SelectGit
// enforces it per attempt; boot-time callers reuse it so a configuration that
// would fail every attempt refuses the process instead.
func TrustedDeveloperGitPath(path string) bool {
	return path == TrustedGitExecutable
}

const maxStoreInteger = uint64(1<<63 - 1)

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
	trusted    bool
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

type repositoryCheckpoint struct {
	root    RepositoryIdentity
	git     gitAdminIdentity
	config  gitAdminIdentity
	objects gitAdminIdentity
}

// Selection is one immutable exact commit of the project repository.
// Repository paths and Git process configuration remain private.
type Selection struct {
	repositoryRoot string
	repository     repositoryCheckpoint
	gitExecutable  string
	gitIdentity    gitFileIdentity
	format         ObjectFormat
	base           ObjectID
}

// RepositoryIdentity returns the exact selected repository-root identity.
func (s Selection) RepositoryIdentity() RepositoryIdentity { return s.repository.root }

// ObjectFormat returns the selected repository object format.
func (s Selection) ObjectFormat() ObjectFormat { return s.format }

// Base returns the exact selected commit object ID.
func (s Selection) Base() ObjectID { return s.base }

// String and GoString deliberately keep repository and executable locators out
// of logs while the immutable selection is passed between daemon-owned phases.
func (s Selection) String() string   { return "selected Git Change" }
func (s Selection) GoString() string { return "change.Selection{private}" }

func (s Selection) valid() bool {
	return s.repositoryRoot != "" && s.repository.root.valid() && s.gitExecutable != "" &&
		s.gitIdentity.inode != 0 &&
		s.format.valid() && s.base.format == s.format
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
