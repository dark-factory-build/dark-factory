//go:build darwin

package changeworker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRetargetSessionTokenReplacesCurrentCredentialAtomically(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "runtime", AttemptTokenName)
	if err := os.Mkdir(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("01234567890123456789012345678901"), 0o600); err != nil {
		t.Fatal(err)
	}
	locator := filepath.Join(root, "session", ".dark-factory-attempt-token")
	if err := os.Mkdir(filepath.Dir(locator), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := retargetSessionToken(locator, target); err != nil {
		t.Fatal(err)
	}

	if got, err := os.ReadFile(locator); err != nil || string(got) != "01234567890123456789012345678901" {
		t.Fatalf("initial session token = %q, %v", got, err)
	}
	if err := os.WriteFile(target, []byte("12345678901234567890123456789012"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := retargetSessionToken(locator, target); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(locator); err != nil || string(got) != "12345678901234567890123456789012" {
		t.Fatalf("rotated session token = %q, %v", got, err)
	}
	info, err := os.Lstat(locator)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("session token metadata = %#v, %v", info, err)
	}
}
