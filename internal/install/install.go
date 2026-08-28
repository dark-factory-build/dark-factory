// Package install owns the private, stopped Go home bootstrap contract.
package install

import (
	"context"
	"errors"
	"os"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
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

// LocalAPIAuthority is the retained endpoint and operator principal for one
// OperationalHome. It deliberately exposes no socket or token pathname: the
// daemon receives an already-bound capability, while path-based discovery
// remains a client-only concern.
type LocalAPIAuthority struct {
	state *localAPIState
}

func (LocalAPIAuthority) String() string   { return "LocalAPIAuthority(<redacted>)" }
func (LocalAPIAuthority) GoString() string { return "LocalAPIAuthority(<redacted>)" }

// LocalAPIProtocol is the sole framing/dispatch capability minted from one
// LocalAPIAuthority. Its fields and transport remain opaque outside install.
type LocalAPIProtocol struct {
	state *localAPIState
}

func (LocalAPIProtocol) String() string   { return "LocalAPIProtocol(<redacted>)" }
func (LocalAPIProtocol) GoString() string { return "LocalAPIProtocol(<redacted>)" }

// LocalAPIConnection is one connection accepted and retained by its exact
// LocalAPIProtocol. Close is once-owned and cannot target a caller-supplied
// transport.
type LocalAPIConnection struct {
	state *localAPIConnectionState
}

func (*LocalAPIConnection) String() string   { return "LocalAPIConnection(<redacted>)" }
func (*LocalAPIConnection) GoString() string { return "LocalAPIConnection(<redacted>)" }

// LocalAPIDispatch is one scoped permission for an authenticated call to
// enter daemon dispatch before OperationalHome shutdown begins.
type LocalAPIDispatch struct {
	state *localAPIDispatchState
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

// OpenStore activates the retained database authority exactly once. The
// returned Store owns a finite, eagerly opened SQLite connection set; closing
// the OperationalHome closes that Store before releasing the home lease.
func (home *OperationalHome) OpenStore(ctx context.Context) (*kernel.Store, error) {
	if home == nil || home.state == nil {
		return nil, ErrClosed
	}
	return home.state.openStore(ctx)
}

// OpenLocalAPI activates the one fixed local API endpoint for this home. It
// may be called exactly once, including after the returned authority closes.
func (home *OperationalHome) OpenLocalAPI(ctx context.Context) (*LocalAPIAuthority, error) {
	if home == nil || home.state == nil {
		return nil, ErrClosed
	}
	return home.state.openLocalAPI(ctx)
}

// ClaimProtocol transfers the single protocol-listener role to its caller.
// The original authority retains only lifetime/Close authority after this.
func (authority *LocalAPIAuthority) ClaimProtocol() (*LocalAPIProtocol, error) {
	if authority == nil || authority.state == nil {
		return nil, ErrClosed
	}
	return authority.state.claimProtocol()
}

// Verify proves that the retained home, operator principal, runtime parent,
// and exact socket binding are still the objects accepted at activation.
func (protocol *LocalAPIProtocol) Verify() error {
	if protocol == nil || protocol.state == nil {
		return ErrClosed
	}
	return protocol.state.verifyProtocol(protocol)
}

// CheckOperator compares a presented bearer with the immutable operator
// commitment after revalidating the complete local-API authority boundary.
func (protocol *LocalAPIProtocol) CheckOperator(bearer []byte) bool {
	return protocol != nil && protocol.state != nil && protocol.state.checkProtocolOperator(protocol, bearer)
}

// Accept returns one opaque, exact connection owned by this protocol.
func (protocol *LocalAPIProtocol) Accept() (*LocalAPIConnection, error) {
	if protocol == nil || protocol.state == nil {
		return nil, ErrClosed
	}
	return protocol.state.accept(protocol)
}

// BeginDispatch linearizes one authenticated call against home shutdown.
func (protocol *LocalAPIProtocol) BeginDispatch() (*LocalAPIDispatch, error) {
	if protocol == nil || protocol.state == nil {
		return nil, ErrClosed
	}
	return protocol.state.beginProtocolDispatch(protocol)
}

// Close joins accepted connections, removes only the exact owned socket, and
// releases retained descriptors. Any uncertain external result is stable and
// retains the owning home lease.
func (authority *LocalAPIAuthority) Close() error {
	if authority == nil || authority.state == nil {
		return nil
	}
	return authority.state.close()
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
