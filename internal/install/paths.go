package install

import "path/filepath"

const (
	runtimesName = "runtimes"
	changesName  = "changes"
	// LocalAPISocketName is the socket basename, exported for the one other
	// legitimate owner of the runtimes directory (the daemon runtime parent)
	// that must exclude exactly this name from its census.
	LocalAPISocketName = "factory.sock"
	// RelayDirectoryName is the home member the relay connector keeps its node
	// identity in. This package owns the home layout, so it declares the name
	// and relayhost consumes it: two literals for one directory is how a home
	// census and its writer drift apart.
	RelayDirectoryName = "relay"
	// MaxSocketPathBytes is the byte budget for a Unix socket path derived
	// from an operational home. It is the conservative minimum across the
	// supported platforms: macOS sun_path holds 104 bytes including the
	// terminator, Linux holds 108, so 103 is safe on both. This package
	// derives the socket paths, so it declares their budget; every validator
	// downstream references this constant rather than restating the number.
	MaxSocketPathBytes = 103
)

// RuntimesPath, ChangesPath, and LocalAPISocketPath are the sole joins of
// the operational home members. Callers validate the home argument; these
// functions only derive. No other join of these members may exist.
func RuntimesPath(home string) string { return filepath.Join(home, runtimesName) }

func ChangesPath(home string) string { return filepath.Join(home, changesName) }

func LocalAPISocketPath(home string) string {
	return filepath.Join(RuntimesPath(home), LocalAPISocketName)
}
