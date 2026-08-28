package kernel

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestRunnerStartUncertaintyIsExactAndDurable(t *testing.T) {
	store, run, keys := admittedOrchestratorRun(t)
	path := storeTestPath(t, store)
	ctx := context.Background()
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	runtimeIdentity, _ := NewPathResourceIdentity(71, 72)
	if _, err := store.ActivateResource(ctx, run.ID, runtime.ID, runtime.Revision, runtimeIdentity, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	startedRun, starting, err := store.BeginRunnerStart(ctx, run.ID, runner.ID, run.Revision, runner.Revision, mustTime(t, 21))
	if err != nil || startedRun.Revision.Int64() != run.Revision.Int64()+1 || starting.State != ResourceStarting || !starting.Identity.Empty() {
		t.Fatalf("begin runner start = run %+v resource %+v err=%v", startedRun, starting, err)
	}
	beforeReplay, _ := store.Factory(ctx)
	replayedRun, replayedRunner, err := store.BeginRunnerStart(ctx, run.ID, runner.ID, run.Revision, runner.Revision, mustTime(t, 99))
	afterReplay, _ := store.Factory(ctx)
	if err != nil || replayedRun.Revision != startedRun.Revision || replayedRunner.Revision != starting.Revision || beforeReplay.Head != afterReplay.Head {
		t.Fatalf("exact begin replay mutated: run=%+v runner=%+v head=%d/%d err=%v", replayedRun, replayedRunner, beforeReplay.Head.Int64(), afterReplay.Head.Int64(), err)
	}
	if _, _, err := store.BeginRunnerStart(ctx, run.ID, runner.ID, startedRun.Revision, starting.Revision, mustTime(t, 22)); !errors.Is(err, ErrConflict) {
		t.Fatalf("second Start permission = %v", err)
	}
	identity := processIdentity(t, 710)
	activeRun, activeRunner, err := store.ActivateRunner(ctx, run.ID, runner.ID, startedRun.Revision, starting.Revision, identity, mustTime(t, 22))
	if err != nil || activeRunner.State != ResourceActive || activeRun.Revision.Int64() != startedRun.Revision.Int64()+1 || !resourceIdentityEqual(activeRunner.Identity, identity) {
		t.Fatalf("activate runner = run %+v resource %+v err=%v", activeRun, activeRunner, err)
	}
	if _, _, err := store.ActivateRunner(ctx, run.ID, runner.ID, startedRun.Revision, starting.Revision, processIdentity(t, 711), mustTime(t, 23)); !errors.Is(err, ErrConflict) {
		t.Fatalf("runner identity replacement = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	fresh, found, err := reopened.Run(ctx, keys.RunID)
	freshRunner, runnerFound, runnerErr := reopened.Resource(ctx, runner.ID)
	if err != nil || runnerErr != nil || !found || !runnerFound || fresh.Revision != activeRun.Revision || freshRunner.State != ResourceActive || !resourceIdentityEqual(freshRunner.Identity, identity) {
		t.Fatalf("reopened runner binding = run %+v/%v/%v runner %+v/%v/%v", fresh, found, err, freshRunner, runnerFound, runnerErr)
	}
}

func TestRecordUnregisteredRunnerConvergedSettlesOnlyStartingUncertainty(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	runtimeIdentity, _ := NewPathResourceIdentity(81, 82)
	if _, err := store.ActivateResource(ctx, run.ID, runtime.ID, runtime.Revision, runtimeIdentity, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	if _, err := store.RecordUnregisteredRunnerConverged(ctx, run.ID, runner.ID, run.Revision, runner.Revision, mustTime(t, 21)); !errors.Is(err, ErrConflict) {
		t.Fatalf("never-started without starting = %v", err)
	}
	startedRun, starting, err := store.BeginRunnerStart(ctx, run.ID, runner.ID, run.Revision, runner.Revision, mustTime(t, 21))
	if err != nil {
		t.Fatal(err)
	}
	settled, err := store.RecordUnregisteredRunnerConverged(ctx, run.ID, runner.ID, startedRun.Revision, starting.Revision, mustTime(t, 22))
	if err != nil || settled.Phase != RunFinalizing || settled.Proposal == nil || settled.Proposal.code != FailureSpawn || settled.ProviderExit != nil || settled.RunnerExit != nil || settled.CredentialRevokedAt == nil {
		t.Fatalf("never-started settlement = %+v err=%v", settled, err)
	}
	resources := resourcesForRunTest(t, store, run.ID)
	if resourceOfKind(t, resources, ResourceRuntimeRoot).State != ResourceReleasing || resourceOfKind(t, resources, ResourceRunnerProcess).State != ResourceReleased || resourceOfKind(t, resources, ResourceProviderProcess).State != ResourceReleased || resourceOfKind(t, resources, ResourceProviderGroup).State != ResourceReleased {
		t.Fatalf("never-started resource footprint = %+v", resources)
	}
	session := terminalSessionForRunTest(t, store, run.ID)
	if session.State != TerminalSessionClosed || session.ActivatedAt != nil {
		t.Fatalf("never-started terminal = %+v", session)
	}
	before, _ := store.Factory(ctx)
	replay, err := store.RecordUnregisteredRunnerConverged(ctx, run.ID, runner.ID, startedRun.Revision, starting.Revision, mustTime(t, 90))
	after, _ := store.Factory(ctx)
	if err != nil || replay.Revision != settled.Revision || before.Head != after.Head {
		t.Fatalf("never-started replay = %+v head=%d/%d err=%v", replay, before.Head.Int64(), after.Head.Int64(), err)
	}
}

func TestConsumeAttemptResultIsExactSingleUseAndClosesOnlyAfterRunner(t *testing.T) {
	store, run, keys := admittedOrchestratorRun(t)
	defer func() { _ = store.Close() }()
	path := storeTestPath(t, store)
	ctx := context.Background()
	reopen := func() {
		t.Helper()
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		store, err = Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
	}
	run, runtimeIdentity, runnerIdentity := activateStartedRunner(t, store, run, 90)
	result, err := NewInnerUnregisteredConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, runtimeIdentity)
	if err != nil {
		t.Fatal(err)
	}
	wrongRuntime, _ := NewPathResourceIdentity(999, 1000)
	wrong, _ := NewInnerUnregisteredConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, wrongRuntime)
	if _, err := store.ConsumeAttemptResult(ctx, wrong, run.Revision, mustTime(t, 24)); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong runtime result = %v", err)
	}
	consumed, err := store.ConsumeAttemptResult(ctx, result, run.Revision, mustTime(t, 24))
	if err != nil || consumed.Phase != RunFinalizing || consumed.Proposal == nil || consumed.Proposal.code != FailureSpawn || consumed.ProviderExit != nil {
		t.Fatalf("consume no-child result = %+v err=%v", consumed, err)
	}
	resources := resourcesForRunTest(t, store, run.ID)
	if resourceOfKind(t, resources, ResourceProviderProcess).State != ResourceReleased || resourceOfKind(t, resources, ResourceProviderGroup).State != ResourceReleased || resourceOfKind(t, resources, ResourceRunnerProcess).State != ResourceReleasing {
		t.Fatalf("consumed footprint = %+v", resources)
	}
	session := terminalSessionForRunTest(t, store, run.ID)
	if session.State != TerminalSessionReleasing {
		t.Fatalf("consumed session = %+v", session)
	}
	before, _ := store.Factory(ctx)
	replay, err := store.ConsumeAttemptResult(ctx, result, run.Revision, mustTime(t, 99))
	after, _ := store.Factory(ctx)
	if err != nil || replay.Revision != consumed.Revision || before.Head != after.Head {
		t.Fatalf("duplicate consume = %+v head=%d/%d err=%v", replay, before.Head.Int64(), after.Head.Int64(), err)
	}
	if _, err := store.AuthorizeAttemptResultRemoval(ctx, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("removal before runner release = %v", err)
	}
	reopen()
	if _, err := store.AuthorizeAttemptResultRemoval(ctx, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("reopened removal before runner release = %v", err)
	}
	if _, _, err := store.CloseTerminalAfterRunner(ctx, result, consumed.Revision, session.Revision, mustTime(t, 25)); !errors.Is(err, ErrConflict) {
		t.Fatalf("close before runner absence = %v", err)
	}
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	afterRunner, releasedRunner, err := store.RecordRecoveredRunnerAbsence(ctx, run.ID, runner.ID, consumed.Revision, runner.Revision, runnerIdentity, mustTime(t, 25))
	if err != nil || releasedRunner.State != ResourceReleased || afterRunner.RunnerExit == nil || !afterRunner.RunnerExit.RecoveredAbsence() {
		t.Fatalf("recovered runner absence = run %+v runner %+v err=%v", afterRunner, releasedRunner, err)
	}
	if _, err := store.AuthorizeAttemptResultRemoval(ctx, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("removal before terminal close = %v", err)
	}
	reopen()
	if _, err := store.AuthorizeAttemptResultRemoval(ctx, result); !errors.Is(err, ErrConflict) {
		t.Fatalf("reopened removal before terminal close = %v", err)
	}
	otherDigest, _ := AttemptDigestFromBytes(make([]byte, DigestBytes))
	wrongAttempt, _ := NewInnerUnregisteredConvergedAttemptResult(run.ID, otherDigest, keys.ResultProofDigest, runtimeIdentity)
	if _, _, err := store.CloseTerminalAfterRunner(ctx, wrongAttempt, afterRunner.Revision, session.Revision, mustTime(t, 26)); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong result closed terminal = %v", err)
	}
	closedRun, closed, err := store.CloseTerminalAfterRunner(ctx, result, afterRunner.Revision, session.Revision, mustTime(t, 26))
	if err != nil || closed.State != TerminalSessionClosed || closedRun.Revision.Int64() != afterRunner.Revision.Int64()+1 {
		t.Fatalf("terminal close = run %+v session %+v err=%v", closedRun, closed, err)
	}
	if _, err := store.AuthorizeAttemptResultRemoval(ctx, wrongAttempt); !errors.Is(err, ErrConflict) {
		t.Fatalf("wrong result authorized removal = %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if authorized, err := store.AuthorizeAttemptResultRemoval(ctx, result); err != nil || authorized.Revision != closedRun.Revision {
			t.Fatalf("idempotent removal authorization %d = %+v err=%v", attempt, authorized, err)
		}
	}
	reopen()
	if authorized, err := store.AuthorizeAttemptResultRemoval(ctx, result); err != nil || authorized.Revision != closedRun.Revision {
		t.Fatalf("reopened closed-terminal removal authorization = %+v err=%v", authorized, err)
	}
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	if _, err := store.ReleaseResource(ctx, run.ID, runtime.ID, runtime.Revision, runtime.Identity, mustTime(t, 27)); err != nil {
		t.Fatal(err)
	}
	reopen()
	if _, err := store.AuthorizeAttemptResultRemoval(ctx, result); err != nil {
		t.Fatalf("removal after runtime release = %v", err)
	}
	terminal, err := store.FinalizeRun(ctx, run.ID, closedRun.Revision, mustTime(t, 28))
	if err != nil || terminal.Phase != RunTerminal {
		t.Fatalf("terminalize exact result = %+v err=%v", terminal, err)
	}
	if _, err := store.ConsumeAttemptResult(ctx, result, terminal.Revision, mustTime(t, 29)); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal result replay = %v", err)
	}
	reopen()
	durable, found, err := store.Run(ctx, run.ID)
	if err != nil || !found || durable.Phase != RunTerminal || durable.RunnerExit == nil || !durable.RunnerExit.RecoveredAbsence() || durable.Proposal == nil || durable.Proposal.code != FailureSpawn {
		t.Fatalf("reopened result lifecycle = %+v found=%v err=%v", durable, found, err)
	}
	if authorized, err := store.AuthorizeAttemptResultRemoval(ctx, result); err != nil || authorized.Phase != RunTerminal {
		t.Fatalf("terminal removal authorization = %+v err=%v", authorized, err)
	}
}

func TestConsumeConvergedResultMatrixAndConcurrentConflict(t *testing.T) {
	store, run, keys := admittedOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	run, runtimeIdentity, _ := activateStartedRunner(t, store, run, 120)
	resources := resourcesForRunTest(t, store, run.ID)
	process := resourceOfKind(t, resources, ResourceProviderProcess)
	group := resourceOfKind(t, resources, ResourceProviderGroup)
	providerIdentity := processIdentity(t, 1220)
	if _, _, err := store.ActivateProviderResources(ctx, run.ID, process.ID, process.Revision, group.ID, group.Revision, providerIdentity, mustTime(t, 23)); err != nil {
		t.Fatal(err)
	}
	session := terminalSessionForRunTest(t, store, run.ID)
	running, err := store.ActivateRun(ctx, run.ID, session.ID, run.Revision, session.Revision, mustTime(t, 24))
	if err != nil {
		t.Fatal(err)
	}
	exitZero, _ := NewAttemptResultExitCode(0)
	exitOne, _ := NewAttemptResultExitCode(1)
	zero, _ := NewInnerConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, runtimeIdentity, providerIdentity, exitZero)
	one, _ := NewInnerConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, runtimeIdentity, providerIdentity, exitOne)
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	errs := make(chan error, 2)
	for _, result := range []AttemptResult{zero, one} {
		result := result
		go func() {
			defer wait.Done()
			<-start
			_, err := store.ConsumeAttemptResult(ctx, result, running.Revision, mustTime(t, 25))
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	successes := 0
	failures := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrRevisionConflict) || errors.Is(err, ErrConflict) {
			failures++
		} else {
			t.Fatalf("concurrent consume = %v", err)
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("concurrent results success/failure = %d/%d", successes, failures)
	}
	fresh, found, err := store.Run(ctx, run.ID)
	if err != nil || !found || fresh.Phase != RunFinalizing || fresh.ProviderExit == nil || fresh.ProviderExit.Sequence() != 1 || fresh.Proposal == nil || fresh.Proposal.code != FailureProviderExit {
		t.Fatalf("winning result = %+v found=%v err=%v", fresh, found, err)
	}
	resources = resourcesForRunTest(t, store, run.ID)
	process = resourceOfKind(t, resources, ResourceProviderProcess)
	group = resourceOfKind(t, resources, ResourceProviderGroup)
	if process.State != ResourceReleased || group.State != ResourceReleased || !resourceIdentityEqual(process.Identity, group.Identity) {
		t.Fatalf("provider pair not atomically released = %+v %+v", process, group)
	}
}

func TestConsumeConvergedResultAdmittedAndFinalizingMatrix(t *testing.T) {
	for _, activePair := range []bool{false, true} {
		name := "declared_pair"
		if activePair {
			name = "active_pair"
		}
		t.Run("admitted_"+name, func(t *testing.T) {
			store, run, keys := admittedOrchestratorRun(t)
			defer store.Close()
			run, runtimeIdentity, _ := activateStartedRunner(t, store, run, 160)
			resources := resourcesForRunTest(t, store, run.ID)
			process := resourceOfKind(t, resources, ResourceProviderProcess)
			group := resourceOfKind(t, resources, ResourceProviderGroup)
			providerIdentity := processIdentity(t, 1620)
			if activePair {
				if _, _, err := store.ActivateProviderResources(context.Background(), run.ID, process.ID, process.Revision, group.ID, group.Revision, providerIdentity, mustTime(t, 23)); err != nil {
					t.Fatal(err)
				}
			}
			exit, _ := NewAttemptResultExitSignal(15)
			result, _ := NewInnerConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, runtimeIdentity, providerIdentity, exit)
			consumed, err := store.ConsumeAttemptResult(context.Background(), result, run.Revision, mustTime(t, 24))
			if err != nil || consumed.Phase != RunFinalizing || consumed.Proposal == nil || consumed.Proposal.code != FailureActivation || consumed.ProviderExit == nil {
				t.Fatalf("admitted converged consume = %+v err=%v", consumed, err)
			}
			process = resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderProcess)
			group = resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderGroup)
			if process.State != ResourceReleased || group.State != ResourceReleased || !resourceIdentityEqual(process.Identity, providerIdentity) || !resourceIdentityEqual(group.Identity, providerIdentity) {
				t.Fatalf("admitted pair release = %+v %+v", process, group)
			}
		})
	}

	t.Run("finalizing_unresolved_pair", func(t *testing.T) {
		store, run, keys := runningStartedOrchestratorRun(t, 180)
		defer store.Close()
		runtimeIdentity := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot).Identity
		provider := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderProcess)
		group := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderGroup)
		proposal, _ := NewFailureProposal(FailureInternal, "first outcome")
		finalizing, err := store.FailRun(context.Background(), run.ID, run.Revision, proposal, mustTime(t, 30))
		if err != nil {
			t.Fatal(err)
		}
		provider = resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderProcess)
		group = resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderGroup)
		marked, process, markedGroup, err := store.MarkProviderResourcesUnresolved(context.Background(), run.ID, provider.ID, group.ID, finalizing.Revision, provider.Revision, group.Revision, provider.Identity, "absence uncertain", mustTime(t, 31))
		if err != nil || process.State != ResourceUnresolved || markedGroup.State != ResourceUnresolved || process.Revision != markedGroup.Revision {
			t.Fatalf("atomic unresolved pair = run %+v pair %+v/%+v err=%v", marked, process, markedGroup, err)
		}
		exit, _ := NewAttemptResultExitCode(7)
		result, _ := NewInnerConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, runtimeIdentity, provider.Identity, exit)
		consumed, err := store.ConsumeAttemptResult(context.Background(), result, marked.Revision, mustTime(t, 32))
		if err != nil || consumed.Proposal == nil || !consumed.Proposal.equal(proposal) || consumed.ProviderExit == nil {
			t.Fatalf("finalizing result consume = %+v err=%v", consumed, err)
		}
		resources := resourcesForRunTest(t, store, run.ID)
		if resourceOfKind(t, resources, ResourceProviderProcess).State != ResourceReleased || resourceOfKind(t, resources, ResourceProviderGroup).State != ResourceReleased || resourceOfKind(t, resources, ResourceRunnerProcess).State != ResourceReleasing || terminalSessionForRunTest(t, store, run.ID).State != TerminalSessionReleasing {
			t.Fatalf("finalizing result footprint = %+v terminal=%+v", resources, terminalSessionForRunTest(t, store, run.ID))
		}
	})
}

func TestProviderPairTransitionsAreAtomic(t *testing.T) {
	store, run, _ := runningOrchestratorRun(t)
	defer store.Close()
	proposal, _ := NewFailureProposal(FailureInternal, "cleanup")
	if _, err := store.FailRun(context.Background(), run.ID, run.Revision, proposal, mustTime(t, 40)); err != nil {
		t.Fatal(err)
	}
	resources := resourcesForRunTest(t, store, run.ID)
	process := resourceOfKind(t, resources, ResourceProviderProcess)
	group := resourceOfKind(t, resources, ResourceProviderGroup)
	before, _ := store.Factory(context.Background())
	if _, err := store.MarkResourceUnresolved(context.Background(), run.ID, process.ID, process.Revision, process.Identity, "uncertain", mustTime(t, 41)); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic provider transition = %v", err)
	}
	after, _ := store.Factory(context.Background())
	if before.Head != after.Head {
		t.Fatalf("rejected generic provider transition advanced invalidation head: %d -> %d", before.Head.Int64(), after.Head.Int64())
	}
	current, found, err := store.Run(context.Background(), run.ID)
	if err != nil || !found {
		t.Fatalf("read run: found=%v err=%v", found, err)
	}
	if _, _, _, err := store.MarkProviderResourcesUnresolved(context.Background(), run.ID, process.ID, group.ID, current.Revision, process.Revision, group.Revision, process.Identity, "uncertain", mustTime(t, 41)); err != nil {
		t.Fatal(err)
	}
	resources = resourcesForRunTest(t, store, run.ID)
	process = resourceOfKind(t, resources, ResourceProviderProcess)
	group = resourceOfKind(t, resources, ResourceProviderGroup)
	if process.State != ResourceUnresolved || group.State != ResourceUnresolved || process.UnresolvedReason != group.UnresolvedReason || process.Revision != group.Revision {
		t.Fatalf("generic provider transition split pair = %+v %+v", process, group)
	}
}

func TestGenericResourceMutationsCannotBypassOwnedProcessEdges(t *testing.T) {
	store, run, _ := admittedOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	resources := resourcesForRunTest(t, store, run.ID)
	runner := resourceOfKind(t, resources, ResourceRunnerProcess)
	if _, err := store.ActivateResource(ctx, run.ID, runner.ID, runner.Revision, processIdentity(t, 1700), mustTime(t, 20)); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic declared-to-active runner bypass = %v", err)
	}
	for _, kind := range []ResourceKind{ResourceProviderProcess, ResourceProviderGroup} {
		resource := resourceOfKind(t, resources, kind)
		identity := processIdentity(t, 1701)
		for name, mutate := range map[string]func() error{
			"activate": func() error {
				_, err := store.ActivateResource(ctx, run.ID, resource.ID, resource.Revision, identity, mustTime(t, 21))
				return err
			},
			"begin release": func() error {
				_, err := store.BeginResourceRelease(ctx, run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, 21))
				return err
			},
			"unresolved": func() error {
				_, err := store.MarkResourceUnresolved(ctx, run.ID, resource.ID, resource.Revision, resource.Identity, "split", mustTime(t, 21))
				return err
			},
			"release": func() error {
				_, err := store.ReleaseResource(ctx, run.ID, resource.ID, resource.Revision, resource.Identity, mustTime(t, 21))
				return err
			},
		} {
			if err := mutate(); !errors.Is(err, ErrConflict) {
				t.Fatalf("generic %s %s bypass = %v", kind.String(), name, err)
			}
		}
	}
}

func TestRuntimeAbsenceAndPreRunnerFailureEdgesAreExact(t *testing.T) {
	t.Run("trusted absent", func(t *testing.T) {
		store, run, _ := admittedOrchestratorRun(t)
		defer store.Close()
		ctx := context.Background()
		runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
		failure, _ := NewFailureProposal(FailureSpawn, "runtime absent")
		if _, err := store.FailRun(ctx, run.ID, run.Revision, failure, mustTime(t, 20)); !errors.Is(err, ErrConflict) {
			t.Fatalf("generic failure substituted for absence evidence = %v", err)
		}
		before, _ := store.Factory(ctx)
		failed, err := store.FailRunWithRuntimeAbsent(ctx, run.ID, runtime.ID, run.Revision, runtime.Revision, failure, mustTime(t, 20))
		if err != nil || failed.Phase != RunFinalizing || failed.Proposal == nil || !failed.Proposal.equal(failure) {
			t.Fatalf("absent runtime failure = %+v err=%v", failed, err)
		}
		for _, resource := range resourcesForRunTest(t, store, run.ID) {
			if resource.State != ResourceReleased || !resource.Identity.Empty() {
				t.Fatalf("absent runtime left effectful resource = %+v", resource)
			}
		}
		if session := terminalSessionForRunTest(t, store, run.ID); session.State != TerminalSessionClosed || session.ActivatedAt != nil {
			t.Fatalf("absent runtime terminal = %+v", session)
		}
		after, _ := store.Factory(ctx)
		if after.Head.Int64() != before.Head.Int64()+1 {
			t.Fatalf("absence invalidation head = %d -> %d", before.Head.Int64(), after.Head.Int64())
		}
		replay, err := store.FailRunWithRuntimeAbsent(ctx, run.ID, runtime.ID, run.Revision, runtime.Revision, failure, mustTime(t, 99))
		afterReplay, _ := store.Factory(ctx)
		if err != nil || replay.Revision != failed.Revision || afterReplay.Head != after.Head {
			t.Fatalf("absence replay = %+v head=%d/%d err=%v", replay, after.Head.Int64(), afterReplay.Head.Int64(), err)
		}
	})

	t.Run("uncertain or bound runtime", func(t *testing.T) {
		store, run, _ := admittedOrchestratorRun(t)
		defer store.Close()
		ctx := context.Background()
		runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
		identity, _ := NewPathResourceIdentity(1702, 1703)
		active, err := store.ActivateResource(ctx, run.ID, runtime.ID, runtime.Revision, identity, mustTime(t, 20))
		if err != nil {
			t.Fatal(err)
		}
		failure, _ := NewFailureProposal(FailureSpawn, "runtime uncertainty")
		if _, err := store.FailRunWithRuntimeAbsent(ctx, run.ID, runtime.ID, run.Revision, active.Revision, failure, mustTime(t, 21)); !errors.Is(err, ErrConflict) {
			t.Fatalf("bound runtime forged absent = %v", err)
		}
		failed, err := store.FailRun(ctx, run.ID, run.Revision, failure, mustTime(t, 21))
		if err != nil || failed.Phase != RunFinalizing {
			t.Fatalf("bound runtime pre-start failure = %+v err=%v", failed, err)
		}
		resources := resourcesForRunTest(t, store, run.ID)
		if resourceOfKind(t, resources, ResourceRuntimeRoot).State != ResourceReleasing || resourceOfKind(t, resources, ResourceRunnerProcess).State != ResourceReleased || resourceOfKind(t, resources, ResourceProviderProcess).State != ResourceReleased || resourceOfKind(t, resources, ResourceProviderGroup).State != ResourceReleased {
			t.Fatalf("bound runtime pre-start footprint = %+v", resources)
		}
	})

	t.Run("runner starting and split active footprint refuse", func(t *testing.T) {
		store, run, _ := admittedOrchestratorRun(t)
		defer store.Close()
		ctx := context.Background()
		runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
		runtimeIdentity, _ := NewPathResourceIdentity(1704, 1705)
		if _, err := store.ActivateResource(ctx, run.ID, runtime.ID, runtime.Revision, runtimeIdentity, mustTime(t, 20)); err != nil {
			t.Fatal(err)
		}
		runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
		started, starting, err := store.BeginRunnerStart(ctx, run.ID, runner.ID, run.Revision, runner.Revision, mustTime(t, 21))
		if err != nil {
			t.Fatal(err)
		}
		failure, _ := NewFailureProposal(FailureSpawn, "must settle Start")
		if _, err := store.FailRun(ctx, run.ID, started.Revision, failure, mustTime(t, 22)); !errors.Is(err, ErrConflict) {
			t.Fatalf("failure while starting = %v", err)
		}
		active, _, err := store.ActivateRunner(ctx, run.ID, runner.ID, started.Revision, starting.Revision, processIdentity(t, 1706), mustTime(t, 22))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.FailRun(ctx, run.ID, active.Revision, failure, mustTime(t, 23)); !errors.Is(err, ErrConflict) {
			t.Fatalf("failure with active runner and declared provider pair = %v", err)
		}
	})
}

func TestLiveRunnerExitReleaseAndResultCloseAreExact(t *testing.T) {
	store, run, keys := runningStartedOrchestratorRun(t, 1800)
	defer store.Close()
	ctx := context.Background()
	resources := resourcesForRunTest(t, store, run.ID)
	runtimeIdentity := resourceOfKind(t, resources, ResourceRuntimeRoot).Identity
	runner := resourceOfKind(t, resources, ResourceRunnerProcess)
	providerIdentity := resourceOfKind(t, resources, ResourceProviderProcess).Identity
	providerExit, _ := NewAttemptResultExitCode(0)
	result, _ := NewInnerConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, runtimeIdentity, providerIdentity, providerExit)
	consumed, err := store.ConsumeAttemptResult(ctx, result, run.Revision, mustTime(t, 30))
	if err != nil {
		t.Fatal(err)
	}
	runner = resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	session := terminalSessionForRunTest(t, store, run.ID)
	outerExit, _ := NewProcessExitCode(1, 0, mustTime(t, 31))
	before, _ := store.Factory(ctx)
	afterExit, released, err := store.RecordLiveRunnerExitAndRelease(ctx, run.ID, runner.ID, consumed.Revision, runner.Revision, runner.Identity, outerExit, mustTime(t, 31))
	if err != nil || released.State != ResourceReleased || afterExit.RunnerExit == nil || !afterExit.RunnerExit.equal(outerExit) {
		t.Fatalf("atomic live runner exit/release = run %+v runner %+v err=%v", afterExit, released, err)
	}
	after, _ := store.Factory(ctx)
	if after.Head.Int64() != before.Head.Int64()+1 {
		t.Fatalf("live exit invalidation = %d -> %d", before.Head.Int64(), after.Head.Int64())
	}
	replay, replayedRunner, err := store.RecordLiveRunnerExitAndRelease(ctx, run.ID, runner.ID, consumed.Revision, runner.Revision, runner.Identity, outerExit, mustTime(t, 99))
	afterReplay, _ := store.Factory(ctx)
	if err != nil || replay.Revision != afterExit.Revision || replayedRunner.Revision != released.Revision || afterReplay.Head != after.Head {
		t.Fatalf("live exit replay = run %+v runner %+v head=%d/%d err=%v", replay, replayedRunner, after.Head.Int64(), afterReplay.Head.Int64(), err)
	}
	if _, err := store.CloseActiveTerminalSession(ctx, run.ID, session.ID, afterExit.Revision, session.Revision, mustTime(t, 32)); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy close bypassed result authority = %v", err)
	}
	closedRun, closed, err := store.CloseTerminalAfterRunner(ctx, result, afterExit.Revision, session.Revision, mustTime(t, 32))
	if err != nil || closed.State != TerminalSessionClosed {
		t.Fatalf("exact result close = run %+v session %+v err=%v", closedRun, closed, err)
	}
	if _, err := store.CloseActiveTerminalSession(ctx, run.ID, session.ID, afterExit.Revision, session.Revision, mustTime(t, 99)); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy close replay bypassed result authority = %v", err)
	}
}

func TestAttemptResultConstructorsRejectMalformedValues(t *testing.T) {
	runtime, _ := NewPathResourceIdentity(1, 2)
	process := processIdentity(t, 1500)
	digest, _ := AttemptDigestFromBytes(make([]byte, DigestBytes))
	exit, _ := NewAttemptResultExitCode(0)
	proof, _ := ResultProofDigestFromBytes(make([]byte, DigestBytes))
	if _, err := NewInnerUnregisteredConvergedAttemptResult(RunID{}, digest, proof, runtime); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero run = %v", err)
	}
	if _, err := NewInnerConvergedAttemptResult(runID(t, 1), digest, proof, process, process, exit); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("process runtime = %v", err)
	}
	if _, err := NewAttemptResultExitSignal(0); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("zero signal = %v", err)
	}
	malformed := AttemptResult{runID: runID(t, 1), attemptDigest: digest, runtimeIdentity: runtime, kind: AttemptResultKind(255)}
	store, _, _ := admittedOrchestratorRun(t)
	defer store.Close()
	if _, err := store.ConsumeAttemptResult(context.Background(), malformed, mustRevision(t, 1), mustTime(t, 20)); !errors.Is(err, ErrInvalidValue) {
		t.Fatalf("unknown kind = %v", err)
	}
}

func TestAttemptResultPhaseGuardRejectsUnknownAndTerminal(t *testing.T) {
	for _, phase := range []RunPhase{0, RunTerminal, RunPhase(255)} {
		if attemptResultConsumablePhase(phase) {
			t.Fatalf("phase %d accepted", phase)
		}
	}
	for _, phase := range []RunPhase{RunAdmitted, RunRunning, RunFinalizing} {
		if !attemptResultConsumablePhase(phase) {
			t.Fatalf("phase %s rejected", phase)
		}
	}
}

func TestRecoveredPreExecRunnerAbsenceFinalizesExactly(t *testing.T) {
	store, run, keys := admittedOrchestratorRun(t)
	defer func() { _ = store.Close() }()
	path := storeTestPath(t, store)
	ctx := context.Background()
	run, _, runnerIdentity := activateStartedRunner(t, store, run, 2200)
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	before, _ := store.Factory(ctx)
	settled, err := store.RecordRecoveredPreExecRunnerAbsence(ctx, run.ID, runner.ID, run.Revision, runner.Revision, runnerIdentity, mustTime(t, 30))
	if err != nil || settled.Phase != RunFinalizing || settled.Proposal == nil || settled.Proposal.code != FailureActivation ||
		settled.ProviderExit != nil || settled.RunnerExit == nil || !settled.RunnerExit.RecoveredAbsence() || settled.CredentialRevokedAt == nil {
		t.Fatalf("pre-exec absence settlement = %+v err=%v", settled, err)
	}
	resources := resourcesForRunTest(t, store, run.ID)
	released := resourceOfKind(t, resources, ResourceRunnerProcess)
	if resourceOfKind(t, resources, ResourceRuntimeRoot).State != ResourceReleasing ||
		released.State != ResourceReleased || !resourceIdentityEqual(released.Identity, runnerIdentity) ||
		resourceOfKind(t, resources, ResourceProviderProcess).State != ResourceReleased || !resourceOfKind(t, resources, ResourceProviderProcess).Identity.Empty() ||
		resourceOfKind(t, resources, ResourceProviderGroup).State != ResourceReleased || !resourceOfKind(t, resources, ResourceProviderGroup).Identity.Empty() {
		t.Fatalf("pre-exec absence footprint = %+v", resources)
	}
	if session := terminalSessionForRunTest(t, store, run.ID); session.State != TerminalSessionClosed || session.ActivatedAt != nil {
		t.Fatalf("pre-exec absence terminal = %+v", session)
	}
	after, _ := store.Factory(ctx)
	if after.Head.Int64() != before.Head.Int64()+1 {
		t.Fatalf("pre-exec absence invalidation head = %d -> %d", before.Head.Int64(), after.Head.Int64())
	}
	replay, err := store.RecordRecoveredPreExecRunnerAbsence(ctx, run.ID, runner.ID, run.Revision, runner.Revision, runnerIdentity, mustTime(t, 90))
	afterReplay, _ := store.Factory(ctx)
	if err != nil || replay.Revision != settled.Revision || afterReplay.Head != after.Head {
		t.Fatalf("pre-exec absence replay = %+v head=%d/%d err=%v", replay, after.Head.Int64(), afterReplay.Head.Int64(), err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	store = reopened
	durable, found, err := store.Run(ctx, run.ID)
	if err != nil || !found || durable.Phase != RunFinalizing || durable.RunnerExit == nil || !durable.RunnerExit.RecoveredAbsence() || durable.Proposal == nil || durable.Proposal.code != FailureActivation {
		t.Fatalf("reopened pre-exec absence = %+v found=%v err=%v", durable, found, err)
	}
	if _, err := store.AuthenticateAttempt(ctx, keys.AttemptDigest); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("pre-exec absence retained attempt credential = %v", err)
	}
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	if _, err := store.ReleaseResource(ctx, run.ID, runtime.ID, runtime.Revision, runtime.Identity, mustTime(t, 95)); err != nil {
		t.Fatal(err)
	}
	durable, _, err = store.Run(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := store.FinalizeRun(ctx, run.ID, durable.Revision, mustTime(t, 96))
	if err != nil || terminal.Phase != RunTerminal || terminal.Terminal == nil || terminal.Terminal.code != FailureActivation {
		t.Fatalf("pre-exec absence terminalization = %+v err=%v", terminal, err)
	}
}

func TestRecoveredPreExecRunnerAbsenceFailsClosed(t *testing.T) {
	t.Run("identity and phase guards", func(t *testing.T) {
		store, run, keys := admittedOrchestratorRun(t)
		defer store.Close()
		ctx := context.Background()
		run, runtimeIdentity, runnerIdentity := activateStartedRunner(t, store, run, 2300)
		runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
		before, _ := store.Factory(ctx)
		if _, err := store.RecordRecoveredPreExecRunnerAbsence(ctx, run.ID, runner.ID, run.Revision, runner.Revision, processIdentity(t, 2399), mustTime(t, 30)); !errors.Is(err, ErrConflict) {
			t.Fatalf("wrong identity = %v", err)
		}
		if _, err := store.RecordRecoveredPreExecRunnerAbsence(ctx, run.ID, runner.ID, mustRevision(t, run.Revision.Int64()+5), runner.Revision, runnerIdentity, mustTime(t, 30)); !errors.Is(err, ErrConflict) {
			t.Fatalf("wrong run revision = %v", err)
		}
		if _, err := store.RecordRecoveredPreExecRunnerAbsence(ctx, run.ID, runner.ID, run.Revision, mustRevision(t, runner.Revision.Int64()+5), runnerIdentity, mustTime(t, 30)); !errors.Is(err, ErrConflict) {
			t.Fatalf("wrong runner revision = %v", err)
		}
		if _, err := store.RecordRecoveredPreExecRunnerAbsence(ctx, run.ID, runner.ID, run.Revision, runner.Revision, runnerIdentity, mustTime(t, 1)); !errors.Is(err, ErrConflict) {
			t.Fatalf("time regression = %v", err)
		}
		result, _ := NewInnerUnregisteredConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, runtimeIdentity)
		consumed, err := store.ConsumeAttemptResult(ctx, result, run.Revision, mustTime(t, 31))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.RecordRecoveredPreExecRunnerAbsence(ctx, run.ID, runner.ID, consumed.Revision, runner.Revision, runnerIdentity, mustTime(t, 32)); !errors.Is(err, ErrConflict) {
			t.Fatalf("finalizing run accepted pre-exec absence = %v", err)
		}
		after, _ := store.Factory(ctx)
		if after.Head.Int64() != before.Head.Int64()+1 {
			t.Fatalf("rejections advanced invalidation head: %d -> %d", before.Head.Int64(), after.Head.Int64())
		}
	})

	t.Run("starting runner has no absence authority", func(t *testing.T) {
		store, run, _ := admittedOrchestratorRun(t)
		defer store.Close()
		ctx := context.Background()
		runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
		runtimeIdentity, _ := NewPathResourceIdentity(2400, 2401)
		if _, err := store.ActivateResource(ctx, run.ID, runtime.ID, runtime.Revision, runtimeIdentity, mustTime(t, 20)); err != nil {
			t.Fatal(err)
		}
		runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
		started, starting, err := store.BeginRunnerStart(ctx, run.ID, runner.ID, run.Revision, runner.Revision, mustTime(t, 21))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.RecordRecoveredPreExecRunnerAbsence(ctx, run.ID, runner.ID, started.Revision, starting.Revision, processIdentity(t, 2402), mustTime(t, 22)); !errors.Is(err, ErrConflict) {
			t.Fatalf("starting runner accepted absence = %v", err)
		}
	})

	t.Run("declared or activated provider pair refuses", func(t *testing.T) {
		store, run, _ := runningStartedOrchestratorRun(t, 2500)
		defer store.Close()
		ctx := context.Background()
		runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
		if _, err := store.RecordRecoveredPreExecRunnerAbsence(ctx, run.ID, runner.ID, run.Revision, runner.Revision, runner.Identity, mustTime(t, 30)); !errors.Is(err, ErrConflict) {
			t.Fatalf("activated provider pair accepted = %v", err)
		}
	})

	t.Run("partial provider release rolls back atomically", func(t *testing.T) {
		store, run, _ := admittedOrchestratorRun(t)
		defer store.Close()
		ctx := context.Background()
		run, _, runnerIdentity := activateStartedRunner(t, store, run, 2600)
		process := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceProviderProcess)
		corruptSQL(t, store, `UPDATE resources SET state = 'released', released_at_ms = updated_at_ms WHERE id = ?`, process.ID.Bytes())
		runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
		if _, err := store.RecordRecoveredPreExecRunnerAbsence(ctx, run.ID, runner.ID, run.Revision, runner.Revision, runnerIdentity, mustTime(t, 30)); err == nil {
			t.Fatal("split provider pair accepted")
		}
		var phase, runnerState string
		var proposal, runnerExit any
		if err := store.writer.QueryRowContext(ctx, `SELECT phase, proposal_kind, runner_exit_kind FROM runs WHERE id = ?`, run.ID.Bytes()).Scan(&phase, &proposal, &runnerExit); err != nil {
			t.Fatal(err)
		}
		if err := store.writer.QueryRowContext(ctx, `SELECT state FROM resources WHERE id = ?`, runner.ID.Bytes()).Scan(&runnerState); err != nil {
			t.Fatal(err)
		}
		if phase != "admitted" || proposal != nil || runnerExit != nil || runnerState != "active" {
			t.Fatalf("partial release leaked state: phase=%s proposal=%v runnerExit=%v runner=%s", phase, proposal, runnerExit, runnerState)
		}
	})
}

func TestWrongResultProofDigestIsRefusedAtEveryResultSeam(t *testing.T) {
	store, run, keys := admittedOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	run, runtimeIdentity, runnerIdentity := activateStartedRunner(t, store, run, 2700)
	wrongProof, err := ResultProofDigestFromBytes(make([]byte, DigestBytes))
	if err != nil {
		t.Fatal(err)
	}
	// The attempt digest is correct; only the proof digest is forged. Every
	// result-authenticated seam must refuse it without touching state.
	forged, err := NewInnerUnregisteredConvergedAttemptResult(run.ID, keys.AttemptDigest, wrongProof, runtimeIdentity)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := store.Factory(ctx)
	if _, err := store.ConsumeAttemptResult(ctx, forged, run.Revision, mustTime(t, 30)); !errors.Is(err, ErrConflict) {
		t.Fatalf("forged proof consumed = %v", err)
	}
	after, _ := store.Factory(ctx)
	fresh, found, err := store.Run(ctx, run.ID)
	if err != nil || !found || before.Head != after.Head || fresh.Phase != RunAdmitted || fresh.Proposal != nil {
		t.Fatalf("forged proof consume mutated state: run=%+v head=%d/%d err=%v", fresh, before.Head.Int64(), after.Head.Int64(), err)
	}
	genuine, err := NewInnerUnregisteredConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, runtimeIdentity)
	if err != nil {
		t.Fatal(err)
	}
	consumed, err := store.ConsumeAttemptResult(ctx, genuine, run.Revision, mustTime(t, 31))
	if err != nil {
		t.Fatal(err)
	}
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	released, _, err := store.RecordRecoveredRunnerAbsence(ctx, run.ID, runner.ID, consumed.Revision, runner.Revision, runnerIdentity, mustTime(t, 32))
	if err != nil {
		t.Fatal(err)
	}
	session := terminalSessionForRunTest(t, store, run.ID)
	if _, _, err := store.CloseTerminalAfterRunner(ctx, forged, released.Revision, session.Revision, mustTime(t, 33)); !errors.Is(err, ErrConflict) {
		t.Fatalf("forged proof closed terminal = %v", err)
	}
	if _, err := store.AuthorizeAttemptResultRemoval(ctx, forged); !errors.Is(err, ErrConflict) {
		t.Fatalf("forged proof authorized removal = %v", err)
	}
	closedRun, closed, err := store.CloseTerminalAfterRunner(ctx, genuine, released.Revision, session.Revision, mustTime(t, 33))
	if err != nil || closed.State != TerminalSessionClosed {
		t.Fatalf("genuine result close = %+v, %v", closed, err)
	}
	if _, err := store.AuthorizeAttemptResultRemoval(ctx, forged); !errors.Is(err, ErrConflict) {
		t.Fatalf("forged proof authorized removal after close = %v", err)
	}
	if authorized, err := store.AuthorizeAttemptResultRemoval(ctx, genuine); err != nil || authorized.Revision != closedRun.Revision {
		t.Fatalf("genuine removal authorization = %+v, %v", authorized, err)
	}
}

func TestConsumeAttemptResultRefusesEveryKindWhileRunnerStarting(t *testing.T) {
	store, run, keys := admittedOrchestratorRun(t)
	defer store.Close()
	ctx := context.Background()
	runtime := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRuntimeRoot)
	runtimeIdentity, _ := NewPathResourceIdentity(2100, 2101)
	if _, err := store.ActivateResource(ctx, run.ID, runtime.ID, runtime.Revision, runtimeIdentity, mustTime(t, 20)); err != nil {
		t.Fatal(err)
	}
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	started, _, err := store.BeginRunnerStart(ctx, run.ID, runner.ID, run.Revision, runner.Revision, mustTime(t, 21))
	if err != nil {
		t.Fatal(err)
	}
	// The attempt-runner target cannot execute before ActivateRunner commits,
	// so no trusted result of any kind can exist while the runner row is
	// starting; consumption must fail closed without touching state.
	unregistered, _ := NewInnerUnregisteredConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, runtimeIdentity)
	exit, _ := NewAttemptResultExitCode(0)
	converged, _ := NewInnerConvergedAttemptResult(run.ID, keys.AttemptDigest, keys.ResultProofDigest, runtimeIdentity, processIdentity(t, 2102), exit)
	before, _ := store.Factory(ctx)
	for name, result := range map[string]AttemptResult{"inner_unregistered_converged": unregistered, "inner_converged": converged} {
		if _, err := store.ConsumeAttemptResult(ctx, result, started.Revision, mustTime(t, 22)); !errors.Is(err, ErrConflict) {
			t.Fatalf("%s consumed while runner starting = %v", name, err)
		}
	}
	after, _ := store.Factory(ctx)
	fresh, found, err := store.Run(ctx, run.ID)
	if err != nil || !found || before.Head != after.Head || fresh.Phase != RunAdmitted || fresh.Proposal != nil || fresh.Revision != started.Revision {
		t.Fatalf("starting-state consume mutated durable state: run=%+v head=%d/%d err=%v", fresh, before.Head.Int64(), after.Head.Int64(), err)
	}
	if resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess).State != ResourceStarting {
		t.Fatal("runner left starting state without activation")
	}
}

func activateStartedRunner(t *testing.T, store *Store, run Run, seed int64) (Run, ResourceIdentity, ResourceIdentity) {
	return activateStartedRunnerAt(t, store, run, seed, 20)
}

func activateStartedRunnerAt(t *testing.T, store *Store, run Run, seed, at int64) (Run, ResourceIdentity, ResourceIdentity) {
	t.Helper()
	ctx := context.Background()
	resources := resourcesForRunTest(t, store, run.ID)
	runtime := resourceOfKind(t, resources, ResourceRuntimeRoot)
	runtimeIdentity, _ := NewPathResourceIdentity(seed, seed+1)
	if _, err := store.ActivateResource(ctx, run.ID, runtime.ID, runtime.Revision, runtimeIdentity, mustTime(t, at)); err != nil {
		t.Fatal(err)
	}
	runner := resourceOfKind(t, resourcesForRunTest(t, store, run.ID), ResourceRunnerProcess)
	started, starting, err := store.BeginRunnerStart(ctx, run.ID, runner.ID, run.Revision, runner.Revision, mustTime(t, at+1))
	if err != nil {
		t.Fatal(err)
	}
	runnerIdentity := processIdentity(t, seed+2)
	active, _, err := store.ActivateRunner(ctx, run.ID, runner.ID, started.Revision, starting.Revision, runnerIdentity, mustTime(t, at+2))
	if err != nil {
		t.Fatal(err)
	}
	return active, runtimeIdentity, runnerIdentity
}

func runningStartedOrchestratorRun(t *testing.T, seed int64) (*Store, Run, AdmissionKeys) {
	t.Helper()
	store, run, keys := admittedOrchestratorRun(t)
	run, _, _ = activateStartedRunner(t, store, run, seed)
	resources := resourcesForRunTest(t, store, run.ID)
	process := resourceOfKind(t, resources, ResourceProviderProcess)
	group := resourceOfKind(t, resources, ResourceProviderGroup)
	if _, _, err := store.ActivateProviderResources(context.Background(), run.ID, process.ID, process.Revision, group.ID, group.Revision, processIdentity(t, seed+10), mustTime(t, 23)); err != nil {
		store.Close()
		t.Fatal(err)
	}
	session := terminalSessionForRunTest(t, store, run.ID)
	running, err := store.ActivateRun(context.Background(), run.ID, session.ID, run.Revision, session.Revision, mustTime(t, 24))
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store, running, keys
}

func storeTestPath(t *testing.T, store *Store) string {
	t.Helper()
	var path string
	if err := store.writer.QueryRow(`PRAGMA database_list`).Scan(new(int), new(string), &path); err != nil {
		t.Fatal(err)
	}
	if path == "" {
		t.Fatal("empty Store path")
	}
	return filepath.Clean(path)
}
