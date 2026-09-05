package browserprotocol

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxRemoteInviteLinkBytes = 8192
	MaxRemoteInviteSVGBytes  = 32768
)

// remoteInviteLinkPrefix is the exact everything-before-the-members of the
// invitation the daemon mints. The link is a live pairing secret, so the wire
// admits only that one syntactic form.
const remoteInviteLinkPrefix = "https://app.darkfactory.build/remote#df_remote&"

// RemoteInvite asks the factory for one remote pairing invitation. It carries
// no members: the invitation is entirely the daemon's to mint.
type RemoteInvite struct{}

type RemoteInviteResult struct {
	Link        string  `json:"link"`
	ExpiresAtMS Decimal `json:"expires_at_ms"`
	SVG         string  `json:"svg"`
}

func EncodeRemoteInvite(id string, value RemoteInvite) ([]byte, error) {
	return encodeControl(TypeRemoteInvite, id, value)
}

func EncodeRemoteInviteResult(id string, value RemoteInviteResult) ([]byte, error) {
	return encodeControl(TypeRemoteInviteResult, id, value)
}

func validRemoteControl(kind MessageType, body any) error {
	if value, ok := body.(*RemoteInvite); ok {
		return validRemoteControl(kind, *value)
	}
	if value, ok := body.(*RemoteInviteResult); ok {
		return validRemoteControl(kind, *value)
	}
	bad := func() error { return fmt.Errorf("%w: invalid %s", ErrMalformed, kind) }
	link := func(value string) bool {
		if len(value) == 0 || len(value) > MaxRemoteInviteLinkBytes || !utf8.ValidString(value) || !strings.HasPrefix(value, remoteInviteLinkPrefix) {
			return false
		}
		for _, character := range value {
			if character < 0x20 || character == 0x7f {
				return false
			}
		}
		return true
	}
	switch value := body.(type) {
	case RemoteInvite:
	case RemoteInviteResult:
		if !link(value.Link) || value.ExpiresAtMS == 0 || !utf8.ValidString(value.SVG) ||
			len(value.SVG) == 0 || len(value.SVG) > MaxRemoteInviteSVGBytes || !strings.HasPrefix(value.SVG, "<svg") {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}
