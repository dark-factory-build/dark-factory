package daemon

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/review"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

// #2079: a worker task blocked as "missing prerequisite" is queued again,
// through the operator retry, by the first merge pass after every declared
// prerequisite's published Change is observed merged at its exact head; and
// not while one is sent back, succeeded at an unpublished head, or open. The
// retry is one: a second block of the same kind waits for the overseer. The
// admission table test in the kernel proves the shared predicate itself.
func TestMergePipelineRetriesTheConsumerBlockedOnAMissingPrerequisite(t *testing.T) {
	fixture, _ := newHomeFixtureAt(t, runner.ShortTempDir())
	project := reviewPublicProject(t, fixture)
	ctx := context.Background()
	customerMode(t, fixture)
	// An open pull request is reconciled by the same pass; keep its review out
	// of this test rather than racing a review goroutine with it.
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend {
		return &publicReviewBackend{storedErr: errors.New("no review in this fixture")}
	}
	clock := int64(1000)
	tick := func() kernel.UnixMillis { clock++; return mustKernelTime(t, clock) }
	fixture.daemon.now = func() time.Time { return time.UnixMilli(tick().Int64()) }
	must := func(step string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}
	factory, err := fixture.store.Factory(ctx)
	must("factory", err)
	_, err = fixture.store.SetDispatch(ctx, factory.Revision, true, tick())
	must("enable dispatch", err)
	repository, err := kernel.RepositoryIDFromBytes(mustIDBytes(t, testID(251)))
	must("repository id", err)
	worker, err := fixture.store.CreateAgent(ctx, kernel.NewAgent{ID: mustAgentID(t, testID(100)), ProjectID: project, Name: "worker", Role: kernel.RoleWorker, Provider: kernel.ProviderCodex, ToolBudgetLimit: 2}, tick())
	must("create worker", err)
	enqueue := func(seed byte, title string, prerequisites ...kernel.TaskPrerequisite) kernel.Task {
		t.Helper()
		incarnation, err := kernel.IncarnationIDFromBytes(mustIDBytes(t, testID(seed+1)))
		must("incarnation id", err)
		task, err := fixture.store.EnqueueTask(ctx, kernel.NewTask{ID: mustTaskID(t, testID(seed)), IncarnationID: incarnation, ProjectID: project, RepositoryID: repository, AssignedAgentID: worker.ID, Title: title, Prerequisites: prerequisites}, tick())
		must("enqueue "+title, err)
		return task
	}
	one := mustRevision(t, 1)
	producer := enqueue(110, "producer")
	consumer := enqueue(120, "consumer", kernel.TaskPrerequisite{TaskID: producer.ID, WorkRevision: one})
	commit := func(fill byte) kernel.CommitID {
		t.Helper()
		id, err := kernel.NewCommitID(kernel.ObjectSHA1, bytes.Repeat([]byte{fill}, 20))
		must("commit id", err)
		return id
	}
	base, first, second := commit(0x0b), commit(0x11), commit(0x22)
	current := func(id kernel.TaskID) kernel.Task {
		t.Helper()
		task, found, err := fixture.store.Task(ctx, id)
		if err != nil || !found {
			t.Fatalf("task %s: found=%v err=%v", id, found, err)
		}
		return task
	}
	resource := func(id kernel.ResourceID) kernel.Resource {
		t.Helper()
		value, found, err := fixture.store.Resource(ctx, id)
		if err != nil || !found {
			t.Fatalf("resource %s: found=%v err=%v", id, found, err)
		}
		return value
	}
	// admit tries one admission with identities from seed; the keys are reused
	// by finish, so a refused admission leaves nothing behind.
	admit := func(seed byte) (kernel.AdmissionKeys, kernel.AdmissionResult) {
		t.Helper()
		id := func(offset byte) []byte { return mustIDBytes(t, testID(seed+offset)) }
		keys := kernel.AdmissionKeys{RuntimeRoot: fmt.Sprint("/runtime/prerequisite-", seed)}
		keys.RunID, _ = kernel.RunIDFromBytes(id(0))
		keys.TerminalSessionID, _ = kernel.TerminalSessionIDFromBytes(id(1))
		keys.CandidateChangeID, _ = kernel.ChangeIDFromBytes(id(2))
		keys.Resources.RuntimeRoot, _ = kernel.ResourceIDFromBytes(id(3))
		keys.Resources.RunnerProcess, _ = kernel.ResourceIDFromBytes(id(4))
		keys.Resources.ProviderProcess, _ = kernel.ResourceIDFromBytes(id(5))
		keys.Resources.ProviderGroup, _ = kernel.ResourceIDFromBytes(id(6))
		var err error
		keys.AttemptDigest, err = kernel.AttemptDigestFromBytes(bytes.Repeat([]byte{seed}, kernel.DigestBytes))
		must("attempt digest", err)
		keys.ResultProofDigest, err = kernel.ResultProofDigestFromBytes(bytes.Repeat([]byte{seed + 1}, kernel.DigestBytes))
		must("proof digest", err)
		admission, err := fixture.store.AdmitNext(ctx, keys, tick())
		must(fmt.Sprintf("admission %d", seed), err)
		return keys, admission
	}
	// finish runs the admitted worker run through the supported lifecycle to
	// its proposal and settlement: a Change retained at head, or at its base,
	// as a worker that blocked before committing leaves it.
	finish := func(keys kernel.AdmissionKeys, run kernel.Run, proposal kernel.Proposal, head *kernel.CommitID) kernel.Run {
		t.Helper()
		seed := keys.AttemptDigest.Bytes()[0]
		runtimeIdentity, err := kernel.NewPathResourceIdentity(1, int64(seed))
		must("runtime identity", err)
		birth, err := kernel.BirthDigestFromBytes(bytes.Repeat([]byte{seed + 2}, kernel.DigestBytes))
		must("birth digest", err)
		runnerIdentity, err := kernel.NewProcessResourceIdentity(int64(seed)+1000, int64(seed)+1001, birth)
		must("runner identity", err)
		providerIdentity, err := kernel.NewProcessResourceIdentity(int64(seed)+1002, int64(seed)+1003, birth)
		must("provider identity", err)
		_, err = fixture.store.ActivateResource(ctx, run.ID, keys.Resources.RuntimeRoot, one, runtimeIdentity, tick())
		must("activate runtime", err)
		startAndActivateRunner(t, fixture.store, run.ID, keys.Resources.RunnerProcess, runnerIdentity, tick())
		process, group := resource(keys.Resources.ProviderProcess), resource(keys.Resources.ProviderGroup)
		_, _, err = fixture.store.ActivateProviderResources(ctx, run.ID, process.ID, process.Revision, group.ID, group.Revision, providerIdentity, tick())
		must("activate provider", err)
		change, found, err := fixture.store.Change(ctx, *run.ChangeID)
		if err != nil || !found {
			t.Fatalf("change: found=%v err=%v", found, err)
		}
		if change.Phase == kernel.ChangeReserved {
			identity, err := kernel.NewFileIdentity(1, 2)
			must("repository identity", err)
			selection, err := kernel.NewChangeSelection(kernel.ObjectSHA1, base, identity)
			must("selection", err)
			change, err = fixture.store.RecordChangePrepared(ctx, change.ID, change.Revision, selection, tick())
			must("prepare change", err)
			change, err = fixture.store.MarkChangeAvailable(ctx, change.ID, change.Revision, base, tick())
			must("change available", err)
		}
		session, found, err := fixture.store.TerminalSessionForRun(ctx, run.ID)
		if err != nil || !found {
			t.Fatalf("terminal session: found=%v err=%v", found, err)
		}
		live, found, err := fixture.store.Run(ctx, run.ID)
		if err != nil || !found {
			t.Fatalf("run: found=%v err=%v", found, err)
		}
		live, err = fixture.store.ActivateRun(ctx, run.ID, session.ID, live.Revision, session.Revision, tick())
		must("activate run", err)
		live, err = fixture.store.ProposeAttemptOutcome(ctx, keys.AttemptDigest, proposal, tick())
		must("propose outcome", err)
		exit, err := kernel.NewAttemptResultExitCode(0)
		must("exit", err)
		result, err := kernel.NewInnerConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, runtimeIdentity, providerIdentity, exit)
		must("attempt result", err)
		live, err = fixture.store.ConsumeAttemptResult(ctx, result, live.Revision, tick())
		must("consume result", err)
		runnerExit, err := kernel.NewProcessExitCode(1, 0, tick())
		must("runner exit", err)
		runnerResource := resource(keys.Resources.RunnerProcess)
		live, _, err = fixture.store.RecordLiveRunnerExitAndRelease(ctx, run.ID, runnerResource.ID, live.Revision, runnerResource.Revision, runnerResource.Identity, runnerExit, tick())
		must("record runner exit", err)
		runtime := resource(keys.Resources.RuntimeRoot)
		_, err = fixture.store.ReleaseResource(ctx, run.ID, runtime.ID, runtime.Revision, runtime.Identity, tick())
		must("release runtime", err)
		if session, found, err = fixture.store.TerminalSessionForRun(ctx, run.ID); err != nil || !found {
			t.Fatalf("terminal session: found=%v err=%v", found, err)
		}
		live, _, err = fixture.store.CloseTerminalAfterRunner(ctx, result, live.Revision, session.Revision, tick())
		must("close terminal", err)
		if change, found, err = fixture.store.Change(ctx, change.ID); err != nil || !found {
			t.Fatalf("change: found=%v err=%v", found, err)
		}
		if head == nil {
			head = &base
		}
		settlement, err := kernel.NewRetainedChangeSettlement(change.Revision, head)
		must("settlement", err)
		terminal, err := fixture.store.FinalizeWorkerRun(ctx, run.ID, live.Revision, settlement, tick())
		must("finalize run", err)
		return terminal
	}
	runNext := func(seed byte, task kernel.TaskID, proposal kernel.Proposal, head *kernel.CommitID) kernel.Run {
		t.Helper()
		keys, admission := admit(seed)
		if !admission.Admitted() || admission.Run.TaskID != task {
			t.Fatalf("admission %d = %+v, want task %s", seed, admission, task)
		}
		return finish(keys, *admission.Run, proposal, head)
	}
	observe := func(pr kernel.ProductionPullRequest) {
		t.Helper()
		at := tick()
		must("observe pull request", fixture.store.RecordProductionObservation(ctx, project, kernel.ProductionObservation{Repository: "team/repo", ObservedAt: at.Int64(), PullRequests: []kernel.ProductionPullRequest{pr}, Checks: []kernel.ProductionCheck{}}, at))
	}
	pullRequest := func(number uint64, head kernel.CommitID, state string) kernel.ProductionPullRequest {
		sha := hex.EncodeToString(head.Bytes())
		return kernel.ProductionPullRequest{Number: number, Title: "producer", URL: fmt.Sprintf("https://github.com/team/repo/pull/%d", number), Head: sha, Branch: "factory/producer", Base: "main", State: state, Review: kernel.ProductionReview{Head: sha, State: "unknown"}}
	}
	success, err := kernel.NewSuccessProposal("producer result")
	must("success proposal", err)
	blocked, err := kernel.NewBlockedProposal("missing prerequisite: task " + producer.ID.String() + " is absent from the assigned base")
	must("blocked proposal", err)
	// The producer succeeds at a head beyond its base; its consumer waits for
	// that head to merge (#2008), then runs and blocks on the base it was given.
	runNext(0x30, producer.ID, success, &first)
	if _, admission := admit(0x40); admission.Admitted() || admission.Reason != kernel.NoAdmissionNoEligibleWork {
		t.Fatalf("consumer admitted before its producer merged: %+v", admission)
	}
	must("publish producer", fixture.store.RecordPublication(ctx, project, producer.ID, "team/repo", pullRequest(12, first, "open"), tick()))
	observe(pullRequest(12, first, "merged"))
	runNext(0x40, consumer.ID, blocked, nil)
	consumer = current(consumer.ID)
	if consumer.Status != kernel.TaskBlocked || consumer.WorkRevision != one || consumer.BlockedReason != blocked.Detail() {
		t.Fatalf("consumer after its run = %+v", consumer)
	}
	// A task sequenced only in prose declares no edge: the same block leaves
	// it to the overseer, whatever merges.
	prose := enqueue(130, "prose-sequenced")
	runNext(0x20, prose.ID, blocked, nil)
	if prose = current(prose.ID); prose.Status != kernel.TaskBlocked {
		t.Fatalf("prose-sequenced task after its run = %+v", prose)
	}
	passLeaves := func(step string, want kernel.Task) {
		t.Helper()
		fixture.daemon.advanceMergePipeline(ctx)
		if got := current(consumer.ID); got.Status != want.Status || got.Revision != want.Revision || got.WorkRevision != want.WorkRevision {
			t.Fatalf("%s: pass left consumer %s revision %d work revision %d, want %s revision %d work revision %d", step, got.Status, got.Revision.Int64(), got.WorkRevision.Int64(), want.Status, want.Revision.Int64(), want.WorkRevision.Int64())
		}
		if got := current(prose.ID); got.Status != kernel.TaskBlocked || got.Revision != prose.Revision {
			t.Fatalf("%s: pass touched the prose-sequenced task: %+v", step, got)
		}
	}

	// Meanwhile the producer is sent back, so the merged head no longer stands
	// for its work: the blocked consumer stays blocked through the correction.
	producer = current(producer.ID)
	_, err = fixture.store.SendBackTask(ctx, nil, producer.ID, producer.Revision, hex.EncodeToString(first.Bytes()), "correct the producer", tick())
	must("send producer back", err)
	passLeaves("producer sent back", consumer)
	runNext(0x50, producer.ID, success, &second)
	if producer = current(producer.ID); producer.Status != kernel.TaskSucceeded || producer.WorkRevision.Int64() != 2 {
		t.Fatalf("corrected producer = %+v", producer)
	}
	passLeaves("corrected head unpublished", consumer)
	must("publish correction", fixture.store.RecordPublication(ctx, project, producer.ID, "team/repo", pullRequest(13, second, "open"), tick()))
	passLeaves("corrected head open", consumer)

	// The merge is observed: the next pass queues the consumer once, as task
	// update --retry would, at its next work revision with the reason cleared.
	observe(pullRequest(13, second, "merged"))
	queued := consumer
	queued.Status, queued.WorkRevision = kernel.TaskQueued, mustRevision(t, 2)
	queued.Revision = mustRevision(t, consumer.Revision.Int64()+1)
	passLeaves("corrected head merged", queued)
	if got := current(consumer.ID); got.BlockedReason != "" || got.AssignedAgentID != worker.ID {
		t.Fatalf("retried consumer = %+v", got)
	}
	passLeaves("already retried", queued)

	// Blocking the same way again with every prerequisite in is not a merge
	// the retry waits for: the task is the overseer's.
	runNext(0x60, consumer.ID, blocked, nil)
	consumer = current(consumer.ID)
	if consumer.Status != kernel.TaskBlocked || consumer.WorkRevision.Int64() != 2 {
		t.Fatalf("consumer after its second run = %+v", consumer)
	}
	passLeaves("blocked again", consumer)
	must("validate store", fixture.store.Validate(ctx))
}
