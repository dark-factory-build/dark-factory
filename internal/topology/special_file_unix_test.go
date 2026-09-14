//go:build darwin || linux

package topology

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestBuildDoesNotOpenSpecialGitFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"source/file.txt": "safe"})
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, ".git", "HEAD"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertCompletes(t, func() error {
		snapshot, err := Build(context.Background(), root, "")
		if err == nil && snapshot.SourceRevision != "" {
			t.Errorf("source revision = %q, want empty", snapshot.SourceRevision)
		}
		return err
	})
}

func assertCompletes(t *testing.T, operation func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- operation() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("topology read blocked on a special file")
	}
}
