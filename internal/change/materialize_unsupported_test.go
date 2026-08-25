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

func TestUnsupportedChangeAPIsFailBeforeFilesystemOrBlobEffect(t *testing.T) {
	parent := t.TempDir()
	prepared, err := Prepare(context.Background(), parent, "target", "stage")
	assertUnsupported(t, err)
	if prepared != nil {
		t.Fatal("unsupported Prepare returned a handle")
	}
	if _, err := InspectPublished(context.Background(), parent, "target", StageIdentity{}, ObjectFormat(1), ObjectID{}); err == nil {
		t.Fatal("unsupported InspectPublished succeeded")
	}
	if err := RemoveRecordedTree(context.Background(), parent, "target", StageIdentity{}); err == nil {
		t.Fatal("unsupported RemoveRecordedTree succeeded")
	}
	for _, name := range []string{"target", "stage"} {
		if _, err := os.Lstat(filepath.Join(parent, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsupported API caused filesystem effect at %s: %v", name, err)
		}
	}
}

func assertUnsupported(t testing.TB, err error) {
	t.Helper()
	var unsupported *UnsupportedError
	if !errors.As(err, &unsupported) || unsupported.Platform != runtime.GOOS {
		t.Fatalf("got %T %v, want UnsupportedError for %s", err, err, runtime.GOOS)
	}
}
