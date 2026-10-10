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

// The numeric repository join is checked against the last observed delegation,
// which changes only through this host (Delegate, Connect, Disconnect). The
// Worker re-authorizes the GitHub user and repository on every MCP call
// (control-plane/src/connection.rs refresh_and_verify and authorize) and answers
// 401 on revocation, so revocation is proven here by the MCP endpoint's answer.
func TestHostMCPRechecksNumericIdentityAndRevocation(t *testing.T) {
	secret := strings.Repeat("ab", 32)
	digest := sha256.Sum256([]byte(secret))
	id := hex.EncodeToString(digest[:])
	remoteID, statusCode, calls, statuses := int64(7), 200, 0, 0
	payload := `{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("missing private credential")
		}
		if statusCode != 200 {
			out.WriteHeader(statusCode)
			return
		}
		switch {
		case strings.HasSuffix(request.URL.Path, "/mcp"):
			calls++
			_, _ = out.Write([]byte(payload))
		case request.Method == http.MethodPut:
			remoteID = 8
			_, _ = out.Write([]byte(`{}`))
		default:
			statuses++
			_ = json.NewEncoder(out).Encode(Status{State: "connected", User: &User{ID: 123, Login: "operator"}, ConnectionID: id, Repositories: []Delegation{{Repository: "team/repo", RepositoryID: remoteID, InstallationID: 1}}})
		}
	}))
	defer server.Close()
	host := &Host{client: NewClient(), connection: connectionRecord{ID: id, Secret: secret, Author: gitauthor.Identity{ID: 123, Login: "operator"}}}
	host.client.origin = server.URL
	request := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe_operation","arguments":{"repository":"team/repo","operation_id":"opaque-original-uuid"}}}`)
	if _, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 7}); err != nil || calls != 1 || statuses != 1 {
		t.Fatalf("initial request: %v calls=%d statuses=%d", err, calls, statuses)
	}
	payload = `{"jsonrpc":"2.0","id":1,"result":{"body":"` + strings.Repeat("x", 1<<20) + `"}}`
	if result, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 7}); err != nil || len(result) <= 1<<20 || statuses != 1 {
		t.Fatalf("bounded large issue page reused the observed delegation: %v statuses=%d", err, statuses)
	}
	if err := host.Delegate(context.Background(), []Delegation{{Repository: "team/repo", RepositoryID: 8, InstallationID: 1}}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 7}); !errors.Is(err, ErrDenied) || calls != 2 || statuses != 2 {
		t.Fatalf("replacement repository: %v calls=%d statuses=%d", err, calls, statuses)
	}
	if _, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 8}); err != nil || calls != 3 || statuses != 2 {
		t.Fatalf("replacement repository joined by its own id: %v calls=%d statuses=%d", err, calls, statuses)
	}
	statusCode = 401
	if result, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 8}); !errors.Is(err, ErrDenied) || len(result) != 0 {
		t.Fatal("revoked request was not denied")
	}
	statusCode = 200
	if _, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 8}); err != nil || statuses != 3 {
		t.Fatalf("a failed call did not re-observe the delegation: %v statuses=%d", err, statuses)
	}
	host.connection = connectionRecord{Disabled: true}
	if !host.CustomerMode() {
		t.Fatal("disconnected customer mode fell back")
	}
	if _, err := host.MCP(context.Background(), request, map[string]uint64{}); !errors.Is(err, ErrDenied) {
		t.Fatal("disabled host called broker")
	}
}
