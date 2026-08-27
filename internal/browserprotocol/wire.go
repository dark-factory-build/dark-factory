package browserprotocol

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Adding a v1 message is a contract change: add its manifest entry, fixture and
// explicit case in the role-specific decoders together.
const (
	MaxControlBytes = 64 << 10
	MaxJSONDepth    = 16
	MaxJSONArray    = 32
	MaxJSONObject   = 32
)

type MessageType string

const (
	TypeHello                 MessageType = "HELLO"
	TypePairProve             MessageType = "PAIR_PROVE"
	TypePairResult            MessageType = "PAIR_RESULT"
	TypeAuthProve             MessageType = "AUTH_PROVE"
	TypeAuthResult            MessageType = "AUTH_RESULT"
	TypeStateGet              MessageType = "STATE_GET"
	TypeStateSnapshot         MessageType = "STATE_SNAPSHOT"
	TypeStateRestart          MessageType = "STATE_RESTART"
	TypeStateSubscribe        MessageType = "STATE_SUBSCRIBE"
	TypeStateEvent            MessageType = "STATE_EVENT"
	TypeStateEntityGet        MessageType = "STATE_ENTITY_GET"
	TypeStateEntity           MessageType = "STATE_ENTITY"
	TypeHumanRequestDetailGet MessageType = "HUMAN_REQUEST_DETAIL_GET"
	TypeHumanRequestDetail    MessageType = "HUMAN_REQUEST_DETAIL"
	TypeError                 MessageType = "ERROR"
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
	ClientID  string `json:"client_id"`
	Signature string `json:"signature"`
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
	ErrorTooLarge           ErrorCode = "too_large"
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
	ID      json.RawMessage `json:"id,omitempty"`
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
func EncodeStateGet(id string, value StateGet) ([]byte, error) {
	return encodeControl(TypeStateGet, id, value)
}
func EncodeStateSnapshot(id string, value StateSnapshot) ([]byte, error) {
	return encodeControl(TypeStateSnapshot, id, value)
}
func EncodeStateRestart(id string, value StateRestart) ([]byte, error) {
	return encodeControl(TypeStateRestart, id, value)
}
func EncodeStateSubscribe(id string, value StateSubscribe) ([]byte, error) {
	return encodeControl(TypeStateSubscribe, id, value)
}
func EncodeStateEvent(id string, value StateEvent) ([]byte, error) {
	return encodeControl(TypeStateEvent, id, value)
}
func EncodeStateEntityGet(id string, value StateEntityGet) ([]byte, error) {
	return encodeControl(TypeStateEntityGet, id, value)
}
func EncodeStateEntity(id string, value StateEntity) ([]byte, error) {
	return encodeControl(TypeStateEntity, id, value)
}
func EncodeHumanRequestDetailGet(id string, value HumanRequestDetailGet) ([]byte, error) {
	return encodeControl(TypeHumanRequestDetailGet, id, value)
}
func EncodeHumanRequestDetail(id string, value HumanRequestDetail) ([]byte, error) {
	return encodeControl(TypeHumanRequestDetail, id, value)
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
	var wireID json.RawMessage
	if id != "" {
		wireID, err = json.Marshal(id)
		if err != nil {
			return nil, fmt.Errorf("%w: id: %v", ErrMalformed, err)
		}
	}
	frame, err := json.Marshal(controlEnvelope{Version: 1, Type: kind, ID: wireID, Body: payload})
	if err != nil {
		return nil, fmt.Errorf("%w: envelope: %v", ErrMalformed, err)
	}
	if len(frame) > MaxControlBytes {
		return nil, ErrOversized
	}
	return frame, nil
}

// DecodeClientControl accepts only frames a browser client may send. ERROR is
// bidirectional; all other message directions are closed in the manifest.
func DecodeClientControl(data []byte) (ControlFrame, error) {
	return decodeControl(data, clientRole)
}

// DecodeServerControl accepts only frames factoryd may send. ERROR is
// bidirectional; all other message directions are closed in the manifest.
func DecodeServerControl(data []byte) (ControlFrame, error) {
	return decodeControl(data, serverRole)
}

type senderRole byte

const (
	clientRole senderRole = 1
	serverRole senderRole = 2
)

func decodeControl(data []byte, role senderRole) (ControlFrame, error) {
	if len(data) > MaxControlBytes {
		return ControlFrame{}, ErrOversized
	}
	if len(data) == 0 || !utf8.Valid(data) {
		return ControlFrame{}, ErrMalformed
	}
	if err := validateJSON(data); err != nil {
		return ControlFrame{}, ErrMalformed
	}
	var envelope controlEnvelope
	if err := unmarshalObject(data, &envelope); err != nil {
		return ControlFrame{}, ErrMalformed
	}
	if envelope.Version != 1 {
		return ControlFrame{}, ErrMalformed
	}
	id, idPresent, ok := decodeID(envelope.ID)
	if !ok || !validIDPresence(id, idPresent, envelope.Type) {
		return ControlFrame{}, ErrMalformed
	}
	if len(envelope.Body) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Body), []byte("null")) {
		return ControlFrame{}, ErrMalformed
	}
	if !typeAllowed(role, envelope.Type) {
		return ControlFrame{}, ErrMalformed
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
	case TypeStateGet:
		body = new(StateGet)
	case TypeStateSnapshot:
		value, err := decodeStateSnapshot(envelope.Body)
		if err != nil {
			return ControlFrame{}, ErrMalformed
		}
		body = value
	case TypeStateRestart:
		body = new(StateRestart)
	case TypeStateSubscribe:
		body = new(StateSubscribe)
	case TypeStateEvent:
		value, err := decodeStateEvent(envelope.Body)
		if err != nil {
			return ControlFrame{}, ErrMalformed
		}
		body = value
	case TypeStateEntityGet:
		body = new(StateEntityGet)
	case TypeStateEntity:
		value, err := decodeStateEntity(envelope.Body)
		if err != nil {
			return ControlFrame{}, ErrMalformed
		}
		body = value
	case TypeHumanRequestDetailGet:
		body = new(HumanRequestDetailGet)
	case TypeHumanRequestDetail:
		body = new(HumanRequestDetail)
	case TypeError:
		var value struct {
			Code      ErrorCode `json:"code"`
			Retryable *bool     `json:"retryable"`
		}
		if err := unmarshalObject(envelope.Body, &value); err != nil {
			return ControlFrame{}, ErrMalformed
		}
		if value.Retryable == nil {
			return ControlFrame{}, ErrMalformed
		}
		body = Error{Code: value.Code, Retryable: *value.Retryable}
		if err := validateBody(envelope.Type, body); err != nil {
			return ControlFrame{}, ErrMalformed
		}
		return ControlFrame{Version: envelope.Version, Type: envelope.Type, ID: id, Body: body}, nil
	default:
		return ControlFrame{}, ErrMalformed
	}
	if !bodyAlreadyDecoded(envelope.Type) {
		if err := unmarshalObject(envelope.Body, body); err != nil {
			return ControlFrame{}, ErrMalformed
		}
	}
	if err := validateBody(envelope.Type, body); err != nil {
		return ControlFrame{}, ErrMalformed
	}
	return ControlFrame{Version: envelope.Version, Type: envelope.Type, ID: id, Body: dereferenceBody(body)}, nil
}

func bodyAlreadyDecoded(kind MessageType) bool {
	return kind == TypeStateSnapshot || kind == TypeStateEvent || kind == TypeStateEntity
}

func decodeID(raw json.RawMessage) (string, bool, bool) {
	if len(raw) == 0 {
		return "", false, true
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true, false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", true, false
	}
	return value, true, true
}

func validIDPresence(id string, present bool, kind MessageType) bool {
	required := idRequired(kind)
	if !present {
		return !required && (kind == TypeHello || kind == TypeError)
	}
	if id == "" {
		return false
	}
	return validateID(id, kind) == nil
}

func idRequired(kind MessageType) bool {
	switch kind {
	case TypePairProve, TypePairResult, TypeAuthProve, TypeAuthResult,
		TypeStateGet, TypeStateSnapshot, TypeStateRestart, TypeStateSubscribe,
		TypeStateEvent, TypeStateEntityGet, TypeStateEntity,
		TypeHumanRequestDetailGet, TypeHumanRequestDetail:
		return true
	default:
		return false
	}
}

func typeAllowed(role senderRole, kind MessageType) bool {
	if kind == TypeError {
		return true
	}
	if role == clientRole {
		return kind == TypePairProve || kind == TypeAuthProve || kind == TypeStateGet ||
			kind == TypeStateSubscribe || kind == TypeStateEntityGet || kind == TypeHumanRequestDetailGet
	}
	return role == serverRole && (kind == TypeHello || kind == TypePairResult || kind == TypeAuthResult ||
		kind == TypeStateSnapshot || kind == TypeStateRestart || kind == TypeStateEvent ||
		kind == TypeStateEntity || kind == TypeHumanRequestDetail)
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
	case *StateGet:
		return *value
	case *StateRestart:
		return *value
	case *StateSubscribe:
		return *value
	case *StateEntityGet:
		return *value
	case *HumanRequestDetailGet:
		return *value
	case *HumanRequestDetail:
		return *value
	case StateSnapshot, StateEvent, StateEntity:
		return value
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
	if err := validateExactJSONShape(trimmed, reflect.TypeOf(target)); err != nil {
		return err
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

func unmarshalArray(data []byte, target any) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return fmt.Errorf("%w: array required", ErrMalformed)
	}
	if err := validateExactJSONShape(trimmed, reflect.TypeOf(target)); err != nil {
		return err
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

func validateExactJSONShape(data []byte, target reflect.Type) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	if target == reflect.TypeOf(json.RawMessage{}) {
		return nil
	}
	switch target.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			return fmt.Errorf("%w: object required", ErrMalformed)
		}
		fields := make(map[string]reflect.Type, target.NumField())
		required := make(map[string]bool, target.NumField())
		for index := 0; index < target.NumField(); index++ {
			field := target.Field(index)
			tag := field.Tag.Get("json")
			name, options, _ := strings.Cut(tag, ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			fields[name] = field.Type
			required[name] = options != "omitempty"
		}
		for name, raw := range object {
			fieldType, ok := fields[name]
			if !ok {
				return fmt.Errorf("%w: unknown object field", ErrMalformed)
			}
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) && fieldType.Kind() == reflect.Pointer {
				continue
			}
			if err := validateExactJSONShape(raw, fieldType); err != nil {
				return err
			}
		}
		for name, needed := range required {
			if _, ok := object[name]; needed && !ok {
				return fmt.Errorf("%w: missing object field", ErrMalformed)
			}
		}
	case reflect.Slice, reflect.Array:
		var values []json.RawMessage
		if err := json.Unmarshal(data, &values); err != nil {
			return fmt.Errorf("%w: array required", ErrMalformed)
		}
		for _, raw := range values {
			if err := validateExactJSONShape(raw, target.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateID(id string, kind MessageType) error {
	required := idRequired(kind)
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
	case TypeStateGet:
		value, ok := body.(StateGet)
		if !ok {
			if pointer, ok := body.(*StateGet); ok {
				value = *pointer
			} else {
				return fmt.Errorf("%w: STATE_GET body type", ErrMalformed)
			}
		}
		return validateStateGet(value)
	case TypeStateSnapshot:
		value, ok := body.(StateSnapshot)
		if !ok {
			return fmt.Errorf("%w: STATE_SNAPSHOT body type", ErrMalformed)
		}
		return validateStateSnapshot(value)
	case TypeStateRestart:
		value, ok := body.(StateRestart)
		if !ok {
			if pointer, ok := body.(*StateRestart); ok {
				value = *pointer
			} else {
				return fmt.Errorf("%w: STATE_RESTART body type", ErrMalformed)
			}
		}
		return validateStateRestart(value)
	case TypeStateSubscribe:
		value, ok := body.(StateSubscribe)
		if !ok {
			if pointer, ok := body.(*StateSubscribe); ok {
				value = *pointer
			} else {
				return fmt.Errorf("%w: STATE_SUBSCRIBE body type", ErrMalformed)
			}
		}
		return validateStateSubscribe(value)
	case TypeStateEvent:
		value, ok := body.(StateEvent)
		if !ok {
			return fmt.Errorf("%w: STATE_EVENT body type", ErrMalformed)
		}
		return validateStateEvent(value)
	case TypeStateEntityGet:
		value, ok := body.(StateEntityGet)
		if !ok {
			if pointer, ok := body.(*StateEntityGet); ok {
				value = *pointer
			} else {
				return fmt.Errorf("%w: STATE_ENTITY_GET body type", ErrMalformed)
			}
		}
		return validateStateEntityGet(value)
	case TypeStateEntity:
		value, ok := body.(StateEntity)
		if !ok {
			return fmt.Errorf("%w: STATE_ENTITY body type", ErrMalformed)
		}
		return validateStateEntity(value)
	case TypeHumanRequestDetailGet:
		value, ok := body.(HumanRequestDetailGet)
		if !ok {
			if pointer, ok := body.(*HumanRequestDetailGet); ok {
				value = *pointer
			} else {
				return fmt.Errorf("%w: HUMAN_REQUEST_DETAIL_GET body type", ErrMalformed)
			}
		}
		return validateHumanRequestDetailGet(value)
	case TypeHumanRequestDetail:
		value, ok := body.(HumanRequestDetail)
		if !ok {
			if pointer, ok := body.(*HumanRequestDetail); ok {
				value = *pointer
			} else {
				return fmt.Errorf("%w: HUMAN_REQUEST_DETAIL body type", ErrMalformed)
			}
		}
		return validateHumanRequestDetail(value)
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
	case ErrorUnauthorized, ErrorInvalidRequest, ErrorUnsupportedVersion, ErrorRateLimited, ErrorNotFound, ErrorStale, ErrorTooLarge, ErrorInternal:
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
	if digits == "" || (len(digits) > 1 && digits[0] == '0') || (len(digits) > 0 && digits[0] == '+') || negative && digits == "0" {
		return errors.New("invalid integer")
	}
	parsed, err := strconv.ParseUint(digits, 10, 64)
	if err != nil || parsed > (1<<53)-1 {
		return errors.New("unsafe integer")
	}
	return nil
}
