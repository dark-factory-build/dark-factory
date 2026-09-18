//go:build darwin

package maintainer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/install"
)

func TestHostDisconnectPersistsBeforeRemoteRevocation(t *testing.T) {
	parent, err := os.MkdirTemp("/private/tmp", "df-connection-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	path := filepath.Join(parent, "home")
	ctx := context.Background()
	if _, err := install.Init(ctx, path); err != nil {
		t.Fatal(err)
	}
	home, err := install.OpenOperationalHome(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	host, err := OpenHost(home)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("cd", 32)
	digest := sha256.Sum256([]byte(secret))
	id := hex.EncodeToString(digest[:])
	offline := false
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		calls++
		if offline {
			out.WriteHeader(503)
			return
		}
		if request.URL.Path == prefix {
			_ = json.NewEncoder(out).Encode(map[string]any{"connection_id": id, "credential": secret, "authorization_url": "https://github.com/login/oauth/authorize?state=fixture", "expires_at": time.Now().Unix() + 600})
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("missing scoped bearer")
		}
		_, _ = out.Write([]byte(`{"state":"disconnected"}`))
	}))
	defer server.Close()
	host.client.origin = server.URL
	if _, err := host.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if !host.CustomerMode() {
		t.Fatal("pending connection is not customer mode")
	}
	if strings.Contains(fmt.Sprintf("%v %#v", host, host), secret) {
		t.Fatal("host exposes credential")
	}
	offline = true
	if err := host.Disconnect(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatal("offline disconnect not reported")
	}
	before := calls
	if _, err := host.Installations(ctx, 1); !errors.Is(err, ErrDenied) || calls != before {
		t.Fatal("disabled connection performed new remote operation")
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
	home, err = install.OpenOperationalHome(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	host, err = OpenHost(home)
	if err != nil {
		t.Fatal(err)
	}
	host.client.origin = server.URL
	status, err := host.Status(ctx)
	if err != nil || status.State != "disconnect_pending" || len(status.Repositories) != 0 || calls != before {
		t.Fatal("restart lost pending revocation")
	}
	offline = false
	if err := host.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	status, err = host.Status(ctx)
	if err != nil || status.State != "disconnected" {
		t.Fatal("revocation retry did not settle")
	}
	if !host.CustomerMode() {
		t.Fatal("disconnect lost customer mode")
	}
	restarted, err := OpenHost(home)
	if err != nil || !restarted.CustomerMode() {
		t.Fatal("restart reenabled legacy mode")
	}
	restarted.client.origin = server.URL
	if _, err := restarted.Connect(ctx); err != nil || !restarted.CustomerMode() {
		t.Fatalf("reconnect: %v", err)
	}
}
