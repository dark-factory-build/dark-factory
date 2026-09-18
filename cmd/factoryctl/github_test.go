package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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

func TestGitHubCLIPrintsVerifiedNativeInstallURL(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.GitHubConnectionInput()
		if !ok || input.Action != "installations" || input.Page != 1 {
			return mustWebErrorReply(t, api.RemoteInvalidRequest)
		}
		return api.NewContentReply(api.GitHubConnectionResult{State: "ok", Installations: &maintainer.Installations{InstallationURL: "https://github.com/apps/factory-maintainer/installations/new"}})
	})
	var out, diagnostic bytes.Buffer
	var opened string
	exit := runWithOpener(context.Background(), []string{"github", "install", "--open"}, webEnvironment(fixture), &out, &diagnostic, func(_ context.Context, link string) error { opened = link; return nil })
	if result := awaitServer(t, done); result.err != nil {
		t.Fatal(result.err)
	}
	if exit != 0 || strings.TrimSpace(out.String()) != "https://github.com/apps/factory-maintainer/installations/new" || opened != strings.TrimSpace(out.String()) || diagnostic.Len() != 0 {
		t.Fatalf("native install URL was not surfaced safely: exit=%d out=%q diagnostic=%q opened=%q", exit, out.String(), diagnostic.String(), opened)
	}
}

func TestGitHubCLIWalksInstallationPagesBeforeOpeningNativeInstallURL(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	done := make(chan serverResult, 1)
	go func() {
		var result serverResult
		for page := 1; page <= 2; page++ {
			connection, err := fixture.listener.Accept()
			if err != nil {
				result.err = err
				done <- result
				return
			}
			var call api.Call
			var handlerErr error
			var reply api.Reply
			if page == 1 {
				reply = api.NewContentReply(api.GitHubConnectionResult{State: "ok", Installations: &maintainer.Installations{NextPage: func() *int { value := 2; return &value }()}})
			} else {
				reply = api.NewContentReply(api.GitHubConnectionResult{State: "ok", Installations: &maintainer.Installations{InstallationURL: "https://github.com/apps/factory-maintainer/installations/new"}})
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			call, err = connection.Receive(ctx)
			if err == nil {
				reply, err = connection.Dispatch(func(value api.Call) api.Reply {
					input, ok := value.GitHubConnectionInput()
					if !ok || input.Action != "installations" || input.Page != page {
						handlerErr = errors.New("unexpected installation page")
					}
					return reply
				})
			}
			if err == nil && handlerErr != nil {
				err = handlerErr
			}
			if err == nil {
				err = connection.Respond(reply)
			}
			receiveErr := err
			if receiveErr == nil {
				receiveErr = connection.AwaitOutcomeReceipt(ctx)
			}
			cancel()
			connection.Close()
			if receiveErr != nil {
				result.call, result.err = call, receiveErr
				done <- result
				return
			}
		}
		done <- result
	}()
	var out, diagnostic bytes.Buffer
	exit := runWithOpener(context.Background(), []string{"github", "install"}, webEnvironment(fixture), &out, &diagnostic, func(context.Context, string) error { return errors.New("must not open when no --open") })
	if result := awaitServer(t, done); result.err != nil {
		t.Fatal(result.err)
	}
	if exit != 0 || strings.TrimSpace(out.String()) != "https://github.com/apps/factory-maintainer/installations/new" || diagnostic.Len() != 0 {
		t.Fatalf("paged native install lookup failed: exit=%d out=%q diagnostic=%q", exit, out.String(), diagnostic.String())
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
