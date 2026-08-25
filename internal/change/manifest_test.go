package change

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"
)

func TestManifestCommitmentV1GoldenAndImmutable(t *testing.T) {
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{0x42}, format.OIDLength()))
	a := mustEntry(t, format, []byte("a.txt"), "100644", []byte("alpha"))
	b := mustEntry(t, format, []byte("bin/run"), "100755", []byte("#!/bin/sh\n"))
	manifest := mustManifest(t, format, base, []Entry{b, a})

	independent := make([]byte, 0)
	independent = append(independent, []byte("dark-factory/change-manifest")...)
	independent = append(independent, 0, 1, 1)
	independent = append(independent, bytes.Repeat([]byte{0x42}, 20)...)
	independent = binary.BigEndian.AppendUint64(independent, 2)
	independent = binary.BigEndian.AppendUint64(independent, uint64(len("alpha")+len("#!/bin/sh\n")))
	for _, entry := range []struct {
		path []byte
		mode string
		data []byte
	}{{[]byte("a.txt"), "100644", []byte("alpha")}, {[]byte("bin/run"), "100755", []byte("#!/bin/sh\n")}} {
		independent = binary.BigEndian.AppendUint32(independent, uint32(len(entry.path)))
		independent = append(independent, entry.path...)
		independent = append(independent, entry.mode...)
		independent = binary.BigEndian.AppendUint64(independent, uint64(len(entry.data)))
		independent = append(independent, independentBlobOID(t, "sha1", entry.data)...)
	}
	if !bytes.Equal(manifest.canonicalBytes(), independent) {
		t.Fatalf("canonical bytes differ\n got: %x\nwant: %x", manifest.canonicalBytes(), independent)
	}
	wantDigest := "1295413e1a6f90ddaf6802b923bc175cd67865f103604488adbc7fd78e62942d"
	if got := manifest.Commitment().Hex(); got != wantDigest {
		t.Fatalf("golden commitment changed: got %s want %s", got, wantDigest)
	}

	pathCopy := a.Path()
	pathCopy[0] = 'z'
	entriesCopy := manifest.Entries()
	entriesCopy[0].path[0] = 'z'
	if got := string(manifest.Entries()[0].Path()); got != "a.txt" {
		t.Fatalf("manifest mutated through accessor: %q", got)
	}
	if !manifest.Commitment().Equal(mustManifest(t, format, base, []Entry{a, b}).Commitment()) {
		t.Fatal("entry permutation changed canonical commitment")
	}
}

func TestManifestCommitmentBindsEveryAuthorityField(t *testing.T) {
	sha1Format := mustFormat(t, "sha1")
	base := mustID(t, sha1Format, bytes.Repeat([]byte{1}, 20))
	entry := mustEntry(t, sha1Format, []byte("raw/name"), "100644", []byte("payload"))
	original := mustManifest(t, sha1Format, base, []Entry{entry}).Commitment()

	mutations := map[string]Manifest{
		"base":         mustManifest(t, sha1Format, mustID(t, sha1Format, bytes.Repeat([]byte{2}, 20)), []Entry{entry}),
		"path":         mustManifest(t, sha1Format, base, []Entry{mustEntry(t, sha1Format, []byte("raw/other"), "100644", []byte("payload"))}),
		"mode":         mustManifest(t, sha1Format, base, []Entry{mustEntry(t, sha1Format, []byte("raw/name"), "100755", []byte("payload"))}),
		"size and oid": mustManifest(t, sha1Format, base, []Entry{mustEntry(t, sha1Format, []byte("raw/name"), "100644", []byte("payload!"))}),
		"file count":   mustManifest(t, sha1Format, base, nil),
	}
	sha256Format := mustFormat(t, "sha256")
	mutations["object format"] = mustManifest(t, sha256Format, mustID(t, sha256Format, bytes.Repeat([]byte{1}, 32)), []Entry{
		mustEntry(t, sha256Format, []byte("raw/name"), "100644", []byte("payload")),
	})
	for name, mutation := range mutations {
		t.Run(name, func(t *testing.T) {
			if original.Equal(mutation.Commitment()) {
				t.Fatalf("%s mutation did not change commitment", name)
			}
		})
	}
}

func TestManifestSortsByRawPathBytes(t *testing.T) {
	format := mustFormat(t, "sha1")
	base := mustID(t, format, bytes.Repeat([]byte{7}, 20))
	upper := mustEntry(t, format, []byte("Z"), "100644", []byte("upper"))
	lower := mustEntry(t, format, []byte("a"), "100644", []byte("lower"))
	manifest := mustManifest(t, format, base, []Entry{lower, upper})
	paths := manifest.Entries()
	if string(paths[0].Path()) != "Z" || string(paths[1].Path()) != "a" {
		t.Fatalf("canonical order = %q, %q; want raw-byte Z, a", paths[0].Path(), paths[1].Path())
	}
}

func TestManifestRejectsUnsafeOrUnboundedInput(t *testing.T) {
	format := mustFormat(t, "sha1")
	oid := mustID(t, format, make([]byte, 20))
	unsafePaths := [][]byte{
		nil, {}, []byte("/absolute"), []byte("."), []byte(".."), []byte("a/./b"), []byte("a/../b"),
		[]byte("a//b"), []byte("a/"), []byte("a\x00b"), []byte(".git/config"), []byte("A/.GiT/config"),
		[]byte{'r', 'a', 'w', '/', 0xfe},
		bytes.Repeat([]byte{'p'}, maxNameBytes+1), bytes.Repeat([]byte{'p'}, maxPathBytes+1),
	}
	for _, path := range unsafePaths {
		if _, err := NewEntry(path, "100644", 0, oid); err == nil {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
	for _, mode := range []string{"", "100600", "120000", "160000"} {
		if _, err := NewEntry([]byte("safe"), mode, 0, oid); err == nil {
			t.Fatalf("unsafe mode accepted: %q", mode)
		}
	}
	if _, err := NewEntry([]byte("safe"), "100644", maxBlobBytes+1, oid); err == nil {
		t.Fatal("oversized blob accepted")
	}
	if _, err := NewObjectID(format, make([]byte, 19)); err == nil {
		t.Fatal("wrong SHA-1 OID length accepted")
	}
	if _, err := NewObjectFormat("SHA1"); err == nil {
		t.Fatal("noncanonical object format accepted")
	}

	entry := mustEntry(t, format, []byte("a"), "100644", nil)
	base := mustID(t, format, bytes.Repeat([]byte{3}, 20))
	if _, err := NewManifest(format, base, []Entry{entry, entry}); err == nil {
		t.Fatal("duplicate path accepted")
	}
	child := mustEntry(t, format, []byte("a/b"), "100644", nil)
	if _, err := NewManifest(format, base, []Entry{entry, child}); err == nil {
		t.Fatal("file/directory prefix collision accepted")
	}
	if _, err := NewManifest(format, base, make([]Entry, maxFileCount+1)); err == nil {
		t.Fatal("file-count overflow accepted")
	}
	large := make([]Entry, 5)
	for i := range large {
		large[i], _ = NewEntry([]byte(fmt.Sprintf("large-%d", i)), "100644", maxBlobBytes, oid)
	}
	if _, err := NewManifest(format, base, large); err == nil {
		t.Fatal("total-byte overflow accepted")
	}

	// Valid UTF-8 bytes remain authoritative and are not case- or
	// normalization-folded. Darwin itself rejects invalid UTF-8 with EILSEQ.
	composed := []byte("raw/é")
	decomposed := []byte("raw/e\u0301")
	if bytes.Equal(composed, decomposed) {
		t.Fatal("normalization fixture collapsed")
	}
	if _, err := NewEntry(composed, "100644", 0, oid); err != nil {
		t.Fatalf("valid raw UTF-8 rejected: %v", err)
	}
	if _, err := NewEntry(decomposed, "100644", 0, oid); err != nil {
		t.Fatalf("valid decomposed UTF-8 rejected: %v", err)
	}
}

func mustFormat(t testing.TB, name string) ObjectFormat {
	t.Helper()
	format, err := NewObjectFormat(name)
	if err != nil {
		t.Fatal(err)
	}
	return format
}

func mustID(t testing.TB, format ObjectFormat, raw []byte) ObjectID {
	t.Helper()
	id, err := NewObjectID(format, raw)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustEntry(t testing.TB, format ObjectFormat, path []byte, mode string, data []byte) Entry {
	t.Helper()
	return mustEntryWithID(t, path, mode, uint64(len(data)), hashBlob(format, data))
}

func mustEntryWithID(t testing.TB, path []byte, mode string, size uint64, oid ObjectID) Entry {
	t.Helper()
	entry, err := NewEntry(path, mode, size, oid)
	if err != nil {
		t.Fatal(err)
	}
	return entry
}

func mustManifest(t testing.TB, format ObjectFormat, base ObjectID, entries []Entry) Manifest {
	t.Helper()
	manifest, err := NewManifest(format, base, entries)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func independentBlobOID(t testing.TB, format string, data []byte) []byte {
	t.Helper()
	contents := append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...)
	if format == "sha256" {
		sum := sha256.Sum256(contents)
		return sum[:]
	}
	sum := sha1.Sum(contents)
	return sum[:]
}
