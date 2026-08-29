package cloudflareadmin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testToken   = "fixture-token"
	testAccount = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

func TestParseCredentialsSelectsOnlyExactAssignments(t *testing.T) {
	content := []byte(strings.Join([]string{
		"CLOUDFLARE_API_TOKEN=" + testToken,
		"IGNORED_COMMAND=$(touch must-not-run)",
		"CLOUDFLARE_ACCOUNT_ID=" + testAccount,
		"export CLOUDFLARE_API_TOKEN=ignored",
	}, "\n"))
	got, err := parseCredentials(content)
	if err != nil {
		t.Fatal(err)
	}
	if got.apiToken != testToken || got.accountID != testAccount {
		t.Fatalf("unexpected selected credentials: %#v", got)
	}
}

func TestParseCredentialsRejectsMissingDuplicateAndMalformedValues(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{"missing token", "CLOUDFLARE_ACCOUNT_ID=" + testAccount, "API_TOKEN is missing"},
		{"missing account", "CLOUDFLARE_API_TOKEN=" + testToken, "ACCOUNT_ID is missing"},
		{"duplicate token", "CLOUDFLARE_API_TOKEN=a\nCLOUDFLARE_API_TOKEN=b\nCLOUDFLARE_ACCOUNT_ID=" + testAccount, "duplicate CLOUDFLARE_API_TOKEN"},
		{"duplicate account", "CLOUDFLARE_API_TOKEN=a\nCLOUDFLARE_ACCOUNT_ID=" + testAccount + "\nCLOUDFLARE_ACCOUNT_ID=" + testAccount, "duplicate CLOUDFLARE_ACCOUNT_ID"},
		{"uppercase account", "CLOUDFLARE_API_TOKEN=a\nCLOUDFLARE_ACCOUNT_ID=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "lowercase hexadecimal"},
		{"short account", "CLOUDFLARE_API_TOKEN=a\nCLOUDFLARE_ACCOUNT_ID=abc", "lowercase hexadecimal"},
		{"carriage return token", "CLOUDFLARE_API_TOKEN=a\r\nCLOUDFLARE_ACCOUNT_ID=" + testAccount, "invalid byte"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseCredentials([]byte(test.content))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestReadCredentialsRequiresOnePrivateStableRegularFile(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "valid")
	writeCredentialFixture(t, valid)
	got, err := readCredentials(valid)
	if err != nil {
		t.Fatal(err)
	}
	if got.apiToken != testToken || got.accountID != testAccount {
		t.Fatalf("unexpected credentials: %#v", got)
	}

	t.Run("mode", func(t *testing.T) {
		path := filepath.Join(root, "broad")
		writeCredentialFixture(t, path)
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatal(err)
		}
		if _, err := readCredentials(path); err == nil || !strings.Contains(err.Error(), "mode 0600") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		path := filepath.Join(root, "symlink")
		if err := os.Symlink(valid, path); err != nil {
			t.Fatal(err)
		}
		if _, err := readCredentials(path); err == nil {
			t.Fatal("symlink was accepted")
		}
	})

	t.Run("hardlink", func(t *testing.T) {
		source := filepath.Join(root, "hardlink-source")
		link := filepath.Join(root, "hardlink")
		writeCredentialFixture(t, source)
		if err := os.Link(source, link); err != nil {
			t.Fatal(err)
		}
		if _, err := readCredentials(source); err == nil || !strings.Contains(err.Error(), "exactly one link") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("directory", func(t *testing.T) {
		path := filepath.Join(root, "directory")
		if err := os.Mkdir(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readCredentials(path); err == nil || !strings.Contains(err.Error(), "regular file") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(root, "oversized")
		if err := os.WriteFile(path, []byte(strings.Repeat("x", maximumCredentialFileBytes+1)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readCredentials(path); err == nil || !strings.Contains(err.Error(), "invalid size") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestPublishLockRejectsSymlinkAndBroadDirectory(t *testing.T) {
	root := t.TempDir()
	link := filepath.Join(t.TempDir(), "common")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if lock, err := acquirePublishLock(link); err == nil {
		lock.Close()
		t.Fatal("symlinked lock authority was accepted")
	}

	broad := filepath.Join(t.TempDir(), "broad")
	if err := os.Mkdir(broad, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(broad, 0o770); err != nil {
		t.Fatal(err)
	}
	if lock, err := acquirePublishLock(broad); err == nil {
		lock.Close()
		t.Fatal("group-writable lock authority was accepted")
	}
}

func writeCredentialFixture(t *testing.T, path string) {
	t.Helper()
	content := "CLOUDFLARE_API_TOKEN=" + testToken + "\nCLOUDFLARE_ACCOUNT_ID=" + testAccount + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
