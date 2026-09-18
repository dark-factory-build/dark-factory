package api

import (
	"encoding/hex"
	"strings"
)

// Legacy records are historical claims to verify, never accepted issue bytes.
type LegacyIntakeHistory struct {
	Number                uint64 `json:"number"`
	Kind                  string `json:"kind"`
	TaskID                string `json:"task_id,omitempty"`
	IncarnationID         string `json:"incarnation_id,omitempty"`
	Fingerprint           string `json:"fingerprint,omitempty"`
	HistoricalContentHash string `json:"historical_content_hash,omitempty"`
}

type LegacyIntakeInput struct {
	ManualAppAuthors           []string              `json:"manual_app_authors,omitempty"`
	AcknowledgePolicyNarrowing bool                  `json:"acknowledge_policy_narrowing,omitempty"`
	ConfigHash                 string                `json:"config_hash"`
	JournalHash                string                `json:"journal_hash"`
	PlanHash                   string                `json:"plan_hash,omitempty"`
	History                    []LegacyIntakeHistory `json:"history"`
}

type LegacyIntakeIssue struct {
	Priority      int64  `json:"priority"`
	Eligibility   string `json:"eligibility"`
	Number        uint64 `json:"number"`
	NodeID        string `json:"node_id"`
	ContentHash   string `json:"content_hash"`
	State         string `json:"state"`
	TaskID        string `json:"task_id,omitempty"`
	IncarnationID string `json:"incarnation_id,omitempty"`
	TaskRevision  uint64 `json:"task_revision,omitempty"`
}

type LegacyIntakePlan struct {
	TargetRepositoryID            string              `json:"target_repository_id,omitempty"`
	RequiresPolicyAcknowledgement bool                `json:"requires_policy_acknowledgement,omitempty"`
	PlanHash                      string              `json:"plan_hash"`
	GitHubRepositoryID            uint64              `json:"github_repository_id"`
	Issues                        []LegacyIntakeIssue `json:"issues"`
	PolicyChanges                 []string            `json:"policy_changes"`
	Blockers                      []string            `json:"blockers"`
}

func validLegacyDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == value
}

func validLegacyIntakeInput(value LegacyIntakeInput, commit bool) bool {
	if !validLegacyDigest(value.ConfigHash) || !validLegacyDigest(value.JournalHash) || len(value.History) > 200 || (commit && !validLegacyDigest(value.PlanHash)) || (value.PlanHash != "" && !validLegacyDigest(value.PlanHash)) {
		return false
	}
	if len(value.ManualAppAuthors) > 25 || (commit && len(value.ManualAppAuthors) > 0 && !value.AcknowledgePolicyNarrowing) {
		return false
	}
	authors := map[string]bool{}
	for _, author := range value.ManualAppAuthors {
		name := strings.TrimPrefix(author, "app/")
		name = strings.TrimSuffix(name, "[bot]")
		if name == author || len(name) == 0 || len(name) > 39 || strings.Trim(name, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-") != "" || authors[author] {
			return false
		}
		authors[author] = true
	}
	seen := map[uint64]bool{}
	for _, item := range value.History {
		if item.Number == 0 || item.Number > 9007199254740991 || seen[item.Number] {
			return false
		}
		seen[item.Number] = true
		if item.Kind == "observed" {
			if item.TaskID != "" || item.IncarnationID != "" || item.Fingerprint != "" || item.HistoricalContentHash != "" {
				return false
			}
		} else if (item.Kind != "processed" && item.Kind != "operation" && item.Kind != "recovery") || !validID(item.TaskID) || !validID(item.IncarnationID) || !validLegacyDigest(item.Fingerprint) || (item.HistoricalContentHash != "" && !validLegacyDigest(item.HistoricalContentHash)) {
			return false
		}
	}
	return true
}
