package relayhost

// relay/fixtures/tokens.json is the shared vector fixture also consumed by
// relay/tests/tokens.vectors.test.mjs on the TypeScript side, so the Go and
// Worker implementations of the token formats documented in relay/README.md
// cannot drift apart unnoticed. This file both regenerates that fixture (only
// with -update, from fixed inputs) and verifies it on every normal test run.
//
// The controller proof is the one piece this package has no production code
// for at all: proofs are signed by a browser's device key and verified only
// by the relay Worker, never by factoryd. It is minted here with the stdlib
// directly, from the fixture's fixed P-256 scalar, purely so the fixture has
// one to carry. ECDSA signing is randomized, so unlike the Ed25519 host token
// and tickets, the proof cannot be re-minted byte-for-byte on every run: it is
// generated once and only ever re-verified afterwards.

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

var updateVectors = flag.Bool("update", false, "regenerate relay/fixtures/tokens.json from the fixed vector inputs")

// Relative to this package's directory, which is where `go test` runs.
const vectorsPath = "../../relay/fixtures/tokens.json"

const proofDomain = "dark-factory-relay/proof\n"

type tokenFixture struct {
	Seed          string        `json:"seed"`
	PublicKey     string        `json:"publicKey"`
	NodeID        string        `json:"nodeId"`
	Generation    uint64        `json:"generation"`
	HostToken     hostFixture   `json:"hostToken"`
	PairTicket    ticketFixture `json:"pairTicket"`
	ControlTicket ticketFixture `json:"controlTicket"`
	Device        deviceFixture `json:"device"`
	Proof         proofFixture  `json:"proof"`
}

type hostFixture struct {
	Sequence    uint64 `json:"sequence"`
	Issued      int64  `json:"issued"`
	PayloadText string `json:"payloadText"`
	Token       string `json:"token"`
}

type ticketFixture struct {
	Controller  string `json:"controller"`
	Device      string `json:"device,omitempty"`
	Purpose     string `json:"purpose"`
	TicketID    string `json:"ticketId"`
	Expires     int64  `json:"expires"`
	PayloadText string `json:"payloadText"`
	Token       string `json:"token"`
}

type deviceFixture struct {
	PrivateScalar string `json:"privateScalar"`
	Point         string `json:"point"`
}

type proofFixture struct {
	TicketID    string `json:"ticketId"`
	Issued      int64  `json:"issued"`
	Nonce       string `json:"nonce"`
	PayloadText string `json:"payloadText"`
	Token       string `json:"token"`
}

// fixedBytes fills a slice deterministically so every input in the fixture is
// reproducible without being all zeroes.
func fixedBytes(n, start int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(start + i)
	}
	return out
}

func payloadTextOf(t *testing.T, token string) string {
	t.Helper()
	separator := strings.IndexByte(token, '.')
	if separator <= 0 {
		t.Fatalf("token %q is not payload.signature", token)
	}
	return token[:separator]
}

func TestWriteTokenVectors(t *testing.T) {
	if !*updateVectors {
		t.Skip("run with -run TestWriteTokenVectors -update to regenerate relay/fixtures/tokens.json")
	}

	seed := fixedBytes(ed25519.SeedSize, 0x00)
	private := ed25519.NewKeyFromSeed(seed)
	public := private.Public().(ed25519.PublicKey)
	nodeID := NodeIDFromPublicKey(public)
	generation := uint64(1_800_000_000)
	identity := Identity{private: private, nodeID: nodeID, generation: generation}

	sequence := uint64(7)
	issued := time.Unix(1_800_000_500, 0)
	hostToken := HostToken(identity, sequence, issued)
	if hostToken == "" {
		t.Fatal("HostToken produced an empty token")
	}

	farFuture := time.Unix(4_102_444_800, 0) // 2100-01-01T00:00:00Z: never expires in any CI run.

	var pairController [ControllerIDSize]byte
	copy(pairController[:], fixedBytes(ControllerIDSize, 0x10))
	var pairTicketID [TicketIDSize]byte
	copy(pairTicketID[:], fixedBytes(TicketIDSize, 0x30))
	pairPayload := TicketPayload{
		Node:       nodeID,
		Controller: base64.RawURLEncoding.EncodeToString(pairController[:]),
		Purpose:    PurposePair,
		Ticket:     base64.RawURLEncoding.EncodeToString(pairTicketID[:]),
		Expires:    farFuture.Unix(),
	}
	pairToken := sign(identity, ticketDomain, pairPayload)
	if pairToken == "" {
		t.Fatal("pair sign produced an empty token")
	}

	// A fixed P-256 device key, built directly with the stdlib: relayhost has
	// no device-key type of its own, since a device key belongs to the browser.
	curve := elliptic.P256()
	scalar := new(big.Int).SetBytes(fixedBytes(32, 0x40))
	x, y := curve.ScalarBaseMult(scalar.Bytes())
	devicePoint := make([]byte, DeviceKeySize)
	devicePoint[0] = 0x04
	x.FillBytes(devicePoint[1:33])
	y.FillBytes(devicePoint[33:65])
	var deviceArray [DeviceKeySize]byte
	copy(deviceArray[:], devicePoint)

	var controlController [ControllerIDSize]byte
	copy(controlController[:], fixedBytes(ControllerIDSize, 0x20))
	var controlTicketID [TicketIDSize]byte
	copy(controlTicketID[:], fixedBytes(TicketIDSize, 0x50))
	controlPayload := TicketPayload{
		Node:       nodeID,
		Controller: base64.RawURLEncoding.EncodeToString(controlController[:]),
		Device:     base64.RawURLEncoding.EncodeToString(deviceArray[:]),
		Purpose:    PurposeControl,
		Ticket:     base64.RawURLEncoding.EncodeToString(controlTicketID[:]),
		Expires:    farFuture.Unix(),
	}
	controlToken := sign(identity, ticketDomain, controlPayload)
	if controlToken == "" {
		t.Fatal("control sign produced an empty token")
	}

	proofPayload := struct {
		Ticket string `json:"ticket"`
		Issued int64  `json:"issued"`
		Nonce  string `json:"nonce"`
	}{
		Ticket: controlPayload.Ticket,
		Issued: issued.Unix(),
		Nonce:  base64.RawURLEncoding.EncodeToString(fixedBytes(TicketIDSize, 0x60)),
	}
	proofEncoded, err := json.Marshal(proofPayload)
	if err != nil {
		t.Fatal(err)
	}
	proofPayloadText := base64.RawURLEncoding.EncodeToString(proofEncoded)
	devicePrivate := &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y}, D: scalar}
	signed := append([]byte(proofDomain), proofPayloadText...)
	hashed := sha256.Sum256(signed)
	r, s, err := ecdsa.Sign(rand.Reader, devicePrivate, hashed[:])
	if err != nil {
		t.Fatal(err)
	}
	proofSignature := make([]byte, 64)
	r.FillBytes(proofSignature[:32])
	s.FillBytes(proofSignature[32:64])
	proofToken := proofPayloadText + "." + base64.RawURLEncoding.EncodeToString(proofSignature)

	fixture := tokenFixture{
		Seed:       base64.RawURLEncoding.EncodeToString(seed),
		PublicKey:  base64.RawURLEncoding.EncodeToString(public),
		NodeID:     nodeID,
		Generation: generation,
		HostToken: hostFixture{
			Sequence:    sequence,
			Issued:      issued.Unix(),
			PayloadText: payloadTextOf(t, hostToken),
			Token:       hostToken,
		},
		PairTicket: ticketFixture{
			Controller:  pairPayload.Controller,
			Purpose:     pairPayload.Purpose,
			TicketID:    pairPayload.Ticket,
			Expires:     pairPayload.Expires,
			PayloadText: payloadTextOf(t, pairToken),
			Token:       pairToken,
		},
		ControlTicket: ticketFixture{
			Controller:  controlPayload.Controller,
			Device:      controlPayload.Device,
			Purpose:     controlPayload.Purpose,
			TicketID:    controlPayload.Ticket,
			Expires:     controlPayload.Expires,
			PayloadText: payloadTextOf(t, controlToken),
			Token:       controlToken,
		},
		Device: deviceFixture{
			PrivateScalar: base64.RawURLEncoding.EncodeToString(fixedBytes(32, 0x40)),
			Point:         base64.RawURLEncoding.EncodeToString(deviceArray[:]),
		},
		Proof: proofFixture{
			TicketID:    proofPayload.Ticket,
			Issued:      proofPayload.Issued,
			Nonce:       proofPayload.Nonce,
			PayloadText: payloadTextOf(t, proofToken),
			Token:       proofToken,
		},
	}

	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(vectorsPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTokenVectorsMatchFixture(t *testing.T) {
	data, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("relay/fixtures/tokens.json: %v (run go test ./internal/relayhost -run TestWriteTokenVectors -update)", err)
	}
	var fixture tokenFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}

	seed, err := base64.RawURLEncoding.DecodeString(fixture.Seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("fixture seed: %v", err)
	}
	private := ed25519.NewKeyFromSeed(seed)
	public := private.Public().(ed25519.PublicKey)
	recordedPublic, err := base64.RawURLEncoding.DecodeString(fixture.PublicKey)
	if err != nil || !bytes.Equal(public, recordedPublic) {
		t.Fatalf("fixture public key does not match the key derived from the fixture seed")
	}
	if got := NodeIDFromPublicKey(public); got != fixture.NodeID {
		t.Fatalf("NodeIDFromPublicKey = %q, fixture recorded %q", got, fixture.NodeID)
	}
	identity := Identity{private: private, nodeID: fixture.NodeID, generation: fixture.Generation}

	// Host token: VerifyHostToken accepts it with the recorded fields, and
	// re-minting from the same inputs reproduces the exact recorded text.
	hostPayload, err := VerifyHostToken(public, fixture.HostToken.Token)
	if err != nil {
		t.Fatalf("VerifyHostToken: %v", err)
	}
	if hostPayload.Node != fixture.NodeID || hostPayload.Key != fixture.PublicKey ||
		hostPayload.Generation != fixture.Generation || hostPayload.Sequence != fixture.HostToken.Sequence ||
		hostPayload.Issued != fixture.HostToken.Issued {
		t.Fatalf("host payload = %+v, want the fixture's recorded fields", hostPayload)
	}
	if payloadTextOf(t, fixture.HostToken.Token) != fixture.HostToken.PayloadText {
		t.Fatal("fixture host token's payload text does not match its own token")
	}
	if remint := HostToken(identity, fixture.HostToken.Sequence, time.Unix(fixture.HostToken.Issued, 0)); remint != fixture.HostToken.Token {
		t.Fatalf("HostToken is not byte-for-byte reproducible:\n got  %q\n want %q", remint, fixture.HostToken.Token)
	}

	// Pair ticket: same two proofs, reconstructing the payload sign() saw.
	pairPayload := TicketPayload{
		Node:       fixture.NodeID,
		Controller: fixture.PairTicket.Controller,
		Purpose:    fixture.PairTicket.Purpose,
		Ticket:     fixture.PairTicket.TicketID,
		Expires:    fixture.PairTicket.Expires,
	}
	if remint := sign(identity, ticketDomain, pairPayload); remint != fixture.PairTicket.Token {
		t.Fatalf("pair ticket is not byte-for-byte reproducible:\n got  %q\n want %q", remint, fixture.PairTicket.Token)
	}
	pairVerified, err := VerifyTicket(public, fixture.PairTicket.Token)
	if err != nil {
		t.Fatalf("VerifyTicket(pair): %v", err)
	}
	if pairVerified.Purpose != PurposePair || pairVerified.Device != "" ||
		pairVerified.Controller != fixture.PairTicket.Controller || pairVerified.Ticket != fixture.PairTicket.TicketID ||
		pairVerified.Expires != fixture.PairTicket.Expires {
		t.Fatalf("pair ticket payload = %+v, want the fixture's recorded fields", pairVerified)
	}

	// Control ticket: same, plus the bound device key.
	controlPayload := TicketPayload{
		Node:       fixture.NodeID,
		Controller: fixture.ControlTicket.Controller,
		Device:     fixture.ControlTicket.Device,
		Purpose:    fixture.ControlTicket.Purpose,
		Ticket:     fixture.ControlTicket.TicketID,
		Expires:    fixture.ControlTicket.Expires,
	}
	if remint := sign(identity, ticketDomain, controlPayload); remint != fixture.ControlTicket.Token {
		t.Fatalf("control ticket is not byte-for-byte reproducible:\n got  %q\n want %q", remint, fixture.ControlTicket.Token)
	}
	controlVerified, err := VerifyTicket(public, fixture.ControlTicket.Token)
	if err != nil {
		t.Fatalf("VerifyTicket(control): %v", err)
	}
	if controlVerified.Purpose != PurposeControl || controlVerified.Device != fixture.Device.Point ||
		controlVerified.Controller != fixture.ControlTicket.Controller || controlVerified.Ticket != fixture.ControlTicket.TicketID ||
		controlVerified.Expires != fixture.ControlTicket.Expires {
		t.Fatalf("control ticket payload = %+v, want the fixture's recorded fields", controlVerified)
	}

	// A tampered signature is refused, on both a host token and a ticket.
	if _, err := VerifyHostToken(public, flipSignatureByte(fixture.HostToken.Token)); !errors.Is(err, ErrToken) {
		t.Fatalf("tampered host token = %v, want ErrToken", err)
	}
	if _, err := VerifyTicket(public, flipSignatureByte(fixture.ControlTicket.Token)); !errors.Is(err, ErrToken) {
		t.Fatalf("tampered control ticket = %v, want ErrToken", err)
	}
}

// flipSignatureByte mirrors relay/tests/helpers.mjs's corrupt(): flip the
// first character of the signature half, leaving the token well-formed.
func flipSignatureByte(token string) string {
	separator := strings.IndexByte(token, '.')
	payload, signature := token[:separator+1], token[separator+1:]
	flipped := byte('A')
	if signature[0] == 'A' {
		flipped = 'B'
	}
	return payload + string(flipped) + signature[1:]
}
