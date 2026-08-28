// Package install owns the private, stopped Go home bootstrap contract.
package install

import (
	"context"
	"errors"
	"os"
)

var (
	ErrUnsupported = errors.New("home initialization is unsupported on this platform")
	ErrInvalidHome = errors.New("home path is invalid")
	ErrUncertain   = errors.New("home publication outcome is uncertain")
	ErrBusy        = errors.New("home is already leased")
	ErrClosed      = errors.New("operational home is closed")
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
// lifetime lock remains held until Close returns.
type OperationalHome struct {
	state *operationalHomeState
}

// OpenOperationalHome validates and leases an existing Go-v1 home for daemon
// use. It accepts operational database sidecars and populated runtime/change
// directories, but never traverses or mutates those directories.
func OpenOperationalHome(ctx context.Context, home string) (*OperationalHome, error) {
	return openOperationalHome(ctx, home)
}

// MemberCapability is a fixed member descriptor capability bound to its
// OperationalHome. Open returns a fresh descriptor only while that home is
// live, after rechecking the retained home and member identities.
//
// The capability deliberately has no pathname accessor: path strings cannot
// carry the retained home binding through a later open.
type MemberCapability struct {
	state *operationalHomeState
	name  string
}

// Open duplicates the capability as an exact fixed-member descriptor. The
// caller owns and must close the returned descriptor.
func (capability MemberCapability) Open() (*os.File, error) {
	if capability.state == nil {
		return nil, ErrClosed
	}
	return capability.state.openCapability(capability.name)
}

// Database returns the bound database-member capability.
func (home *OperationalHome) Database() (MemberCapability, error) {
	return home.memberCapability("factory.sqlite3")
}

// Runtimes returns the bound runtimes-parent capability.
func (home *OperationalHome) Runtimes() (MemberCapability, error) {
	return home.memberCapability("runtimes")
}

// Changes returns the bound Changes-parent capability.
func (home *OperationalHome) Changes() (MemberCapability, error) {
	return home.memberCapability("changes")
}

func (home *OperationalHome) memberCapability(name string) (MemberCapability, error) {
	if home == nil || home.state == nil {
		return MemberCapability{}, ErrClosed
	}
	return home.state.memberCapability(name)
}

// Close revalidates the retained home identity, then releases the lifetime
// lock last. It is idempotent; it never repairs or removes a path.
func (home *OperationalHome) Close() error {
	if home == nil || home.state == nil {
		return nil
	}
	return home.state.close()
}
