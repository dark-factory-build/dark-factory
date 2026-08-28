//go:build !darwin

package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/changeworker"
	"github.com/dark-factory-build/dark-factory/internal/install"
)

func TestUnsupportedRuntimeFailsBeforeEffect(t *testing.T) {
	root := t.TempDir()
	managed, err := OpenRuntimeParent(context.Background(), install.MemberCapability{}, root)
	if !errors.Is(err, errUnsupported) || managed != nil {
		t.Fatalf("OpenRuntimeParent error = %v", err)
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
	if _, err := runtime.PublishWorkerConfig(context.Background(), changeworker.Config{}); !errors.Is(err, errUnsupported) {
		t.Fatalf("PublishWorkerConfig error = %v", err)
	}
}
