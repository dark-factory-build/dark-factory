package daemon

import (
	"context"
	"errors"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/gitauthor"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/linear"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

// ConfigureMaintainer binds the private credential owner before listeners open.
func (daemon *Daemon) ConfigureMaintainer(home *install.OperationalHome) error {
	host, err := maintainer.OpenHost(home)
	if err != nil {
		return err
	}
	daemon.github = host
	daemon.linear, err = linear.Open(home)
	return err
}

// GitHubConnection is shared by the authenticated operator transport and the
// paired browser's administration gate. It never returns connection credentials.
func (daemon *Daemon) GitHubConnection(ctx context.Context, input api.GitHubConnectionInput) api.GitHubConnectionResult {
	result := api.GitHubConnectionResult{State: "ok"}
	if !api.ValidGitHubConnectionInput(input) {
		result.State = "invalid"
		return result
	}
	if daemon.github == nil {
		result.State = "unavailable"
		return result
	}
	var err error
	switch input.Action {
	case "connect":
		daemon.maintainerMu.Lock()
		defer daemon.maintainerMu.Unlock()
		if !daemon.github.CustomerMode() {
			runs, readErr := daemon.store.RecoverableRuns(ctx)
			if readErr != nil {
				return api.GitHubConnectionResult{State: "unavailable"}
			}
			for _, run := range runs {
				if run.Run.Role == kernel.RoleOrchestrator {
					return api.GitHubConnectionResult{State: "legacy_overseers_running"}
				}
			}
		}
		value, callErr := daemon.github.Connect(ctx)
		err = callErr
		result.Authorization = &value
	case "confirm":
		err = daemon.github.Confirm(ctx, input.Code)
	case "status", "refresh":
		value, callErr := daemon.github.Status(ctx)
		err = callErr
		result.Status = &value
	case "installations":
		value, callErr := daemon.github.Installations(ctx, input.Page)
		err = callErr
		result.Installations = &value
	case "repositories":
		value, callErr := daemon.github.Repositories(ctx, input.InstallationID, input.Page)
		err = callErr
		result.Repositories = &value
	case "delegate":
		err = daemon.github.Delegate(ctx, input.Repositories)
	case "disconnect":
		err = daemon.github.Disconnect(ctx)
	}
	if err == nil {
		return result
	}
	// Failed authorization never carries partial or previously cached metadata.
	switch {
	case errors.Is(err, install.ErrBusy):
		return api.GitHubConnectionResult{State: "legacy_overseers_running"}
	case errors.Is(err, maintainer.ErrDenied):
		return api.GitHubConnectionResult{State: "denied"}
	case errors.Is(err, maintainer.ErrInvalid):
		return api.GitHubConnectionResult{State: "invalid"}
	case errors.Is(err, maintainer.ErrAlreadyConnected):
		return api.GitHubConnectionResult{State: "already_connected"}
	default:
		return api.GitHubConnectionResult{State: "unavailable"}
	}
}

func (daemon *Daemon) gitAuthor(ctx context.Context) gitauthor.Identity {
	if daemon.github == nil {
		return gitauthor.Identity{}
	}
	return daemon.github.GitAuthor(ctx)
}
