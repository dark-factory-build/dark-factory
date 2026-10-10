package maintainer

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
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
// gate as disconnect. The broker verifies the owner's live GitHub access on
// every call, so the join reads the delegations of the last connected status
// rather than paying for that verification twice on the owner's quota
// (#1510); an unknown repository or a denial reads status again.
func (host *Host) MCP(ctx context.Context, request json.RawMessage, repositories map[string]uint64) (json.RawMessage, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	credential, err := host.credential()
	if err == nil && (host.delegations == nil || !delegated(host.delegations, repositories)) {
		credential, err = host.authorizeRepositories(ctx, repositories)
	}
	if err != nil {
		return nil, err
	}
	var call struct {
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(request, &call) != nil || !toolName.MatchString(call.Params.Name) {
		call.Params.Name = "other"
	}
	ctx = context.WithValue(ctx, operationKey{}, "mcp "+call.Params.Name)
	var response json.RawMessage
	if err := host.client.requestBounded(ctx, credential, http.MethodPost, prefix+"/"+credential.id+"/mcp", request, &response, 8<<20); err != nil {
		if errors.Is(err, ErrDenied) {
			host.delegations = nil
		}
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
	if status.State != "connected" || !delegated(status.Repositories, repositories) {
		return Credential{}, ErrDenied
	}
	return credential, nil
}

// toolName bounds what a caller's tool name can add to telemetry.
var toolName = regexp.MustCompile(`^[a-z_]{1,64}$`)

// delegated reports whether every repository name is delegated at its id.
func delegated(delegations []Delegation, repositories map[string]uint64) bool {
	for name, id := range repositories {
		if !slices.ContainsFunc(delegations, func(d Delegation) bool {
			return d.RepositoryID > 0 && uint64(d.RepositoryID) == id && strings.EqualFold(d.Repository, name)
		}) {
			return false
		}
	}
	return true
}
