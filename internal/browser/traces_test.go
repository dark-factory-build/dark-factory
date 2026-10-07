package browser

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
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

func (backend *traceBackend) ReceiveTraces(body []byte) error {
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
		observations, _, err := opgraph.DecodeOTLP([]byte(backend.received[len(backend.received)-1]), 2_000)
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
