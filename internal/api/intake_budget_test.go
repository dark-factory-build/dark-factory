package api

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestIntakeConfigurationListFitsPrivateReplyBudget(t *testing.T) {
	source := IntakeSource{ID: strings.Repeat("a", 32), ProjectID: strings.Repeat("b", 32), GitHubRepositoryID: math.MaxInt64, Revision: math.MaxUint64, Enabled: true, Repository: strings.Repeat("a", 39) + "/" + strings.Repeat("r", 100), TargetRepositoryID: strings.Repeat("c", 32), OverseerAgentID: strings.Repeat("d", 32), Label: strings.Repeat("\x01", 100), Policy: "trusted_authors", PollSeconds: 86400, AdmissionLimit: 200, PriorityDefault: -1000000, PriorityByLabel: map[string]int64{}, Sync: &IntakeSync{LastAttemptAt: math.MaxInt64, LastSuccessAt: math.MaxInt64, ImportedTasks: 200, State: "unavailable", Error: "unavailable"}}
	for i := 0; i < 25; i++ {
		source.TrustedAuthors = append(source.TrustedAuthors, fmt.Sprintf("%039d", i))
		source.PriorityByLabel[strings.Repeat("\"", 30)+fmt.Sprintf("%02d", i)] = -1000000
	}
	rules, _ := json.Marshal(source.PriorityByLabel)
	// Add the remaining allowed bytes explicitly: this upper bound is stronger
	// than any particular label encoding or allowed distribution of 25 rules.
	padding := 2048 - len(rules)
	if padding < 0 {
		t.Fatal("fixture exceeds priority JSON ceiling")
	}
	result := IntakeResult{State: "ok", Sources: make([]IntakeSource, 200)}
	for i := range result.Sources {
		result.Sources[i] = source
	}
	data, err := json.Marshal(result)
	if err != nil || len(data)+200*padding >= 1<<20 {
		t.Fatalf("maximum list exceeds reply budget: %d %v", len(data)+200*padding, err)
	}
}
