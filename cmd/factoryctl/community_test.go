package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPublicBacklogDoesNotUseAmbientProxy(t *testing.T) {
	proxyCalls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxyCalls++ }))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	server := httptest.NewTLSServer(http.HandlerFunc(func(out http.ResponseWriter, request *http.Request) {
		if request.Host != "darkfactory.build" || request.URL.Path != "/api/backlog" || request.Header.Get("Proxy-Authorization") != "" {
			t.Errorf("unexpected public request: %s %s", request.Host, request.URL.Path)
		}
		_, _ = io.WriteString(out, `{"status":"ok","repository":"dark-factory-build/dark-factory","issues":[]}`)
	}))
	defer server.Close()
	client := publicBacklogClient()
	transport := client.Transport.(*http.Transport)
	defer transport.CloseIdleConnections()
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	var out bytes.Buffer
	if err := readPublicBacklog(context.Background(), client, &out); err != nil || proxyCalls != 0 {
		t.Fatalf("public read used ambient proxy or failed: calls=%d error=%v", proxyCalls, err)
	}
}

type publicBacklogTransport func(*http.Request) (*http.Response, error)

func (transport publicBacklogTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestPublicBacklogFixedReadAndFailures(t *testing.T) {
	for _, fixture := range []struct {
		body   string
		status int
		valid  bool
	}{
		{`{"status":"ok","repository":"dark-factory-build/dark-factory","issues":[{"title":"hostile\u001b[31m","thumbs_up":3}],"truncated":true}`, 200, true},
		{`{"status":"unavailable"}`, 503, false},
		{`{"status":"ok","repository":"private/other"}`, 200, false},
		{`{"status":"ok","repository":"dark-factory-build/dark-factory"} trailing`, 200, false},
		{strings.Repeat("x", (1<<20)+1), 200, false},
	} {
		client := &http.Client{Transport: publicBacklogTransport(func(request *http.Request) (*http.Response, error) {
			if request.URL.String() != publicBacklogURL || request.Method != http.MethodGet || request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
				t.Fatalf("unexpected credentialled or configurable read: %v", request)
			}
			return &http.Response{StatusCode: fixture.status, Body: io.NopCloser(strings.NewReader(fixture.body))}, nil
		})}
		var out bytes.Buffer
		err := readPublicBacklog(context.Background(), client, &out)
		if (err == nil) != fixture.valid || !fixture.valid && out.Len() != 0 || strings.ContainsRune(out.String(), '\x1b') {
			t.Fatalf("backlog validity=%v error=%v output bytes=%d", fixture.valid, err, out.Len())
		}
	}
}

func TestFeedbackWithoutFactoryOrCredentials(t *testing.T) {
	for _, kind := range []string{"bug", "feature"} {
		var out, diagnostic bytes.Buffer
		var opened string
		code := runWithOpener(context.Background(), []string{"feedback", kind, "--open", "--agent-assisted", "--factory-name", "Workshop & friends"}, func(string) string {
			t.Fatal("feedback read environment/private factory state")
			return ""
		}, &out, &diagnostic, func(_ context.Context, link string) error { opened = link; return nil })
		link, err := url.Parse(strings.TrimSpace(out.String()))
		if code != 0 || err != nil || link.Scheme != "https" || link.Host != "darkfactory.build" || link.Path != "/feedback" || opened != link.String() {
			t.Fatalf("report route: code=%d link=%q error=%v", code, out.String(), err)
		}
		query := link.Query()
		if query.Get("kind") != kind || query.Get("agent_assisted") != "true" || query.Get("factory_name") != "Workshop & friends" || query.Get("version") == "" || query.Get("source") == "" || query.Get("target") == "" || len(query) != 6 || len(opened) > 2048 {
			t.Fatalf("unexpected public fields: %v", query)
		}
		if !strings.Contains(diagnostic.String(), "No issue has been submitted") {
			t.Fatal("must distinguish opening from submission")
		}
	}
	for _, args := range [][]string{{}, {"unknown"}, {"bug", "--factory-name", "name\nsecret"}, {"bug", "--factory-name", strings.Repeat("x", 129)}, {"bug", "--labels", "ready"}, {"bug", "extra"}} {
		var out, diagnostic bytes.Buffer
		if code := runFeedback(context.Background(), args, &out, &diagnostic, func(context.Context, string) error { t.Fatal("opened invalid report"); return nil }); code != exitUsage || out.Len() != 0 {
			t.Fatalf("invalid report %q: code=%d out=%q", args, code, out.String())
		}
	}
	var out, diagnostic bytes.Buffer
	if code := runFeedback(context.Background(), []string{"bug", "--open"}, &out, &diagnostic, func(context.Context, string) error { return errors.New("browser failed") }); code != exitFailure || !strings.HasPrefix(out.String(), "https://darkfactory.build/feedback?") {
		t.Fatal("opening failure must retain printable fallback")
	}
}
