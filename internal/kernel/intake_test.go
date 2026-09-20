package kernel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func intakeSourceForTest(t *testing.T, policy IntakePolicy) IntakeSource {
	t.Helper()
	id, err := IntakeSourceIDFromBytes(bytes.Repeat([]byte{220}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	return IntakeSource{
		ID: id, GitHubRepositoryID: 42, GitHubRepositoryName: "owner/repository",
		ProjectID: projectID(t, 1), TargetRepositoryID: repositoryID(t, 2),
		LabelFilter: "factory:ready", Enabled: true, Policy: policy,
		TrustedGitHubLogins: []string{"Maintainer"}, PollSeconds: 60, AdmissionLimit: 25,
		Revision: mustRevision(t, 1), CreatedAt: mustTime(t, 1), UpdatedAt: mustTime(t, 1),
	}
}

func intakeSnapshotForTest() IntakeIssueSnapshot {
	return IntakeIssueSnapshot{GitHubRepositoryID: 42, IssueNumber: 7, NodeID: "I_kwDOExample", Title: "Improve intake", Body: "Exact reviewed body", AuthorLogin: "maintainer", AuthorType: GitHubAuthorUser}
}

func TestPreviewIntakeRequiresReviewAfterAnyContentEdit(t *testing.T) {
	source := intakeSourceForTest(t, IntakePolicyTrustedAuthors)
	snapshot := intakeSnapshotForTest()
	if got := PreviewIntake(source, snapshot, nil); got != IntakeEligibleTrusted {
		t.Fatalf("first trusted snapshot = %q", got)
	}
	acceptanceID, taskID, incarnationID, err := intakeAcceptanceIDs(snapshot, source.ProjectID, source.TargetRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	accepted := IntakeAcceptance{ID: acceptanceID, Snapshot: snapshot, BodyHash: snapshot.BodyHash(), ProjectID: source.ProjectID, RepositoryID: source.TargetRepositoryID, TaskID: taskID, IncarnationID: incarnationID, CreatedAt: mustTime(t, 2)}
	if got := PreviewIntake(source, snapshot, &accepted); got != IntakeAlreadyAccepted {
		t.Fatalf("unchanged accepted snapshot = %q", got)
	}
	withdrawnAt := mustTime(t, 3)
	accepted.WithdrawnAt = &withdrawnAt
	if got := PreviewIntake(source, snapshot, &accepted); got != IntakeWithdrawn {
		t.Fatalf("withdrawn accepted snapshot = %q", got)
	}
	accepted.WithdrawnAt = nil
	edited := snapshot
	edited.Body = "Exact reviewed body, edited"
	if got := PreviewIntake(source, edited, &accepted); got != IntakeContentChanged {
		t.Fatalf("trusted author edit = %q, want content change", got)
	}
	retitled := snapshot
	retitled.Title = "Retitled intake"
	if got := PreviewIntake(source, retitled, &accepted); got != IntakeContentChanged {
		t.Fatalf("trusted author title edit = %q, want content change", got)
	}
	manual := intakeSourceForTest(t, IntakePolicyManual)
	if got := PreviewIntake(manual, snapshot, nil); got != IntakeNeedsManualAcceptance {
		t.Fatalf("manual first snapshot = %q", got)
	}
	untrusted := snapshot
	untrusted.AuthorLogin = "other"
	if got := PreviewIntake(source, untrusted, nil); got != IntakeUntrustedAuthor {
		t.Fatalf("untrusted first snapshot = %q", got)
	}
	bot := snapshot
	bot.AuthorType = GitHubAuthorBot
	if got := PreviewIntake(source, bot, nil); got != IntakeUntrustedAuthor {
		t.Fatalf("trusted-login bot snapshot = %q", got)
	}
	disabled := source
	disabled.Enabled = false
	if got := PreviewIntake(disabled, snapshot, nil); got != IntakeSourceDisabled {
		t.Fatalf("disabled source = %q", got)
	}
}

func TestPreviewIntakeMarksOversizedContentUnsupported(t *testing.T) {
	source := intakeSourceForTest(t, IntakePolicyManual)
	snapshot := intakeSnapshotForTest()
	snapshot.Body = string(make([]byte, maxIntakeBodyBytes+1))
	if got := PreviewIntake(source, snapshot, nil); got != IntakeContentTooLarge {
		t.Fatalf("oversized snapshot = %q", got)
	}
}

func TestIntakeAcceptanceIDsDoNotContainSourceConfigurationIdentity(t *testing.T) {
	snapshot := intakeSnapshotForTest()
	project := projectID(t, 1)
	repository := repositoryID(t, 2)
	firstAcceptance, firstTask, firstIncarnation, err := intakeAcceptanceIDs(snapshot, project, repository)
	if err != nil {
		t.Fatal(err)
	}
	secondAcceptance, secondTask, secondIncarnation, err := intakeAcceptanceIDs(snapshot, project, repository)
	if err != nil {
		t.Fatal(err)
	}
	if firstAcceptance != secondAcceptance || firstTask != secondTask || firstIncarnation != secondIncarnation {
		t.Fatal("overlapping sources would create different work identities")
	}
	otherRepository := repositoryID(t, 3)
	_, otherTask, _, err := intakeAcceptanceIDs(snapshot, project, otherRepository)
	if err != nil {
		t.Fatal(err)
	}
	if otherTask == firstTask {
		t.Fatal("destination repository was not bound into task identity")
	}
}

func TestIntakeContentHashIsOnlyReviewedTitleAndBody(t *testing.T) {
	snapshot := intakeSnapshotForTest()
	original := snapshot.ContentHash()
	snapshot.AuthorLogin = "other"
	if snapshot.ContentHash() != original {
		t.Fatal("author entered reviewed content hash")
	}
	snapshot.Title = "Retitled"
	if snapshot.ContentHash() == original {
		t.Fatal("title did not enter reviewed content hash")
	}
}

func TestIntakeSourceValidationRejectsUnstableConfiguration(t *testing.T) {
	for name, mutate := range map[string]func(*IntakeSource){
		"empty trusted set": func(source *IntakeSource) { source.TrustedGitHubLogins = nil },
		"duplicate trusted login": func(source *IntakeSource) {
			source.TrustedGitHubLogins = append(source.TrustedGitHubLogins, "maintainer")
		},
		"unstable repository id": func(source *IntakeSource) { source.GitHubRepositoryID = 0 },
		"default target missing": func(source *IntakeSource) { source.TargetRepositoryID = RepositoryID{} },
	} {
		t.Run(name, func(t *testing.T) {
			source := intakeSourceForTest(t, IntakePolicyTrustedAuthors)
			mutate(&source)
			if ValidIntakeSource(source) {
				t.Fatal("accepted invalid source")
			}
		})
	}
}

func TestIntakeSourceTrustedAuthorLimit(t *testing.T) {
	source := intakeSourceForTest(t, IntakePolicyTrustedAuthors)
	source.TrustedGitHubLogins = make([]string, maxTrustedGitHubLogins)
	for index := range source.TrustedGitHubLogins {
		source.TrustedGitHubLogins[index] = fmt.Sprintf("reviewer%02d", index)
	}
	if !ValidIntakeSource(source) {
		t.Fatal("trusted-author limit rejected")
	}
	source.TrustedGitHubLogins = append(source.TrustedGitHubLogins, "reviewer26")
	if ValidIntakeSource(source) {
		t.Fatal("trusted-author overflow accepted")
	}
}

func TestCreateIntakeSourceCapsGlobalSourcesAfterExactReplay(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	defer store.Close()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 247), Name: "source cap", Root: "/source-cap"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	specification := func(index int) NewIntakeSource {
		raw := make([]byte, IDBytes)
		raw[0], raw[1] = byte(index+1), byte((index+1)>>8)
		id, err := IntakeSourceIDFromBytes(raw)
		if err != nil {
			t.Fatal(err)
		}
		return NewIntakeSource{ID: id, GitHubRepositoryID: 42, GitHubRepositoryName: "owner/repository", ProjectID: project.ID, TargetRepositoryID: RepositoryID(project.ID), LabelFilter: fmt.Sprintf("source-%d", index), Policy: IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 25}
	}
	var last NewIntakeSource
	for index := 0; index < globalMaxIntakeSources; index++ {
		last = specification(index)
		if _, err := store.CreateIntakeSource(ctx, last, mustTime(t, int64(index+2))); err != nil {
			t.Fatalf("source %d: %v", index+1, err)
		}
	}
	replay, err := store.CreateIntakeSource(ctx, last, mustTime(t, 1000))
	if err != nil || replay.ID != last.ID {
		t.Fatalf("200th source replay = %+v, %v", replay, err)
	}
	if _, err := store.CreateIntakeSource(ctx, specification(globalMaxIntakeSources), mustTime(t, 1001)); !errors.Is(err, ErrConflict) {
		t.Fatalf("201st source = %v, want conflict", err)
	}
	sources, err := store.IntakeSources(ctx)
	if err != nil || len(sources) != globalMaxIntakeSources {
		t.Fatalf("source count = %d, %v", len(sources), err)
	}
}

func TestAcceptedIntakeImportsOnceAcrossOverlappingSourcesAndWithdrawal(t *testing.T) {
	for _, linear := range []bool{false, true} {
		t.Run(fmt.Sprint("linear=", linear), func(t *testing.T) {
			ctx := context.Background()
			store, _ := newTestStore(t)
			defer store.Close()
			project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 230), Name: "intake", Root: "/intake"}, mustTime(t, 1))
			if err != nil {
				t.Fatal(err)
			}
			newSource := func(seed byte, label string) NewIntakeSource {
				id, err := IntakeSourceIDFromBytes(bytes.Repeat([]byte{seed}, IDBytes))
				if err != nil {
					t.Fatal(err)
				}
				spec := NewIntakeSource{ID: id, GitHubRepositoryID: 42, GitHubRepositoryName: "owner/repository", ProjectID: project.ID, TargetRepositoryID: RepositoryID(project.ID), LabelFilter: label, Policy: IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 25}
				if linear {
					spec.GitHubRepositoryID = 0
					spec.LinearTeamID = "11111111-1111-4111-8111-111111111111"
					spec.GitHubRepositoryName = "Engineering"
				}
				return spec
			}
			first, err := store.CreateIntakeSource(ctx, newSource(231, "ready"), mustTime(t, 2))
			if err != nil {
				t.Fatal(err)
			}
			first, err = store.SetIntakeSourceEnabled(ctx, first.ID, first.Revision, true, mustTime(t, 3))
			if err != nil {
				t.Fatal(err)
			}
			updated := newSource(231, "reviewed")
			first, err = store.UpdateIntakeSource(ctx, first.ID, first.Revision, updated, true, mustTime(t, 4))
			if err != nil || first.LabelFilter != "reviewed" || !first.Enabled || first.Revision.Int64() != 2 {
				t.Fatalf("updated source = %+v, %v", first, err)
			}
			configured, err := store.ProjectIntakeSources(ctx, project.ID)
			if err != nil || len(configured) != 1 || configured[0].ID != first.ID {
				t.Fatalf("project source list = %+v, %v", configured, err)
			}
			second, err := store.CreateIntakeSource(ctx, newSource(232, "triaged"), mustTime(t, 4))
			if err != nil {
				t.Fatal(err)
			}
			snapshot := intakeSnapshotForTest()
			if linear {
				snapshot.GitHubRepositoryID = 0
				snapshot.LinearTeamID = first.LinearTeamID
				snapshot.NodeID = "22222222-2222-4222-8222-222222222222"
				snapshot.URL = "https://linear.app/acme/issue/ENG-7/intake"
			}
			accepted, err := store.AcceptIntakeSnapshot(ctx, first.ID, snapshot, mustTime(t, 4))
			if err != nil {
				t.Fatal(err)
			}
			overlap, err := store.AcceptIntakeSnapshot(ctx, second.ID, snapshot, mustTime(t, 5))
			if err != nil {
				t.Fatal(err)
			}
			if overlap.ID != accepted.ID || overlap.TaskID != accepted.TaskID {
				t.Fatal("overlapping sources created duplicate accepted work")
			}
			if _, err := store.WithdrawIntakeAcceptance(ctx, accepted.ID, mustTime(t, 6)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 7)); !errors.Is(err, ErrConflict) {
				t.Fatalf("withdrawn acceptance imported: %v", err)
			}
			edited := snapshot
			edited.Body = "explicitly accepted edit"
			accepted, err = store.AcceptIntakeSnapshot(ctx, first.ID, edited, mustTime(t, 8))
			if err != nil {
				t.Fatal(err)
			}
			first, err = store.SetIntakeSourceEnabled(ctx, first.ID, first.Revision, false, mustTime(t, 9))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.SetIntakeSourceEnabled(ctx, second.ID, second.Revision, false, mustTime(t, 9)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 10)); !errors.Is(err, ErrConflict) {
				t.Fatalf("paused source imported new task: %v", err)
			}
			first, err = store.SetIntakeSourceEnabled(ctx, first.ID, first.Revision, true, mustTime(t, 11))
			if err != nil {
				t.Fatal(err)
			}
			firstTask, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 12))
			if err != nil {
				t.Fatal(err)
			}
			retry, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 13))
			if err != nil {
				t.Fatal(err)
			}
			if retry.ID != firstTask.ID || retry.IncarnationID != firstTask.IncarnationID {
				t.Fatal("crash retry did not replay exact task")
			}
			terminal, err := store.UpdateTask(ctx, firstTask.ID, firstTask.Revision, TaskPatch{Cancel: true}, mustTime(t, 14))
			if err != nil {
				t.Fatal(err)
			}
			if terminal.Status != TaskCancelled {
				t.Fatalf("terminal task = %s", terminal.Status)
			}
			newest := edited
			newest.Body = "later accepted instructions"
			if _, err := store.AcceptIntakeSnapshot(ctx, first.ID, newest, mustTime(t, 14)); err != nil {
				t.Fatal(err)
			}
			replayTerminal, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 15))
			if err != nil || replayTerminal.Status != TaskCancelled || replayTerminal.Revision != terminal.Revision {
				t.Fatalf("terminal acceptance replay = %+v, %v", replayTerminal, err)
			}
			if got := PreviewIntake(first, edited, &accepted); got != IntakeAlreadyAccepted {
				t.Fatalf("accepted edit preview = %q", got)
			}
			if got := PreviewIntake(first, snapshot, &accepted); got != IntakeContentChanged {
				t.Fatalf("old content preview = %q", got)
			}
		})
	}
}

func TestPendingIntakeAcceptancesSkipsSupersededWithdrawnAndImportedReceipts(t *testing.T) {
	ctx := context.Background()
	store, _ := newTestStore(t)
	defer store.Close()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 240), Name: "pending-intake", Root: "/pending-intake"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	sourceID, err := IntakeSourceIDFromBytes(bytes.Repeat([]byte{241}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.CreateIntakeSource(ctx, NewIntakeSource{ID: sourceID, GitHubRepositoryID: 42, GitHubRepositoryName: "owner/repository", ProjectID: project.ID, TargetRepositoryID: RepositoryID(project.ID), Policy: IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 25}, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	source, err = store.SetIntakeSourceEnabled(ctx, source.ID, source.Revision, true, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	older := intakeSnapshotForTest()
	first, err := store.AcceptIntakeSnapshot(ctx, source.ID, older, mustTime(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	newerSnapshot := older
	newerSnapshot.Body = "newer reviewed body"
	for _, timestamp := range []int64{4, 3} {
		if _, err := store.AcceptIntakeSnapshot(ctx, source.ID, newerSnapshot, mustTime(t, timestamp)); !errors.Is(err, ErrRevisionConflict) {
			t.Fatalf("distinct acceptance at non-increasing time %d: %v", timestamp, err)
		}
	}
	if replay, err := store.AcceptIntakeSnapshot(ctx, source.ID, older, mustTime(t, 3)); err != nil || replay.ID != first.ID {
		t.Fatalf("exact acceptance retry with earlier time: %+v %v", replay, err)
	}
	newer, err := store.AcceptIntakeSnapshot(ctx, source.ID, newerSnapshot, mustTime(t, 5))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingIntakeAcceptances(ctx, source.ID, 25)
	if err != nil || len(pending) != 1 || pending[0].ID != newer.ID {
		t.Fatalf("pending newest receipt = %+v, %v", pending, err)
	}
	after, err := store.PendingIntakeAcceptancesAfter(ctx, source.ID, 1, newer.ID)
	if err != nil || len(after) != 0 {
		t.Fatalf("cursor repeated inspected receipt: %+v, %v", after, err)
	}
	wrapped, err := store.PendingIntakeAcceptancesAfter(ctx, source.ID, 1, IntakeAcceptanceID{})
	if err != nil || len(wrapped) != 1 || wrapped[0].ID != newer.ID {
		t.Fatalf("new sweep lost pending receipt: %+v, %v", wrapped, err)
	}
	if _, err := store.ImportIntakeAcceptance(ctx, first.ID, mustTime(t, 6), source); !errors.Is(err, ErrConflict) {
		t.Fatalf("superseded pending receipt imported: %v", err)
	}
	if _, err := store.WithdrawIntakeAcceptance(ctx, newer.ID, mustTime(t, 6)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIntakeAcceptance(ctx, first.ID, mustTime(t, 6)); !errors.Is(err, ErrConflict) {
		t.Fatalf("withdrawing latest revived older import: %v", err)
	}
	if _, found, err := store.Task(ctx, first.TaskID); err != nil || found {
		t.Fatalf("superseded receipt created a task: found=%v err=%v", found, err)
	}
	pending, err = store.PendingIntakeAcceptances(ctx, source.ID, 25)
	if err != nil || len(pending) != 0 {
		t.Fatalf("withdrawn latest revived older receipt = %+v, %v", pending, err)
	}
	other := older
	other.IssueNumber = 8
	other.NodeID = "I_kwDOOther"
	accepted, err := store.AcceptIntakeSnapshot(ctx, source.ID, other, mustTime(t, 7))
	if err != nil {
		t.Fatal(err)
	}
	source, err = store.SetIntakeSourceEnabled(ctx, source.ID, source.Revision, false, mustTime(t, 8))
	if err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingIntakeAcceptances(ctx, source.ID, 25)
	if err != nil || len(pending) != 0 {
		t.Fatalf("paused source pending receipt = %+v, %v", pending, err)
	}
	source, err = store.SetIntakeSourceEnabled(ctx, source.ID, source.Revision, true, mustTime(t, 9))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ImportIntakeAcceptance(ctx, accepted.ID, mustTime(t, 10)); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingIntakeAcceptances(ctx, source.ID, 25)
	if err != nil || len(pending) != 0 {
		t.Fatalf("imported receipt remained pending = %+v, %v", pending, err)
	}
	if first.ID == newer.ID {
		t.Fatal("content edit did not create a distinct receipt")
	}
}

func TestTrustedIntakeSourcePersistsAndValidatesAuthorsAfterReopen(t *testing.T) {
	ctx := context.Background()
	store, path := newTestStore(t)
	defer store.Close()
	project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 245), Name: "trusted intake", Root: "/trusted-intake"}, mustTime(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	id, err := IntakeSourceIDFromBytes(bytes.Repeat([]byte{246}, IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	spec := NewIntakeSource{ID: id, GitHubRepositoryID: 42, GitHubRepositoryName: "owner/repository", ProjectID: project.ID, TargetRepositoryID: RepositoryID(project.ID), Policy: IntakePolicyTrustedAuthors, TrustedGitHubLogins: []string{"Maintainer"}, PollSeconds: 60, AdmissionLimit: 25}
	source, err := store.CreateIntakeSource(ctx, spec, mustTime(t, 2))
	if err != nil {
		t.Fatal(err)
	}
	if len(source.TrustedGitHubLogins) != 1 || source.TrustedGitHubLogins[0] != "maintainer" {
		t.Fatalf("trusted authors: %+v", source)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sources, err := store.ProjectIntakeSources(ctx, project.ID)
	if err != nil || len(sources) != 1 || sources[0].Policy != IntakePolicyTrustedAuthors || len(sources[0].TrustedGitHubLogins) != 1 {
		t.Fatalf("reopened trusted source: %+v %v", sources, err)
	}
	spec.TrustedGitHubLogins = []string{"reviewer"}
	source, err = store.UpdateIntakeSource(ctx, id, source.Revision, spec, false, mustTime(t, 3))
	if err != nil {
		t.Fatal(err)
	}
	if len(source.TrustedGitHubLogins) != 1 || source.TrustedGitHubLogins[0] != "reviewer" {
		t.Fatalf("changed authors: %+v", source)
	}
	corruptSQL(t, store, `DELETE FROM intake_source_trusted_logins WHERE source_id = ?`, id.Bytes())
	if _, _, err := store.IntakeSource(ctx, id); !errors.Is(err, ErrCorruptState) {
		t.Fatalf("missing trusted authors accepted: %v", err)
	}
}

func TestBotAuthorMetadataNeverGrantsAutomaticAcceptance(t *testing.T) {
	source := intakeSourceForTest(t, IntakePolicyTrustedAuthors)
	for _, login := range []string{"maintainer", "github-actions[bot]"} {
		snapshot := intakeSnapshotForTest()
		snapshot.AuthorType, snapshot.AuthorLogin = GitHubAuthorBot, login
		if !validIntakeIssueSnapshot(snapshot) || PreviewIntake(source, snapshot, nil) != IntakeUntrustedAuthor {
			t.Fatalf("bot metadata rejected or trusted: %q", login)
		}
		source.Policy = IntakePolicyManual
		if PreviewIntake(source, snapshot, nil) != IntakeNeedsManualAcceptance {
			t.Fatalf("bot cannot be reviewed manually: %q", login)
		}
		source.Policy = IntakePolicyTrustedAuthors
	}
	if validGitHubAuthor("github-actions[bot]", GitHubAuthorUser) || validGitHubLogin("github-actions[bot]") || validGitHubAuthor("app/owner", GitHubAuthorBot) {
		t.Fatal("bot spelling leaked into human authority or invalid metadata")
	}
}

func TestIntakePriorityRulesBoundEncodedOperatorConfiguration(t *testing.T) {
	source := intakeSourceForTest(t, IntakePolicyManual)
	source.PriorityByLabel = map[string]int64{}
	for i := 0; i < 25; i++ {
		source.PriorityByLabel[fmt.Sprint("label", i)] = 1
	}
	if !ValidIntakeSource(source) {
		t.Fatal("supported priority mapping rejected")
	}
	source.PriorityByLabel["overflow"] = 1
	if ValidIntakeSource(source) {
		t.Fatal("26 priority rules accepted")
	}
	source.PriorityByLabel = map[string]int64{}
	for i := 0; i < 4; i++ {
		source.PriorityByLabel[strings.Repeat("\x01", 99)+fmt.Sprint(i)] = 1
	}
	if ValidIntakeSource(source) {
		t.Fatal("escaped priority JSON exceeds the private reply ceiling")
	}
}

func TestIntakeReacceptsRestoredContentWithoutDuplicatingWork(t *testing.T) {
	for _, imported := range []bool{false, true} {
		t.Run(fmt.Sprint(imported), func(t *testing.T) {
			ctx := context.Background()
			store, path := newTestStore(t)
			project, err := store.CreateProject(ctx, NewProject{ID: projectID(t, 240), Name: "restored", Root: "/restored"}, mustTime(t, 1))
			if err != nil {
				t.Fatal(err)
			}
			spec := intakeSourceForTest(t, IntakePolicyManual)
			source, err := store.CreateIntakeSource(ctx, NewIntakeSource{ID: spec.ID, GitHubRepositoryID: 42, GitHubRepositoryName: "owner/repository", ProjectID: project.ID, TargetRepositoryID: RepositoryID(project.ID), Policy: IntakePolicyManual, PollSeconds: 60, AdmissionLimit: 25}, mustTime(t, 2))
			if err != nil {
				t.Fatal(err)
			}
			source, err = store.SetIntakeSourceEnabled(ctx, source.ID, source.Revision, true, mustTime(t, 3))
			if err != nil {
				t.Fatal(err)
			}
			snapshot := intakeSnapshotForTest()
			first, err := store.AcceptIntakeSnapshot(ctx, source.ID, snapshot, mustTime(t, 4))
			if err != nil {
				t.Fatal(err)
			}
			if imported {
				if _, err := store.ImportIntakeAcceptance(ctx, first.ID, mustTime(t, 5)); err != nil {
					t.Fatal(err)
				}
			}
			changed := snapshot
			changed.Body = "Different reviewed instructions"
			second, err := store.AcceptIntakeSnapshot(ctx, source.ID, changed, mustTime(t, 6))
			if err != nil {
				t.Fatal(err)
			}
			// Restoring A is a new explicit review, not a rewrite of its receipt.
			restored, err := store.AcceptIntakeSnapshot(ctx, source.ID, snapshot, mustTime(t, 7))
			if err != nil || restored.ID != first.ID || restored.TaskID != first.TaskID || restored.CreatedAt != first.CreatedAt {
				t.Fatalf("restored receipt: %+v %v", restored, err)
			}
			if retry, err := store.AcceptIntakeSnapshot(ctx, source.ID, snapshot, mustTime(t, 5)); err != nil || retry.ID != first.ID {
				t.Fatalf("retry: %+v %v", retry, err)
			}
			if _, err := store.AcceptIntakeSnapshot(ctx, source.ID, changed, mustTime(t, 7)); !errors.Is(err, ErrRevisionConflict) {
				t.Fatalf("nonincreasing review: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			latest, found, err := store.LatestIntakeAcceptance(ctx, snapshot, project.ID, source.TargetRepositoryID)
			if err != nil || !found || latest.ID != first.ID {
				t.Fatalf("latest after restart: %+v %v", latest, err)
			}
			pending, err := store.PendingIntakeAcceptances(ctx, source.ID, 25)
			expected := 1
			if imported {
				expected = 0
			}
			if err != nil || len(pending) != expected || expected == 1 && pending[0].ID != first.ID {
				t.Fatalf("pending: %+v %v", pending, err)
			}
			if _, err := store.ImportIntakeAcceptance(ctx, second.ID, mustTime(t, 8)); !errors.Is(err, ErrConflict) {
				t.Fatalf("superseded B imported: %v", err)
			}
			task, err := store.ImportIntakeAcceptance(ctx, first.ID, mustTime(t, 8))
			if err != nil || task.ID != first.TaskID {
				t.Fatalf("A task: %+v %v", task, err)
			}
			var tasks, reviews int
			if err := store.readers.QueryRow(`SELECT (SELECT COUNT(*) FROM tasks), (SELECT COUNT(*) FROM intake_acceptance_reviews)`).Scan(&tasks, &reviews); err != nil || tasks != 1 || reviews != 1 {
				t.Fatalf("dedup: tasks=%d reviews=%d err=%v", tasks, reviews, err)
			}
			if _, err := store.WithdrawIntakeAcceptance(ctx, first.ID, mustTime(t, 9)); err != nil {
				t.Fatal(err)
			}
			if _, err := store.AcceptIntakeSnapshot(ctx, source.ID, changed, mustTime(t, 10)); err != nil {
				t.Fatal(err)
			}
			if withdrawn, err := store.AcceptIntakeSnapshot(ctx, source.ID, snapshot, mustTime(t, 11)); err != nil || withdrawn.WithdrawnAt == nil {
				t.Fatalf("withdrawal revived: %+v %v", withdrawn, err)
			}
			latest, _, err = store.LatestIntakeAcceptance(ctx, snapshot, project.ID, source.TargetRepositoryID)
			if err != nil || latest.ID != second.ID {
				t.Fatalf("withdrawn review promoted: %+v %v", latest, err)
			}
		})
	}
}
