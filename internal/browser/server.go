package browser

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
)

const (
	Path           = "/browser/v1"
	maxOrigins     = 8
	maxConnections = 32
	// A maximum valid 4,096-entity state needs at most 516 page requests.
	// 1,024 retains duplicate IDs for the whole connection while leaving room
	// for entity refreshes, detail reads and subscription restart.
	maxRequests         = 1024
	readQueueSize       = 8
	maxHeaderBytes      = 8 << 10
	authenticationLimit = 5 * time.Second
	writeLimit          = 2 * time.Second
	readHeaderLimit     = 2 * time.Second
	shutdownHeaderLimit = 2 * time.Second
	implementedCaps     = browserprotocol.CapabilityObserve | browserprotocol.CapabilityPrivateHumanRequestDetail
)

type Config struct {
	Address        string
	AllowedOrigins []string
	Backend        Backend
}

type Server struct {
	backend  Backend
	host     string
	origins  map[string]struct{}
	listener net.Listener
	http     *http.Server

	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	closing     bool
	connections map[*connection]struct{}
	clients     map[[browserprotocol.ClientIDSize]byte]map[*connection]struct{}
	slots       chan struct{}
	serveDone   chan struct{}
	closeOnce   sync.Once
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
	ctx, cancel := context.WithCancel(context.Background())
	server := &Server{
		backend:     config.Backend,
		host:        listener.Addr().String(),
		origins:     origins,
		listener:    listener,
		ctx:         ctx,
		cancel:      cancel,
		connections: make(map[*connection]struct{}),
		clients:     make(map[[browserprotocol.ClientIDSize]byte]map[*connection]struct{}),
		slots:       make(chan struct{}, maxConnections),
		serveDone:   make(chan struct{}),
	}
	server.http = &http.Server{
		Handler:           http.HandlerFunc(server.handle),
		ReadHeaderTimeout: readHeaderLimit,
		IdleTimeout:       shutdownHeaderLimit,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	go func() {
		defer close(server.serveDone)
		_ = server.http.Serve(listener)
	}()
	return server, nil
}

func (server *Server) Addr() string {
	if server == nil {
		return ""
	}
	return server.host
}

func (server *Server) Close() error {
	if server == nil {
		return nil
	}
	server.closeOnce.Do(func() {
		server.mu.Lock()
		server.closing = true
		connections := make([]*connection, 0, len(server.connections))
		for current := range server.connections {
			connections = append(connections, current)
		}
		server.mu.Unlock()
		server.cancel()
		_ = server.http.Close()
		for _, current := range connections {
			current.stop()
		}
		for _, current := range connections {
			<-current.done
		}
		<-server.serveDone
	})
	return nil
}

// CloseClient is the non-reentrant revocation seam. The daemon commits
// revocation before calling it; this method performs no authorization call.
func (server *Server) CloseClient(clientID [browserprotocol.ClientIDSize]byte) {
	if server == nil || zero16(clientID) {
		return
	}
	server.mu.Lock()
	set := server.clients[clientID]
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
}

func (server *Server) handle(writer http.ResponseWriter, request *http.Request) {
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

func (server *Server) registerPrincipal(current *connection, principal Principal) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.closing {
		return false
	}
	current.principal = principal
	current.authenticated = true
	set := server.clients[principal.ClientID]
	if set == nil {
		set = make(map[*connection]struct{})
		server.clients[principal.ClientID] = set
	}
	set[current] = struct{}{}
	return true
}

func (server *Server) unregister(current *connection) {
	server.mu.Lock()
	delete(server.connections, current)
	if current.authenticated {
		set := server.clients[current.principal.ClientID]
		delete(set, current)
		if len(set) == 0 {
			delete(server.clients, current.principal.ClientID)
		}
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

func nonzero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return true
		}
	}
	return false
}

func validatePrincipal(principal Principal) error {
	if zero16(principal.ClientID) || principal.Capabilities&browserprotocol.CapabilityObserve == 0 || principal.Capabilities&^implementedCaps != 0 {
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
	case errors.Is(err, ErrTooLarge):
		return browserprotocol.Error{Code: browserprotocol.ErrorTooLarge}
	case errors.Is(err, ErrRateLimited):
		return browserprotocol.Error{Code: browserprotocol.ErrorRateLimited, Retryable: true}
	default:
		return browserprotocol.Error{Code: browserprotocol.ErrorInternal}
	}
}
