// Package change commits to and materializes exact, repository-free source trees.
package change

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"slices"
	"strings"
	"unicode/utf8"
)

// Frozen Change manifest/materialization bounds shared with Store validation.
const (
	MaxEntryCount        uint64 = 10_000
	MaxDepth                    = 64
	MaxRelativePathBytes        = 1_023
	MaxComponentBytes           = 255
	MaxBlobBytes         uint64 = 256 << 20
	MaxTotalBlobBytes    uint64 = 1 << 30
)

const commitmentPrefix = "dark-factory/change-manifest\x00\x01"

// ObjectFormat is one closed Git object-format value.
type ObjectFormat byte

const (
	objectFormatSHA1 ObjectFormat = iota + 1
	objectFormatSHA256
)

// NewObjectFormat constructs the closed SHA-1/SHA-256 object-format value.
func NewObjectFormat(name string) (ObjectFormat, error) {
	switch name {
	case "sha1":
		return objectFormatSHA1, nil
	case "sha256":
		return objectFormatSHA256, nil
	default:
		return 0, &ValidationError{Reason: fmt.Sprintf("unsupported object format %q", name)}
	}
}

// Name returns the canonical Git object-format name.
func (f ObjectFormat) Name() string {
	switch f {
	case objectFormatSHA1:
		return "sha1"
	case objectFormatSHA256:
		return "sha256"
	default:
		return ""
	}
}

// OIDLength returns the exact raw object-ID length, or zero for an invalid format.
func (f ObjectFormat) OIDLength() int {
	switch f {
	case objectFormatSHA1:
		return sha1.Size
	case objectFormatSHA256:
		return sha256.Size
	default:
		return 0
	}
}

func (f ObjectFormat) valid() bool { return f == objectFormatSHA1 || f == objectFormatSHA256 }

func (f ObjectFormat) newHash() hash.Hash {
	switch f {
	case objectFormatSHA1:
		return sha1.New()
	case objectFormatSHA256:
		return sha256.New()
	default:
		panic("invalid object format reached hash construction")
	}
}

// ObjectID is an immutable raw Git object ID.
type ObjectID struct {
	format ObjectFormat
	raw    [sha256.Size]byte
}

// NewObjectID validates and copies one raw Git object ID.
func NewObjectID(format ObjectFormat, raw []byte) (ObjectID, error) {
	if !format.valid() || len(raw) != format.OIDLength() {
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
func (id ObjectID) Bytes() []byte { return bytes.Clone(id.raw[:id.format.OIDLength()]) }

// Hex returns the lowercase hexadecimal object ID.
func (id ObjectID) Hex() string { return hex.EncodeToString(id.raw[:id.format.OIDLength()]) }

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
	if _, err := validatePath(path); err != nil {
		return Entry{}, err
	}
	if mode != "100644" && mode != "100755" {
		return Entry{}, &ValidationError{Reason: fmt.Sprintf("unsupported mode %q", mode)}
	}
	if !oid.format.valid() {
		return Entry{}, &ValidationError{Reason: "entry has an invalid object ID"}
	}
	if size > MaxBlobBytes {
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
	format      ObjectFormat
	base        ObjectID
	entries     []Entry
	directories []string
	entryCount  uint64
	blobBytes   uint64
}

// NewManifest validates, sorts and copies an exact selected tree. EntryCount
// includes regular files and unique implied directories, excluding the root.
func NewManifest(format ObjectFormat, base ObjectID, entries []Entry) (Manifest, error) {
	if !format.valid() || !base.format.valid() || base.format != format {
		return Manifest{}, &ValidationError{Reason: "manifest base does not match its object format"}
	}
	if uint64(len(entries)) > MaxEntryCount {
		return Manifest{}, &LimitError{Reason: "total entry limit exceeded"}
	}
	canonical := make([]Entry, len(entries))
	directorySet := make(map[string]struct{})
	var total uint64
	for i, entry := range entries {
		copyEntry, err := NewEntry(entry.path, entry.mode, entry.size, entry.oid)
		if err != nil {
			return Manifest{}, err
		}
		if copyEntry.oid.format != format {
			return Manifest{}, &ValidationError{Reason: "entry object format differs from manifest"}
		}
		if total > MaxTotalBlobBytes-copyEntry.size {
			return Manifest{}, &LimitError{Reason: "total blob byte limit exceeded"}
		}
		total += copyEntry.size
		components := strings.Split(string(copyEntry.path), "/")
		for depth := 1; depth < len(components); depth++ {
			directorySet[strings.Join(components[:depth], "/")] = struct{}{}
		}
		if uint64(len(entries)+len(directorySet)) > MaxEntryCount {
			return Manifest{}, &LimitError{Reason: "total entry limit exceeded"}
		}
		canonical[i] = copyEntry
	}
	slices.SortFunc(canonical, func(a, b Entry) int { return bytes.Compare(a.path, b.path) })
	for i := range canonical {
		if i == 0 {
			continue
		}
		previous := canonical[i-1].path
		current := canonical[i].path
		if bytes.Equal(previous, current) {
			return Manifest{}, &ValidationError{Reason: "duplicate manifest path"}
		}
		if len(current) > len(previous) && bytes.HasPrefix(current, previous) && current[len(previous)] == '/' {
			return Manifest{}, &ValidationError{Reason: "file path is also a directory prefix"}
		}
	}
	directories := make([]string, 0, len(directorySet))
	for directory := range directorySet {
		directories = append(directories, directory)
	}
	slices.Sort(directories)
	return Manifest{
		format:      format,
		base:        base,
		entries:     canonical,
		directories: directories,
		entryCount:  uint64(len(canonical) + len(directories)),
		blobBytes:   total,
	}, nil
}

// ObjectFormat returns the manifest object format.
func (m Manifest) ObjectFormat() ObjectFormat { return m.format }

// Base returns the exact selected base object ID.
func (m Manifest) Base() ObjectID { return m.base }

// EntryCount returns regular files plus unique implied directories, excluding root.
func (m Manifest) EntryCount() uint64 { return m.entryCount }

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

// ParseCommitment copies one exact 32-byte commitment.
func ParseCommitment(raw []byte) (Commitment, error) {
	if len(raw) != sha256.Size {
		return Commitment{}, &ValidationError{Reason: "manifest commitment must be exactly 32 bytes"}
	}
	var commitment Commitment
	copy(commitment.sum[:], raw)
	return commitment, nil
}

// Bytes returns a copy of the commitment bytes.
func (c Commitment) Bytes() []byte { return bytes.Clone(c.sum[:]) }

// Hex returns the lowercase hexadecimal commitment.
func (c Commitment) Hex() string { return hex.EncodeToString(c.sum[:]) }

// Equal reports exact commitment equality.
func (c Commitment) Equal(other Commitment) bool { return c.sum == other.sum }

// Commitment returns the canonical ManifestCommitmentV1 SHA-256 digest.
func (m Manifest) Commitment() Commitment { return Commitment{sum: sha256.Sum256(m.canonicalBytes())} }

func (m Manifest) canonicalBytes() []byte {
	oidLength := m.format.OIDLength()
	capacity := len(commitmentPrefix) + 1 + oidLength + 16
	for _, entry := range m.entries {
		capacity += 4 + len(entry.path) + 6 + 8 + oidLength
	}
	encoded := make([]byte, 0, capacity)
	encoded = append(encoded, commitmentPrefix...)
	encoded = append(encoded, byte(m.format))
	encoded = append(encoded, m.base.raw[:oidLength]...)
	encoded = binary.BigEndian.AppendUint64(encoded, m.entryCount)
	encoded = binary.BigEndian.AppendUint64(encoded, m.blobBytes)
	for _, entry := range m.entries {
		encoded = binary.BigEndian.AppendUint32(encoded, uint32(len(entry.path)))
		encoded = append(encoded, entry.path...)
		encoded = append(encoded, entry.mode...)
		encoded = binary.BigEndian.AppendUint64(encoded, entry.size)
		encoded = append(encoded, entry.oid.raw[:oidLength]...)
	}
	return encoded
}

func validatePath(path []byte) (int, error) {
	if len(path) == 0 || len(path) > MaxRelativePathBytes || path[0] == '/' || path[len(path)-1] == '/' || bytes.IndexByte(path, 0) >= 0 {
		return 0, &ValidationError{Reason: "unsafe manifest path"}
	}
	// Darwin openat(2) rejects non-UTF-8 path components with EILSEQ. Reject
	// them before Prepare so no declared staging effect is needed.
	if !utf8.Valid(path) {
		return 0, &ValidationError{Reason: "Darwin cannot preserve a non-UTF-8 path"}
	}
	components := bytes.Split(path, []byte{'/'})
	if len(components) > MaxDepth {
		return 0, &LimitError{Reason: "manifest depth limit exceeded"}
	}
	for _, component := range components {
		if len(component) == 0 || len(component) > MaxComponentBytes || bytes.Equal(component, []byte(".")) || bytes.Equal(component, []byte("..")) {
			return 0, &ValidationError{Reason: "unsafe manifest path component"}
		}
		if strings.EqualFold(string(component), ".git") {
			return 0, &ValidationError{Reason: ".git path components are forbidden"}
		}
	}
	return len(components), nil
}

func hashBlob(format ObjectFormat, data []byte) ObjectID {
	id, err := hashBlobContext(context.Background(), format, data, nil)
	if err != nil {
		panic("background blob hash was canceled")
	}
	return id
}

func hashBlobContext(ctx context.Context, format ObjectFormat, data []byte, eachChunk func() error) (ObjectID, error) {
	hasher := format.newHash()
	_, _ = fmt.Fprintf(hasher, "blob %d\x00", len(data))
	for offset := 0; offset < len(data); {
		if err := ctx.Err(); err != nil {
			return ObjectID{}, err
		}
		end := min(offset+(64<<10), len(data))
		_, _ = hasher.Write(data[offset:end])
		offset = end
		if eachChunk != nil {
			if err := eachChunk(); err != nil {
				return ObjectID{}, err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return ObjectID{}, err
	}
	return NewObjectID(format, hasher.Sum(nil))
}
