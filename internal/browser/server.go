package browser

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/opgraph"
	"github.com/dark-factory-build/dark-factory/internal/relayhost"
)

const (
	Path = "/browser"
	// TracesPath accepts OTLP/HTTP trace exports from local processes.
	TracesPath = "/v1/traces"
	// MetricsPath and LogsPath accept OTLP/HTTP agent telemetry exports.
	MetricsPath = "/v1/metrics"
	LogsPath    = "/v1/logs"
	// PublicPath serves the safe public projection of one project.
	PublicPath     = "/v1/public/"
	maxOrigins     = 8
	maxConnections = 32
	// One coherent snapshot is one request, and a notification burst
	// collapses into at most one trailing refresh. 1,024 per requestWindow
	// leaves ample room for refreshes, detail reads, terminal control, task
	// enqueue and the floor's run-path and operational-graph polling.
	maxRequests         = 1024
	readQueueSize       = 8
	maxHeaderBytes      = 8 << 10
	authenticationLimit = 5 * time.Second
	writeLimit          = 2 * time.Second
	readHeaderLimit     = 2 * time.Second
	shutdownHeaderLimit = 2 * time.Second
	implementedCaps     = browserprotocol.CapabilityObserve | browserprotocol.CapabilityPrivateHumanRequestDetail | browserprotocol.CapabilityHumanActions | browserprotocol.CapabilityTerminalInput | browserprotocol.CapabilityAdministration
)

type Config struct {
	Address        string
	AllowedOrigins []string
	Backend        Backend
}

// Observer is an optional Backend capability: the daemon watching its own
// listener. Only fixed protocol names reach it, never request content.
type Observer interface {
	Observe(attributes map[string]string)
}

// ErrorClassifier is an optional Backend capability: name the backend's own
// errors as this package's. The transport applies it once, to every error it
// answers a request with, so no return path reaches the wire unnamed.
type ErrorClassifier interface {
	ClassifyError(err error) error
}

// TraceReceiver is an optional Backend capability: aggregate an OTLP trace
// export without retaining it. remote marks one the relay carried.
type TraceReceiver interface {
	ReceiveTraces(body []byte, remote bool) error
}

// AgentTelemetryReceiver is an optional Backend capability: count a local
// OTLP metrics or logs export (path is MetricsPath or LogsPath) against the
// runs its resources name, keeping no record.
type AgentTelemetryReceiver interface {
	ReceiveAgentTelemetry(path string, body []byte, protobuf bool) error
}

// PublicProjector is an optional Backend capability: the public projection
// of one project, already reduced to its allowlist.
type PublicProjector interface {
	PublicWorld(ctx context.Context, projectID string) ([]byte, error)
}

type clientLifecycle struct {
	connections    map[*connection]struct{}
	authenticating map[*connection]struct{}
	revoking       int
}

type Server struct {
	backend            Backend
	terminalBackend    TerminalBackend
	taskBackend        TaskBackend
	consoleBackend     ConsoleBackend
	githubBackend      GitHubBackend
	host               string
	origins            map[string]struct{}
	terminalAckTimeout time.Duration
	http               *http.Server
	now                func() time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu              sync.Mutex
	closing         bool
	connections     map[*connection]struct{}
	clientLifecycle map[[browserprotocol.ClientIDSize]byte]*clientLifecycle
	pairing         map[*connection]struct{}
	pairingBlocked  int
	slots           chan struct{}
	serveDone       chan struct{}
	serveErr        error
	cleanupErr      error
	closeErr        error
	closeOnce       sync.Once
}

func Listen(config Config) (*Server, error) {
	if config.Backend == nil {
		return nil, fmt.Errorf("browser: backend is required")
	}
	if err := validateAddress(config.Address); err != nil {
		return nil, err
	}
	origins, err := validateOrigins(config.AllowedOrigins)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp4", config.Address)
	if err != nil {
		return nil, fmt.Errorf("browser: listen: %w", err)
	}
	return start(config.Backend, origins, listener, time.Now), nil
}

func start(backend Backend, origins map[string]struct{}, listener net.Listener, clock func() time.Time) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	server := &Server{
		now:                clock,
		backend:            backend,
		terminalBackend:    func() TerminalBackend { value, _ := backend.(TerminalBackend); return value }(),
		taskBackend:        func() TaskBackend { value, _ := backend.(TaskBackend); return value }(),
		consoleBackend:     func() ConsoleBackend { value, _ := backend.(ConsoleBackend); return value }(),
		githubBackend:      func() GitHubBackend { value, _ := backend.(GitHubBackend); return value }(),
		host:               listener.Addr().String(),
		origins:            origins,
		terminalAckTimeout: time.Duration(browserprotocol.TerminalAckTimeoutMS) * time.Millisecond,
		ctx:                ctx,
		cancel:             cancel,
		connections:        make(map[*connection]struct{}),
		clientLifecycle:    make(map[[browserprotocol.ClientIDSize]byte]*clientLifecycle),
		pairing:            make(map[*connection]struct{}),
		slots:              make(chan struct{}, maxConnections),
		serveDone:          make(chan struct{}),
	}
	server.http = &http.Server{
		Handler:           http.HandlerFunc(server.handle),
		ReadHeaderTimeout: readHeaderLimit,
		IdleTimeout:       shutdownHeaderLimit,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	go func() {
		defer close(server.serveDone)
		if err := server.http.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			server.mu.Lock()
			server.serveErr = fmt.Errorf("browser: serve: %w", err)
			server.closing = true
			connections := make([]*connection, 0, len(server.connections))
			for current := range server.connections {
				connections = append(connections, current)
			}
			server.mu.Unlock()
			server.cancel()
			for _, current := range connections {
				current.stop()
			}
		}
	}()
	return server
}

func (server *Server) Addr() string {
	if server == nil {
		return ""
	}
	return server.host
}

// ServeDone closes if the listener stops, including an unexpected Serve
// failure. Err reports that bounded failure without exposing request data.
func (server *Server) ServeDone() <-chan struct{} {
	if server == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return server.serveDone
}

func (server *Server) Err() error {
	if server == nil {
		return nil
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.serveErr
}

func (server *Server) Close() error {
	if server == nil {
		return nil
	}
	server.closeOnce.Do(func() {
		var closeErrors []error
		server.mu.Lock()
		server.closing = true
		connections := make([]*connection, 0, len(server.connections))
		for current := range server.connections {
			connections = append(connections, current)
		}
		server.mu.Unlock()
		server.cancel()
		if err := server.http.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			closeErrors = append(closeErrors, err)
		}
		for _, current := range connections {
			current.stop()
		}
		for _, current := range connections {
			<-current.done
		}
		<-server.serveDone
		server.mu.Lock()
		if server.serveErr != nil {
			closeErrors = append(closeErrors, server.serveErr)
		}
		if server.cleanupErr != nil {
			closeErrors = append(closeErrors, server.cleanupErr)
		}
		server.closeErr = errors.Join(closeErrors...)
		server.mu.Unlock()
	})
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.closeErr
}

// CloseClient is the non-reentrant revocation seam. The daemon commits
// revocation before calling it; this method performs no authorization call.
func (server *Server) CloseClient(clientID [browserprotocol.ClientIDSize]byte) error {
	if server == nil || zero16(clientID) {
		return nil
	}
	server.mu.Lock()
	lifecycle := server.lifecycleLocked(clientID)
	lifecycle.revoking++
	server.pairingBlocked++
	set := make(map[*connection]struct{})
	for current := range lifecycle.connections {
		set[current] = struct{}{}
	}
	for current := range lifecycle.authenticating {
		set[current] = struct{}{}
	}
	// A pairing call has no client ID until Backend returns its daemon-minted
	// result. Fence and join all calls already in flight so none can register
	// this client after revocation returns.
	for current := range server.pairing {
		set[current] = struct{}{}
	}
	connections := make([]*connection, 0, len(set))
	for current := range set {
		connections = append(connections, current)
	}
	server.mu.Unlock()
	for _, current := range connections {
		current.stop()
	}
	for _, current := range connections {
		<-current.done
	}
	var closeErrors []error
	for _, current := range connections {
		if current.cleanupErr != nil {
			closeErrors = append(closeErrors, current.cleanupErr)
		}
	}
	server.mu.Lock()
	lifecycle = server.clientLifecycle[clientID]
	if lifecycle != nil {
		lifecycle.revoking--
		server.removeLifecycleIfIdleLocked(clientID)
	}
	server.pairingBlocked--
	server.mu.Unlock()
	return errors.Join(closeErrors...)
}

func (server *Server) handle(writer http.ResponseWriter, request *http.Request) {
	if observer, ok := server.backend.(Observer); ok && request.URL.Path == Path {
		host, port, _ := net.SplitHostPort(server.host)
		observer.Observe(map[string]string{"network.transport": "tcp", "server.address": host, "server.port": port})
		observer.Observe(map[string]string{"url.path": request.URL.Path, "http.request.method": request.Method})
	}
	if request.URL.Path == TracesPath || request.URL.Path == MetricsPath || request.URL.Path == LogsPath {
		server.handleOTLP(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, PublicPath) {
		server.handlePublic(writer, request)
		return
	}
	if request.URL.Path != Path || request.URL.EscapedPath() != Path || request.URL.RawQuery != "" {
		http.Error(writer, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	origin, ok := server.validRequest(request)
	if !ok {
		http.Error(writer, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	select {
	case server.slots <- struct{}{}:
	default:
		http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	reserved := true
	defer func() {
		if reserved {
			<-server.slots
		}
	}()
	server.mu.Lock()
	closing := server.closing
	server.mu.Unlock()
	if closing {
		http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	websocketConnection, err := websocket.Accept(writer, request, &websocket.AcceptOptions{
		OriginPatterns:  []string{origin},
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	websocketConnection.SetReadLimit(browserprotocol.MaxControlBytes)
	ctx, cancel := context.WithCancel(server.ctx)
	current := &connection{
		server: server,
		ws:     websocketConnection,
		host:   server.host,
		origin: origin,
		ctx:    ctx,
		cancel: cancel,
		frames: make(chan incoming, readQueueSize),
		done:   make(chan struct{}),
	}
	server.mu.Lock()
	if server.closing {
		server.mu.Unlock()
		cancel()
		_ = websocketConnection.CloseNow()
		return
	}
	server.connections[current] = struct{}{}
	server.mu.Unlock()
	reserved = false
	defer func() { <-server.slots }()
	current.run()
}

func (server *Server) validRequest(request *http.Request) (string, bool) {
	if request.Host != server.host {
		return "", false
	}
	values := request.Header.Values("Origin")
	if len(values) != 1 || values[0] == "null" {
		return "", false
	}
	_, ok := server.origins[values[0]]
	return values[0], ok
}

func (server *Server) beginAuthentication(current *connection, clientID [browserprotocol.ClientIDSize]byte) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closing || zero16(clientID) {
		return false
	}
	lifecycle := server.lifecycleLocked(clientID)
	if lifecycle.revoking != 0 {
		server.removeLifecycleIfIdleLocked(clientID)
		return false
	}
	lifecycle.authenticating[current] = struct{}{}
	current.authenticating = true
	current.authenticatingID = clientID
	return true
}

func (server *Server) beginPairing(current *connection) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closing || server.pairingBlocked != 0 {
		return false
	}
	server.pairing[current] = struct{}{}
	current.pairing = true
	return true
}

func (server *Server) finishAuthentication(current *connection, requested [browserprotocol.ClientIDSize]byte, result Authentication, accept bool) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.removeAuthenticatingLocked(current)
	if !accept || server.closing || result.Principal.ClientID != requested || result.Principal.ConnectionID.zero() {
		return false
	}
	lifecycle := server.lifecycleLocked(requested)
	if lifecycle.revoking != 0 {
		server.removeLifecycleIfIdleLocked(requested)
		return false
	}
	server.registerLocked(current, result.Principal)
	return true
}

func (server *Server) registerPair(current *connection, result Authentication, accept bool) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.removePairingLocked(current)
	if !accept || server.closing || zero16(result.Principal.ClientID) || result.Principal.ConnectionID.zero() {
		return false
	}
	lifecycle := server.lifecycleLocked(result.Principal.ClientID)
	if lifecycle.revoking != 0 {
		server.removeLifecycleIfIdleLocked(result.Principal.ClientID)
		return false
	}
	server.registerLocked(current, result.Principal)
	return true
}

func (server *Server) registerLocked(current *connection, principal Principal) {
	current.principal = principal
	current.authenticated = true
	server.lifecycleLocked(principal.ClientID).connections[current] = struct{}{}
}

func (server *Server) removeAuthenticatingLocked(current *connection) {
	if !current.authenticating {
		return
	}
	clientID := current.authenticatingID
	if lifecycle := server.clientLifecycle[clientID]; lifecycle != nil {
		delete(lifecycle.authenticating, current)
	}
	current.authenticating = false
	current.authenticatingID = [browserprotocol.ClientIDSize]byte{}
	server.removeLifecycleIfIdleLocked(clientID)
}

func (server *Server) removePairingLocked(current *connection) {
	if !current.pairing {
		return
	}
	delete(server.pairing, current)
	current.pairing = false
}

func (server *Server) lifecycleLocked(clientID [browserprotocol.ClientIDSize]byte) *clientLifecycle {
	lifecycle := server.clientLifecycle[clientID]
	if lifecycle == nil {
		lifecycle = &clientLifecycle{
			connections:    make(map[*connection]struct{}),
			authenticating: make(map[*connection]struct{}),
		}
		server.clientLifecycle[clientID] = lifecycle
	}
	return lifecycle
}

func (server *Server) removeLifecycleIfIdleLocked(clientID [browserprotocol.ClientIDSize]byte) {
	lifecycle := server.clientLifecycle[clientID]
	if lifecycle != nil && lifecycle.revoking == 0 && len(lifecycle.connections) == 0 && len(lifecycle.authenticating) == 0 {
		delete(server.clientLifecycle, clientID)
	}
}

func (server *Server) recordCleanup(err error) {
	server.mu.Lock()
	if server.cleanupErr == nil {
		// Retain one bounded typed failure, not an attacker-growable log.
		server.cleanupErr = err
	}
	server.mu.Unlock()
}

func (server *Server) unregister(current *connection) {
	server.mu.Lock()
	delete(server.connections, current)
	server.removePairingLocked(current)
	server.removeAuthenticatingLocked(current)
	if current.authenticated {
		clientID := current.principal.ClientID
		if lifecycle := server.clientLifecycle[clientID]; lifecycle != nil {
			delete(lifecycle.connections, current)
		}
		server.removeLifecycleIfIdleLocked(clientID)
		current.authenticated = false
		current.principal = Principal{}
	}
	server.mu.Unlock()
}

func validateAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" || port == "" {
		return fmt.Errorf("browser: address must be exact IPv4 loopback with a port")
	}
	parsed, err := strconv.Atoi(port)
	if err != nil || parsed < 0 || parsed > 65535 || strconv.Itoa(parsed) != port {
		return fmt.Errorf("browser: invalid loopback port")
	}
	return nil
}

func validateOrigins(values []string) (map[string]struct{}, error) {
	if len(values) == 0 || len(values) > maxOrigins {
		return nil, fmt.Errorf("browser: require one to %d exact origins", maxOrigins)
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.String() != value || strings.Contains(value, "*") || len(value) > browserprotocol.MaxTextBytes {
			return nil, fmt.Errorf("browser: invalid exact origin")
		}
		if _, duplicate := result[value]; duplicate {
			return nil, fmt.Errorf("browser: duplicate origin")
		}
		result[value] = struct{}{}
	}
	return result, nil
}

func randomNonce() ([browserprotocol.NonceSize]byte, error) {
	var value [browserprotocol.NonceSize]byte
	_, err := rand.Read(value[:])
	return value, err
}

func newConnectionID() (ConnectionID, error) {
	var id ConnectionID
	if _, err := rand.Read(id.value[:]); err != nil {
		return ConnectionID{}, err
	}
	if id.zero() {
		return ConnectionID{}, fmt.Errorf("browser: random connection identity is zero")
	}
	return id, nil
}

func nonzero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return true
		}
	}
	return false
}

func validateAuthentication(result Authentication) error {
	// A backend proves only durable client authority. Accepting or overwriting a
	// backend-selected connection identity would conceal an authority-boundary
	// violation, so fail closed before transport registration instead.
	if zero16(result.Principal.ClientID) || !result.Principal.ConnectionID.zero() || result.Capabilities&browserprotocol.CapabilityObserve == 0 || result.Capabilities&^implementedCaps != 0 {
		return ErrUnauthorized
	}
	return nil
}

func errorFrame(err error) browserprotocol.Error {
	switch {
	case errors.Is(err, ErrUnauthorized):
		return browserprotocol.Error{Code: browserprotocol.ErrorUnauthorized}
	case errors.Is(err, ErrNotFound):
		return browserprotocol.Error{Code: browserprotocol.ErrorNotFound}
	case errors.Is(err, ErrStale):
		return browserprotocol.Error{Code: browserprotocol.ErrorStale}
	case errors.Is(err, ErrInvalidRequest):
		return browserprotocol.Error{Code: browserprotocol.ErrorInvalidRequest}
	case errors.Is(err, ErrTooLarge):
		return browserprotocol.Error{Code: browserprotocol.ErrorTooLarge}
	case errors.Is(err, ErrRateLimited):
		return browserprotocol.Error{Code: browserprotocol.ErrorRateLimited, Retryable: true}
	default:
		// Say why our own reply was refused, once per reason. ponytail: every 64th new reason forgets the rest.
		if _, seen := refusals.LoadOrStore(err.Error(), true); !seen {
			if refusalCount.Add(1)%64 == 0 {
				refusals.Clear()
			}
			if errors.Is(err, browserprotocol.ErrMalformed) {
				fmt.Fprintf(os.Stderr, "%s factoryd: reply refused: %v\n", time.Now().UTC().Format(time.RFC3339), err)
			}
		}
		return browserprotocol.Error{Code: browserprotocol.ErrorInternal}
	}
}

var (
	refusals     sync.Map
	refusalCount atomic.Int64
)

func zero16(value [16]byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}
	return true
}

// handleOTLP takes OTLP/HTTP traces, metrics or logs, protobuf or JSON,
// optionally gzipped, from local processes only: a browser page carries an
// Origin and cannot send either type without a preflight this listener never
// answers. Bodies are bounded before and after decompression.
func (server *Server) handleOTLP(writer http.ResponseWriter, request *http.Request) {
	traces, _ := server.backend.(TraceReceiver)
	agents, _ := server.backend.(AgentTelemetryReceiver)
	switch {
	case request.URL.Path == TracesPath && traces == nil || request.URL.Path != TracesPath && agents == nil:
		http.Error(writer, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	case request.Method != http.MethodPost:
		http.Error(writer, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	case request.Header.Get("Origin") != "":
		http.Error(writer, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}
	protobuf := strings.HasPrefix(request.Header.Get("Content-Type"), "application/x-protobuf")
	if !protobuf && !strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
		http.Error(writer, http.StatusText(http.StatusUnsupportedMediaType), http.StatusUnsupportedMediaType)
		return
	}
	var reader io.Reader = http.MaxBytesReader(writer, request.Body, browserprotocol.MaxSnapshotBytes)
	if request.Header.Get("Content-Encoding") == "gzip" {
		unzipped, err := gzip.NewReader(reader)
		if err != nil {
			http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
			return
		}
		reader = io.LimitReader(unzipped, browserprotocol.MaxSnapshotBytes+1)
	}
	body, err := io.ReadAll(reader)
	if err == nil && len(body) > browserprotocol.MaxSnapshotBytes {
		err = errors.New("otlp export too large")
	}
	if err == nil && request.URL.Path != TracesPath {
		err = agents.ReceiveAgentTelemetry(request.URL.Path, body, protobuf)
	} else if err == nil {
		if protobuf {
			body, err = opgraph.OTLPProtobufJSON(body)
		}
		if err == nil {
			err = traces.ReceiveTraces(body, request.Header.Get(relayhost.RemoteHeader) != "")
		}
	}
	if err != nil {
		http.Error(writer, http.StatusText(http.StatusBadRequest), http.StatusBadRequest)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write([]byte("{}"))
}

// handlePublic answers only a local reader that names this listener: no page
// (no Origin) and no rebound hostname (Host must be the listener itself).
func (server *Server) handlePublic(writer http.ResponseWriter, request *http.Request) {
	projector, ok := server.backend.(PublicProjector)
	if !ok || request.Method != http.MethodGet || request.Header.Get("Origin") != "" || request.Host != server.host {
		http.Error(writer, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), backendCallLimit)
	defer cancel()
	body, err := projector.PublicWorld(ctx, strings.TrimPrefix(request.URL.Path, PublicPath))
	if err != nil {
		http.Error(writer, http.StatusText(http.StatusNotFound), http.StatusNotFound)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(body)
}
