package daemon

import (
	"context"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// RunIntake owns source polling for the daemon lifetime. Operator calls still
// use the same Intake path; this loop only supplies the durable source tick
// that used to be driven by a host launchd controller.
func (daemon *Daemon) RunIntake(ctx context.Context) error {
	if daemon == nil || daemon.store == nil || ctx == nil {
		return kernel.ErrInvalidValue
	}
	for {
		if err := daemon.tickIntake(ctx); err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (daemon *Daemon) tickIntake(ctx context.Context) error {
	sources, err := daemon.store.IntakeSources(ctx)
	if err != nil {
		return err
	}
	now := daemon.now()
	for _, source := range sources {
		if !source.Enabled {
			continue
		}
		daemon.intakeTickMu.Lock()
		state := daemon.intakeTicks[source.ID]
		due := state.next.IsZero() || !now.Before(state.next)
		if due {
			if state.page == 0 {
				state.page = 1
			}
			state.next = now.Add(time.Duration(source.PollSeconds) * time.Second)
			daemon.intakeTicks[source.ID] = state
		}
		daemon.intakeTickMu.Unlock()
		if !due {
			continue
		}

		// The existing intake mutex fences a remote tick against configuration,
		// accept, withdraw, and import operations.
		daemon.intakeMu.Lock()
		result := daemon.previewIntake(ctx, source, state.page, true, state.acceptanceCursor)
		daemon.intakeMu.Unlock()
		if result.State != "ok" {
			continue
		}
		daemon.intakeTickMu.Lock()
		state = daemon.intakeTicks[source.ID]
		if result.NextPage == nil {
			state.page = 1
		} else {
			state.page = *result.NextPage
		}
		state.acceptanceCursor = result.AcceptanceCursor
		daemon.intakeTicks[source.ID] = state
		daemon.intakeTickMu.Unlock()
	}
	return nil
}
