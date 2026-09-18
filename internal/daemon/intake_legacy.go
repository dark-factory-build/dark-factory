package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

func legacyExistingContent(record kernel.LegacyIntakeRecord, hash [32]byte) bool {
	if record.HistoricalContentHash != nil {
		return *record.HistoricalContentHash == hash
	}
	return record.HasHistory && record.ContentHash == hash
}

func (daemon *Daemon) legacyIntake(ctx context.Context, input api.IntakeInput, at kernel.UnixMillis) api.IntakeResult {
	if daemon.github == nil {
		return intakeFailure(maintainer.ErrUnavailable)
	}
	status, err := daemon.github.Status(ctx)
	if err != nil {
		return intakeFailure(err)
	}
	return daemon.legacyIntakeWithStatus(ctx, input, at, status)
}

func (daemon *Daemon) legacyIntakeWithStatus(ctx context.Context, input api.IntakeInput, at kernel.UnixMillis, status maintainer.Status) api.IntakeResult {
	if status.State != "connected" {
		return intakeFailure(maintainer.ErrDenied)
	}
	id, err := browserID(input.SourceID, kernel.IntakeSourceIDFromBytes)
	if err != nil {
		return intakeFailure(err)
	}
	project, err := browserID(input.ProjectID, kernel.ProjectIDFromBytes)
	if err != nil {
		return intakeFailure(err)
	}
	config := *input.Configuration
	agent, err := browserID(config.OverseerAgentID, kernel.AgentIDFromBytes)
	if err != nil {
		return intakeFailure(err)
	}
	prior, priorFound, err := daemon.store.LegacyIntakeMigration(ctx, id)
	if err != nil {
		return intakeFailure(err)
	}
	var remoteID uint64
	for _, delegated := range status.Repositories {
		if delegated.RepositoryID > 0 && ((!priorFound && strings.EqualFold(delegated.Repository, config.Repository)) || (priorFound && uint64(delegated.RepositoryID) == prior.RepositoryID)) {
			remoteID = uint64(delegated.RepositoryID)
		}
	}
	if remoteID == 0 {
		return intakeFailure(maintainer.ErrDenied)
	}
	if priorFound {
		if (input.Legacy.PlanHash != "" && hex.EncodeToString(prior.PlanHash[:]) != input.Legacy.PlanHash) || hex.EncodeToString(prior.ConfigHash[:]) != input.Legacy.ConfigHash || hex.EncodeToString(prior.JournalHash[:]) != input.Legacy.JournalHash {
			return intakeFailure(kernel.ErrConflict)
		}
		return api.IntakeResult{State: "legacy_committed", Legacy: &api.LegacyIntakePlan{PlanHash: hex.EncodeToString(prior.PlanHash[:]), GitHubRepositoryID: remoteID}}
	}
	selected, found, err := daemon.store.DefaultProjectRepository(ctx, project)
	if err != nil {
		return intakeFailure(err)
	}
	if !found || !selected.Enabled || (input.Action == "legacy_commit" && config.TargetRepositoryID != "" && config.TargetRepositoryID != selected.ID.String()) {
		return intakeFailure(kernel.ErrConflict)
	}
	target := selected.ID
	config.TargetRepositoryID = target.String()
	operator, found, err := daemon.store.Agent(ctx, agent)
	if err != nil {
		return intakeFailure(err)
	}
	if !found || operator.Archived || operator.Role != kernel.RoleOrchestrator || operator.ProjectID != project {
		return intakeFailure(kernel.ErrConflict)
	}
	source := kernel.IntakeSource{ID: id, ProjectID: project, TargetRepositoryID: target, OverseerAgentID: agent, GitHubRepositoryID: remoteID, GitHubRepositoryName: config.Repository, LabelFilter: config.Label, Enabled: true, Policy: kernel.IntakePolicy(config.Policy), TrustedGitHubLogins: config.TrustedAuthors, PriorityDefault: config.PriorityDefault, PriorityByLabel: config.PriorityByLabel, PollSeconds: config.PollSeconds, AdmissionLimit: config.AdmissionLimit}
	source.Revision, _ = kernel.NewRevision(1)
	source.CreatedAt, source.UpdatedAt = at, at
	if !kernel.ValidIntakeSource(source) {
		return intakeFailure(kernel.ErrInvalidValue)
	}
	plan := api.LegacyIntakePlan{TargetRepositoryID: target.String(), RequiresPolicyAcknowledgement: len(input.Legacy.ManualAppAuthors) > 0, GitHubRepositoryID: remoteID, Issues: []api.LegacyIntakeIssue{}, Blockers: []string{}, PolicyChanges: []string{"existing backlog is suppressed until explicit review", "comments, labels and activity timestamps no longer create work", "content edits require fresh acceptance"}}
	if input.Legacy.ReviewCompanion {
		proof, verified, proofErr := daemon.store.RepositorySourceIdentity(ctx, target)
		if proofErr != nil {
			return intakeFailure(proofErr)
		}
		publicationID, pinned, proofErr := daemon.store.RepositoryGitHubID(ctx, target)
		if proofErr != nil {
			return intakeFailure(proofErr)
		}
		if !verified || !pinned || proof.PublicationRepository == "" {
			plan.Blockers = append(plan.Blockers, "review_publication_unbound: bind the destination repository GitHub identity before cutover")
		} else {
			plan.PublicationRepository = proof.PublicationRepository
			delegated := false
			for _, grant := range status.Repositories {
				if uint64(grant.RepositoryID) == publicationID && strings.EqualFold(grant.Repository, proof.PublicationRepository) {
					delegated = true
				}
			}
			if !delegated {
				plan.Blockers = append(plan.Blockers, "review_publication_not_delegated: connect and delegate the frozen publication repository before cutover")
			}
		}
	}
	if plan.RequiresPolicyAcknowledgement {
		plan.PolicyChanges = append(plan.PolicyChanges, "App/bot authorship is not human approval: already imported work and receipts are preserved; future bot-authored issues require explicit manual acceptance")
	}
	settled, err := daemon.store.LegacyIntakeProjectSettled(ctx, project)
	if err != nil {
		return intakeFailure(err)
	}
	if !settled {
		plan.Blockers = append(plan.Blockers, "project_not_quiescent: finish or cancel existing project work using normal task controls, then preview again")
	}
	if config.Policy == string(kernel.IntakePolicyManual) {
		plan.PolicyChanges = append(plan.PolicyChanges, "legacy automatic author policy changes to manual acceptance")
	}
	sources, err := daemon.store.IntakeSources(ctx)
	if err != nil {
		return intakeFailure(err)
	}
	if len(sources) >= 200 {
		plan.Blockers = append(plan.Blockers, "source_limit: this factory already has 200 sources; legacy remains unchanged")
	}
	for _, existing := range sources {
		if existing.ID == id || (existing.GitHubRepositoryID == remoteID && existing.ProjectID == project && existing.TargetRepositoryID == target && existing.LabelFilter == config.Label) {
			plan.Blockers = append(plan.Blockers, "source_conflict: an existing source already owns this ID or configuration; legacy remains unchanged")
		}
	}
	if len(plan.Blockers) > 0 {
		return api.IntakeResult{State: "legacy_blocked", Legacy: &plan}
	}
	issues := map[uint64]maintainer.Issue{}
	for page := uint32(1); ; {
		observed, err := daemon.readIntakeIssues(ctx, config.Repository, remoteID, page, config.Label, 0)
		if err != nil {
			return intakeFailure(err)
		}
		if observed.RepositoryID != remoteID {
			return intakeFailure(maintainer.ErrDenied)
		}
		for _, issue := range observed.Issues {
			issues[issue.Number] = issue
		}
		if len(issues) > 200 || (len(issues) == 200 && observed.NextPage != nil) {
			return api.IntakeResult{State: "overflow"}
		}
		if observed.NextPage == nil {
			break
		}
		if *observed.NextPage <= page || *observed.NextPage > 1000 {
			return intakeFailure(maintainer.ErrInvalid)
		}
		page = *observed.NextPage
	}
	history := map[uint64]api.LegacyIntakeHistory{}
	for _, record := range input.Legacy.History {
		history[record.Number] = record
		if _, exists := issues[record.Number]; !exists {
			issue, err := daemon.exactIntakeIssue(ctx, source, record.Number)
			if err != nil {
				return intakeFailure(err)
			}
			issues[record.Number] = issue
		}
	}
	if len(issues) > 200 {
		return api.IntakeResult{State: "overflow"}
	}
	numbers := make([]uint64, 0, len(issues))
	for number := range issues {
		numbers = append(numbers, number)
	}
	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })
	records := make([]kernel.LegacyIntakeRecord, 0, len(numbers))
	for _, number := range numbers {
		issue := issues[number]
		if issue.Number != number || issue.NodeID == "" {
			return intakeFailure(maintainer.ErrInvalid)
		}
		hash := intakeSnapshot(source, issue).ContentHash()
		record := kernel.LegacyIntakeRecord{IssueNumber: number, NodeID: issue.NodeID, ContentHash: hash}
		view := api.LegacyIntakeIssue{Priority: kernel.IntakePriority(source, issue.Labels), Number: number, NodeID: issue.NodeID, ContentHash: hex.EncodeToString(hash[:]), State: "baseline_observed"}
		view.Eligibility = string(kernel.PreviewIntake(source, intakeSnapshot(source, issue), nil))
		if !intakeMatches(source, issue) {
			view.Eligibility = "filter_or_state_changed"
		}
		if old, found := history[number]; found && old.Kind != "observed" {
			record.HasHistory = true
			view.State = "history_unresolved"
			marker := "FACTORY_SOURCE " + config.Repository + "#" + strconv.FormatUint(number, 10)
			taskDigest := sha256.Sum256([]byte("source\x00" + marker + "\x00" + old.Fingerprint))
			expectedTask := hex.EncodeToString(taskDigest[:16])
			incarnationDigest := sha256.Sum256([]byte("incarnation\x00" + expectedTask))
			if old.TaskID != expectedTask || old.IncarnationID != hex.EncodeToString(incarnationDigest[:16]) {
				return intakeFailure(kernel.ErrConflict)
			}
			taskID, _ := browserID(old.TaskID, kernel.TaskIDFromBytes)
			incarnation, _ := browserID(old.IncarnationID, kernel.IncarnationIDFromBytes)
			recovery, found, err := daemon.store.TaskRecovery(ctx, taskID, incarnation)
			if err != nil {
				return intakeFailure(err)
			}
			if !found {
				if _, exists, err := daemon.store.Task(ctx, taskID); err != nil {
					return intakeFailure(err)
				} else if exists {
					return intakeFailure(kernel.ErrConflict)
				}
			}
			if old.Kind == "recovery" || (found && (recovery.Task.Status == kernel.TaskQueued || recovery.Task.Status == kernel.TaskRunning || recovery.Task.Status == kernel.TaskBlocked || recovery.NeedsOperatorRecovery || recovery.Run != nil && recovery.Run.Phase != kernel.RunTerminal)) || (!found && old.Kind == "operation") {
				plan.Blockers = append(plan.Blockers, "legacy_work_unsettled:"+old.TaskID)
			}
			if found {
				if recovery.Task.ProjectID != project || recovery.Task.AssignedAgentID != agent {
					return intakeFailure(kernel.ErrConflict)
				}
				route, bound, err := daemon.store.TaskRepository(ctx, taskID)
				if err != nil {
					return intakeFailure(err)
				}
				if !bound || route.ID != target {
					plan.Blockers = append(plan.Blockers, "legacy_destination_mismatch:"+old.TaskID+": retained work targets a different repository; review destinations before cutover")
				}
				record.TaskID, record.IncarnationID, record.TaskRevision = taskID, incarnation, recovery.Task.Revision
				view.TaskID, view.IncarnationID, view.TaskRevision = old.TaskID, old.IncarnationID, uint64(recovery.Task.Revision.Int64())
				if old.HistoricalContentHash != "" {
					data, _ := hex.DecodeString(old.HistoricalContentHash)
					record.HistoricalContentHash = (*[32]byte)(data)
					view.State = "history_linked"
				}
			}
		}
		plan.Issues = append(plan.Issues, view)
		records = append(records, record)
	}
	// Only identity, content, policy and terminal task authority enter the plan.
	// Comments, reactions and remote activity timestamps cannot invalidate it.
	encoded, err := json.Marshal(struct {
		Input api.IntakeInput
		Plan  api.LegacyIntakePlan
	}{Input: api.IntakeInput{Action: "legacy_preview", SourceID: input.SourceID, ProjectID: input.ProjectID, Configuration: &config, Legacy: &api.LegacyIntakeInput{ReviewCompanion: input.Legacy.ReviewCompanion, ManualAppAuthors: input.Legacy.ManualAppAuthors, AcknowledgePolicyNarrowing: plan.RequiresPolicyAcknowledgement, ConfigHash: input.Legacy.ConfigHash, JournalHash: input.Legacy.JournalHash, History: input.Legacy.History}}, Plan: plan})
	if err != nil {
		return intakeFailure(err)
	}
	digest := sha256.Sum256(encoded)
	plan.PlanHash = hex.EncodeToString(digest[:])
	if len(plan.Blockers) > 0 {
		return api.IntakeResult{State: "legacy_blocked", Legacy: &plan}
	}
	if input.Action == "legacy_preview" {
		return api.IntakeResult{State: "legacy_preview", Legacy: &plan}
	}
	if plan.RequiresPolicyAcknowledgement && !input.Legacy.AcknowledgePolicyNarrowing {
		return api.IntakeResult{State: "policy_acknowledgement_required", Legacy: &plan}
	}
	if input.Legacy.PlanHash != plan.PlanHash {
		return api.IntakeResult{State: "stale", Legacy: &plan}
	}
	configHash, _ := hex.DecodeString(input.Legacy.ConfigHash)
	journalHash, _ := hex.DecodeString(input.Legacy.JournalHash)
	_, err = daemon.store.CommitLegacyIntakeMigration(ctx, kernel.NewIntakeSource{ID: id, ProjectID: project, TargetRepositoryID: target, OverseerAgentID: agent, GitHubRepositoryID: remoteID, GitHubRepositoryName: config.Repository, LabelFilter: config.Label, Policy: kernel.IntakePolicy(config.Policy), TrustedGitHubLogins: config.TrustedAuthors, PriorityDefault: config.PriorityDefault, PriorityByLabel: config.PriorityByLabel, PollSeconds: config.PollSeconds, AdmissionLimit: config.AdmissionLimit}, kernel.LegacyIntakeReceipt{RepositoryID: remoteID, PlanHash: digest, ConfigHash: [32]byte(configHash), JournalHash: [32]byte(journalHash)}, records, at)
	if err != nil {
		return intakeFailure(err)
	}
	return api.IntakeResult{State: "legacy_committed", Legacy: &plan}
}

// legacyIntakeLineage reads managed work for the frozen review companion route.
// The migration receipt supplies remote identity; a live read proves the node.
func (daemon *Daemon) legacyIntakeLineage(ctx context.Context, input api.IntakeInput) api.IntakeResult {
	sourceID, _ := browserID(input.SourceID, kernel.IntakeSourceIDFromBytes)
	receipt, found, err := daemon.store.LegacyIntakeMigration(ctx, sourceID)
	if err != nil {
		return intakeFailure(err)
	}
	if !found || hex.EncodeToString(receipt.PlanHash[:]) != input.Legacy.PlanHash || hex.EncodeToString(receipt.ConfigHash[:]) != input.Legacy.ConfigHash || hex.EncodeToString(receipt.JournalHash[:]) != input.Legacy.JournalHash {
		return api.IntakeResult{State: "conflict"}
	}
	projectID, _ := browserID(input.ProjectID, kernel.ProjectIDFromBytes)
	targetID, _ := browserID(input.Configuration.TargetRepositoryID, kernel.RepositoryIDFromBytes)
	page, err := daemon.readIntakeIssues(ctx, input.Configuration.Repository, receipt.RepositoryID, 1, "", input.IssueNumber)
	if err != nil {
		return intakeFailure(err)
	}
	if page.RepositoryID != receipt.RepositoryID || len(page.Issues) != 1 || page.Issues[0].Number != input.IssueNumber {
		return api.IntakeResult{State: "denied"}
	}
	accepted, found, err := daemon.store.ImportedIntakeIssue(ctx, receipt.RepositoryID, input.IssueNumber, page.Issues[0].NodeID, projectID, targetID)
	if err != nil {
		return intakeFailure(err)
	}
	if !found {
		source := kernel.IntakeSource{ProjectID: projectID, TargetRepositoryID: targetID, GitHubRepositoryID: receipt.RepositoryID}
		old, retained, err := daemon.store.LegacyIntakeSuppression(ctx, source, intakeSnapshot(source, page.Issues[0]))
		if err != nil {
			return intakeFailure(err)
		}
		if retained && old.TaskID != (kernel.TaskID{}) {
			return api.IntakeResult{State: "legacy_existing_work", TaskID: old.TaskID.String()}
		}
		return api.IntakeResult{State: "not_found"}
	}
	if accepted.WithdrawnAt != nil {
		return api.IntakeResult{State: "withdrawn"}
	}
	return api.IntakeResult{State: "imported", AcceptanceID: accepted.ID.String(), TaskID: accepted.TaskID.String()}
}
