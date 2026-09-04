// Package browserprotocol contains the small, framework-neutral cryptographic
// pieces shared by the browser transport and its clients.
package browserprotocol

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"unicode/utf8"
)

const (
	ProtocolVersion = uint16(2)
	DaemonIDSize    = 16
	BootIDSize      = 16
	NonceSize       = 32
	ChallengeSize   = 32
	ClientIDSize    = 16
	PublicKeySize   = 65
	SignatureSize   = 64
	MaxTextBytes    = 4096
)

var (
	ErrInvalidLength    = errors.New("browser protocol: invalid field length")
	ErrInvalidText      = errors.New("browser protocol: invalid host or origin")
	ErrInvalidPublicKey = errors.New("browser protocol: invalid P-256 public key")
	ErrInvalidSignature = errors.New("browser protocol: invalid ECDSA signature")
)

// PairTranscript contains the values bound by a pairing proof. Byte slices
// are used deliberately: callers crossing the wire must be checked for the
// exact fixed lengths instead of being able to construct a malformed array.
type PairTranscript struct {
	DaemonID        []byte
	BootID          []byte
	ConnectionNonce []byte
	Challenge       []byte
	PublicKeySEC1   []byte
	ValidatedHost   string
	ValidatedOrigin string
}

// AuthTranscript contains the values bound by an existing-client proof.
type AuthTranscript struct {
	DaemonID        []byte
	BootID          []byte
	ConnectionNonce []byte
	ClientID        []byte
	ValidatedHost   string
	ValidatedOrigin string
}

const (
	pairDomain = "dark-factory/browser/v2/pair\x00"
	authDomain = "dark-factory/browser/v2/auth\x00"
)

// BuildPairTranscript returns the exact bytes signed by a browser while
// pairing. The domain is intentionally unlength-prefixed; every field after
// the version has a u32 big-endian byte length.
func BuildPairTranscript(input PairTranscript) ([]byte, error) {
	return buildTranscript(pairDomain, [][]byte{
		input.DaemonID,
		input.BootID,
		input.ConnectionNonce,
		input.Challenge,
		input.PublicKeySEC1,
		[]byte(input.ValidatedHost),
		[]byte(input.ValidatedOrigin),
	}, []int{DaemonIDSize, BootIDSize, NonceSize, ChallengeSize, PublicKeySize, 0, 0})
}

// BuildAuthTranscript returns the exact bytes signed by a browser while
// authenticating an existing client.
func BuildAuthTranscript(input AuthTranscript) ([]byte, error) {
	return buildTranscript(authDomain, [][]byte{
		input.DaemonID,
		input.BootID,
		input.ConnectionNonce,
		input.ClientID,
		[]byte(input.ValidatedHost),
		[]byte(input.ValidatedOrigin),
	}, []int{DaemonIDSize, BootIDSize, NonceSize, ClientIDSize, 0, 0})
}

func buildTranscript(domain string, fields [][]byte, fixed []int) ([]byte, error) {
	if len(fields) != len(fixed) {
		return nil, fmt.Errorf("%w: field count", ErrInvalidLength)
	}
	result := make([]byte, 0, len(domain)+2+len(fields)*4)
	result = append(result, domain...)
	var version [2]byte
	binary.BigEndian.PutUint16(version[:], ProtocolVersion)
	result = append(result, version[:]...)
	for i, field := range fields {
		if fixed[i] != 0 && len(field) != fixed[i] {
			return nil, fmt.Errorf("%w: field %d is %d bytes, want %d", ErrInvalidLength, i, len(field), fixed[i])
		}
		if fixed[i] == 0 {
			if err := validateText(field); err != nil {
				return nil, fmt.Errorf("field %d: %w", i, err)
			}
		}
		if uint64(len(field)) > uint64(^uint32(0)) {
			return nil, fmt.Errorf("%w: field %d exceeds u32", ErrInvalidLength, i)
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(field)))
		result = append(result, length[:]...)
		result = append(result, field...)
	}
	return result, nil
}

func validateText(value []byte) error {
	if len(value) == 0 || len(value) > MaxTextBytes || !utf8.Valid(value) {
		return ErrInvalidText
	}
	for _, b := range value {
		if b == 0 {
			return ErrInvalidText
		}
	}
	return nil
}

// VerifySignature verifies an IEEE-P1363 P-256 signature over transcript.
// The complete transcript is hashed exactly once here. ASN.1 and alternate
// public-key encodings are intentionally not accepted.
func VerifySignature(publicKeySEC1, signature, transcript []byte) error {
	if len(signature) != SignatureSize {
		return fmt.Errorf("%w: signature is %d bytes, want %d", ErrInvalidSignature, len(signature), SignatureSize)
	}
	if len(publicKeySEC1) != PublicKeySize || publicKeySEC1[0] != 4 {
		return fmt.Errorf("%w: require 65-byte uncompressed SEC1 point", ErrInvalidPublicKey)
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), publicKeySEC1)
	if x == nil || y == nil || !elliptic.P256().IsOnCurve(x, y) {
		return ErrInvalidPublicKey
	}
	curve := elliptic.P256()
	order := curve.Params().N
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	if r.Sign() <= 0 || r.Cmp(order) >= 0 || s.Sign() <= 0 || s.Cmp(order) >= 0 {
		return ErrInvalidSignature
	}
	digest := sha256.Sum256(transcript)
	if !ecdsa.Verify(&ecdsa.PublicKey{Curve: curve, X: x, Y: y}, digest[:], r, s) {
		return ErrInvalidSignature
	}
	return nil
}
