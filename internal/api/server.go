//go:build darwin || linux

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// CallKind is the closed set of requests accepted by the local API.
type CallKind uint8

const (
	CallHealth CallKind = iota + 1
	CallSnapshot
	CallCreateProject
	CallCreateShellAgent
	CallEnqueueTask
	CallSetDispatch
	CallSucceed
	CallBlock
	CallFail
	CallRequestHuman
)

// AttemptDigest is the SHA-256 digest of one raw attempt bearer. The bearer is
// erased before Receive returns and is never representable outside this package.
type AttemptDigest struct{ value [sha256.Size]byte }

func (digest AttemptDigest) Bytes() [sha256.Size]byte { return digest.value }
func (AttemptDigest) String() string                  { return "AttemptDigest(<redacted>)" }
func (AttemptDigest) GoString() string                { return "AttemptDigest(<redacted>)" }

func digestAttemptCredential(bearer credential) AttemptDigest {
	return AttemptDigest{value: sha256.Sum256(bearer[:])}
}

// Call is an immutable decoded request. Only the accessor matching Kind
// returns true.
type Call struct {
	kind             CallKind
	digest           AttemptDigest
	project          CreateProjectInput
	agent            CreateShellAgentInput
	task             EnqueueTaskInput
	humanQuestion    HumanQuestionInput
	expectedRevision uint64
	enabled          bool
	text             string
}

func (call Call) Kind() CallKind { return call.kind }
func (call Call) String() string { return "Call(<redacted>)" }
func (call Call) GoString() string {
	return "Call(<redacted>)"
}

func (call Call) AttemptDigest() (AttemptDigest, bool) {
	switch call.kind {
	case CallSucceed, CallBlock, CallFail, CallRequestHuman:
		return call.digest, true
	default:
		return AttemptDigest{}, false
	}
}

func (call Call) CreateProjectInput() (CreateProjectInput, bool) {
	return call.project, call.kind == CallCreateProject
}

func (call Call) CreateShellAgentInput() (CreateShellAgentInput, bool) {
	return call.agent, call.kind == CallCreateShellAgent
}

func (call Call) EnqueueTaskInput() (EnqueueTaskInput, bool) {
	return call.task, call.kind == CallEnqueueTask
}

func (call Call) Dispatch() (uint64, bool, bool) {
	return call.expectedRevision, call.enabled, call.kind == CallSetDispatch
}

func (call Call) Result() (string, bool) {
	return call.text, call.kind == CallSucceed
}

func (call Call) Detail() (string, bool) {
	return call.text, call.kind == CallBlock || call.kind == CallFail
}

func (call Call) HumanQuestionInput() (HumanQuestionInput, bool) {
	return call.humanQuestion, call.kind == CallRequestHuman
}

type replyKind uint8

const (
	replyHealth replyKind = iota + 1
	replySnapshot
	replyMutation
	replyError
)

// Reply is constructed only through the four fixed reply constructors.
type Reply struct {
	kind     replyKind
	health   HealthStatus
	snapshot DashboardSnapshot
	mutation MutationResult
	code     RemoteErrorCode
}

func (Reply) String() string   { return "Reply(<redacted>)" }
func (Reply) GoString() string { return "Reply(<redacted>)" }

func NewHealthReply(status HealthStatus) Reply {
	return Reply{kind: replyHealth, health: status}
}

func NewSnapshotReply(snapshot DashboardSnapshot) (Reply, error) {
	if !validSnapshot(snapshot) {
		return Reply{}, ErrInvalidInput
	}
	projects := make([]ProjectSummary, len(snapshot.Projects))
	copy(projects, snapshot.Projects)
	snapshot.Projects = projects
	agents := make([]AgentSummary, len(snapshot.Agents))
	copy(agents, snapshot.Agents)
	snapshot.Agents = agents
	tasks := make([]TaskSummary, len(snapshot.Tasks))
	copy(tasks, snapshot.Tasks)
	snapshot.Tasks = tasks
	return Reply{kind: replySnapshot, snapshot: snapshot}, nil
}

func NewMutationReply(result MutationResult) (Reply, error) {
	if result.Revision == 0 {
		return Reply{}, ErrInvalidInput
	}
	return Reply{kind: replyMutation, mutation: result}, nil
}

func NewErrorReply(code RemoteErrorCode) (Reply, error) {
	if !validRemoteCode(code) {
		return Reply{}, ErrInvalidInput
	}
	return Reply{kind: replyError, code: code}, nil
}

// Listener owns one exact filesystem socket and the operator token loaded at
// creation. It creates no goroutines; the caller owns the accept loop.
type Listener struct {
	path      string
	name      string
	tokenPath string
	token     tokenRecord
	record    socketRecord
	root      *privateRoot
	listener  *net.UnixListener
	closer    *listenerClose
}

type listenerClose struct {
	once sync.Once
	err  error
}

func (Listener) String() string   { return "Listener(<redacted>)" }
func (Listener) GoString() string { return "Listener(<redacted>)" }

// Listen creates one EUID-owned mode-0600 Unix socket in an already-private
// parent. A pre-existing target is never removed or replaced.
func Listen(socketPath, operatorTokenPath string) (*Listener, error) {
	if !validCanonicalPath(socketPath, maxSocketPathBytes) {
		return nil, ErrInvalidListener
	}
	token, err := loadToken(operatorTokenPath)
	if err != nil {
		return nil, ErrInvalidListener
	}
	root, _, err := openPrivateParent(socketPath)
	if err != nil {
		return nil, ErrInvalidListener
	}
	name := filepath.Base(socketPath)
	if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		root.Close()
		return nil, ErrInvalidListener
	}

	unixListener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		root.Close()
		return nil, ErrInvalidListener
	}
	unixListener.SetUnlinkOnClose(false)
	listener := &Listener{
		path: socketPath, name: name, tokenPath: operatorTokenPath, token: token,
		root: root, listener: unixListener, closer: &listenerClose{},
	}

	created, ok := listener.currentSocket(false)
	if !ok {
		_ = unixListener.Close()
		_ = root.Close()
		return nil, ErrInvalidListener
	}
	listener.record = socketRecord{socket: created}
	if err := unix.Fchmodat(int(root.directory.Fd()), name, 0o600, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		listener.abortCreate()
		return nil, ErrInvalidListener
	}
	strict, ok := listener.currentSocket(true)
	if !ok || !strict.sameObject(created) {
		listener.abortCreate()
		return nil, ErrInvalidListener
	}
	listener.record.socket = strict
	parentInfo, err := root.directory.Stat()
	parent, ok := identityOf(parentInfo)
	if err != nil || !ok || !validPrivateParent(parentInfo) {
		listener.abortCreate()
		return nil, ErrInvalidListener
	}
	listener.record.parent = parent
	absolute, err := inspectSocket(socketPath)
	if err != nil || !absolute.same(listener.record) {
		listener.abortCreate()
		return nil, ErrInvalidListener
	}
	return listener, nil
}

func (listener *Listener) currentSocket(strict bool) (fileIdentity, bool) {
	info, err := listener.root.Lstat(listener.name)
	if err != nil {
		return fileIdentity{}, false
	}
	identity, ok := identityOf(info)
	if !ok || info.Mode()&os.ModeSocket == 0 || identity.uid != uint32(os.Geteuid()) || identity.links != 1 || identity.size != 0 {
		return fileIdentity{}, false
	}
	if strict && !validSocketInfo(info) {
		return fileIdentity{}, false
	}
	return identity, true
}

func (listener *Listener) abortCreate() {
	_ = listener.listener.Close()
	if current, ok := listener.currentSocket(false); ok && current.sameObject(listener.record.socket) {
		_ = unix.Unlinkat(int(listener.root.directory.Fd()), listener.name, 0)
	}
	_ = listener.root.Close()
}

// Accept synchronously accepts one connection and verifies both the socket
// identity and peer EUID. The caller must close the returned Connection.
func (listener *Listener) Accept() (*Connection, error) {
	return listener.accept(verifyPeerEUID)
}

func (listener *Listener) accept(peerCheck func(net.Conn) error) (*Connection, error) {
	before, err := inspectSocket(listener.path)
	if err != nil || !before.same(listener.record) {
		return nil, ErrInvalidListener
	}
	connection, err := listener.listener.AcceptUnix()
	if err != nil {
		return nil, ErrTransport
	}
	after, inspectErr := inspectSocket(listener.path)
	if inspectErr != nil || !after.same(listener.record) || peerCheck(connection) != nil {
		_ = connection.Close()
		return nil, ErrInvalidListener
	}
	return &Connection{
		connection: connection, operatorTokenPath: listener.tokenPath,
		operatorToken: listener.token,
	}, nil
}

// Close closes the listener and removes only the exact socket created by
// Listen. A replacement at the recorded name is left untouched.
func (listener *Listener) Close() error {
	listener.closer.once.Do(func() {
		closeErr := listener.listener.Close()
		current, ok := listener.currentSocket(false)
		if !ok || !current.same(listener.record.socket) {
			listener.closer.err = ErrInvalidListener
		} else if err := unix.Unlinkat(int(listener.root.directory.Fd()), listener.name, 0); err != nil {
			listener.closer.err = ErrInvalidListener
		}
		if err := listener.root.Close(); err != nil && listener.closer.err == nil {
			listener.closer.err = ErrInvalidListener
		}
		if closeErr != nil && !errors.Is(closeErr, net.ErrClosed) && listener.closer.err == nil {
			listener.closer.err = ErrTransport
		}
	})
	return listener.closer.err
}

type connectionState uint8

const (
	connectionNew connectionState = iota
	connectionReading
	connectionReceived
	connectionResponded
	connectionClosed
)

// Connection owns one accepted socket and permits exactly one Receive and one
// Respond. Receive owns and joins its cancellation watcher before returning.
type Connection struct {
	connection        *net.UnixConn
	operatorTokenPath string
	operatorToken     tokenRecord
	domain            byte
	kind              CallKind
	state             connectionState
}

func (Connection) String() string   { return "Connection(<redacted>)" }
func (Connection) GoString() string { return "Connection(<redacted>)" }

// Receive reads one complete request and requires client EOF before returning
// a dispatchable Call. Invalid requests are answered, when a valid response
// domain is available, with a fixed error code.
func (connection *Connection) Receive(ctx context.Context) (Call, error) {
	if connection == nil || connection.connection == nil || connection.state != connectionNew {
		return Call{}, ErrProtocol
	}
	connection.state = connectionReading
	if err := connection.setDeadline(ctx); err != nil {
		return Call{}, err
	}
	stopCancellation := watchCancellation(ctx, connection.connection)
	defer stopCancellation()
	payload, err := readFrame(connection.connection)
	if err != nil {
		return Call{}, classifyFrameError(ctx, err)
	}
	if len(payload) < requestPrelude {
		return Call{}, connection.reject(RemoteInvalidRequest)
	}
	defer clear(payload[:requestPrelude])
	domain := payload[1]
	if domain == operatorDomain || domain == attemptDomain {
		connection.domain = domain
	}
	if err := requireEOF(connection.connection); err != nil {
		if errors.Is(err, ErrProtocol) {
			return Call{}, connection.reject(RemoteInvalidRequest)
		}
		return Call{}, classifyFrameError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return Call{}, err
	}
	if domain != operatorDomain && domain != attemptDomain {
		return Call{}, ErrProtocol
	}
	if payload[0] != protocolGeneration {
		return Call{}, connection.reject(RemoteUnsupportedProtocol)
	}
	var bearer credential
	copy(bearer[:], payload[2:requestPrelude])
	defer clear(bearer[:])
	if domain == operatorDomain {
		current, tokenErr := loadToken(connection.operatorTokenPath)
		if tokenErr != nil || !current.same(connection.operatorToken) || !bearer.equal(current.bearer) {
			return Call{}, connection.reject(RemoteUnauthorized)
		}
	}
	call, code := decodeCall(domain, bearer, payload[requestPrelude:])
	if err := ctx.Err(); err != nil {
		return Call{}, err
	}
	if code != "" {
		return Call{}, connection.reject(code)
	}
	connection.kind = call.kind
	connection.state = connectionReceived
	return call, nil
}

func (connection *Connection) setDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(requestTimeout)
	if requested, ok := ctx.Deadline(); ok && requested.Before(deadline) {
		deadline = requested
	}
	if err := connection.connection.SetDeadline(deadline); err != nil {
		return ErrTransport
	}
	return nil
}

type incomingRequest struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func decodeCall(domain byte, bearer credential, encoded []byte) (Call, RemoteErrorCode) {
	var request incomingRequest
	if err := decodeExact(encoded, &request); err != nil || !jsonObject(request.Params) {
		return Call{}, RemoteInvalidRequest
	}
	kind, methodDomain := methodKind(request.Method)
	if kind == 0 {
		return Call{}, RemoteInvalidRequest
	}
	if methodDomain != domain {
		return Call{}, RemoteForbidden
	}
	call := Call{kind: kind}
	if domain == attemptDomain {
		call.digest = digestAttemptCredential(bearer)
	}
	switch kind {
	case CallHealth, CallSnapshot:
		if err := decodeExact(request.Params, &struct{}{}); err != nil {
			return Call{}, RemoteInvalidRequest
		}
	case CallCreateProject:
		if err := decodeExact(request.Params, &call.project); err != nil || !validID(call.project.ID) || !validText(call.project.Name, 1, 128) || !validText(call.project.Root, 1, 4096) {
			return Call{}, RemoteInvalidRequest
		}
	case CallCreateShellAgent:
		if err := decodeExact(request.Params, &call.agent); err != nil || !validID(call.agent.ID) || !validID(call.agent.ProjectID) || !validText(call.agent.Name, 1, 128) || call.agent.Role != "worker" && call.agent.Role != "orchestrator" || call.agent.ToolBudgetLimit < 1 || call.agent.ToolBudgetLimit > 1_000_000_000 {
			return Call{}, RemoteInvalidRequest
		}
	case CallEnqueueTask:
		if err := decodeExact(request.Params, &call.task); err != nil || !validID(call.task.ID) || !validID(call.task.ProjectID) || !validID(call.task.AssignedAgentID) || !validID(call.task.IncarnationID) || !validText(call.task.Title, 1, 1024) || !validText(call.task.Body, 0, 131072) || call.task.Priority < -1_000_000 || call.task.Priority > 1_000_000 {
			return Call{}, RemoteInvalidRequest
		}
	case CallSetDispatch:
		var input struct {
			ExpectedRevision uint64 `json:"expected_revision"`
			Enabled          bool   `json:"enabled"`
		}
		if err := decodeExact(request.Params, &input); err != nil || input.ExpectedRevision == 0 {
			return Call{}, RemoteInvalidRequest
		}
		call.expectedRevision, call.enabled = input.ExpectedRevision, input.Enabled
	case CallSucceed:
		var input struct {
			Result string `json:"result"`
		}
		if err := decodeExact(request.Params, &input); err != nil || !validText(input.Result, 0, 131072) {
			return Call{}, RemoteInvalidRequest
		}
		call.text = input.Result
	case CallBlock:
		detail, ok := decodeAttemptDetail(request.Params)
		if !ok || !validText(detail, 1, 4096) {
			return Call{}, RemoteInvalidRequest
		}
		call.text = detail
	case CallFail:
		detail, ok := decodeAttemptDetail(request.Params)
		if !ok || !validText(detail, 0, 4096) {
			return Call{}, RemoteInvalidRequest
		}
		call.text = detail
	case CallRequestHuman:
		if err := decodeExact(request.Params, &call.humanQuestion); err != nil || !validID(call.humanQuestion.IdempotencyKey) || !validText(call.humanQuestion.Question, 1, 8192) {
			return Call{}, RemoteInvalidRequest
		}
	}
	return call, ""
}

func decodeAttemptDetail(encoded []byte) (string, bool) {
	var input struct {
		Detail string `json:"detail"`
	}
	if err := decodeExact(encoded, &input); err != nil {
		return "", false
	}
	return input.Detail, true
}

func jsonObject(encoded []byte) bool {
	trimmed := bytes.TrimSpace(encoded)
	return len(trimmed) >= 2 && trimmed[0] == '{' && trimmed[len(trimmed)-1] == '}'
}

func methodKind(method string) (CallKind, byte) {
	switch method {
	case "health":
		return CallHealth, operatorDomain
	case "snapshot":
		return CallSnapshot, operatorDomain
	case "create_project":
		return CallCreateProject, operatorDomain
	case "create_shell_agent":
		return CallCreateShellAgent, operatorDomain
	case "enqueue_task":
		return CallEnqueueTask, operatorDomain
	case "set_dispatch":
		return CallSetDispatch, operatorDomain
	case "succeed":
		return CallSucceed, attemptDomain
	case "block":
		return CallBlock, attemptDomain
	case "fail":
		return CallFail, attemptDomain
	case "request_human":
		return CallRequestHuman, attemptDomain
	default:
		return 0, 0
	}
}

// Respond writes exactly one response for the previously received Call. The
// response domain is taken from that request and cannot be supplied by callers.
func (connection *Connection) Respond(reply Reply) error {
	if connection == nil || connection.connection == nil || connection.state != connectionReceived || !replyMatches(connection.kind, reply.kind) {
		return ErrProtocol
	}
	connection.state = connectionResponded
	if err := connection.writeReply(reply); err != nil {
		return err
	}
	return nil
}

func replyMatches(kind CallKind, reply replyKind) bool {
	if reply == replyError {
		return true
	}
	switch kind {
	case CallHealth:
		return reply == replyHealth
	case CallSnapshot:
		return reply == replySnapshot
	case CallCreateProject, CallCreateShellAgent, CallEnqueueTask, CallSetDispatch, CallSucceed, CallBlock, CallFail, CallRequestHuman:
		return reply == replyMutation
	default:
		return false
	}
}

func (connection *Connection) reject(code RemoteErrorCode) error {
	if connection.domain != operatorDomain && connection.domain != attemptDomain {
		return ErrProtocol
	}
	reply, err := NewErrorReply(code)
	if err != nil {
		return ErrProtocol
	}
	_ = connection.writeReply(reply)
	connection.state = connectionResponded
	return ErrProtocol
}

func (connection *Connection) writeReply(reply Reply) error {
	var data []byte
	var err error
	switch reply.kind {
	case replyHealth:
		data, err = json.Marshal(reply.health)
	case replySnapshot:
		data, err = json.Marshal(reply.snapshot)
	case replyMutation:
		data, err = json.Marshal(reply.mutation)
	case replyError:
	default:
		return ErrProtocol
	}
	if err != nil {
		return ErrProtocol
	}
	ok := reply.kind != replyError
	envelope := responseEnvelope{OK: &ok}
	if ok {
		envelope.Data = data
	} else {
		envelope.Error = reply.code
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return ErrProtocol
	}
	if len(encoded)+responsePrelude > maxFrameBytes {
		if reply.kind == replyError {
			return ErrProtocol
		}
		tooLarge, _ := NewErrorReply(RemoteTooLarge)
		return connection.writeReply(tooLarge)
	}
	payload := make([]byte, responsePrelude+len(encoded))
	payload[0], payload[1] = protocolGeneration, connection.domain
	copy(payload[responsePrelude:], encoded)
	if err := writeFrame(connection.connection, payload); err != nil {
		return ErrTransport
	}
	if err := connection.connection.CloseWrite(); err != nil {
		return ErrTransport
	}
	return nil
}

func (connection *Connection) Close() error {
	if connection == nil || connection.connection == nil || connection.state == connectionClosed {
		return nil
	}
	connection.state = connectionClosed
	if err := connection.connection.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return ErrTransport
	}
	return nil
}
