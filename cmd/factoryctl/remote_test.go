//go:build darwin || linux

package main

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"rsc.io/qr"
)

const (
	remoteTestNode      = "abcdefghijklmnopqrstuvwxyz234567"
	remoteTestChallenge = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	remoteTestDigest    = "4884fdaafea47c29fea7159d0daddd9c085d6200e1359e85bb81736af6b7c837"
	remoteTestExpires   = 1757000000
	remoteTestExpiresAt = "2025-09-04T15:33:20Z"
)

func TestParseExactRemoteCommands(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		command attemptCommand
		help    bool
	}{
		{name: "remote help", args: []string{"remote", "--help"}, help: true},
		{name: "pair help", args: []string{"remote", "pair", "--help"}, help: true},
		{name: "pair", args: []string{"remote", "pair"}, command: attemptCommand{kind: commandRemotePair}},
		{name: "status", args: []string{"remote", "status"}, command: attemptCommand{kind: commandRemoteStatus}},
	} {
		t.Run(test.name, func(t *testing.T) {
			command, help, ok := parse(test.args)
			if !ok || help != test.help || command != test.command {
				t.Fatalf("parse = %+v, help=%t, ok=%t", command, help, ok)
			}
		})
	}
}

func TestInvalidRemoteSyntaxStopsBeforeEnvironment(t *testing.T) {
	for index, args := range [][]string{
		{"remote"},
		{"remote", "open"},
		{"remote", "pair", "--node", remoteTestNode},
		{"remote", "status", "extra"},
		{"remote", "pair", "extra"},
	} {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			lookups := 0
			var stdout, stderr bytes.Buffer
			exit := runWithOpener(context.Background(), args, func(string) string {
				lookups++
				return "/private/should-not-be-read"
			}, &stdout, &stderr, nil)
			if exit != exitUsage || lookups != 0 || stdout.Len() != 0 || stderr.String() != usage {
				t.Fatalf("run = exit %d lookups %d stdout %q stderr %q", exit, lookups, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRemotePairPrintsTheLinkItsCodeAndItsExpiry(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	invitation := testInvitation(t, remoteTestChallenge, remoteTestDigest)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		if call.Kind() != api.CallRemotePair {
			return mustWebErrorReply(t, api.RemoteInvalidRequest)
		}
		reply, err := api.NewRemoteInvitationReply(invitation)
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := runWithOpener(context.Background(), []string{"remote", "pair"}, webEnvironment(fixture), &stdout, &stderr, nil)
	if result := awaitServer(t, done); result.err != nil {
		t.Fatal(result.err)
	}
	code, err := renderQR(invitation.Link)
	if err != nil {
		t.Fatal(err)
	}
	want := invitation.Link + "\n" + code + "expires " + remoteTestExpiresAt + "\n"
	if exit != 0 || stderr.Len() != 0 || stdout.String() != want {
		t.Fatalf("remote pair = exit %d stderr %q\nstdout %q\nwant %q", exit, stderr.String(), stdout.String(), want)
	}
	// The link is the invitation, so it is printed once and whole; the raw
	// challenge appears only inside that one line, never as a separate field.
	if strings.Count(stdout.String(), invitation.Link) != 1 || strings.Count(stdout.String(), remoteTestChallenge) != 1 {
		t.Fatal("remote pair printed the challenge outside the link")
	}
}

func TestRemotePairRefusesAnInvitationWhoseLinkIsNotTheReportedChallenge(t *testing.T) {
	const otherChallenge = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	for _, test := range []struct {
		name       string
		invitation func(*testing.T) api.RemoteInvitation
	}{
		{name: "digest names another challenge", invitation: func(t *testing.T) api.RemoteInvitation {
			return testInvitation(t, otherChallenge, remoteTestDigest)
		}},
		{name: "link names another node", invitation: func(t *testing.T) api.RemoteInvitation {
			invitation := testInvitation(t, remoteTestChallenge, remoteTestDigest)
			invitation.NodeID = "zyxwvutsrqponmlkjihgfedcba234567"
			return invitation
		}},
		{name: "link names another expiry", invitation: func(t *testing.T) api.RemoteInvitation {
			invitation := testInvitation(t, remoteTestChallenge, remoteTestDigest)
			invitation.Expires = remoteTestExpires + 1
			return invitation
		}},
		{name: "link is a web launch", invitation: func(t *testing.T) api.RemoteInvitation {
			invitation := testInvitation(t, remoteTestChallenge, remoteTestDigest)
			invitation.Link = "https://app.darkfactory.build/#df_pair=" + remoteTestChallenge
			return invitation
		}},
		{name: "link carries a query", invitation: func(t *testing.T) api.RemoteInvitation {
			invitation := testInvitation(t, remoteTestChallenge, remoteTestDigest)
			invitation.Link = strings.Replace(invitation.Link, "/remote#", "/remote?leak=1#", 1)
			return invitation
		}},
		{name: "fragment is not the invitation marker", invitation: func(t *testing.T) api.RemoteInvitation {
			invitation := testInvitation(t, remoteTestChallenge, remoteTestDigest)
			invitation.Link = strings.Replace(invitation.Link, "#df_remote&", "#relay=x&", 1)
			return invitation
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAPIFixture(t)
			defer fixture.close(t)
			invitation := test.invitation(t)
			done := serveOne(fixture.listener, func(call api.Call) api.Reply {
				if call.Kind() != api.CallRemotePair {
					return mustWebErrorReply(t, api.RemoteInvalidRequest)
				}
				reply, err := api.NewRemoteInvitationReply(invitation)
				if err != nil {
					// A reply the transport itself refuses is a stronger
					// refusal than the one under test; report it as an error.
					return mustWebErrorReply(t, api.RemoteInternal)
				}
				return reply
			})
			var stdout, stderr bytes.Buffer
			exit := runWithOpener(context.Background(), []string{"remote", "pair"}, webEnvironment(fixture), &stdout, &stderr, nil)
			if result := awaitServer(t, done); result.err != nil {
				t.Fatal(result.err)
			}
			if exit == 0 || stdout.Len() != 0 {
				t.Fatalf("inexact invitation printed: exit %d stdout %q", exit, stdout.String())
			}
			if stderr.String() != "factoryctl: remote pair returned an invitation that is not exact; refusing to print it\n" &&
				stderr.String() != "factoryctl: remote pair was not accepted\n" {
				t.Fatalf("inexact invitation stderr = %q", stderr.String())
			}
		})
	}
}

func TestRemoteCommandsReportADisabledRelayExactly(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		kind api.CallKind
	}{
		{name: "pair", args: []string{"remote", "pair"}, kind: api.CallRemotePair},
		{name: "status", args: []string{"remote", "status"}, kind: api.CallRemoteStatus},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAPIFixture(t)
			defer fixture.close(t)
			done := serveOne(fixture.listener, func(call api.Call) api.Reply {
				if call.Kind() != test.kind {
					return mustWebErrorReply(t, api.RemoteInvalidRequest)
				}
				return mustWebErrorReply(t, api.RemoteNotFound)
			})
			var stdout, stderr bytes.Buffer
			exit := runWithOpener(context.Background(), test.args, webEnvironment(fixture), &stdout, &stderr, nil)
			if result := awaitServer(t, done); result.err != nil {
				t.Fatal(result.err)
			}
			if exit == 0 || stdout.Len() != 0 || stderr.String() != "factoryctl: remote access is not enabled; start factoryd with --relay-origin\n" {
				t.Fatalf("disabled relay = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRemoteStatusPrintsTheNodeRelayAndConnection(t *testing.T) {
	fixture := newAPIFixture(t)
	defer fixture.close(t)
	done := serveOne(fixture.listener, func(call api.Call) api.Reply {
		if call.Kind() != api.CallRemoteStatus {
			return mustWebErrorReply(t, api.RemoteInvalidRequest)
		}
		reply, err := api.NewRemoteStatusReply(api.RemoteStatus{NodeID: remoteTestNode, RelayOrigin: "wss://relay.darkfactory.build", Connected: true, Sessions: 2})
		if err != nil {
			t.Fatal(err)
		}
		return reply
	})
	var stdout, stderr bytes.Buffer
	exit := runWithOpener(context.Background(), []string{"remote", "status"}, webEnvironment(fixture), &stdout, &stderr, nil)
	if result := awaitServer(t, done); result.err != nil {
		t.Fatal(result.err)
	}
	want := `{"node_id":"` + remoteTestNode + `","relay_origin":"wss://relay.darkfactory.build","connected":true,"sessions":2}` + "\n"
	if exit != 0 || stderr.Len() != 0 || stdout.String() != want {
		t.Fatalf("remote status = exit %d stdout %q stderr %q", exit, stdout.String(), stderr.String())
	}
}

// TestTerminalCodeDrawsTheRealSymbol reads the drawing back into modules and
// checks the structure a scanner needs: an exact quiet zone and the three
// finder patterns in their corners. Without this the renderer could be off by
// one module, or inverted, and still look plausible.
func TestTerminalCodeDrawsTheRealSymbol(t *testing.T) {
	// 517 bytes: a realistic invitation link, and just large enough to force
	// a version-15 symbol. Rendered width is size + 8 columns (the
	// 4-module quiet zone on each side): 85 for version 15.
	text := "https://app.darkfactory.build/remote#df_remote=" + strings.Repeat("A", 470)
	rendered, err := renderQR(text)
	if err != nil {
		t.Fatal(err)
	}
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		t.Fatal(err)
	}
	if code.Size != 77 {
		t.Fatalf("fixture text encodes as version %d, want version 15 (size 77)", (code.Size-17)/4)
	}
	span := code.Size + 2*qrQuietModules
	grid := decodeHalfBlocks(t, rendered, span)

	for y := 0; y < span; y++ {
		for x := 0; x < span; x++ {
			quiet := x < qrQuietModules || y < qrQuietModules || x >= span-qrQuietModules || y >= span-qrQuietModules
			if quiet && grid[y][x] {
				t.Fatalf("dark module inside the quiet zone at (%d,%d)", x, y)
			}
			if grid[y][x] != code.Black(x-qrQuietModules, y-qrQuietModules) {
				t.Fatalf("module (%d,%d) does not match the encoded symbol", x, y)
			}
		}
	}

	finder := []string{"#######", "#     #", "# ### #", "# ### #", "# ### #", "#     #", "#######"}
	for _, corner := range [][2]int{
		{qrQuietModules, qrQuietModules},
		{span - qrQuietModules - 7, qrQuietModules},
		{qrQuietModules, span - qrQuietModules - 7},
	} {
		for row := 0; row < 7; row++ {
			for column := 0; column < 7; column++ {
				if grid[corner[1]+row][corner[0]+column] != (finder[row][column] == '#') {
					t.Fatalf("finder pattern at %v is wrong at (%d,%d)", corner, column, row)
				}
			}
		}
	}
}

// decodeQuadrants inverts renderQR: it turns each quadrant-block character
// back into the modules it drew. The mapping is written out here literally,
// not derived from renderQR's own switch, so a renderer that inverts or
// mis-draws a glyph cannot also make its own proof pass.
func decodeHalfBlocks(t *testing.T, rendered string, span int) [][]bool {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
	if len(lines) != (span+1)/2 {
		t.Fatalf("rendered rows = %d, want %d", len(lines), (span+1)/2)
	}
	grid := make([][]bool, len(lines)*2)
	for index := range grid {
		grid[index] = make([]bool, span)
	}
	for row, line := range lines {
		runes := []rune(line)
		if len(runes) != span {
			t.Fatalf("rendered row %d has %d columns, want %d", row, len(runes), span)
		}
		for column, character := range runes {
			// Ink is a LIGHT module, so the DARK grid this returns is the
			// negation of what each glyph draws.
			var upperDark, lowerDark bool
			switch character {
			case '█':
				upperDark, lowerDark = false, false
			case '▀':
				upperDark, lowerDark = false, true
			case '▄':
				upperDark, lowerDark = true, false
			case ' ':
				upperDark, lowerDark = true, true
			default:
				t.Fatalf("rendered row %d column %d is %q, which is not a half block", row, column, character)
			}
			grid[row*2][column] = upperDark
			grid[row*2+1][column] = lowerDark
		}
	}
	return grid[:span]
}

func testInvitation(t *testing.T, challenge, digest string) api.RemoteInvitation {
	t.Helper()
	return api.RemoteInvitation{
		Link: "https://app.darkfactory.build/remote#df_remote" +
			"&node=" + remoteTestNode +
			"&daemon=0123456789abcdef0123456789abcdef" +
			"&challenge=" + challenge +
			"&ticket=dGlja2V0.c2lnbmF0dXJl" +
			"&expires=" + strconv.Itoa(remoteTestExpires),
		NodeID:          remoteTestNode,
		Expires:         remoteTestExpires,
		ChallengeDigest: digest,
	}
}
