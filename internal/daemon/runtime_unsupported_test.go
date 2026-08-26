//go:build !darwin

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedRuntimeFailsBeforeEffect(t *testing.T) {
	root := t.TempDir()
	parent, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	managed, err := CreateRuntimeParent(parent)
	if !errors.Is(err, errUnsupported) || managed != nil {
		t.Fatalf("CreateRuntimeParent error = %v", err)
	}
	if _, err := CreateRuntime(managed, "run"); !errors.Is(err, errUnsupported) {
		t.Fatalf("CreateRuntime error = %v", err)
	}
	if _, err := AdoptRuntime(managed, "run"); !errors.Is(err, errUnsupported) {
		t.Fatalf("AdoptRuntime error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "run")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported runtime gained effect: %v", err)
	}
	var runtime Runtime
	if _, err := runtime.PublishAttemptToken(context.Background(), [32]byte{1}); !errors.Is(err, errUnsupported) {
		t.Fatalf("PublishAttemptToken error = %v", err)
	}
	if _, err := runtime.PublishWorkerConfig(context.Background(), workerConfig{}); !errors.Is(err, errUnsupported) {
		t.Fatalf("PublishWorkerConfig error = %v", err)
	}
}
