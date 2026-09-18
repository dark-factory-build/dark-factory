package api

import (
	"strings"
	"testing"
)

func TestLegacyPolicyNarrowingRequiresCommitAcknowledgement(t *testing.T) {
	value := LegacyIntakeInput{ConfigHash: strings.Repeat("a", 64), JournalHash: strings.Repeat("b", 64), PlanHash: strings.Repeat("c", 64), ManualAppAuthors: []string{"app/factory", "automation[bot]"}}
	if !validLegacyIntakeInput(value, false) || validLegacyIntakeInput(value, true) {
		t.Fatal("preview and unacknowledged commit were not distinguished")
	}
	value.AcknowledgePolicyNarrowing = true
	if !validLegacyIntakeInput(value, true) {
		t.Fatal("explicit acknowledgement refused")
	}
	value.ManualAppAuthors = []string{"ordinary-human"}
	if validLegacyIntakeInput(value, true) {
		t.Fatal("human silently relabelled as app author")
	}
}

func TestLegacyLineageRequiresFrozenRouteAndReadOnlyActionFields(t *testing.T) {
	id := strings.Repeat("a", 32)
	input := IntakeInput{Action: "legacy_lineage", SourceID: id, ProjectID: id, IssueNumber: 7, Configuration: &IntakeConfiguration{TargetRepositoryID: id}, Legacy: &LegacyIntakeInput{ConfigHash: strings.Repeat("a", 64), JournalHash: strings.Repeat("b", 64), PlanHash: strings.Repeat("c", 64)}}
	if !ValidIntakeInput(input) {
		t.Fatal("valid lineage refused")
	}
	input.ExpectedRevision = 1
	if ValidIntakeInput(input) {
		t.Fatal("mutation field accepted")
	}
	input.ExpectedRevision = 0
	input.Configuration.TargetRepositoryID = ""
	if ValidIntakeInput(input) {
		t.Fatal("unfrozen destination accepted")
	}
}
