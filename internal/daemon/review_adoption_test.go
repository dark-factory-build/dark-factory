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
		want              bool
	}{
		{"trusted user", "maintainer", "User", true},
		{"bot only", "maintainer", "Bot", false},
		{"missing identity", "", "User", false},
		{"untrusted user", "other", "User", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := trustedIntakeCommitAuthor([]kernel.IntakeSource{source}, accepted, test.login, test.kind); got != test.want {
				t.Fatalf("trustedIntakeCommitAuthor(%q, %q)=%v, want %v", test.login, test.kind, got, test.want)
			}
		})
	}
}
