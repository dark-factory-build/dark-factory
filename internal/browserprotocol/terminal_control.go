package browserprotocol

import "fmt"

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
