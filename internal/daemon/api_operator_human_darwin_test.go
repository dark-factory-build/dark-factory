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

func newOperatorAPITestFixture(t *testing.T, daemon *Daemon) *operatorAPITestFixture {
	t.Helper()
	authHome := filepath.Join(runtimeTempDir(t), "auth")
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
		}{result: result, err: err}
	}()
	uncertain := readTerminalEffectWire(t, fixture.peer)
	replyTerminalEffect(t, fixture.peer, uncertain, runner.TerminalResultUncertain, 0)
	unknownResult := <-unknownDone
	if unknownResult.err != nil || unknownResult.result.HumanReply == nil || unknownResult.result.HumanReply.RequestID != unknownRequest.ID.String() || unknownResult.result.HumanReply.State != "delivery_unknown" {
		t.Fatalf("uncertain operator human reply = %+v, %v", unknownResult.result, unknownResult.err)
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
	} else {
		var remote *api.RemoteError
		if !errors.As(err, &remote) || remote.Code() != api.RemoteRevisionConflict {
			t.Fatalf("delivery-unknown human request replay error = %v", err)
		}
	}
	waitOperatorAPI(t, done)
	expectNoTerminalEffectWire(t, fixture.peer)
}
