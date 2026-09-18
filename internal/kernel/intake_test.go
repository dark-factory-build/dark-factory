package kernel

import (
	"bytes"
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
			if validIntakeSource(source) {
				t.Fatal("accepted invalid source")
			}
		})
	}
}
