package browserprotocol

import (
	"strings"
	"testing"
)

func TestGitHubConnectionValidationKeepsThePrivateBridgeBounded(t *testing.T) {
	good := GitHubConnection{Action: "delegate", Repositories: []GitHubDelegation{{InstallationID: 7, RepositoryID: 9, Repository: "factory-org/worker"}}}
	if _, err := encodeControl(TypeGitHubConnection, "github-1", good); err != nil {
		t.Fatal(err)
	}
	for _, value := range []GitHubConnection{
		{Action: "confirm", Code: "abcdef1234"},
		{Action: "delegate", Page: 2, Repositories: good.Repositories},
		{Action: "delegate", Repositories: []GitHubDelegation{{InstallationID: 7, RepositoryID: 9, Repository: "factory-org/worker\n"}}},
	} {
		if _, err := encodeControl(TypeGitHubConnection, "github-1", value); err != ErrMalformed {
			t.Fatalf("invalid GitHub request accepted: %#v (%v)", value, err)
		}
	}
}

func TestGitHubConnectionResultRejectsUnboundedPrivateMetadata(t *testing.T) {
	value := GitHubConnectionResult{State: "ok", Status: &GitHubStatus{ConnectionID: "connection", State: "connected", Repositories: []GitHubDelegation{{InstallationID: 7, RepositoryID: 9, Repository: "factory-org/worker"}}}}
	if _, err := EncodeGitHubConnectionResult("github-1", value); err != nil {
		t.Fatal(err)
	}
	value.Status.Repositories[0].Repository = "factory-org/private\n"
	if _, err := EncodeGitHubConnectionResult("github-1", value); err != ErrMalformed {
		t.Fatalf("invalid GitHub result accepted: %v", err)
	}
}

func TestGitHubConnectionResultUsesDecimalExpiryAndAllowsDisconnectedEmptyID(t *testing.T) {
	value := GitHubConnectionResult{
		State:         "ok",
		Authorization: &GitHubAuthorization{ConnectionID: "connection", URL: "https://github.com/login/oauth/authorize", ExpiresAt: Decimal(123)},
		Status:        &GitHubStatus{State: "disconnected", Repositories: []GitHubDelegation{}},
	}
	encoded, err := EncodeGitHubConnectionResult("github-1", value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"expires_at":"123"`) {
		t.Fatalf("expiry was not encoded as a decimal string: %s", encoded)
	}
}

func TestGitHubConnectionResultAllowsPendingDisconnectWithoutAnActiveID(t *testing.T) {
	if _, err := EncodeGitHubConnectionResult("github-1", GitHubConnectionResult{
		State:  "ok",
		Status: &GitHubStatus{State: "disconnect_pending", Repositories: []GitHubDelegation{}},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGitHubConnectionResultValidatesNativeInstallationURL(t *testing.T) {
	value := GitHubConnectionResult{State: "ok", Installations: &GitHubInstallations{InstallationURL: "https://github.com/apps/factory-maintainer/installations/new"}}
	if _, err := EncodeGitHubConnectionResult("github-1", value); err != nil {
		t.Fatal(err)
	}
	for _, url := range []string{
		"https://github.com/apps/factory-maintainer/installations/new?next=evil",
		"https://github.com/apps/../evil/installations/new",
		"https://github.com/apps/../installations/new",
		"https://evil.example/apps/factory/installations/new",
		"https://github.com:444/apps/factory/installations/new",
		"javascript:alert(1)",
	} {
		value.Installations.InstallationURL = url
		if _, err := EncodeGitHubConnectionResult("github-1", value); err != ErrMalformed {
			t.Fatalf("invalid native installation URL accepted: %q (%v)", url, err)
		}
	}
}
