//go:build darwin || linux

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProductionVerifyParsingAndMetaTag(t *testing.T) {
	sha := strings.Repeat("a", 40)
	command, help, ok := parse([]string{"production", "verify", "--url", "https://example.test/", "--url", "https://example.test/healthz", "--meta-tag", "build-revision", sha})
	if !ok || help || command.kind != commandProductionVerify || len(command.verificationURLs) != 2 || command.metaTag != "build-revision" || command.expectedSHA != sha {
		t.Fatalf("production verify parse = %+v, help=%v, ok=%v", command, help, ok)
	}
	if got := sourceCommitMeta(`<meta content='`+sha+`' name="build-revision" />`, "build-revision"); got != sha {
		t.Fatalf("source commit meta = %q", got)
	}
}

func TestProductionVerifyHTTPFixturesSeparateSourceAndHealth(t *testing.T) {
	sha := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/source":
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write([]byte(`<meta name="build-revision" content="` + sha + `">`))
		case "/wrong":
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write([]byte(`<meta name="build-revision" content="` + strings.Repeat("b", 40) + `">`))
		case "/health":
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write([]byte(`<meta name="build-revision" content="` + sha + `">`))
		case "/bad":
			response.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer server.Close()
	client := server.Client()
	observed, _, err := verifySourceURL(context.Background(), client, server.URL+"/source", "build-revision")
	if err != nil || observed != sha {
		t.Fatalf("source verification = %q, %v", observed, err)
	}
	observed, _, err = verifySourceURL(context.Background(), client, server.URL+"/health", "build-revision")
	if err != nil || observed != sha {
		t.Fatalf("health URL verification = %q, %v", observed, err)
	}
	if _, _, err := verifySourceURL(context.Background(), client, server.URL+"/missing", "build-revision"); err == nil {
		t.Fatal("missing source meta tag was accepted")
	}
	wrong, _, err := verifySourceURL(context.Background(), client, server.URL+"/source", "other-revision")
	if err == nil || wrong != "" {
		t.Fatalf("wrong source meta tag = %q, %v", wrong, err)
	}
	wrong, _, err = verifySourceURL(context.Background(), client, server.URL+"/wrong", "build-revision")
	if err != nil || wrong == sha {
		t.Fatalf("wrong source SHA = %q, %v", wrong, err)
	}
	if _, _, err := verifySourceURL(context.Background(), client, server.URL+"/bad", "build-revision"); err == nil {
		t.Fatal("unhealthy URL was accepted")
	}
}

func TestProductionVerifyIgnoresSpoofedMetaText(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, body := range []string{
		`<!-- <meta name="build-revision" content="` + sha + `"> -->`,
		`<script>const template = '<meta name="build-revision" content="` + sha + `">';</script>`,
	} {
		if got := sourceCommitMeta(body, "build-revision"); got != "" {
			t.Fatalf("spoof was accepted: %q", got)
		}
	}
}

func TestProductionVerifyRejectsUnconfiguredOrCredentialedURLs(t *testing.T) {
	for _, args := range [][]string{
		{"production", "verify", "deadbeef"},
		{"production", "verify", "--url", "http://example.test", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"production", "verify", "--url", "https://user@example.test", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	} {
		if _, _, ok := parse(args); ok {
			t.Fatalf("accepted invalid production verify args: %v", args)
		}
	}
}
