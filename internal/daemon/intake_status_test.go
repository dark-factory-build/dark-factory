//go:build darwin || linux

package daemon

import (
	"context"
	"database/sql"
	"encoding/hex"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

type intakePollFixture struct {
	*dispatchFixture
	source kernel.IntakeSource
	issue  maintainer.Issue
	lists  atomic.Int64
	now    time.Time
	closed bool
}

// newIntakePollFixture is one enabled trusted-author source whose fake
// backend lists one labelled issue by a trusted author.
func newIntakePollFixture(t *testing.T, pollSeconds uint32) *intakePollFixture {
	fixture := &intakePollFixture{dispatchFixture: newDispatchFixture(t), now: time.UnixMilli(1_000_000)}
	ctx := context.Background()
	project, agent := mustProjectID(t, testID(190)), mustAgentID(t, testID(191))
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "poll", Root: "/intake-poll"}, mustKernelTime(t, 101)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: agent, ProjectID: project, Name: "overseer", Role: kernel.RoleOrchestrator, Provider: kernel.ProviderShell, ToolBudgetLimit: 10}, mustKernelTime(t, 102)); err != nil {
		t.Fatal(err)
	}
	raw, _ := hex.DecodeString(testID(192))
	id, _ := kernel.IntakeSourceIDFromBytes(raw)
	source, err := fixture.store.CreateIntakeSource(ctx, kernel.NewIntakeSource{ID: id, ProjectID: project, TargetRepositoryID: kernel.RepositoryID(project), OverseerAgentID: agent, GitHubRepositoryID: 42, GitHubRepositoryName: "team/issues", LabelFilter: "factory:ready", Policy: kernel.IntakePolicyTrustedAuthors, TrustedGitHubLogins: []string{"owner"}, PollSeconds: pollSeconds, AdmissionLimit: 25}, mustKernelTime(t, 103))
	if err != nil {
		t.Fatal(err)
	}
	if fixture.source, err = fixture.store.SetIntakeSourceEnabled(ctx, id, source.Revision, true, mustKernelTime(t, 104)); err != nil {
		t.Fatal(err)
	}
	fixture.issue = maintainer.Issue{ID: 7, NodeID: "I_poll", Number: 7, Title: "Polled", Body: "Do the polled work", State: "open", Labels: []string{"factory:ready"}, URL: "https://github.com/team/issues/issues/7"}
	fixture.issue.Author.Login, fixture.issue.Author.Type = "owner", "User"
	fixture.attach(fixture.daemon)
	return fixture
}

func (fixture *intakePollFixture) attach(daemon *Daemon) {
	daemon.now = func() time.Time { return fixture.now }
	daemon.intakeIssues = func(_ context.Context, _ string, _ uint64, _ uint32, _ string, number uint64) (maintainer.IssuePage, error) {
		if number == 0 {
			fixture.lists.Add(1)
			if fixture.closed {
				return maintainer.IssuePage{RepositoryID: 42, Issues: []maintainer.Issue{}}, nil
			}
		}
		return maintainer.IssuePage{RepositoryID: 42, Issues: []maintainer.Issue{fixture.issue}}, nil
	}
}

func (fixture *intakePollFixture) tasks(t *testing.T) []kernel.Task {
	t.Helper()
	ctx := context.Background()
	accepted, found, err := fixture.store.LatestIntakeAcceptance(ctx, intakeSnapshot(fixture.source, fixture.issue), fixture.source.ProjectID, fixture.source.TargetRepositoryID)
	if err != nil || !found {
		t.Fatalf("acceptance: found=%v err=%v", found, err)
	}
	tasks, err := fixture.store.IntakeTasksForAcceptance(ctx, accepted.ID)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", "file:"+fixture.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var all int
	if err := database.QueryRow(`SELECT COUNT(*) FROM tasks`).Scan(&all); err != nil || all != len(tasks) {
		t.Fatalf("tasks=%d intake tasks=%d err=%v", all, len(tasks), err)
	}
	return tasks
}

func TestSchedulerPollsDueIntakeSourceWithoutAController(t *testing.T) {
	fixture := newIntakePollFixture(t, 5)
	ctx, cancel := context.WithCancel(context.Background())
	polls := make(chan time.Time, 1)
	done := make(chan error, 1)
	go func() {
		done <- fixture.daemon.RunScheduler(ctx, SupervisorSpec{schedulerPoll: polls, scheduledAttempt: func(ctx context.Context, _ SupervisorSpec) (kernel.Run, error) {
			<-ctx.Done()
			return kernel.Run{}, ctx.Err()
		}})
	}()
	polls <- time.Now()
	deadline := time.Now().Add(5 * time.Second)
	for fixture.lists.Load() == 0 || fixture.daemon.intakeBusy.Load() {
		if time.Now().After(deadline) {
			t.Fatal("scheduler never polled intake")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := waitSchedulerDone(t, done); err != nil {
		t.Fatal(err)
	}
	tasks := fixture.tasks(t)
	if len(tasks) != 1 || tasks[0].Status != kernel.TaskQueued || tasks[0].Body != "Do the polled work" {
		t.Fatalf("imported tasks = %+v", tasks)
	}
	result := fixture.daemon.Intake(context.Background(), api.IntakeInput{Action: "list"})
	if len(result.Sources) != 1 || result.Sources[0].Sync == nil || result.Sources[0].Sync.State != "ok" || result.Sources[0].Sync.ImportedTasks != 1 {
		t.Fatalf("poll status = %+v", result.Sources)
	}
}

func TestIntakePollWaitsForInterval(t *testing.T) {
	fixture := newIntakePollFixture(t, 60)
	ctx := context.Background()
	fixture.daemon.pollIntake(ctx)
	fixture.now = fixture.now.Add(59 * time.Second)
	fixture.daemon.pollIntake(ctx)
	if got := fixture.lists.Load(); got != 1 {
		t.Fatalf("a source not yet due was polled: lists=%d", got)
	}
	fixture.now = fixture.now.Add(time.Second)
	fixture.daemon.pollIntake(ctx)
	if got := fixture.lists.Load(); got != 2 {
		t.Fatalf("a due source was not polled: lists=%d", got)
	}
	if tasks := fixture.tasks(t); len(tasks) != 1 {
		t.Fatalf("repeat poll duplicated work: %+v", tasks)
	}
}

func TestIntakePollRestartRescansWithoutDuplicates(t *testing.T) {
	fixture := newIntakePollFixture(t, 60)
	ctx := context.Background()
	fixture.daemon.pollIntake(ctx)
	first := fixture.tasks(t)
	restarted, err := newDaemon(fixture.store, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	fixture.attach(restarted)
	restarted.pollIntake(ctx)
	if got := fixture.lists.Load(); got != 2 {
		t.Fatalf("restart did not rescan: lists=%d", got)
	}
	if again := fixture.tasks(t); len(again) != 1 || again[0].ID != first[0].ID {
		t.Fatalf("restart duplicated work: before=%+v after=%+v", first, again)
	}
}

func TestIntakePollCancelsWithdrawnQueuedWork(t *testing.T) {
	fixture := newIntakePollFixture(t, 60)
	ctx := context.Background()
	fixture.daemon.pollIntake(ctx)
	tasks := fixture.tasks(t)
	accepted, _, _ := fixture.store.LatestIntakeAcceptance(ctx, intakeSnapshot(fixture.source, fixture.issue), fixture.source.ProjectID, fixture.source.TargetRepositoryID)
	// A durable withdrawal whose own reconciliation never ran, for an issue
	// no longer listed: the poller must still finish it.
	if _, err := fixture.store.WithdrawIntakeAcceptance(ctx, accepted.ID, mustKernelTime(t, 2_000_000)); err != nil {
		t.Fatal(err)
	}
	fixture.closed = true
	fixture.now = fixture.now.Add(time.Minute)
	fixture.daemon.pollIntake(ctx)
	task, _, err := fixture.store.Task(ctx, tasks[0].ID)
	if err != nil || task.Status != kernel.TaskCancelled {
		t.Fatalf("withdrawn work = %+v err=%v", task, err)
	}
}
