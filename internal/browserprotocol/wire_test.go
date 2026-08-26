package browserprotocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixturePath(name string) string {
	return filepath.Join("..", "..", "protocol", "browser", "v1", "fixtures", name)
}

func fixtureBytes(t *testing.T, name string) []byte {
	t.Helper()
	value, err := os.ReadFile(fixturePath(name))
	if err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSpace(value)
}

func TestControlFixturesRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		body func() ([]byte, error)
	}{
		{"hello", func() ([]byte, error) {
			return EncodeHello(Hello{DaemonID: "000102030405060708090a0b0c0d0e0f", BootID: "101112131415161718191a1b1c1d1e1f", ConnectionNonce: "202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f"})
		}},
		{"pair_prove", func() ([]byte, error) {
			return EncodePairProve("pair-1", PairProve{Challenge: "404142434445464748494a4b4c4d4e4f505152535455565758595a5b5c5d5e5f", PublicKeySEC1: "046b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c2964fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f5", Signature: "7cf27b188d034f7e8a52380304b51ac3c08969e277f21b35a60b48fc47669978590880bbb870c3d990948ae0a125780fb7d3551078d00dd7d2effd32273fd853"})
		}},
		{"pair_result", func() ([]byte, error) {
			return EncodePairResult("pair-1", PairResult{ClientID: "606162636465666768696a6b6c6d6e6f", Capabilities: 15})
		}},
		{"auth_prove", func() ([]byte, error) {
			return EncodeAuthProve("auth-1", AuthProve{ClientID: "606162636465666768696a6b6c6d6e6f", PublicKeySEC1: "046b17d1f2e12c4247f8bce6e563a440f277037d812deb33a0f4a13945d898c2964fe342e2fe1a7f9b8ee7eb4a7c0f9e162bce33576b315ececbb6406837bf51f5", Signature: "7cf27b188d034f7e8a52380304b51ac3c08969e277f21b35a60b48fc476699787e2963d01c3c5aa8d7cbb5fcf666c7a16e892b0e889805d2a547e76b4450ee80"})
		}},
		{"auth_result", func() ([]byte, error) {
			return EncodeAuthResult("auth-1", AuthResult{ClientID: "606162636465666768696a6b6c6d6e6f", Capabilities: 9})
		}},
		{"error", func() ([]byte, error) { return EncodeError("", Error{Code: ErrorUnauthorized}) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := test.body()
			if err != nil {
				t.Fatal(err)
			}
			want := fixtureBytes(t, test.name+".json")
			if !bytes.Equal(got, want) {
				t.Fatalf("fixture drift:\n got %s\nwant %s", got, want)
			}
			decoded, err := DecodeControl(got)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Version != 1 {
				t.Fatalf("version %d", decoded.Version)
			}
			again, err := encodeDecoded(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(again, want) {
				t.Fatalf("round-trip drift:\n got %s\nwant %s", again, want)
			}
		})
	}
}

func encodeDecoded(frame ControlFrame) ([]byte, error) {
	switch value := frame.Body.(type) {
	case Hello:
		return EncodeHello(value)
	case PairProve:
		return EncodePairProve(frame.ID, value)
	case PairResult:
		return EncodePairResult(frame.ID, value)
	case AuthProve:
		return EncodeAuthProve(frame.ID, value)
	case AuthResult:
		return EncodeAuthResult(frame.ID, value)
	case Error:
		return EncodeError(frame.ID, value)
	default:
		return nil, errors.New("unknown decoded body")
	}
}

func TestBinaryFixturesRoundTrip(t *testing.T) {
	for _, name := range []string{"terminal_input.hex", "terminal_output.hex"} {
		t.Run(name, func(t *testing.T) {
			want, err := hex.DecodeString(string(fixtureBytes(t, name)))
			if err != nil {
				t.Fatal(err)
			}
			frame, err := DecodeTerminalFrame(want)
			if err != nil {
				t.Fatal(err)
			}
			got, err := encodeTerminalFrame(frame)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("binary fixture drift: %x != %x", got, want)
			}
		})
	}
}

func TestControlMalformed(t *testing.T) {
	valid := string(fixtureBytes(t, "hello.json"))
	deep := `{"v":1,"type":"HELLO","body":{"daemon_id":"` + strings.Repeat("00", 16) + `","boot_id":"` + strings.Repeat("11", 16) + `","connection_nonce":"` + strings.Repeat("22", 32) + `","x":[` + strings.Repeat("[", MaxJSONDepth+2) + "0" + strings.Repeat("]", MaxJSONDepth+2) + `]}}`
	arrays := `{"v":1,"type":"HELLO","body":{"daemon_id":"` + strings.Repeat("00", 16) + `","boot_id":"` + strings.Repeat("11", 16) + `","connection_nonce":"` + strings.Repeat("22", 32) + `","x":[` + strings.TrimSuffix(strings.Repeat("0,", MaxJSONArray+1), ",") + `]}}`
	cases := []struct{ name, data string }{
		{"wrong version", strings.Replace(valid, `"v":1`, `"v":2`, 1)},
		{"unknown type", strings.Replace(valid, `"HELLO"`, `"NOPE"`, 1)},
		{"missing body", `{"v":1,"type":"HELLO"}`},
		{"unknown envelope field", strings.Replace(valid, `,"body"`, `,"extra":1,"body"`, 1)},
		{"unknown body field", strings.Replace(valid, `}}`, `,"extra":1}}`, 1)},
		{"duplicate envelope", strings.Replace(valid, `,"type"`, `,"v":1,"type"`, 1)},
		{"duplicate body", strings.Replace(valid, `,"boot_id"`, `,"daemon_id":"`+strings.Repeat("00", 16)+`","boot_id"`, 1)},
		{"trailing", valid + ` {}`},
		{"array bound", arrays},
		{"depth bound", deep},
		{"unsafe number", strings.Replace(valid, `"v":1`, `"v":9007199254740992`, 1)},
		{"fractional number", strings.Replace(valid, `"v":1`, `"v":1.0`, 1)},
		{"upper hex", strings.Replace(valid, "000102030405060708090a0b0c0d0e0f", "000102030405060708090A0b0c0d0e0f", 1)},
		{"bad id", strings.Replace(valid, `"type":"HELLO"`, `"type":"HELLO","id":"bad id"`, 1)},
		{"hello id", strings.Replace(valid, `"type":"HELLO"`, `"type":"HELLO","id":"x"`, 1)},
		{"error missing retryable", `{"v":1,"type":"ERROR","body":{"code":"unauthorized"}}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeControl([]byte(test.data)); err == nil {
				t.Fatal("malformed frame accepted")
			}
		})
	}
	if _, err := DecodeControl(append([]byte(valid), 0xff)); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if _, err := DecodeControl(bytes.Repeat([]byte{' '}, MaxControlBytes+1)); !errors.Is(err, ErrOversized) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestControlIDAndBodyValidation(t *testing.T) {
	goodID := "x"
	if _, err := EncodePairResult(goodID, PairResult{ClientID: strings.Repeat("60", 16), Capabilities: CapabilityObserve}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", strings.Repeat("x", 65), "x y", "é", "\x00"} {
		if _, err := EncodePairResult(id, PairResult{ClientID: strings.Repeat("60", 16), Capabilities: CapabilityObserve}); err == nil {
			t.Fatalf("invalid id %q accepted", id)
		}
	}
	for _, caps := range []Capabilities{0, 16, 0xffffffff} {
		if _, err := EncodePairResult("x", PairResult{ClientID: strings.Repeat("60", 16), Capabilities: caps}); err == nil {
			t.Fatalf("invalid capability %d accepted", caps)
		}
	}
	if _, err := EncodeError("x", Error{Code: "private-sentinel"}); err == nil {
		t.Fatal("unknown error accepted")
	}
}

func TestManifestMatchesImplementedRegistry(t *testing.T) {
	data := fixtureBytes(t, "../manifest.json")
	var manifest struct {
		Version      int `json:"version"`
		Capabilities []struct {
			Name  string `json:"name"`
			Bit   int    `json:"bit"`
			Value byte   `json:"value"`
		} `json:"capabilities"`
		Control []struct {
			Type      string `json:"type"`
			Direction string `json:"direction"`
			ID        string `json:"id"`
		} `json:"control"`
		Terminal struct {
			Magic       string `json:"magic"`
			Version     int    `json:"version"`
			HeaderBytes int    `json:"header_bytes"`
			MaxPayload  int    `json:"max_payload_bytes"`
			Opcodes     []struct {
				Name      string `json:"name"`
				Value     byte   `json:"value"`
				Direction string `json:"direction"`
			} `json:"opcodes"`
		} `json:"terminal"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Version != 1 || len(manifest.Control) != 6 || len(manifest.Terminal.Opcodes) != 2 {
		t.Fatalf("manifest registry incomplete: %+v", manifest)
	}
	capabilityNames := []string{"observe", "private_human_request_detail", "human_actions", "terminal_input"}
	capabilityValues := []byte{1, 2, 4, 8}
	if len(manifest.Capabilities) != len(capabilityNames) {
		t.Fatalf("capability registry size = %d", len(manifest.Capabilities))
	}
	for i, name := range capabilityNames {
		if manifest.Capabilities[i].Name != name || manifest.Capabilities[i].Bit != i || manifest.Capabilities[i].Value != capabilityValues[i] {
			t.Fatalf("capability[%d] drift: %+v", i, manifest.Capabilities[i])
		}
	}
	want := []string{"HELLO", "PAIR_PROVE", "PAIR_RESULT", "AUTH_PROVE", "AUTH_RESULT", "ERROR"}
	for i, name := range want {
		if manifest.Control[i].Type != name {
			t.Fatalf("control[%d] = %q", i, manifest.Control[i].Type)
		}
	}
	if manifest.Terminal.Magic != "DF" || manifest.Terminal.Version != 1 || manifest.Terminal.HeaderBytes != TerminalHeaderSize || manifest.Terminal.MaxPayload != MaxTerminalPayload {
		t.Fatal("binary manifest drift")
	}
	if manifest.Terminal.Opcodes[0].Value != byte(TerminalInputOpcode) || manifest.Terminal.Opcodes[1].Value != byte(TerminalOutputOpcode) {
		t.Fatal("opcode registry drift")
	}
	for _, name := range []string{"hello", "pair_prove", "pair_result", "auth_prove", "auth_result", "error"} {
		if len(fixtureBytes(t, name+".json")) == 0 {
			t.Fatal("empty fixture")
		}
	}
}

func TestBinaryMalformed(t *testing.T) {
	valid, err := hex.DecodeString(string(fixtureBytes(t, "terminal_input.hex")))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func([]byte)
	}{
		{"short", func(v []byte) { _ = v[:TerminalHeaderSize-1] }},
		{"magic", func(v []byte) { v[0] = 'X' }},
		{"version", func(v []byte) { v[2] = 2 }},
		{"opcode", func(v []byte) { v[3] = 9 }},
		{"zero session", func(v []byte) {
			for i := 4; i < 20; i++ {
				v[i] = 0
			}
		}},
		{"zero input sequence", func(v []byte) {
			for i := 20; i < 28; i++ {
				v[i] = 0
			}
		}},
		{"zero input generation", func(v []byte) {
			for i := 28; i < 36; i++ {
				v[i] = 0
			}
		}},
		{"wrong length", func(v []byte) { v[39]++ }},
		{"zero payload", func(v []byte) { v[39] = 0; v = v[:TerminalHeaderSize] }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value := append([]byte(nil), valid...)
			if test.name == "short" {
				if _, err := DecodeTerminalFrame(value[:TerminalHeaderSize-1]); err == nil {
					t.Fatal("short accepted")
				}
				return
			}
			test.mutate(value)
			if _, err := DecodeTerminalFrame(value); err == nil {
				t.Fatal("malformed accepted")
			}
		})
	}
	output, err := hex.DecodeString(string(fixtureBytes(t, "terminal_output.hex")))
	if err != nil {
		t.Fatal(err)
	}
	output[35] = 1
	if _, err := DecodeTerminalFrame(output); err == nil {
		t.Fatal("output generation accepted")
	}
	tooLarge := make([]byte, TerminalHeaderSize+MaxTerminalPayload+1)
	tooLarge[0], tooLarge[1], tooLarge[2], tooLarge[3] = 'D', 'F', 1, byte(TerminalOutputOpcode)
	if _, err := DecodeTerminalFrame(tooLarge); err == nil {
		t.Fatal("oversize accepted")
	}
	if _, err := EncodeTerminalInput([16]byte{1}, 1, 1, bytes.Repeat([]byte{'x'}, MaxTerminalPayload+1)); err == nil {
		t.Fatal("oversize encode accepted")
	}
}

func TestBinaryMutationGuards(t *testing.T) {
	id := [16]byte{1}
	if _, err := EncodeTerminalOutput(id, 1, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeTerminalInput(id, 1, 1, []byte("x")); err != nil {
		t.Fatal(err)
	}
	// These are the causal checks that kill the common guard mutations:
	// accepting output generation, skipping payload-length equality, and
	// accepting a zero input sequence/generation.
	output, _ := EncodeTerminalOutput(id, 1, []byte("x"))
	output[35] = 1
	if _, err := DecodeTerminalFrame(output); err == nil {
		t.Fatal("output generation mutation survived")
	}
	input, _ := EncodeTerminalInput(id, 1, 1, []byte("x"))
	input[39] = 2
	if _, err := DecodeTerminalFrame(input); err == nil {
		t.Fatal("payload-length mutation survived")
	}
	input, _ = EncodeTerminalInput(id, 1, 1, []byte("x"))
	input[27] = 0
	for i := 20; i < 28; i++ {
		input[i] = 0
	}
	if _, err := DecodeTerminalFrame(input); err == nil {
		t.Fatal("zero sequence mutation survived")
	}
}

func TestDecodeErrorsAreStable(t *testing.T) {
	_, err := DecodeControl([]byte(`{"v":1,"type":"NOPE","body":{}}`))
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(fmt.Sprint(err), "private") {
		t.Fatal("private text in public error")
	}
}
