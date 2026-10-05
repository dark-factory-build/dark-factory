package daemon

import (
	"context"
	"errors"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

const (
	runLimitDetail   = "Run time limit reached"
	stalledRunDetail = "stalled: no terminal output or attempt call for 10m; last output: "
	// ownerlessRunAge keeps re-recovery off a run that is still between
	// admission and live-owner registration.
	ownerlessRunAge = 10 * time.Minute
)

// enforceRunLiveness runs on every scheduler poll. It cancels runs past their
// limit (measured from admission, so a run stuck before provider startup
// cannot evade it), fails a live attempt that has been quiet for the stall
// budget, and repeats startup recovery for runs that have had no live owner
// and no update for ownerlessRunAge. A failed or cancelled run reaches
// finalizing, which its live owner answers by stopping the provider, as for
// an operator stop. Every edge is CAS-protected: a result or stop that won
// first is left untouched. Only a run that never started is retried, by
// its finalization (kernel.NeverStartedRunDetail).
func (daemon *Daemon) enforceRunLiveness(ctx context.Context, spec SupervisorSpec) error {
	at, err := daemon.timestamp()
	if err != nil {
		return err
	}
	runs, err := daemon.store.OverdueRuns(ctx, at)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if _, err := daemon.store.CancelRun(ctx, run.ID, run.Revision, runLimitDetail, at); err != nil && !casLost(err) {
			return err
		}
	}
	now := daemon.livenessTimestamp()
	daemon.attemptMu.Lock()
	var stalled []*liveAttempt
	for _, attempt := range daemon.attempts {
		if attempt.stalled(now) {
			stalled = append(stalled, attempt)
		}
	}
	daemon.attemptMu.Unlock()
	for _, attempt := range stalled {
		run, found, err := daemon.store.Run(ctx, attempt.runID)
		if err != nil {
			return err
		}
		if !found || run.Phase != kernel.RunRunning {
			continue
		}
		_, _, output := attempt.diagnosticSnapshot()
		detail := kernel.NeverStartedRunDetail
		if len(output) > 0 || !attempt.neverStarted() {
			dropped := len(output) > 512
			if dropped {
				output = output[len(output)-512:]
			}
			detail = stalledRunDetail + terminalTextProjection(output, dropped, 512)
		}
		proposal, err := kernel.NewFailureProposal(kernel.FailureProtocol, detail)
		if err != nil {
			return err
		}
		if _, err := daemon.store.FailRun(ctx, run.ID, run.Revision, proposal, at); err != nil && !casLost(err) {
			return err
		}
		attempt.notify()
	}
	if spec.RuntimeParent == nil || spec.ChangeParent == "" {
		return nil
	}
	return daemon.recoverOwnerlessRuns(ctx, spec.RuntimeParent, spec.ChangeParent, at.Int64()-ownerlessRunAge.Milliseconds())
}

func casLost(err error) bool {
	return errors.Is(err, kernel.ErrConflict) || errors.Is(err, kernel.ErrRevisionConflict)
}
