package change

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
	device uint64
	inode  uint64
}

// Selection is one immutable exact commit and its complete regular-file tree.
// Repository paths and Git process configuration remain private.
type Selection struct {
	repositoryRoot     string
	repositoryIdentity RepositoryIdentity
	gitExecutable      string
	gitIdentity        gitFileIdentity
	format             ObjectFormat
	base               ObjectID
	manifest           Manifest
}

// RepositoryIdentity returns the exact selected repository-root identity.
func (s Selection) RepositoryIdentity() RepositoryIdentity { return s.repositoryIdentity }

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

func (s Selection) valid() bool {
	return s.repositoryRoot != "" && s.repositoryIdentity.valid() && s.gitExecutable != "" &&
		s.gitIdentity.inode != 0 && s.format.valid() && s.base.format == s.format &&
		s.manifest.format == s.format && s.manifest.base.equal(s.base)
}

// GitProcessError reports a sealed Git child lifecycle failure without
// exposing its stderr or object contents.
type GitProcessError struct {
	Operation string
	Cause     error
}

func (e *GitProcessError) Error() string { return "Git " + e.Operation + " failed: " + e.Cause.Error() }
func (e *GitProcessError) Unwrap() error { return e.Cause }

// GitProtocolError reports malformed or mismatched bounded Git output. Raw
// output is intentionally absent.
type GitProtocolError struct{ Reason string }

func (e *GitProtocolError) Error() string { return "invalid Git protocol response: " + e.Reason }

type gitProcessEvent string

const (
	gitProcessStarted gitProcessEvent = "started"
	gitProcessTermed  gitProcessEvent = "term"
	gitProcessKilled  gitProcessEvent = "kill"
	gitProcessWaited  gitProcessEvent = "waited"
)

type gitProcessHook func(gitProcessEvent)
