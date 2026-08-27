package browser

import (
	"context"
	"errors"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
)

var (
	ErrUnauthorized           = errors.New("browser: unauthorized")
	ErrNotFound               = errors.New("browser: not found")
	ErrStale                  = errors.New("browser: stale")
	ErrTooLarge               = errors.New("browser: too large")
	ErrRateLimited            = errors.New("browser: rate limited")
	ErrSubscriptionUnresolved = errors.New("browser: subscription cleanup unresolved")
)

// Identity is the daemon identity sent in HELLO. The boot ID changes on every
// daemon start; the daemon ID remains stable for one fresh Go runtime home.
type Identity struct {
	DaemonID [browserprotocol.DaemonIDSize]byte
	BootID   [browserprotocol.BootIDSize]byte
}

// Principal contains only daemon-minted browser authority. The transport
// never accepts capabilities or a client identity from an operation frame.
type Principal struct {
	ClientID [browserprotocol.ClientIDSize]byte
}

// Authentication is used only for the daemon-minted handshake result.
// Capabilities are negotiated once and are deliberately not cached on the
// connection as later authorization; Backend reloads authority per operation.
type Authentication struct {
	Principal    Principal
	Capabilities browserprotocol.Capabilities
}

type PairRequest struct {
	Identity
	ConnectionNonce [browserprotocol.NonceSize]byte
	Challenge       [browserprotocol.ChallengeSize]byte
	PublicKeySEC1   [browserprotocol.PublicKeySize]byte
	Signature       [browserprotocol.SignatureSize]byte
	Host            string
	Origin          string
}

type AuthRequest struct {
	Identity
	ConnectionNonce [browserprotocol.NonceSize]byte
	ClientID        [browserprotocol.ClientIDSize]byte
	Signature       [browserprotocol.SignatureSize]byte
	Host            string
	Origin          string
}

// Cursor is the decoded neutral browser continuation. Backend implementations
// must still revalidate its head, kind and exact identity against durable
// state. Opaque wire encoding remains transport-owned.
type Cursor struct {
	Head     browserprotocol.Decimal
	Kind     browserprotocol.StateKind
	AfterID  [16]byte
	HasAfter bool
}

type StatePage struct {
	Head       browserprotocol.Decimal
	Kind       browserprotocol.StateKind
	Items      browserprotocol.StateItems
	NextCursor *Cursor
}

// RestartError is a bounded causal result, not an internal diagnostic.
type RestartError struct {
	State browserprotocol.StateRestart
}

func (err *RestartError) Error() string { return "browser: state restart required" }

// StateUpdate is exactly one subscription result. A backend sends either an
// event or a restart, never both. Closing Updates without a restart is treated
// as an internal failure and forces reconnect.
type StateUpdate struct {
	Event   *browserprotocol.StateEvent
	Restart *browserprotocol.StateRestart
	// Floor is the current durable event-retention floor for Event updates.
	// Explicit Restart already contains its floor and requires this field zero.
	Floor browserprotocol.Decimal
}

// StateSubscription is owned by one WebSocket connection. Cancel must be
// nonblocking. Updates, Done and Err are nonblocking accessors. Done must close
// after the producer exits, after which Err is stable. This lets the connection
// bound shutdown without abandoning a goroutine inside an uncooperative
// synchronous Close call.
type StateSubscription interface {
	Updates() <-chan StateUpdate
	Cancel()
	Done() <-chan struct{}
	Err() error
}

// Backend is the complete daemon boundary for this state-only slice. Every
// operation after authentication receives only the daemon-minted client ID;
// the implementation revalidates durable authority and canonical state.
// Every method must return promptly when its context is cancelled. The
// transport never wraps a noncooperative call in an orphanable goroutine.
type Backend interface {
	Identity(context.Context) (Identity, error)
	Pair(context.Context, PairRequest) (Authentication, error)
	Authenticate(context.Context, AuthRequest) (Authentication, error)
	StatePage(context.Context, [browserprotocol.ClientIDSize]byte, *Cursor) (StatePage, error)
	StateEntity(context.Context, [browserprotocol.ClientIDSize]byte, browserprotocol.StateEntityGet) (browserprotocol.StateEntity, error)
	HumanRequestDetail(context.Context, [browserprotocol.ClientIDSize]byte, browserprotocol.HumanRequestDetailGet) (browserprotocol.HumanRequestDetail, error)
	SubscribeState(context.Context, [browserprotocol.ClientIDSize]byte, browserprotocol.Decimal) (StateSubscription, error)
}
