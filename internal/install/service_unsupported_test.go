//go:build !darwin

package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
)

func TestServiceOperationsAreUnsupportedWithoutFilesystemEffects(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	userHome := filepath.Join(root, "user")
	if status, err := InspectService(context.Background(), home, userHome); status != (ServiceStatus{}) || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported status touched home: %v", err)
	}
	identity, ok := buildinfo.Expected("1.2.3", "1234567890abcdef1234567890abcdef12345678", "darwin/amd64")
	if !ok {
		t.Fatal("invalid fixture identity")
	}
	if bundle, err := OpenServiceBundle(filepath.Join(root, "factoryctl"), identity); bundle != nil || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("bundle = %v, %v", bundle, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "factoryctl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported bundle touched source: %v", err)
	}
}
