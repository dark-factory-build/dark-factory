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
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	localAPISocketName = "factory.sock"
	localAPITokenBytes = 32
	localAPIMaxPath    = 103
	localAPIProbeLimit = 250 * time.Millisecond
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

	listener            *net.UnixListener
	socketID            identity
	connections         map[*net.UnixConn]struct{}
	accepting           int
	verifying           int
	claimed             bool
	closing             bool
	closed              bool
	poisonErr           error
	cleanupErr          error
	retainedConnections []*net.UnixConn
	closeErr            error
}

// These narrow seams sit at external effects. Production leaves the phase
// hook nil and uses the concrete Darwin operations below.
var localAPIPhaseHook func(string) error
var localAPISyncDirectory = unix.Fsync
var localAPIUnlinkAt = unix.Unlinkat
var localAPIBeforeUnlink func(string)
var localAPIChmod = func(parent int, name string, mode uint32) error {
	return unix.Fchmodat(parent, name, mode, unix.AT_SYMLINK_NOFOLLOW)
}
var localAPIProbe = probeLocalAPISocket
var localAPICloseFile = func(file *os.File) error { return file.Close() }
var localAPICloseListener = func(listener *net.UnixListener) error { return listener.Close() }

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
		locator:     filepath.Join(filepath.Dir(state.databasePath), runtimesName, localAPISocketName),
		connections: make(map[*net.UnixConn]struct{}),
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
		return state.rejectLocalAPIConstruction(authority, errors.Join(ErrUncertain, err))
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
	if err := state.proveListenerBinding(ctx); err != nil {
		return errors.Join(ErrUncertain, err)
	}
	state.socketID = created
	if err := atLocalAPIPhase("after bind"); err != nil {
		return err
	}
	if err := atLocalAPIPhase("before chmod"); err != nil {
		return err
	}
	if err := localAPIChmod(int(state.runtimes.Fd()), localAPISocketName, 0o600); err != nil {
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

func (state *localAPIState) proveListenerBinding(ctx context.Context) error {
	probeCtx, cancel := context.WithTimeout(ctx, localAPIProbeLimit)
	defer cancel()
	deadline, ok := probeCtx.Deadline()
	if !ok {
		return errors.New("local API binding proof has no deadline")
	}
	if err := state.listener.SetDeadline(deadline); err != nil {
		return errors.New("local API binding proof could not bound accept")
	}
	defer state.listener.SetDeadline(time.Time{})
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return errors.New("local API binding proof could not create nonce")
	}
	client, err := (&net.Dialer{}).DialContext(probeCtx, "unix", state.locator)
	if err != nil {
		return errors.New("local API binding proof could not connect")
	}
	defer client.Close()
	if err := client.SetDeadline(deadline); err != nil {
		return errors.New("local API binding proof could not bound client")
	}
	if written, err := client.Write(nonce[:]); written != len(nonce) || err != nil {
		return errors.New("local API binding proof could not write nonce")
	}
	server, err := state.listener.AcceptUnix()
	if err != nil {
		return errors.New("local API binding proof reached another listener")
	}
	defer server.Close()
	if err := server.SetDeadline(deadline); err != nil {
		return errors.New("local API binding proof could not bound server")
	}
	var received [len(nonce)]byte
	if _, err := io.ReadFull(server, received[:]); err != nil || subtle.ConstantTimeCompare(received[:], nonce[:]) != 1 {
		return errors.New("local API binding proof nonce differed")
	}
	return nil
}

func (state *localAPIState) removeExactStaleSocket(ctx context.Context, expected identity) error {
	if err := eligibleLocalAPISocket(expected, false); err != nil {
		return err
	}
	probeCtx, cancel := context.WithTimeout(ctx, localAPIProbeLimit)
	defer cancel()
	if err := localAPIProbe(probeCtx, state.locator); err == nil {
		return ErrBusy
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
	if err := unlinkExactLocalAPISocket(state.runtimes, expected, true, "stale"); err != nil {
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

func probeLocalAPISocket(ctx context.Context, locator string) error {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", locator)
	if err != nil {
		return err
	}
	_ = connection.Close()
	return nil
}

func (state *localAPIState) readBoundToken() ([localAPITokenBytes]byte, error) {
	var contents [localAPITokenBytes]byte
	if err := sameMemberFileIdentity(state.tokenID, state.token, false); err != nil {
		return contents, err
	}
	read, err := state.token.ReadAt(contents[:], 0)
	if read != len(contents) || err != nil && !errors.Is(err, io.EOF) {
		return contents, errors.Join(io.ErrUnexpectedEOF, err)
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
	if err := sameMemberFileIdentity(state.tokenID, state.token, false); err != nil {
		return err
	}
	if err := recheckIdentityBinding(state.home.home, tokenName, state.tokenID); err != nil {
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

func (state *localAPIState) claimProtocol() error {
	if err := state.beginVerify(); err != nil {
		return err
	}
	defer state.endVerify()
	if err := state.verifyBindings(); err != nil {
		return state.poison(err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.poisonErr != nil {
		return state.poisonErr
	}
	if state.closing || state.closed {
		return ErrClosed
	}
	if state.claimed {
		return ErrBusy
	}
	state.claimed = true
	return nil
}

func (state *localAPIState) accept() (*net.UnixConn, error) {
	if err := state.verify(); err != nil {
		return nil, err
	}
	state.mu.Lock()
	if state.closing || state.closed || state.listener == nil {
		state.mu.Unlock()
		return nil, ErrClosed
	}
	listener := state.listener
	state.accepting++
	state.mu.Unlock()

	connection, err := listener.AcceptUnix()
	state.mu.Lock()
	state.accepting--
	stopped := state.closing || state.closed
	if err == nil && !stopped {
		state.connections[connection] = struct{}{}
	} else if connection != nil {
		_ = connection.Close()
		connection = nil
	}
	state.cond.Broadcast()
	state.mu.Unlock()
	if err != nil {
		if stopped {
			return nil, ErrClosed
		}
		return nil, errors.New("local API accept failed")
	}
	if connection == nil {
		return nil, ErrClosed
	}
	if err := state.verify(); err != nil {
		_ = state.releaseConnection(connection)
		return nil, err
	}
	return connection, nil
}

func (state *localAPIState) releaseConnection(connection *net.UnixConn) error {
	closeErr := connection.Close()
	state.mu.Lock()
	delete(state.connections, connection)
	if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		state.cleanupErr = errors.Join(state.cleanupErr, errors.New("close local API connection failed"))
		state.retainedConnections = append(state.retainedConnections, connection)
	}
	state.cond.Broadcast()
	state.mu.Unlock()
	if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		return errors.New("close local API connection failed")
	}
	return nil
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
	if state.closing {
		for !state.closed {
			state.cond.Wait()
		}
		err := state.closeErr
		state.mu.Unlock()
		return err
	}
	state.closing = true
	listener := state.listener
	connections := make([]*net.UnixConn, 0, len(state.connections))
	for connection := range state.connections {
		connections = append(connections, connection)
	}
	state.mu.Unlock()

	var result error
	if listener != nil {
		if err := localAPICloseListener(listener); err != nil && !errors.Is(err, net.ErrClosed) {
			result = errors.Join(result, errors.New("close local API listener failed"))
		}
	}
	for _, connection := range connections {
		if err := connection.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			result = errors.Join(result, errors.New("close local API connection failed"))
		}
	}

	state.mu.Lock()
	for state.accepting != 0 || state.verifying != 0 || len(state.connections) != 0 {
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
	if err := unlinkExactLocalAPISocket(state.runtimes, state.socketID, false, "owned"); err != nil {
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

func unlinkExactLocalAPISocket(parent *os.File, expected identity, exactMetadata bool, phase string) error {
	if localAPIBeforeUnlink != nil {
		localAPIBeforeUnlink(phase)
	}
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
	if err := localAPIUnlinkAt(int(parent.Fd()), localAPISocketName, 0); err != nil {
		return err
	}
	return nil
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
	err := unix.Fstatat(int(parent.Fd()), localAPISocketName, &stat, unix.AT_SYMLINK_NOFOLLOW)
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
