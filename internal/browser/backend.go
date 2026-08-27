package browser

import (
	"context"
	"errors"
	"fmt"

	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
)

var (
	ErrUnauthorized           = errors.New("browser: unauthorized")
	ErrNotFound               = errors.New("browser: not found")
	ErrStale                  = errors.New("browser: stale")
	ErrTooLarge               = errors.New("browser: too large")
	ErrRateLimited            = errors.New("browser: rate limited")
	ErrSubscriptionUnresolved = errors.New("browser: subscription cleanup unresolved")
	ErrTerminalPartial        = errors.New("browser: terminal input was partial")
	ErrTerminalUncertain      = errors.New("browser: terminal input outcome is uncertain")
)

// Identity is the daemon identity sent in HELLO. The boot ID changes on every
// daemon start; the daemon ID remains stable for one fresh Go runtime home.
type Identity struct {
	DaemonID [browserprotocol.DaemonIDSize]byte
	BootID   [browserprotocol.BootIDSize]byte
}

// ConnectionID is one transport-minted WebSocket generation. Its bytes stay
// private to this package: callers may retain and compare the value but cannot
// construct a nonzero identity or put it on the wire.
type ConnectionID struct {
	value [browserprotocol.ClientIDSize]byte
}

func (id ConnectionID) zero() bool { return id == (ConnectionID{}) }

const connectionIDRedaction = "[private browser connection]"

// Format ignores every verb, flag, width and precision so no diagnostic form
// can fall through to the private numeric byte representation.
func (ConnectionID) Format(state fmt.State, _ rune) {
	_, _ = state.Write([]byte(connectionIDRedaction))
}

// Principal contains durable daemon-minted client authority plus one private,
// transport-minted connection identity. Backend authentication results must
// leave ConnectionID zero; the transport installs it after proof succeeds and
// before any operation can run.
type Principal struct {
	ClientID     [browserprotocol.ClientIDSize]byte
	ConnectionID ConnectionID `json:"-"`
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

// TerminalEvent is the small daemon-to-transport projection. It contains no
// runner identity or descriptor and is delivered from the daemon's existing
// bounded attachment queue.
type TerminalEvent struct {
	Kind       TerminalEventKind
	Accepted   bool
	Sequence   uint64
	Start      uint64
	End        uint64
	Floor      uint64
	Head       uint64
	ExitCode   int
	ExitSignal int
	Aborted    bool
	Payload    []byte
}

type TerminalEventKind uint8

const (
	TerminalEventAttached TerminalEventKind = iota + 1
	TerminalEventOutput
	TerminalEventReset
	TerminalEventPTYEOF
	TerminalEventExit
)

// TerminalAttachment exposes the daemon's receive-only queue directly. Close
// is synchronous: it does not return until the owner has joined the detach.
type TerminalAttachment interface {
	Events() <-chan TerminalEvent
	Close() error
}

type TerminalAttachRequest struct {
	Principal Principal
	Request   browserprotocol.TerminalAttach
}
type TerminalResizeRequest struct {
	Principal Principal
	Request   browserprotocol.TerminalResize
}
type TerminalInputRequest struct {
	Principal                    Principal
	RunID                        string
	SessionID                    string
	RunRevision, SessionRevision browserprotocol.Decimal
	Frame                        browserprotocol.TerminalFrame
}
type TerminalLeaseResult struct {
	Operation         string
	RunID             string
	SessionID         string
	Generation        browserprotocol.Decimal
	ExpiresAtMS       *browserprotocol.Decimal
	LastInputSequence browserprotocol.Decimal
	RunRevision       browserprotocol.Decimal
	SessionRevision   browserprotocol.Decimal
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
	TerminalTarget(context.Context, [browserprotocol.ClientIDSize]byte, browserprotocol.TerminalTargetGet) (browserprotocol.TerminalTarget, error)
	SubscribeState(context.Context, [browserprotocol.ClientIDSize]byte, browserprotocol.Decimal) (StateSubscription, error)
}

// TerminalBackend is the optional effect half of browser v1. Keeping it
// separate preserves the small state backend seam used by bootstrap/tests;
// production daemon backends implement both interfaces.
type TerminalBackend interface {
	Backend
	AttachTerminal(context.Context, TerminalAttachRequest) (TerminalAttachment, error)
	AcquireTerminalLease(context.Context, Principal, browserprotocol.TerminalLeaseAcquire) (TerminalLeaseResult, error)
	RenewTerminalLease(context.Context, Principal, browserprotocol.TerminalLeaseRenew) (TerminalLeaseResult, error)
	ReleaseTerminalLease(context.Context, Principal, browserprotocol.TerminalLeaseRelease) (TerminalLeaseResult, error)
	ResizeTerminal(context.Context, TerminalResizeRequest) error
	InputTerminal(context.Context, TerminalInputRequest) (uint32, error)
	ReplyHumanRequest(context.Context, Principal, browserprotocol.HumanRequestReply) (browserprotocol.HumanRequestReplyResult, error)
	CancelHumanRequestRun(context.Context, Principal, browserprotocol.HumanRequestCancelRun) (browserprotocol.HumanRequestActionResult, error)
}
