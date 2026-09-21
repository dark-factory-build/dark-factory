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

	// Waits for the started refresh to finish and reports the window it left.
	settle := func() time.Time {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			releaseMu.Lock()
			pending, next := releasePending, releaseNext
			releaseMu.Unlock()
			if !pending {
				return next
			}
			if time.Now().After(deadline) {
				t.Fatal("the release refresh never settled")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	open := func(body string) {
		t.Helper()
		releaseMu.Lock()
		cached := releaseValue
		releaseNext = time.Time{}
		releaseMu.Unlock()
		bodies <- body
		// The refresh cannot retake the lock before this call returns, so a
		// console request is answered from the cache it had, never from GitHub.
		if served := latestPublishedRelease(); served != cached {
			t.Fatalf("a console request waited for GitHub: %+v", served)
		}
	}

	releaseMu.Lock()
	releaseEndpoint, releaseClient = server.URL, server.Client()
	releaseValue, releasePending = api.PublishedRelease{}, false
	releaseMu.Unlock()

	// A refused read closes the window exactly as a served one does, so a
	// rate-limited or offline host never reaches out more often than a healthy one.
	open(`not json`)
	refused := settle()
	if value := latestPublishedRelease(); value != (api.PublishedRelease{}) {
		t.Fatalf("a refused read invented a release: %+v", value)
	}
	if refused.Before(time.Now().Add(releaseInterval - time.Minute)) {
		t.Fatalf("a refused read reopened the window early: %v", refused)
	}

	open(`{"tag_name":"v0.4.3"}`)
	served := settle()
	if value := latestPublishedRelease(); value.Version != "v0.4.3" {
		t.Fatalf("the cached release never arrived: %+v", value)
	}
	if served.Before(time.Now().Add(releaseInterval - time.Minute)) {
		t.Fatalf("a served release did not cache: %v", served)
	}
	releaseMu.Lock()
	releaseNext = time.Now().Add(24 * time.Hour)
	releaseMu.Unlock()
}
