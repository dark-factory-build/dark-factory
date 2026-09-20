package browserprotocol

import (
	"fmt"
	"unicode/utf8"
)

const MaxTaskInstructionBytes = 32768
const MaxTaskAttachmentBytes = 8 << 20
const MaxTaskAttachments = 8
const TaskAttachmentChunkBytes = 24 << 10

type TaskAttachment struct {
	Name string
	Data []byte
}

// Uploads are connection-local until TASK_ENQUEUE commits them with the task.
// Index zero at offset zero starts a new batch, discarding unfinished uploads.
type TaskAttachmentChunk struct {
	Index  int     `json:"index"`
	Offset Decimal `json:"offset"`
	Size   Decimal `json:"size"`
	Name   string  `json:"name"`
	Data   []byte  `json:"data"`
}
type TaskAttachmentResult struct {
	Offset Decimal `json:"offset"`
}

func EncodeTaskAttachment(id string, value TaskAttachmentChunk) ([]byte, error) {
	return encodeControl(TypeTaskAttachment, id, value)
}
func EncodeTaskAttachmentResult(id string, value TaskAttachmentResult) ([]byte, error) {
	return encodeControl(TypeTaskAttachmentResult, id, value)
}

// TaskEnqueue submits an instruction from an agent's pane. Mode "now" starts
// it on that agent, "queue" queues it for that agent, and "any" queues it for
// any eligible worker in that agent's project.
type TaskEnqueue struct {
	TaskID        string `json:"task_id"`
	IncarnationID string `json:"incarnation_id"`
	AgentID       string `json:"agent_id"`
	// RepositoryID is optional only when the project has a default repository.
	// It is private routing configuration, never a STATE member.
	RepositoryID          string           `json:"repository_id,omitempty"`
	ExpectedAgentRevision Decimal          `json:"expected_agent_revision"`
	Instruction           string           `json:"instruction"`
	Mode                  string           `json:"mode,omitempty"`
	AttachmentCount       int              `json:"attachment_count,omitempty"`
	Attachments           []TaskAttachment `json:"-"`
}

type TaskEnqueueResult struct {
	TaskID        string  `json:"task_id"`
	Revision      Decimal `json:"revision"`
	AgentRevision Decimal `json:"agent_revision"`
}

func EncodeTaskEnqueue(id string, value TaskEnqueue) ([]byte, error) {
	return encodeControl(TypeTaskEnqueue, id, value)
}

func EncodeTaskEnqueueResult(id string, value TaskEnqueueResult) ([]byte, error) {
	return encodeControl(TypeTaskEnqueueResult, id, value)
}

func validTaskControl(kind MessageType, body any) error {
	body = indirect(body)
	bad := func() error { return fmt.Errorf("%w: invalid %s", ErrMalformed, kind) }
	id := func(value string) bool {
		decoded, err := fixedHex("id", value, 16)
		if err != nil {
			return false
		}
		for _, b := range decoded {
			if b != 0 {
				return true
			}
		}
		return false
	}
	positive := func(value Decimal) bool { return value > 0 }
	switch value := body.(type) {
	case TaskAttachmentChunk:
		if value.Index < 0 || value.Index >= MaxTaskAttachments || value.Offset > MaxTaskAttachmentBytes || value.Size < 1 || value.Size > MaxTaskAttachmentBytes || len(value.Name) < 1 || len(value.Name) > 255 || !utf8.ValidString(value.Name) || len(value.Data) < 1 || len(value.Data) > TaskAttachmentChunkBytes || uint64(value.Offset)+uint64(len(value.Data)) > uint64(value.Size) {
			return bad()
		}
	case TaskAttachmentResult:
		if value.Offset < 1 || value.Offset > MaxTaskAttachmentBytes {
			return bad()
		}
	case TaskEnqueue:
		if value.AttachmentCount < 0 || value.AttachmentCount > MaxTaskAttachments {
			return bad()
		}
		if value.Mode != "" && value.Mode != "now" && value.Mode != "queue" && value.Mode != "any" || !id(value.TaskID) || !id(value.IncarnationID) || !id(value.AgentID) || value.RepositoryID != "" && !id(value.RepositoryID) || !positive(value.ExpectedAgentRevision) || !utf8.ValidString(value.Instruction) || len(value.Instruction) == 0 || len([]byte(value.Instruction)) > MaxTaskInstructionBytes {
			return bad()
		}
	case TaskEnqueueResult:
		if !id(value.TaskID) || !positive(value.Revision) || !positive(value.AgentRevision) {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}
