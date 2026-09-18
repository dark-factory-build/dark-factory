//go:build darwin

package daemon

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	for _, secretKey := range []string{"token", "secret", "password"} {
		markerFree := []byte("{\"" + secretKey + "\":\n\"hunter2\"}\nnext\n")
		keyLineEnd := bytes.IndexByte(markerFree, '\n')
		for cursor := 1; cursor < keyLineEnd; cursor++ {
			got, _ := redactTerminalWindow(markerFree[cursor:], uint64(cursor), terminalLookbehind{start: 0, bytes: markerFree[:cursor]})
			if bytes.Contains(got, []byte("hunter2")) || !bytes.Contains(got, []byte("next")) {
				t.Fatalf("marker-free split JSON %s cursor=%d handling = %q", secretKey, cursor, got)
			}
		}
		if secretKey == "token" {
			// If replay begins above the key's opening byte, the ambiguous final
			// suffix is conservatively suppressed rather than exposed.
			if got, _ := redactTerminalWindow(markerFree[6:], 6); bytes.Contains(got, []byte("hunter2")) || !bytes.Contains(got, []byte("next")) {
				t.Fatalf("floor-loss ambiguous split JSON secret handling = %q", got)
			}
		}
	}
	multiline := []byte("ordinary one\nordinary two\n{\"token\":\n\"multiline-secret\"}\nnext\n")
	keyCursor := bytes.Index(multiline, []byte("token")) + 2
	got, _ = redactTerminalWindow(multiline[keyCursor:], uint64(keyCursor), terminalLookbehind{start: 0, bytes: multiline[:keyCursor]})
	if bytes.Contains(got, []byte("multiline-secret")) || !bytes.Contains(got, []byte("next")) {
		t.Fatalf("multiline lookbehind redaction = %q", got)
	}
	liveTail, omitted := redactTerminalWindow([]byte("ret\n"), 0, terminalLookbehind{start: 22, bytes: []byte("Authorization: Bearer sec")})
	if bytes.Contains(liveTail, []byte("ret")) || omitted != 0 || len(liveTail) != len("ret\n") {
		t.Fatalf("live lookbehind redaction = %q omitted=%d", liveTail, omitted)
	}
	for _, key := range []string{"message", "login", "version", "monkey"} {
		benign := []byte("{\"" + key + "\":\n\"visible\"}\n")
		keyLineEnd := bytes.IndexByte(benign, '\n')
		for cursor := 1; cursor < keyLineEnd; cursor++ {
			got, _ := redactTerminalWindow(benign[cursor:], uint64(cursor), terminalLookbehind{start: 0, bytes: benign[:cursor]})
			if !bytes.Contains(got, []byte("visible")) {
				t.Fatalf("cursor-boundary benign %s JSON was over-redacted at cursor %d: %q", key, cursor, got)
			}
		}
	}
}

func TestTerminalWindowRedactsEscapedJSONQuotesAcrossCursor(t *testing.T) {
	for _, line := range [][]byte{
		[]byte(`{"token":"prefix\"escaped-secret-suffix"}` + "\n"),
		[]byte(`{"cwd":"/Users/operator/quo\"ted/private"}` + "\n"),
	} {
		got, _ := redactTerminalWindow(line, 0)
		if bytes.Contains(got, []byte("escaped-secret-suffix")) || bytes.Contains(got, []byte("/Users/")) || bytes.Contains(got, []byte("ted/private")) {
			t.Fatalf("escaped JSON leaked: %q", got)
		}
		for cursor := 1; cursor < len(line); cursor++ {
			got, _ := redactTerminalWindow(line[cursor:], 0, terminalLookbehind{start: 0, bytes: line[:cursor]})
			if bytes.Contains(got, []byte("escaped-secret-suffix")) || bytes.Contains(got, []byte("/Users/")) || bytes.Contains(got, []byte("ted/private")) {
				t.Fatalf("cursor=%d escaped JSON leaked: %q", cursor, got)
			}
		}
	}
}

func TestTerminalTextProjectionNormalizesControlsBeforeRedaction(t *testing.T) {
	payload := []byte("\x1b[?25l\x1b[2J\x1b[HWaiting for operator approval\x1b[10;1H" +
		`{"token":"prefix\"esc` + "\x1b[3C" + `aped-secret` + "\r\n" +
		`{"cwd":"/Users/operator/quo\"ted/private` + "\x1b[2K" +
		"\x1b]0;/Users/operator/title\a")
	got := terminalTextProjection(payload, false, 65536)
	if !strings.Contains(got, "Waiting for operator approval") || strings.Contains(got, "aped-secret") || strings.Contains(got, "/Users/") {
		t.Fatalf("projection leaked or lost state: %q", got)
	}
	for _, value := range []byte(got) {
		if value < 0x20 || value == 0x7f || value == 0x1b {
			t.Fatalf("projection retained terminal control %#x in %q", value, got)
		}
	}
	if got := terminalTextProjection([]byte("cret-fragment\x1b[2Ccontinued\nvisible state\x1b["), true, 65536); got != "" {
		t.Fatalf("dropped prefix or incomplete escape projection = %q", got)
	}
	if got := terminalTextProjection([]byte("cret-fragment\x1b[2Ccontinued"), true, 65536); got != "" {
		t.Fatalf("unterminated dropped record exposed = %q", got)
	}
	for _, secret := range [][]byte{
		[]byte(`{"token":` + "\r\n\x1b[4C" + `"incomplete-secret`),
		[]byte(`{"cwd":"/Users/oper` + "\x1b[2C" + `ator/private`),
	} {
		if got := terminalTextProjection(secret, false, 65536); strings.Contains(got, "incomplete-secret") || strings.Contains(got, "/Users/") || strings.Contains(got, "ator/private") {
			t.Fatalf("incomplete sensitive projection leaked: %q", got)
		}
	}
	bounded := terminalTextProjection([]byte("old state\x1b[Hcurrent state"), false, 7)
	if bounded != "t state" || !utf8.ValidString(bounded) {
		t.Fatalf("bounded projection = %q", bounded)
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
		{"live credential continuation after attachment", active, 40, 65536, 40, false, false, false, true, "***\n"},
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
				context := []byte("Authorization: Bearer sec")
				writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalAttached), Correlation: attach.Correlation, Sequence: test.cursor, Head: test.head, Status: string(runner.TerminalResultOK), ContextStart: test.head - uint64(len(context)), Context: context})
				writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalOutput), Start: test.head, End: test.head + 4, Payload: []byte("ret\n")})
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
				if got.err != nil || got.value.Source != "live" || string(got.value.Payload) != test.want || got.value.Head != test.head+4 || got.value.NextCursor != test.head+4 || got.value.Gap {
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

func TestTerminalObservationSettledCursorUsesRetainedLookbehind(t *testing.T) {
	retained := []byte("{\"token\":\n\"hunter2\"}\nnext\n")
	floor, offset := uint64(100), uint64(1)
	got, _ := redactTerminalWindow(retained[offset:], floor+offset, terminalLookbehind{start: floor, bytes: retained[:offset]})
	if bytes.Contains(got, []byte("hunter2")) || !bytes.Contains(got, []byte("next")) {
		t.Fatalf("settled cursor redaction = %q", got)
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

func TestOperatorTerminalObservationReadsExactRunningWorkerAndOverseer(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttemptInProject(t, fixture, 11, testID(11), "worker")
	overseer := prepareActiveAttemptInProject(t, fixture, 41, testID(11), "orchestrator")
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
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
	data := []byte("worker output\nAuthorization: Bearer secret-value\n")
	read := func(target activeAttempt) api.TerminalObservation {
		t.Helper()
		input := api.TerminalObserveInput{ProjectID: active.run.ProjectID.String(), TaskID: target.run.TaskID.String(), RunID: target.run.ID.String(), MaxBytes: 65536}
		done := fixture.serve(t)
		result := make(chan api.TerminalObservation, 1)
		errs := make(chan error, 1)
		go func() {
			value, readErr := operator.TerminalObserve(ctx, input)
			result <- value
			errs <- readErr
		}()
		attach := readTerminalEffectWire(t, peer)
		for attach.Kind != string(runner.TerminalAttach) {
			attach = readTerminalEffectWire(t, peer)
		}
		writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalAttached), Correlation: attach.Correlation, Sequence: 0, Head: uint64(len(data)), Status: string(runner.TerminalResultOK)})
		writeTerminalEffectWire(t, peer, terminalEffectWireFrame{Version: 1, Kind: string(runner.TerminalOutput), Correlation: attach.Correlation, Start: 0, End: uint64(len(data)), Payload: data})
		value := <-result
		readErr := <-errs
		waitDispatch(t, done)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return value
	}
	worker := read(active)
	if !strings.Contains(string(worker.Payload), "worker output") || strings.Contains(string(worker.Payload), "secret-value") {
		t.Fatalf("worker observation leaked or lost output: %q", worker.Payload)
	}
	// The operator may target an exact running overseer too; this fixture has no
	// attached PTY for it, so the read may be unavailable but must pass identity
	// authorization rather than return forbidden.
	overseerInput := api.TerminalObserveInput{ProjectID: overseer.run.ProjectID.String(), TaskID: overseer.run.TaskID.String(), RunID: overseer.run.ID.String(), MaxBytes: 1024}
	done := fixture.serve(t)
	_, overseerErr := operator.TerminalObserve(ctx, overseerInput)
	waitDispatch(t, done)
	var overseerRemote *api.RemoteError
	if errors.As(overseerErr, &overseerRemote) && overseerRemote.Code() == api.RemoteForbidden {
		t.Fatalf("operator overseer identity rejected: %v", overseerErr)
	}
	for _, mutation := range []func(*api.TerminalObserveInput){
		func(value *api.TerminalObserveInput) { value.ProjectID = testID(90) },
		func(value *api.TerminalObserveInput) { value.TaskID = testID(90) },
		func(value *api.TerminalObserveInput) { value.RunID = testID(90) },
	} {
		input := api.TerminalObserveInput{ProjectID: active.run.ProjectID.String(), TaskID: active.run.TaskID.String(), RunID: active.run.ID.String(), MaxBytes: 1024}
		mutation(&input)
		done := fixture.serve(t)
		_, readErr := operator.TerminalObserve(ctx, input)
		waitDispatch(t, done)
		var remote *api.RemoteError
		if !errors.As(readErr, &remote) || remote.Code() != api.RemoteForbidden {
			t.Fatalf("operator identity mismatch = %v", readErr)
		}
	}
}

func TestOperatorTerminalObservationReadsSettledDiagnostics(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttemptInProject(t, fixture, 41, testID(11), "orchestrator")
	completeAdapterRun(t, fixture.store, active.run, "finished")
	ctx := context.Background()
	payload := []byte("finished output\nAuthorization: Bearer private-value\n")
	if err := fixture.store.SaveTerminalDiagnostics(ctx, kernel.TerminalDiagnostics{RunID: active.run.ID, Head: uint64(len(payload)), Payload: payload, CapturedAt: mustKernelTime(t, 2000)}); err != nil {
		t.Fatal(err)
	}
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	done := fixture.serve(t)
	observed, err := operator.TerminalObserve(ctx, api.TerminalObserveInput{ProjectID: active.run.ProjectID.String(), TaskID: active.run.TaskID.String(), RunID: active.run.ID.String(), MaxBytes: 1024})
	waitDispatch(t, done)
	if err != nil || observed.Source != "stored" || !bytes.Contains(observed.Payload, []byte("finished output")) || bytes.Contains(observed.Payload, []byte("private-value")) {
		t.Fatalf("settled observation = %+v, %v", observed, err)
	}
	done = fixture.serve(t)
	observed, err = operator.TerminalObserve(ctx, api.TerminalObserveInput{ProjectID: active.run.ProjectID.String(), TaskID: active.run.TaskID.String(), RunID: active.run.ID.String(), MaxBytes: 1024, Text: true})
	waitDispatch(t, done)
	if err != nil || !observed.TextMode || observed.Source != "stored" || !strings.Contains(observed.Text, "finished output") || strings.Contains(observed.Text, "private-value") {
		t.Fatalf("settled text observation = %+v, %v", observed, err)
	}
}

func TestOperatorTerminalTextReadsLiveRetainedSnapshotWithoutTerminalEffect(t *testing.T) {
	fixture := newDispatchFixture(t)
	active := prepareActiveAttemptInProject(t, fixture, 51, testID(11), "worker")
	session, found, err := fixture.store.TerminalSessionForRun(context.Background(), active.run.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	controller, peer := readyTerminalEffectController(t)
	live := newLiveAttempt(fixture.daemon, active.run.ID, session.ID, controller)
	output := []byte("\x1b[2J\x1b[HWaiting for operator input\x1b[3Ctoken=private-value")
	live.retainDiagnosticOutput(0, uint64(len(output)), output)
	if err := fixture.daemon.registerLiveAttempt(live); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		fixture.daemon.unregisterLiveAttempt(active.run.ID, live)
		_ = controller.Close()
		_ = peer.Close()
	})
	operator, err := api.NewOperatorClient(fixture.socket, fixture.operator)
	if err != nil {
		t.Fatal(err)
	}
	input := api.TerminalObserveInput{ProjectID: active.run.ProjectID.String(), TaskID: active.run.TaskID.String(), RunID: active.run.ID.String(), MaxBytes: 1024, Text: true}
	done := fixture.serve(t)
	observed, err := operator.TerminalObserve(context.Background(), input)
	waitDispatch(t, done)
	if err != nil || !observed.TextMode || observed.Source != "live" || !strings.Contains(observed.Text, "Waiting for operator input") || strings.Contains(observed.Text, "private-value") || len(observed.Payload) != 0 || observed.NextCursor != 0 {
		t.Fatalf("live text observation = %+v, %v", observed, err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var effect [1]byte
	if _, err := peer.Read(effect[:]); err == nil {
		t.Fatal("text observation emitted a terminal effect")
	}
	input.TaskID = testID(90)
	done = fixture.serve(t)
	_, err = operator.TerminalObserve(context.Background(), input)
	waitDispatch(t, done)
	var remote *api.RemoteError
	if !errors.As(err, &remote) || remote.Code() != api.RemoteForbidden {
		t.Fatalf("text observation identity mismatch = %v", err)
	}
}
