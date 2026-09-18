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
