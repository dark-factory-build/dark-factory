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

// MCP keeps the credential and the live numeric repository join under the
// same gate as disconnect. The broker repeats live GitHub authorization.
func (host *Host) MCP(ctx context.Context, request json.RawMessage, repositories map[string]uint64) (json.RawMessage, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	credential, err := host.authorizeRepositories(ctx, repositories)
	if err != nil {
		return nil, err
	}
	var response json.RawMessage
	if err := host.client.requestBounded(ctx, credential, http.MethodPost, prefix+"/"+credential.id+"/mcp", request, &response, 8<<20); err != nil {
		return nil, err
	}
	return response, nil
}

// AuthorizeRepositories verifies the retained live customer delegation without
// sending a broker MCP request.
func (host *Host) AuthorizeRepositories(ctx context.Context, repositories map[string]uint64) error {
	host.mu.Lock()
	defer host.mu.Unlock()
	_, err := host.authorizeRepositories(ctx, repositories)
	return err
}

func (host *Host) authorizeRepositories(ctx context.Context, repositories map[string]uint64) (Credential, error) {
	credential, err := host.credential()
	if err != nil {
		return Credential{}, err
	}
	status, err := host.status(ctx, credential)
	if err != nil {
		return Credential{}, err
	}
	if status.State != "connected" {
		return Credential{}, ErrDenied
	}
	for name, id := range repositories {
		found := false
		for _, delegated := range status.Repositories {
			if delegated.RepositoryID > 0 && uint64(delegated.RepositoryID) == id && strings.EqualFold(delegated.Repository, name) {
				found = true
				break
			}
		}
		if !found {
			return Credential{}, ErrDenied
		}
	}
	return credential, nil
}
