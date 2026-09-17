//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/runner"
)

func TestTerminalWindowRedactionCannotBeBypassedByCursor(t *testing.T) {
	data := []byte("compile x.go\nAuthorization: Bearer private-value\nconfig /Users/operator/.codex/auth.json\n{\"token\":\n\"json-secret\"}\n{\"path\":\"/Users/operator/\n.secret\"}\n{\"message\":\n\"visible\"}\nfinished\npartial secret=value")
	for cursor := 0; cursor < len(data); cursor++ {
		for size := 1; size <= len(data)-cursor; size++ {
			got, omitted := redactTerminalWindow(data[cursor:cursor+size], uint64(cursor))
			if bytes.Contains(got, []byte("private-value")) || bytes.Contains(got, []byte("/Users/")) || bytes.Contains(got, []byte("json-secret")) || bytes.Contains(got, []byte("secret=value")) || len(got)+int(omitted) != size {
				t.Fatalf("cursor=%d size=%d leaked or miscounted: %q omitted=%d", cursor, size, got, omitted)
			}
		}
	}
	got, _ := redactTerminalWindow(data, 0)
	if !bytes.Contains(got, []byte("compile x.go")) || !bytes.Contains(got, []byte("finished")) {
		t.Fatalf("lost useful output: %q", got)
	}
	if got, _ := redactTerminalWindow([]byte("{\"token\":\n\"split-secret\"}\n{\"message\":\n\"visible\"}\n"), 0); bytes.Contains(got, []byte("split-secret")) || !bytes.Contains(got, []byte("visible")) {
		t.Fatalf("split JSON redaction or benign JSON handling = %q", got)
	}
	if got, _ := redactTerminalWindow([]byte("{\"path\":\"/Users/operator/\n.secret\"}\n"), 0); bytes.Contains(got, []byte("/Users/")) || bytes.Contains(got, []byte(".secret")) {
		t.Fatalf("split JSON path leaked: %q", got)
	}
	benign := []byte("{\"message\":\n\"visible\"}\n")
	if got, _ := redactTerminalWindow(benign[1:], 1); !bytes.Contains(got, []byte("visible")) {
		t.Fatalf("cursor-boundary benign JSON was over-redacted: %q", got)
	}
}

func TestTerminalObservationAPIReadsExactBoundedSnapshot(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttemptInProject(t, fixture, 11, testID(11), "worker")
	overseer := prepareActiveAttemptInProject(t, fixture, 41, testID(11), "orchestrator")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	session, found, err := fixture.store.TerminalSessionForRun(ctx, active.run.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	controller, peer := readyTerminalEffectController(t)
	live := newLiveAttempt(fixture.daemon, active.run.ID, session.ID, controller)
	live.releaseSent, live.readySeen = true, true
	if err := fixture.daemon.registerLiveAttempt(live); err != nil {
		t.Fatal(err)
	}
	startLiveAttempt(live, ctx)
	t.Cleanup(func() { _ = live.close(); _ = peer.Close() })
	data := []byte("compile\nAuthorization: Bearer secret-value\ndone\n")
	input := api.TerminalObserveInput{ProjectID: active.run.ProjectID.String(), TaskID: active.run.TaskID.String(), RunID: active.run.ID.String(), MaxBytes: 65536}
	cases := []struct {
		name     string
		caller   activeAttempt
		cursor   uint64
		budget   uint32
		head     uint64
		reset    bool
		rejected bool
		eof      bool
		live     bool
		want     string
	}{
		{"worker exact snapshot", active, 0, 65536, uint64(len(data)), false, false, false, false, "done\n"},
		{"overseer reads worker", overseer, 0, 65536, uint64(len(data)), false, false, false, false, "done\n"},
		{"raw response budget", active, 0, 12, uint64(len(data)), false, false, false, false, "compile\n"},
		{"cursor inside secret", active, uint64(strings.Index(string(data), "secret-value") + 3), 65536, uint64(len(data)), false, false, false, false, "done\n"},
		{"fixed snapshot excludes new output", active, 0, 65536, 8, false, false, false, false, "compile\n"},
		{"empty at head", active, uint64(len(data)), 65536, uint64(len(data)), false, false, true, false, ""},
		{"live output after attachment", active, 8, 65536, 8, false, false, false, true, "live\n"},
		{"expired replay cursor", active, 0, 65536, uint64(len(data)), true, false, false, false, ""},
		{"future cursor rejected attach", active, 99, 65536, 12, false, true, false, false, ""},
	}
	creditRead := false
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := input
			request.Cursor, request.MaxBytes = test.cursor, test.budget
			done := fixture.serve(t)
			type response struct {
				value api.TerminalObservation
				err   error
			}
			result := make(chan response, 1)
			go func() { v, e := test.caller.client.TerminalObserve(ctx, request); result <- response{v, e} }()
			attach := readTerminalEffectWire(t, peer)
			// A previous read's detach may precede this attach.
			for attach.Kind != string(runner.TerminalAttach) {
				attach = readTerminalEffectWire(t, peer)
			}
			// Credit is shared by the stream, not repeated per attachment.
			if !creditRead {
				credit := readTerminalEffectWire(t, peer)
				if credit.Kind != string(runner.TerminalCredit) || credit.Credit != liveAttemptCredit {
					t.Fatalf("initial replay credit=%+v", credit)
				}
				creditRead = true
			}
			if test.reset {
				writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalReset), Correlation: attach.Correlation, Floor: 8, Head: test.head})
			} else if test.rejected {
				writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalAttached), Correlation: attach.Correlation, Sequence: test.cursor, Head: test.head, Status: string(runner.TerminalResultRejected)})
			} else if test.live {
				writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalAttached), Correlation: attach.Correlation, Sequence: test.cursor, Head: test.head, Status: string(runner.TerminalResultOK)})
				writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalOutput), Start: test.head, End: test.head + 5, Payload: []byte("live\n")})
				writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalPTYEOF)})
			} else {
				writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalAttached), Correlation: attach.Correlation, Sequence: test.cursor, Head: test.head, Status: string(runner.TerminalResultOK)})
				for start := int(test.cursor); start < int(test.head); {
					end := min(start+17, int(test.head))
					writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalOutput), Correlation: attach.Correlation, Start: uint64(start), End: uint64(end), Payload: data[start:end]})
					start = end
				}
				if test.head < uint64(len(data)) {
					writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalOutput), Start: test.head, End: uint64(len(data)), Payload: data[test.head:]})
				}
			}
			if test.eof {
				writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalPTYEOF)})
			}
			got := <-result
			waitDispatch(t, done)
			wantNext := min(test.head, test.cursor+uint64(test.budget))
			if test.reset {
				wantNext = 8
			}
			if test.rejected {
				if got.err != nil || !got.value.Gap || got.value.Cursor != test.head || got.value.NextCursor != test.head || got.value.Omitted != 0 {
					t.Fatalf("future-cursor reset=%+v err=%v", got.value, got.err)
				}
				return
			}
			if test.live {
				if got.err != nil || got.value.Source != "live" || string(got.value.Payload) != test.want || got.value.Head != test.head+5 || got.value.NextCursor != test.head+5 || got.value.Gap {
					t.Fatalf("live observation=%+v err=%v", got.value, got.err)
				}
				return
			}
			if got.err != nil || got.value.NextCursor != wantNext || got.value.Head != test.head || got.value.Gap != test.reset || strings.Contains(string(got.value.Payload), "secret-value") || !strings.Contains(string(got.value.Payload), test.want) {
				t.Fatalf("snapshot=%+v err=%v", got.value, got.err)
			}
			if uint64(len(got.value.Payload))+got.value.Omitted != got.value.NextCursor-test.cursor {
				t.Fatalf("raw byte accounting=%+v", got.value)
			}
		})
	}
	sibling := prepareActiveAttemptInProject(t, fixture, 71, testID(11), "worker")
	for _, refused := range []struct {
		name   string
		caller activeAttempt
		target activeAttempt
	}{
		{"worker cannot read sibling", sibling, active},
		{"overseer cannot read overseer", overseer, overseer},
	} {
		t.Run(refused.name, func(t *testing.T) {
			denied := input
			denied.TaskID, denied.RunID = refused.target.run.TaskID.String(), refused.target.run.ID.String()
			done := fixture.serve(t)
			_, err := refused.caller.client.TerminalObserve(ctx, denied)
			waitDispatch(t, done)
			var remote *api.RemoteError
			if !errors.As(err, &remote) || remote.Code() != api.RemoteForbidden {
				t.Fatalf("role refusal = %v", err)
			}
		})
	}
	for _, mutation := range []func(*api.TerminalObserveInput){func(v *api.TerminalObserveInput) { v.ProjectID = testID(90) }, func(v *api.TerminalObserveInput) { v.TaskID = testID(90) }, func(v *api.TerminalObserveInput) { v.RunID = testID(90) }} {
		wrong := input
		mutation(&wrong)
		done := fixture.serve(t)
		_, err := active.client.TerminalObserve(ctx, wrong)
		waitDispatch(t, done)
		var remote *api.RemoteError
		if !errors.As(err, &remote) || (remote.Code() != api.RemoteForbidden && remote.Code() != api.RemoteConflict) {
			t.Fatalf("mismatched identity=%v", err)
		}
	}
	// Settling the caller revokes its attempt authority even if its terminal
	// transport is still registered during cleanup.
	done := fixture.serve(t)
	_, err = active.client.Block(ctx, "observation complete")
	waitDispatch(t, done)
	if err != nil {
		t.Fatal(err)
	}
	done = fixture.serve(t)
	_, err = active.client.TerminalObserve(ctx, input)
	waitDispatch(t, done)
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteUnauthorized {
		t.Fatalf("settled caller retained observation authority: %v", err)
	}

}

func TestTerminalObservationTargetAuthorizationMatrix(t *testing.T) {
	project := mustProjectID(t, testID(1))
	foreignProject := mustProjectID(t, testID(2))
	workerTask := mustTaskID(t, testID(3))
	siblingTask := mustTaskID(t, testID(4))
	workerRunID := mustRunID(t, testID(5))
	siblingRunID := mustRunID(t, testID(6))

	worker := kernel.AttemptAuthority{ProjectID: project, TaskID: workerTask, RunID: workerRunID, Role: kernel.RoleWorker}
	overseer := kernel.AttemptAuthority{ProjectID: project, Role: kernel.RoleOrchestrator}
	target := func(projectID kernel.ProjectID, taskID kernel.TaskID, runID kernel.RunID, role kernel.AgentRole) kernel.Run {
		return kernel.Run{ProjectID: projectID, TaskID: taskID, ID: runID, Role: role}
	}

	tests := []struct {
		name      string
		authority kernel.AttemptAuthority
		project   kernel.ProjectID
		task      kernel.TaskID
		run       kernel.Run
		allowed   bool
	}{
		{name: "worker reads own exact run", authority: worker, project: project, task: workerTask, run: target(project, workerTask, workerRunID, kernel.RoleWorker), allowed: true},
		{name: "overseer reads worker in own project", authority: overseer, project: project, task: workerTask, run: target(project, workerTask, workerRunID, kernel.RoleWorker), allowed: true},
		{name: "worker cannot read sibling run", authority: worker, project: project, task: siblingTask, run: target(project, siblingTask, siblingRunID, kernel.RoleWorker)},
		{name: "overseer cannot read overseer run", authority: overseer, project: project, task: siblingTask, run: target(project, siblingTask, siblingRunID, kernel.RoleOrchestrator)},
		{name: "overseer cannot cross project", authority: overseer, project: foreignProject, task: siblingTask, run: target(foreignProject, siblingTask, siblingRunID, kernel.RoleWorker)},
		{name: "worker cannot use stale run identity", authority: worker, project: project, task: workerTask, run: target(project, workerTask, siblingRunID, kernel.RoleWorker)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := terminalObservationTargetAllowed(test.authority, test.project, test.task, test.run); got != test.allowed {
				t.Fatalf("terminal observation authorization = %v, want %v", got, test.allowed)
			}
		})
	}
}
