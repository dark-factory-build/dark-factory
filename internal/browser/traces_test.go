package browser

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

type traceBackend struct {
	*fakeBackend
	received []string
}

func (backend *traceBackend) ReceiveTraces(body []byte) error {
	backend.received = append(backend.received, string(body))
	return nil
}

// Only local processes may post traces: never a page, never protobuf.
func TestTracesAcceptOnlyLocalJSON(t *testing.T) {
	backend := &traceBackend{fakeBackend: newFakeBackend()}
	server, _ := startHeldClockServer(t, backend)
	post := func(contentType, origin string) int {
		request, _ := http.NewRequest(http.MethodPost, "http://"+server.Addr()+TracesPath, strings.NewReader(`{"resourceSpans":[]}`))
		request.Header.Set("Content-Type", contentType)
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if status := post("application/json", ""); status != http.StatusOK || len(backend.received) != 1 {
		t.Fatalf("local JSON export = %d, received %d", status, len(backend.received))
	}
	if status := post("application/json", "https://evil.example"); status != http.StatusForbidden {
		t.Fatalf("page-originated export = %d", status)
	}
	if status := post("application/x-protobuf", ""); status != http.StatusUnsupportedMediaType {
		t.Fatalf("protobuf export = %d", status)
	}
	if len(backend.received) != 1 {
		t.Fatal("a refused export reached the backend")
	}
}

type publicBackend struct{ *fakeBackend }

func (publicBackend) PublicWorld(_ context.Context, projectID string) ([]byte, error) {
	return []byte(`{"project":"` + projectID + `"}`), nil
}

// The public projection is for local readers only: no page, no rebound host.
func TestPublicWorldAnswersOnlyTheListenerItself(t *testing.T) {
	server, _ := startHeldClockServer(t, publicBackend{newFakeBackend()})
	get := func(host, origin string) int {
		request, _ := http.NewRequest(http.MethodGet, "http://"+server.Addr()+PublicPath+"p1", nil)
		if host != "" {
			request.Host = host
		}
		if origin != "" {
			request.Header.Set("Origin", origin)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		return response.StatusCode
	}
	if status := get("", ""); status != http.StatusOK {
		t.Fatalf("local read = %d", status)
	}
	if status := get("rebound.example:43123", ""); status != http.StatusNotFound {
		t.Fatalf("rebound host = %d", status)
	}
	if status := get("", "https://evil.example"); status != http.StatusNotFound {
		t.Fatalf("page read = %d", status)
	}
}
