//go:build darwin || linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestContentCommandParsing(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	command, help, ok := parse([]string{"attempt", "content", "deprecate", "--project", id, "--id", id, "--revision", "2"})
	if !ok || help || command.kind != commandContentDeprecate || command.contentRevision != 2 {
		t.Fatalf("content deprecate parse = %+v, help=%t, ok=%t", command, help, ok)
	}
	_, _, ok = parse([]string{"attempt", "content", "create", "--project", id, "--kind", "procedure", "--title", "safe", "--body", "x", "--body-file", "-"})
	if ok {
		t.Fatal("accepted mutually exclusive body inputs")
	}
	command, help, ok = parse([]string{"content", "read", "--id", id, "--revision", "2"})
	if !ok || help || command.kind != commandContentRead || command.project != "" {
		t.Fatalf("operator metadata read parse = %+v, help=%t, ok=%t", command, help, ok)
	}
	command, help, ok = parse([]string{"attempt", "content", "evidence-list", "--id", id, "--revision", "2", "--limit", "4"})
	if !ok || help || command.kind != commandContentEvidenceList {
		t.Fatalf("attempt evidence list parse = %+v, help=%t, ok=%t", command, help, ok)
	}
	command, help, ok = parse([]string{"content", "attachments", "--project", id, "--task", id, "--revision", "3"})
	if !ok || help || command.kind != commandContentAttachments {
		t.Fatalf("operator attachments parse = %+v, help=%t, ok=%t", command, help, ok)
	}
	command, help, ok = parse([]string{"content", "list", "--project", id})
	if !ok || help || command.head != 0 {
		t.Fatalf("content list discovery defaults = %+v, help=%t, ok=%t", command, help, ok)
	}
	file := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(file, []byte("file body"), 0o600); err != nil {
		t.Fatal(err)
	}
	command, help, ok = parse([]string{"content", "create", "--project", id, "--kind", "procedure", "--title", "safe", "--body-file", file})
	if !ok || help {
		t.Fatalf("body file parse = %+v, help=%t, ok=%t", command, help, ok)
	}
	body, err := contentBody(command)
	if err != nil || body != "file body" {
		t.Fatalf("body file read = %q, %v", body, err)
	}
	oldStdin := os.Stdin
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = reader
	t.Cleanup(func() { os.Stdin = oldStdin })
	if _, err := writer.WriteString("stdin body"); err != nil {
		t.Fatal(err)
	}
	_ = writer.Close()
	command.bodyFile = "-"
	body, err = contentBody(command)
	if err != nil || body != "stdin body" {
		t.Fatalf("stdin body read = %q, %v", body, err)
	}
}

func TestContentCallerBoundsAndDerivedProvenance(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	for _, argv := range [][]string{
		{"attempt", "content", "body", "--id", id, "--revision", "1", "--limit", "65537"},
		{"attempt", "content", "evidence", "--project", id, "--id", id, "--revision", "1", "--tested-source", "source", "--result", "passed", "--evaluator", "operator"},
		{"attempt", "content", "create", "--project", id, "--kind", "procedure", "--title", "safe", "--body", strings.Repeat("x", 1<<20+1)},
	} {
		if _, _, ok := parse(argv); ok {
			t.Fatal("accepted out-of-bounds input or caller-supplied provenance")
		}
	}
}
