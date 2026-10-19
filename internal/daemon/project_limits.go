package daemon

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

const (
	stalledRunDetail = "stalled: no terminal output or attempt call for 10m; last output: "
	// ownerlessRunAge keeps re-recovery off a run that is still between
	// admission and live-owner registration.
	ownerlessRunAge = 10 * time.Minute
)

// enforceRunLiveness runs on every scheduler poll. It cancels worker runs past
// their limit (measured from admission, so a run stuck before provider startup
// cannot evade it), fails an overseer run past its limit, fails a live attempt that has been quiet for the stall
// budget, and repeats startup recovery for runs that have had no live owner
// and no update for ownerlessRunAge. A failed or cancelled run reaches
// finalizing, which its live owner answers by stopping the provider, as for
// an operator stop. Every edge is CAS-protected: a result or stop that won
// first is left untouched. A run that never started and an overseer past its
// limit are retried once, by their finalization (kernel.FailureTransient).
func (daemon *Daemon) enforceRunLiveness(ctx context.Context, spec SupervisorSpec) error {
	at, err := daemon.timestamp()
	if err != nil {
		return err
	}
	runs, err := daemon.store.OverdueRuns(ctx, at)
	if err != nil {
		return err
	}
	overseerLimit, err := kernel.NewFailureProposal(kernel.FailureTransient, kernel.OverseerRunLimitDetail)
	if err != nil {
		return err
	}
	for _, run := range runs {
		// Cancelling an overseer's task would drop its work (an intake task):
		// at any run limit it fails and is requeued once instead.
		if run.Role == kernel.RoleOrchestrator {
			_, err = daemon.store.FailRun(ctx, run.ID, run.Revision, overseerLimit, at)
		} else {
			_, err = daemon.store.CancelRun(ctx, run.ID, run.Revision, kernel.RunLimitDetail, at)
		}
		if err != nil && !casLost(err) {
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
		floor, _, output := attempt.diagnosticSnapshot()
		code, detail := stalledRunProposal(run.Provider, attempt.neverStarted(), floor, output)
		proposal, err := kernel.NewFailureProposal(code, detail)
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

// Codex reports an exhausted account or a model at capacity as a cell of its
// own screen and then idles at its prompt, so the stall budget ends the run.
// Both notices are read only then, from a Codex run's whole projected tail, and
// only under Codex's own glyph: a task, prompt, source or tool output that
// quotes the words is ordinary text, as is anything a Claude run prints. Codex
// 0.163 draws the glyph and its message in separate cells, so a gap between
// them is allowed.
// ponytail: fixed strings of one provider's TUI; recognise a structured
// provider event instead if Codex ever reports these outside its screen.
var codexUsageLimit = regexp.MustCompile(`■[[:space:]]*(You've hit your` + ` usage limit.{0,1000})`)
var codexModelCapacity = regexp.MustCompile(`⚠[[:space:]]*Selected model` + ` is at capacity`)

// stalledRunProposal classifies a stalled run from its retained terminal
// output, projected whole before the detail is cut: a TUI's last raw bytes are
// mostly escapes, and cutting them first leaves an empty tail. A Codex notice
// is read even from a run that made no attempt call yet, as it can be Codex's
// first and only act.
func stalledRunProposal(provider kernel.Provider, neverStarted bool, floor uint64, output []byte) (kernel.FailureCode, string) {
	if provider == kernel.ProviderCodex {
		text := terminalTextProjection(output, false, len(output))
		if report := codexUsageLimit.FindStringSubmatch(text); report != nil {
			return kernel.FailureProviderExit, "provider usage limit (retry after its reset or select another account): " + report[1]
		}
		if codexModelCapacity.MatchString(text) {
			return kernel.FailureTransient, kernel.ProviderCapacityRunDetail
		}
	}
	if neverStarted {
		return kernel.FailureTransient, kernel.NeverStartedRunDetail
	}
	return kernel.FailureProtocol, stalledRunDetail + terminalTextProjection(output, floor > 0, 512)
}

func casLost(err error) bool {
	return errors.Is(err, kernel.ErrConflict) || errors.Is(err, kernel.ErrRevisionConflict)
}
