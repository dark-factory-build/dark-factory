package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
)

type agentControlTestBackend struct {
	*fakeBackend
	mismatch   bool
	historyErr error
}

var _ AgentControlBackend = (*agentControlTestBackend)(nil)

func (b *agentControlTestBackend) ControlAgent(_ context.Context, _ Principal, r browserprotocol.AgentControl) (browserprotocol.AgentControlResult, error) {
	result := browserprotocol.AgentControlResult{OperationID: r.OperationID, TaskID: r.TaskID, RunID: r.RunID, Status: "delivered"}
	if b.mismatch {
		result.OperationID = strings.Repeat("04", 16)
	}
	return result, nil
}
func (b *agentControlTestBackend) TaskHistory(_ context.Context, _ [16]byte, r browserprotocol.TaskHistoryGet) (browserprotocol.TaskHistory, error) {
	return browserprotocol.TaskHistory{TaskID: r.TaskID, Entries: []browserprotocol.TaskHistoryEntry{}}, b.historyErr
}

// classifyingBackend names context errors the way the daemon does, so the test
// observes where the transport applies a backend's classifier: everywhere.
type classifyingBackend struct{ *agentControlTestBackend }

func (classifyingBackend) ClassifyError(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ErrRateLimited
	}
	return err
}

func TestEveryMethodErrorIsClassifiedOnceAtTheFrame(t *testing.T) {
	requests := map[string]func(string) ([]byte, error){
		"state": func(id string) ([]byte, error) { return testEncodeStateGet(id, browserprotocol.StateGet{}) },
		"watch": func(id string) ([]byte, error) { return testEncodeStateWatch(id, browserprotocol.StateWatch{}) },
		"detail": func(id string) ([]byte, error) {
			return testEncodeHumanRequestDetailGet(id, browserprotocol.HumanRequestDetailGet{RequestID: requestID, ExpectedRevision: 1})
		},
		"target": func(id string) ([]byte, error) {
			return testEncodeTerminalTargetGet(id, browserprotocol.TerminalTargetGet{AgentID: strings.Repeat("01", 16), ExpectedAgentRevision: 1, ExpectedHead: 7})
		},
		"history": func(id string) ([]byte, error) {
			return testEncodeTaskHistoryGet(id, browserprotocol.TaskHistoryGet{TaskID: strings.Repeat("02", 16)})
		},
	}
	for _, test := range []struct {
		cause     error
		code      browserprotocol.ErrorCode
		retryable bool
	}{
		{context.Canceled, browserprotocol.ErrorRateLimited, true},
		{fmt.Errorf("store read: %w", context.DeadlineExceeded), browserprotocol.ErrorRateLimited, true},
		{errors.New("unnamed"), browserprotocol.ErrorInternal, false},
	} {
		fake := newFakeBackend()
		fake.stateErr, fake.subErr, fake.detailErr, fake.targetErr = test.cause, test.cause, test.cause, test.cause
		server := startTaskServer(t, classifyingBackend{&agentControlTestBackend{fakeBackend: fake, historyErr: test.cause}})
		connection, _ := dialServer(t, server, testOrigin)
		authenticate(t, connection)
		for name, encode := range requests {
			payload, err := encode(name)
			if err != nil {
				t.Fatal(err)
			}
			writeClientFrame(t, connection, payload)
			frame := readServerFrame(t, connection)
			assertError(t, frame, test.code)
			if frame.ID != name || bool(frame.Body.(browserprotocol.Error).Retryable) != test.retryable {
				t.Fatalf("%s %v: frame=%+v, want retryable=%v", name, test.cause, frame, test.retryable)
			}
		}
	}
}

func TestAgentControlTransportCorrelatesDurableOperation(t *testing.T) {
	request := browserprotocol.AgentControl{OperationID: strings.Repeat("01", 16), TaskID: strings.Repeat("02", 16), RunID: strings.Repeat("03", 16), ExpectedTaskRevision: 1, ExpectedRunRevision: 2, Action: "interrupt"}
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "wrong operation"}[mismatch], func(t *testing.T) {
			backend := &agentControlTestBackend{fakeBackend: newFakeBackend(), mismatch: mismatch}
			server := startTaskServer(t, backend)
			connection, _ := dialServer(t, server, testOrigin)
			authenticate(t, connection)
			payload, err := testEncodeAgentControl("control", request)
			if err != nil {
				t.Fatal(err)
			}
			writeClientFrame(t, connection, payload)
			frame := readServerFrame(t, connection)
			if mismatch {
				assertError(t, frame, browserprotocol.ErrorInternal)
				return
			}
			if frame.Type != browserprotocol.TypeAgentControlResult || frame.ID != "control" || frame.Body.(browserprotocol.AgentControlResult).OperationID != request.OperationID {
				t.Fatalf("result: %+v", frame)
			}
			payload, err = testEncodeTaskHistoryGet("history", browserprotocol.TaskHistoryGet{TaskID: request.TaskID})
			if err != nil {
				t.Fatal(err)
			}
			writeClientFrame(t, connection, payload)
			frame = readServerFrame(t, connection)
			if frame.Type != browserprotocol.TypeTaskHistory || frame.ID != "history" || frame.Body.(browserprotocol.TaskHistory).TaskID != request.TaskID {
				t.Fatalf("history: %+v", frame)
			}
			payload, err = testEncodeTaskDetailGet("detail", browserprotocol.TaskDetailGet{TaskID: request.TaskID, ExpectedRevision: request.ExpectedTaskRevision})
			if err != nil {
				t.Fatal(err)
			}
			writeClientFrame(t, connection, payload)
			frame = readServerFrame(t, connection)
			if frame.Type != browserprotocol.TypeTaskDetail || frame.ID != "detail" || frame.Body.(browserprotocol.TaskDetail).TaskID != request.TaskID {
				t.Fatalf("detail: %+v", frame)
			}
		})
	}
}
