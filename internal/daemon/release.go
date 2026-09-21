package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

// The console shows which published release exists; installing one stays a
// terminal command. One cached read of the public releases endpoint serves
// every client: a console request never waits for GitHub, an offline or
// rate-limited host keeps the previous answer or none, and a daemon no console
// asks never reaches out at all.
const (
	releaseTagURL   = "https://github.com/dark-factory-build/dark-factory/releases/tag/"
	releaseInterval = 6 * time.Hour
	releaseRetry    = 10 * time.Minute
)

var (
	releaseVersion  = regexp.MustCompile(`^v?[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$`)
	errNoRelease    = errors.New("no published release")
	releaseEndpoint = "https://api.github.com/repos/dark-factory-build/dark-factory/releases/latest"
	releaseClient   = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	releaseMu       sync.Mutex
	releaseValue    api.PublishedRelease
	releaseNext     time.Time
	releasePending  bool
)

func latestPublishedRelease() api.PublishedRelease {
	releaseMu.Lock()
	defer releaseMu.Unlock()
	if !releasePending && !time.Now().Before(releaseNext) {
		releasePending = true
		go refreshPublishedRelease()
	}
	return releaseValue
}

func refreshPublishedRelease() {
	releaseMu.Lock()
	client, endpoint := releaseClient, releaseEndpoint
	releaseMu.Unlock()
	// The client's own timeout bounds the whole exchange, body included.
	value, err := readPublishedRelease(context.Background(), client, endpoint)
	releaseMu.Lock()
	defer releaseMu.Unlock()
	releasePending, releaseNext = false, time.Now().Add(releaseRetry)
	if err == nil {
		releaseValue, releaseNext = value, time.Now().Add(releaseInterval)
	}
}

// The response is untrusted input: only a bounded version tag is taken from it,
// and the link is built from that tag rather than from a supplied URL.
func readPublishedRelease(ctx context.Context, client *http.Client, endpoint string) (api.PublishedRelease, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return api.PublishedRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	response, err := client.Do(request)
	if err != nil {
		return api.PublishedRelease{}, err
	}
	defer response.Body.Close()
	const maxBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return api.PublishedRelease{}, err
	}
	var result struct {
		TagName    string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if response.StatusCode != http.StatusOK || len(body) > maxBytes || json.Unmarshal(body, &result) != nil ||
		result.Draft || result.Prerelease || !releaseVersion.MatchString(result.TagName) {
		return api.PublishedRelease{}, errNoRelease
	}
	return api.PublishedRelease{Version: result.TagName, URL: releaseTagURL + result.TagName}, nil
}
