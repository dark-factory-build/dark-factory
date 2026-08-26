package main

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const testOrigin = "https://preview.example.test"

type eventCollector struct {
	mu     sync.Mutex
	events []ProbeEvent
}

func (c *eventCollector) add(event ProbeEvent) {
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
}

func (c *eventCollector) snapshot() []ProbeEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ProbeEvent(nil), c.events...)
}

func eventServer(t *testing.T) (*Server, *eventCollector) {
	t.Helper()
	collector := &eventCollector{}
	server, err := NewServer(Config{BindHost: defaultHost, Port: 0, ExpectedOrigin: testOrigin, EventWriter: collector.add})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, collector
}

func testServer(t *testing.T) *Server {
	t.Helper()
	server, err := NewServer(Config{BindHost: defaultHost, Port: 0, ExpectedOrigin: testOrigin})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

func TestConfigRequiresLoopbackAndExactOrigin(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "localhost", "::1", "192.0.2.1", ""} {
		config := Config{BindHost: host, Port: 1, ExpectedOrigin: testOrigin, Path: defaultPath, MaxPayload: maxFramePayload}
		if host == "" {
			// NewServer supplies the safe default when the field is omitted.
			continue
		}
		if err := config.validate(); err == nil {
			t.Errorf("validate(%q) accepted a non-loopback bind", host)
		}
	}
	for _, origin := range []string{"", "null", "http://example.test", "https://", "https://example.test/path", "https://user@example.test", "https://example.test?x=1"} {
		config := Config{BindHost: defaultHost, Port: 1, ExpectedOrigin: origin, Path: defaultPath, MaxPayload: maxFramePayload}
		if err := config.validate(); err == nil {
			t.Errorf("validate(%q) accepted an invalid origin", origin)
		}
	}
	if _, err := NewServer(Config{BindHost: defaultHost, Port: 1, ExpectedOrigin: testOrigin}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestFrameMasksAndBinaryOutput(t *testing.T) {
	input := maskedFrame(2, []byte("hello"), [4]byte{1, 2, 3, 4})
	got, err := readFrame(bytes.NewReader(input), maxFramePayload)
	if err != nil {
		t.Fatal(err)
	}
	if got.opcode != 2 || string(got.payload) != "hello" {
		t.Fatalf("decoded frame = opcode %d payload %q", got.opcode, got.payload)
	}
	var output bytes.Buffer
	if err := writeFrame(&output, 2, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), []byte{0x82, 5, 'h', 'e', 'l', 'l', 'o'}) {
		t.Fatalf("server binary frame = %x", output.Bytes())
	}
}

func TestFrameBoundsAndMalformedInput(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"unmasked", []byte{0x82, 1, 'x'}, errInvalidFrame},
		{"fragmented", []byte{0x02, 0x81, 1, 2, 3, 4, 'x'}, errInvalidFrame},
		{"reserved-bit", []byte{0xc2, 0x81, 1, 2, 3, 4, 'x'}, errInvalidFrame},
		{"text", maskedFrame(1, []byte("x"), [4]byte{1, 2, 3, 4}), errInvalidFrame},
		{"oversized", []byte{0x82, 0xff, 0, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0}, errFrameTooLarge},
		{"noncanonical-126", []byte{0x82, 0xfe, 0, 125, 0, 0, 0, 0}, errInvalidFrame},
		{"noncanonical-127", []byte{0x82, 0xff, 0, 0, 0, 0, 0, 0, 0, 125, 0, 0, 0, 0}, errInvalidFrame},
		{"truncated-mask", []byte{0x82, 0x81, 1}, io.ErrUnexpectedEOF},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readFrame(bytes.NewReader(tc.data), maxFramePayload)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
	if err := writeFrame(io.Discard, 2, make([]byte, maxFramePayload+1)); !errors.Is(err, errFrameTooLarge) {
		t.Fatalf("oversized output error = %v", err)
	}
}

func TestServerClosesOnOversizedFrame(t *testing.T) {
	server := testServer(t)
	conn, response, reader, err := openWebSocket(server)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status = %d", response.StatusCode)
	}
	// The declared length is over the bound; no payload allocation or body is
	// needed for the server to refuse it.
	if _, err := conn.Write([]byte{0x82, 0xff, 0, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	closeFrame, err := readFrameWithMask(reader, maxFramePayload, false)
	if err != nil {
		t.Fatal(err)
	}
	if closeFrame.opcode != 8 || !bytes.Equal(closeFrame.payload, closePayload(closeTooBig)) {
		t.Fatalf("oversized refusal = %#v", closeFrame)
	}
}

func TestUpgradeRequiresExactHostAndOrigin(t *testing.T) {
	server := testServer(t)
	cases := []struct {
		name   string
		host   string
		origin *string
		status int
	}{
		{"missing-origin", server.Ready().ExpectedHost, nil, http.StatusForbidden},
		{"null-origin", server.Ready().ExpectedHost, stringPtr("null"), http.StatusForbidden},
		{"wrong-origin", server.Ready().ExpectedHost, stringPtr("https://other.example.test"), http.StatusForbidden},
		{"wrong-host", "127.0.0.1:1", stringPtr(testOrigin), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, response, _, err := handshake(server, tc.host, tc.origin)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", response.StatusCode, tc.status)
			}
		})
	}
	conn, response, reader, err := openWebSocket(server)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("valid upgrade status = %d", response.StatusCode)
	}
	accept := sha1.Sum([]byte(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16)) + webSocketGUID))
	if response.Header.Get("Upgrade") != "websocket" || response.Header.Get("Connection") != "Upgrade" || response.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(accept[:]) {
		t.Fatalf("invalid websocket handshake response: %#v", response.Header)
	}
	if err := writeClientFrame(conn, 2, []byte("echo")); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	echo, err := readFrameWithMask(reader, maxFramePayload, false)
	if err != nil || echo.opcode != 2 || string(echo.payload) != "echo" {
		t.Fatalf("echo = %#v, error = %v", echo, err)
	}
}

func TestReconnectAndGracefulClose(t *testing.T) {
	server := testServer(t)
	for attempt := 0; attempt < 2; attempt++ {
		conn, response, reader, err := openWebSocket(server)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("attempt %d status = %d", attempt, response.StatusCode)
		}
		if err := writeClientFrame(conn, 8, []byte{3, 232}); err != nil {
			t.Fatal(err)
		}
		conn.SetReadDeadline(time.Now().Add(time.Second))
		closeFrame, err := readFrameWithMask(reader, maxFramePayload, false)
		if err != nil || closeFrame.opcode != 8 || !bytes.Equal(closeFrame.payload, []byte{3, 232}) {
			t.Fatalf("close reply = %#v, error = %v", closeFrame, err)
		}
		conn.Close()
		waitForConnections(t, server, 0)
	}
}

func TestEvidenceEventsAreStructuredAndBounded(t *testing.T) {
	server, collector := eventServer(t)
	conn, response, _, err := handshake(server, server.Ready().ExpectedHost, stringPtr("https://wrong.example.test"))
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong origin status = %d", response.StatusCode)
	}
	conn, response, reader, err := openWebSocket(server)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("valid upgrade status = %d", response.StatusCode)
	}
	if err := writeClientFrame(conn, 2, []byte("evidence")); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := readFrameWithMask(reader, maxFramePayload, false); err != nil {
		t.Fatal(err)
	}
	if err := writeClientFrame(conn, 8, closePayload(1000)); err != nil {
		t.Fatal(err)
	}
	if _, err := readFrameWithMask(reader, maxFramePayload, false); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	waitForConnections(t, server, 0)
	conn, _, _, err = openWebSocket(server)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	waitForConnections(t, server, 0)

	events := collector.snapshot()
	var rejected, accepted, inbound, outbound, graceful bool
	openCount := 0
	for _, event := range events {
		switch event.Event {
		case "upgrade":
			if event.Result == "origin" {
				rejected = event.Host == server.Ready().ExpectedHost && event.Origin == "https://wrong.example.test" && event.Path == defaultPath && event.Status == http.StatusForbidden
			}
			if event.Result == "accepted" {
				accepted = event.Host == server.Ready().ExpectedHost && event.Origin == testOrigin && event.Path == defaultPath && event.Status == http.StatusSwitchingProtocols
			}
		case "frame":
			inbound = inbound || event.Direction == "inbound" && event.Frame == "binary" && event.Bytes == len("evidence")
			outbound = outbound || event.Direction == "outbound" && event.Frame == "binary" && event.Bytes == len("evidence")
		case "close":
			graceful = graceful || event.Result == "graceful" && event.Code == 1000
		case "connection":
			if event.Phase == "open" {
				openCount++
			}
		}
	}
	if !rejected || !accepted || !inbound || !outbound || !graceful || openCount != 2 {
		t.Fatalf("missing structured evidence: %#v", events)
	}
}

func TestShutdownClosesConnectedSocket(t *testing.T) {
	server := testServer(t)
	conn, response, _, err := openWebSocket(server)
	if err != nil {
		t.Fatal(err)
	}
	waitForConnections(t, server, 1)
	if response.StatusCode != http.StatusSwitchingProtocols || server.ActiveConnections() != 1 {
		t.Fatalf("connection did not register")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	var one [1]byte
	if _, err := conn.Read(one[:]); err == nil {
		t.Fatal("closed server left socket readable")
	}
	if server.ActiveConnections() != 0 {
		t.Fatalf("active connections after close = %d", server.ActiveConnections())
	}
}

func TestDuplicateHeadersHTTPVersionForceQueryAndCapacityRefuse(t *testing.T) {
	server := testServer(t)
	for _, extra := range []string{
		"Upgrade: websocket\r\n",
		"Connection: Upgrade\r\n",
		"Sec-WebSocket-Version: 13\r\n",
	} {
		conn, response, _, err := handshakeWith(server, server.Ready().ExpectedHost, stringPtr(testOrigin), "HTTP/1.1", server.Ready().Path, extra)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("duplicate header status = %d", response.StatusCode)
		}
	}
	conn, response, _, err := handshakeWith(server, server.Ready().ExpectedHost, stringPtr(testOrigin), "HTTP/1.0", server.Ready().Path, "")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if response.StatusCode != http.StatusHTTPVersionNotSupported {
		t.Fatalf("HTTP/1.0 status = %d", response.StatusCode)
	}
	conn, response, _, err = handshakeWith(server, server.Ready().ExpectedHost, stringPtr(testOrigin), "HTTP/1.1", server.Ready().Path+"?", "")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("ForceQuery status = %d", response.StatusCode)
	}

	connections := make([]net.Conn, 0, maxConnections)
	for i := 0; i < maxConnections; i++ {
		conn, response, _, err := openWebSocket(server)
		if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
			t.Fatalf("capacity connection %d: response=%v error=%v", i, response, err)
		}
		connections = append(connections, conn)
	}
	conn, response, _, err = openWebSocket(server)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("over-capacity status = %d", response.StatusCode)
	}
	for _, conn := range connections {
		conn.Close()
	}
	waitForConnections(t, server, 0)
}

func TestPartialFrameReadDeadline(t *testing.T) {
	server, err := NewServer(Config{BindHost: defaultHost, Port: 0, ExpectedOrigin: testOrigin, ReadTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.Start(); err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	conn, response, reader, err := openWebSocket(server)
	if err != nil || response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade response=%v error=%v", response, err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{0x82, 0x81, 1}); err != nil {
		t.Fatal(err)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	closeFrame, err := readFrameWithMask(reader, maxFramePayload, false)
	if err != nil || closeFrame.opcode != 8 || !bytes.Equal(closeFrame.payload, closePayload(closeProtocol)) {
		t.Fatalf("partial frame refusal=%#v error=%v", closeFrame, err)
	}
}

func TestProbePageIsSelfContainedAndCredentialFree(t *testing.T) {
	data, err := os.ReadFile("probe.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(data)
	for _, forbidden := range []string{"<script src=", "<link rel=", "location.search", "location.hash", "fetch("} {
		if strings.Contains(strings.ToLower(page), strings.ToLower(forbidden)) {
			t.Errorf("probe page contains forbidden %q", forbidden)
		}
	}
	if !strings.Contains(page, "ws://127.0.0.1:43123/browser/v1") {
		t.Error("probe page does not contain the documented loopback URL")
	}
	if strings.Contains(page, "setTimeout") || !strings.Contains(page, "socket.close(1000") {
		t.Error("explicit close must not schedule an automatic reconnect")
	}
	for _, required := range []string{
		"const thisGeneration = ++generation",
		"try {\n        connection = new WebSocket",
		"catch (error) {",
		"successfulGeneration = true",
		"binary send requested while closed",
		"binary send failed",
	} {
		if !strings.Contains(page, required) {
			t.Errorf("probe page lacks failure-safe behavior %q", required)
		}
	}
}

func TestReadinessAndEventsShareOneOrderedWriter(t *testing.T) {
	var output bytes.Buffer
	writer := &jsonEventWriter{encoder: json.NewEncoder(&output)}
	writer.WriteReady(Ready{URL: "ws://127.0.0.1:1/browser/v1"})
	writer.Write(ProbeEvent{Event: "upgrade", Result: "accepted", Status: http.StatusSwitchingProtocols})
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"url":"ws://127.0.0.1:1/browser/v1"`) || !strings.Contains(lines[1], `"event":"upgrade"`) {
		t.Fatalf("readiness/events order = %q", output.String())
	}
}

func stringPtr(value string) *string { return &value }

func openWebSocket(server *Server) (net.Conn, *http.Response, *bufio.Reader, error) {
	return handshake(server, server.Ready().ExpectedHost, stringPtr(server.Ready().ExpectedOrigin))
}

func handshake(server *Server, host string, origin *string) (net.Conn, *http.Response, *bufio.Reader, error) {
	return handshakeWith(server, host, origin, "HTTP/1.1", server.Ready().Path, "")
}

func handshakeWith(server *Server, host string, origin *string, protocol, path, extra string) (net.Conn, *http.Response, *bufio.Reader, error) {
	conn, err := net.Dial("tcp", server.listener.Addr().String())
	if err != nil {
		return nil, nil, nil, err
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 16))
	request := fmt.Sprintf("GET %s %s\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n", path, protocol, host, key)
	if origin != nil {
		request += "Origin: " + *origin + "\r\n"
	}
	request += extra
	request += "\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		conn.Close()
		return nil, nil, nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		conn.Close()
		return nil, nil, nil, err
	}
	return conn, response, reader, nil
}

func writeClientFrame(conn net.Conn, opcode byte, payload []byte) error {
	return writeMasked(conn, maskedFrame(opcode, payload, [4]byte{5, 6, 7, 8}))
}

func maskedFrame(opcode byte, payload []byte, mask [4]byte) []byte {
	var frame bytes.Buffer
	frame.WriteByte(0x80 | opcode)
	switch {
	case len(payload) < 126:
		frame.WriteByte(0x80 | byte(len(payload)))
	case len(payload) <= 65535:
		frame.Write([]byte{0x80 | 126, byte(len(payload) >> 8), byte(len(payload))})
	default:
		frame.Write([]byte{0x80 | 127, 0, 0, 0, 0, byte(len(payload) >> 24), byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload))})
	}
	frame.Write(mask[:])
	for i, value := range payload {
		frame.WriteByte(value ^ mask[i%4])
	}
	return frame.Bytes()
}

func writeMasked(w io.Writer, data []byte) error {
	_, err := w.Write(data)
	return err
}

func waitForConnections(t *testing.T, server *Server, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if server.ActiveConnections() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("active connections = %d, want %d", server.ActiveConnections(), want)
}
