package maintainer

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// CustomerMode stays true after disconnect. An opted-in home never inherits
// the legacy owner's credential path again.
func (host *Host) CustomerMode() bool {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.connection.ID != "" || host.connection.Disabled
}

// MCP keeps the credential and the numeric repository join under the same
// gate as disconnect. The join uses the last observed delegation, which only
// this host can change; the broker repeats live GitHub authorization of the
// user and repository on every call (control-plane/src/connection.rs).
func (host *Host) MCP(ctx context.Context, request json.RawMessage, repositories map[string]uint64) (json.RawMessage, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	credential, err := host.authorizeRepositories(ctx, repositories)
	if err != nil {
		return nil, err
	}
	var response json.RawMessage
	if err := host.client.requestBounded(ctx, credential, http.MethodPost, prefix+"/"+credential.id+"/mcp", request, &response, 8<<20); err != nil {
		host.delegations = nil // re-observe a revoked or changed connection
		return nil, err
	}
	return response, nil
}

func (host *Host) authorizeRepositories(ctx context.Context, repositories map[string]uint64) (Credential, error) {
	credential, err := host.credential()
	if err != nil {
		return Credential{}, err
	}
	if host.delegations == nil || !delegated(host.delegations, repositories) {
		status, err := host.status(ctx, credential)
		if err != nil {
			return Credential{}, err
		}
		if status.State != "connected" || !delegated(status.Repositories, repositories) {
			return Credential{}, ErrDenied
		}
	}
	return credential, nil
}

func delegated(delegations []Delegation, repositories map[string]uint64) bool {
	for name, id := range repositories {
		found := false
		for _, delegated := range delegations {
			if delegated.RepositoryID > 0 && uint64(delegated.RepositoryID) == id && strings.EqualFold(delegated.Repository, name) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
