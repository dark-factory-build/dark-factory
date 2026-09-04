package relayhost

import "testing"

// The record count alone would let one stalled session pin 64 MiB of outbound
// payload. The byte bound is what actually caps it, so it is proved directly.
func TestOutboundQueueBoundsOneSessionByBytesAndReleasesOnDrain(t *testing.T) {
	queue := newOutboundQueue()
	owner := newSession(1)
	record := Record{Type: RecordText, Connection: 1, Payload: make([]byte, maxSessionQueuedBytes/2)}

	if !queue.push(owner, record) || !queue.push(owner, record) {
		t.Fatal("the queue refused a session inside its byte budget")
	}
	if queue.push(owner, Record{Type: RecordText, Connection: 1, Payload: []byte("one more")}) {
		t.Fatal("the queue accepted a session past its byte budget")
	}
	// A connection-level record is never rate limited by one controller.
	if !queue.push(nil, Record{Type: RecordRevoke, Payload: []byte("{}")}) {
		t.Fatal("the queue refused a connection-level record")
	}
	if records := queue.drain(); len(records) != 3 {
		t.Fatalf("drained %d records, want 3", len(records))
	}
	if owner.queued != 0 || owner.queuedBytes != 0 {
		t.Fatalf("drain left budget queued=%d bytes=%d", owner.queued, owner.queuedBytes)
	}
	if !queue.push(owner, record) {
		t.Fatal("a drained session did not get its byte budget back")
	}
}
