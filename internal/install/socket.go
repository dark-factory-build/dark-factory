package install

import "path/filepath"

const (
	runtimesName       = "runtimes"
	localAPISocketName = "factory.sock"
)

// LocalAPISocketPath is the one canonical derivation of the local API socket
// pathname inside an operational home. The daemon binds exactly here and
// every client dials exactly here; callers validate the home argument, this
// function only derives. No other join of these members may exist.
func LocalAPISocketPath(home string) string {
	return filepath.Join(home, runtimesName, localAPISocketName)
}
