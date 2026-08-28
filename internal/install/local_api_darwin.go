//go:build darwin

package install

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	localAPITokenBytes = operatorTokenBytes
	localAPIMaxPath    = 103
	localAPIProbeLimit = 250 * time.Millisecond
	localAPIAcceptPoll = 50 * time.Millisecond
)

type localAPIState struct {
	mu   sync.Mutex
	cond *sync.Cond

	home       *operationalHomeState
	token      *os.File
	tokenID    identity
	runtimes   *os.File
	runtimesID identity
	digest     [sha256.Size]byte
	locator    string

	listener         *net.UnixListener
	socketID         identity
	connections      map[*LocalAPIConnection]struct{}
	quarantined      map[*LocalAPIConnection]struct{}
	probeConnections []net.Conn
	accepting        int
	verifying        int
	dispatching      int
	protocol         *LocalAPIProtocol
	closing          bool
	closeRunning     bool
	closed           bool
	poisonErr        error
	cleanupErr       error
	closeErr         error
}

type localAPIConnectionState struct {
	owner           *localAPIState
	self            *LocalAPIConnection
	raw             *net.UnixConn
	closeMu         sync.Mutex
	transportClosed bool
	closed          bool
	once            sync.Once
	closeErr        error
}

type localAPIDispatchState struct {
	owner *localAPIState
	self  *LocalAPIDispatch
	once  sync.Once
}

// These narrow seams sit at external effects. Production leaves the phase
// hook nil and uses the concrete Darwin operations below.
var localAPIPhaseHook func(string) error
var localAPISyncDirectory = unix.Fsync
var localAPIUnlinkAt = unix.Unlinkat
var localAPIChmod = func(parent int, name string, mode uint32) error {
	return unix.Fchmodat(parent, name, mode, unix.AT_SYMLINK_NOFOLLOW)
}
var localAPIDial = func(ctx context.Context, locator string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "unix", locator)
}
var localAPICloseFile = func(file *os.File) error { return file.Close() }
var localAPICloseListener = func(listener *net.UnixListener) error { return listener.Close() }
var localAPICloseConnection = func(connection *net.UnixConn) error { return connection.Close() }
var localAPICloseProbeConnection = func(connection net.Conn) error { return connection.Close() }
var localAPISetListenerDeadline = func(listener *net.UnixListener, deadline time.Time) error {
	return listener.SetDeadline(deadline)
}

func (state *operationalHomeState) openLocalAPI(ctx context.Context) (*LocalAPIAuthority, error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.closed || state.parent == nil || state.home == nil {
		return nil, ErrClosed
	}
	if state.localAPI != nil {
		return nil, ErrBusy
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := recheckOperationalCoreIdentityByState(state); err != nil {
		return nil, errors.Join(ErrUncertain, err)
	}
	tokenMember, tokenOK := state.members[tokenName]
	runtimesMember, runtimesOK := state.members[runtimesName]
	if !tokenOK || tokenMember.file == nil || !runtimesOK || runtimesMember.file == nil || !runtimesMember.directory {
		return nil, errors.Join(ErrUncertain, fmt.Errorf("local API retained members are unavailable"))
	}
	if err := recheckLocalAPIMember(state, tokenName, tokenMember); err != nil {
		return nil, errors.Join(ErrUncertain, err)
	}
	if err := recheckLocalAPIMember(state, runtimesName, runtimesMember); err != nil {
		return nil, errors.Join(ErrUncertain, err)
	}
	token, err := duplicateOperationalFile(tokenMember.file, "local API operator principal")
	if err != nil {
		return nil, err
	}
	authorityState := &localAPIState{
		home: state, token: token, tokenID: tokenMember.identity,
		locator:     LocalAPISocketPath(filepath.Dir(state.databasePath)),
		connections: make(map[*LocalAPIConnection]struct{}),
		quarantined: make(map[*LocalAPIConnection]struct{}),
	}
	authorityState.cond = sync.NewCond(&authorityState.mu)
	authority := &LocalAPIAuthority{state: authorityState}
	runtimes, err := duplicateOperationalFile(runtimesMember.file, "local API socket parent")
	if err != nil {
		return state.rejectLocalAPIConstruction(authority, err)
	}
	authorityState.runtimes = runtimes
	authorityState.runtimesID = runtimesMember.identity
	if len(authorityState.locator) > localAPIMaxPath || !filepath.IsAbs(authorityState.locator) {
		return state.rejectLocalAPIConstruction(authority, fmt.Errorf("%w: local API socket locator is invalid", ErrInvalidHome))
	}
	contents, err := authorityState.readBoundToken()
	if err != nil {
		cause := authorityState.poison(err)
		_ = authorityState.rejectActivation(cause, true)
		state.localAPI = authority
		return authority, authorityState.closeErr
	}
	authorityState.digest = sha256.Sum256(contents[:])
	if err := authorityState.activate(ctx); err != nil {
		retain := errors.Is(err, ErrUncertain)
		cleanupErr := authorityState.rejectActivation(err, retain)
		if retain || cleanupErr != nil {
			state.localAPI = authority
			return authority, authorityState.closeErr
		}
		return nil, err
	}
	state.localAPI = authority
	return authority, nil
}

func (state *operationalHomeState) rejectLocalAPIConstruction(authority *LocalAPIAuthority, cause error) (*LocalAPIAuthority, error) {
	cleanupErr := authority.state.rejectActivation(cause, false)
	if cleanupErr == nil {
		return nil, cause
	}
	state.localAPI = authority
	return authority, authority.state.closeErr
}

func recheckLocalAPIMember(home *operationalHomeState, name string, member retainedMember) error {
	if member.directory {
		if err := exactDirectory(member.file, false); err != nil {
			return fmt.Errorf("recheck local API member: %w", err)
		}
	}
	if err := sameMemberFileIdentity(member.identity, member.file, member.directory); err != nil {
		return fmt.Errorf("recheck local API member: %w", err)
	}
	if err := recheckIdentityBinding(home.home, name, member.identity); err != nil {
		return fmt.Errorf("recheck local API member binding: %w", err)
	}
	return nil
}

func (state *localAPIState) activate(ctx context.Context) error {
	if err := state.verifyWithoutSocket(); err != nil {
		return err
	}
	if err := atLocalAPIPhase("before stale inspection"); err != nil {
		return err
	}
	present, current, err := localAPISocketAt(state.runtimes, false)
	if err != nil {
		return err
	}
	if present {
		if err := state.removeExactStaleSocket(ctx, current); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := state.verifyWithoutSocket(); err != nil {
		return err
	}
	if err := atLocalAPIPhase("before bind"); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: state.locator, Net: "unix"})
	if err != nil {
		return fmt.Errorf("%w: local API socket bind failed", ErrInvalidHome)
	}
	listener.SetUnlinkOnClose(false)
	state.listener = listener
	if err := atLocalAPIPhase("before record"); err != nil {
		return err
	}
	present, created, err := localAPISocketAt(state.runtimes, false)
	if err != nil || !present {
		return errors.Join(fmt.Errorf("record local API socket: %w", ErrUncertain), err)
	}
	if err := state.proveListenerBinding(ctx, created); err != nil {
		return errors.Join(ErrUncertain, err)
	}
	if err := atLocalAPIPhase("after bind"); err != nil {
		return err
	}
	if err := atLocalAPIPhase("before chmod"); err != nil {
		return err
	}
	if err := localAPIChmod(int(state.runtimes.Fd()), LocalAPISocketName, 0o600); err != nil {
		return fmt.Errorf("secure local API socket: %w", err)
	}
	if err := atLocalAPIPhase("after chmod"); err != nil {
		return err
	}
	present, secured, err := localAPISocketAt(state.runtimes, true)
	if err != nil || !present || sameObjectIdentity(created, secured) != nil {
		return errors.Join(fmt.Errorf("recheck secured local API socket: %w", ErrUncertain), err)
	}
	state.socketID = secured
	if err := atLocalAPIPhase("after record"); err != nil {
		return err
	}
	if err := localAPISyncDirectory(int(state.runtimes.Fd())); err != nil {
		return fmt.Errorf("sync local API socket parent: %w", err)
	}
	if err := atLocalAPIPhase("after sync"); err != nil {
		return err
	}
	if err := state.verifyBindings(); err != nil {
		return err
	}
	return ctx.Err()
}

func (state *localAPIState) proveListenerBinding(ctx context.Context, created identity) (result error) {
	probeCtx, cancel := context.WithTimeout(ctx, localAPIProbeLimit)
	defer cancel()
	deadline, ok := probeCtx.Deadline()
	if !ok {
		return errors.New("local API binding proof has no deadline")
	}
	if err := localAPISetListenerDeadline(state.listener, deadline); err != nil {
		return errors.New("local API binding proof could not bound accept")
	}
	var probes []net.Conn
	defer func() {
		if err := localAPISetListenerDeadline(state.listener, time.Time{}); err != nil {
			result = errors.Join(ErrUncertain, result, errors.New("local API binding proof could not reset accept deadline"))
		}
		if err := state.closeProbeConnections(probes...); err != nil {
			result = errors.Join(ErrUncertain, result, err)
		}
	}()
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return errors.New("local API binding proof could not create nonce")
	}
	client, err := localAPIDial(probeCtx, state.locator)
	if client != nil {
		probes = append(probes, client)
	}
	if err != nil {
		return errors.New("local API binding proof could not connect")
	}
	if client == nil {
		return errors.Join(ErrUncertain, errors.New("local API binding proof returned no connection"))
	}
	if err := client.SetDeadline(deadline); err != nil {
		return errors.New("local API binding proof could not bound client")
	}
	if written, err := client.Write(nonce[:]); written != len(nonce) || err != nil {
		return errors.New("local API binding proof could not write nonce")
	}
	server, err := state.listener.AcceptUnix()
	if server != nil {
		probes = append(probes, server)
	}
	if err != nil {
		return errors.New("local API binding proof reached another listener")
	}
	if server == nil {
		return errors.Join(ErrUncertain, errors.New("local API binding proof accepted no connection"))
	}
	if err := server.SetDeadline(deadline); err != nil {
		return errors.New("local API binding proof could not bound server")
	}
	var received [len(nonce)]byte
	if _, err := io.ReadFull(server, received[:]); err != nil || subtle.ConstantTimeCompare(received[:], nonce[:]) != 1 {
		return errors.New("local API binding proof nonce differed")
	}
	// Only the listener that received this unpredictable nonce may claim the
	// descriptor-relative record inspected immediately after bind. Recording
	// here lets later deadline/connection cleanup uncertainty remove that exact
	// proven leaf without adopting an unrelated socket.
	state.socketID = created
	return nil
}

func (state *localAPIState) closeProbeConnections(connections ...net.Conn) error {
	var result error
	for _, connection := range connections {
		if connection == nil {
			continue
		}
		if err := localAPICloseProbeConnection(connection); err != nil && !errors.Is(err, net.ErrClosed) {
			state.mu.Lock()
			state.probeConnections = append(state.probeConnections, connection)
			state.mu.Unlock()
			result = errors.Join(result, errors.New("close local API probe connection failed"))
		}
	}
	return result
}

func (state *localAPIState) removeExactStaleSocket(ctx context.Context, expected identity) error {
	if err := eligibleLocalAPISocket(expected, false); err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, localAPIProbeLimit)
	defer cancel()
	if err := state.probeExistingSocket(probeCtx); err == nil {
		return ErrBusy
	} else if errors.Is(err, ErrUncertain) {
		return err
	} else if ctxErr := probeCtx.Err(); ctxErr != nil {
		return ctxErr
	} else if !errors.Is(err, unix.ECONNREFUSED) {
		return fmt.Errorf("%w: existing local API socket liveness is ambiguous", ErrInvalidHome)
	}
	present, current, err := localAPISocketAt(state.runtimes, false)
	if err != nil || !present || sameIdentities(expected, current) != nil {
		return errors.Join(fmt.Errorf("%w: existing local API socket changed during probe", ErrInvalidHome), err)
	}
	if err := state.verifyWithoutSocket(); err != nil {
		return err
	}
	if err := atLocalAPIPhase("before stale unlink"); err != nil {
		return err
	}
	if err := unlinkExactLocalAPISocket(state.runtimes, expected, true); err != nil {
		return fmt.Errorf("remove exact stale local API socket: %w", err)
	}
	if err := localAPISyncDirectory(int(state.runtimes.Fd())); err != nil {
		return errors.Join(ErrUncertain, fmt.Errorf("sync stale local API removal: %w", err))
	}
	present, _, err = localAPISocketAt(state.runtimes, false)
	if err != nil || present {
		return errors.Join(ErrUncertain, fmt.Errorf("prove stale local API socket removal: %w", err))
	}
	return state.verifyWithoutSocket()
}

func (state *localAPIState) probeExistingSocket(ctx context.Context) error {
	connection, dialErr := localAPIDial(ctx, state.locator)
	if connection == nil && dialErr == nil {
		return errors.Join(ErrUncertain, errors.New("local API stale probe returned no connection"))
	}
	if connection != nil {
		if closeErr := state.closeProbeConnections(connection); closeErr != nil {
			return errors.Join(ErrUncertain, closeErr)
		}
		// A returned transport is positive evidence of a live peer even when a
		// dialer also reports an error. Never reinterpret that owned connection
		// as ECONNREFUSED and delete the socket it reached.
		return nil
	}
	return dialErr
}

func (state *localAPIState) readBoundToken() ([localAPITokenBytes]byte, error) {
	var contents [localAPITokenBytes]byte
	if err := sameMemberFileIdentity(state.tokenID, state.token, false); err != nil {
		return contents, err
	}
	if err := recheckIdentityBinding(state.home.home, tokenName, state.tokenID); err != nil {
		return contents, err
	}
	read, err := state.token.ReadAt(contents[:], 0)
	if read != len(contents) || err != nil {
		return contents, errors.Join(io.ErrUnexpectedEOF, err)
	}
	var extra [1]byte
	if read, err := state.token.ReadAt(extra[:], localAPITokenBytes); read != 0 || !errors.Is(err, io.EOF) {
		return contents, fmt.Errorf("%w: operator principal size changed", ErrInvalidHome)
	}
	if err := validateOperatorToken(contents[:]); err != nil {
		return contents, err
	}
	if err := sameMemberFileIdentity(state.tokenID, state.token, false); err != nil {
		return contents, err
	}
	if err := recheckIdentityBinding(state.home.home, tokenName, state.tokenID); err != nil {
		return contents, err
	}
	return contents, nil
}

func (state *localAPIState) verifyWithoutSocket() error {
	if err := recheckOperationalCoreIdentityByState(state.home); err != nil {
		return err
	}
	if err := exactDirectory(state.runtimes, false); err != nil {
		return err
	}
	if err := sameMemberFileIdentity(state.runtimesID, state.runtimes, true); err != nil {
		return err
	}
	if err := recheckIdentityBinding(state.home.home, runtimesName, state.runtimesID); err != nil {
		return err
	}
	contents, err := state.readBoundToken()
	if err != nil {
		return err
	}
	digest := sha256.Sum256(contents[:])
	if subtle.ConstantTimeCompare(digest[:], state.digest[:]) != 1 {
		return fmt.Errorf("%w: operator principal changed", ErrInvalidHome)
	}
	return nil
}

func (state *localAPIState) verifyBindings() error {
	if err := state.verifyWithoutSocket(); err != nil {
		return err
	}
	present, current, err := localAPISocketAt(state.runtimes, true)
	if err != nil || !present || sameIdentities(state.socketID, current) != nil {
		return errors.Join(fmt.Errorf("%w: local API socket binding changed", ErrInvalidHome), err)
	}
	return nil
}

func (state *localAPIState) beginVerify() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.poisonErr != nil {
		return state.poisonErr
	}
	if state.closing || state.closed || state.listener == nil {
		return ErrClosed
	}
	state.verifying++
	return nil
}

func (state *localAPIState) poison(cause error) error {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.poisonErr == nil {
		state.poisonErr = errors.Join(ErrUncertain, errors.New("local API authority binding changed"), cause)
	}
	return state.poisonErr
}

func (state *localAPIState) endVerify() {
	state.mu.Lock()
	state.verifying--
	state.cond.Broadcast()
	state.mu.Unlock()
}

func (state *localAPIState) verify() error {
	if state == nil {
		return ErrClosed
	}
	if err := state.beginVerify(); err != nil {
		return err
	}
	defer state.endVerify()
	if err := state.verifyBindings(); err != nil {
		return state.poison(err)
	}
	return nil
}

func (state *localAPIState) checkOperator(bearer []byte) bool {
	if len(bearer) != localAPITokenBytes || state.beginVerify() != nil {
		return false
	}
	defer state.endVerify()
	if err := state.verifyBindings(); err != nil {
		_ = state.poison(err)
		return false
	}
	presented := sha256.Sum256(bearer)
	return subtle.ConstantTimeCompare(presented[:], state.digest[:]) == 1
}

func (state *localAPIState) claimProtocol() (*LocalAPIProtocol, error) {
	if err := state.beginVerify(); err != nil {
		return nil, err
	}
	defer state.endVerify()
	if err := state.verifyBindings(); err != nil {
		return nil, state.poison(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.poisonErr != nil {
		return nil, state.poisonErr
	}
	if state.closing || state.closed {
		return nil, ErrClosed
	}
	if state.protocol != nil {
		return nil, ErrBusy
	}
	protocol := &LocalAPIProtocol{state: state}
	state.protocol = protocol
	return protocol, nil
}

func (state *localAPIState) ownsProtocol(protocol *LocalAPIProtocol) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	return protocol != nil && state.protocol == protocol
}

func (state *localAPIState) verifyProtocol(protocol *LocalAPIProtocol) error {
	if !state.ownsProtocol(protocol) {
		return ErrClosed
	}
	return state.verify()
}

func (state *localAPIState) checkProtocolOperator(protocol *LocalAPIProtocol, bearer []byte) bool {
	return state.ownsProtocol(protocol) && state.checkOperator(bearer)
}

func (state *localAPIState) accept(protocol *LocalAPIProtocol) (*LocalAPIConnection, error) {
	if !state.ownsProtocol(protocol) {
		return nil, ErrClosed
	}
	if err := state.verify(); err != nil {
		return nil, err
	}
	state.mu.Lock()
	if state.closing || state.closed || state.listener == nil || state.protocol != protocol {
		state.mu.Unlock()
		return nil, ErrClosed
	}
	listener := state.listener
	state.accepting++
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.accepting--
		state.cond.Broadcast()
		state.mu.Unlock()
	}()

	for {
		state.mu.Lock()
		stopped := state.closing || state.closed
		state.mu.Unlock()
		if stopped {
			return nil, ErrClosed
		}
		if err := localAPISetListenerDeadline(listener, time.Now().Add(localAPIAcceptPoll)); err != nil {
			return nil, state.poison(errors.New("local API accept deadline failed"))
		}
		raw, err := listener.AcceptUnix()
		if err != nil {
			state.mu.Lock()
			stopped = state.closing || state.closed
			state.mu.Unlock()
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() && !stopped {
				continue
			}
			if stopped {
				return nil, ErrClosed
			}
			return nil, errors.New("local API accept failed")
		}
		if raw == nil {
			return nil, state.poison(errors.New("local API accept returned no connection"))
		}
		connection := &LocalAPIConnection{state: &localAPIConnectionState{owner: state, raw: raw}}
		connection.state.self = connection
		state.mu.Lock()
		state.connections[connection] = struct{}{}
		stopped = state.closing || state.closed
		state.mu.Unlock()
		if stopped {
			_ = connection.Close()
			return nil, ErrClosed
		}
		if err := verifyLocalAPIPeer(raw); err != nil {
			closeErr := connection.Close()
			return nil, errors.Join(err, closeErr)
		}
		if err := state.verify(); err != nil {
			closeErr := connection.Close()
			return nil, errors.Join(err, closeErr)
		}
		return connection, nil
	}
}

func (state *localAPIState) closeExactConnection(connection *LocalAPIConnection, releaseOwner bool) error {
	if connection == nil || connection.state == nil || connection.state.self != connection || connection.state.owner != state || connection.state.raw == nil {
		return ErrClosed
	}
	connection.state.closeMu.Lock()
	defer connection.state.closeMu.Unlock()
	if connection.state.closed {
		return nil
	}
	state.mu.Lock()
	_, active := state.connections[connection]
	_, quarantined := state.quarantined[connection]
	if !active && !quarantined {
		state.mu.Unlock()
		return ErrClosed
	}
	if connection.state.transportClosed {
		if releaseOwner {
			connection.state.closed = true
			delete(state.connections, connection)
			delete(state.quarantined, connection)
			state.cond.Broadcast()
		}
		state.mu.Unlock()
		return nil
	}
	raw := connection.state.raw
	state.mu.Unlock()

	closeErr := localAPICloseConnection(raw)
	state.mu.Lock()
	if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		delete(state.connections, connection)
		if !quarantined {
			state.cleanupErr = errors.Join(state.cleanupErr, errors.New("close local API connection failed"))
		}
		state.quarantined[connection] = struct{}{}
	} else {
		connection.state.transportClosed = true
		if releaseOwner {
			connection.state.closed = true
			delete(state.connections, connection)
			delete(state.quarantined, connection)
		}
	}
	state.cond.Broadcast()
	state.mu.Unlock()
	if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		return errors.Join(ErrUncertain, errors.New("close local API connection failed"))
	}
	return nil
}

func (state *localAPIState) beginProtocolDispatch(protocol *LocalAPIProtocol) (*LocalAPIDispatch, error) {
	if !state.ownsProtocol(protocol) {
		return nil, ErrClosed
	}
	if err := state.verify(); err != nil {
		return nil, err
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.poisonErr != nil {
		return nil, state.poisonErr
	}
	if state.closing || state.closed || state.protocol != protocol {
		return nil, ErrClosed
	}
	state.dispatching++
	dispatch := &LocalAPIDispatch{state: &localAPIDispatchState{owner: state}}
	dispatch.state.self = dispatch
	return dispatch, nil
}

func (state *localAPIState) endDispatch() {
	state.mu.Lock()
	state.dispatching--
	state.cond.Broadcast()
	state.mu.Unlock()
}

func (state *localAPIState) close() error {
	if state == nil {
		return nil
	}
	state.mu.Lock()
	if state.closed {
		err := state.closeErr
		state.mu.Unlock()
		return err
	}
	if state.closeRunning {
		for !state.closed {
			state.cond.Wait()
		}
		err := state.closeErr
		state.mu.Unlock()
		return err
	}
	state.closing = true
	state.closeRunning = true
	listener := state.listener
	connections := make([]*LocalAPIConnection, 0, len(state.connections))
	for connection := range state.connections {
		connections = append(connections, connection)
	}
	state.mu.Unlock()

	var result error
	if listener != nil {
		if err := localAPISetListenerDeadline(listener, time.Now()); err != nil && !errors.Is(err, net.ErrClosed) {
			result = errors.Join(result, errors.New("stop local API accept failed"))
		}
		if err := localAPICloseListener(listener); err != nil && !errors.Is(err, net.ErrClosed) {
			result = errors.Join(result, errors.New("close local API listener failed"))
		}
	}
	for _, connection := range connections {
		if err := state.closeExactConnection(connection, false); err != nil && !errors.Is(err, ErrClosed) {
			result = errors.Join(result, errors.New("close local API connection failed"))
		}
	}

	state.mu.Lock()
	for state.accepting != 0 || state.verifying != 0 || state.dispatching != 0 || len(state.connections) != 0 {
		state.cond.Wait()
	}
	poisonErr := state.poisonErr
	cleanupErr := state.cleanupErr
	state.mu.Unlock()

	result = errors.Join(result, poisonErr, cleanupErr)
	if result == nil {
		if err := state.verifyWithoutSocket(); err != nil {
			result = errors.Join(result, err)
		}
		if err := state.removeOwnedSocket(); err != nil {
			result = errors.Join(result, err)
		}
	}
	if result == nil {
		result = state.closeUnusedDescriptors()
	}
	if result != nil {
		result = errors.Join(ErrUncertain, result)
	}

	state.mu.Lock()
	state.closeErr = result
	state.closed = true
	state.cond.Broadcast()
	state.mu.Unlock()
	return result
}

func (state *localAPIState) fenceClose() {
	state.mu.Lock()
	state.closing = true
	state.cond.Broadcast()
	state.mu.Unlock()
}

func (state *localAPIState) removeOwnedSocket() error {
	if state.socketID == (identity{}) {
		if state.listener != nil {
			return fmt.Errorf("local API socket identity was not recorded")
		}
		return nil
	}
	present, current, err := localAPISocketAt(state.runtimes, false)
	if err != nil || !present || sameObjectIdentity(state.socketID, current) != nil {
		return errors.Join(fmt.Errorf("local API socket ownership is unresolved"), err)
	}
	if err := unlinkExactLocalAPISocket(state.runtimes, state.socketID, false); err != nil {
		return fmt.Errorf("remove local API socket: %w", err)
	}
	if err := localAPISyncDirectory(int(state.runtimes.Fd())); err != nil {
		return fmt.Errorf("sync local API socket removal: %w", err)
	}
	present, _, err = localAPISocketAt(state.runtimes, false)
	if err != nil || present {
		return errors.Join(fmt.Errorf("prove local API socket removal"), err)
	}
	return nil
}

func unlinkExactLocalAPISocket(parent *os.File, expected identity, exactMetadata bool) error {
	present, current, err := localAPISocketAt(parent, false)
	if err != nil || !present {
		return errors.Join(errors.New("local API socket changed before removal"), err)
	}
	identityErr := sameObjectIdentity(expected, current)
	if exactMetadata {
		identityErr = sameIdentities(expected, current)
	}
	if identityErr != nil {
		return errors.New("local API socket changed before removal")
	}
	if err := localAPIUnlinkAt(int(parent.Fd()), LocalAPISocketName, 0); err != nil {
		// Darwin unlinkat does not report whether an interrupted or failed call
		// removed the directory entry. Every syscall error is therefore an
		// ambiguous ownership result, not authority to retry or rebind.
		return errors.Join(ErrUncertain, errors.New("local API socket removal outcome is uncertain"))
	}
	return nil
}

func (connection *LocalAPIConnection) Read(buffer []byte) (int, error) {
	if connection == nil || connection.state == nil || connection.state.self != connection || connection.state.raw == nil {
		return 0, ErrClosed
	}
	return connection.state.raw.Read(buffer)
}

func (connection *LocalAPIConnection) Write(buffer []byte) (int, error) {
	if connection == nil || connection.state == nil || connection.state.self != connection || connection.state.raw == nil {
		return 0, ErrClosed
	}
	return connection.state.raw.Write(buffer)
}

func (connection *LocalAPIConnection) SetDeadline(deadline time.Time) error {
	if connection == nil || connection.state == nil || connection.state.self != connection || connection.state.raw == nil {
		return ErrClosed
	}
	return connection.state.raw.SetDeadline(deadline)
}

func (connection *LocalAPIConnection) CloseWrite() error {
	if connection == nil || connection.state == nil || connection.state.self != connection || connection.state.raw == nil {
		return ErrClosed
	}
	return connection.state.raw.CloseWrite()
}

func (connection *LocalAPIConnection) Close() error {
	if connection == nil || connection.state == nil || connection.state.self != connection || connection.state.owner == nil {
		if connection != nil && connection.state != nil && connection.state.self != connection {
			return ErrClosed
		}
		return nil
	}
	connection.state.once.Do(func() {
		connection.state.closeErr = connection.state.owner.closeExactConnection(connection, true)
	})
	return connection.state.closeErr
}

func (dispatch *LocalAPIDispatch) Close() error {
	if dispatch == nil || dispatch.state == nil || dispatch.state.owner == nil {
		return nil
	}
	if dispatch.state.self != dispatch {
		return ErrClosed
	}
	dispatch.state.once.Do(func() { dispatch.state.owner.endDispatch() })
	return nil
}

func verifyLocalAPIPeer(connection *net.UnixConn) error {
	if connection == nil {
		return ErrInvalidHome
	}
	raw, err := connection.SyscallConn()
	if err != nil {
		return ErrInvalidHome
	}
	var peer localAPIPeerCredential
	var socketErr syscall.Errno
	if err := raw.Control(func(fd uintptr) {
		length := uint32(unsafe.Sizeof(peer))
		_, _, socketErr = syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd, localAPISOLLocal, localAPILocalPeerCred, uintptr(unsafe.Pointer(&peer)), uintptr(unsafe.Pointer(&length)), 0)
		if socketErr == 0 && length != uint32(unsafe.Sizeof(peer)) {
			socketErr = syscall.EINVAL
		}
		runtime.KeepAlive(&peer)
	}); err != nil || socketErr != 0 || peer.version != 0 || peer.uid != uint32(os.Geteuid()) || peer.groupCount < 0 || int(peer.groupCount) > len(peer.groups) {
		return ErrInvalidHome
	}
	return nil
}

const (
	localAPISOLLocal      = 0
	localAPILocalPeerCred = 1
)

type localAPIPeerCredential struct {
	version    uint32
	uid        uint32
	groupCount int16
	_          [2]byte
	groups     [16]uint32
}

func (state *localAPIState) rejectActivation(cause error, retain bool) error {
	var result error
	if state.listener != nil {
		if err := localAPICloseListener(state.listener); err != nil && !errors.Is(err, net.ErrClosed) {
			result = errors.Join(result, errors.New("close rejected local API listener failed"))
		}
		if result == nil {
			if err := state.verifyWithoutSocket(); err != nil {
				result = errors.Join(result, err)
			}
			if err := state.removeOwnedSocket(); err != nil {
				result = errors.Join(result, err)
			}
		}
	}
	if result == nil && !retain {
		result = state.closeUnusedDescriptors()
	}
	if retain || result != nil {
		result = errors.Join(ErrUncertain, cause, result)
	}
	state.mu.Lock()
	state.closing = true
	state.closed = true
	state.closeErr = result
	state.cond.Broadcast()
	state.mu.Unlock()
	return result
}

func (state *localAPIState) closeUnusedDescriptors() error {
	var result error
	if state.token != nil {
		if err := localAPICloseFile(state.token); err != nil {
			result = errors.Join(result, fmt.Errorf("close local API operator principal: %w", err))
		} else {
			state.token = nil
		}
	}
	if state.runtimes != nil {
		if err := localAPICloseFile(state.runtimes); err != nil {
			result = errors.Join(result, fmt.Errorf("close local API socket parent: %w", err))
		} else {
			state.runtimes = nil
		}
	}
	return result
}

func localAPISocketAt(parent *os.File, strict bool) (bool, identity, error) {
	var stat unix.Stat_t
	err := unix.Fstatat(int(parent.Fd()), LocalAPISocketName, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, identity{}, nil
	}
	if err != nil {
		return false, identity{}, fmt.Errorf("inspect local API socket: %w", err)
	}
	current := toIdentity(stat)
	if err := eligibleLocalAPISocket(current, strict); err != nil {
		return true, current, err
	}
	return true, current, nil
}

func eligibleLocalAPISocket(current identity, strict bool) error {
	if current.mode&unix.S_IFMT != unix.S_IFSOCK || current.uid != uint32(os.Geteuid()) || current.nlink != 1 || current.size != 0 {
		return fmt.Errorf("%w: local API socket is not an exact owner socket", ErrInvalidHome)
	}
	if strict && !exactMode(current.mode, 0o600) {
		return fmt.Errorf("%w: local API socket is not owner-only 0600", ErrInvalidHome)
	}
	return nil
}

func atLocalAPIPhase(phase string) error {
	if localAPIPhaseHook == nil {
		return nil
	}
	return localAPIPhaseHook(phase)
}
