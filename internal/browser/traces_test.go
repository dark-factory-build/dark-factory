package browser

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/opgraph"
	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

type traceBackend struct {
	*fakeBackend
	received []string
}

func (backend *traceBackend) ReceiveTraces(body []byte, remote bool) error {
	backend.received = append(backend.received, string(body))
	return nil
}

// Only local processes may post traces, as JSON or as the protobuf most SDKs
// send by default, gzipped or not; never a page and never a simple form type.
func TestTracesAcceptOnlyLocalOTLP(t *testing.T) {
	backend := &traceBackend{fakeBackend: newFakeBackend()}
	server, _ := startHeldClockServer(t, backend)
	post := func(contentType, encoding, origin string, body []byte) int {
		request, _ := http.NewRequest(http.MethodPost, "http://"+server.Addr()+TracesPath, bytes.NewReader(body))
		request.Header.Set("Content-Type", contentType)
		if encoding != "" {
			request.Header.Set("Content-Encoding", encoding)
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
	if status := post("application/json", "", "", []byte(`{"resourceSpans":[]}`)); status != http.StatusOK || len(backend.received) != 1 {
		t.Fatalf("local JSON export = %d, received %d", status, len(backend.received))
	}
	text := func(value string) *commonv1.AnyValue {
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: value}}
	}
	export, err := proto.Marshal(&tracev1.TracesData{ResourceSpans: []*tracev1.ResourceSpans{{
		Resource: &resourcev1.Resource{Attributes: []*commonv1.KeyValue{{Key: "service.name", Value: text("shop")}}},
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: []*tracev1.Span{{
			Kind: tracev1.Span_SPAN_KIND_SERVER, StartTimeUnixNano: 1_000_000_000, EndTimeUnixNano: 1_050_000_000,
			Attributes: []*commonv1.KeyValue{{Key: "http.route", Value: text("/orders/{id}")}},
		}}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	var zipped bytes.Buffer
	writer := gzip.NewWriter(&zipped)
	_, _ = writer.Write(export)
	_ = writer.Close()
	for _, encoding := range []string{"", "gzip"} {
		body := export
		if encoding == "gzip" {
			body = zipped.Bytes()
		}
		if status := post("application/x-protobuf", encoding, "", body); status != http.StatusOK {
			t.Fatalf("protobuf export (%q) = %d", encoding, status)
		}
		// The receiver gets the OTLP JSON mapping, which folds into one observation of the route.
		observations, _, err := opgraph.DecodeOTLP([]byte(backend.received[len(backend.received)-1]), 2_000, false)
		if err != nil || len(observations) != 1 || observations[0].Attributes["http.route"] != "/orders/{id}" || observations[0].Attributes["service.name"] != "shop" {
			t.Fatalf("protobuf export (%q) folded to %+v, %v", encoding, observations, err)
		}
	}
	if status := post("application/x-protobuf", "", "https://evil.example", export); status != http.StatusForbidden {
		t.Fatalf("page-originated export = %d", status)
	}
	for _, simple := range []string{"text/plain", "application/x-www-form-urlencoded"} {
		if status := post(simple, "", "", export); status != http.StatusUnsupportedMediaType {
			t.Fatalf("%s export = %d", simple, status)
		}
	}
	if status := post("application/x-protobuf", "", "", []byte("not protobuf")); status != http.StatusBadRequest {
		t.Fatalf("malformed protobuf = %d", status)
	}
	if len(backend.received) != 3 {
		t.Fatalf("refused exports reached the backend: %d received", len(backend.received))
	}
}

type agentTelemetryBackend struct {
	*fakeBackend
	received []string
}

func (backend *agentTelemetryBackend) ReceiveAgentTelemetry(path string, body []byte, protobuf bool) error {
	if string(body) == "refuse" {
		return errors.New("malformed")
	}
	backend.received = append(backend.received, fmt.Sprint(path, " ", protobuf, " ", string(body)))
	return nil
}

// Agent metrics and logs take the same local-only path as traces: no page, no
// simple form type, POST only, gzip decoded before the backend sees it.
func TestAgentTelemetryAcceptsOnlyLocalOTLP(t *testing.T) {
	backend := &agentTelemetryBackend{fakeBackend: newFakeBackend()}
	server, _ := startHeldClockServer(t, backend)
	send := func(method, path, contentType, encoding, origin string, body []byte) int {
		request, _ := http.NewRequest(method, "http://"+server.Addr()+path, bytes.NewReader(body))
		request.Header.Set("Content-Type", contentType)
		if encoding != "" {
			request.Header.Set("Content-Encoding", encoding)
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
	var zipped bytes.Buffer
	writer := gzip.NewWriter(&zipped)
	_, _ = writer.Write([]byte("export"))
	_ = writer.Close()
	for _, path := range []string{MetricsPath, LogsPath} {
		backend.received = nil
		if status := send(http.MethodPost, path, "application/json", "", "", []byte("{}")); status != http.StatusOK {
			t.Fatalf("%s JSON = %d", path, status)
		}
		if status := send(http.MethodPost, path, "application/x-protobuf", "gzip", "", zipped.Bytes()); status != http.StatusOK {
			t.Fatalf("%s gzipped protobuf = %d", path, status)
		}
		if want := []string{path + " false {}", path + " true export"}; !slices.Equal(backend.received, want) {
			t.Fatalf("%s received %q, want %q", path, backend.received, want)
		}
		for _, refused := range []struct {
			method, contentType, origin, body string
			status                            int
		}{
			{http.MethodPost, "application/x-protobuf", "https://evil.example", "export", http.StatusForbidden},
			{http.MethodPost, "text/plain", "", "export", http.StatusUnsupportedMediaType},
			{http.MethodGet, "application/json", "", "", http.StatusMethodNotAllowed},
			{http.MethodPost, "application/json", "", "refuse", http.StatusBadRequest},
		} {
			if status := send(refused.method, path, refused.contentType, "", refused.origin, []byte(refused.body)); status != refused.status {
				t.Fatalf("%s %+v = %d", path, refused, status)
			}
		}
		if len(backend.received) != 2 {
			t.Fatalf("%s refused exports reached the backend: %q", path, backend.received)
		}
	}
	// A backend without the capability has no such endpoint.
	plain, _ := startHeldClockServer(t, newFakeBackend())
	request, _ := http.NewRequest(http.MethodPost, "http://"+plain.Addr()+MetricsPath, bytes.NewReader([]byte("{}")))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("metrics without a receiver = %d", response.StatusCode)
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
