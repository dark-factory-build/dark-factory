package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

func intakeFailure(err error) api.IntakeResult {
	state := "unavailable"
	switch {
	case errors.Is(err, maintainer.ErrDenied):
		state = "denied"
	case errors.Is(err, kernel.ErrNotFound):
		state = "not_found"
	case errors.Is(err, kernel.ErrConflict), errors.Is(err, kernel.ErrRevisionConflict):
		state = "conflict"
	case errors.Is(err, kernel.ErrInvalidValue), errors.Is(err, maintainer.ErrInvalid):
		state = "invalid"
	}
	return api.IntakeResult{State: state}
}
func intakeSourceView(source kernel.IntakeSource) api.IntakeSource {
	return api.IntakeSource{ID: source.ID.String(), ProjectID: source.ProjectID.String(), GitHubRepositoryID: source.GitHubRepositoryID, Enabled: source.Enabled, Revision: uint64(source.Revision.Int64()),
		PriorityDefault: source.PriorityDefault, PriorityByLabel: source.PriorityByLabel, Repository: source.GitHubRepositoryName, TargetRepositoryID: source.TargetRepositoryID.String(), OverseerAgentID: source.OverseerAgentID.String(), Label: source.LabelFilter, Policy: string(source.Policy), TrustedAuthors: source.TrustedGitHubLogins, PollSeconds: source.PollSeconds, AdmissionLimit: source.AdmissionLimit}
}

// Intake is the one authenticated operator path. Remote content and identity
// always come from the configured connection, never from browser assertions.
func (daemon *Daemon) Intake(ctx context.Context, input api.IntakeInput) api.IntakeResult {
	if !api.ValidIntakeInput(input) {
		return api.IntakeResult{State: "invalid"}
	}
	at, err := daemon.timestamp()
	if err != nil {
		return intakeFailure(err)
	}
	if input.Action == "legacy_lineage" {
		return daemon.legacyIntakeLineage(ctx, input)
	}
	if input.Action == "legacy_preview" || input.Action == "legacy_commit" {
		return daemon.legacyIntake(ctx, input, at)
	}
	if input.Action == "list" {
		var sources []kernel.IntakeSource
		if input.ProjectID == "" {
			sources, err = daemon.store.IntakeSources(ctx)
		} else {
			project, parseErr := browserID(input.ProjectID, kernel.ProjectIDFromBytes)
			if parseErr != nil {
				return intakeFailure(kernel.ErrInvalidValue)
			}
			sources, err = daemon.store.ProjectIntakeSources(ctx, project)
		}
		if err != nil {
			return intakeFailure(err)
		}
		result := api.IntakeResult{State: "ok", Sources: []api.IntakeSource{}}
		sync := daemon.intakeSync()
		for _, source := range sources {
			view := intakeSourceView(source)
			if status, found := sync[source.ID.String()]; found {
				view.Sync = &status
			}
			result.Sources = append(result.Sources, view)
		}
		return result
	}
	if input.Action == "withdraw" || input.Action == "import" {
		id, parseErr := browserID(input.AcceptanceID, kernel.IntakeAcceptanceIDFromBytes)
		if parseErr != nil {
			return intakeFailure(kernel.ErrInvalidValue)
		}
		accepted, found, readErr := daemon.store.IntakeAcceptance(ctx, id)
		if readErr != nil {
			return intakeFailure(readErr)
		}
		if !found {
			return intakeFailure(kernel.ErrNotFound)
		}
		if input.Action == "withdraw" {
			accepted, err = daemon.store.WithdrawIntakeAcceptance(ctx, id, at)
			if err == nil {
				err = daemon.reconcileIntakeWithdrawal(ctx, accepted)
			}
			if err != nil {
				return api.IntakeResult{State: "withdrawal_pending", AcceptanceID: id.String(), TaskID: accepted.TaskID.String()}
			}
			return api.IntakeResult{State: "withdrawn", AcceptanceID: id.String(), TaskID: accepted.TaskID.String()}
		}
		task, importErr := daemon.importAcceptedIntake(ctx, accepted)
		if importErr != nil {
			return intakeFailure(importErr)
		}
		return api.IntakeResult{State: "imported", AcceptanceID: id.String(), TaskID: task.ID.String()}
	}
	sourceID, err := browserID(input.SourceID, kernel.IntakeSourceIDFromBytes)
	if err != nil {
		return intakeFailure(kernel.ErrInvalidValue)
	}
	if input.Action == "create" || input.Action == "update" {
		if daemon.github == nil {
			return intakeFailure(maintainer.ErrUnavailable)
		}
		status, statusErr := daemon.github.Status(ctx)
		if statusErr != nil {
			return intakeFailure(statusErr)
		}
		if status.State != "connected" || status.User == nil {
			return intakeFailure(maintainer.ErrDenied)
		}
		config := *input.Configuration
		var remoteID uint64
		for _, repository := range status.Repositories {
			if strings.EqualFold(repository.Repository, config.Repository) {
				remoteID = uint64(repository.RepositoryID)
				config.Repository = repository.Repository
			}
		}
		if remoteID == 0 {
			return intakeFailure(maintainer.ErrDenied)
		}
		project, parseErr := browserID(input.ProjectID, kernel.ProjectIDFromBytes)
		if parseErr != nil {
			return intakeFailure(kernel.ErrInvalidValue)
		}
		target, parseErr := browserID(config.TargetRepositoryID, kernel.RepositoryIDFromBytes)
		if parseErr != nil {
			return intakeFailure(kernel.ErrInvalidValue)
		}
		agent, parseErr := browserID(config.OverseerAgentID, kernel.AgentIDFromBytes)
		if parseErr != nil {
			return intakeFailure(kernel.ErrInvalidValue)
		}
		authors := append([]string(nil), config.TrustedAuthors...)
		for index, author := range authors {
			if author == "@me" {
				authors[index] = status.User.Login
			}
		}
		spec := kernel.NewIntakeSource{ID: sourceID, GitHubRepositoryID: remoteID, GitHubRepositoryName: config.Repository, ProjectID: project, TargetRepositoryID: target, OverseerAgentID: agent, LabelFilter: config.Label, Policy: kernel.IntakePolicy(config.Policy), TrustedGitHubLogins: authors, PriorityDefault: config.PriorityDefault, PriorityByLabel: config.PriorityByLabel, PollSeconds: config.PollSeconds, AdmissionLimit: config.AdmissionLimit}
		var source kernel.IntakeSource
		if input.Action == "create" {
			source, err = daemon.store.CreateIntakeSource(ctx, spec, at)
		} else {
			revision, revisionErr := kernel.NewRevision(int64(input.ExpectedRevision))
			if revisionErr != nil {
				return intakeFailure(kernel.ErrInvalidValue)
			}
			source, err = daemon.store.UpdateIntakeSource(ctx, sourceID, revision, spec, false, at)
		}
		if err != nil {
			return intakeFailure(err)
		}
		return api.IntakeResult{State: "ok", Sources: []api.IntakeSource{intakeSourceView(source)}}
	}
	source, found, err := daemon.store.IntakeSource(ctx, sourceID)
	if err != nil {
		return intakeFailure(err)
	}
	if !found {
		return intakeFailure(kernel.ErrNotFound)
	}
	switch input.Action {
	case "enable", "pause":
		revision, revisionErr := kernel.NewRevision(int64(input.ExpectedRevision))
		if revisionErr != nil {
			return intakeFailure(kernel.ErrInvalidValue)
		}
		source, err = daemon.store.SetIntakeSourceEnabled(ctx, sourceID, revision, input.Action == "enable", at)
		if err != nil {
			return intakeFailure(err)
		}
		return api.IntakeResult{State: "ok", Sources: []api.IntakeSource{intakeSourceView(source)}}
	case "accept":
		if uint64(source.Revision.Int64()) != input.ExpectedRevision {
			return intakeFailure(kernel.ErrRevisionConflict)
		}
		issue, readErr := daemon.exactIntakeIssue(ctx, source, input.IssueNumber)
		if readErr != nil {
			return intakeFailure(readErr)
		}
		snapshot := intakeSnapshot(source, issue)
		digest := snapshot.ContentHash()
		if hex.EncodeToString(digest[:]) != input.ContentHash {
			return api.IntakeResult{State: "content_changed"}
		}
		if !intakeMatches(source, issue) {
			return api.IntakeResult{State: "ineligible"}
		}
		legacy, exists, legacyErr := daemon.store.LegacyIntakeSuppression(ctx, source, snapshot)
		if legacyErr != nil {
			return intakeFailure(legacyErr)
		}
		if exists && legacyExistingContent(legacy, digest) {
			if legacy.TaskID == (kernel.TaskID{}) {
				return api.IntakeResult{State: "legacy_history_unresolved"}
			}
			return api.IntakeResult{State: "legacy_existing_work", TaskID: legacy.TaskID.String()}
		}
		accepted, acceptErr := daemon.store.AcceptIntakeSnapshot(ctx, source.ID, snapshot, at, source.Revision)
		if acceptErr != nil {
			return intakeFailure(acceptErr)
		}
		if accepted.WithdrawnAt != nil {
			return api.IntakeResult{State: "withdrawn", AcceptanceID: accepted.ID.String(), TaskID: accepted.TaskID.String()}
		}
		return api.IntakeResult{State: "accepted", AcceptanceID: accepted.ID.String(), TaskID: accepted.TaskID.String()}
	case "preview", "refresh", "tick":
		return daemon.previewIntake(ctx, source, input.Page, input.Action == "tick", input.AcceptanceCursor)
	}
	return api.IntakeResult{State: "invalid"}
}

func intakeSnapshot(source kernel.IntakeSource, issue maintainer.Issue) kernel.IntakeIssueSnapshot {
	return kernel.IntakeIssueSnapshot{GitHubRepositoryID: source.GitHubRepositoryID, IssueNumber: issue.Number, NodeID: issue.NodeID, Title: issue.Title, Body: issue.Body, AuthorLogin: issue.Author.Login, AuthorType: kernel.GitHubAuthorType(strings.ToLower(issue.Author.Type))}
}
func intakeMatches(source kernel.IntakeSource, issue maintainer.Issue) bool {
	if issue.State != "open" {
		return false
	}
	if source.LabelFilter == "" {
		return true
	}
	for _, label := range issue.Labels {
		if strings.EqualFold(label, source.LabelFilter) {
			return true
		}
	}
	return false
}
func (daemon *Daemon) exactIntakeIssue(ctx context.Context, source kernel.IntakeSource, number uint64) (maintainer.Issue, error) {
	page, err := daemon.readIntakeIssues(ctx, source.GitHubRepositoryName, source.GitHubRepositoryID, 1, "", number)
	if err != nil {
		return maintainer.Issue{}, err
	}
	return page.Issues[0], nil
}
func (daemon *Daemon) importAcceptedIntake(ctx context.Context, accepted kernel.IntakeAcceptance) (kernel.Task, error) {
	sources, err := daemon.store.ProjectIntakeSources(ctx, accepted.ProjectID)
	if err != nil {
		return kernel.Task{}, err
	}
	for _, source := range sources {
		if !source.Enabled || source.GitHubRepositoryID != accepted.Snapshot.GitHubRepositoryID || source.TargetRepositoryID != accepted.RepositoryID {
			continue
		}
		issue, err := daemon.exactIntakeIssue(ctx, source, accepted.Snapshot.IssueNumber)
		if err != nil {
			return kernel.Task{}, err
		}
		snapshot := intakeSnapshot(source, issue)
		if !intakeMatches(source, issue) || snapshot.NodeID != accepted.Snapshot.NodeID || snapshot.ContentHash() != accepted.Snapshot.ContentHash() {
			continue
		}
		at, err := daemon.timestamp()
		if err != nil {
			return kernel.Task{}, err
		}
		priority := kernel.IntakePriority(source, issue.Labels)
		task, err := daemon.store.ImportIntakeAcceptanceWithPriority(ctx, accepted.ID, at, source, priority)
		if err == nil && task.Status == kernel.TaskQueued && task.Priority != priority {
			task, err = daemon.store.UpdateTaskForOperator(ctx, task.ID, task.Revision, kernel.TaskPatch{Priority: &priority}, at)
		}
		if err == nil {
			daemon.notifyScheduler()
		}
		return task, err
	}
	return kernel.Task{}, kernel.ErrConflict
}
func (daemon *Daemon) reconcileIntakeWithdrawal(ctx context.Context, accepted kernel.IntakeAcceptance) error {
	tasks, err := daemon.store.IntakeTasksForAcceptance(ctx, accepted.ID)
	if err != nil {
		return err
	}
	var failures []error
	for _, task := range tasks {
		if err := daemon.withdrawIntakeTask(ctx, accepted.ID, task); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (daemon *Daemon) withdrawIntakeTask(ctx context.Context, acceptanceID kernel.IntakeAcceptanceID, task kernel.Task) error {
	at, err := daemon.timestamp()
	if err != nil {
		return err
	}
	if task.Status == kernel.TaskQueued {
		_, err = daemon.store.UpdateTaskForOperator(ctx, task.ID, task.Revision, kernel.TaskPatch{Cancel: true}, at)
		return err
	}
	if task.Status != kernel.TaskRunning {
		return nil
	}
	runs, err := daemon.store.RecoverableRuns(ctx)
	if err != nil {
		return err
	}
	for _, value := range runs {
		run := value.Run
		if run.TaskID != task.ID {
			continue
		}
		if run.Phase == kernel.RunFinalizing || run.Phase == kernel.RunTerminal {
			return nil
		}
		digest := sha256.Sum256([]byte("intake-withdraw/" + acceptanceID.String() + "/" + run.ID.String()))
		operation, err := kernel.TaskInterventionIDFromBytes(digest[:kernel.IDBytes])
		if err != nil {
			return err
		}
		_, err = daemon.store.StopRunForOperator(ctx, kernel.TaskInterventionRequest{OperationID: operation, TaskID: task.ID, RunID: run.ID, ExpectedTaskRevision: task.Revision, ExpectedRunRevision: run.Revision, Kind: kernel.TaskInterventionStop}, nil, at)
		daemon.notifyScheduler()
		return err
	}
	return kernel.ErrConflict
}

func (daemon *Daemon) previewIntake(ctx context.Context, source kernel.IntakeSource, page uint32, tick bool, cursor string) api.IntakeResult {
	// Leave response time inside the existing 90-second dispatch budget.
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	result := api.IntakeResult{State: "ok", ReviewedRevision: uint64(source.Revision.Int64()), Candidates: []api.IntakeCandidate{}, ImportedTasks: []string{}}
	failure := func(err error) api.IntakeResult {
		result.State = intakeFailure(err).State
		return result
	}

	if tick {
		withdrawals, err := daemon.store.PendingIntakeWithdrawals(ctx, source.ID, source.AdmissionLimit)
		if err != nil {
			return failure(err)
		}
		for _, accepted := range withdrawals {
			if err := daemon.reconcileIntakeWithdrawal(ctx, accepted); err != nil {
				return api.IntakeResult{State: "withdrawal_pending"}
			}
		}
	}
	if tick && !source.Enabled {
		return api.IntakeResult{State: "paused"}
	}
	// Keep bounded receipt scanning independent of discovery and admission limits.
	// The existing controller journal owns progress; an empty cursor wraps.
	if tick {
		after := kernel.IntakeAcceptanceID{}
		if cursor != "" {
			after, _ = browserID(cursor, kernel.IntakeAcceptanceIDFromBytes) // ValidIntakeInput checked it.
		}
		const receiptPageSize = 25
		pending, err := daemon.store.PendingIntakeAcceptancesAfter(ctx, source.ID, receiptPageSize, after)
		if err != nil {
			return failure(err)
		}
		result.AcceptanceProgress = true
		for index, accepted := range pending {
			result.AcceptanceCursor = accepted.ID.String()
			task, err := daemon.importAcceptedIntake(ctx, accepted)
			if err == nil {
				result.ImportedTasks = append(result.ImportedTasks, task.ID.String())
			} else if !errors.Is(err, kernel.ErrConflict) && !errors.Is(err, kernel.ErrRevisionConflict) {
				return failure(err)
			}
			if index == len(pending)-1 && len(pending) < receiptPageSize {
				result.AcceptanceCursor = ""
			}
			if len(result.ImportedTasks) >= int(source.AdmissionLimit) {
				break
			}
		}
	}
	observed, err := daemon.readIntakeIssues(ctx, source.GitHubRepositoryName, source.GitHubRepositoryID, page, source.LabelFilter, 0)
	if err != nil {
		return failure(err)
	}
	result.NextPage = observed.NextPage
	for _, issue := range observed.Issues {
		snapshot := intakeSnapshot(source, issue)
		accepted, found, err := daemon.store.LatestIntakeAcceptance(ctx, source.GitHubRepositoryID, issue.Number, issue.NodeID, source.ProjectID, source.TargetRepositoryID)
		if err != nil {
			return failure(err)
		}
		var prior *kernel.IntakeAcceptance
		if found {
			prior = &accepted
		}
		previewSource := source
		previewSource.Enabled = true
		reason := string(kernel.PreviewIntake(previewSource, snapshot, prior))
		if !intakeMatches(source, issue) {
			reason = "filter_or_state_changed"
		}
		hash := snapshot.ContentHash()
		candidate := api.IntakeCandidate{Number: issue.Number, URL: issue.URL, Title: issue.Title, Body: issue.Body, Author: issue.Author.Login, Labels: issue.Labels, ContentHash: hex.EncodeToString(hash[:]), Reason: reason}
		if len(candidate.Title) > 900 || len(candidate.Body) > 5000 {
			candidate.Title = "Issue content exceeds the supported intake size"
			candidate.Body = ""
			candidate.Truncated = true
			reason = "content_too_large"
			candidate.Reason = reason
		}
		if found {
			candidate.AcceptanceID = accepted.ID.String()
			candidate.TaskID = accepted.TaskID.String()
		}
		if !found {
			legacy, suppressed, err := daemon.store.LegacyIntakeSuppression(ctx, source, snapshot)
			if err != nil {
				return intakeFailure(err)
			}
			if suppressed {
				reason = "legacy_suppressed"
				if legacy.HasHistory && legacy.HistoricalContentHash == nil {
					reason = "legacy_history_unresolved"
				}
				if legacy.TaskID != (kernel.TaskID{}) {
					candidate.TaskID = legacy.TaskID.String()
					if legacyExistingContent(legacy, hash) {
						reason = "legacy_existing_work"
					}
				}
				candidate.Reason = reason
			}
		}
		if tick && reason == string(kernel.IntakeEligibleTrusted) && len(result.ImportedTasks) < int(source.AdmissionLimit) {
			at, err := daemon.timestamp()
			if err != nil {
				return failure(err)
			}
			accepted, err = daemon.store.AcceptIntakeSnapshot(ctx, source.ID, snapshot, at, source.Revision)
			if err != nil {
				return failure(err)
			}
			task, err := daemon.importAcceptedIntake(ctx, accepted)
			if err != nil {
				return failure(err)
			}
			candidate.AcceptanceID = accepted.ID.String()
			candidate.TaskID = task.ID.String()
			candidate.Reason = "imported"
			result.ImportedTasks = append(result.ImportedTasks, task.ID.String())
		}
		if tick && found && reason == string(kernel.IntakeAlreadyAccepted) {
			_, imported, err := daemon.store.Task(ctx, accepted.TaskID)
			if err != nil {
				return intakeFailure(err)
			}
			if imported || len(result.ImportedTasks) < int(source.AdmissionLimit) {
				task, err := daemon.importAcceptedIntake(ctx, accepted)
				if err == nil && !imported {
					result.ImportedTasks = append(result.ImportedTasks, task.ID.String())
					candidate.Reason = "imported"
				} else if err != nil && !errors.Is(err, kernel.ErrConflict) && !errors.Is(err, kernel.ErrRevisionConflict) {
					return intakeFailure(err)
				}
			}
		}
		if tick && found && accepted.WithdrawnAt != nil {
			if err := daemon.reconcileIntakeWithdrawal(ctx, accepted); err != nil {
				candidate.Reason = "withdrawal_pending"
			}
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	return result
}

func (daemon *Daemon) readIntakeIssues(ctx context.Context, repository string, id uint64, page uint32, label string, number uint64) (maintainer.IssuePage, error) {
	if daemon.intakeIssues != nil {
		return daemon.intakeIssues(ctx, repository, id, page, label, number)
	}
	if daemon.github == nil {
		return maintainer.IssuePage{}, maintainer.ErrUnavailable
	}
	return daemon.github.Issues(ctx, repository, id, page, label, number)
}
