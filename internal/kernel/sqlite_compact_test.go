package kernel

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestBackupToRefusesAnyExistingDestination(t *testing.T) {
	store, live := newTestStore(t)
	defer store.Close()
	ctx := context.Background()
	existing := filepath.Join(t.TempDir(), "operator.sqlite3")
	if err := os.WriteFile(existing, []byte("operator data"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{existing, live} {
		if err := store.BackupTo(ctx, path); !errors.Is(err, ErrConflict) {
			t.Fatalf("backup to existing %s: %v", path, err)
		}
	}
	if data, err := os.ReadFile(existing); err != nil || string(data) != "operator data" {
		t.Fatalf("existing destination changed: %q %v", data, err)
	}
	if after, err := os.ReadFile(live); err != nil || !bytes.Equal(before, after) {
		t.Fatalf("live database changed: %v", err)
	}
	fresh := filepath.Join(t.TempDir(), "backup.sqlite3")
	if err := store.BackupTo(ctx, fresh); err != nil {
		t.Fatalf("backup to fresh path: %v", err)
	}
	if err := store.BackupTo(ctx, fresh); !errors.Is(err, ErrConflict) {
		t.Fatalf("second backup over its own file: %v", err)
	}
}
