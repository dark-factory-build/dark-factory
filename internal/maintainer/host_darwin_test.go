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
	"sync/atomic"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/gitauthor"
	"github.com/dark-factory-build/dark-factory/internal/install"
	"golang.org/x/sys/unix"
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

func TestHostInitialConnectFencesLegacyControllerUntilCredentialSaved(t *testing.T) {
	parent, err := os.MkdirTemp("/private/tmp", "df-connect-fence-")
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
	defer home.Close()
	host, err := OpenHost(home)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(path+".autonomy.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	calls := 0
	secret := strings.Repeat("ab", 32)
	digest := sha256.Sum256([]byte(secret))
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		calls++
		if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); !errors.Is(err, unix.EWOULDBLOCK) {
			t.Error("activation released legacy fence before credential save")
		}
		_ = json.NewEncoder(out).Encode(map[string]any{"connection_id": hex.EncodeToString(digest[:]), "credential": secret, "authorization_url": "https://github.com/login/oauth/authorize?state=fixture", "expires_at": time.Now().Unix() + 600})
	}))
	defer server.Close()
	host.client.origin = server.URL
	if _, err := host.Connect(ctx); !errors.Is(err, install.ErrBusy) || calls != 0 || host.CustomerMode() {
		t.Fatalf("busy legacy pass permitted connect: %v calls=%d", err, calls)
	}
	if _, err := os.Stat(filepath.Join(path, "maintainer.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("blocked connect wrote credential")
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".autonomy.lock", 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Connect(ctx); err == nil || calls != 0 {
		t.Fatal("unsafe legacy lock permitted connect")
	}
	if err := os.Chmod(path+".autonomy.lock", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".autonomy.lock", path+".saved-lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".saved-lock", path+".autonomy.lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Connect(ctx); err == nil || calls != 0 {
		t.Fatal("symlink legacy lock permitted connect")
	}
	if err := os.Remove(path + ".autonomy.lock"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".saved-lock", path+".autonomy.lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	if !host.CustomerMode() || calls != 1 {
		t.Fatal("retry did not establish customer mode")
	}
	if _, err := os.Stat(filepath.Join(path, "maintainer.json")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal("durable connect retained fence", err)
	}
}

func TestHostGitAuthorPersistsVerifiedUserAndDisconnect(t *testing.T) {
	parent, err := os.MkdirTemp("/private/tmp", "df-author-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(parent)
	path := filepath.Join(parent, "home")
	ctx := context.Background()
	if _, err := install.Init(ctx, path); err != nil {
		t.Fatal(err)
	}
	home, err := install.OpenOperationalHome(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	host, err := OpenHost(home)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("ab", 32)
	digest := sha256.Sum256([]byte(secret))
	id := hex.EncodeToString(digest[:])
	// Existing credential records have no cached author and fill it on first use.
	if err := host.save(connectionRecord{ID: id, Secret: secret}); err != nil {
		t.Fatal(err)
	}
	var response atomic.Value
	response.Store(User{ID: 123, Login: "operator"})
	var offline atomic.Bool
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if offline.Load() {
			out.WriteHeader(503)
			return
		}
		user := response.Load().(User)
		_ = json.NewEncoder(out).Encode(Status{State: "connected", ConnectionID: id, User: &user, Repositories: []Delegation{}})
	}))
	defer server.Close()
	host.client.origin = server.URL
	want := gitauthor.Identity{ID: 123, Login: "operator"}
	if got := host.GitAuthor(ctx); got != want || calls.Load() != 1 {
		t.Fatalf("verified author: %+v, calls=%d", got, calls.Load())
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
	offline.Store(true)
	if got := host.GitAuthor(ctx); got != want || calls.Load() != 1 {
		t.Fatalf("offline cached author: %+v, calls=%d", got, calls.Load())
	}
	offline.Store(false)
	response.Store(User{ID: 123, Login: "renamed"})
	if _, err := host.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if got := host.GitAuthor(ctx); got.Login != "renamed" || got.ID != 123 {
		t.Fatal(got)
	}
	response.Store(User{ID: 456, Login: "other"})
	if _, err := host.Status(ctx); !errors.Is(err, ErrInvalid) {
		t.Fatalf("changed user accepted: %v", err)
	}
	response.Store(User{ID: 123, Login: "injected\nname"})
	if _, err := host.Status(ctx); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid author accepted: %v", err)
	}
	offline.Store(true)
	if err := host.Disconnect(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	before := calls.Load()
	if got := host.GitAuthor(ctx); got != (gitauthor.Identity{}) || calls.Load() != before {
		t.Fatalf("disabled author: %+v", got)
	}
	restarted, err := OpenHost(home)
	if err != nil {
		t.Fatal(err)
	}
	if got := restarted.GitAuthor(ctx); got != (gitauthor.Identity{}) {
		t.Fatalf("restart credited disconnected user: %+v", got)
	}
}
