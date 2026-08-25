//go:build !darwin

package change

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestUnsupportedMaterializeFailsBeforeFilesystemOrBlobEffect(t *testing.T) {
	parent := t.TempDir()
	sourceCalled := false
	_, err := Materialize(context.Background(), parent, "must-not-exist", Manifest{}, func(context.Context, ObjectID) ([]byte, error) {
		sourceCalled = true
		return nil, nil
	})
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) || unsupported.Platform != runtime.GOOS {
		t.Fatalf("got %T %v, want UnsupportedError for %s", err, err, runtime.GOOS)
	}
	if sourceCalled {
		t.Fatal("unsupported platform called blob source")
	}
	if _, err := os.Lstat(filepath.Join(parent, "must-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsupported platform caused filesystem effect: %v", err)
	}
}
