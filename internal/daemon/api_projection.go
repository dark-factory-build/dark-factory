package daemon

import (
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func projectSnapshot(snapshot kernel.DashboardSnapshot) api.DashboardSnapshot {
	result := api.DashboardSnapshot{
		Head: uint64(snapshot.Head.Int64()),
		Factory: api.FactorySummary{
			DispatchEnabled: snapshot.Factory.DispatchEnabled,
			Capacity:        snapshot.Factory.Capacity,
			ActiveRuns:      snapshot.Factory.ActiveRuns,
			Revision:        uint64(snapshot.Factory.Revision.Int64()),
		},
		Projects: make([]api.ProjectSummary, 0, len(snapshot.Projects)),
		Agents:   make([]api.AgentSummary, 0, len(snapshot.Agents)),
		Tasks:    make([]api.TaskSummary, 0, len(snapshot.Tasks)),
	}
	for _, project := range snapshot.Projects {
		result.Projects = append(result.Projects, api.ProjectSummary{
			ID: project.ID.String(), Name: project.Name, RunBudgetLimit: project.RunBudgetLimit, RunsUsed: project.RunsUsed, MaxRunSeconds: project.MaxRunSeconds, TokenLimit: project.Tokens.TokenLimit, TokensUsed: project.Tokens.TokensUsed, SpecialistRuns: project.SpecialistRuns, SpecialistOpenProposals: project.SpecialistOpenProposals, Revision: uint64(project.Revision.Int64()),
		})
	}
	for _, agent := range snapshot.Agents {
		result.Agents = append(result.Agents, projectAgentSummary(agent))
	}
	for _, task := range snapshot.Tasks {
		result.Tasks = append(result.Tasks, api.TaskSummary{
			ID: task.ID.String(), ProjectID: task.ProjectID.String(), AssignedAgentID: optionalAgentText(task.AssignedAgentID),
			IncarnationID: task.IncarnationID.String(), WorkRevision: uint64(task.WorkRevision.Int64()), Title: task.Title, Status: task.Status, BlockedReason: task.BlockedReason, UpdatedAt: uint64(task.UpdatedAt.Int64()), IssueNumber: task.IssueNumber, MissionID: task.MissionID, Priority: task.Priority, Revision: uint64(task.Revision.Int64()),
		})
	}
	return result
}

func projectPublicSnapshot(snapshot kernel.PublicSnapshot, defaults func(string, string) (string, string, string)) api.DashboardSnapshot {
	base := kernel.DashboardSnapshot{Head: snapshot.Head, Factory: snapshot.Factory, Agents: snapshot.Agents, Tasks: snapshot.Tasks}
	for _, project := range snapshot.Projects {
		base.Projects = append(base.Projects, kernel.ProjectSummary{ID: project.ID, Name: project.Name, RunBudgetLimit: project.RunBudgetLimit, RunsUsed: project.RunsUsed, MaxRunSeconds: project.MaxRunSeconds, Tokens: project.Tokens, SpecialistRuns: project.SpecialistRuns, SpecialistOpenProposals: project.SpecialistOpenProposals, Revision: project.Revision})
	}
	result := projectSnapshot(base)
	result.Accounts = make([]api.AccountSummary, 0, len(snapshot.Accounts))
	result.PeerQuestions = make([]api.PeerQuestionSummary, 0, len(snapshot.PeerQuestions))
	for _, account := range snapshot.Accounts {
		result.Accounts = append(result.Accounts, api.AccountSummary{ID: account.ID.String(), Provider: account.Provider, Home: account.Home, Label: account.Label, Revision: uint64(account.Revision.Int64())})
	}
	for _, question := range snapshot.PeerQuestions {
		result.PeerQuestions = append(result.PeerQuestions, api.PeerQuestionSummary{ID: question.ID.String(), SourceTaskID: question.SourceTaskID.String(), TargetTaskID: question.TargetTaskID.String(), Answered: question.Answered, Revision: uint64(question.Revision.Int64())})
	}
	homes := make(map[kernel.AccountID]string, len(snapshot.Accounts))
	for _, account := range snapshot.Accounts {
		homes[account.ID] = account.Home
	}
	for index, agent := range snapshot.Agents {
		if defaults == nil {
			continue
		}
		model, effort, source := defaults(agent.Provider, homes[agent.AccountID])
		if agent.Model != "" {
			model, source = agent.Model, "agent"
		}
		if agent.ReasoningEffort != "" {
			effort = agent.ReasoningEffort
		}
		result.Agents[index].EffectiveModel, result.Agents[index].EffectiveReasoningEffort, result.Agents[index].ModelSource = model, effort, source
	}
	return result
}

// projectOverseerSnapshot carries each settled Change's identities. Where
// to read one comes only from an explicit attempt source request.
func projectOverseerSnapshot(snapshot kernel.OverseerSnapshot) (api.OverseerSnapshot, error) {
	result := api.OverseerSnapshot{
		ProjectID: snapshot.ProjectID.String(), Head: uint64(snapshot.Head.Int64()), NextOffset: snapshot.NextOffset, NextTextOffset: snapshot.NextTextOffset,
		Agents: []api.AgentSummary{}, Tasks: []api.OverseerTask{}, Runs: []api.OverseerRun{}, Questions: []api.OverseerQuestion{}, PeerQuestions: []api.PeerQuestion{}, History: []api.OverseerIntervention{}, Handoffs: []api.RetainedChangeHandoff{},
	}
	for _, agent := range snapshot.Agents {
		result.Agents = append(result.Agents, projectAgentSummary(agent))
	}
	for _, task := range snapshot.Tasks {
		result.Tasks = append(result.Tasks, api.OverseerTask{ID: task.ID.String(), ProjectID: task.ProjectID.String(), AssignedAgentID: optionalAgentText(task.AssignedAgentID), Title: task.Title, Objective: task.Objective, ObjectiveTruncated: task.ObjectiveTruncated, Status: task.Status.String(), Priority: task.Priority, BlockedReason: task.BlockedReason, Result: task.Result, ResultTruncated: task.ResultTruncated, Revision: uint64(task.Revision.Int64())})
	}
	for _, handoff := range snapshot.Handoffs {
		result.Handoffs = append(result.Handoffs, projectHandoffIdentity(handoff))
	}
	for _, run := range snapshot.Runs {
		result.Runs = append(result.Runs, api.OverseerRun{ID: run.ID.String(), AgentID: run.AgentID.String(), TaskID: run.TaskID.String(), Phase: run.Phase.String(), Revision: uint64(run.Revision.Int64())})
	}
	for _, question := range snapshot.Questions {
		result.Questions = append(result.Questions, api.OverseerQuestion{ID: question.ID.String(), AgentID: question.AgentID.String(), TaskID: question.TaskID.String(), Status: question.Status.String(), Revision: uint64(question.Revision.Int64()), Question: question.Question})
	}
	for _, question := range snapshot.PeerQuestions {
		result.PeerQuestions = append(result.PeerQuestions, api.PeerQuestion{ID: question.ID.String(), SourceTaskID: question.SourceTaskID.String(), TargetTaskID: question.TargetTaskID.String(), Question: question.Question, Answer: question.Answer, RecipientDeliveryState: question.RecipientDeliveryState.String(), AnswerDeliveryState: question.AnswerDeliveryState.String(), Revision: uint64(question.Revision.Int64())})
	}
	for _, item := range snapshot.History {
		detail := ""
		if item.ResultDetail != nil {
			detail = *item.ResultDetail
		}
		payload, truncated := item.Payload, snapshot.HistoryExcerpt
		if truncated {
			payload, truncated = overseerAPIExcerpt(payload)
		}
		successor := ""
		if item.SuccessorTaskID != nil {
			successor = item.SuccessorTaskID.String()
		}
		result.History = append(result.History, api.OverseerIntervention{OperationID: item.OperationID.String(), TaskID: item.TaskID.String(), RunID: item.RunID.String(), SuccessorTaskID: successor, Kind: item.Kind.String(), Actor: item.Actor.String(), Payload: payload, PayloadTruncated: truncated, State: item.State.String(), Detail: detail, CreatedAtMs: uint64(item.CreatedAt.Int64())})
	}
	return result, nil
}

func projectAgentSummary(agent kernel.AgentSummary) api.AgentSummary {
	result := api.AgentSummary{
		ID: agent.ID.String(), ProjectID: agent.ProjectID.String(), Name: agent.Name,
		Role: agent.Role, Provider: agent.Provider, Paused: agent.Paused, Archived: agent.Archived,
		Model: agent.Model, ReasoningEffort: agent.ReasoningEffort,
		ToolBudgetLimit: agent.ToolBudgetLimit, ToolCallsUsed: agent.ToolCallsUsed,
		IdlePolicy: string(agent.Idle.Policy), IdleAfterSeconds: agent.Idle.AfterSeconds,
		IdleInstruction: agent.Idle.Instruction, IdleRunBudget: agent.Idle.RunBudget, IdleRunsUsed: agent.Idle.RunsUsed, IdleWakeOn: agent.Idle.WakeOn,
		AccountID: optionalAccountText(agent.AccountID), Revision: uint64(agent.Revision.Int64()),
		Appearance: api.AgentAppearance{Automatic: agent.Appearance.Automatic, Skin: agent.Appearance.Skin, Hair: agent.Appearance.Hair, HairColour: agent.Appearance.HairColour, Face: agent.Appearance.Face, Outfit: agent.Appearance.Outfit, ClothesColour: agent.Appearance.ClothesColour, Shoes: agent.Appearance.Shoes, Tool: agent.Appearance.Tool, Headwear: agent.Appearance.Headwear},
	}
	if agent.Specialist != nil {
		result.Specialist = &api.SpecialistSummary{NextReviewAtMillis: uint64(max(agent.Specialist.NextReviewAt, 0)), NextReason: agent.Specialist.NextReason, Waiting: agent.Specialist.Waiting, QuietReviews: uint8(agent.Specialist.QuietReviews), OpenProposals: uint16(agent.Specialist.OpenProposals), OpenProposalLimit: uint16(agent.Specialist.OpenProposalLimit), LastReviewTaskID: agent.Specialist.LastReviewTaskID}
	}
	return result
}

func optionalAccountText(account kernel.AccountID) string {
	if account == (kernel.AccountID{}) {
		return ""
	}
	return account.String()
}

// projectHandoffIdentity projects the durable identities of one settled
// Change: no path, and the head only once the Change has a worktree.
func projectHandoffIdentity(handoff kernel.RetainedChangeHandoff) api.RetainedChangeHandoff {
	return api.RetainedChangeHandoff{ChangeID: handoff.ChangeID.String(), BaseCommit: handoff.BaseCommit, HeadCommit: handoff.HeadCommit, TaskID: handoff.TaskID.String(), TaskWorkRevision: uint64(handoff.TaskWorkRevision.Int64()), ChangeRevision: uint64(handoff.ChangeRevision.Int64())}
}

func overseerAPIExcerpt(value string) (string, bool) {
	const limit = 1024
	if len(value) <= limit {
		return value, false
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}

func parseID(value string) ([]byte, error) {
	if len(value) != 32 || value != strings.ToLower(value) || value == strings.Repeat("0", 32) {
		return nil, fmt.Errorf("%w: invalid identifier", kernel.ErrInvalidValue)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid identifier", kernel.ErrInvalidValue)
	}
	return decoded, nil
}

// optionalAgentText serves a task's assigned agent: the zero identity is an
// empty string, an unclaimed task any eligible worker may take.
func optionalAgentText(id kernel.AgentID) string {
	if id == (kernel.AgentID{}) {
		return ""
	}
	return id.String()
}

// parseOptionalAgentID reads a task's assigned agent: empty means any
// eligible worker in the project.
func parseOptionalAgentID(value string) (kernel.AgentID, error) {
	if value == "" {
		return kernel.AgentID{}, nil
	}
	return decodeID(value, kernel.AgentIDFromBytes)
}

func parseAgentRole(value string) (kernel.AgentRole, error) {
	switch value {
	case "worker":
		return kernel.RoleWorker, nil
	case "orchestrator":
		return kernel.RoleOrchestrator, nil
	default:
		return 0, fmt.Errorf("%w: invalid agent role", kernel.ErrInvalidValue)
	}
}
