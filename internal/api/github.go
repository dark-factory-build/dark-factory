//go:build darwin || linux

package api

import (
	"context"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

// GitHubConnectionInput is the single operator contract used by CLI and paired
// administration browsers. It never accepts a broker credential or broker URL.
type GitHubConnectionInput struct {
	Action         string                  `json:"action"`
	Code           string                  `json:"code,omitempty"`
	Page           int                     `json:"page,omitempty"`
	InstallationID int64                   `json:"installation_id,omitempty"`
	Repositories   []maintainer.Delegation `json:"repositories,omitempty"`
}
type GitHubConnectionResult struct {
	State         string                    `json:"state"`
	Authorization *maintainer.Authorization `json:"authorization,omitempty"`
	Status        *maintainer.Status        `json:"status,omitempty"`
	Installations *maintainer.Installations `json:"installations,omitempty"`
	Repositories  *maintainer.Repositories  `json:"repositories,omitempty"`
}

func ValidGitHubConnectionInput(input GitHubConnectionInput) bool {
	if input.Action != "confirm" && input.Code != "" || input.Action != "delegate" && len(input.Repositories) != 0 {
		return false
	}
	if input.Action != "installations" && input.Action != "repositories" && input.Page != 0 {
		return false
	}
	if input.Action != "repositories" && input.InstallationID != 0 {
		return false
	}
	switch input.Action {
	case "connect", "status", "refresh", "disconnect":
		return true
	case "confirm":
		return len(input.Code) == 10 && strings.IndexFunc(input.Code, func(c rune) bool { return !(c >= '0' && c <= '9' || c >= 'A' && c <= 'F') }) < 0
	case "installations":
		return input.Page >= 1 && input.Page <= 1000
	case "repositories":
		return input.InstallationID > 0 && input.Page >= 1 && input.Page <= 1000
	case "delegate":
		if len(input.Repositories) > 100 {
			return false
		}
		for _, repository := range input.Repositories {
			parts := strings.Split(repository.Repository, "/")
			if repository.InstallationID <= 0 || repository.RepositoryID <= 0 || len(parts) != 2 || len(parts[0]) < 1 || len(parts[0]) > 39 || len(parts[1]) < 1 || len(parts[1]) > 100 || strings.IndexFunc(repository.Repository, func(c rune) bool {
				return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("/._-", c))
			}) >= 0 {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (client *OperatorClient) GitHubConnection(ctx context.Context, input GitHubConnectionInput) (GitHubConnectionResult, error) {
	if !ValidGitHubConnectionInput(input) {
		return GitHubConnectionResult{}, ErrInvalidInput
	}
	var result GitHubConnectionResult
	if err := client.client.call(ctx, "github_connection", input, &result); err != nil {
		return GitHubConnectionResult{}, err
	}
	switch result.State {
	case "ok", "denied", "unavailable", "invalid", "already_connected":
		return result, nil
	default:
		return GitHubConnectionResult{}, ErrProtocol
	}
}
