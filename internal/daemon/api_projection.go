package daemon

import (
	"encoding/hex"
	"fmt"
	"strings"

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
			ID: project.ID.String(), Name: project.Name, Revision: uint64(project.Revision.Int64()),
		})
	}
	for _, agent := range snapshot.Agents {
		result.Agents = append(result.Agents, api.AgentSummary{
			ID: agent.ID.String(), ProjectID: agent.ProjectID.String(), Name: agent.Name,
			Role: agent.Role, Paused: agent.Paused, Revision: uint64(agent.Revision.Int64()),
		})
	}
	for _, task := range snapshot.Tasks {
		result.Tasks = append(result.Tasks, api.TaskSummary{
			ID: task.ID.String(), ProjectID: task.ProjectID.String(), AssignedAgentID: task.AssignedAgentID.String(),
			Title: task.Title, Status: task.Status, Priority: task.Priority, Revision: uint64(task.Revision.Int64()),
		})
	}
	return result
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

func parseProjectID(value string) (kernel.ProjectID, error) {
	decoded, err := parseID(value)
	if err != nil {
		return kernel.ProjectID{}, err
	}
	return kernel.ProjectIDFromBytes(decoded)
}

func parseAgentID(value string) (kernel.AgentID, error) {
	decoded, err := parseID(value)
	if err != nil {
		return kernel.AgentID{}, err
	}
	return kernel.AgentIDFromBytes(decoded)
}

func parseTaskID(value string) (kernel.TaskID, error) {
	decoded, err := parseID(value)
	if err != nil {
		return kernel.TaskID{}, err
	}
	return kernel.TaskIDFromBytes(decoded)
}

func parseIncarnationID(value string) (kernel.IncarnationID, error) {
	decoded, err := parseID(value)
	if err != nil {
		return kernel.IncarnationID{}, err
	}
	return kernel.IncarnationIDFromBytes(decoded)
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
