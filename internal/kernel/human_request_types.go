package kernel

import (
	"fmt"
	"unicode/utf8"
)

const (
	MaxHumanRequestQuestionBytes = 8192
	MaxHumanRequestReplyBytes    = 8192
	MaxOpenHumanRequests         = 1024
)

type HumanRequestKind uint8

const HumanRequestQuestion HumanRequestKind = 1

func (kind HumanRequestKind) String() string {
	if kind == HumanRequestQuestion {
		return "question"
	}
	return ""
}

func parseHumanRequestKind(value string) (HumanRequestKind, error) {
	if value != HumanRequestQuestion.String() {
		return 0, corruptControl("human request kind", value)
	}
	return HumanRequestQuestion, nil
}

type HumanRequestStatus uint8

const (
	HumanRequestOpen HumanRequestStatus = iota + 1
	HumanRequestDelivering
	HumanRequestDeliveryUnknown
	HumanRequestResolved
	HumanRequestStale
)

func (status HumanRequestStatus) String() string {
	switch status {
	case HumanRequestOpen:
		return "open"
	case HumanRequestDelivering:
		return "delivering"
	case HumanRequestDeliveryUnknown:
		return "delivery_unknown"
	case HumanRequestResolved:
		return "resolved"
	case HumanRequestStale:
		return "stale"
	default:
		return ""
	}
}

func parseHumanRequestStatus(value string) (HumanRequestStatus, error) {
	for status := HumanRequestOpen; status <= HumanRequestStale; status++ {
		if status.String() == value {
			return status, nil
		}
	}
	return 0, corruptControl("human request status", value)
}

type HumanRequestResolution uint8

const (
	HumanRequestResolutionReply HumanRequestResolution = iota + 1
	HumanRequestResolutionStale
)

func (resolution HumanRequestResolution) String() string {
	switch resolution {
	case HumanRequestResolutionReply:
		return "reply"
	case HumanRequestResolutionStale:
		return "stale"
	default:
		return ""
	}
}

func parseHumanRequestResolution(value string) (HumanRequestResolution, error) {
	for resolution := HumanRequestResolutionReply; resolution <= HumanRequestResolutionStale; resolution++ {
		if resolution.String() == value {
			return resolution, nil
		}
	}
	return 0, corruptControl("human request resolution", value)
}

type HumanRequest struct {
	ID                HumanRequestID
	RunID             RunID
	IdempotencyKey    [IDBytes]byte `json:"-"`
	Kind              HumanRequestKind
	Status            HumanRequestStatus
	QuestionText      string                  `json:"-"`
	DeliveryID        *HumanRequestDeliveryID `json:"-"`
	DeliveryStartedAt *UnixMillis             `json:"-"`
	Resolution        *HumanRequestResolution `json:"-"`
	ClosedAt          *UnixMillis             `json:"-"`
	Revision          Revision
	CreatedAt         UnixMillis
	UpdatedAt         UnixMillis
}

// HumanRequestProjection is the bounded, safe card sent to clients. It has
// no provider-authored question or reply text.
type HumanRequestProjection struct {
	ID              HumanRequestID
	ProjectID       ProjectID
	ProjectName     string
	AgentID         AgentID
	AgentName       string
	TaskID          TaskID
	TaskTitle       string
	RunID           RunID
	CreatedAt       UnixMillis
	UpdatedAt       UnixMillis
	Revision        Revision
	Kind            HumanRequestKind
	Title           string
	Summary         string
	WhyHumanNeeded  string
	Status          HumanRequestStatus
	ReplyMaxBytes   uint32
	CanOpenTerminal bool
}

type HumanRequestDetail struct {
	ID           HumanRequestID
	Revision     Revision
	QuestionText string
}

type NewHumanQuestion struct {
	IdempotencyKey [IDBytes]byte
	QuestionText   string
}

type HumanDelivery struct {
	RequestID  HumanRequestID
	RunID      RunID
	DeliveryID HumanRequestDeliveryID
	Revision   Revision
	Reply      []byte `json:"-"`
}

func (input NewHumanQuestion) valid() error {
	if input.IdempotencyKey == [IDBytes]byte{} {
		return fmt.Errorf("%w: zero human request idempotency key", ErrInvalidValue)
	}
	if !utf8TextWithin(input.QuestionText, 1, MaxHumanRequestQuestionBytes) {
		return fmt.Errorf("%w: invalid human request question", ErrInvalidValue)
	}
	return nil
}

func utf8TextWithin(value string, minimum, maximum int) bool {
	bytes := len([]byte(value))
	return utf8.ValidString(value) && bytes >= minimum && bytes <= maximum
}
