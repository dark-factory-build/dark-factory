// Package main contains a deliberately disposable WebSocket connectivity
// probe. It is not part of the Dark Factory runtime.
package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultHost      = "127.0.0.1"
	defaultPath      = "/browser/v1"
	maxFramePayload  = 64 * 1024
	maxHeaderBytes   = 16 * 1024
	maxConnections   = 8
	frameReadTimeout = 5 * time.Second
	webSocketGUID    = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	closeNormal      = 1000
	closeProtocol    = 1002
	closeTooBig      = 1009
)

var errInvalidFrame = errors.New("invalid websocket frame")

// Config is the complete policy surface of the probe. BindHost intentionally
// has no "any" mode: widening the listener is a configuration error.
type Config struct {
	BindHost       string
	Port           int
	ExpectedOrigin string
	Path           string
	MaxPayload     int
	EventWriter    func(ProbeEvent)
	ReadyWriter    func(Ready)
	ReadTimeout    time.Duration
}

func (c Config) validate() error {
	if c.BindHost != defaultHost {
		return fmt.Errorf("bind host must be exactly %q", defaultHost)
	}
	if c.Port < 0 || c.Port > 65535 {
		return fmt.Errorf("port must be between 0 and 65535")
	}
	if c.Path == "" || !strings.HasPrefix(c.Path, "/") || strings.ContainsAny(c.Path, "?#") {
		return fmt.Errorf("path must be an absolute path without query or fragment")
	}
	if c.MaxPayload <= 0 || c.MaxPayload > maxFramePayload {
		return fmt.Errorf("max payload must be between 1 and %d", maxFramePayload)
	}
	if c.ReadTimeout < 0 {
		return fmt.Errorf("read timeout cannot be negative")
	}
	u, err := url.Parse(c.ExpectedOrigin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("expected origin must be an origin such as https://app.example")
	}
	return nil
}

// Ready is printed as one JSON object by the command. URLs and policy values
// are explicit so a browser test never has to guess the port or origin.
type Ready struct {
	URL            string `json:"url"`
	ExpectedHost   string `json:"expected_host"`
	ExpectedOrigin string `json:"expected_origin"`
	Path           string `json:"path"`
	MaxFrameBytes  int    `json:"max_frame_bytes"`
}

// ProbeEvent is the intentionally small JSONL evidence vocabulary. It records
// policy inputs and lifecycle facts, never arbitrary HTTP headers or payloads.
type ProbeEvent struct {
	Event           string `json:"event"`
	Result          string `json:"result,omitempty"`
	Phase           string `json:"phase,omitempty"`
	Host            string `json:"host"`
	Origin          string `json:"origin"`
	Path            string `json:"path"`
	Status          int    `json:"status,omitempty"`
	Direction       string `json:"direction,omitempty"`
	Frame           string `json:"frame,omitempty"`
	Bytes           int    `json:"bytes,omitempty"`
	Code            int    `json:"code,omitempty"`
	ConnectionIndex uint64 `json:"connection_index,omitempty"`
}

// Server owns only the probe listener and accepted WebSocket connections.
type Server struct {
	config Config
	ready  Ready

	listener net.Listener
	http     *http.Server

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	nextID uint64
	wg     sync.WaitGroup
	slots  chan struct{}
}

func NewServer(config Config) (*Server, error) {
	if config.BindHost == "" {
		config.BindHost = defaultHost
	}
	if config.Path == "" {
		config.Path = defaultPath
	}
	if config.MaxPayload == 0 {
		config.MaxPayload = maxFramePayload
	}
	if config.ReadTimeout == 0 {
		config.ReadTimeout = frameReadTimeout
	}
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Server{config: config, conns: make(map[net.Conn]struct{}), slots: make(chan struct{}, maxConnections)}, nil
}

// Start binds before returning, making readiness deterministic. Port zero is
// useful for tests; a normal smoke run should use a fixed disposable port.
func (s *Server) Start() (Ready, error) {
	if s.listener != nil {
		return Ready{}, errors.New("server already started")
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(s.config.BindHost, strconv.Itoa(s.config.Port)))
	if err != nil {
		return Ready{}, err
	}
	s.listener = listener
	port := listener.Addr().(*net.TCPAddr).Port
	s.ready = Ready{
		URL:            "ws://" + net.JoinHostPort(defaultHost, strconv.Itoa(port)) + s.config.Path,
		ExpectedHost:   net.JoinHostPort(defaultHost, strconv.Itoa(port)),
		ExpectedOrigin: s.config.ExpectedOrigin,
		Path:           s.config.Path,
		MaxFrameBytes:  s.config.MaxPayload,
	}
	s.http = &http.Server{
		Handler:           http.HandlerFunc(s.handleHTTP),
		ReadHeaderTimeout: 2 * time.Second,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	if s.config.ReadyWriter != nil {
		s.config.ReadyWriter(s.ready)
	}
	go func() {
		_ = s.http.Serve(listener)
	}()
	return s.ready, nil
}

func (s *Server) Ready() Ready { return s.ready }

// Close is idempotent and waits for every upgraded connection handler. Closing
// the socket first unblocks a handler in Read or Write, so no goroutine leaks.
func (s *Server) Close() error {
	if s.http == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.wg.Wait()
		return nil
	}
	s.closed = true
	connections := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		connections = append(connections, conn)
	}
	s.mu.Unlock()
	err := s.http.Close()
	for _, conn := range connections {
		_ = conn.Close()
	}
	s.wg.Wait()
	return err
}

func (s *Server) ActiveConnections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.conns)
}

func (s *Server) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if r.ProtoMajor != 1 || r.ProtoMinor != 1 {
		s.rejectUpgrade(w, r, http.StatusHTTPVersionNotSupported, "http_version", "HTTP/1.1 required")
		return
	}
	if r.Method != http.MethodGet || r.URL.Path != s.config.Path || r.URL.RawQuery != "" || r.URL.ForceQuery {
		s.rejectUpgrade(w, r, http.StatusNotFound, "path", "websocket endpoint only")
		return
	}
	if r.Host != s.ready.ExpectedHost {
		s.rejectUpgrade(w, r, http.StatusForbidden, "host", "host refused")
		return
	}
	origins := r.Header.Values("Origin")
	if len(origins) != 1 || origins[0] != s.config.ExpectedOrigin {
		s.rejectUpgrade(w, r, http.StatusForbidden, "origin", "origin refused")
		return
	}
	if len(r.Header.Values("Connection")) != 1 || len(r.Header.Values("Upgrade")) != 1 || len(r.Header.Values("Sec-WebSocket-Version")) != 1 || !headerToken(r.Header.Get("Connection"), "upgrade") || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		s.rejectUpgrade(w, r, http.StatusBadRequest, "handshake", "websocket upgrade required")
		return
	}
	if r.Header.Get("Sec-WebSocket-Version") != "13" {
		s.rejectUpgrade(w, r, http.StatusBadRequest, "handshake", "websocket version 13 required")
		return
	}
	key := r.Header.Get("Sec-WebSocket-Key")
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decoded) != 16 || len(r.Header.Values("Sec-WebSocket-Key")) != 1 {
		s.rejectUpgrade(w, r, http.StatusBadRequest, "handshake", "invalid websocket key")
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		s.rejectUpgrade(w, r, http.StatusInternalServerError, "handshake", "hijacking unavailable")
		return
	}
	select {
	case s.slots <- struct{}{}:
	default:
		s.rejectUpgrade(w, r, http.StatusServiceUnavailable, "capacity", "connection capacity reached")
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		<-s.slots
		return
	}
	acceptHash := sha1.Sum([]byte(key + webSocketGUID))
	_, writeErr := fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(acceptHash[:]))
	if writeErr == nil {
		writeErr = rw.Flush()
	}
	if writeErr != nil {
		_ = conn.Close()
		return
	}
	connectionID, registered := s.addConnection(conn)
	if !registered {
		<-s.slots
		_ = conn.Close()
		return
	}
	s.emit(ProbeEvent{Event: "upgrade", Result: "accepted", Host: r.Host, Origin: origins[0], Path: r.URL.Path, Status: http.StatusSwitchingProtocols})
	s.emit(ProbeEvent{Event: "connection", Phase: "open", ConnectionIndex: connectionID})
	defer func() {
		s.emit(ProbeEvent{Event: "connection", Phase: "closed", ConnectionIndex: connectionID})
		s.removeConnection(conn)
		<-s.slots
		_ = conn.Close()
	}()
	s.serveWebSocket(conn, rw, connectionID)
}

func (s *Server) addConnection(conn net.Conn) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, false
	}
	s.nextID++
	s.conns[conn] = struct{}{}
	s.wg.Add(1)
	return s.nextID, true
}

func (s *Server) removeConnection(conn net.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
	s.wg.Done()
}

func (s *Server) serveWebSocket(conn net.Conn, rw *bufio.ReadWriter, connectionID uint64) {
	for {
		_ = conn.SetReadDeadline(time.Now().Add(s.config.ReadTimeout))
		frame, err := readFrame(rw.Reader, s.config.MaxPayload)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return
			}
			code := closeProtocol
			if errors.Is(err, errFrameTooLarge) {
				code = closeTooBig
			}
			s.emit(ProbeEvent{Event: "frame", Result: "rejected", Direction: "inbound", Frame: "invalid", ConnectionIndex: connectionID})
			_ = writeClose(rw, code)
			_ = rw.Flush()
			s.emit(ProbeEvent{Event: "close", Result: "protocol_error", Code: code, ConnectionIndex: connectionID})
			return
		}
		switch frame.opcode {
		case 2: // binary
			s.emit(ProbeEvent{Event: "frame", Result: "accepted", Direction: "inbound", Frame: "binary", Bytes: len(frame.payload), ConnectionIndex: connectionID})
			if err := writeFrame(rw, 2, frame.payload); err != nil {
				return
			}
			s.emit(ProbeEvent{Event: "frame", Result: "sent", Direction: "outbound", Frame: "binary", Bytes: len(frame.payload), ConnectionIndex: connectionID})
		case 8: // close
			_ = writeFrame(rw, 8, frame.payload)
			_ = rw.Flush()
			code := closeNormal
			if len(frame.payload) >= 2 {
				code = int(frame.payload[0])<<8 | int(frame.payload[1])
			}
			s.emit(ProbeEvent{Event: "close", Result: "graceful", Code: code, ConnectionIndex: connectionID})
			return
		case 9: // ping
			s.emit(ProbeEvent{Event: "frame", Result: "accepted", Direction: "inbound", Frame: "ping", Bytes: len(frame.payload), ConnectionIndex: connectionID})
			if err := writeFrame(rw, 10, frame.payload); err != nil {
				return
			}
			s.emit(ProbeEvent{Event: "frame", Result: "sent", Direction: "outbound", Frame: "pong", Bytes: len(frame.payload), ConnectionIndex: connectionID})
		case 10: // pong
			// No application action is needed.
			s.emit(ProbeEvent{Event: "frame", Result: "accepted", Direction: "inbound", Frame: "pong", Bytes: len(frame.payload), ConnectionIndex: connectionID})
		default:
			_ = writeClose(rw, closeProtocol)
			_ = rw.Flush()
			s.emit(ProbeEvent{Event: "close", Result: "protocol_error", Code: closeProtocol, ConnectionIndex: connectionID})
			return
		}
		if err := rw.Flush(); err != nil {
			return
		}
	}
}

func (s *Server) rejectUpgrade(w http.ResponseWriter, r *http.Request, status int, result, message string) {
	s.emit(ProbeEvent{Event: "upgrade", Result: result, Host: r.Host, Origin: r.Header.Get("Origin"), Path: r.URL.Path, Status: status})
	rejectHTTP(w, status, message)
}

func (s *Server) emit(event ProbeEvent) {
	if s.config.EventWriter != nil {
		s.config.EventWriter(event)
	}
}

func rejectHTTP(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Connection", "close")
	http.Error(w, message, status)
}

func headerToken(value, wanted string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), wanted) {
			return true
		}
	}
	return false
}

type frame struct {
	opcode  byte
	payload []byte
}

var errFrameTooLarge = errors.New("websocket frame exceeds bound")

func readFrame(r io.Reader, maxPayload int) (frame, error) {
	return readFrameWithMask(r, maxPayload, true)
}

func readFrameWithMask(r io.Reader, maxPayload int, requireMask bool) (frame, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return frame{}, err
	}
	if header[0]&0x70 != 0 || header[0]&0x80 == 0 {
		return frame{}, errInvalidFrame
	}
	fin, opcode := header[0]&0x80 != 0, header[0]&0x0f
	if !fin || (opcode != 2 && opcode != 8 && opcode != 9 && opcode != 10) {
		return frame{}, errInvalidFrame
	}
	masked := header[1]&0x80 != 0
	if masked != requireMask {
		return frame{}, errInvalidFrame
	}
	length := uint64(header[1] & 0x7f)
	if length == 126 {
		var extended [2]byte
		if _, err := io.ReadFull(r, extended[:]); err != nil {
			return frame{}, err
		}
		length = uint64(extended[0])<<8 | uint64(extended[1])
		if length < 126 {
			return frame{}, errInvalidFrame
		}
	} else if length == 127 {
		var extended [8]byte
		if _, err := io.ReadFull(r, extended[:]); err != nil {
			return frame{}, err
		}
		if extended[0]&0x80 != 0 {
			return frame{}, errInvalidFrame
		}
		for _, b := range extended {
			length = (length << 8) | uint64(b)
		}
		if length < 65536 {
			return frame{}, errInvalidFrame
		}
	}
	if length > uint64(maxPayload) || (opcode >= 8 && length > 125) {
		return frame{}, errFrameTooLarge
	}
	var mask [4]byte
	if requireMask {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return frame{}, err
		}
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(r, payload); err != nil {
		return frame{}, err
	}
	if requireMask {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	if opcode == 8 && !validClosePayload(payload) {
		return frame{}, errInvalidFrame
	}
	return frame{opcode: opcode, payload: payload}, nil
}

func validClosePayload(payload []byte) bool {
	if len(payload) == 0 {
		return true
	}
	if len(payload) == 1 || (len(payload) >= 2 && !utf8.Valid(payload[2:])) {
		return false
	}
	code := int(payload[0])<<8 | int(payload[1])
	switch {
	case code >= 1000 && code <= 1003:
	case code >= 1007 && code <= 1011:
	default:
		return false
	}
	return true
}

func writeFrame(w io.Writer, opcode byte, payload []byte) error {
	if len(payload) > maxFramePayload || opcode&0x08 != 0 && len(payload) > 125 {
		return errFrameTooLarge
	}
	var header []byte
	switch {
	case len(payload) < 126:
		header = []byte{0x80 | opcode, byte(len(payload))}
	case len(payload) <= 65535:
		header = []byte{0x80 | opcode, 126, byte(len(payload) >> 8), byte(len(payload))}
	default:
		header = []byte{0x80 | opcode, 127, 0, 0, 0, 0, byte(len(payload) >> 24), byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload))}
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func writeClose(w io.Writer, code int) error {
	return writeFrame(w, 8, closePayload(code))
}

func closePayload(code int) []byte {
	return []byte{byte(code >> 8), byte(code)}
}
