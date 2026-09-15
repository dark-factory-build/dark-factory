package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToolchainReadRootsRejectAuthorityExpansion(t *testing.T) {
	for _, roots := range []string{"/", "/Users", "/Users/operator", "/opt/homebrew", "/opt/tools/node:", "/opt/tools/../secrets", "/opt/tools/node:/opt/tools/node", "/Users/operator/.ssh/keys", "/Users/operator/.SSH/keys", "/Users/operator/.codex/config", strings.Repeat("/opt/tools/", 256)} {
		if ValidToolchainReadRoots(roots) {
			t.Errorf("accepted broad or malformed roots %q", roots)
		}
	}
	for _, roots := range []string{"/private/users/operator", "/private/users/operator/.codex", "/private/factory/data", "/private/factory/data/child", "/private/factory"} {
		if ToolchainReadRootsAllowed(roots, "/private/users/operator", "/private/factory/data") {
			t.Errorf("accepted private root %q", roots)
		}
	}
	if !ToolchainReadRootsAllowed("/private/users/operator/tools/node", "/private/users/operator", "/private/factory") {
		t.Fatal("exact software root under account home refused")
	}
}

func TestToolchainReadRootsRequireCanonicalPrivateInstallation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	software := filepath.Join(root, "software")
	if err := os.Mkdir(software, 0700); err != nil {
		t.Fatal(err)
	}
	if err := CheckToolchainReadRoots(software, "/private/account"); err != nil {
		t.Fatalf("software root: %v", err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(software, alias); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{alias, software + "/missing"} {
		if CheckToolchainReadRoots(invalid, "") == nil {
			t.Fatalf("accepted %q", invalid)
		}
	}
	if err := os.Chmod(software, 0770); err != nil {
		t.Fatal(err)
	}
	if CheckToolchainReadRoots(software, "") == nil {
		t.Fatal("accepted group-writable software")
	}
}
