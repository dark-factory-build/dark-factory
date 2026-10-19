package daemon

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// A stall after output fails the task. A stall with no output at all means the
// run never started: its task is queued again once, and a second such stall
// fails it.
func TestRunLivenessStopsStalledProvider(t *testing.T) {
	fixture := newSupervisorFixture(t, "set -eu\nprintf x >> __WITNESS__\nsleep 30\n")
	run := stallNextRun(t, fixture, true)
	if run.Proposal.Code() != kernel.FailureProtocol || !strings.HasPrefix(run.Proposal.Detail(), stalledRunDetail) {
		t.Fatalf("stalled run = %+v", run)
	}
	fixture.assertReleased(t, run)
	assertTaskStatus(t, fixture, kernel.TaskFailed)
}

func TestRunLivenessRequeuesRunThatNeverStarted(t *testing.T) {
	fixture := newSupervisorFixture(t, "set -eu\nprintf x >> __WITNESS__\nsleep 30\n")
	for _, want := range []kernel.TaskStatus{kernel.TaskQueued, kernel.TaskFailed} {
		if run := stallNextRun(t, fixture, false); run.Proposal.Code() != kernel.FailureTransient || run.Proposal.Detail() != kernel.NeverStartedRunDetail {
			t.Fatalf("silent stalled run = %+v", run)
		}
		assertTaskStatus(t, fixture, want)
	}
}

func assertTaskStatus(t *testing.T, fixture *supervisorFixture, want kernel.TaskStatus) {
	t.Helper()
	task, found, err := fixture.store.Task(context.Background(), fixture.taskID)
	if err != nil || !found || task.Status != want {
		t.Fatalf("stalled task = %+v, want %v, found=%v, err=%v", task, want, found, err)
	}
}

// stallNextRun admits the next run, waits for its provider to start (a shell
// program through its witness, credited with terminal output if output is set;
// a fixture without a witness through its complete first output line), then
// fails it as stalled.
func stallNextRun(t *testing.T, fixture *supervisorFixture, output bool) kernel.Run {
	t.Helper()
	var skew atomic.Int64
	fixture.daemon.livenessClock = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	done := make(chan struct {
		run kernel.Run
		err error
	}, 1)
	go func() {
		run, err := fixture.daemon.RunNext(context.Background(), fixture.spec)
		done <- struct {
			run kernel.Run
			err error
		}{run, err}
	}()
	if fixture.witness != "" {
		if err := waitForWitness(fixture.witness, 10*time.Second); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(fixture.witness); err != nil {
			t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		started := false
		fixture.daemon.attemptMu.Lock()
		for _, attempt := range fixture.daemon.attempts {
			attempt.livenessMu.Lock()
			started = !attempt.startedAt.IsZero()
			attempt.livenessMu.Unlock()
			if fixture.witness == "" {
				_, _, retained := attempt.diagnosticSnapshot()
				started = bytes.HasSuffix(retained, []byte("\n"))
			}
			if started && output {
				attempt.markTerminalOutput(time.Now(), 1)
			}
		}
		fixture.daemon.attemptMu.Unlock()
		if started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("live attempt never started")
		}
	}
	skew.Store(int64(stalledRunLivenessThreshold))
	if err := fixture.daemon.enforceRunLiveness(context.Background(), SupervisorSpec{}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		run kernel.Run
		err error
	}
	select {
	case result = <-done:
	case <-time.After(12 * time.Second):
		t.Fatal("stall did not stop the provider")
	}
	if result.err != nil || result.run.Phase != kernel.RunTerminal || result.run.Proposal == nil {
		t.Fatalf("stalled run = %+v, err=%v", result.run, result.err)
	}
	return result.run
}

// Codex reports an exhausted account or a model at capacity in its screen and
// then idles at its prompt, so only the stall reads the notice, from the whole
// raw retained output of a Codex run, even one that made no attempt call, and
// only as Codex styles it: glyph in a cell of its own, apart from its message
// (in a non-adjacent cell since Codex 0.163). A task, prompt or tool text that
// quotes the words, or the whole notice with its glyph, plain or in one style,
// stalls as before, as does any output of a Claude run; and an escape-heavy raw
// tail still yields its useful text. Fixtures are split so this source carries
// no notice.
func TestStalledRunProposalReadsCodexNotices(t *testing.T) {
	capacity := func(gap string) string {
		return "\x1b[33m⚠\x1b[39m" + gap + "\x1b[1mSelected model" + " is at capacity. Please try a different model.\x1b[22m\r\n"
	}
	limit := []string{"\x1b[31m■\x1b[39m \x1b[1mYou've hit your us", "age limit. Upgrade to Pro or try again at Sep 25th, 3:00 PM.\x1b[39m\r\n\x1b[1mApproaching rate limits\x1b[0m"}
	quoted := "issue: the terminal shows `Selected model" + " is at capacity` or `You've hit your" + " usage limit`\r\n"
	// The whole notices, glyphs included, as a task cell draws them: plain, and
	// in the cell's one style with the glyph first on its line.
	echoed := "issue: the terminal shows ⚠ Selected model" + " is at capacity and ■ You've hit your" + " usage limit. Upgrade to Pro\r\n"
	styledEcho := "\x1b[2m\x1b[36m⚠ Selected model" + " is at capacity. Please try a different model.\r\n■ You've hit your" + " usage limit. Upgrade to Pro\x1b[0m\r\n"
	for _, test := range []struct {
		name             string
		provider         kernel.Provider
		neverStarted     bool
		frames           []string
		code             kernel.FailureCode
		prefix, contains string
	}{
		{"codex capacity", kernel.ProviderCodex, false, []string{quoted, capacity(" ")}, kernel.FailureTransient, kernel.ProviderCapacityRunDetail, ""},
		{"codex capacity cells apart", kernel.ProviderCodex, false, []string{capacity("\x1b[12;3H")}, kernel.FailureTransient, kernel.ProviderCapacityRunDetail, ""},
		{"codex capacity wrapped", kernel.ProviderCodex, false, []string{capacity("\r\n  ")}, kernel.FailureTransient, kernel.ProviderCapacityRunDetail, ""},
		{"codex capacity before any attempt call", kernel.ProviderCodex, true, []string{capacity(" ")}, kernel.FailureTransient, kernel.ProviderCapacityRunDetail, ""},
		{"codex usage limit split frames", kernel.ProviderCodex, false, append([]string{quoted}, limit...), kernel.FailureProviderExit, "provider usage limit", "You've hit your" + " usage limit. Upgrade to Pro or try again at Sep 25th, 3:00 PM."},
		{"codex echo", kernel.ProviderCodex, false, []string{quoted}, kernel.FailureProtocol, stalledRunDetail, "Selected model" + " is at capacity"},
		{"codex echo of the whole notices", kernel.ProviderCodex, false, []string{echoed}, kernel.FailureProtocol, stalledRunDetail, "⚠ Selected model" + " is at capacity and ■ You've hit your"},
		{"codex styled echo of the whole notices", kernel.ProviderCodex, false, []string{styledEcho}, kernel.FailureProtocol, stalledRunDetail, "model. ■ You've hit your"},
		{"codex echo before any attempt call", kernel.ProviderCodex, true, []string{echoed}, kernel.FailureTransient, kernel.NeverStartedRunDetail, ""},
		{"claude notice", kernel.ProviderClaudeCode, false, append([]string{capacity(" ")}, limit...), kernel.FailureProtocol, stalledRunDetail, "Approaching rate limits"},
		{"claude echo", kernel.ProviderClaudeCode, false, []string{echoed}, kernel.FailureProtocol, stalledRunDetail, "You've hit your"},
		{"escape-heavy tail", kernel.ProviderClaudeCode, false, []string{"useful last line\r\n", strings.Repeat("\x1b[2K\x1b[1A", 100)}, kernel.FailureProtocol, stalledRunDetail + "useful last line", ""},
		{"adopted replay gap keeps the prompt line", kernel.ProviderShell, false, []string{"Login expired\r\n> "}, kernel.FailureProtocol, stalledRunDetail + "Login expired >", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			var attempt liveAttempt
			cursor := uint64(40) // retained from a replay floor, as after a takeover
			for _, frame := range test.frames {
				attempt.retainDiagnosticOutput(cursor, cursor+uint64(len(frame)), []byte(frame))
				cursor += uint64(len(frame))
			}
			_, _, output := attempt.diagnosticSnapshot()
			code, detail := stalledRunProposal(test.provider, test.neverStarted, output)
			if code != test.code || !strings.HasPrefix(detail, test.prefix) || !strings.Contains(detail, test.contains) {
				t.Fatalf("%v: %v %q", test.provider, code, strings.ReplaceAll(detail, " ", "_"))
			}
		})
	}
}

// Codex at model capacity idles at its prompt: the stall reads its notice, and
// the task is queued again once, failing on a second in a row.
func TestCodexModelCapacityRequeuesOnce(t *testing.T) {
	fixture := newSupervisorFixture(t, "unused shell task")
	if err := replaceSupervisorAgentLaunchControls(fixture.storePath, fixture.agentID, kernel.ProviderCodex, "", ""); err != nil {
		t.Fatal(err)
	}
	execSupervisorSQL(t, fixture.storePath, `UPDATE tasks SET body = ? WHERE id = ?`, "capacity", fixture.taskID.Bytes())
	tools := filepath.Join(fixture.root, "tools")
	if err := os.Mkdir(tools, 0o700); err != nil {
		t.Fatal(err)
	}
	copySupervisorExecutable(t, supervisorTestExecutable(t), filepath.Join(tools, "codex"))
	fixture.spec.ToolPath = tools + ":" + fixture.spec.ToolPath
	fixture.witness = "" // the Codex fixture's own notice line, not a witness, marks its start
	for _, want := range []kernel.TaskStatus{kernel.TaskQueued, kernel.TaskFailed} {
		run := stallNextRun(t, fixture, true)
		if run.Proposal.Code() != kernel.FailureTransient || run.Proposal.Detail() != kernel.ProviderCapacityRunDetail {
			t.Fatalf("capacity run = %v", run.Proposal)
		}
		fixture.assertReleased(t, run)
		assertTaskStatus(t, fixture, want)
	}
}
