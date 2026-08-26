package browserprotocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The v1 control protocol intentionally contains only the handshake and
// authentication messages needed before the rest of the browser API exists.
// Adding a message is a contract change: add its manifest entry, fixture and
// explicit case in DecodeControl together.
const (
	MaxControlBytes = 64 << 10
	MaxJSONDepth    = 16
	MaxJSONArray    = 32
	MaxJSONObject   = 32
)

type MessageType string

const (
	TypeHello      MessageType = "HELLO"
	TypePairProve  MessageType = "PAIR_PROVE"
	TypePairResult MessageType = "PAIR_RESULT"
	TypeAuthProve  MessageType = "AUTH_PROVE"
	TypeAuthResult MessageType = "AUTH_RESULT"
	TypeError      MessageType = "ERROR"
)

// Capability bits are deliberately a fixed wire bitmask, not an extensible
// permission registry. Observe is mandatory on every paired client.
type Capabilities uint32

const (
	CapabilityObserve Capabilities = 1 << iota
	CapabilityPrivateHumanRequestDetail
	CapabilityHumanActions
	CapabilityTerminalInput
)

const knownCapabilities = CapabilityObserve |
	CapabilityPrivateHumanRequestDetail |
	CapabilityHumanActions |
	CapabilityTerminalInput

type Hello struct {
	DaemonID        string `json:"daemon_id"`
	BootID          string `json:"boot_id"`
	ConnectionNonce string `json:"connection_nonce"`
}

type PairProve struct {
	Challenge     string `json:"challenge"`
	PublicKeySEC1 string `json:"public_key_sec1"`
	Signature     string `json:"signature"`
}

type PairResult struct {
	ClientID     string       `json:"client_id"`
	Capabilities Capabilities `json:"capabilities"`
}

type AuthProve struct {
	ClientID      string `json:"client_id"`
	PublicKeySEC1 string `json:"public_key_sec1"`
	Signature     string `json:"signature"`
}

type AuthResult struct {
	ClientID     string       `json:"client_id"`
	Capabilities Capabilities `json:"capabilities"`
}

// Error is intentionally terse. Private diagnostics never cross the browser
// boundary; the client can use the finite code and retryable bit only.
type Error struct {
	Code      ErrorCode `json:"code"`
	Retryable bool      `json:"retryable"`
}

type ErrorCode string

const (
	ErrorUnauthorized       ErrorCode = "unauthorized"
	ErrorInvalidRequest     ErrorCode = "invalid_request"
	ErrorUnsupportedVersion ErrorCode = "unsupported_version"
	ErrorRateLimited        ErrorCode = "rate_limited"
	ErrorNotFound           ErrorCode = "not_found"
	ErrorStale              ErrorCode = "stale"
	ErrorInternal           ErrorCode = "internal"
)

// ControlFrame is the decoded, closed v1 union. Body is always one of the six
// concrete structs above; callers never need a map[string]any assertion.
type ControlFrame struct {
	Version uint16
	Type    MessageType
	ID      string
	Body    any
}

var (
	ErrMalformed = errors.New("browser protocol: malformed control frame")
	ErrOversized = errors.New("browser protocol: control frame too large")
)

type controlEnvelope struct {
	Version uint16          `json:"v"`
	Type    MessageType     `json:"type"`
	ID      string          `json:"id,omitempty"`
	Body    json.RawMessage `json:"body"`
}

func EncodeHello(value Hello) ([]byte, error) { return encodeControl(TypeHello, "", value) }
func EncodePairProve(id string, value PairProve) ([]byte, error) {
	return encodeControl(TypePairProve, id, value)
}
func EncodePairResult(id string, value PairResult) ([]byte, error) {
	return encodeControl(TypePairResult, id, value)
}
func EncodeAuthProve(id string, value AuthProve) ([]byte, error) {
	return encodeControl(TypeAuthProve, id, value)
}
func EncodeAuthResult(id string, value AuthResult) ([]byte, error) {
	return encodeControl(TypeAuthResult, id, value)
}
func EncodeError(id string, value Error) ([]byte, error) {
	return encodeControl(TypeError, id, value)
}

func encodeControl(kind MessageType, id string, body any) ([]byte, error) {
	if err := validateID(id, kind); err != nil {
		return nil, err
	}
	if err := validateBody(kind, body); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("%w: body: %v", ErrMalformed, err)
	}
	frame, err := json.Marshal(controlEnvelope{Version: 1, Type: kind, ID: id, Body: payload})
	if err != nil {
		return nil, fmt.Errorf("%w: envelope: %v", ErrMalformed, err)
	}
	if len(frame) > MaxControlBytes {
		return nil, ErrOversized
	}
	return frame, nil
}

func DecodeControl(data []byte) (ControlFrame, error) {
	if len(data) > MaxControlBytes {
		return ControlFrame{}, ErrOversized
	}
	if len(data) == 0 || !utf8.Valid(data) {
		return ControlFrame{}, ErrMalformed
	}
	if err := validateJSON(data); err != nil {
		return ControlFrame{}, err
	}
	var envelope controlEnvelope
	if err := unmarshalObject(data, &envelope); err != nil {
		return ControlFrame{}, err
	}
	if envelope.Version != 1 {
		return ControlFrame{}, fmt.Errorf("%w: version %d", ErrMalformed, envelope.Version)
	}
	if err := validateID(envelope.ID, envelope.Type); err != nil {
		return ControlFrame{}, err
	}
	if len(envelope.Body) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Body), []byte("null")) {
		return ControlFrame{}, fmt.Errorf("%w: missing body", ErrMalformed)
	}
	var body any
	switch envelope.Type {
	case TypeHello:
		body = new(Hello)
	case TypePairProve:
		body = new(PairProve)
	case TypePairResult:
		body = new(PairResult)
	case TypeAuthProve:
		body = new(AuthProve)
	case TypeAuthResult:
		body = new(AuthResult)
	case TypeError:
		var value struct {
			Code      ErrorCode `json:"code"`
			Retryable *bool     `json:"retryable"`
		}
		if err := unmarshalObject(envelope.Body, &value); err != nil {
			return ControlFrame{}, err
		}
		if value.Retryable == nil {
			return ControlFrame{}, fmt.Errorf("%w: ERROR requires retryable", ErrMalformed)
		}
		body = Error{Code: value.Code, Retryable: *value.Retryable}
		if err := validateBody(envelope.Type, body); err != nil {
			return ControlFrame{}, err
		}
		return ControlFrame{Version: envelope.Version, Type: envelope.Type, ID: envelope.ID, Body: body}, nil
	default:
		return ControlFrame{}, fmt.Errorf("%w: unknown type %q", ErrMalformed, envelope.Type)
	}
	if err := unmarshalObject(envelope.Body, body); err != nil {
		return ControlFrame{}, err
	}
	if err := validateBody(envelope.Type, body); err != nil {
		return ControlFrame{}, err
	}
	return ControlFrame{Version: envelope.Version, Type: envelope.Type, ID: envelope.ID, Body: dereferenceBody(body)}, nil
}

func dereferenceBody(body any) any {
	switch value := body.(type) {
	case *Hello:
		return *value
	case *PairProve:
		return *value
	case *PairResult:
		return *value
	case *AuthProve:
		return *value
	case *AuthResult:
		return *value
	case *Error:
		return *value
	default:
		panic("browser protocol: unhandled body")
	}
}

func unmarshalObject(data []byte, target any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return fmt.Errorf("%w: object required", ErrMalformed)
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON", ErrMalformed)
	}
	return nil
}

func validateID(id string, kind MessageType) error {
	required := kind == TypePairProve || kind == TypePairResult || kind == TypeAuthProve || kind == TypeAuthResult
	if id == "" {
		if required {
			return fmt.Errorf("%w: %s requires id", ErrMalformed, kind)
		}
		return nil
	}
	if len(id) > 64 {
		return fmt.Errorf("%w: id too long", ErrMalformed)
	}
	for _, b := range []byte(id) {
		if b < 0x21 || b > 0x7e {
			return fmt.Errorf("%w: id must be printable ASCII", ErrMalformed)
		}
	}
	if kind == TypeHello {
		return fmt.Errorf("%w: HELLO cannot have id", ErrMalformed)
	}
	return nil
}

func validateBody(kind MessageType, body any) error {
	switch kind {
	case TypeHello:
		value, ok := body.(Hello)
		if !ok {
			if pointer, ok := body.(*Hello); ok {
				value = *pointer
			} else {
				return fmt.Errorf("%w: HELLO body type", ErrMalformed)
			}
		}
		return validateHello(value)
	case TypePairProve:
		value, ok := body.(PairProve)
		if !ok {
			if pointer, ok := body.(*PairProve); ok {
				value = *pointer
			} else {
				return fmt.Errorf("%w: PAIR_PROVE body type", ErrMalformed)
			}
		}
		return validatePairProve(value)
	case TypePairResult:
		value, ok := body.(PairResult)
		if !ok {
			if pointer, ok := body.(*PairResult); ok {
				value = *pointer
			} else {
				return fmt.Errorf("%w: PAIR_RESULT body type", ErrMalformed)
			}
		}
		return validatePairResult(value)
	case TypeAuthProve:
		value, ok := body.(AuthProve)
		if !ok {
			if pointer, ok := body.(*AuthProve); ok {
				value = *pointer
			} else {
				return fmt.Errorf("%w: AUTH_PROVE body type", ErrMalformed)
			}
		}
		return validateAuthProve(value)
	case TypeAuthResult:
		value, ok := body.(AuthResult)
		if !ok {
			if pointer, ok := body.(*AuthResult); ok {
				value = *pointer
			} else {
				return fmt.Errorf("%w: AUTH_RESULT body type", ErrMalformed)
			}
		}
		return validateAuthResult(value)
	case TypeError:
		value, ok := body.(Error)
		if !ok {
			if pointer, ok := body.(*Error); ok {
				value = *pointer
			} else {
				return fmt.Errorf("%w: ERROR body type", ErrMalformed)
			}
		}
		return validateError(value)
	default:
		return fmt.Errorf("%w: unknown type", ErrMalformed)
	}
}

func validateHello(value Hello) error {
	if _, err := fixedHex("daemon_id", value.DaemonID, DaemonIDSize); err != nil {
		return err
	}
	if _, err := fixedHex("boot_id", value.BootID, BootIDSize); err != nil {
		return err
	}
	if _, err := fixedHex("connection_nonce", value.ConnectionNonce, NonceSize); err != nil {
		return err
	}
	return nil
}

func validatePairProve(value PairProve) error {
	if _, err := fixedHex("challenge", value.Challenge, ChallengeSize); err != nil {
		return err
	}
	if key, err := fixedHex("public_key_sec1", value.PublicKeySEC1, PublicKeySize); err != nil {
		return err
	} else if key[0] != 4 {
		return fmt.Errorf("%w: public key encoding", ErrMalformed)
	}
	_, err := fixedHex("signature", value.Signature, SignatureSize)
	return err
}

func validatePairResult(value PairResult) error {
	if _, err := fixedHex("client_id", value.ClientID, ClientIDSize); err != nil {
		return err
	}
	return validateCapabilities(value.Capabilities)
}

func validateAuthProve(value AuthProve) error {
	if _, err := fixedHex("client_id", value.ClientID, ClientIDSize); err != nil {
		return err
	}
	if key, err := fixedHex("public_key_sec1", value.PublicKeySEC1, PublicKeySize); err != nil {
		return err
	} else if key[0] != 4 {
		return fmt.Errorf("%w: public key encoding", ErrMalformed)
	}
	_, err := fixedHex("signature", value.Signature, SignatureSize)
	return err
}

func validateAuthResult(value AuthResult) error {
	if _, err := fixedHex("client_id", value.ClientID, ClientIDSize); err != nil {
		return err
	}
	return validateCapabilities(value.Capabilities)
}

func validateCapabilities(value Capabilities) error {
	if value&^knownCapabilities != 0 || value&CapabilityObserve == 0 {
		return fmt.Errorf("%w: invalid capabilities", ErrMalformed)
	}
	return nil
}

func validateError(value Error) error {
	switch value.Code {
	case ErrorUnauthorized, ErrorInvalidRequest, ErrorUnsupportedVersion, ErrorRateLimited, ErrorNotFound, ErrorStale, ErrorInternal:
		return nil
	default:
		return fmt.Errorf("%w: unknown error code %q", ErrMalformed, value.Code)
	}
}

func fixedHex(name, value string, size int) ([]byte, error) {
	if len(value) != size*2 || value != strings.ToLower(value) {
		return nil, fmt.Errorf("%w: %s must be lowercase %d-byte hex", ErrMalformed, name, size)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != size {
		return nil, fmt.Errorf("%w: %s must be lowercase %d-byte hex", ErrMalformed, name, size)
	}
	return decoded, nil
}

// validateJSON checks the properties encoding/json intentionally leaves
// permissive: duplicate names, bounded nesting/arrays, and safe integer
// spelling. Typed decoding below then rejects unknown fields.
func validateJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, 0); err != nil {
		return fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON", ErrMalformed)
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int) error {
	if depth > MaxJSONDepth {
		return errors.New("JSON nesting too deep")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			keys := make(map[string]struct{}, MaxJSONObject)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := keys[key]; exists {
					return fmt.Errorf("duplicate object key %q", key)
				}
				if len(keys) == MaxJSONObject {
					return errors.New("too many object members")
				}
				keys[key] = struct{}{}
				if err := scanJSONValue(decoder, depth+1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("unterminated object")
			}
		case '[':
			count := 0
			for decoder.More() {
				if count == MaxJSONArray {
					return errors.New("array too large")
				}
				if err := scanJSONValue(decoder, depth+1); err != nil {
					return err
				}
				count++
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("unterminated array")
			}
		default:
			return errors.New("unexpected delimiter")
		}
	case json.Number:
		if err := validateJSONNumber(string(value)); err != nil {
			return err
		}
	case string, bool, nil:
		// Raw input UTF-8 was checked before tokenization.
	default:
		return errors.New("unexpected JSON token")
	}
	return nil
}

func validateJSONNumber(value string) error {
	if value == "" || strings.ContainsAny(value, ".eE") {
		return errors.New("only canonical integers are allowed")
	}
	negative := value[0] == '-'
	digits := value
	if negative {
		digits = value[1:]
	}
	if digits == "" || (len(digits) > 1 && digits[0] == '0') || (len(digits) > 0 && digits[0] == '+') {
		return errors.New("invalid integer")
	}
	parsed, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || parsed > (1<<53)-1 {
		return errors.New("unsafe integer")
	}
	return nil
}
