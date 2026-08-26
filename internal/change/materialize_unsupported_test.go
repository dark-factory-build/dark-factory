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
	adopted, err := AdoptPrepared(context.Background(), parent, "target", "stage")
	assertUnsupported(t, err)
	if adopted != nil {
		t.Fatal("unsupported AdoptPrepared returned a handle")
	}
	if _, err := InspectPublished(context.Background(), parent, "target", StageIdentity{}, ObjectFormat(1), ObjectID{}); err == nil {
		t.Fatal("unsupported InspectPublished succeeded")
	}
	if verified, err := (Published{}).Reinspect(context.Background()); err == nil || verified != nil {
		t.Fatalf("unsupported Reinspect = %+v, %v", verified, err)
	}
	verified := &VerifiedPublished{}
	if _, err := verified.Facts(); err == nil {
		t.Fatal("unsupported verified Facts succeeded")
	}
	if duplicate, err := verified.DuplicateDirectory(context.Background()); err == nil || duplicate != nil {
		t.Fatalf("unsupported verified duplicate = %+v, %v", duplicate, err)
	}
	if err := verified.Close(); err == nil {
		t.Fatal("unsupported verified Close succeeded")
	}
	if err := RemoveRecordedTree(context.Background(), parent, "target", StageIdentity{}); err == nil {
		t.Fatal("unsupported RemoveRecordedTree succeeded")
	}
	repositoryIdentity, err := NewRepositoryIdentity(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SelectGit(context.Background(), filepath.Join(parent, "git"), filepath.Join(parent, "repository"), "HEAD", repositoryIdentity); err == nil {
		t.Fatal("unsupported SelectGit succeeded")
	}
	if _, err := OpenGitBlobs(context.Background(), filepath.Join(parent, "git"), filepath.Join(parent, "repository"), Selection{}); err == nil {
		t.Fatal("unsupported OpenGitBlobs succeeded")
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
