package browser

import (
	"io"
	"net/http"
	"testing"
)

func TestConsoleServesOnlyAnAllowedOriginOnTheExactHost(t *testing.T) {
	server, err := Listen(Config{Address: "127.0.0.1:0", AllowedOrigins: []string{testOrigin}, Backend: newFakeBackend(), Console: []byte("signed")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	get := func(host, origin string) (int, string, string) {
		request, _ := http.NewRequest(http.MethodGet, "http://"+server.Addr()+ConsolePath, nil)
		request.Host = host
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, response.Header.Get("Access-Control-Allow-Origin"), string(body)
	}
	if status, allowed, body := get(server.Addr(), testOrigin); status != http.StatusOK || allowed != testOrigin || body != "signed" {
		t.Fatalf("allowed origin = %d %q %q", status, allowed, body)
	}
	for _, refused := range [][2]string{{server.Addr(), devOrigin}, {server.Addr(), ""}, {"localhost", testOrigin}} {
		if status, allowed, _ := get(refused[0], refused[1]); status != http.StatusNotFound || allowed != "" {
			t.Fatalf("host %q origin %q = %d %q", refused[0], refused[1], status, allowed)
		}
	}
}
