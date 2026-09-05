package browserprotocol

import (
	"strings"
	"testing"
)

func TestRemoteInviteControlBoundsAndDirection(t *testing.T) {
	wire, err := EncodeRemoteInvite("invite-1", RemoteInvite{})
	if err != nil {
		t.Fatal(err)
	}
	if frame, err := DecodeClientControl(wire); err != nil || frame.Type != TypeRemoteInvite {
		t.Fatalf("request round-trip = %+v, err=%v", frame, err)
	}
	if _, err := DecodeServerControl(wire); err != ErrMalformed {
		t.Fatalf("client request crossed server decoder: %v", err)
	}
	result := RemoteInviteResult{
		Link:        remoteInviteLinkPrefix + "node=n0&expires=1767225600",
		ExpiresAtMS: 1767225600000,
		SVG:         `<svg viewBox="0 0 1 1"/>`,
	}
	resultWire, err := EncodeRemoteInviteResult("invite-1", result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeServerControl(resultWire); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeClientControl(resultWire); err != ErrMalformed {
		t.Fatalf("server result crossed client decoder: %v", err)
	}
	// The link is a live pairing secret: only the one minted form is accepted.
	for _, link := range []string{"", "https://app.darkfactory.build/remote#df_remote", "http://app.darkfactory.build/remote#df_remote&x=1", result.Link + "\n", result.Link + strings.Repeat("x", MaxRemoteInviteLinkBytes)} {
		invalid := result
		invalid.Link = link
		if _, err := EncodeRemoteInviteResult("invite-2", invalid); err == nil {
			t.Fatalf("invalid link accepted: %q", link)
		}
	}
	for _, svg := range []string{"", "<rect/>", "<svg" + strings.Repeat("x", MaxRemoteInviteSVGBytes)} {
		invalid := result
		invalid.SVG = svg
		if _, err := EncodeRemoteInviteResult("invite-3", invalid); err == nil {
			t.Fatalf("invalid svg accepted: %q", svg[:min(len(svg), 16)])
		}
	}
	invalid := result
	invalid.ExpiresAtMS = 0
	if _, err := EncodeRemoteInviteResult("invite-4", invalid); err == nil {
		t.Fatal("zero expiry accepted")
	}
}
