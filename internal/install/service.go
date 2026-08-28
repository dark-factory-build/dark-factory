package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
)

const serviceLabel = "com.dark-factory.factoryd"

const serviceMaxPathBytes = 4096

var (
	ErrServiceAmbiguous = errors.New("service ownership is ambiguous")
	ErrServiceBundle    = errors.New("service bundle is invalid")
	ErrServiceLaunchctl = errors.New("launchctl result is not authoritative")
	ErrServicePlist     = errors.New("service plist is invalid")
)

// ServiceState is the bounded read-only projection returned by factoryctl.
// This first slice can prove only exact absence; every partial or present
// external fact remains ambiguous until the crash-safe install receipt lands.
type ServiceState string

const (
	ServiceAbsent    ServiceState = "absent"
	ServiceAmbiguous ServiceState = "ambiguous"
)

// ServiceStatus contains no home, executable, plist, socket, token, or
// launchctl diagnostic text. PID is reported only after a strict launchctl
// parse and does not grant process authority.
type ServiceStatus struct {
	State ServiceState `json:"state"`
	PID   int          `json:"pid,omitempty"`
}

// ServiceBundle retains the exact three sibling release executables accepted
// from factoryctl's own directory. Future installation code snapshots these
// descriptors; it never re-resolves an ambient PATH or a changed source name.
type ServiceBundle struct {
	state *serviceBundleState
}

// OpenServiceBundle validates and retains factoryctl, factoryd and
// factory-runner under one immutable release identity.
func OpenServiceBundle(factoryctlPath string, expected buildinfo.Identity) (*ServiceBundle, error) {
	return openServiceBundle(factoryctlPath, expected)
}

// Snapshot writes one validated bundle member into an already-opened private
// staging directory. The fixed component name is both source and destination.
func (bundle *ServiceBundle) Snapshot(parent *os.File, component string) error {
	if bundle == nil || bundle.state == nil {
		return ErrClosed
	}
	return bundle.state.snapshot(parent, component)
}

// Close releases the retained source authority. It is idempotent.
func (bundle *ServiceBundle) Close() error {
	if bundle == nil || bundle.state == nil {
		return nil
	}
	return bundle.state.close()
}

func (bundle ServiceBundle) String() string   { return "ServiceBundle(<redacted>)" }
func (bundle ServiceBundle) GoString() string { return "ServiceBundle(<redacted>)" }

// InspectService is a strictly read-only service projection. It never opens a
// mutation lock, recovers pending state, writes installation metadata, or
// invokes a mutating launchctl verb.
func InspectService(ctx context.Context, home, userHome string) (ServiceStatus, error) {
	return inspectService(ctx, home, userHome, runLaunchctl)
}

// ServicePlist renders the one Go-v1 launchd job. Exact byte comparison is the
// parser: accepting a plist means accepting precisely this finite allowlist.
func ServicePlist(home string) ([]byte, [sha256.Size]byte, error) {
	if !validServicePath(home) || filepath.Base(home) == string(filepath.Separator) {
		return nil, [sha256.Size]byte{}, fmt.Errorf("%w: home path", ErrServicePlist)
	}
	program := filepath.Join(home, "bin", "current", "factoryd")
	var escapedHome, escapedProgram bytes.Buffer
	escapeXML(&escapedHome, home)
	escapeXML(&escapedProgram, program)
	body := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>` + serviceLabel + `</string>
    <key>ProgramArguments</key>
    <array>
        <string>` + escapedProgram.String() + `</string>
        <string>--home</string>
        <string>` + escapedHome.String() + `</string>
    </array>
    <key>WorkingDirectory</key>
    <string>` + escapedHome.String() + `</string>
    <key>RunAtLoad</key>
    <true/>
    <key>AbandonProcessGroup</key>
    <true/>
    <key>ProcessType</key>
    <string>Background</string>
    <key>Umask</key>
    <integer>63</integer>
</dict>
</plist>
`)
	return body, sha256.Sum256(body), nil
}

func validServicePath(value string) bool {
	return value != "" && len(value) <= serviceMaxPathBytes && filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsRune(value, 0)
}

func escapeXML(destination *bytes.Buffer, value string) {
	for _, character := range value {
		switch character {
		case '&':
			destination.WriteString("&amp;")
		case '<':
			destination.WriteString("&lt;")
		case '>':
			destination.WriteString("&gt;")
		case '"':
			destination.WriteString("&quot;")
		case '\'':
			destination.WriteString("&apos;")
		default:
			destination.WriteRune(character)
		}
	}
}
