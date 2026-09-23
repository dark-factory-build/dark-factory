//go:build darwin || linux

package daemon

import (
	"strings"
	"testing"
)

func TestReviewProviderEnvironmentExcludesProviderAndGitCredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	t.Setenv("GH_TOKEN", "github-secret")
	t.Setenv("SSH_AUTH_SOCK", "/private/socket")
	t.Setenv("PATH", "/usr/bin")

	environment := strings.Join(filteredReviewEnvironment(), "\n")
	for _, secret := range []string{"openai-secret", "anthropic-secret", "github-secret", "/private/socket"} {
		if strings.Contains(environment, secret) {
			t.Fatalf("review environment retained %q", secret)
		}
	}
	if !strings.Contains(environment, "PATH=/usr/bin") {
		t.Fatal("review environment dropped the provider path")
	}
}

func TestSafeReviewPathRefusesCheckoutMetadataAndTraversal(t *testing.T) {
	for _, path := range []string{".git/config", "../outside", "/absolute", ""} {
		if _, err := safeReviewPath(path); err == nil {
			t.Fatalf("safeReviewPath accepted %q", path)
		}
	}
	if _, err := safeReviewPath("internal/daemon/review.go"); err != nil {
		t.Fatalf("safeReviewPath refused ordinary path: %v", err)
	}
}
