// Package buildinfo owns the immutable identity linked into every local-runtime binary.
package buildinfo

import (
	"crypto/sha256"
	"encoding/hex"
	"runtime"
	"strings"
)

// Version and Source are set by the release build with -ldflags -X. Their
// development values are deliberately recognizable and cannot be mistaken for
// a release receipt by the installer.
var (
	version = "development"
	source  = "development"
)

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
	return Identity{version: version, source: source, target: target, buildID: digest(version, source, target)}
}

func (identity Identity) Version() string { return identity.version }
func (identity Identity) Source() string  { return identity.source }
func (identity Identity) Target() string  { return identity.target }
func (identity Identity) BuildID() string { return identity.buildID }

// Release reports whether the linked identity is suitable for an installed
// release receipt. Development binaries remain runnable in isolated tests but
// cannot accidentally be accepted as a release bundle.
func (identity Identity) Release() bool {
	return validVersion(identity.version) && validSource(identity.source) && validTarget(identity.target) && validDigest(identity.buildID) && identity.buildID == digest(identity.version, identity.source, identity.target)
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
