package relayhost

import "sync"

const (
	// maxSessionQueued bounds one session's unwritten records. A controller
	// the relay cannot drain fast enough loses only its own session.
	maxSessionQueued = 64
	// maxCoalescedBytes bounds one coalesced host message. A record larger
	// than this is never split; it travels alone.
	maxCoalescedBytes = 256 << 10
)

// outboundQueue is the single writer's inbox for one relay connection.
// Ownership is per connection: closing it makes every later push a refusal,
// so a session that loses its race with a reconnect cannot resurrect a
// record onto the new connection.
type outboundQueue struct {
	mu      sync.Mutex
	records []queued
	closed  bool
	signal  chan struct{}
}

type queued struct {
	record  Record
	session *session
}

func newOutboundQueue() *outboundQueue {
	return &outboundQueue{signal: make(chan struct{}, 1)}
}

// push enqueues one record. A session-attributed push is refused once that
// session already holds maxSessionQueued unwritten records; a nil session is
// a connection-level record (CLOSE for a finished session, REVOKE) and is
// never rate limited by one controller.
func (queue *outboundQueue) push(owner *session, record Record) bool {
	queue.mu.Lock()
	if queue.closed {
		queue.mu.Unlock()
		return false
	}
	if owner != nil {
		if owner.queued >= maxSessionQueued {
			queue.mu.Unlock()
			return false
		}
		owner.queued++
	}
	queue.records = append(queue.records, queued{record: record, session: owner})
	queue.mu.Unlock()
	select {
	case queue.signal <- struct{}{}:
	default:
	}
	return true
}

// drain takes everything queued right now and releases each session's budget.
func (queue *outboundQueue) drain() []Record {
	queue.mu.Lock()
	pending := queue.records
	queue.records = nil
	records := make([]Record, 0, len(pending))
	for _, item := range pending {
		if item.session != nil {
			item.session.queued--
		}
		records = append(records, item.record)
	}
	queue.mu.Unlock()
	return records
}

func (queue *outboundQueue) close() {
	queue.mu.Lock()
	queue.closed = true
	queue.records = nil
	queue.mu.Unlock()
	select {
	case queue.signal <- struct{}{}:
	default:
	}
}

// coalesce packs records into as few messages as the bound allows without
// ever splitting one record.
func coalesce(records []Record) [][]byte {
	var messages [][]byte
	var current []byte
	for _, record := range records {
		size := RecordHeaderBytes + len(record.Payload)
		if len(current) > 0 && len(current)+size > maxCoalescedBytes {
			messages = append(messages, current)
			current = nil
		}
		current = AppendRecord(current, record)
	}
	if len(current) > 0 {
		messages = append(messages, current)
	}
	return messages
}
