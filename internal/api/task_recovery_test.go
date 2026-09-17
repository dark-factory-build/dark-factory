//go:build darwin || linux

package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
)

func TestTaskRecoveryWireValidation(t *testing.T) {
	valid := TaskRecovery{State: "found", TaskID: id('1'), IncarnationID: id('2'), ProjectID: id('3'), AssignedAgentID: id('4'), WorkRevision: 1, Revision: 1, Status: "queued", ArtifactPaths: []string{}, Disposition: "queued", OverseerNotification: "none"}
	invalidText := valid
	invalidText.Status, invalidText.Result = "succeeded", "\xff"
	if _, err := NewTaskRecoveryReply(invalidText); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("accepted invalid UTF-8 result")
	}
	tests := []struct {
		name    string
		change  func(*TaskRecovery)
		invalid bool
	}{
		{"found", func(*TaskRecovery) {}, false},
		{"result", func(v *TaskRecovery) { v.Status = "succeeded"; v.Result = "done" }, false},
		{"result on queued", func(v *TaskRecovery) { v.Result = "old result" }, true},
		{"oversized result", func(v *TaskRecovery) {
			v.Status = "succeeded"
			v.Result = strings.Repeat("x", MaxRecoveryResultBytes+1)
		}, true},
		{"false truncation", func(v *TaskRecovery) { v.Status = "succeeded"; v.ResultTruncated = true }, true},
		{"run detail without run", func(v *TaskRecovery) { v.RunOutcome = "failed"; v.RunDetail = "cause" }, true},
		{"missing", func(v *TaskRecovery) { *v = TaskRecovery{State: "missing", ArtifactPaths: []string{}} }, false},
		{"status", func(v *TaskRecovery) { v.Status = "" }, true},
		{"revision", func(v *TaskRecovery) { v.Revision = 0 }, true},
		{"work revision", func(v *TaskRecovery) { v.WorkRevision = 0 }, true},
		{"project", func(v *TaskRecovery) { v.ProjectID = "invalid" }, true},
		{"artifacts", func(v *TaskRecovery) { v.ArtifactPaths = nil }, true},
		{"missing data", func(v *TaskRecovery) { v.State = "missing" }, true},
		{"source without change", func(v *TaskRecovery) { v.SourceFormat = "sha1" }, true},
		{"prior work run", func(v *TaskRecovery) {
			v.WorkRevision = 2
			v.RunID = id('5')
			v.RunRevision = 1
			v.RunWorkRevision = 1
			v.RunOutcome = "failed"
			v.RunDetail = "prior failure"
		}, false},
		{"future work run", func(v *TaskRecovery) { v.RunID = id('5'); v.RunRevision = 1; v.RunWorkRevision = 2 }, true},
		{"run revision", func(v *TaskRecovery) { v.RunID = id('5') }, true},
		{"pending overseer", func(v *TaskRecovery) {
			v.OverseerNotification, v.OverseerAgentID = "pending", id('6')
			v.OverseerTaskID, v.OverseerTaskStatus, v.OverseerTaskTitle = id('7'), "running", "Coordinate release"
		}, false},
		{"older daemon without additive fields", func(v *TaskRecovery) { v.Disposition, v.OverseerNotification = "", "" }, false},
		{"scheduled", func(v *TaskRecovery) { v.OverseerNotification, v.OverseerAgentID = "scheduled", id('6') }, false},
		{"delivered is not a claim", func(v *TaskRecovery) { v.OverseerNotification, v.OverseerAgentID = "delivered", id('6') }, true},
		{"notification without overseer", func(v *TaskRecovery) { v.OverseerNotification = "scheduled" }, true},
		{"overseer without notification", func(v *TaskRecovery) { v.OverseerAgentID = id('6') }, true},
		{"overseer task without overseer", func(v *TaskRecovery) {
			v.OverseerTaskID, v.OverseerTaskStatus, v.OverseerTaskTitle = id('7'), "queued", "t"
		}, true},
		{"needs you", func(v *TaskRecovery) { v.Disposition, v.HumanRequestID = "needs_you", id('9') }, false},
		{"needs you without request", func(v *TaskRecovery) { v.Disposition = "needs_you" }, true},
		{"request without needs you", func(v *TaskRecovery) { v.HumanRequestID = id('9') }, true},
		{"disposition", func(v *TaskRecovery) { v.Disposition = "handled" }, true},
		{"run evidence", func(v *TaskRecovery) {
			v.RunID, v.RunRevision, v.RunWorkRevision, v.RunOutcome, v.RunDetail = id('5'), 1, 1, "failed", "provider exited before an attempt outcome"
			v.RunProviderExit, v.RunRunningMs = "code 1", 3000
		}, false},
		{"exit without run", func(v *TaskRecovery) { v.RunProviderExit = "code 1" }, true},
		{"malformed exit", func(v *TaskRecovery) {
			v.RunID, v.RunRevision, v.RunWorkRevision, v.RunProviderExit = id('5'), 1, 1, "code one"
		}, true},
		{"head without change", func(v *TaskRecovery) { v.ChangeHeadCommit = strings.Repeat("a", 40) }, true},
		{"missing with disposition", func(v *TaskRecovery) {
			*v = TaskRecovery{State: "missing", ArtifactPaths: []string{}, Disposition: "none"}
		}, true},
		{"relative path", func(v *TaskRecovery) {
			v.RunID = id('5')
			v.RunRevision = 1
			v.RunWorkRevision = 1
			v.ArtifactPaths = []string{"../private"}
		}, true},
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
	value := TaskRecovery{State: "found", TaskID: id('1'), IncarnationID: id('2'), ProjectID: id('3'), AssignedAgentID: id('4'), WorkRevision: 1, Revision: 1, Status: "running", RunID: id('5'), RunRevision: 1, RunWorkRevision: 1, ArtifactPaths: []string{"/private/runtime"}, Disposition: "running", OverseerNotification: "none"}
	reply, err := NewTaskRecoveryReply(value)
	if err != nil {
		t.Fatal(err)
	}
	value.ArtifactPaths[0] = "/other"
	if reply.taskRecovery.ArtifactPaths[0] != "/private/runtime" {
		t.Fatal("reply aliases caller artifacts")
	}
}

func TestTaskRecoveryMaximumEscapedTextFitsFrame(t *testing.T) {
	value := TaskRecovery{State: "found", TaskID: id('1'), IncarnationID: id('2'), ProjectID: id('3'), AssignedAgentID: id('4'), WorkRevision: 1, Revision: 1, Status: "succeeded", Result: strings.Repeat("<", MaxRecoveryResultBytes), ResultTruncated: true, RunID: id('5'), RunRevision: 1, RunWorkRevision: 1, RunOutcome: "succeeded", ArtifactPaths: []string{}, Disposition: "succeeded", OverseerNotification: "scheduled", OverseerAgentID: id('6'), OverseerTaskID: id('7'), OverseerTaskStatus: "queued", OverseerTaskTitle: strings.Repeat("<", 1024), LastProgressAtMs: 1 << 62, RunProviderExit: "signal 9", RunRunningMs: 1 << 62}
	for range 16 {
		value.ArtifactPaths = append(value.ArtifactPaths, "/"+strings.Repeat("<", 4095))
	}
	if _, err := NewTaskRecoveryReply(value); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded)+responsePrelude > maxFrameBytes {
		t.Fatalf("response bytes=%d err=%v", len(encoded), err)
	}
}
