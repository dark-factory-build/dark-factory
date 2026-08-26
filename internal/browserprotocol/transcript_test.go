package browserprotocol

import (
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type fixture struct {
	Pair fixturePair `json:"pair"`
	Auth fixtureAuth `json:"auth"`
}
type fixturePair struct {
	DaemonID        string `json:"daemon_id"`
	BootID          string `json:"boot_id"`
	ConnectionNonce string `json:"connection_nonce"`
	Challenge       string `json:"challenge"`
	PublicKeySEC1   string `json:"public_key_sec1"`
	Host            string `json:"host"`
	Origin          string `json:"origin"`
	Transcript      string `json:"transcript"`
	Signature       string `json:"signature"`
}
type fixtureAuth struct {
	DaemonID        string `json:"daemon_id"`
	BootID          string `json:"boot_id"`
	ConnectionNonce string `json:"connection_nonce"`
	ClientID        string `json:"client_id"`
	Host            string `json:"host"`
	Origin          string `json:"origin"`
	Transcript      string `json:"transcript"`
}

func readFixture(t *testing.T) fixture {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(source), "..", "..", "protocol", "browser", "v1", "fixtures", "transcript_v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value fixture
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}
func decodeFixtureHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
func pairFromFixture(t *testing.T, value fixturePair) PairTranscript {
	t.Helper()
	return PairTranscript{DaemonID: decodeFixtureHex(t, value.DaemonID), BootID: decodeFixtureHex(t, value.BootID), ConnectionNonce: decodeFixtureHex(t, value.ConnectionNonce), Challenge: decodeFixtureHex(t, value.Challenge), PublicKeySEC1: decodeFixtureHex(t, value.PublicKeySEC1), ValidatedHost: value.Host, ValidatedOrigin: value.Origin}
}
func authFromFixture(t *testing.T, value fixtureAuth) AuthTranscript {
	t.Helper()
	return AuthTranscript{DaemonID: decodeFixtureHex(t, value.DaemonID), BootID: decodeFixtureHex(t, value.BootID), ConnectionNonce: decodeFixtureHex(t, value.ConnectionNonce), ClientID: decodeFixtureHex(t, value.ClientID), ValidatedHost: value.Host, ValidatedOrigin: value.Origin}
}

func TestTranscriptFixturesRoundTripAndVerify(t *testing.T) {
	value := readFixture(t)
	pair := pairFromFixture(t, value.Pair)
	gotPair, err := BuildPairTranscript(pair)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotPair) != string(decodeFixtureHex(t, value.Pair.Transcript)) {
		t.Fatalf("pair transcript changed: got %x", gotPair)
	}
	if err := VerifySignature(pair.PublicKeySEC1, decodeFixtureHex(t, value.Pair.Signature), gotPair); err != nil {
		t.Fatalf("valid pair signature rejected: %v", err)
	}
	auth, err := BuildAuthTranscript(authFromFixture(t, value.Auth))
	if err != nil {
		t.Fatal(err)
	}
	if string(auth) != string(decodeFixtureHex(t, value.Auth.Transcript)) {
		t.Fatalf("auth transcript changed: got %x", auth)
	}
}

func TestTranscriptMutationCannotVerify(t *testing.T) {
	value := readFixture(t)
	input := pairFromFixture(t, value.Pair)
	transcript, err := BuildPairTranscript(input)
	if err != nil {
		t.Fatal(err)
	}
	signature := decodeFixtureHex(t, value.Pair.Signature)
	for _, index := range []int{0, len(pairDomain) - 1, len(pairDomain), len(pairDomain) + 1, len(pairDomain) + 2, len(pairDomain) + 3, len(transcript) - 1} {
		mutated := append([]byte(nil), transcript...)
		mutated[index] ^= 1
		if err := VerifySignature(input.PublicKeySEC1, signature, mutated); err == nil {
			t.Fatalf("mutation at transcript byte %d still verified", index)
		}
	}
	for name, mutate := range map[string]func(*PairTranscript){
		"daemon": func(v *PairTranscript) { v.DaemonID[0] ^= 1 }, "boot": func(v *PairTranscript) { v.BootID[0] ^= 1 }, "nonce": func(v *PairTranscript) { v.ConnectionNonce[0] ^= 1 }, "challenge": func(v *PairTranscript) { v.Challenge[0] ^= 1 }, "key": func(v *PairTranscript) { v.PublicKeySEC1[1] ^= 1 }, "host": func(v *PairTranscript) { v.ValidatedHost = "127.0.0.1:43124" }, "origin": func(v *PairTranscript) { v.ValidatedOrigin = "https://evil.example" },
	} {
		t.Run(name, func(t *testing.T) {
			mutated := input
			mutated.DaemonID = append([]byte(nil), input.DaemonID...)
			mutated.BootID = append([]byte(nil), input.BootID...)
			mutated.ConnectionNonce = append([]byte(nil), input.ConnectionNonce...)
			mutated.Challenge = append([]byte(nil), input.Challenge...)
			mutated.PublicKeySEC1 = append([]byte(nil), input.PublicKeySEC1...)
			mutate(&mutated)
			changed, err := BuildPairTranscript(mutated)
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifySignature(input.PublicKeySEC1, signature, changed); err == nil {
				t.Fatal("mutated input still verified")
			}
		})
	}
}

func TestTextValidation(t *testing.T) {
	base := pairFromFixture(t, readFixture(t).Pair)
	cases := []struct {
		name, value string
		origin      bool
	}{
		{"empty host", "", false}, {"empty origin", "", true},
		{"NUL host", "127.0.0.1\x00:43123", false}, {"NUL origin", "https://app.darkfactory.build\x00", true},
		{"invalid UTF-8 host", string([]byte{0xff}), false}, {"invalid UTF-8 origin", string([]byte{0xff}), true},
		{"oversized host", string(make([]byte, MaxTextBytes+1)), false}, {"oversized origin", string(make([]byte, MaxTextBytes+1)), true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := base
			if test.origin {
				input.ValidatedOrigin = test.value
			} else {
				input.ValidatedHost = test.value
			}
			if _, err := BuildPairTranscript(input); !errors.Is(err, ErrInvalidText) {
				t.Fatalf("error = %v, want ErrInvalidText", err)
			}
		})
	}
}

func TestFixedFieldLengths(t *testing.T) {
	base := pairFromFixture(t, readFixture(t).Pair)
	cases := []struct {
		name string
		edit func(*PairTranscript)
	}{{"daemon", func(v *PairTranscript) { v.DaemonID = v.DaemonID[:1] }}, {"boot", func(v *PairTranscript) { v.BootID = v.BootID[:1] }}, {"nonce", func(v *PairTranscript) { v.ConnectionNonce = v.ConnectionNonce[:1] }}, {"challenge", func(v *PairTranscript) { v.Challenge = v.Challenge[:1] }}, {"public key", func(v *PairTranscript) { v.PublicKeySEC1 = v.PublicKeySEC1[:1] }}}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			if _, err := BuildPairTranscript(input); !errors.Is(err, ErrInvalidLength) {
				t.Fatalf("error = %v, want ErrInvalidLength", err)
			}
		})
	}
}

func TestAuthFixedFieldLengths(t *testing.T) {
	value := readFixture(t)
	base := authFromFixture(t, value.Auth)
	cases := []struct {
		name string
		edit func(*AuthTranscript)
	}{
		{"daemon", func(v *AuthTranscript) { v.DaemonID = v.DaemonID[:1] }},
		{"boot", func(v *AuthTranscript) { v.BootID = v.BootID[:1] }},
		{"nonce", func(v *AuthTranscript) { v.ConnectionNonce = v.ConnectionNonce[:1] }},
		{"client", func(v *AuthTranscript) { v.ClientID = v.ClientID[:1] }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.edit(&input)
			if _, err := BuildAuthTranscript(input); !errors.Is(err, ErrInvalidLength) {
				t.Fatalf("error = %v, want ErrInvalidLength", err)
			}
		})
	}
}

func TestPublicKeyAndSignatureEncoding(t *testing.T) {
	value := readFixture(t)
	pair := pairFromFixture(t, value.Pair)
	transcript := decodeFixtureHex(t, value.Pair.Transcript)
	signature := decodeFixtureHex(t, value.Pair.Signature)
	cases := []struct {
		name     string
		key, sig []byte
	}{
		{"short key", pair.PublicKeySEC1[:64], signature}, {"compressed key", append([]byte{2}, pair.PublicKeySEC1[1:33]...), signature}, {"off curve key", append([]byte{4}, make([]byte, 64)...), signature}, {"short signature", pair.PublicKeySEC1, signature[:63]}, {"long signature", pair.PublicKeySEC1, append(signature, 0)}, {"zero r", pair.PublicKeySEC1, append(make([]byte, 32), signature[32:]...)}, {"zero s", pair.PublicKeySEC1, append(append([]byte(nil), signature[:32]...), make([]byte, 32)...)}, {"ASN.1", pair.PublicKeySEC1, []byte{0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := VerifySignature(test.key, test.sig, transcript); err == nil {
				t.Fatal("malformed encoding verified")
			}
		})
	}
	order := elliptic.P256().Params().N.Bytes()
	for _, name := range []string{"r >= N", "s >= N"} {
		t.Run(name, func(t *testing.T) {
			bad := make([]byte, SignatureSize)
			if name[0] == 'r' {
				copy(bad[32-len(order):32], order)
				copy(bad[32:], signature[32:])
			} else {
				copy(bad, signature[:32])
				copy(bad[64-len(order):], order)
			}
			if err := VerifySignature(pair.PublicKeySEC1, bad, transcript); err == nil {
				t.Fatal("out-of-range scalar verified")
			}
		})
	}
}

func TestTranscriptIsHashedExactlyOnce(t *testing.T) {
	value := readFixture(t)
	pair := pairFromFixture(t, value.Pair)
	transcript := decodeFixtureHex(t, value.Pair.Transcript)
	signature := decodeFixtureHex(t, value.Pair.Signature)
	first := sha256.Sum256(transcript)
	second := sha256.Sum256(first[:])
	doubleHashedSignature := signForHash(big.NewInt(1), big.NewInt(2), second[:])
	if err := VerifySignature(pair.PublicKeySEC1, doubleHashedSignature, transcript); err == nil {
		t.Fatal("double-hashed signature verified")
	}
	if err := VerifySignature(pair.PublicKeySEC1, signature, transcript); err != nil {
		t.Fatalf("single-hashed fixture rejected: %v", err)
	}
}

func signForHash(private, nonce *big.Int, digest []byte) []byte {
	curve := elliptic.P256()
	x, _ := curve.ScalarBaseMult(nonce.Bytes())
	r := new(big.Int).Mod(x, curve.Params().N)
	s := new(big.Int).Mul(r, private)
	s.Add(s, new(big.Int).SetBytes(digest))
	s.Mul(s, new(big.Int).ModInverse(nonce, curve.Params().N))
	s.Mod(s, curve.Params().N)
	sig := make([]byte, SignatureSize)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return sig
}
