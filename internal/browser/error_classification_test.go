package browser

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
)

// classifyingBackend names context errors the way the daemon does, so the test
// observes where the transport applies a backend's classifier: everywhere.
type classifyingBackend struct{ *fakeBackend }

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
		server := startTaskServer(t, classifyingBackend{fake})
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
