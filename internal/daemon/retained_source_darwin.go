//go:build darwin

package daemon

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func (daemon *Daemon) materializeAttemptSource(ctx context.Context, live *liveAttempt, handoff kernel.RetainedChangeHandoff) (string, error) {
	if !live.acquireSourceGate(ctx) {
		return "", ctx.Err()
	}
	defer func() { <-live.sourceGate }()
	live.sourceMu.Lock()
	defer live.sourceMu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if live.sourceRoot == "" || filepath.Base(live.sourceRoot) != "retained-source" {
		return "", errInvalidContract
	}
	if path, ok := live.sourceSnapshots[handoff]; ok {
		return path, nil
	}
	changeState, found, err := daemon.store.Change(ctx, handoff.ChangeID)
	if err != nil {
		return "", err
	}
	parent := daemon.changeParent.Load()
	if parent == nil || *parent == "" || !found || changeState.Phase != kernel.ChangeRetained || changeState.Revision != handoff.ChangeRevision || changeState.Selection == nil || changeState.TreeIdentity == nil {
		return "", errors.Join(kernel.ErrConflict, errInvalidContract)
	}
	format, err := change.NewObjectFormat(changeState.Selection.ObjectFormat().String())
	if err != nil {
		return "", err
	}
	base, err := change.NewObjectID(format, changeState.Selection.Commit().Bytes())
	if err != nil {
		return "", err
	}
	commitment, err := change.ParseCommitment(changeState.Selection.Commitment().Bytes())
	if err != nil {
		return "", err
	}
	tree, err := change.NewStageIdentity(uint64(changeState.TreeIdentity.Device()), uint64(changeState.TreeIdentity.Inode()))
	if err != nil {
		return "", err
	}
	result := changeworker.Result{Format: format, Base: base, Commitment: commitment, EntryCount: uint64(changeState.Selection.EntryCount()), BlobBytes: changeState.Selection.TotalBytes(), Tree: tree}
	path, err := changeworker.MaterializeRetainedSource(ctx, *parent, filepath.Dir(live.sourceRoot), changeworker.RetainedSource{ID: handoff.ChangeID.String(), Result: result})
	if err != nil {
		return "", err
	}
	if path != filepath.Join(live.sourceRoot, handoff.ChangeID.String()) {
		return "", errInvalidContract
	}
	live.sourceSnapshots[handoff] = path
	return path, nil
}
