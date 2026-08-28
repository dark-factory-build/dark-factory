//go:build darwin

package install

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func TestOperationalHomeLeasesPopulatedGoHome(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	store, err := kernel.Open(context.Background(), filepath.Join(homePath, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	runtimeSentinel := []byte("runtime descendant must not be read")
	changeSentinel := []byte("change descendant must not be read")
	if err := os.WriteFile(filepath.Join(homePath, runtimesName, "sentinel"), runtimeSentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(homePath, changesName, "sentinel"), changeSentinel, 0o600); err != nil {
		t.Fatal(err)
	}

	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	if home.DatabasePath() != filepath.Join(homePath, databaseName) || home.OperatorTokenPath() != filepath.Join(homePath, tokenName) || home.RuntimesPath() != filepath.Join(homePath, runtimesName) || home.ChangesPath() != filepath.Join(homePath, changesName) {
		t.Fatal("operational home returned a non-canonical fixed member path")
	}
	if _, err := OpenOperationalHome(context.Background(), homePath); !errors.Is(err, ErrBusy) {
		t.Fatalf("second operational opener = %v, want busy", err)
	}
	if got, err := os.ReadFile(filepath.Join(homePath, runtimesName, "sentinel")); err != nil || !bytes.Equal(got, runtimeSentinel) {
		t.Fatalf("runtime descendant changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(homePath, changesName, "sentinel")); err != nil || !bytes.Equal(got, changeSentinel) {
		t.Fatalf("change descendant changed: %q, %v", got, err)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
	if err := home.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOperationalHomeCloseReportsReplacementWithoutMutation(t *testing.T) {
	parent := installTempDir(t)
	homePath := filepath.Join(parent, "home")
	if _, err := Init(context.Background(), homePath); err != nil {
		t.Fatal(err)
	}
	home, err := OpenOperationalHome(context.Background(), homePath)
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(homePath, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(homePath, 0o700); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(homePath, "replacement")
	if err := os.WriteFile(replacement, []byte("must remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if err := home.Close(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("close after home replacement = %v, want uncertain", err)
	}
	after, err := os.ReadFile(replacement)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("replacement changed after uncertain close: %q, %v", after, err)
	}
}
