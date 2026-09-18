package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
)

func TestGitHubRejectsInvalidActionsBeforeCredentials(t *testing.T) {
	for _, args := range [][]string{{}, {"unknown"}, {"confirm", "bad"}, {"repositories"}, {"manage"}, {"installations", "--page", "0"}, {"delegate"}, {"disconnect", "extra"}, {"status", "--token", "secret"}} {
		var out, diagnostic bytes.Buffer
		code := runGitHub(context.Background(), args, func(string) string { t.Fatal("invalid request read credentials"); return "" }, &out, &diagnostic, nil)
		if code != exitUsage || out.Len() != 0 {
			t.Fatalf("invalid action %q returned %d", args, code)
		}
	}
}

func TestGitHubCLIUsesOperatorConnection(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.GitHubConnectionInput()
		if !ok || input.Action != "connect" {
			return mustWebErrorReply(t, api.RemoteInvalidRequest)
		}
		return api.NewContentReply(api.GitHubConnectionResult{State: "ok", Authorization: &maintainer.Authorization{ConnectionID: strings.Repeat("a", 64), URL: "https://github.com/login/oauth/authorize?state=fixture", ExpiresAt: 1900000000}})
	})
	var out, diagnostic bytes.Buffer
	var opened string
	exit := runWithOpener(context.Background(), []string{"github", "connect", "--open"}, webEnvironment(fixture), &out, &diagnostic, func(_ context.Context, link string) error { opened = link; return nil })
	if result := awaitServer(t, done); result.err != nil {
		t.Fatal(result.err)
	}
	if exit != 0 || opened != "https://github.com/login/oauth/authorize?state=fixture" || !strings.Contains(diagnostic.String(), "confirm CODE") || strings.Contains(out.String(), "credential") {
		t.Fatal("CLI failed scoped connect flow")
	}
}

func TestGitHubAttemptCannotBorrowOperatorCredentials(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	operatorEnvironment := webEnvironment(fixture)
	var out, diagnostic bytes.Buffer
	exit := runWithOpener(context.Background(), []string{"github", "disconnect"}, func(name string) string {
		if name == "DARK_FACTORY_ATTEMPT_TOKEN_FILE" {
			return "/private/worker/attempt.token"
		}
		if operatorEnvironment(name) != "" {
			t.Fatal("attempt session read operator credential configuration")
		}
		return operatorEnvironment(name)
	}, &out, &diagnostic, nil)
	if exit != exitFailure || out.Len() != 0 || !strings.Contains(diagnostic.String(), "operator session") {
		t.Fatal("attempt session reached operator GitHub settings")
	}
}
