package daemon

import (
	"bytes"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestTrustedIntakeCommitAuthorUsesProviderIdentity(t *testing.T) {
	repository, err := kernel.RepositoryIDFromBytes(bytes.Repeat([]byte{2}, kernel.IDBytes))
	if err != nil {
		t.Fatal(err)
	}
	accepted := kernel.IntakeAcceptance{SourceRepository: "feed/issues", RepositoryID: repository}
	source := kernel.IntakeSource{TargetRepositoryID: repository, GitHubRepositoryName: "feed/issues", Policy: kernel.IntakePolicyTrustedAuthors, TrustedGitHubLogins: []string{"Maintainer"}}
	for _, test := range []struct {
		name, login, kind string
		authenticated     bool
		want              bool
	}{
		{"trusted authenticated user", "maintainer", "User", true, true},
		{"attributed but unsigned user", "maintainer", "User", false, false},
		{"bot only", "maintainer", "Bot", true, false},
		{"missing identity", "", "User", true, false},
		{"untrusted user", "other", "User", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := trustedIntakeCommitAuthor([]kernel.IntakeSource{source}, accepted, test.login, test.kind, test.authenticated); got != test.want {
				t.Fatalf("trustedIntakeCommitAuthor(%q, %q, %t)=%v, want %v", test.login, test.kind, test.authenticated, got, test.want)
			}
		})
	}
}
