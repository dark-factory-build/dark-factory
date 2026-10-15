package browser

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestConsoleAndShellServeOnlyTheExactHostOwnPageOrAnAllowedOrigin(t *testing.T) {
	server, err := Listen(Config{Address: "127.0.0.1:0", AllowedOrigins: []string{testOrigin}, Backend: newFakeBackend(), Console: []byte("signed")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	get := func(path, host, origin, site string) (int, string, string) {
		request, _ := http.NewRequest(http.MethodGet, "http://"+server.Addr()+path, nil)
		request.Host = host
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		if site != "" {
			request.Header.Set("Sec-Fetch-Site", site)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, response.Header.Get("Access-Control-Allow-Origin"), string(body)
	}
	if status, allowed, body := get(ConsolePath, server.Addr(), testOrigin, "cross-site"); status != http.StatusOK || allowed != testOrigin || body != "signed" {
		t.Fatalf("allowed origin = %d %q %q", status, allowed, body)
	}
	if status, allowed, body := get(ConsolePath, server.Addr(), "", "same-origin"); status != http.StatusOK || allowed != "" || body != "signed" {
		t.Fatalf("own page = %d %q %q", status, allowed, body)
	}
	for _, site := range []string{"none", "same-origin"} {
		if status, _, body := get("/", server.Addr(), "", site); status != http.StatusOK || !strings.Contains(body, `fetch("/console.js")`) {
			t.Fatalf("shell %s = %d %q", site, status, body)
		}
	}
	for _, path := range []string{ConsolePath, "/"} {
		for _, refused := range [][3]string{{server.Addr(), devOrigin, "cross-site"}, {server.Addr(), "", ""}, {server.Addr(), "", "cross-site"}, {"localhost", testOrigin, "cross-site"}, {"localhost", "", "none"}} {
			if status, allowed, _ := get(path, refused[0], refused[1], refused[2]); status != http.StatusNotFound || allowed != "" {
				t.Fatalf("%s host %q origin %q site %q = %d %q", path, refused[0], refused[1], refused[2], status, allowed)
			}
		}
	}
}

// TestHeldSocketKeepsTheConsoleOriginAcrossARestart stands in for launchd: a
// socket held outside the server must outlive the server's close, refuse a
// second local listener (even one asking SO_REUSEPORT) while no server runs,
// and serve the next server on the same console origin.
func TestHeldSocketKeepsTheConsoleOriginAcrossARestart(t *testing.T) {
	held, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	socket, err := held.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = socket.Close() })
	address := held.Addr().String()
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Listen(Config{Address: "127.0.0.1:1", AllowedOrigins: []string{testOrigin}, Backend: newFakeBackend(), Console: []byte("signed"), Socket: socket}); err == nil {
		t.Fatal("a held socket served another address")
	}
	reusePort := net.ListenConfig{Control: func(_, _ string, raw syscall.RawConn) error {
		var setErr error
		err := raw.Control(func(fd uintptr) { setErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1) })
		return errors.Join(err, setErr)
	}}
	for restart := range 2 {
		server, err := Listen(Config{Address: address, AllowedOrigins: []string{testOrigin}, Backend: newFakeBackend(), Console: []byte("signed"), Socket: socket})
		if err != nil {
			t.Fatalf("restart %d: %v", restart, err)
		}
		request, _ := http.NewRequest(http.MethodGet, "http://"+address+"/", nil)
		request.Header.Set("Sec-Fetch-Site", "none")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if server.Addr() != address || response.StatusCode != http.StatusOK || !strings.Contains(string(body), `fetch("/console.js")`) {
			t.Fatalf("restart %d: %s served %d %q", restart, server.Addr(), response.StatusCode, body)
		}
		http.DefaultClient.CloseIdleConnections()
		if err := server.Close(); err != nil {
			t.Fatal(err)
		}
		if squatter, err := net.Listen("tcp4", address); err == nil {
			_ = squatter.Close()
			t.Fatalf("restart %d: a second listener took %s", restart, address)
		}
		if squatter, err := reusePort.Listen(context.Background(), "tcp4", address); err == nil {
			_ = squatter.Close()
			t.Fatalf("restart %d: a SO_REUSEPORT listener took %s", restart, address)
		}
	}
}
