//go:build darwin || linux

package api

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIntakeOperatorBoundaryAndExactActionFields(t *testing.T) {
	id := strings.Repeat("ab", 16)
	inputs := []IntakeInput{
		{Action: "list"}, {Action: "preview", SourceID: id, Page: 1},
		{Action: "accept", SourceID: id, ExpectedRevision: 1, IssueNumber: 9, ContentHash: strings.Repeat("cd", 32)},
		{Action: "withdraw", AcceptanceID: id}, {Action: "tick", SourceID: id, Page: 1000},
		{Action: "enable", SourceID: id, ExpectedRevision: 2, ReviewedRevision: 2},
		{Action: "tick", SourceID: id, Page: 1, AcceptanceCursor: id},
	}
	for _, input := range inputs {
		encoded, _ := json.Marshal(struct {
			Method string      `json:"method"`
			Params IntakeInput `json:"params"`
		}{"intake", input})
		call, code := decodeCall(operatorDomain, testCredential('I'), encoded)
		if code != "" || call.Kind() != CallIntake {
			t.Fatalf("%s: %v", input.Action, code)
		}
		if _, code := decodeCall(attemptDomain, testCredential('I'), encoded); code != RemoteForbidden {
			t.Fatalf("worker intake %s: %v", input.Action, code)
		}
		input.Configuration = &IntakeConfiguration{}
		if ValidIntakeInput(input) {
			t.Fatalf("irrelevant configuration accepted for %s", input.Action)
		}
	}
	for _, input := range []IntakeInput{
		{Action: "preview", SourceID: id, Page: 1, AcceptanceCursor: id},
		{Action: "tick", SourceID: id, Page: 1, AcceptanceCursor: "bad"},
		{Action: "list", AcceptanceID: id}, {Action: "enable", SourceID: id, ExpectedRevision: 2, ReviewedRevision: 1},
		{Action: "accept", SourceID: id, ExpectedRevision: 1, IssueNumber: 9, ContentHash: strings.Repeat("z", 64)},
		{Action: "preview", SourceID: id, Page: 1001}, {Action: "withdraw", AcceptanceID: id, SourceID: id},
	} {
		if ValidIntakeInput(input) {
			t.Fatalf("invalid action accepted: %+v", input)
		}
	}
}

// Exercise the real framed operator server and exact client decoder. JSON
// marshaling alone misses anonymous-field flattening at this boundary.
func TestIntakeSourceCrossesPrivateOperatorSocket(t *testing.T) {
	listener, socket := newAPITestListener(t, testCredential('I'))
	defer closeAPITestListener(t, listener)
	expected := IntakeResult{State: "ok", Sources: []IntakeSource{{
		ID: strings.Repeat("ab", 16), ProjectID: strings.Repeat("bc", 16),
		GitHubRepositoryID: 17, Repository: "fixture/repository", TargetRepositoryID: strings.Repeat("cd", 16),
		OverseerAgentID: strings.Repeat("de", 16), Label: "ready", Policy: "manual", TrustedAuthors: []string{},
		PollSeconds: 30, AdmissionLimit: 1, Enabled: true, Revision: 2,
	}}}
	done := startServerReceive(t, listener, func(call Call) Reply {
		input, ok := call.IntakeInput()
		if !ok || input.Action != "list" {
			return NewContentReply(IntakeResult{State: "invalid"})
		}
		return NewContentReply(expected)
	})
	client, err := NewOperatorClient(socket, filepath.Join(filepath.Dir(filepath.Dir(socket)), "operator.token"))
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Intake(t.Context(), IntakeInput{Action: "list"})
	if err != nil || !reflect.DeepEqual(result, expected) {
		t.Fatalf("intake socket result = %+v, %v", result, err)
	}
	if served := <-done; served.err != nil {
		t.Fatal(served.err)
	}
}
