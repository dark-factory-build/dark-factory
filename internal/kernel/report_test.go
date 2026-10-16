package kernel

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
	"github.com/dark-factory-build/dark-factory/internal/review"
)

// report counts a run's tokens by who spent them against the merges in its
// bucket; why names a pull request's head, the reconciler's escalation and
// whom it waits on, and a task without one.
func TestReportAndWhyReadTheReconcilersState(t *testing.T) {
	t.Parallel()
	store, _, project, agent := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	ctx := context.Background()
	task, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, 201), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, 202), Title: "oversee"}, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	admitted, err := store.AdmitNext(ctx, admissionKeys(t, 210, nil), mustTime(t, 5))
	if err != nil || !admitted.Admitted() {
		t.Fatalf("admission = %+v, %v", admitted, err)
	}
	if err := store.AddRunTokens(ctx, admitted.Run.ID, 300); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, 'example/factory', 'pull_request', '9', '', ?, 6)`
	if _, err := store.writer.ExecContext(ctx, insert, project.ID.Bytes(), `{"number":9,"state":"merged","head":"`+strings.Repeat("c", 40)+`","merged_at":"1970-01-01T00:00:01Z"}`); err != nil {
		t.Fatal(err)
	}
	store.opened = time.Date(2026, 10, 15, 5, 30, 59, 0, time.FixedZone("", 3600))
	lines, err := store.Report(ctx, &project.ID, 0, 3600000)
	want := []string{
		"1970-01-01T00:00Z runs=1 failed=0% tokens=300 worker=0 overseer=300 specialist=0 merges=1 tokens/merge=300",
		"top task tokens=300 " + task.ID.String() + ` running "oversee"`,
		"lessons=0 reads=0 citations=0 stale=0",
		"factoryd since=2026-10-15T04:30Z source=" + buildinfo.Current().Source(),
	}
	if err != nil || len(lines) != len(want)+1 || !slices.Equal(lines[:len(want)], want) || !strings.HasPrefix(lines[len(want)], "wal_bytes=") {
		t.Fatalf("report = %q, %v", lines, err)
	}
	head := strings.Repeat("a", 40)
	escalate(t, store, project.ID, "7", "open", head, head, 7)
	lines, err = store.Why(ctx, &project.ID, 7, TaskID{})
	if err != nil || len(lines) != 8 || lines[1] != "head: "+head || !strings.Contains(lines[6], "escalated: stuck 7") || lines[7] != "waits on: operator: factoryd escalated it to the overseer" {
		t.Fatalf("why pull = %q, %v", lines, err)
	}
	lines, err = store.Why(ctx, &project.ID, 0, task.ID)
	if err != nil || len(lines) != 4 || !strings.HasPrefix(lines[1], "run: admitted  agent="+agent.ID.String()+" at ") || lines[2] != "pull: none published" || lines[3] != "waits on: worker: its task is running" {
		t.Fatalf("why task = %q, %v", lines, err)
	}
	if lines, err = store.Why(ctx, nil, 7, TaskID{}); err != nil || len(lines) != 8 || lines[0] != "pull: example/factory#7 open \"\"" {
		t.Fatalf("singleton why pull = %q, %v", lines, err)
	}
	if _, err := store.Why(ctx, &project.ID, 8, TaskID{}); err != ErrNotFound {
		t.Fatalf("why of an unknown pull = %v", err)
	}
	// An escalation names its own repository's pull request, not a newer one
	// with the same number elsewhere in the project.
	other := strings.Repeat("d", 40)
	if _, err := store.writer.ExecContext(ctx, `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, 'example/other', 'pull_request', '7', '', ?, 99)`, project.ID.Bytes(), `{"number":7,"state":"open","head":"`+other+`"}`); err != nil {
		t.Fatal(err)
	}
	read, err := store.beginRead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	line := escalationWhy(ctx, read.connection, project.ID, "[reviewer:op7]")
	read.Close()
	if !strings.Contains(line, "pull: example/factory#7") || !strings.Contains(line, "head: "+head) {
		t.Fatalf("escalation why = %q", line)
	}
	// Only required checks count, as in the reconciler: an optional failure or
	// pending check predicts nothing.
	for id, check := range map[string]string{"c1": `"name":"go","state":"completed","conclusion":"success","required":true`, "c2": `"name":"lint","state":"completed","conclusion":"failure"`, "c3": `"name":"docs","state":"in_progress","conclusion":""`} {
		if _, err := store.writer.ExecContext(ctx, `INSERT INTO production_records (project_id, repository, kind, identity, visual_id, document, observed_at_ms) VALUES (?, 'example/factory', 'check', ?, '', ?, 99)`, project.ID.Bytes(), id, `{`+check+`,"revision":"`+head+`","scope":"head","pull_requests":[7]}`); err != nil {
			t.Fatal(err)
		}
	}
	if read, err = store.beginRead(ctx); err != nil {
		t.Fatal(err)
	}
	line = escalationWhy(ctx, read.connection, project.ID, "[reviewer:op7]")
	read.Close()
	if !strings.Contains(line, "; required checks: 1 passed, 0 pending, 0 failed;") {
		t.Fatalf("why checks = %q", line)
	}
	// A failing check outranks a pending one and a missing verdict, as in review.Decide.
	if waits := whyPullWaits(ProductionPullRequest{State: "open"}, false, Task{}, "", nil, 1, 1); waits != "factoryd: failing checks send it back on its next pass" {
		t.Fatalf("failing and pending checks wait on %q", waits)
	}
	// An allowed, green, unqueued head whose task was cancelled: the reconciler
	// leaves it, so it waits on no factoryd pass.
	if waits := whyPullWaits(ProductionPullRequest{State: "open"}, true, Task{Status: TaskCancelled}, "", &review.Operation{State: "enqueued", Verdict: "allow"}, 0, 0); waits != "operator: its task was cancelled, so factoryd no longer advances it" {
		t.Fatalf("a cancelled task's pull waits on %q", waits)
	}
	if waits := whyPullWaits(ProductionPullRequest{State: "open"}, false, Task{}, "", &review.Operation{State: "ejected", Failures: review.FailuresBeforeEscalation, LastAction: "could not enqueue it (attempt 6): conflict"}, 0, 0); waits != "factoryd: gave up; last action: could not enqueue it (attempt 6): conflict" {
		t.Fatalf("an ejected operation waits on %q", waits)
	}
	if waits := whyPullWaits(ProductionPullRequest{State: "open"}, false, Task{}, "", &review.Operation{State: "ejected", Detail: "conflicts with main"}, 0, 0); waits != "factoryd: its next pass acts (every 5 minutes)" {
		t.Fatalf("an actionable ejected operation waits on %q", waits)
	}
}

func TestWhyRendersStoredCheckAnnotationsAndReviewFindings(t *testing.T) {
	t.Parallel()
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	ctx := context.Background()
	head := strings.Repeat("e", 40)
	if err := store.RecordProductionObservation(ctx, project.ID, ProductionObservation{
		Repository: "example/factory",
		ObservedAt: 10,
		PullRequests: []ProductionPullRequest{{
			Number: 7, Title: "Exact head", URL: "https://github.com/example/factory/pull/7", Head: head, BaseSHA: strings.Repeat("f", 40),
			Branch: "feature/exact", Base: "main", State: "open", Review: ProductionReview{Head: head, URL: "https://github.com/example/factory/pull/7#review", State: "block", Findings: "review found an exact-head regression"},
		}},
		Checks: []ProductionCheck{{
			ID: "ci:7", Name: "ci", Revision: head, Scope: "head", State: "completed", Conclusion: "failure", Required: true,
			PullRequests: []uint64{7}, Jobs: []ProductionJob{{Name: "go", State: "completed", Conclusion: "failure", Annotations: []string{"--- FAIL: TestExactHead (1s)"}}},
		}},
	}, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	lines, err := store.Why(ctx, &project.ID, 7, TaskID{})
	joined := strings.Join(lines, "\n")
	if err != nil || !strings.Contains(joined, "check failure details:") || !strings.Contains(joined, "TestExactHead") || !strings.Contains(joined, "review findings: review found an exact-head regression") {
		t.Fatalf("why evidence = %q, %v", lines, err)
	}
}

func TestStatusIsBoundedAndCountsHistory(t *testing.T) {
	store, _, project, agent := newAdmissionStore(t, RoleWorker, 1)
	defer store.Close()
	ctx := context.Background()
	for index := 0; index < StatusRecentLimit+3; index++ {
		if _, err := store.EnqueueTask(ctx, NewTask{ID: taskID(t, byte(240+index)), ProjectID: project.ID, AssignedAgentID: agent.ID, IncarnationID: incarnationID(t, byte(280+index)), Title: "task"}, mustTime(t, int64(index+1))); err != nil {
			t.Fatal(err)
		}
	}
	status, err := store.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Projects != 1 || status.Agents != 1 || status.Tasks != StatusRecentLimit+3 || status.Accounts != 0 || len(status.RecentTasks) != StatusRecentLimit {
		t.Fatalf("status = %+v", status)
	}
}

func TestReportIncludesFailedMergeGroupCheck(t *testing.T) {
	t.Parallel()
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	check := ProductionCheck{ID: "merge-group:12", Name: "merge_group", Revision: strings.Repeat("a", 40), Scope: "merge_group", State: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/12", PullRequests: []uint64{7}, Jobs: []ProductionJob{{Name: "go", State: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/12", Annotations: []string{"--- FAIL: TestQueue"}}}}
	if err := store.RecordProductionObservation(context.Background(), project.ID, ProductionObservation{Repository: "example/factory", ObservedAt: 10, Checks: []ProductionCheck{check}}, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	lines, err := store.Report(context.Background(), &project.ID, 0, 3600000)
	if err != nil || len(lines) != 4 || !strings.Contains(lines[0], "failure new x1") || !strings.Contains(lines[0], "TestQueue") {
		t.Fatalf("report = %q, %v", lines, err)
	}
}

// Every release updated in the window reports its state, phase and reason: a
// build that failed before serving, a rolled-back trial, and one still
// running. A served one reports its local API outage, unknown when its staging
// generation recorded no withdrawal (an older build, or one that died).
func TestReportNamesEachReleaseOutcome(t *testing.T) {
	t.Parallel()
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	for _, release := range []ProductionDelivery{
		{Revision: strings.Repeat("a", 40), State: "verified", WithdrawnAt: 1_000, ServedAt: 2_000},
		{Revision: strings.Repeat("b", 40), State: "verified", WithdrawnAt: 10_000, ServedAt: 22_500},
		{Revision: strings.Repeat("c", 40), State: "failed", Phase: "trial", Reason: "the new build was not promoted (answered false): it exited", WithdrawnAt: 30_000, ServedAt: 40_000},
		{Revision: strings.Repeat("d", 40), State: "running", Phase: "trial", WithdrawnAt: 50_000},
		{Revision: strings.Repeat("e", 40), State: "failed", Phase: "build", Reason: "go build: exit status 1\n" + strings.Repeat("x", 300), UpdatedAt: 60_000},
	} {
		release.ID, release.Kind, release.Destination, release.PullRequests = "release:"+release.Revision, "runtime", "factoryd", []uint64{}
		if err := store.RecordDelivery(context.Background(), project.ID, "example/factory", release, mustTime(t, max(release.ServedAt, release.WithdrawnAt, release.UpdatedAt))); err != nil {
			t.Fatal(err)
		}
	}
	lines, err := store.Report(context.Background(), &project.ID, 5_000, 1)
	var restarts []string
	for _, line := range lines {
		if strings.HasPrefix(line, "restart ") {
			restarts = append(restarts, line)
		}
	}
	want := []string{
		"restart release:bbbbbbbbbbbb verified phase=- served=1970-01-01T00:00Z outage=12.5s reason=-",
		"restart release:cccccccccccc failed phase=trial served=1970-01-01T00:00Z outage=10s reason=the new build was not promoted (answered false): it exited",
		"restart release:dddddddddddd running phase=trial reason=-",
		"restart release:eeeeeeeeeeee failed phase=build reason=go build: exit status 1 " + strings.Repeat("x", 136),
	}
	if err != nil || !slices.Equal(restarts, want) {
		t.Fatalf("restarts = %q, %v", restarts, err)
	}
}

func TestFailureLedgerKeepsDistinctMergeGroupTestsDistinct(t *testing.T) {
	t.Parallel()
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	for id, test := range map[string]string{"12": "TestQueue", "13": "TestOther"} {
		check := ProductionCheck{ID: "merge-group:" + id, Name: "merge_group", Revision: strings.Repeat(id[:1], 40), Scope: "merge_group", State: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/" + id, PullRequests: []uint64{7}, Jobs: []ProductionJob{{Name: "go", State: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/" + id, Annotations: []string{"--- FAIL: " + test}}}}
		if err := store.RecordProductionObservation(context.Background(), project.ID, ProductionObservation{Repository: "example/factory", ObservedAt: 10, Checks: []ProductionCheck{check}}, mustTime(t, 10)); err != nil {
			t.Fatal(err)
		}
	}
	lines, err := store.Report(context.Background(), &project.ID, 0, 3600000)
	joined := strings.Join(lines, "\n")
	if err != nil || strings.Count(joined, "failure new x1") != 2 || !strings.Contains(joined, "test testqueue") || !strings.Contains(joined, "test testother") {
		t.Fatalf("report = %q, %v", lines, err)
	}
}

func TestReportKeepsDistinctNodeFailuresDistinct(t *testing.T) {
	t.Parallel()
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	revision := strings.Repeat("a", 40)
	checks := []ProductionCheck{
		{ID: "node:1", Name: "main", Revision: revision, Scope: "main", State: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/1", Jobs: []ProductionJob{{Name: "node", State: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/1", Annotations: []string{"Failing step: Run tests: ✖ opening a question"}}}},
		{ID: "node:2", Name: "main", Revision: revision, Scope: "main", State: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/2", Jobs: []ProductionJob{{Name: "node", State: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/2", Annotations: []string{"Failing step: Run tests: ✖ opening a settings"}}}},
	}
	if err := store.RecordProductionObservation(context.Background(), project.ID, ProductionObservation{Repository: "example/factory", ObservedAt: 10, Checks: checks}, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	lines, err := store.Report(context.Background(), &project.ID, 0, 3600000)
	joined := strings.Join(lines, "\n")
	if err != nil || strings.Count(joined, "failure new x1") != 2 || !strings.Contains(joined, "test opening a question") || !strings.Contains(joined, "test opening a settings") {
		t.Fatalf("report = %q, %v", lines, err)
	}
}

func TestReportIncludesMainCheckWithoutPullRequest(t *testing.T) {
	t.Parallel()
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	check := ProductionCheck{ID: "main:13", Name: "main", Revision: strings.Repeat("a", 40), Scope: "main", State: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/13"}
	if err := store.RecordProductionObservation(context.Background(), project.ID, ProductionObservation{Repository: "example/factory", ObservedAt: 10, Checks: []ProductionCheck{check}}, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	lines, err := store.Report(context.Background(), &project.ID, 0, 3600000)
	if err != nil || len(lines) != 4 || !strings.Contains(lines[0], "failure new x1") || !strings.Contains(lines[0], "example/factory") || !strings.Contains(lines[0], check.URL) || !strings.Contains(lines[0], "main at aaaaaaaa") {
		t.Fatalf("report = %q, %v", lines, err)
	}
}

func TestReportIncludesCheckRepositoryAndRunURL(t *testing.T) {
	t.Parallel()
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	check := ProductionCheck{ID: "main:127", Name: "main", Revision: strings.Repeat("b", 40), Scope: "main", State: "completed", Conclusion: "failure", URL: "https://github.com/dark-factory-build/dark-factory-site/actions/runs/38629320865"}
	if err := store.RecordProductionObservation(context.Background(), project.ID, ProductionObservation{Repository: "dark-factory-build/dark-factory-site", ObservedAt: 10, Checks: []ProductionCheck{check}}, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	lines, err := store.Report(context.Background(), &project.ID, 0, 3600000)
	if err != nil || len(lines) != 4 || !strings.Contains(strings.Join(lines, "\n"), "dark-factory-build/dark-factory-site") || !strings.Contains(strings.Join(lines, "\n"), check.URL) {
		t.Fatalf("report = %q, %v", lines, err)
	}
}

func TestHeadAndMainChecksWithSameRevisionRemainDistinct(t *testing.T) {
	t.Parallel()
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	revision := strings.Repeat("a", 40)
	checks := []ProductionCheck{
		{ID: "head:13", Name: "build", Revision: revision, Scope: "head", State: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/13", PullRequests: []uint64{7}},
		{ID: "main:13", Name: "build", Revision: revision, Scope: "main", State: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/14"},
	}
	if err := store.RecordProductionObservation(context.Background(), project.ID, ProductionObservation{Repository: "example/factory", ObservedAt: 10, Checks: checks}, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	page, err := store.Production(context.Background(), project.ID, 0, 8, UnixMillis{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, record := range page.Records {
		if record.Kind == "check" {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("stored checks = %d, records = %+v", count, page.Records)
	}
}

func TestRepeatedCompletedCheckIsNotNewAgain(t *testing.T) {
	t.Parallel()
	store, _, project, _ := newAdmissionStore(t, RoleOrchestrator, 1)
	defer store.Close()
	check := ProductionCheck{ID: "main:13", Name: "main", Revision: strings.Repeat("a", 40), Scope: "main", State: "completed", Conclusion: "failure", URL: "https://github.com/o/r/actions/runs/13"}
	observation := ProductionObservation{Repository: "example/factory", ObservedAt: 10, Checks: []ProductionCheck{check}}
	if err := store.RecordProductionObservation(context.Background(), project.ID, observation, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	observation.ObservedAt = 20
	if err := store.RecordProductionObservation(context.Background(), project.ID, observation, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	lines, err := store.Report(context.Background(), &project.ID, 15, 1)
	if err != nil || strings.Contains(strings.Join(lines, "\n"), "failure ") {
		t.Fatalf("repeated failure report = %q, %v", lines, err)
	}
}

func TestReportLessonCountersUseLatestRevision(t *testing.T) {
	t.Parallel()
	// The lesson counters are new report behavior; this guards their stated
	// latest-revision contract rather than asserting a pre-existing projection.
	store, run, keys := runningWorkerRun(t)
	defer store.Close()
	ctx := context.Background()
	spec := contentSpec(t, run.ProjectID, 221, "lesson")
	spec.Kind = ContentLesson
	spec.Author = "operator:local"
	spec.SourceReferences = `{"status":"needs_revalidation","evidence":["old source"],"verification":"go test ./internal/kernel"}`
	lesson, err := store.CreateContent(ctx, spec, mustTime(t, 10))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordContentAccessForAttempt(ctx, keys.AttemptDigest, ContentAccess{ContentID: lesson.ID, ContentRevision: lesson.Revision, Kind: "read", ByteLength: 1, CreatedAt: mustTime(t, 10)}); err != nil {
		t.Fatal(err)
	}
	spec.Title = "revalidated"
	spec.SourceReferences = `{"status":"current","evidence":["new source"],"verification":"go test ./internal/kernel"}`
	if _, err := store.ReviseContent(ctx, lesson.Revision, spec, mustTime(t, 11)); err != nil {
		t.Fatal(err)
	}
	lines, err := store.Report(ctx, &run.ProjectID, 0, 3600000)
	if err != nil || len(lines) != 4 || lines[1] != "lessons=1 reads=0 citations=1 stale=0" || !strings.HasPrefix(lines[2], "factoryd since=") || !strings.Contains(lines[2], " source=") || !strings.HasPrefix(lines[3], "wal_bytes=") {
		t.Fatalf("latest lesson report = %q, %v", lines, err)
	}
}

// why names the agent whose run a provider exit ended, beside its status, time
// and reason: every specialist wake shares the title "Standing instruction".
func TestWhyNamesTheAgentOfAProviderExitRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, run, keys := admittedOrchestratorRun(t)
	defer store.Close()
	run, runtimeIdentity, runnerIdentity := activateStartedRunner(t, store, run, 120)
	resources := resourcesForRunTest(t, store, run.ID)
	process, group := resourceOfKind(t, resources, ResourceProviderProcess), resourceOfKind(t, resources, ResourceProviderGroup)
	providerIdentity := processIdentity(t, 1220)
	if _, _, err := store.ActivateProviderResources(ctx, run.ID, process.ID, process.Revision, group.ID, group.Revision, providerIdentity, mustTime(t, 23)); err != nil {
		t.Fatal(err)
	}
	session := terminalSessionForRunTest(t, store, run.ID)
	running, err := store.ActivateRun(ctx, run.ID, session.ID, run.Revision, session.Revision, mustTime(t, 24))
	if err != nil {
		t.Fatal(err)
	}
	exit, _ := NewAttemptResultExitCode(1)
	result, _ := NewInnerConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, runtimeIdentity, providerIdentity, exit)
	consumed, err := store.ConsumeAttemptResult(ctx, result, running.Revision, mustTime(t, 25))
	if err != nil {
		t.Fatal(err)
	}
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	afterRunner, _, err := store.RecordRecoveredRunnerAbsence(ctx, run.ID, runner.ID, consumed.Revision, runner.Revision, runnerIdentity, mustTime(t, 26))
	if err != nil {
		t.Fatal(err)
	}
	closed, _, err := store.CloseTerminalAfterRunner(ctx, result, afterRunner.Revision, terminalSessionForRunTest(t, store, run.ID).Revision, mustTime(t, 27))
	if err != nil {
		t.Fatal(err)
	}
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	if _, err := store.ReleaseResource(ctx, run.ID, runtime.ID, runtime.Revision, runtime.Identity, mustTime(t, 28)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeRun(ctx, run.ID, closed.Revision, mustTime(t, 70)); err != nil {
		t.Fatal(err)
	}
	lines, err := store.Why(ctx, nil, 0, run.TaskID)
	if err != nil || len(lines) < 2 || lines[1] != "run: terminal failed agent="+run.AgentID.String()+" at "+time.UnixMilli(mustTime(t, 70).Int64()).UTC().Format(time.RFC3339)+" provider exited before an attempt outcome" {
		t.Fatalf("why provider exit = %q, %v", lines, err)
	}
}
