package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalCILeaseDirectoryRefusesLegacyAndForeignStorage(t *testing.T) {
	fixture := newSupervisorFixture(t, supervisorProgram(t, false, false))
	repository := filepath.Join(fixture.root, "repository")
	legacy := filepath.Join(repository, ".git", ".dark-factory-local-ci.lock")
	if err := os.Mkdir(legacy, 0700); err != nil {
		t.Fatal(err)
	}
	common, err := resolveGitCommonDir(context.Background(), fixture.spec.GitExecutable, repository)
	if err != nil || common != filepath.Join(repository, ".git") {
		t.Fatalf("repository Git directory = %q, %v", common, err)
	}
	if _, err := resolveGitCommonDir(context.Background(), fixture.spec.GitExecutable, filepath.Join(fixture.root, "changes")); err == nil {
		t.Fatal("a directory outside the repository resolved a Git directory")
	}
	if _, err := prepareLocalCILeaseDirectory(common); err == nil {
		t.Fatal("legacy holder was ignored")
	}
	if err := os.Remove(legacy); err != nil {
		t.Fatal(err)
	}
	directory, err := prepareLocalCILeaseDirectory(common)
	if err != nil {
		t.Fatal(err)
	}
	if directory != filepath.Join(repository, ".git", "dark-factory-local-ci") {
		t.Fatalf("broad lease authority: %q", directory)
	}
	if target, err := os.Readlink(legacy); err != nil || target != "dark-factory-local-ci/.dark-factory-local-ci.lock" {
		t.Fatalf("legacy barrier: %q, %v", target, err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repository, directory); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareLocalCILeaseDirectory(common); err == nil {
		t.Fatal("foreign lease directory symlink accepted")
	}
}
