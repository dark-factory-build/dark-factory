package browser

import (
	"io"
	"net/http"
	"strings"
	"testing"
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
