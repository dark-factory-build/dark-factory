package kernel

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestFailureSignatureIgnoresIdentifiersAndNumbers(t *testing.T) {
	exit := sql.NullInt64{Int64: 70, Valid: true}
	a := failureSignature("failed", "provider_exit", exit, sql.NullInt64{}, exit, "provider exited before an attempt outcome")
	b := failureSignature("failed", "provider_exit", exit, sql.NullInt64{}, exit, "provider exited before an attempt outcome")
	if a != b || a != "failed/provider_exit/70/-/70 provider exited before an attempt outcome" {
		t.Fatalf("signature = %q / %q", a, b)
	}
	first := failureSignature("blocked", "", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, "task 6717e53f321acc7b request 42 failed twice: local API transport failed")
	second := failureSignature("blocked", "", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, "task 9c3ec56bee6513d5 request 7 failed twice: other words")
	if first != second {
		t.Fatalf("same cause, different ids: %q vs %q", first, second)
	}
	if other := failureSignature("failed", "provider_exit", sql.NullInt64{Int64: 1, Valid: true}, sql.NullInt64{}, sql.NullInt64{}, "provider exited before an attempt outcome"); other == a {
		t.Fatal("a different exit code shared a signature")
	}
}

func TestFailureSignatureKeepsNodeAndTAPTestsDistinct(t *testing.T) {
	node := failureSignature("check", "main", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, "Failing step: Run tests: ✖ renders board")
	tap := failureSignature("check", "main", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, "Failing step: Run tests: not ok 3 - console loads")
	if node == tap || !strings.Contains(node, "test renders") || !strings.Contains(tap, "test console") {
		t.Fatalf("signatures = %q / %q", node, tap)
	}
	first := failureSignature("check", "merge_group", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, "Failing step: Run tests: ✖ TestQueue1")
	second := failureSignature("check", "merge_group", sql.NullInt64{}, sql.NullInt64{}, sql.NullInt64{}, "Failing step: Run tests: ✖ TestQueue2")
	if first == second || !strings.Contains(first, "test testqueue1") || !strings.Contains(second, "test testqueue2") {
		t.Fatalf("numeric signatures = %q / %q", first, second)
	}
}

// One failure is enough: the first run of a signature is reported as new, an
// unscoped report covers every project, and a window after it reports nothing.
func TestReportListsFailureSignaturesFromTheFirstOccurrence(t *testing.T) {
	ctx := context.Background()
	proposal, _ := NewBlockedProposal("missing prerequisite: the exact task")
	store, finalizing := finalizingReleasedRunAfter(t, RoleWorker, proposal, nil)
	defer store.Close()
	change, _, err := store.Change(ctx, *finalizing.ChangeID)
	settlement, settleErr := NewRetainedChangeSettlement(change.Revision, change.HeadCommit)
	if err = errors.Join(err, settleErr); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeWorkerRun(ctx, finalizing.ID, finalizing.Revision, settlement, mustTime(t, 80)); err != nil {
		t.Fatal(err)
	}
	report, err := store.Report(ctx, nil, 0, 3600000)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(report, "\n")
	if !strings.Contains(joined, "runs=1 ") || !strings.Contains(joined, "failure new x1 ") || !strings.Contains(joined, "blocked/") || !strings.Contains(joined, " missing prerequisite: ") {
		t.Fatalf("unscoped report = %q", report)
	}
	if !strings.HasPrefix(report[len(report)-1], "wal_bytes=") {
		t.Fatalf("report does not end with its WAL line: %q", report)
	}
	later, err := store.Report(ctx, nil, mustTime(t, 81).Int64()+3600000, 3600000)
	if err != nil || strings.Contains(strings.Join(later, "\n"), "failure ") {
		t.Fatalf("report after the failure = %q, %v", later, err)
	}
}

// A specialist woken by failures hears of a signature the first time it
// occurs, and not again once it was seen before its last review.
func TestSpecialistFailureEventsAreNewSignaturesOnly(t *testing.T) {
	ctx := context.Background()
	proposal, _ := NewBlockedProposal("missing prerequisite: the exact task")
	store, finalizing := finalizingReleasedRunAfter(t, RoleWorker, proposal, nil)
	defer store.Close()
	change, _, err := store.Change(ctx, *finalizing.ChangeID)
	settlement, settleErr := NewRetainedChangeSettlement(change.Revision, change.HeadCommit)
	if err = errors.Join(err, settleErr); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeWorkerRun(ctx, finalizing.ID, finalizing.Revision, settlement, mustTime(t, 80)); err != nil {
		t.Fatal(err)
	}
	connection, err := store.readerConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	agent := Agent{ProjectID: finalizing.ProjectID}
	agent.Idle.WakeOn = "failures"
	events, err := specialistEvents(ctx, connection, agent, 0)
	if err != nil || len(events) != 1 || !strings.HasPrefix(events[0], "- new failure x1 blocked/") || !strings.Contains(events[0], " missing prerequisite: ") {
		t.Fatalf("events = %q, %v", events, err)
	}
	if events, err := specialistEvents(ctx, connection, agent, mustTime(t, 81).Int64()); err != nil || len(events) != 0 {
		t.Fatalf("events after the failure = %q, %v", events, err)
	}
}
