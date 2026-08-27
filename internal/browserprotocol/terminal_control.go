package browserprotocol

import "fmt"

func EncodeHumanRequestReply(id string, v HumanRequestReply) ([]byte, error) {
	return encodeControl(TypeHumanRequestReply, id, v)
}
func EncodeHumanRequestReplyResult(id string, v HumanRequestReplyResult) ([]byte, error) {
	return encodeControl(TypeHumanRequestReplyResult, id, v)
}
func EncodeHumanRequestCancelRun(id string, v HumanRequestCancelRun) ([]byte, error) {
	return encodeControl(TypeHumanRequestCancelRun, id, v)
}
func EncodeHumanRequestActionResult(id string, v HumanRequestActionResult) ([]byte, error) {
	return encodeControl(TypeHumanRequestActionResult, id, v)
}
func EncodeTerminalAttach(id string, v TerminalAttach) ([]byte, error) {
	return encodeControl(TypeTerminalAttach, id, v)
}
func EncodeTerminalAttached(id string, v TerminalAttached) ([]byte, error) {
	return encodeControl(TypeTerminalAttached, id, v)
}
func EncodeTerminalAck(v TerminalAck) ([]byte, error) { return encodeControl(TypeTerminalAck, "", v) }
func EncodeTerminalLeaseAcquire(id string, v TerminalLeaseAcquire) ([]byte, error) {
	return encodeControl(TypeTerminalLeaseAcquire, id, v)
}
func EncodeTerminalLeaseRenew(id string, v TerminalLeaseRenew) ([]byte, error) {
	return encodeControl(TypeTerminalLeaseRenew, id, v)
}
func EncodeTerminalLeaseRelease(id string, v TerminalLeaseRelease) ([]byte, error) {
	return encodeControl(TypeTerminalLeaseRelease, id, v)
}
func EncodeTerminalLeaseResult(id string, v TerminalLeaseResult) ([]byte, error) {
	return encodeControl(TypeTerminalLeaseResult, id, v)
}
func EncodeTerminalResize(id string, v TerminalResize) ([]byte, error) {
	return encodeControl(TypeTerminalResize, id, v)
}
func EncodeTerminalResized(id string, v TerminalResized) ([]byte, error) {
	return encodeControl(TypeTerminalResized, id, v)
}
func EncodeTerminalDetach(id string, v TerminalDetach) ([]byte, error) {
	return encodeControl(TypeTerminalDetach, id, v)
}
func EncodeTerminalDetached(id string, v TerminalDetached) ([]byte, error) {
	return encodeControl(TypeTerminalDetached, id, v)
}
func EncodeTerminalInputResult(id string, v TerminalInputResult) ([]byte, error) {
	return encodeControl(TypeTerminalInputResult, id, v)
}
func EncodeTerminalEOF(id string, v TerminalEOF) ([]byte, error) {
	return encodeControl(TypeTerminalEOF, id, v)
}
func EncodeTerminalExit(id string, v TerminalExit) ([]byte, error) {
	return encodeControl(TypeTerminalExit, id, v)
}
func EncodeTerminalReset(id string, v TerminalReset) ([]byte, error) {
	return encodeControl(TypeTerminalReset, id, v)
}

const (
	MaxTerminalUnackedBytes        = 65536
	TerminalAckTimeoutMS           = 10000
	MaxTerminalRows         uint16 = 4096
	MaxTerminalCols         uint16 = 4096
)

type HumanRequestReply struct {
	RunID            string  `json:"run_id"`
	RequestID        string  `json:"request_id"`
	ExpectedRevision Decimal `json:"expected_revision"`
	Reply            string  `json:"reply"`
}
type HumanRequestReplyResult struct {
	RequestID string  `json:"request_id"`
	Revision  Decimal `json:"revision"`
	Status    string  `json:"status"`
}
type HumanRequestCancelRun struct {
	RunID                   string  `json:"run_id"`
	RequestID               string  `json:"request_id"`
	ExpectedRequestRevision Decimal `json:"expected_request_revision"`
	ExpectedRunRevision     Decimal `json:"expected_run_revision"`
}
type HumanRequestActionResult struct {
	Action          string  `json:"action"`
	RunID           string  `json:"run_id"`
	RunRevision     Decimal `json:"run_revision"`
	RequestID       string  `json:"request_id"`
	RequestRevision Decimal `json:"request_revision"`
	Status          string  `json:"status"`
}
type TerminalAttach struct {
	RunID                   string  `json:"run_id"`
	SessionID               string  `json:"session_id"`
	ExpectedRunRevision     Decimal `json:"expected_run_revision"`
	ExpectedSessionRevision Decimal `json:"expected_session_revision"`
	AfterSequence           Decimal `json:"after_sequence"`
}
type TerminalAttached struct {
	SessionID            string  `json:"session_id"`
	Floor                Decimal `json:"floor"`
	Head                 Decimal `json:"head"`
	AcknowledgedSequence Decimal `json:"acknowledged_sequence"`
	MaxUnackedBytes      uint64  `json:"max_unacked_bytes"`
}
type TerminalAck struct {
	SessionID    string  `json:"session_id"`
	NextSequence Decimal `json:"next_sequence"`
}
type TerminalLeaseAcquire struct {
	RunID                   string  `json:"run_id"`
	SessionID               string  `json:"session_id"`
	ExpectedRunRevision     Decimal `json:"expected_run_revision"`
	ExpectedSessionRevision Decimal `json:"expected_session_revision"`
}
type TerminalLeaseRenew struct {
	RunID                   string  `json:"run_id"`
	SessionID               string  `json:"session_id"`
	Generation              Decimal `json:"generation"`
	ExpectedRunRevision     Decimal `json:"expected_run_revision"`
	ExpectedSessionRevision Decimal `json:"expected_session_revision"`
}
type TerminalLeaseRelease = TerminalLeaseRenew
type TerminalLeaseResult struct {
	Operation         string   `json:"operation"`
	RunID             string   `json:"run_id"`
	SessionID         string   `json:"session_id"`
	Generation        Decimal  `json:"generation"`
	ExpiresAtMS       *Decimal `json:"expires_at_ms,omitempty"`
	LastInputSequence Decimal  `json:"last_input_sequence"`
	RunRevision       Decimal  `json:"run_revision"`
	SessionRevision   Decimal  `json:"session_revision"`
}
type TerminalResize struct {
	RunID                   string  `json:"run_id"`
	SessionID               string  `json:"session_id"`
	Generation              Decimal `json:"generation"`
	ExpectedRunRevision     Decimal `json:"expected_run_revision"`
	ExpectedSessionRevision Decimal `json:"expected_session_revision"`
	Rows                    uint16  `json:"rows"`
	Cols                    uint16  `json:"cols"`
}
type TerminalResized struct {
	SessionID  string  `json:"session_id"`
	Generation Decimal `json:"generation"`
	Rows       uint16  `json:"rows"`
	Cols       uint16  `json:"cols"`
}
type TerminalDetach struct {
	SessionID string `json:"session_id"`
}
type TerminalDetached = TerminalDetach
type TerminalInputResult struct {
	SessionID     string  `json:"session_id"`
	Generation    Decimal `json:"generation"`
	Sequence      Decimal `json:"sequence"`
	Status        string  `json:"status"`
	AcceptedBytes uint64  `json:"accepted_bytes"`
}
type TerminalEOF struct {
	SessionID string `json:"session_id"`
}
type TerminalExit struct {
	SessionID  string `json:"session_id"`
	ExitCode   int64  `json:"exit_code"`
	ExitSignal int64  `json:"exit_signal"`
	Aborted    bool   `json:"aborted"`
}
type TerminalReset struct {
	SessionID string  `json:"session_id"`
	Floor     Decimal `json:"floor"`
	Head      Decimal `json:"head"`
}

func validTerminalControl(kind MessageType, body any) error {
	switch value := body.(type) {
	case *HumanRequestReply:
		return validTerminalControl(kind, *value)
	case *HumanRequestReplyResult:
		return validTerminalControl(kind, *value)
	case *HumanRequestCancelRun:
		return validTerminalControl(kind, *value)
	case *HumanRequestActionResult:
		return validTerminalControl(kind, *value)
	case *TerminalAttach:
		return validTerminalControl(kind, *value)
	case *TerminalAttached:
		return validTerminalControl(kind, *value)
	case *TerminalAck:
		return validTerminalControl(kind, *value)
	case *TerminalLeaseAcquire:
		return validTerminalControl(kind, *value)
	case *TerminalLeaseRenew:
		return validTerminalControl(kind, *value)
	case *TerminalLeaseResult:
		return validTerminalControl(kind, *value)
	case *TerminalResize:
		return validTerminalControl(kind, *value)
	case *TerminalResized:
		return validTerminalControl(kind, *value)
	case *TerminalDetach:
		return validTerminalControl(kind, *value)
	case *TerminalInputResult:
		return validTerminalControl(kind, *value)
	case *TerminalEOF:
		return validTerminalControl(kind, *value)
	case *TerminalExit:
		return validTerminalControl(kind, *value)
	case *TerminalReset:
		return validTerminalControl(kind, *value)
	}
	bad := func() error { return fmt.Errorf("%w: invalid %s", ErrMalformed, kind) }
	id := func(s string) bool { _, err := fixedHex("id", s, 16); return err == nil }
	pos := func(d Decimal) bool { return d > 0 }
	dims := func(n uint16) bool { return n >= 1 && n <= 4096 }
	switch v := body.(type) {
	case HumanRequestReply:
		if !id(v.RunID) || !id(v.RequestID) || v.ExpectedRevision == 0 || v.Reply == "" || len([]byte(v.Reply)) > MaxHumanReplyBytes {
			return bad()
		}
	case HumanRequestReplyResult:
		if !id(v.RequestID) || v.Revision == 0 || (v.Status != "resolved" && v.Status != "delivery_unknown") {
			return bad()
		}
	case HumanRequestCancelRun:
		if !id(v.RunID) || !id(v.RequestID) || !pos(v.ExpectedRequestRevision) || !pos(v.ExpectedRunRevision) {
			return bad()
		}
	case HumanRequestActionResult:
		if v.Action != "cancel_run" || !id(v.RunID) || !id(v.RequestID) || !pos(v.RunRevision) || !pos(v.RequestRevision) || v.Status != "resolved" {
			return bad()
		}
	case TerminalAttach:
		if !id(v.RunID) || !id(v.SessionID) || !pos(v.ExpectedRunRevision) || !pos(v.ExpectedSessionRevision) {
			return bad()
		}
	case TerminalAttached:
		if !id(v.SessionID) || v.MaxUnackedBytes != MaxTerminalUnackedBytes || v.AcknowledgedSequence > v.Head {
			return bad()
		}
	case TerminalAck:
		if !id(v.SessionID) || !pos(v.NextSequence) {
			return bad()
		}
	case TerminalLeaseAcquire:
		if !id(v.RunID) || !id(v.SessionID) || !pos(v.ExpectedRunRevision) || !pos(v.ExpectedSessionRevision) {
			return bad()
		}
	case TerminalLeaseRenew:
		if !id(v.RunID) || !id(v.SessionID) || !pos(v.Generation) || !pos(v.ExpectedRunRevision) || !pos(v.ExpectedSessionRevision) {
			return bad()
		}
	case TerminalLeaseResult:
		if (v.Operation != "acquired" && v.Operation != "renewed" && v.Operation != "released") || !id(v.RunID) || !id(v.SessionID) || !pos(v.Generation) || v.Operation == "released" && v.ExpiresAtMS != nil {
			return bad()
		}
	case TerminalResize:
		if !id(v.RunID) || !id(v.SessionID) || !pos(v.Generation) || !pos(v.ExpectedRunRevision) || !pos(v.ExpectedSessionRevision) || !dims(v.Rows) || !dims(v.Cols) {
			return bad()
		}
	case TerminalResized:
		if !id(v.SessionID) || !pos(v.Generation) || !dims(v.Rows) || !dims(v.Cols) {
			return bad()
		}
	case TerminalDetach:
		if !id(v.SessionID) {
			return bad()
		}
	case TerminalInputResult:
		if !id(v.SessionID) || !pos(v.Generation) || !pos(v.Sequence) || (v.Status != "accepted" && v.Status != "rejected" && v.Status != "partial" && v.Status != "uncertain") || v.AcceptedBytes > MaxTerminalPayload {
			return bad()
		}
	case TerminalEOF:
		if !id(v.SessionID) {
			return bad()
		}
	case TerminalExit:
		if !id(v.SessionID) || v.ExitCode < 0 || v.ExitSignal < 0 {
			return bad()
		}
	case TerminalReset:
		if !id(v.SessionID) || v.Floor > v.Head {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}
