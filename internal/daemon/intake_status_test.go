package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntakeStatusPrivateBoundedSummary(t *testing.T) {
	root := filepath.Join(t.TempDir(), "factory")
	directory := root + ".intake"
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "status.json")
	id := strings.Repeat("ab", 16)
	data := []byte(`{"version":1,"sources":{"` + id + `":{"last_attempt_at":20,"last_success_at":10,"imported_tasks":1,"state":"error","error":"unavailable"}}}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	daemon := &Daemon{}
	daemon.ConfigureIntakeController(root)
	got := daemon.intakeSync()
	if got[id].LastSuccessAt != 10 || got[id].Error != "unavailable" {
		t.Fatalf("lost stale sync evidence: %+v", got)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if daemon.intakeSync() != nil {
		t.Fatal("non-private status accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "other.json")
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if daemon.intakeSync() != nil {
		t.Fatal("symlink status accepted")
	}
}
