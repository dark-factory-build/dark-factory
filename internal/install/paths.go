package install

import "path/filepath"

const (
	runtimesName = "runtimes"
	changesName  = "changes"
	// LocalAPISocketName is the socket basename, exported for the one other
	// legitimate owner of the runtimes directory (the daemon runtime parent)
	// that must exclude exactly this name from its census.
	LocalAPISocketName = "factory.sock"
)

// RuntimesPath, ChangesPath, and LocalAPISocketPath are the sole joins of
// the operational home members. Callers validate the home argument; these
// functions only derive. No other join of these members may exist.
func RuntimesPath(home string) string { return filepath.Join(home, runtimesName) }

func ChangesPath(home string) string { return filepath.Join(home, changesName) }

func LocalAPISocketPath(home string) string {
	return filepath.Join(RuntimesPath(home), LocalAPISocketName)
}
