//go:build darwin || linux

package api

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

const (
	contractNodeID  = "abcdefghijklmnopqrstuvwxyz234567"
	contractDigest  = "4884fdaafea47c29fea7159d0daddd9c085d6200e1359e85bb81736af6b7c837"
	contractLink    = "https://app.darkfactory.build/remote#df_remote=eyJyZWxheSI6IndzczovL3JlbGF5In0"
	contractRelayWS = "wss://relay.darkfactory.build"
)

func TestRemoteMethodsDecodeAsEmptyOperatorCalls(t *testing.T) {
	bearer := testCredential('R')
	for _, test := range []struct {
		name string
		body string
		kind CallKind
	}{
		{name: "pair", body: `{"method":"remote_pair","params":{}}`, kind: CallRemotePair},
		{name: "status", body: `{"method":"remote_status","params":{}}`, kind: CallRemoteStatus},
	} {
		t.Run(test.name, func(t *testing.T) {
			call, code := decodeCall(operatorDomain, bearer, []byte(test.body))
			if code != "" || call.Kind() != test.kind {
				t.Fatalf("decode = %v, %q", call.Kind(), code)
			}
			// An operator call must never carry an attempt identity, and the
			// no-input operations expose no accessor at all.
			if _, ok := call.AttemptDigest(); ok {
				t.Fatal("remote call exposed an attempt digest")
			}
			if _, ok := call.WebAbandonOpenInput(); ok {
				t.Fatal("remote call decoded a web abandon input")
			}
			if _, code := decodeCall(attemptDomain, bearer, []byte(test.body)); code != RemoteForbidden {
				t.Fatalf("attempt domain = %q, want %s", code, RemoteForbidden)
			}
			// Tolerance is additive: an unknown member reaches no field of a
			// no-input call, while a duplicate name is still refused outright.
			withInput := strings.Replace(test.body, `"params":{}`, `"params":{"node_id":"`+contractNodeID+`"}`, 1)
			if _, code := decodeCall(operatorDomain, bearer, []byte(withInput)); code != "" {
				t.Fatalf("unknown member = %q, want tolerated", code)
			}
			duplicated := strings.Replace(test.body, `"params":{}`, `"params":{"a":1,"a":2}`, 1)
			if _, code := decodeCall(operatorDomain, bearer, []byte(duplicated)); code != RemoteInvalidRequest {
				t.Fatalf("duplicate member = %q, want %s", code, RemoteInvalidRequest)
			}
		})
	}
}

func TestRemoteRepliesArePairedWithTheirOwnCalls(t *testing.T) {
	invitation, err := NewRemoteInvitationReply(RemoteInvitation{Link: contractLink, NodeID: contractNodeID, Expires: 1, ChallengeDigest: contractDigest})
	if err != nil {
		t.Fatal(err)
	}
	status, err := NewRemoteStatusReply(RemoteStatus{NodeID: contractNodeID, RelayOrigin: contractRelayWS})
	if err != nil {
		t.Fatal(err)
	}
	if !replyMatches(CallRemotePair, invitation.kind) || !replyMatches(CallRemoteStatus, status.kind) {
		t.Fatal("remote replies do not match their own calls")
	}
	if replyMatches(CallRemotePair, status.kind) || replyMatches(CallRemoteStatus, invitation.kind) {
		t.Fatal("remote replies are interchangeable across calls")
	}
	if replyMatches(CallWebOpen, invitation.kind) || replyMatches(CallWebStatus, status.kind) {
		t.Fatal("a remote reply satisfies a web call")
	}
	// The redacted projections are the only public rendering of a reply that
	// holds a live pairing challenge and relay ticket.
	if strings.Contains(invitation.String()+invitation.GoString(), "df_remote") {
		t.Fatal("invitation reply formatting exposed the link")
	}
}

func TestRemoteInvitationAndStatusValidationIsStrict(t *testing.T) {
	valid := RemoteInvitation{Link: contractLink, NodeID: contractNodeID, Expires: 1, ChallengeDigest: contractDigest}
	if !validRemoteInvitation(valid) {
		t.Fatal("canonical invitation rejected")
	}
	for name, mutate := range map[string]func(*RemoteInvitation){
		"http link":        func(i *RemoteInvitation) { i.Link = "http://app.darkfactory.build/remote#df_remote=x" },
		"empty link":       func(i *RemoteInvitation) { i.Link = "" },
		"newline in link":  func(i *RemoteInvitation) { i.Link += "\nrm -rf" },
		"uppercase node":   func(i *RemoteInvitation) { i.NodeID = strings.ToUpper(contractNodeID) },
		"base32 excluded":  func(i *RemoteInvitation) { i.NodeID = "0bcdefghijklmnopqrstuvwxyz234567" },
		"short node":       func(i *RemoteInvitation) { i.NodeID = contractNodeID[:31] },
		"zero expiry":      func(i *RemoteInvitation) { i.Expires = 0 },
		"negative expiry":  func(i *RemoteInvitation) { i.Expires = -1 },
		"zero digest":      func(i *RemoteInvitation) { i.ChallengeDigest = strings.Repeat("0", 64) },
		"nonhex digest":    func(i *RemoteInvitation) { i.ChallengeDigest = strings.Repeat("g", 64) },
		"uppercase digest": func(i *RemoteInvitation) { i.ChallengeDigest = strings.ToUpper(contractDigest) },
	} {
		invalid := valid
		mutate(&invalid)
		if validRemoteInvitation(invalid) {
			t.Fatalf("invalid invitation accepted: %s", name)
		}
		if _, err := NewRemoteInvitationReply(invalid); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid invitation reply constructed: %s: %v", name, err)
		}
	}

	connected := RemoteStatus{NodeID: contractNodeID, RelayOrigin: contractRelayWS, Connected: true, Sessions: 3}
	if !validRemoteStatus(connected) || !validRemoteStatus(RemoteStatus{NodeID: contractNodeID, RelayOrigin: "ws://127.0.0.1:8787"}) {
		t.Fatal("canonical remote status rejected")
	}
	for name, invalid := range map[string]RemoteStatus{
		"missing node":         {RelayOrigin: contractRelayWS},
		"https origin":         {NodeID: contractNodeID, RelayOrigin: "https://relay.darkfactory.build"},
		"empty origin":         {NodeID: contractNodeID},
		"wildcard origin":      {NodeID: contractNodeID, RelayOrigin: "wss://*"},
		"negative sessions":    {NodeID: contractNodeID, RelayOrigin: contractRelayWS, Connected: true, Sessions: -1},
		"sessions while down":  {NodeID: contractNodeID, RelayOrigin: contractRelayWS, Sessions: 1},
		"space in relay":       {NodeID: contractNodeID, RelayOrigin: "wss://relay a"},
		"node id is hex-wide":  {NodeID: "0123456789abcdef0123456789abcdef", RelayOrigin: contractRelayWS},
		"node id has padding":  {NodeID: "abcdefghijklmnopqrstuvwxyz23456=", RelayOrigin: contractRelayWS},
		"origin without proto": {NodeID: contractNodeID, RelayOrigin: "relay.darkfactory.build"},
	} {
		if validRemoteStatus(invalid) {
			t.Fatalf("invalid remote status accepted: %s", name)
		}
		if _, err := NewRemoteStatusReply(invalid); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid remote status reply constructed: %s: %v", name, err)
		}
	}
}

func TestRemoteClientUsesExactWireAndRefusesInexactReplies(t *testing.T) {
	invitationBody := `{"link":"` + contractLink + `","node_id":"` + contractNodeID + `","expires":1757000000,"challenge_digest":"` + contractDigest + `"}`
	statusBody := `{"node_id":"` + contractNodeID + `","relay_origin":"` + contractRelayWS + `","connected":true,"sessions":2}`
	for _, test := range []struct {
		name     string
		response string
		request  string
		call     func(*OperatorClient) (any, error)
		wantErr  error
	}{
		{
			name: "pair", response: successResponse(invitationBody), request: `{"method":"remote_pair","params":{}}`,
			call: func(client *OperatorClient) (any, error) { return client.RemotePair(context.Background()) },
		},
		{
			name: "pair refuses an insecure link", response: successResponse(`{"link":"http://app.darkfactory.build/remote#df_remote=x","node_id":"` + contractNodeID + `","expires":1,"challenge_digest":"` + contractDigest + `"}`),
			request: `{"method":"remote_pair","params":{}}`, wantErr: ErrProtocol,
			call: func(client *OperatorClient) (any, error) { return client.RemotePair(context.Background()) },
		},
		{
			name: "status", response: successResponse(statusBody), request: `{"method":"remote_status","params":{}}`,
			call: func(client *OperatorClient) (any, error) { return client.RemoteStatus(context.Background()) },
		},
		{
			name: "status refuses sessions without a connection", response: successResponse(`{"node_id":"` + contractNodeID + `","relay_origin":"` + contractRelayWS + `","connected":false,"sessions":2}`),
			request: `{"method":"remote_status","params":{}}`, wantErr: ErrProtocol,
			call: func(client *OperatorClient) (any, error) { return client.RemoteStatus(context.Background()) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			bearer := testCredential('R')
			fixture := newWireFixture(t, bearer, func(connection net.Conn, _ []byte) error {
				return writeTestResponse(connection, wireOperatorDomain, test.response)
			})
			client, err := NewOperatorClient(fixture.socket, fixture.token)
			if err != nil {
				t.Fatal(err)
			}
			result, callErr := test.call(client)
			if test.wantErr != nil {
				if !errors.Is(callErr, test.wantErr) {
					t.Fatalf("call error = %v, want %v", callErr, test.wantErr)
				}
				// A refused reply yields nothing at all: there is no cleanup
				// identity a caller could act on, unlike a web launch.
				switch value := result.(type) {
				case RemoteInvitation:
					if value != (RemoteInvitation{}) {
						t.Fatalf("refused invitation retained %+v", value)
					}
				case RemoteStatus:
					if value != (RemoteStatus{}) {
						t.Fatalf("refused status retained %+v", value)
					}
				default:
					t.Fatalf("unexpected result %T", result)
				}
			} else if callErr != nil {
				t.Fatal(callErr)
			}
			if got := requestJSON(t, <-fixture.request, wireOperatorDomain, bearer); got != test.request {
				t.Fatalf("request = %s, want %s", got, test.request)
			}
			fixture.wait(t)
		})
	}
}
