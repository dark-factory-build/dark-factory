//go:build darwin || linux

package main

import "testing"

func TestContentCommandParsing(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	command, help, ok := parse([]string{"attempt", "content", "deprecate", "--project", id, "--id", id, "--revision", "2", "--author", "operator"})
	if !ok || help || command.kind != commandContentDeprecate || command.contentRevision != 2 {
		t.Fatalf("content deprecate parse = %+v, help=%t, ok=%t", command, help, ok)
	}
	_, _, ok = parse([]string{"attempt", "content", "create", "--project", id, "--kind", "procedure", "--title", "safe", "--author", "operator", "--body", "x", "--body-file", "-"})
	if ok {
		t.Fatal("accepted mutually exclusive body inputs")
	}
}
