package main

import "testing"

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
