package daemon

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/ncruces/go-sqlite3"
)

const schedulerPollInterval = time.Second

type schedulerEventKind uint8

const (
	schedulerAdmission schedulerEventKind = iota + 1
	schedulerDone
)

type schedulerEvent struct {
	kind     schedulerEventKind
	id       uint64
	admitted bool
	run      kernel.Run
	err      error
}

type scheduledOwner struct {
	observed bool
	admitted bool
}

// RunScheduler is the one process-owned admission coordinator. It owns and
// joins every synchronous RunNext call it starts. SQLite still chooses the
// exact runnable work and enforces capacity inside AdmitNext; wakeups and the
// single unobserved probe are only bounded scheduling hints.
func (daemon *Daemon) RunScheduler(ctx context.Context, spec SupervisorSpec) error {
	if daemon == nil || daemon.store == nil || ctx == nil {
		return fmt.Errorf("%w: invalid scheduler", kernel.ErrInvalidValue)
	}
	if err := daemon.beginScheduler(); err != nil {
		return err
	}
	defer daemon.endScheduler()

	ownedCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runAttempt := spec.scheduledAttempt
	if runAttempt == nil {
		runAttempt = daemon.RunNext
	}
	validateCompletion := spec.scheduledCompletion
	if validateCompletion == nil {
		validateCompletion = func(observed kernel.Run) error {
			return daemon.validateScheduledCompletion(spec.ChangeParent, spec.UnsettledCompletion, observed)
		}
	}
	events := make(chan schedulerEvent, (kernel.MaxFactoryCapacity+1)*2)
	owners := make(map[uint64]*scheduledOwner, kernel.MaxFactoryCapacity+1)
	var nextID, probeID uint64
	stopping := false
	var resultErr error
	// Dispatch is an advisory scheduling gate; admission still validates the
	// complete durable graph atomically when dispatch resumes.
	dispatchEnabled := func() bool {
		factory, err := daemon.store.Factory(ownedCtx)
		if err != nil {
			if cancellation := ownedCtx.Err(); cancellation == nil || !schedulerOnlyCancellation(err, cancellation) {
				resultErr = errors.Join(resultErr, err)
			}
			stopping = true
			cancel()
			return false
		}
		return factory.DispatchEnabled
	}

	startProbe := func() {
		if !dispatchEnabled() {
			return
		}
		nextID++
		id := nextID
		probeID = id
		owners[id] = &scheduledOwner{}
		attemptSpec := spec
		attemptSpec.scheduledAttempt = nil
		attemptSpec.scheduledCompletion = nil
		attemptSpec.schedulerPoll = nil
		go func() {
			observations := 0
			attemptSpec.admissionObserved = func(admitted bool) {
				observations++
				events <- schedulerEvent{kind: schedulerAdmission, id: id, admitted: admitted, err: duplicateAdmissionObservation(observations)}
			}
			run, err := runAttempt(ownedCtx, attemptSpec)
			events <- schedulerEvent{kind: schedulerDone, id: id, run: run, err: err}
		}()
	}

	pollEvents := spec.schedulerPoll
	var poll *time.Ticker
	if pollEvents == nil {
		poll = time.NewTicker(schedulerPollInterval)
		pollEvents = poll.C
		defer poll.Stop()
	}
	ctxDone := ctx.Done()
	if err := ownedCtx.Err(); err == nil {
		startProbe()
	} else {
		stopping = true
	}

	for !stopping || len(owners) != 0 {
		select {
		case <-ctxDone:
			if !stopping {
				stopping = true
				cancel()
			}
			ctxDone = nil
		case <-daemon.schedulerWake:
			if !stopping && resultErr == nil && probeID == 0 {
				startProbe()
			}
		case <-pollEvents:
			if !stopping && resultErr == nil {
				// Worker idle rules and event-driven overseer wakeups are enqueued
				// on enabled ticks ahead of the probe that admits them. Pausing
				// defers automatic tasks without consuming their causal events. A round that
				// fails is retried next tick; the admission probe stays exact.
				if err := daemon.enforceRunLimits(ownedCtx); err != nil {
					// Cancellation can interrupt the read before the Done arm runs.
					// Preserve unrelated failures even when shutdown races with them.
					if cancellation := ownedCtx.Err(); cancellation == nil || !schedulerOnlyCancellation(err, cancellation) {
						resultErr = err
					}
					stopping = true
					cancel()
				} else if at, err := daemon.timestamp(); err == nil && dispatchEnabled() {
					_, _ = daemon.store.EnqueueIdleInstructions(ownedCtx, at)
					_, _ = daemon.store.EnqueueOverseerWakeups(ownedCtx, at)
					if _, promoteErr := daemon.store.PromoteQueuedContinuations(ownedCtx, at); promoteErr != nil && !errors.Is(promoteErr, context.Canceled) {
						resultErr = promoteErr
						stopping = true
						cancel()
					}
				}
			}
			if !stopping && resultErr == nil && probeID == 0 {
				startProbe()
			}
		case event := <-events:
			owner := owners[event.id]
			if owner == nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("%w: unknown scheduler owner", kernel.ErrCorruptState))
				if !stopping {
					stopping = true
					cancel()
				}
				continue
			}
			switch event.kind {
			case schedulerAdmission:
				if owner.observed || event.err != nil {
					resultErr = errors.Join(resultErr, event.err, fmt.Errorf("%w: duplicate admission observation", kernel.ErrCorruptState))
					if !stopping {
						stopping = true
						cancel()
					}
					continue
				}
				owner.observed = true
				owner.admitted = event.admitted
				if probeID == event.id {
					probeID = 0
				}
				if event.admitted && !stopping && resultErr == nil {
					startProbe()
				}
			case schedulerDone:
				admittedOwner := owner.observed && owner.admitted
				delete(owners, event.id)
				if probeID == event.id {
					probeID = 0
				}
				if !owner.observed {
					// Completion can beat the cancellation select arm. The context,
					// not which event was selected first, determines cancellation.
					if cancellation := ownedCtx.Err(); cancellation == nil || !schedulerOnlyCancellation(event.err, cancellation) {
						resultErr = errors.Join(resultErr, event.err, fmt.Errorf("%w: attempt ended before admission was observed", kernel.ErrCorruptState))
					}
				} else if owner.admitted {
					if completionErr := validateCompletion(event.run); completionErr != nil {
						resultErr = errors.Join(resultErr, event.err, completionErr)
					}
				} else if event.run.ID != (kernel.RunID{}) || event.err == nil || !errors.Is(event.err, kernel.ErrConflict) {
					resultErr = errors.Join(resultErr, event.err, fmt.Errorf("%w: invalid no-admission completion", kernel.ErrCorruptState))
				}
				if resultErr != nil && !stopping {
					stopping = true
					cancel()
				} else if admittedOwner && !stopping && probeID == 0 {
					startProbe()
				}
			default:
				resultErr = errors.Join(resultErr, fmt.Errorf("%w: invalid scheduler event", kernel.ErrCorruptState))
				if !stopping {
					stopping = true
					cancel()
				}
			}
		}
	}
	return resultErr
}

// A joined reconciliation failure must survive even when another leaf is the
// shutdown cancellation. Outcome-unknown also requires recovery, not silence.
func schedulerOnlyCancellation(err, cancellation error) bool {
	var unknown *kernel.OutcomeUnknownError
	if err == nil || cancellation == nil || errors.As(err, &unknown) {
		return false
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		causes := wrapped.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !schedulerOnlyCancellation(cause, cancellation) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		if cause := wrapped.Unwrap(); cause != nil {
			return schedulerOnlyCancellation(cause, cancellation)
		}
	}
	return err == cancellation || errors.Is(err, sqlite3.INTERRUPT)
}

func duplicateAdmissionObservation(count int) error {
	if count == 1 {
		return nil
	}
	return fmt.Errorf("%w: repeated scheduler observation", kernel.ErrCorruptState)
}

func (daemon *Daemon) validateScheduledCompletion(changeParent string, unsettled func(kernel.RunID, error), observed kernel.Run) error {
	if observed.ID == (kernel.RunID{}) {
		return kernel.NewOutcomeUnknownError(fmt.Errorf("%w: admitted attempt returned no run", kernel.ErrCorruptState))
	}
	readRun := daemon.store.Run
	if daemon.scheduledRun != nil {
		readRun = daemon.scheduledRun
	}
	var current kernel.Run
	var found bool
	var err error
	for attempt := 0; attempt < supervisorReconcileAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), liveAttemptStoreTimeout)
		current, found, err = readRun(ctx, observed.ID)
		cancel()
		if err == nil || found || errors.Is(err, kernel.ErrCorruptState) {
			break
		}
	}
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrCorruptState
		}
		return kernel.NewOutcomeUnknownError(err)
	}
	if current.Phase == kernel.RunFinalizing && current.Proposal != nil {
		settled, settleErr := daemon.settleRun(daemon.cleanupCtx, changeParent, current.ID)
		if settleErr != nil {
			if errors.Is(settleErr, kernel.ErrConflict) {
				if unsettled != nil {
					unsettled(current.ID, settleErr)
				}
				return nil
			}
			return kernel.NewOutcomeUnknownError(fmt.Errorf("daemon: scheduled completion was not settled: %w", settleErr))
		}
		current = settled
	}
	if current.Phase == kernel.RunRunning {
		// A clean protocol-2 shutdown handover leaves the run running and
		// owned by its reparented runner under a fresh grant; the next
		// daemon adopts it. RunNext never returns a running run otherwise —
		// every other exit finalizes — so this is never a stray running
		// completion masquerading as a handover.
		return nil
	}
	if current.Phase != kernel.RunTerminal {
		return kernel.NewOutcomeUnknownError(fmt.Errorf("%w: scheduled run remained %s", kernel.ErrConflict, current.Phase.String()))
	}
	return nil
}

func (daemon *Daemon) notifyScheduler() {
	if daemon == nil || daemon.schedulerWake == nil {
		return
	}
	select {
	case daemon.schedulerWake <- struct{}{}:
	default:
	}
}

func (daemon *Daemon) beginScheduler() error {
	daemon.attemptMu.Lock()
	defer daemon.attemptMu.Unlock()
	daemon.schedulerMu.Lock()
	defer daemon.schedulerMu.Unlock()
	if daemon.closing {
		return ErrTerminalClosed
	}
	if daemon.schedulerRunning {
		return kernel.ErrConflict
	}
	daemon.schedulerRunning = true
	return nil
}

func (daemon *Daemon) endScheduler() {
	daemon.schedulerMu.Lock()
	daemon.schedulerRunning = false
	daemon.schedulerMu.Unlock()
}
