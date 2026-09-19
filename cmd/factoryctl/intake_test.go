//go:build darwin

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func TestIntakeControllerCommandsUseOperatorTransport(t *testing.T) {
	for _, args := range [][]string{{"intake", "config"}, {"intake", "tick", "--source", strings.Repeat("ab", 16), "--page", "1", "--acceptance-cursor", strings.Repeat("cd", 16)}} {
		t.Run(args[1], func(t *testing.T) {
			fixture := newAPIFixture(t)
			defer fixture.close(t)
			done := serveOne(fixture.listener, func(call api.Call) api.Reply {
				input, ok := call.IntakeInput()
				if !ok || !api.ValidIntakeInput(input) {
					t.Error("missing intake operator call")
				}
				return api.NewContentReply(api.IntakeResult{State: "ok", Sources: []api.IntakeSource{}})
			})
			var stdout, stderr bytes.Buffer
			exit := run(context.Background(), args, webEnvironment(fixture), &stdout, &stderr)
			result := awaitServer(t, done)
			if exit != 0 || result.err != nil {
				t.Fatalf("intake command exit=%d server=%v stderr=%s", exit, result.err, stderr.String())
			}
		})
	}
}

func TestIntakeCreateUsesNamedConfigurationAndMintsSourceID(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	project, target, overseer := strings.Repeat("ab", 16), strings.Repeat("cd", 16), strings.Repeat("ef", 16)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		input, ok := call.IntakeInput()
		if !ok || input.Action != "create" || input.SourceID == "" || input.ProjectID != project || input.Configuration == nil {
			t.Errorf("intake create = %+v", input)
		} else if got := input.Configuration; got.Repository != "team/source" || got.TargetRepositoryID != target || got.OverseerAgentID != overseer || got.Policy != "trusted_authors" || len(got.TrustedAuthors) != 1 || got.TrustedAuthors[0] != "octocat" || got.PollSeconds != 60 || got.AdmissionLimit != 25 {
			t.Errorf("named configuration = %+v", got)
		}
		return api.NewContentReply(api.IntakeResult{State: "ok"})
	})
	var stdout, stderr bytes.Buffer
	args := []string{"intake", "create", "--project", project, "--repository", "team/source", "--target-repository", target, "--overseer", overseer, "--policy", "trusted-authors", "--trusted-author", "octocat"}
	if exit := run(context.Background(), args, webEnvironment(fixture), &stdout, &stderr); exit != 0 || stderr.Len() != 0 {
		t.Fatalf("intake create exit=%d stderr=%s", exit, stderr.String())
	}
	if result := awaitServer(t, done); result.err != nil {
		t.Fatal(result.err)
	}
}

func TestIntakeNamedConfigurationRejectsUnsafePolicy(t *testing.T) {
	id := strings.Repeat("ab", 16)
	for _, args := range [][]string{
		{"intake", "create", "--project", id, "--repository", "team/source", "--target-repository", id, "--policy", "trusted-authors"},
		{"intake", "create", "--project", id, "--repository", "team/source", "--target-repository", id, "--poll-seconds", "4"},
		{"intake", "create", "--project", id, "--repository", "team/source", "--target-repository", id, "--trusted-author", "octocat", "--trusted-author", "OCTOCAT"},
	} {
		if _, _, ok := parse(args); ok {
			t.Fatalf("unsafe intake configuration accepted: %v", args)
		}
	}
}

func TestIntakeControllerWaitsBeyondAttemptDeadline(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		connection, err := fixture.listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer connection.Close()
		_, err = connection.Receive(ctx)
		if err == nil {
			err = connection.RefreshDeadline(ctx)
		}
		if err == nil {
			reply, dispatchErr := connection.Dispatch(func(api.Call) api.Reply {
				time.Sleep(6 * time.Second) // Reproduces the old five-second CLI deadline.
				return api.NewContentReply(api.IntakeResult{State: "ok", AcceptanceProgress: true, AcceptanceCursor: strings.Repeat("cd", 16)})
			})
			err = dispatchErr
			if err == nil {
				err = connection.Respond(reply)
			}
		}
		done <- err
	}()
	var stdout, stderr bytes.Buffer
	exit := run(t.Context(), []string{"intake", "tick", "--source", strings.Repeat("ab", 16), "--page", "1"}, webEnvironment(fixture), &stdout, &stderr)
	if err := <-done; err != nil || exit != 0 || !strings.Contains(stdout.String(), strings.Repeat("cd", 16)) {
		t.Fatalf("slow intake: exit=%d err=%v stderr=%s", exit, err, stderr.String())
	}
}
