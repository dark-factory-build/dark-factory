//go:build darwin || linux

package api

import "testing"

func TestGitHubConnectionOperatorOnly(t *testing.T) {
	bearer := testCredential('G')
	for _, body := range []string{
		`{"method":"github_connection","params":{"action":"connect"}}`,
		`{"method":"github_connection","params":{"action":"confirm","code":"012345ABCD"}}`,
		`{"method":"github_connection","params":{"action":"repositories","installation_id":7,"page":1}}`,
		`{"method":"github_connection","params":{"action":"delegate","repositories":[{"installation_id":7,"repository_id":8,"repository":"org/repo"}]}}`,
	} {
		call, code := decodeCall(operatorDomain, bearer, []byte(body))
		if code != "" || call.Kind() != CallGitHubConnection {
			t.Fatalf("operator: %v", code)
		}
		if _, ok := call.GitHubConnectionInput(); !ok {
			t.Fatal("missing typed request")
		}
		if _, code := decodeCall(attemptDomain, bearer, []byte(body)); code != RemoteForbidden {
			t.Fatal("worker reached connection operations")
		}
	}
	for _, input := range []GitHubConnectionInput{
		{Action: "arbitrary"}, {Action: "status", Code: "012345ABCD"}, {Action: "confirm", Code: "bad"},
		{Action: "repositories", Page: 1}, {Action: "installations", Page: 1001}, {Action: "disconnect", InstallationID: 7},
	} {
		if ValidGitHubConnectionInput(input) {
			t.Fatalf("invalid request accepted: %s", input.Action)
		}
	}
}
