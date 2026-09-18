//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

type operatorAPITestFixture struct {
	client   *api.OperatorClient
	listener *api.Listener
	daemon   *Daemon
}

func TestOperatorHumanReplyResolvesYieldedContinuation(t *testing.T) {
	adapter := newAdapterFixture(t, kernel.BrowserCapabilityObserve)
	run := adapterRunningRun(t, adapter.store, 40)
	ctx := context.Background()
	var priorKey [kernel.IDBytes]byte
	copy(priorKey[:], adapterID(t, 63))
	prior, err := adapter.store.CreateHumanQuestionForAttempt(ctx, run.CredentialDigest, kernel.NewHumanQuestion{IdempotencyKey: priorKey, QuestionText: "prior decision"}, adapterTime(t, 450))
	if err != nil {
		t.Fatal(err)
	}
	priorOperation, _ := kernel.HumanRequestDeliveryIDFromBytes(adapterID(t, 62))
	delivery, err := adapter.store.BeginHumanReplyForOperator(ctx, prior.ID, prior.Revision, priorOperation, "answered", adapterTime(t, 451))
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.store.AcknowledgeHumanReply(ctx, prior.ID, priorOperation, delivery.Revision, adapterTime(t, 452)); err != nil {
		t.Fatal(err)
	}
	var key [kernel.IDBytes]byte
	copy(key[:], adapterID(t, 60))
	request, err := adapter.store.CreateHumanQuestionForAttempt(context.Background(), run.CredentialDigest, kernel.NewHumanQuestion{IdempotencyKey: key, QuestionText: "continue?"}, adapterTime(t, 500))
	if err != nil {
		t.Fatal(err)
	}
	var condition kernel.ContinuationConditionID
	copy(condition[:], request.ID.Bytes())
	if _, err := adapter.store.YieldContinuationForAttempt(context.Background(), run.CredentialDigest, kernel.ConditionHumanRequest, condition, request.Revision, adapterTime(t, 500)); err != nil {
		t.Fatal(err)
	}
	completeYieldedOperatorRun(t, adapter.store, run)
	before, err := adapter.store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.store.ResolveHumanContinuationForOperator(ctx, request.ID, request.Revision, priorOperation, "collision", adapterTime(t, 506)); !errors.Is(err, kernel.ErrConflict) {
		t.Fatalf("cross-request operation collision = %v", err)
	}
	after, err := adapter.store.Snapshot(ctx)
	if err != nil || after.Head != before.Head {
		t.Fatalf("collision mutated durable state: %v -> %v, %v", before.Head, after.Head, err)
	}
	operator := newOperatorAPITestFixture(t, adapter.daemon)
	done := operator.serve(t)
	result, err := operator.client.HumanReply(context.Background(), api.OverseerHumanReplyInput{OperationID: hex.EncodeToString(adapterID(t, 61)), RequestID: request.ID.String(), ExpectedRevision: uint64(request.Revision.Int64()), Reply: "continue"})
	if err != nil || result.HumanReply == nil || result.HumanReply.State != "resolved" {
		t.Fatalf("yielded operator reply = %+v, %v", result, err)
	}
	waitOperatorAPI(t, done)
	resolved, err := adapter.store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	operation, _ := kernel.HumanRequestDeliveryIDFromBytes(adapterID(t, 61))
	if _, err := adapter.store.ResolveHumanContinuationForOperator(ctx, request.ID, request.Revision, operation, "continue", adapterTime(t, 507)); !errors.Is(err, kernel.ErrRevisionConflict) {
		t.Fatalf("stale resolved-request replay = %v", err)
	}
	replayed, err := adapter.store.Snapshot(ctx)
	if err != nil || replayed.Head != resolved.Head {
		t.Fatalf("replay promoted again: %v -> %v, %v", resolved.Head, replayed.Head, err)
	}
}

func completeYieldedOperatorRun(t *testing.T, store *kernel.Store, run kernel.Run) {
	t.Helper()
	ctx := context.Background()
	current, _, _ := store.Run(ctx, run.ID)
	resources, _ := store.Resources(ctx, run.ID)
	var runtimeRoot, providerProcess, runnerProcess kernel.Resource
	for _, resource := range resources {
		switch resource.Kind {
		case kernel.ResourceRuntimeRoot:
			runtimeRoot = resource
		case kernel.ResourceProviderProcess:
			providerProcess = resource
		case kernel.ResourceRunnerProcess:
			runnerProcess = resource
		}
	}
	providerExit, _ := kernel.NewAttemptResultExitCode(0)
	result, err := kernel.NewInnerConvergedAttemptResult(run.ID, run.CredentialDigest, run.ResultProofDigest(), runtimeRoot.Identity, providerProcess.Identity, providerExit)
	if err != nil {
		t.Fatal(err)
	}
	if current, err = store.ConsumeAttemptResult(ctx, result, current.Revision, adapterTime(t, 501)); err != nil {
		t.Fatal(err)
	}
	resources, _ = store.Resources(ctx, run.ID)
	for _, resource := range resources {
		if resource.Kind == kernel.ResourceRuntimeRoot {
			runtimeRoot = resource
		} else if resource.Kind == kernel.ResourceRunnerProcess {
			runnerProcess = resource
		}
	}
	runnerExit, _ := kernel.NewProcessExitCode(1, 0, adapterTime(t, 502))
	if current, _, err = store.RecordLiveRunnerExitAndRelease(ctx, run.ID, runnerProcess.ID, current.Revision, runnerProcess.Revision, runnerProcess.Identity, runnerExit, adapterTime(t, 502)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReleaseResource(ctx, run.ID, runtimeRoot.ID, runtimeRoot.Revision, runtimeRoot.Identity, adapterTime(t, 503)); err != nil {
		t.Fatal(err)
	}
	session, _, _ := store.TerminalSessionForRun(ctx, run.ID)
	if current, _, err = store.CloseTerminalAfterRunner(ctx, result, current.Revision, session.Revision, adapterTime(t, 504)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeRun(ctx, run.ID, current.Revision, adapterTime(t, 505)); err != nil {
		t.Fatal(err)
	}
}

func newOperatorAPITestFixture(t *testing.T, daemon *Daemon) *operatorAPITestFixture {
	t.Helper()
	authRoot := runtimeTempDir(t)
	authHome := filepath.Join(authRoot, "auth")
	if _, err := install.Init(context.Background(), authHome); err != nil {
		if errors.Is(err, install.ErrUnsupported) {
			t.Skip("operational local API is unsupported on this platform")
		}
		t.Fatal(err)
	}
	token := filepath.Join(authHome, "operator.token")
	if err := os.WriteFile(token, bytes.Repeat([]byte{'o'}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	home, err := install.OpenOperationalHome(context.Background(), authHome)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := home.OpenLocalAPI(context.Background())
	if err != nil {
		_ = home.Close()
		t.Fatal(err)
	}
	listener, err := api.Listen(authority)
	if err != nil {
		_ = home.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = home.Close()
	})
	socket := install.LocalAPISocketPath(authHome)
	client, err := api.NewOperatorClient(socket, token)
	if err != nil {
		t.Fatal(err)
	}
	return &operatorAPITestFixture{client: client, listener: listener, daemon: daemon}
}

func (fixture *operatorAPITestFixture) serve(t *testing.T) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		connection, err := fixture.listener.Accept()
		if err != nil {
			done <- err
			return
		}
		done <- fixture.daemon.HandleConnection(context.Background(), connection)
	}()
	return done
}

func waitOperatorAPI(t *testing.T, done <-chan error) {
	t.Helper()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func createOperatorHumanRequest(t *testing.T, fixture *terminalEffectFixture, seed byte) kernel.HumanRequest {
	t.Helper()
	var key [kernel.IDBytes]byte
	copy(key[:], adapterID(t, seed))
	request, err := fixture.adapter.store.CreateHumanQuestionForAttempt(context.Background(), fixture.run.CredentialDigest, kernel.NewHumanQuestion{
		IdempotencyKey: key, QuestionText: "operator question",
	}, adapterTime(t, int64(seed)+400))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestOperatorHumanListReplyRoundTripAndDeliveryUnknown(t *testing.T) {
	fixture := newTerminalEffectFixture(t)
	operator := newOperatorAPITestFixture(t, fixture.adapter.daemon)
	ctx := context.Background()
	request := createOperatorHumanRequest(t, fixture, 230)

	done := operator.serve(t)
	listed, err := operator.client.HumanRequests(ctx)
	if err != nil || len(listed.Requests) != 1 || listed.Requests[0].ID != request.ID.String() || listed.Requests[0].Status != "open" || listed.Requests[0].Question != "operator question" {
		t.Fatalf("operator human request list = %+v, %v", listed, err)
	}
	waitOperatorAPI(t, done)

	done = operator.serve(t)
	if _, err := operator.client.HumanReply(ctx, api.OverseerHumanReplyInput{
		OperationID: hex.EncodeToString(adapterID(t, 236)), RequestID: request.ID.String(), ExpectedRevision: uint64(request.Revision.Int64() + 1), Reply: "stale",
	}); err == nil {
		t.Fatal("stale open human request reply succeeded")
	} else {
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != api.RemoteRevisionConflict {
			t.Fatalf("stale open human request error = %v", err)
		}
	}
	waitOperatorAPI(t, done)
	expectNoTerminalEffectWire(t, fixture.peer)

	replyDone := make(chan struct {
		result api.MutationResult
		err    error
	}, 1)
	done = operator.serve(t)
	go func() {
		result, err := operator.client.HumanReply(ctx, api.OverseerHumanReplyInput{
			OperationID: hex.EncodeToString(adapterID(t, 231)), RequestID: request.ID.String(), ExpectedRevision: uint64(request.Revision.Int64()), Reply: "operator answer",
		})
		replyDone <- struct {
			result api.MutationResult
			err    error
		}{result: result, err: err}
	}()
	command := readTerminalEffectWire(t, fixture.peer)
	if command.Kind != string(runner.TerminalHumanReply) || string(command.Payload) != "operator answer" || !command.Submit {
		t.Fatalf("operator human reply command = %+v", command)
	}
	replyTerminalEffect(t, fixture.peer, command, runner.TerminalResultOK, uint32(len(command.Payload)))
	result := <-replyDone
	if result.err != nil || result.result.HumanReply == nil || result.result.HumanReply.State != "resolved" {
		t.Fatalf("operator human reply = %+v, %v", result.result, result.err)
	}
	waitOperatorAPI(t, done)

	done = operator.serve(t)
	if _, err := operator.client.HumanReply(ctx, api.OverseerHumanReplyInput{
		OperationID: hex.EncodeToString(adapterID(t, 232)), RequestID: request.ID.String(), ExpectedRevision: uint64(request.Revision.Int64()), Reply: "replay",
	}); err == nil {
		t.Fatal("replay of resolved human request succeeded")
	} else {
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != api.RemoteRevisionConflict {
			t.Fatalf("resolved human request replay error = %v", err)
		}
	}
	waitOperatorAPI(t, done)
	expectNoTerminalEffectWire(t, fixture.peer)

	unknownRequest := createOperatorHumanRequest(t, fixture, 233)
	unknownDone := make(chan struct {
		result api.MutationResult
		err    error
	}, 1)
	done = operator.serve(t)
	go func() {
		result, err := operator.client.HumanReply(ctx, api.OverseerHumanReplyInput{
			OperationID: hex.EncodeToString(adapterID(t, 234)), RequestID: unknownRequest.ID.String(), ExpectedRevision: uint64(unknownRequest.Revision.Int64()), Reply: "uncertain answer",
		})
		unknownDone <- struct {
			result api.MutationResult
			err    error
		}{result, err}
	}()
	uncertain := readTerminalEffectWire(t, fixture.peer)
	replyTerminalEffect(t, fixture.peer, uncertain, runner.TerminalResultUncertain, 0)
	if outcome := <-unknownDone; outcome.err != nil || outcome.result.HumanReply == nil || outcome.result.HumanReply.State != "delivery_unknown" {
		t.Fatalf("uncertain delivery outcome = %+v", outcome)
	}
	waitOperatorAPI(t, done)
	unknown, found, err := fixture.adapter.store.HumanRequest(ctx, unknownRequest.ID)
	if err != nil || !found || unknown.Status != kernel.HumanRequestDeliveryUnknown {
		t.Fatalf("uncertain operator human request = %+v, found=%v, err=%v", unknown, found, err)
	}

	done = operator.serve(t)
	if _, err := operator.client.HumanReply(ctx, api.OverseerHumanReplyInput{
		OperationID: hex.EncodeToString(adapterID(t, 235)), RequestID: unknownRequest.ID.String(), ExpectedRevision: uint64(unknownRequest.Revision.Int64()), Reply: "second delivery",
	}); err == nil {
		t.Fatal("delivery-unknown human request replay succeeded")
	}
	waitOperatorAPI(t, done)
	expectNoTerminalEffectWire(t, fixture.peer)
}
