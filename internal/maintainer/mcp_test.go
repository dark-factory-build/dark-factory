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
	"time"
)

// The broker verifies live GitHub access on every MCP call, so the host joins
// repositories against the last connected status instead of reading status
// before each call (#1510), and reads it again after a denial.
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
		if strings.HasSuffix(request.URL.Path, "/mcp") {
			calls++
			_, _ = out.Write([]byte(payload))
			return
		}
		statuses++
		_ = json.NewEncoder(out).Encode(Status{State: "connected", User: &User{ID: 123, Login: "operator"}, ConnectionID: id, Repositories: []Delegation{{Repository: "team/repo", RepositoryID: remoteID, InstallationID: 1}}})
	}))
	defer server.Close()
	host := &Host{client: NewClient(), connection: connectionRecord{ID: id, Secret: secret, Author: gitauthor.Identity{ID: 123, Login: "operator"}}}
	host.client.origin = server.URL
	request := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe_operation","arguments":{"repository":"team/repo","operation_id":"opaque-original-uuid"}}}`)
	if err := host.AuthorizeRepositories(context.Background(), map[string]uint64{"team/repo": 7}); err != nil || calls != 0 {
		t.Fatalf("authorization unexpectedly called MCP: %v calls=%d", err, calls)
	}
	if _, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 7}); err != nil || calls != 1 || statuses != 1 {
		t.Fatalf("initial request: %v calls=%d statuses=%d", err, calls, statuses)
	}
	payload = `{"jsonrpc":"2.0","id":1,"result":{"body":"` + strings.Repeat("x", 1<<20) + `"}}`
	if result, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 7}); err != nil || len(result) <= 1<<20 || statuses != 1 {
		t.Fatalf("bounded large issue page: %v statuses=%d", err, statuses)
	}
	if _, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 8}); !errors.Is(err, ErrDenied) || calls != 2 || statuses != 2 {
		t.Fatalf("undelegated id: %v calls=%d statuses=%d", err, calls, statuses)
	}
	statusCode = 401
	if result, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 7}); !errors.Is(err, ErrDenied) || len(result) != 0 || calls != 2 {
		t.Fatalf("revoked request: %v calls=%d", err, calls)
	}
	// After a denial the next call reads status first, and a replaced
	// repository never reaches MCP.
	remoteID, statusCode = 8, 200
	if _, err := host.MCP(context.Background(), request, map[string]uint64{"team/repo": 7}); !errors.Is(err, ErrDenied) || calls != 2 || statuses != 3 {
		t.Fatalf("replacement repository: %v calls=%d statuses=%d", err, calls, statuses)
	}
	host.connection = connectionRecord{Disabled: true}
	if !host.CustomerMode() {
		t.Fatal("disconnected customer mode fell back")
	}
	if _, err := host.MCP(context.Background(), request, map[string]uint64{}); !errors.Is(err, ErrDenied) {
		t.Fatal("disabled host called broker")
	}
}

// The broker's x-ratelimit-* headers become the host's quota, reported on
// status; a reply without them keeps the last one. Each request names its
// operation for per-operation accounting (#1510).
func TestHostReportsTheBrokerQuotaAndNamesOperations(t *testing.T) {
	secret := strings.Repeat("ab", 32)
	digest := sha256.Sum256([]byte(secret))
	id := hex.EncodeToString(digest[:])
	remaining := "4900"
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		if remaining != "" {
			out.Header().Set("X-Ratelimit-Remaining", remaining)
			out.Header().Set("X-Ratelimit-Limit", "5000")
			out.Header().Set("X-Ratelimit-Reset", "2000000000")
		}
		if strings.HasSuffix(request.URL.Path, "/mcp") {
			_, _ = out.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
			return
		}
		_ = json.NewEncoder(out).Encode(Status{State: "connected", User: &User{ID: 123, Login: "operator"}, ConnectionID: id, Repositories: []Delegation{{Repository: "team/repo", RepositoryID: 7, InstallationID: 1}}})
	}))
	defer server.Close()
	host := &Host{client: NewClient(), connection: connectionRecord{ID: id, Secret: secret, Author: gitauthor.Identity{ID: 123, Login: "operator"}}}
	host.client.origin = server.URL
	var operations []string
	host.Instrument(func(client *http.Client) *http.Client {
		next := client.Transport
		return &http.Client{Transport: roundTrip(func(request *http.Request) (*http.Response, error) {
			operations = append(operations, Operation(request.Context()))
			return next.RoundTrip(request)
		})}
	})
	if _, ok := host.Quota(); ok {
		t.Fatal("a quota before any reply")
	}
	status, err := host.Status(context.Background())
	if err != nil || status.Quota == nil || *status.Quota != (Quota{Remaining: 4900, Limit: 5000, Reset: 2000000000}) || status.Quota.Low(time.Unix(1999999999, 0)) {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	remaining = "499"
	if _, err := host.MCP(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_pull_requests","arguments":{"repository":"team/repo"}}}`), map[string]uint64{"team/repo": 7}); err != nil {
		t.Fatal(err)
	}
	remaining = ""
	if _, err := host.MCP(context.Background(), json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"Not A Tool"}}`), map[string]uint64{"team/repo": 7}); err != nil {
		t.Fatal(err)
	}
	if quota, ok := host.Quota(); !ok || quota.Remaining != 499 || !quota.Low(time.Unix(1999999999, 0)) || quota.Low(time.Unix(2000000000, 0)) {
		t.Fatalf("quota = %+v", quota)
	}
	if (Quota{Remaining: 500, Limit: 5000, Reset: 2000000000}).Low(time.Unix(0, 0)) {
		t.Fatal("a tenth exactly is low")
	}
	if strings.Join(operations, ",") != "get connection,mcp list_pull_requests,mcp other" {
		t.Fatalf("operations = %q", operations)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
