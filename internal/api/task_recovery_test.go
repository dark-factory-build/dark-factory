//go:build darwin || linux

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
)

func TestTaskRecoveryWireValidation(t *testing.T) {
	valid := TaskRecovery{State: "found", TaskID: id('1'), IncarnationID: id('2'), ProjectID: id('3'), AssignedAgentID: id('4'), WorkRevision: 1, Revision: 1, Status: "queued", ArtifactPaths: []string{}}
	tests := []struct {
		name    string
		change  func(*TaskRecovery)
		invalid bool
	}{
		{"found", func(*TaskRecovery) {}, false},
		{"missing", func(v *TaskRecovery) { *v = TaskRecovery{State: "missing", ArtifactPaths: []string{}} }, false},
		{"status", func(v *TaskRecovery) { v.Status = "" }, true},
		{"revision", func(v *TaskRecovery) { v.Revision = 0 }, true},
		{"work revision", func(v *TaskRecovery) { v.WorkRevision = 0 }, true},
		{"project", func(v *TaskRecovery) { v.ProjectID = "invalid" }, true},
		{"artifacts", func(v *TaskRecovery) { v.ArtifactPaths = nil }, true},
		{"missing data", func(v *TaskRecovery) { v.State = "missing" }, true},
		{"source without change", func(v *TaskRecovery) { v.SourceFormat = "sha1" }, true},
		{"run revision", func(v *TaskRecovery) { v.RunID = id('5') }, true},
		{"relative path", func(v *TaskRecovery) { v.RunID = id('5'); v.RunRevision = 1; v.ArtifactPaths = []string{"../private"} }, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			test.change(&value)
			if _, err := NewTaskRecoveryReply(value); errors.Is(err, ErrInvalidInput) != test.invalid {
				t.Fatalf("reply validation = %v", err)
			}
			body, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			fixture := newWireFixture(t, testCredential('R'), func(c net.Conn, _ []byte) error {
				return writeTestResponse(c, wireOperatorDomain, successResponse(string(body)))
			})
			client, err := NewOperatorClient(fixture.socket, fixture.token)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.TaskRecovery(context.Background(), TaskRecoveryInput{TaskID: valid.TaskID, IncarnationID: valid.IncarnationID})
			if errors.Is(err, ErrProtocol) != test.invalid {
				t.Fatalf("wire validation = %v", err)
			}
			<-fixture.request
			fixture.wait(t)
		})
	}
}

func TestTaskRecoveryReplyOwnsArtifacts(t *testing.T) {
	value := TaskRecovery{State: "found", TaskID: id('1'), IncarnationID: id('2'), ProjectID: id('3'), AssignedAgentID: id('4'), WorkRevision: 1, Revision: 1, Status: "running", RunID: id('5'), RunRevision: 1, ArtifactPaths: []string{"/private/runtime"}}
	reply, err := NewTaskRecoveryReply(value)
	if err != nil {
		t.Fatal(err)
	}
	value.ArtifactPaths[0] = "/other"
	if reply.taskRecovery.ArtifactPaths[0] != "/private/runtime" {
		t.Fatal("reply aliases caller artifacts")
	}
}
