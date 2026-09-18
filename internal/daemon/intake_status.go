package daemon

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

// ConfigureIntakeController sets only the installed controller's fixed status
// location, before listeners open. The controller remains its sole writer.
func (daemon *Daemon) ConfigureIntakeController(home string) { daemon.intakeControllerHome = home }

func (daemon *Daemon) intakeSync() map[string]api.IntakeSync {
	if daemon.intakeControllerHome == "" {
		return nil
	}
	directory := daemon.intakeControllerHome + ".intake"
	parent, err := os.Lstat(directory)
	if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0700 || !intakeStatusOwned(parent) {
		return nil
	}
	path := filepath.Join(directory, "status.json")
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0600 || before.Size() > 64<<10 || !intakeStatusOwned(before) {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil
	}
	var summary struct {
		Version int                       `json:"version"`
		Sources map[string]api.IntakeSync `json:"sources"`
	}
	if json.Unmarshal(data, &summary) != nil || summary.Version != 1 || len(summary.Sources) > 200 {
		return nil
	}
	for id, value := range summary.Sources {
		if _, err := contentID(id); err != nil || value.LastAttemptAt < 0 || value.LastSuccessAt < 0 || value.ImportedTasks > 200 {
			return nil
		}
		switch value.State {
		case "ok", "paused", "error":
		default:
			return nil
		}
		switch value.Error {
		case "", "denied", "unavailable", "invalid", "stale", "conflict", "overflow":
		default:
			return nil
		}
	}
	return summary.Sources
}

func intakeStatusOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Geteuid())
}
