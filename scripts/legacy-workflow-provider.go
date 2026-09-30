// This disposable native executable is the committed Codex provider image in
// the legacy workflow E2E. The runner deliberately commits executable files;
// a Python shebang cannot stand in for that boundary on macOS. The external
// deterministic behavior remains in legacy-workflow-fake.py.
package main

import (
	"os"
	"path/filepath"
	"syscall"
)

func main() {
	executable, err := os.Executable()
	if err != nil {
		os.Exit(70)
	}
	path := filepath.Join(filepath.Dir(executable), "legacy-workflow-fake.py")
	environment := append(os.Environ(), "DARK_FACTORY_FIXTURE_TOOL=codex")
	arguments := append([]string{"/usr/bin/python3", path}, os.Args[1:]...)
	if err := syscall.Exec("/usr/bin/python3", arguments, environment); err != nil {
		os.Exit(70)
	}
}
