package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func TestToolchainReadRootsAcceptTrustedSystemInstallation(t *testing.T) {
	root := trustedSystemToolchainRoot
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("CommandLineTools is unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckToolchainReadRoots(root, ""); err != nil {
		t.Fatalf("trusted system installation: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		t.Fatalf("test installation is not root-owned: %#v", info.Sys())
	}
	for _, untrusted := range []struct {
		path string
		uid  uint32
	}{{filepath.Dir(root), 0}, {root + "/SDKs", 0}, {root, 501}} {
		if trustedSystemToolchainOwnership(untrusted.path, untrusted.uid) {
			t.Fatalf("accepted untrusted system installation %q uid %d", untrusted.path, untrusted.uid)
		}
	}
	if CheckToolchainReadRoots(root, root) == nil || CheckToolchainReadRoots(root, "", root) == nil {
		t.Fatal("system installation bypassed account/private exclusions")
	}
}
