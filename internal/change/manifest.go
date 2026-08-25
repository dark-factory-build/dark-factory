// Package change commits to and materializes exact, repository-free source trees.
package change

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maxFileCount  = 10_000
	maxPathBytes  = 4_096
	maxNameBytes  = 255
	maxBlobBytes  = 256 << 20
	maxTotalBytes = 1 << 30
)

var commitmentPrefix = []byte("dark-factory/change-manifest\x00\x01")

// ObjectFormat is one supported Git object format.
type ObjectFormat struct {
	name   string
	tag    byte
	oidLen int
}

// NewObjectFormat constructs the closed SHA-1/SHA-256 object-format value.
func NewObjectFormat(name string) (ObjectFormat, error) {
	switch name {
	case "sha1":
		return ObjectFormat{name: name, tag: 1, oidLen: sha1.Size}, nil
	case "sha256":
		return ObjectFormat{name: name, tag: 2, oidLen: sha256.Size}, nil
	default:
		return ObjectFormat{}, &ValidationError{Reason: fmt.Sprintf("unsupported object format %q", name)}
	}
}

// Name returns the canonical Git object-format name.
func (f ObjectFormat) Name() string { return f.name }

// OIDLength returns the exact raw object-ID length.
func (f ObjectFormat) OIDLength() int { return f.oidLen }

func (f ObjectFormat) valid() bool {
	return (f.tag == 1 && f.name == "sha1" && f.oidLen == sha1.Size) ||
		(f.tag == 2 && f.name == "sha256" && f.oidLen == sha256.Size)
}

func (f ObjectFormat) newHash() hash.Hash {
	if f.tag == 1 {
		return sha1.New()
	}
	return sha256.New()
}

// ObjectID is an immutable raw Git object ID.
type ObjectID struct {
	format ObjectFormat
	raw    [sha256.Size]byte
}

// NewObjectID validates and copies one raw Git object ID.
func NewObjectID(format ObjectFormat, raw []byte) (ObjectID, error) {
	if !format.valid() || len(raw) != format.oidLen {
		return ObjectID{}, &ValidationError{Reason: "object ID length does not match its format"}
	}
	var id ObjectID
	id.format = format
	copy(id.raw[:], raw)
	return id, nil
}

// Format returns the ID's object format.
func (id ObjectID) Format() ObjectFormat { return id.format }

// Bytes returns a copy of the raw object ID.
func (id ObjectID) Bytes() []byte { return bytes.Clone(id.raw[:id.format.oidLen]) }

// Hex returns the lowercase hexadecimal object ID.
func (id ObjectID) Hex() string { return hex.EncodeToString(id.raw[:id.format.oidLen]) }

func (id ObjectID) equal(other ObjectID) bool {
	return id.format == other.format && id.raw == other.raw
}

// Entry is one immutable regular-file manifest entry.
type Entry struct {
	path []byte
	mode string
	size uint64
	oid  ObjectID
}

// NewEntry validates and copies one selected regular-file entry.
func NewEntry(path []byte, mode string, size uint64, oid ObjectID) (Entry, error) {
	if err := validatePath(path); err != nil {
		return Entry{}, err
	}
	if mode != "100644" && mode != "100755" {
		return Entry{}, &ValidationError{Reason: fmt.Sprintf("unsupported mode %q", mode)}
	}
	if !oid.format.valid() {
		return Entry{}, &ValidationError{Reason: "entry has an invalid object ID"}
	}
	if size > maxBlobBytes {
		return Entry{}, &LimitError{Reason: "blob byte limit exceeded"}
	}
	return Entry{path: bytes.Clone(path), mode: mode, size: size, oid: oid}, nil
}

// Path returns a copy of the raw Git path bytes.
func (e Entry) Path() []byte { return bytes.Clone(e.path) }

// Mode returns the canonical Git mode.
func (e Entry) Mode() string { return e.mode }

// Size returns the selected blob size.
func (e Entry) Size() uint64 { return e.size }

// ObjectID returns the selected blob ID.
func (e Entry) ObjectID() ObjectID { return e.oid }

// Manifest is an immutable, canonical selected-tree manifest.
type Manifest struct {
	format    ObjectFormat
	base      ObjectID
	entries   []Entry
	blobBytes uint64
}

// NewManifest validates, sorts and copies an exact selected tree.
func NewManifest(format ObjectFormat, base ObjectID, entries []Entry) (Manifest, error) {
	if !format.valid() || !base.format.valid() || base.format != format {
		return Manifest{}, &ValidationError{Reason: "manifest base does not match its object format"}
	}
	if len(entries) > maxFileCount {
		return Manifest{}, &LimitError{Reason: "file count limit exceeded"}
	}
	canonical := make([]Entry, len(entries))
	var total uint64
	for i, entry := range entries {
		copyEntry, err := NewEntry(entry.path, entry.mode, entry.size, entry.oid)
		if err != nil {
			return Manifest{}, err
		}
		if copyEntry.oid.format != format {
			return Manifest{}, &ValidationError{Reason: "entry object format differs from manifest"}
		}
		if total > maxTotalBytes-copyEntry.size {
			return Manifest{}, &LimitError{Reason: "total blob byte limit exceeded"}
		}
		total += copyEntry.size
		canonical[i] = copyEntry
	}
	slices.SortFunc(canonical, func(a, b Entry) int { return bytes.Compare(a.path, b.path) })
	for i := range canonical {
		if i > 0 {
			previous := canonical[i-1].path
			current := canonical[i].path
			if bytes.Equal(previous, current) {
				return Manifest{}, &ValidationError{Reason: "duplicate manifest path"}
			}
			if len(current) > len(previous) && bytes.HasPrefix(current, previous) && current[len(previous)] == '/' {
				return Manifest{}, &ValidationError{Reason: "file path is also a directory prefix"}
			}
		}
	}
	return Manifest{format: format, base: base, entries: canonical, blobBytes: total}, nil
}

// ObjectFormat returns the manifest object format.
func (m Manifest) ObjectFormat() ObjectFormat { return m.format }

// Base returns the exact selected base object ID.
func (m Manifest) Base() ObjectID { return m.base }

// FileCount returns the exact regular-file count.
func (m Manifest) FileCount() uint64 { return uint64(len(m.entries)) }

// BlobBytes returns the exact sum of selected blob sizes.
func (m Manifest) BlobBytes() uint64 { return m.blobBytes }

// Entries returns an immutable copy of the canonical raw-path order.
func (m Manifest) Entries() []Entry {
	entries := make([]Entry, len(m.entries))
	for i, entry := range m.entries {
		entries[i] = Entry{path: bytes.Clone(entry.path), mode: entry.mode, size: entry.size, oid: entry.oid}
	}
	return entries
}

// Commitment is an immutable SHA-256 manifest commitment.
type Commitment struct{ sum [sha256.Size]byte }

// Bytes returns a copy of the commitment bytes.
func (c Commitment) Bytes() []byte { return bytes.Clone(c.sum[:]) }

// Hex returns the lowercase hexadecimal commitment.
func (c Commitment) Hex() string { return hex.EncodeToString(c.sum[:]) }

// Equal reports exact commitment equality.
func (c Commitment) Equal(other Commitment) bool { return c.sum == other.sum }

// Commitment returns the canonical ManifestCommitmentV1 SHA-256 digest.
func (m Manifest) Commitment() Commitment {
	return Commitment{sum: sha256.Sum256(m.canonicalBytes())}
}

func (m Manifest) canonicalBytes() []byte {
	capacity := len(commitmentPrefix) + 1 + m.format.oidLen + 16
	for _, entry := range m.entries {
		capacity += 4 + len(entry.path) + 6 + 8 + m.format.oidLen
	}
	encoded := make([]byte, 0, capacity)
	encoded = append(encoded, commitmentPrefix...)
	encoded = append(encoded, m.format.tag)
	encoded = append(encoded, m.base.raw[:m.format.oidLen]...)
	encoded = binary.BigEndian.AppendUint64(encoded, uint64(len(m.entries)))
	encoded = binary.BigEndian.AppendUint64(encoded, m.blobBytes)
	for _, entry := range m.entries {
		encoded = binary.BigEndian.AppendUint32(encoded, uint32(len(entry.path)))
		encoded = append(encoded, entry.path...)
		encoded = append(encoded, entry.mode...)
		encoded = binary.BigEndian.AppendUint64(encoded, entry.size)
		encoded = append(encoded, entry.oid.raw[:m.format.oidLen]...)
	}
	return encoded
}

func validatePath(path []byte) error {
	if len(path) == 0 || len(path) > maxPathBytes || path[0] == '/' || path[len(path)-1] == '/' || bytes.IndexByte(path, 0) >= 0 {
		return &ValidationError{Reason: "unsafe manifest path"}
	}
	// Darwin rejects non-UTF-8 path components at openat(2) with EILSEQ. Reject
	// them before staging rather than letting a filesystem boundary reinterpret
	// or partially materialize raw Git path bytes.
	if !utf8.Valid(path) {
		return &ValidationError{Reason: "Darwin cannot preserve a non-UTF-8 path"}
	}
	for _, component := range bytes.Split(path, []byte{'/'}) {
		if len(component) == 0 || len(component) > maxNameBytes || bytes.Equal(component, []byte(".")) || bytes.Equal(component, []byte("..")) {
			return &ValidationError{Reason: "unsafe manifest path component"}
		}
		if strings.EqualFold(string(component), ".git") {
			return &ValidationError{Reason: ".git path components are forbidden"}
		}
	}
	return nil
}

func hashBlob(format ObjectFormat, data []byte) ObjectID {
	hasher := format.newHash()
	_, _ = fmt.Fprintf(hasher, "blob %d\x00", len(data))
	_, _ = hasher.Write(data)
	id, err := NewObjectID(format, hasher.Sum(nil))
	if err != nil {
		panic(errors.New("validated object format produced an invalid hash"))
	}
	return id
}
