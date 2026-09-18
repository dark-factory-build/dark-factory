package browserprotocol

import "testing"

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
