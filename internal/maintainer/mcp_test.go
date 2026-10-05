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

func TestHostMCPForwardsWithoutStatus(t *testing.T) {
	secret := strings.Repeat("ab", 32)
	digest := sha256.Sum256([]byte(secret))
	id := hex.EncodeToString(digest[:])
	calls := 0
	payload := `{"jsonrpc":"2.0","id":1,"result":{"body":"` + strings.Repeat("x", 1<<20) + `"}}`
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+secret || !strings.HasSuffix(request.URL.Path, "/"+id+"/mcp") {
			t.Errorf("unexpected request %s", request.URL.Path)
		}
		calls++
		_, _ = out.Write([]byte(payload))
	}))
	defer server.Close()
	host := &Host{client: NewClient(), connection: connectionRecord{ID: id, Secret: secret, Author: gitauthor.Identity{ID: 123, Login: "operator"}}}
	host.client.origin = server.URL
	request := json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"observe_operation","arguments":{"repository":"team/repo","operation_id":"opaque-original-uuid"}}}`)
	if result, err := host.MCP(context.Background(), request); err != nil || len(result) <= 1<<20 || calls != 1 {
		t.Fatalf("bounded large reply: %v calls=%d", err, calls)
	}
	host.connection = connectionRecord{Disabled: true}
	if !host.CustomerMode() {
		t.Fatal("disconnected customer mode fell back")
	}
	if _, err := host.MCP(context.Background(), request); !errors.Is(err, ErrDenied) || calls != 1 {
		t.Fatal("disabled host called broker")
	}
}
