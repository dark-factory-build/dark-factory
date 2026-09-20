package maintainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/dark-factory-build/dark-factory/internal/gitauthor"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHostMCPRechecksNumericIdentityAndRevocation(t *testing.T) {
	secret := strings.Repeat("ab", 32)
	digest := sha256.Sum256([]byte(secret))
	id := hex.EncodeToString(digest[:])
	remoteID, statusCode, calls := int64(7), 200, 0
	payload := `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("missing private credential")
		}
		if strings.HasSuffix(request.URL.Path, "/mcp") {
			calls++
			_, _ = out.Write([]byte(payload))
			return
		}
		if statusCode != 200 {
			out.WriteHeader(statusCode)
			return
		}
		_ = json.NewEncoder(out).Encode(Status{State: "connected", User: &User{ID: 123, Login: "operator"}, ConnectionID: id, Repositories: []Delegation{{Repository: "team/repo", RepositoryID: remoteID, InstallationID: 1}}})
	}))
	defer server.Close()
	host := &Host{client: NewClient(), connection: connectionRecord{ID: id, Secret: secret, Author: gitauthor.Identity{ID: 123, Login: "operator"}}}
	host.client.origin = server.URL
	request := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe_operation","arguments":{"repository":"team/repo","operation_id":"opaque-original-uuid"}}}`)
	if err := host.AuthorizeRepositories(context.Background(), map[string]uint64{"team/repo": 7}); err != nil || calls != 0 {
		t.Fatalf("authorization unexpectedly called MCP: %v calls=%d", err, calls)
	}
	if _, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 7}); err != nil || calls != 1 {
		t.Fatalf("initial request: %v calls=%d", err, calls)
	}
	payload = `{"jsonrpc":"2.0","id":1,"result":{"body":"` + strings.Repeat("x", 1<<20) + `"}}`
	if result, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 7}); err != nil || len(result) <= 1<<20 {
		t.Fatalf("bounded large issue page: %v", err)
	}
	remoteID = 8
	if _, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 7}); !errors.Is(err, ErrDenied) || calls != 2 {
		t.Fatalf("replacement repository: %v calls=%d", err, calls)
	}
	remoteID, statusCode = 7, 401
	if result, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 7}); !errors.Is(err, ErrDenied) || len(result) != 0 || calls != 2 {
		t.Fatal("revoked request reached MCP")
	}
	host.connection = connectionRecord{Disabled: true}
	if !host.CustomerMode() {
		t.Fatal("disconnected customer mode fell back")
	}
	if _, err := host.MCP(context.Background(), request, map[string]uint64{}); !errors.Is(err, ErrDenied) {
		t.Fatal("disabled host called broker")
	}
}
