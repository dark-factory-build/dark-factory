package maintainer

import (
	"context"
	"encoding/json"
	"net/http"
)

// CustomerMode stays true after disconnect. An opted-in home never inherits
// the legacy owner's credential path again.
func (host *Host) CustomerMode() bool {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.connection.ID != "" || host.connection.Disabled
}

// MCP forwards with the retained credential. The broker authorizes every
// tools/call live against its delegated repositories; that is the only check.
func (host *Host) MCP(ctx context.Context, request json.RawMessage) (json.RawMessage, error) {
	host.mu.Lock()
	defer host.mu.Unlock()
	credential, err := host.credential()
	if err != nil {
		return nil, err
	}
	var response json.RawMessage
	if err := host.client.requestBounded(ctx, credential, http.MethodPost, prefix+"/"+credential.id+"/mcp", request, &response, 8<<20); err != nil {
		return nil, err
	}
	return response, nil
}
