package change

import (
	"context"
	"path/filepath"
)

// BlobSource returns the exact bytes for one selected Git blob. The caller
// retains ownership of the returned slice; Materialize never exposes it.
type BlobSource func(context.Context, ObjectID) ([]byte, error)

// MaterializeResult is the immutable identity of one published plain tree.
type MaterializeResult struct {
	path       string
	commitment Commitment
	device     uint64
	inode      uint64
	fileCount  uint64
	blobBytes  uint64
}

// Path returns the absolute path of the published Change.
func (r MaterializeResult) Path() string { return r.path }

// Commitment returns the canonical selected and reconstructed tree commitment.
func (r MaterializeResult) Commitment() Commitment { return r.commitment }

// Device returns the published root directory's device identity.
func (r MaterializeResult) Device() uint64 { return r.device }

// Inode returns the published root directory's inode identity.
func (r MaterializeResult) Inode() uint64 { return r.inode }

// FileCount returns the exact number of regular files.
func (r MaterializeResult) FileCount() uint64 { return r.fileCount }

// BlobBytes returns the exact total regular-file bytes.
func (r MaterializeResult) BlobBytes() uint64 { return r.blobBytes }

// Materialize creates and atomically publishes an exact repository-free Change
// below parent. target must be one name, never a path.
func Materialize(ctx context.Context, parent, target string, manifest Manifest, source BlobSource) (MaterializeResult, error) {
	return materialize(ctx, parent, target, manifest, source, nil)
}

type materializeStep string

const (
	stepBeforeEntryParentOpen   materializeStep = "before entry parent open"
	stepBeforeFileCreate        materializeStep = "before file create"
	stepBeforeFileWrite         materializeStep = "before file write"
	stepBeforeFileFsync         materializeStep = "before file fsync"
	stepBeforeTreeVerify        materializeStep = "before tree verify"
	stepBeforeTreeFsync         materializeStep = "before tree fsync"
	stepBeforeRename            materializeStep = "before no-replace rename"
	stepAfterRename             materializeStep = "after no-replace rename"
	stepBeforeParentFsync       materializeStep = "before parent fsync"
	stepBeforeOwnedStageCleanup materializeStep = "before owned staging cleanup"
)

type materializePoint struct {
	step        materializeStep
	parent      string
	stagingName string
	targetName  string
	entryPath   []byte
}

type materializeHook func(materializePoint) error

func changePath(parent, target string) string { return filepath.Join(parent, target) }
