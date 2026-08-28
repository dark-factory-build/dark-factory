// Package buildinfo owns the immutable identity linked into every local-runtime binary.
package buildinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"runtime"
	"strings"
)

// receipt is replaced once by the release build with -ldflags -X. Keeping the
// complete identity in one linked value lets the packager verify the same
// bytes that Current consumes instead of trusting a filename or build command.
var receipt = "development"

// Identity is the common release identity carried by factoryd,
// factory-runner, and factoryctl.
type Identity struct {
	version string
	source  string
	target  string
	buildID string
}

// Current returns a fresh immutable value; it exposes no mutable global state.
func Current() Identity {
	target := runtime.GOOS + "/" + runtime.GOARCH
	identity, ok := parseReceipt(receipt)
	if !ok || identity.target != target {
		return Identity{version: "development", source: "development", target: target, buildID: "development"}
	}
	return identity
}

func (identity Identity) Version() string { return identity.version }
func (identity Identity) Source() string  { return identity.source }
func (identity Identity) Target() string  { return identity.target }
func (identity Identity) BuildID() string { return identity.buildID }

// Expected constructs the only valid release identity for the three linked
// fields. The boolean is false for malformed or unsupported values.
func Expected(version, source, target string) (Identity, bool) {
	identity := Identity{version: version, source: source, target: target}
	identity.buildID = digest(version, source, target)
	return identity, identity.Release()
}

// Receipt is the exact value linked into a release binary. It is deliberately
// not a general serialization format; v1 has one closed four-field shape.
func (identity Identity) Receipt() string {
	if !identity.Release() {
		return ""
	}
	return strings.Join([]string{identity.version, identity.source, identity.target, identity.buildID}, "|")
}

// WriteJSON emits the bounded diagnostic identity consumed by release tests
// and the Homebrew formula. No private runtime state is representable here.
func (identity Identity) WriteJSON(writer io.Writer) error {
	return json.NewEncoder(writer).Encode(struct {
		Version string `json:"version"`
		Source  string `json:"source"`
		Target  string `json:"target"`
		BuildID string `json:"build_id"`
		Release bool   `json:"release"`
	}{identity.version, identity.source, identity.target, identity.buildID, identity.Release()})
}

// Release reports whether the linked identity is suitable for an installed
// release receipt. Development binaries remain runnable in isolated tests but
// cannot accidentally be accepted as a release bundle.
func (identity Identity) Release() bool {
	return validVersion(identity.version) && validSource(identity.source) && validTarget(identity.target) && validDigest(identity.buildID) && identity.buildID == digest(identity.version, identity.source, identity.target)
}

func parseReceipt(value string) (Identity, bool) {
	fields := strings.Split(value, "|")
	if len(fields) != 4 {
		return Identity{}, false
	}
	identity := Identity{version: fields[0], source: fields[1], target: fields[2], buildID: fields[3]}
	return identity, identity.Release()
}

func digest(version, source, target string) string {
	hash := sha256.Sum256([]byte("dark-factory/go-v1\x00" + version + "\x00" + source + "\x00" + target))
	return hex.EncodeToString(hash[:])
}

func validVersion(value string) bool {
	if value == "" || len(value) > 64 || value[0] < '0' || value[0] > '9' {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune(".-+", character) {
			continue
		}
		return false
	}
	return true
}

func validSource(value string) bool { return validLowerHex(value, 40) }

func validTarget(value string) bool {
	return value == "darwin/arm64" || value == "darwin/amd64"
}

func validDigest(value string) bool { return validLowerHex(value, 64) }

func validLowerHex(value string, length int) bool {
	if len(value) != length || value == strings.Repeat("0", length) {
		return false
	}
	for _, character := range []byte(value) {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}
