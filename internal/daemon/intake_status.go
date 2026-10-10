package daemon

import (
	"context"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// intakePoll is one source's poll progress. It lives only in memory: a
// restart rescans from page 1, and acceptance and import are idempotent.
type intakePoll struct {
	revision kernel.Revision
	page     uint32
	cursor   string
	due      time.Time
	sync     api.IntakeSync
	waiting  []api.IntakeCandidate // this scan's, published to sync when it wraps
}

// tickIntake polls every due intake source beside the scheduler loop, one
// pass at a time.
func (daemon *Daemon) tickIntake(ctx context.Context) {
	if !daemon.intakeBusy.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer daemon.intakeBusy.Store(false)
		daemon.pollIntake(ctx)
	}()
}

func (daemon *Daemon) pollIntake(ctx context.Context) {
	sources, err := daemon.store.IntakeSources(ctx)
	if err != nil {
		return
	}
	for _, source := range sources {
		if ctx.Err() != nil {
			return
		}
		daemon.pollIntakeSource(ctx, source)
	}
}

// intakeReachable is true where the source's backend is configured: Linear,
// or the customer GitHub connection. The package-test seam replaces both.
func (daemon *Daemon) intakeReachable(source kernel.IntakeSource) bool {
	if daemon.intakeIssues != nil {
		return true
	}
	if source.LinearTeamID != "" {
		return daemon.linear != nil
	}
	return daemon.customerMaintainer()
}

func (daemon *Daemon) pollIntakeSource(ctx context.Context, source kernel.IntakeSource) {
	if !daemon.intakeReachable(source) || source.LinearTeamID == "" && daemon.githubQuotaLow() {
		return
	}
	daemon.intakeMu.Lock()
	defer daemon.intakeMu.Unlock()
	if daemon.intakePolls == nil {
		daemon.intakePolls = map[kernel.IntakeSourceID]*intakePoll{}
	}
	poll := daemon.intakePolls[source.ID]
	if poll == nil || poll.revision != source.Revision {
		poll = &intakePoll{revision: source.Revision, page: 1}
		daemon.intakePolls[source.ID] = poll
	}
	now := daemon.now()
	if now.Before(poll.due) {
		return
	}
	interval := time.Duration(source.PollSeconds) * time.Second
	result := daemon.previewIntake(ctx, source, poll.page, true, poll.cursor)
	poll.sync.LastAttemptAt = now.Unix()
	poll.sync.ImportedTasks = uint16(len(result.ImportedTasks))
	if result.State != "ok" && result.State != "paused" {
		// Only confirmed receipt progress advances; the page is kept.
		if result.AcceptanceProgress {
			poll.cursor = result.AcceptanceCursor
		}
		poll.due = now.Add(min(interval, time.Minute))
		poll.sync.State, poll.sync.Error = "error", result.State
		return
	}
	for _, candidate := range result.Candidates {
		// ponytail: the inbox keeps 100 per source; a larger backlog stays in Sources.
		if (candidate.Reason == string(kernel.IntakeNeedsManualAcceptance) || candidate.Reason == string(kernel.IntakeUntrustedAuthor)) && len(poll.waiting) < 100 {
			candidate.Body = ""
			poll.waiting = append(poll.waiting, candidate)
		}
	}
	page := poll.page
	poll.page, poll.cursor = 1, result.AcceptanceCursor
	if result.NextPage != nil && *result.NextPage > page {
		poll.page = *result.NextPage
	}
	poll.due = now.Add(interval)
	if poll.page == 1 {
		poll.sync.Waiting, poll.waiting = poll.waiting, nil
	}
	if poll.page != 1 || poll.cursor != "" {
		// A backlog continues promptly instead of waiting a full interval.
		poll.due = now.Add(5 * time.Second)
	}
	poll.sync.LastSuccessAt, poll.sync.State, poll.sync.Error = now.Unix(), result.State, ""
}

// intakeSync reports the poller's status by source; callers hold intakeMu.
func (daemon *Daemon) intakeSync() map[string]api.IntakeSync {
	sync := make(map[string]api.IntakeSync, len(daemon.intakePolls))
	for id, poll := range daemon.intakePolls {
		if poll.sync.LastAttemptAt != 0 {
			sync[id.String()] = poll.sync
		}
	}
	return sync
}
