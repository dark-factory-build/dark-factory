//go:build !darwin

package install

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceOperationsAreUnsupportedWithoutFilesystemEffects(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if status, err := InspectService(context.Background(), home); status != (ServiceStatus{}) || !errors.Is(err, ErrUnsupported) {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported status touched home: %v", err)
	}
}
