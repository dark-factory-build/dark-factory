package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"rsc.io/qr"
)

// qrQuietModules is the standard four-module margin. It is drawn rather than
// assumed: a terminal edge is not a quiet zone.
const qrQuietModules = 4

// maxInvitationLinkBytes bounds the printed link. It is larger than the web
// launch URL because an invitation carries a relay ticket in its fragment.
const maxInvitationLinkBytes = 8192

func parseRemote(args []string) (attemptCommand, bool, bool) {
	if len(args) == 2 && helpFlag(args[1]) {
		return attemptCommand{}, true, true
	}
	if len(args) == 3 && helpFlag(args[2]) {
		switch args[1] {
		case "pair", "status":
			return attemptCommand{}, true, true
		}
		return attemptCommand{}, false, false
	}
	if len(args) != 2 {
		return attemptCommand{}, false, false
	}
	switch args[1] {
	case "pair":
		return attemptCommand{kind: commandRemotePair}, false, true
	case "status":
		return attemptCommand{kind: commandRemoteStatus}, false, true
	}
	return attemptCommand{}, false, false
}

func runRemote(ctx context.Context, command attemptCommand, getenv func(string) string, stdout, stderr io.Writer) int {
	socket, token := getenv("DARK_FACTORY_SOCKET"), getenv("DARK_FACTORY_OPERATOR_TOKEN_FILE")
	if socket == "" || token == "" {
		_, _ = io.WriteString(stderr, "factoryctl: remote client configuration is invalid\n")
		return exitFailure
	}
	client, err := api.NewOperatorClient(socket, token)
	if err != nil {
		_, _ = io.WriteString(stderr, "factoryctl: remote client configuration is invalid\n")
		return exitFailure
	}
	callContext, cancel := context.WithTimeout(ctx, attemptRequestTimeout)
	defer cancel()
	switch command.kind {
	case commandRemoteStatus:
		result, callErr := client.RemoteStatus(callContext)
		if callErr != nil {
			return writeRemoteFailure(stderr, "remote status", callErr)
		}
		return writeJSON(stdout, result)
	case commandRemotePair:
		invitation, callErr := client.RemotePair(callContext)
		if callErr != nil {
			return writeRemoteFailure(stderr, "remote pair", callErr)
		}
		// The link carries a live pairing challenge and relay ticket. It is
		// printed only after this process has proved it names the exact
		// challenge the daemon reported minting.
		if _, exact := exactInvitationDigest(invitation); !exact {
			_, _ = io.WriteString(stderr, "factoryctl: remote pair returned an invitation that is not exact; refusing to print it\n")
			return exitFailure
		}
		code, codeErr := renderQR(invitation.Link)
		if codeErr != nil {
			_, _ = io.WriteString(stderr, "factoryctl: remote pair could not render the invitation code\n")
			return exitFailure
		}
		_, _ = fmt.Fprintf(stdout, "%s\n%sexpires %s\n", invitation.Link, code, time.Unix(invitation.Expires, 0).UTC().Format(time.RFC3339))
		return 0
	default:
		return exitUsage
	}
}

func writeRemoteFailure(stderr io.Writer, subject string, err error) int {
	var remote *api.RemoteError
	if errors.As(err, &remote) && remote.Code() == api.RemoteNotFound {
		_, _ = io.WriteString(stderr, "factoryctl: remote access is not enabled; start factoryd with --relay-origin\n")
		return exitFailure
	}
	return writeWebFailure(stderr, subject, err)
}

// remoteLinkBase is the exact everything-before-the-fragment of an invitation.
const remoteLinkBase = "https://app.darkfactory.build/remote"

// exactInvitationDigest proves the only invitation factoryctl will print: the
// link is the fixed syntactic form, its fragment parses as query pairs naming
// the same node and expiry as the reply, and the daemon's digest is SHA-256 of
// the challenge the link itself carries.
func exactInvitationDigest(invitation api.RemoteInvitation) (string, bool) {
	if len(invitation.Link) < 1 || len(invitation.Link) > maxInvitationLinkBytes || !utf8.ValidString(invitation.Link) || strings.ContainsRune(invitation.Link, 0) || !validHex(invitation.ChallengeDigest, 64) {
		return "", false
	}
	// The fragment is read raw. url.Parse would percent-decode it once before
	// ParseQuery decoded it again, which would corrupt an escaped label.
	base, fragment, found := strings.Cut(invitation.Link, "#")
	if !found || base != remoteLinkBase || !strings.HasPrefix(fragment, "df_remote&") {
		return "", false
	}
	values, err := url.ParseQuery(fragment)
	if err != nil {
		return "", false
	}
	// Unknown members are ignored deliberately; only these three carry
	// anything this command must check.
	if values.Get("node") == "" || values.Get("node") != invitation.NodeID || values.Get("expires") != strconv.FormatInt(invitation.Expires, 10) || !validHex(values.Get("challenge"), 64) {
		return "", false
	}
	challenge, err := hex.DecodeString(values.Get("challenge"))
	if err != nil || len(challenge) != 32 {
		return "", false
	}
	digest := sha256.Sum256(challenge)
	encoded := hex.EncodeToString(digest[:])
	if encoded != invitation.ChallengeDigest {
		return "", false
	}
	return encoded, true
}

// renderQR draws one QR code as text, two module rows per line, using only
// the half-block characters and the space. A terminal cell is roughly twice
// as tall as wide, so one module per column and two per row keeps modules
// square; packing two modules per column as well (quadrant glyphs) squashes
// the symbol horizontally and finder-pattern cross-checks reject it. Ink is a
// LIGHT module (and the quiet zone); terminals are overwhelmingly
// dark-background, so a dark module is left blank instead — the `qrencode
// -t ANSIUTF8` convention. Drawn the other way around, a code is its own
// negative on the terminals developers actually use, and many scanners
// refuse it.
func renderQR(text string) (string, error) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return "", err
	}
	if code == nil || code.Size <= 0 {
		return "", errors.New("factoryctl: the invitation produced no QR code")
	}
	span := code.Size + 2*qrQuietModules
	var builder strings.Builder
	for row := 0; row < span; row += 2 {
		for column := 0; column < span; column++ {
			// Black reports false outside the code, so the quiet zone needs no
			// special case: it is simply the region no module covers, and
			// reads as light.
			upperLight := !code.Black(column-qrQuietModules, row-qrQuietModules)
			lowerLight := !code.Black(column-qrQuietModules, row+1-qrQuietModules)
			switch {
			case upperLight && lowerLight:
				builder.WriteRune('█')
			case upperLight:
				builder.WriteRune('▀')
			case lowerLight:
				builder.WriteRune('▄')
			default:
				builder.WriteRune(' ')
			}
		}
		builder.WriteByte('\n')
	}
	return builder.String(), nil
}
