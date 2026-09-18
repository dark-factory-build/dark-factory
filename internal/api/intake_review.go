package api

import (
	"encoding/hex"
	"strings"
)

// IntakeReviewInput is the fixed publication vocabulary of the installed
// controller. Neither caller-selected URLs nor arbitrary MCP requests cross it.
type IntakeReviewInput struct {
	Tool                      string `json:"tool"`
	Page                      uint32 `json:"page,omitempty"`
	PullNumber                uint64 `json:"pull_number,omitempty"`
	ReviewID                  uint64 `json:"review_id,omitempty"`
	OperationID               string `json:"operation_id,omitempty"`
	CorrectsReviewOperationID string `json:"corrects_review_operation_id,omitempty"`
	EnqueueOperationID        string `json:"enqueue_operation_id,omitempty"`
	HeadSHA                   string `json:"head_sha,omitempty"`
	Base                      string `json:"base,omitempty"`
	Event                     string `json:"event,omitempty"`
	Body                      string `json:"body,omitempty"`
	ReviewedBodyDigest        string `json:"reviewed_body_digest,omitempty"`
}

type IntakeReviewResult struct {
	Repository   string `json:"repository"`
	RepositoryID uint64 `json:"repository_id"`
	Response     string `json:"response,omitempty"`
}

func reviewUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil && value == strings.ToLower(value)
}
func validIntakeReview(value IntakeReviewInput) bool {
	allowed := IntakeReviewInput{Tool: value.Tool}
	valid := true
	switch value.Tool {
	case "configuration", "maintainer_status":
	case "list_pull_requests":
		allowed.Page, allowed.PullNumber = value.Page, value.PullNumber
		valid = value.Page > 0 && value.Page <= 1000 && (value.PullNumber == 0 || value.Page == 1)
	case "observe_pull_request_review":
		allowed.PullNumber, allowed.ReviewID = value.PullNumber, value.ReviewID
		valid = value.PullNumber > 0 && value.ReviewID > 0
	case "observe_operation":
		allowed.OperationID = value.OperationID
		valid = reviewUUID(value.OperationID)
	case "submit_pull_request_review":
		allowed.PullNumber, allowed.OperationID, allowed.HeadSHA = value.PullNumber, value.OperationID, value.HeadSHA
		allowed.CorrectsReviewOperationID, allowed.Event, allowed.Body = value.CorrectsReviewOperationID, value.Event, value.Body
		valid = value.PullNumber > 0 && reviewUUID(value.OperationID) && validHex(value.HeadSHA, 20) && (value.CorrectsReviewOperationID == "" || reviewUUID(value.CorrectsReviewOperationID)) && (value.Event == "ALLOW" || value.Event == "REQUEST_CHANGES") && len(value.Body) > 0 && len(value.Body) <= 65536
	case "enqueue_pull_request", "observe_pull_request_merge":
		allowed.PullNumber, allowed.HeadSHA, allowed.Base, allowed.ReviewedBodyDigest = value.PullNumber, value.HeadSHA, value.Base, value.ReviewedBodyDigest
		valid = value.PullNumber > 0 && validHex(value.HeadSHA, 20) && value.Base != "" && len(value.Base) <= 240 && !strings.ContainsAny(value.Base, "\x00\r\n") && strings.HasPrefix(value.ReviewedBodyDigest, "sha256:") && validHex(strings.TrimPrefix(value.ReviewedBodyDigest, "sha256:"), 32)
		if value.Tool == "enqueue_pull_request" {
			allowed.OperationID = value.OperationID
			valid = valid && reviewUUID(value.OperationID)
		} else {
			allowed.EnqueueOperationID = value.EnqueueOperationID
			valid = valid && reviewUUID(value.EnqueueOperationID)
		}
	default:
		return false
	}
	return valid && allowed == value && value.PullNumber <= 9007199254740991 && value.ReviewID <= 9007199254740991
}
func validHex(value string, size int) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == size && value == strings.ToLower(value)
}
