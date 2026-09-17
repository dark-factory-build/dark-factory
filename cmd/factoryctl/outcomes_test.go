package main

import (
	"testing"
)

func TestOutcomeCommandParsingAndDocumentBounds(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	command, help, ok := parse([]string{"outcome", "write", "--id", id, "--project", id, "--revision", "2", "--document", `{"kind":"outcome","state":"open"}`})
	if !ok || help || command.kind != commandOutcomeWrite || command.contentRevision != 2 {
		t.Fatalf("write parse = %+v, help=%t, ok=%t", command, help, ok)
	}
	command, help, ok = parse([]string{"attempt", "outcome", "read", "--project", id, "--id", id})
	if !ok || help || command.kind != commandOutcomeRead {
		t.Fatalf("attempt read parse = %+v, help=%t, ok=%t", command, help, ok)
	}
	if _, _, ok := parse([]string{"outcome", "write", "--id", id, "--project", id, "--document", "{}", "--document-file", "-"}); ok {
		t.Fatal("document and document-file accepted together")
	}
}
