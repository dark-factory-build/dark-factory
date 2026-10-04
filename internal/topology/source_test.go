package topology

import (
	"archive/tar"
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestArchiveHasImmutableRevisionExactInventoryAndRejectsEscapes(t *testing.T) {
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for name, body := range map[string]string{"go.mod": "module example.com/fixture\n", "move/move.go": "package move\n", "move/move_test.go": "package move\n", "node_modules/large.js": "ignored"} {
		if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		writer.Write([]byte(body))
	}
	writer.Close()
	first, err := BuildArchive(context.Background(), archive.Bytes(), "project", strings.Repeat("a", 40))
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildArchive(context.Background(), archive.Bytes(), "project", strings.Repeat("b", 40))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Files) != 3 || first.SourceRevision == second.SourceRevision || first.Digest == second.Digest {
		t.Fatalf("revision/files = %+v", first)
	}
	for i, node := range first.Nodes {
		if node.ID != second.Nodes[i].ID {
			t.Fatal("revision changed entity identity")
		}
	}
	owned := map[string]int{}
	for _, file := range first.Files {
		owner, ok := NodeForPath(first, file.Path)
		if !ok {
			t.Fatal("unowned file")
		}
		owned[owner.ID]++
	}
	count := 0
	for _, n := range owned {
		count += n
	}
	if count != 3 {
		t.Fatal("double-counted files")
	}
	var escaped bytes.Buffer
	writer = tar.NewWriter(&escaped)
	writer.WriteHeader(&tar.Header{Name: "../escape", Mode: 0600, Size: 0})
	writer.Close()
	if _, err := BuildArchive(context.Background(), escaped.Bytes(), "project", ""); err == nil {
		t.Fatal("archive escaped root")
	}
}
