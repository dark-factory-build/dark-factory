package change

import (
	"context"
	"path/filepath"
)

// BlobSource returns the exact bytes for one selected Git blob. It must not
// mutate the returned slice while PopulateAndPublish is using it.
type BlobSource func(context.Context, ObjectID) ([]byte, error)

// StageIdentity is one immutable filesystem device/inode identity.
type StageIdentity struct {
	device uint64
	inode  uint64
}

const maxStoreInteger = uint64(1<<63 - 1)

// NewStageIdentity reconstructs one exact identity from SQLite INTEGER values.
func NewStageIdentity(device, inode uint64) (StageIdentity, error) {
	if device > maxStoreInteger || inode == 0 || inode > maxStoreInteger {
		return StageIdentity{}, &ValidationError{Reason: "stage identity is not representable in Store"}
	}
	return StageIdentity{device: device, inode: inode}, nil
}

// Device returns the filesystem device number.
func (i StageIdentity) Device() uint64 { return i.device }

// Inode returns the filesystem inode number.
func (i StageIdentity) Inode() uint64 { return i.inode }

// Equal reports exact device/inode equality.
func (i StageIdentity) Equal(other StageIdentity) bool { return i == other }

func (i StageIdentity) valid() bool {
	return i.device <= maxStoreInteger && i.inode > 0 && i.inode <= maxStoreInteger
}

// TreeFacts are immutable facts reconstructed from one exact plain tree.
type TreeFacts struct {
	identity   StageIdentity
	commitment Commitment
	entryCount uint64
	blobBytes  uint64
}

// Identity returns the exact tree-root identity.
func (f TreeFacts) Identity() StageIdentity { return f.identity }

// Commitment returns the reconstructed manifest commitment.
func (f TreeFacts) Commitment() Commitment { return f.commitment }

// EntryCount returns regular files plus directories, excluding the root.
func (f TreeFacts) EntryCount() uint64 { return f.entryCount }

// BlobBytes returns the exact total regular-file bytes.
func (f TreeFacts) BlobBytes() uint64 { return f.blobBytes }

// Published identifies one successfully published Change.
type Published struct {
	path  string
	facts TreeFacts
}

// Path returns the clean absolute published path.
func (p Published) Path() string { return p.path }

// Facts returns reconstructed publication facts.
func (p Published) Facts() TreeFacts { return p.facts }

type materializeStep string

const (
	stepAfterPrepareMkdir     materializeStep = "after prepare mkdir"
	stepBeforePrepareFsync    materializeStep = "before prepare fsync"
	stepDuringBlobHash        materializeStep = "during blob hash"
	stepBeforeEntryParentOpen materializeStep = "before entry parent open"
	stepBeforeFileCreate      materializeStep = "before file create"
	stepBeforeFileWrite       materializeStep = "before file write"
	stepBeforeFileFsync       materializeStep = "before file fsync"
	stepBeforeTreeVerify      materializeStep = "before tree verify"
	stepDuringTreeScan        materializeStep = "during tree scan"
	stepDuringDirectoryRead   materializeStep = "during directory read"
	stepBeforeTreeFsync       materializeStep = "before tree fsync"
	stepDuringTreeFsync       materializeStep = "during tree fsync"
	stepBeforeRename          materializeStep = "before no-replace rename"
	stepAfterRename           materializeStep = "after no-replace rename"
	stepBeforeParentFsync     materializeStep = "before parent fsync"
	stepBeforeRecordedRemoval materializeStep = "before recorded removal"
)

type materializePoint struct {
	step        materializeStep
	parent      string
	stagingName string
	targetName  string
	entryPath   []byte
}

type materializeHook func(materializePoint) error

func changePath(parent, name string) string { return filepath.Join(parent, name) }
