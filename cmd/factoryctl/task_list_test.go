package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func TestParseTaskListScopes(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		name string
		args []string
		ok   bool
	}{
		{"agent", []string{"task", "list", "--agent", id}, true},
		{"project", []string{"task", "list", "--project", id}, true},
		{"both", []string{"task", "list", "--agent", id, "--project", id}, false},
		{"neither", []string{"task", "list"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			command, _, ok := parseOperator(test.args)
			if ok != test.ok {
				t.Fatalf("parseOperator(%v) ok=%v, want %v (%+v)", test.args, ok, test.ok, command)
			}
		})
	}
}

func TestTaskListScopesUseOperatorTransport(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		name  string
		scope []string
		want  map[string]any
	}{
		{"agent", []string{"--agent", id}, map[string]any{"agent_id": id}},
		{"project with cursor", []string{"--project", id, "--cursor", "123:" + id}, map[string]any{"project_id": id, "before_updated_at_ms": float64(123), "before_task_id": id}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAPIFixture(t)
			defer fixture.close(t)
			done := serveOne(fixture.listener, func(call api.Call) api.Reply {
				input, ok := call.ProjectContentInput()
				if !ok || input.Operation != "task_list" {
					t.Errorf("operator call = %+v", input)
					return api.NewContentReply(map[string]any{})
				}
				var got map[string]any
				if err := json.Unmarshal(input.Input, &got); err != nil {
					t.Errorf("task list input: %v", err)
				}
				for key, want := range test.want {
					if got[key] != want {
						t.Errorf("input[%q] = %v, want %v", key, got[key], want)
					}
				}
				return api.NewContentReply(map[string]any{"tasks": []any{}})
			})
			var stdout, stderr bytes.Buffer
			args := append([]string{"task", "list"}, test.scope...)
			if exit := run(context.Background(), args, webEnvironment(fixture), &stdout, &stderr); exit != 0 || stderr.Len() != 0 {
				t.Fatalf("task list exit=%d stderr=%q", exit, stderr.String())
			}
			if result := awaitServer(t, done); result.err != nil {
				t.Fatal(result.err)
			}
		})
	}
}
