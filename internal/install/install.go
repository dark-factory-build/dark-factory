// Package install owns the private, stopped Go home bootstrap contract.
package install

import (
	"context"
	"errors"
)

var (
	ErrUnsupported = errors.New("home initialization is unsupported on this platform")
	ErrInvalidHome = errors.New("home path is invalid")
	ErrUncertain   = errors.New("home publication outcome is uncertain")
	ErrBusy        = errors.New("home is already leased")
)

type State uint8

const (
	Published State = iota + 1
	Ready
)

// Result describes a successful operation without exposing private contents.
type Result struct {
	State State
}

// Init creates and atomically publishes one fresh Go home, or reports that an
// exact home is already ready. It never repairs an existing path.
func Init(ctx context.Context, home string) (Result, error) { return initHome(ctx, home) }

// Doctor validates one stopped fresh Go home without modifying the filesystem.
func Doctor(ctx context.Context, home string) (Result, error) { return inspectHome(ctx, home) }

// OperationalHome is the retained authority for one live Go home. The home
// lifetime lock remains held until Close returns. Its accessors expose only
// the fixed paths consumed by the daemon; they do not expose private bytes.
type OperationalHome struct {
	state operationalHomeState
}

// OpenOperationalHome validates and leases an existing Go-v1 home for daemon
// use. It accepts operational database sidecars and populated runtime/change
// directories, but never traverses or mutates those directories.
func OpenOperationalHome(ctx context.Context, home string) (*OperationalHome, error) {
	return openOperationalHome(ctx, home)
}

// DatabasePath returns the exact database member path.
func (home *OperationalHome) DatabasePath() string { return home.path("factory.sqlite3") }

// OperatorTokenPath returns the exact operator-token member path. The token
// bytes themselves are never returned by this package.
func (home *OperationalHome) OperatorTokenPath() string { return home.path("operator.token") }

// RuntimesPath returns the exact runtime-parent member path.
func (home *OperationalHome) RuntimesPath() string { return home.path("runtimes") }

// ChangesPath returns the exact Changes-parent member path.
func (home *OperationalHome) ChangesPath() string { return home.path("changes") }

func (home *OperationalHome) path(name string) string {
	if home == nil {
		return ""
	}
	return home.state.path(name)
}

// Close revalidates the retained home identity, then releases the lifetime
// lock last. It is idempotent; it never repairs or removes a path.
func (home *OperationalHome) Close() error {
	if home == nil {
		return nil
	}
	return home.state.close()
}
