package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestDispatchFixtureTraversesUnreadableAncestors(t *testing.T) {
	const childEnv = "DARK_FACTORY_TEST_ANCESTOR_SANDBOX"
	if mode := os.Getenv(childEnv); mode != "" {
		ancestors := []string{"/private", "/private/tmp"}
		if mode == "generated" {
			ancestors = []string{"/Users"}
		}
		for _, path := range ancestors {
			if _, err := os.ReadDir(path); err == nil {
				t.Fatalf("sandbox unexpectedly lists ancestor %s", path)
			}
		}
		if secret := os.Getenv("DARK_FACTORY_TEST_ANCESTOR_SECRET"); secret != "" {
			if _, err := os.ReadFile(secret); err == nil {
				t.Fatal("sandbox unexpectedly reads sibling secret")
			}
		}
		parent := "/private/tmp"
		if mode != "generated" {
			parent = os.TempDir()
		}
		fixture := newDispatchFixtureAt(t, parent)
		active := prepareActiveAttempt(t, fixture, 101)
		done := fixture.serve(t)
		if _, err := active.client.Succeed(context.Background(), "sandbox outcome"); err != nil {
			t.Fatal(err)
		}
		waitDispatch(t, done)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	// Real providers receive a private TMPDIR. Keep scratch and fixture cleanup
	// inside that grant rather than inheriting a host/CI shared temp root.
	privateTemp, err := os.MkdirTemp("/private/tmp", "df-ancestor-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(privateTemp); err != nil {
			t.Error(err)
		}
	})
	secret := filepath.Join(t.TempDir(), "unrelated-secret")
	if err := os.WriteFile(secret, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	secret, err = filepath.EvalSymlinks(secret)
	if err != nil {
		t.Fatal(err)
	}
	// Only directory data is denied: descendants remain authorized. This
	// separates traversal from listing without loosening any live profile.
	profile := fmt.Sprintf(`(version 1)(allow default)
(deny file-read-data (literal "/private") (literal "/private/tmp") (literal %q))`, secret)
	cmd := exec.Command("/usr/bin/sandbox-exec", "-p", profile, executable, "-test.run=^TestDispatchFixtureTraversesUnreadableAncestors$", "-test.count=1")
	cmd.Env = append(os.Environ(), childEnv+"=1", "DARK_FACTORY_TEST_ANCESTOR_SECRET="+secret, "TMPDIR="+privateTemp)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("private fixture sandbox: %v\n%s", err, output)
	}
	entries, err := os.ReadDir(privateTemp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private fixture left scratch state: %v, %v", entries, err)
	}
}
