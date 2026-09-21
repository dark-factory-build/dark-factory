package daemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

// No other test in this package reaches GitHub: the shared window starts closed
// and the release test opens it against its own server.
func init() { releaseNext = time.Now().Add(24 * time.Hour) }

func TestPublishedReleaseIsValidatedCachedAndNeverBlocksTheConsole(t *testing.T) {
	bodies := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, <-bodies)
	}))
	defer server.Close()
	read := func(body string) (api.PublishedRelease, error) {
		bodies <- body
		return readPublishedRelease(context.Background(), server.Client(), server.URL)
	}

	value, err := read(`{"tag_name":"v0.4.2","html_url":"https://evil.test/anything"}`)
	if err != nil || value != (api.PublishedRelease{Version: "v0.4.2", URL: releaseTagURL + "v0.4.2"}) {
		t.Fatalf("release = %+v, err = %v", value, err)
	}
	for _, body := range []string{
		`{"tag_name":"v0.4.2","draft":true}`,
		`{"tag_name":"v0.4.2","prerelease":true}`,
		`{"tag_name":"../../other/repo/releases/tag/v1"}`,
		`{"tag_name":"v1 <script>"}`,
		`{"tag_name":""}`,
		`not json`,
	} {
		if _, err := read(body); err == nil {
			t.Fatalf("accepted %s", body)
		}
	}

	releaseMu.Lock()
	releaseEndpoint, releaseClient = server.URL, server.Client()
	releaseValue, releaseNext, releasePending = api.PublishedRelease{}, time.Time{}, false
	releaseMu.Unlock()
	if first := latestPublishedRelease(); first != (api.PublishedRelease{}) {
		t.Fatalf("a console request waited for GitHub: %+v", first)
	}
	bodies <- `{"tag_name":"v0.4.3"}`
	deadline := time.Now().Add(10 * time.Second)
	for latestPublishedRelease().Version != "v0.4.3" {
		if time.Now().After(deadline) {
			t.Fatal("the cached release never arrived")
		}
		time.Sleep(10 * time.Millisecond)
	}
	releaseMu.Lock()
	next, pending := releaseNext, releasePending
	releaseNext = time.Now().Add(24 * time.Hour)
	releaseMu.Unlock()
	if pending || next.Before(time.Now().Add(releaseInterval-time.Minute)) {
		t.Fatalf("a served release did not cache: pending=%v next=%v", pending, next)
	}
}
