//go:build darwin || linux

package api

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestProductionObserveUsesOperatorWireAndValidatesInput(t *testing.T) {
	bearer := testCredential('P')
	fixture := newWireFixture(t, bearer, func(connection net.Conn, request []byte) error {
		if !strings.Contains(string(request), `"method":"production_observe"`) || !strings.Contains(string(request), `"repository":"team/repo"`) {
			return errors.New("production observation was not sent on operator wire")
		}
		return writeTestResponse(connection, wireOperatorDomain, successResponse(`{"state":"recorded"}`))
	})
	client, err := NewOperatorClient(fixture.socket, fixture.token)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ProductionObserve(context.Background(), ProductionInput{ProjectID: id('1'), Observation: kernel.ProductionObservation{Repository: "team/repo", ObservedAt: 42}})
	if err != nil || result.State != "recorded" {
		t.Fatalf("production observe = %+v, %v", result, err)
	}
	fixture.wait(t)
	if _, err := client.ProductionObserve(context.Background(), ProductionInput{ProjectID: "short", Observation: kernel.ProductionObservation{Repository: "team/repo"}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid project accepted: %v", err)
	}
	if _, err := client.ProductionObserve(context.Background(), ProductionInput{ProjectID: id('1')}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty repository accepted: %v", err)
	}
}

func TestProductionObserveDecodeIsOperatorOnlyAndRequiresRepository(t *testing.T) {
	good := []byte(`{"method":"production_observe","params":{"project_id":"` + id('1') + `","observation":{"repository":"team/repo"}}}`)
	call, code := decodeCall(operatorDomain, credential{}, good)
	if code != "" {
		t.Fatalf("valid production observation rejected: %s", code)
	}
	input, ok := call.ProductionInput()
	if !ok || input.ProjectID != id('1') || input.Observation.Repository != "team/repo" {
		t.Fatalf("production input = %+v, ok=%v", input, ok)
	}
	bad := []byte(`{"method":"production_observe","params":{"project_id":"` + id('1') + `","observation":{}}}`)
	if _, code := decodeCall(operatorDomain, credential{}, bad); code != RemoteInvalidRequest {
		t.Fatalf("empty repository code = %s", code)
	}
	if _, code := decodeCall(attemptDomain, credential{}, good); code != RemoteForbidden {
		t.Fatalf("attempt production observe code = %s", code)
	}
}
