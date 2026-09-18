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
	"strings"
	"testing"
	"time"
)

func TestConnectionFlowAndDeniedData(t *testing.T) {
	secret := strings.Repeat("ab", 32)
	digest := sha256.Sum256([]byte(secret))
	id := hex.EncodeToString(digest[:])
	denied := false
	var operations []string
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		operations = append(operations, request.Method+" "+request.URL.RequestURI())
		if request.URL.Path == prefix {
			if request.Header.Get("Authorization") != "" {
				t.Error("creation received an existing credential")
			}
			_ = json.NewEncoder(out).Encode(map[string]any{"connection_id": id, "credential": secret, "authorization_url": "https://github.com/login/oauth/authorize?state=fixture", "expires_at": time.Now().Unix() + 600})
			return
		}
		if request.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("missing connection authentication")
		}
		if denied {
			out.WriteHeader(401)
			_, _ = out.Write([]byte(`{"state":"connected","repositories":[{"repository":"private/hidden"}]}`))
			return
		}
		switch {
		case strings.HasSuffix(request.URL.Path, "/installations"):
			_, _ = out.Write([]byte(`{"installations":[],"next_page":2}`))
		case strings.HasSuffix(request.URL.Path, "/repositories") && request.Method == http.MethodGet:
			_, _ = out.Write([]byte(`{"repositories":[{"id":7,"full_name":"org/repo","permissions":{"pull":true}}],"next_page":null}`))
		default:
			_ = json.NewEncoder(out).Encode(Status{ConnectionID: id, State: "connected", Repositories: []Delegation{}})
		}
	}))
	defer server.Close()
	client := NewClient()
	client.origin = server.URL
	ctx := context.Background()
	credential, authorization, err := client.Connect(ctx)
	if err != nil || authorization.ConnectionID != id {
		t.Fatalf("connect: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%v %#v", credential, credential), secret) {
		t.Fatal("credential formatting leaks secret")
	}
	encoded, _ := json.Marshal(credential)
	if string(encoded) != "{}" {
		t.Fatal("credential enters JSON")
	}
	if _, err := client.Status(ctx, credential); err != nil {
		t.Fatal(err)
	}
	installations, err := client.Installations(ctx, credential, 1)
	if err != nil || installations.NextPage == nil || *installations.NextPage != 2 {
		t.Fatal("lost filtered page continuation")
	}
	repositories, err := client.Repositories(ctx, credential, 3, 2)
	if err != nil || len(repositories.Items) != 1 || repositories.Items[0].Permissions.Push {
		t.Fatal("read-only discovery failed")
	}
	if err := client.Delegate(ctx, credential, []Delegation{{InstallationID: 3, RepositoryID: 7, Repository: "org/repo"}}); err != nil {
		t.Fatal(err)
	}
	denied = true
	status, err := client.Status(ctx, credential)
	if !errors.Is(err, ErrDenied) || len(status.Repositories) != 0 || status.State != "" {
		t.Fatal("denied response retained private state")
	}
	denied = false
	if err := client.Disconnect(ctx, credential); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 7 || operations[3] != "GET "+prefix+"/"+id+"/repositories?installation_id=3&page=2" {
		t.Fatal("unexpected connection operations")
	}
}

func TestClientRefusesRedirectsAndUnboundedReplies(t *testing.T) {
	for _, fixture := range []struct {
		status int
		body   string
		want   error
	}{
		{302, `{}`, ErrUnavailable}, {503, `{}`, ErrUnavailable}, {200, strings.Repeat("x", (1<<20)+1), ErrUnavailable},
		{200, `{`, ErrInvalid}, {403, `{"credential":"must not surface"}`, ErrDenied},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
			out.Header().Set("Location", "https://example.invalid/stolen")
			out.WriteHeader(fixture.status)
			_, _ = out.Write([]byte(fixture.body))
		}))
		client := NewClient()
		client.origin = server.URL
		_, _, err := client.Connect(context.Background())
		server.Close()
		if !errors.Is(err, fixture.want) {
			t.Fatalf("status %d: %v", fixture.status, err)
		}
	}
	client := NewClient()
	if client.http.Transport.(*http.Transport).Proxy != nil {
		t.Fatal("connection uses ambient proxy credentials")
	}
}
