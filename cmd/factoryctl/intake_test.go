//go:build darwin

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func TestIntakeControllerCommandsUseOperatorTransport(t *testing.T) {
	for _, args := range [][]string{{"intake", "config"}, {"intake", "tick", "--source", strings.Repeat("ab", 16), "--page", "1"}} {
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
