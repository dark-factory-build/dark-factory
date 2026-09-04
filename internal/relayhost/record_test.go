package relayhost

import (
	"bytes"
	"errors"
	"testing"
)

func TestRecordCodecRoundTripsSeveralRecordsInOneMessage(t *testing.T) {
	want := []Record{
		{Type: RecordOpen, Connection: 7, Payload: []byte(`{"controller":"AA"}`)},
		{Type: RecordText, Connection: 7, Payload: []byte("hello")},
		{Type: RecordBinary, Connection: 9, Payload: []byte{0x00, 0xff, 0x10}},
		{Type: RecordClose, Connection: 9, Payload: nil},
		{Type: RecordRevoke, Connection: 0, Payload: []byte(`{"controller":"AA"}`)},
	}
	var message []byte
	for _, record := range want {
		message = AppendRecord(message, record)
	}
	got, err := DecodeRecords(message)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("decoded %d records, want %d", len(got), len(want))
	}
	for index := range want {
		if got[index].Type != want[index].Type || got[index].Connection != want[index].Connection || !bytes.Equal(got[index].Payload, want[index].Payload) {
			t.Fatalf("record %d = %+v, want %+v", index, got[index], want[index])
		}
	}
}

func TestRecordCodecFailsClosedOnEveryBound(t *testing.T) {
	full := AppendRecord(nil, Record{Type: RecordText, Connection: 1, Payload: []byte("abcd")})
	for _, testCase := range []struct {
		name    string
		message []byte
		want    error
	}{
		{name: "empty message", message: nil, want: ErrRecordTruncated},
		{name: "truncated header", message: full[:5], want: ErrRecordTruncated},
		{name: "truncated payload", message: full[:len(full)-1], want: ErrRecordTruncated},
		{name: "trailing partial record", message: append(append([]byte(nil), full...), 0x02, 0x00), want: ErrRecordTruncated},
		{name: "unknown type", message: []byte{0x06, 0, 0, 0, 1, 0, 0, 0, 0}, want: ErrRecordType},
		{name: "declared payload beyond bound", message: []byte{0x02, 0, 0, 0, 1, 0xff, 0xff, 0xff, 0xff}, want: ErrRecordOversized},
		{name: "message beyond bound", message: make([]byte, MaxHostMessageBytes+1), want: ErrRecordOversized},
	} {
		if _, err := DecodeRecords(testCase.message); !errors.Is(err, testCase.want) {
			t.Fatalf("%s = %v, want %v", testCase.name, err, testCase.want)
		}
	}
	// The largest legal payload must still decode, so the bound is exact
	// rather than merely conservative.
	largest := AppendRecord(nil, Record{Type: RecordBinary, Connection: 1, Payload: make([]byte, maxRecordPayloadBytes)})
	if records, err := DecodeRecords(largest); err != nil || len(records) != 1 || len(records[0].Payload) != maxRecordPayloadBytes {
		t.Fatalf("largest legal record = %d records, %v", len(records), err)
	}
}

func TestCoalesceNeverSplitsARecordAndSendsALargeOneAlone(t *testing.T) {
	small := Record{Type: RecordText, Connection: 1, Payload: bytes.Repeat([]byte("a"), 1024)}
	large := Record{Type: RecordBinary, Connection: 2, Payload: make([]byte, 1<<20)}
	messages := coalesce([]Record{small, small, large, small})
	if len(messages) != 3 {
		t.Fatalf("coalesced into %d messages, want 3", len(messages))
	}
	if got := len(messages[0]); got != 2*(recordHeaderBytes+1024) {
		t.Fatalf("first message = %d bytes, want the two small records", got)
	}
	if got := len(messages[1]); got != recordHeaderBytes+(1<<20) {
		t.Fatalf("second message = %d bytes, want the large record alone", got)
	}
	for index, message := range messages {
		if _, err := DecodeRecords(message); err != nil {
			t.Fatalf("message %d does not decode: %v", index, err)
		}
	}
}

func TestOutboundQueueBoundsOneSessionWithoutAffectingAnother(t *testing.T) {
	queue := newOutboundQueue()
	slow, other := newSession(1), newSession(2)
	for index := 0; index < maxSessionQueued; index++ {
		if !queue.push(slow, Record{Type: RecordText, Connection: 1}) {
			t.Fatalf("push %d was refused below the bound", index)
		}
	}
	if queue.push(slow, Record{Type: RecordText, Connection: 1}) {
		t.Fatal("push beyond the per-session bound was accepted")
	}
	if !queue.push(other, Record{Type: RecordText, Connection: 2}) {
		t.Fatal("a second session was refused by the first session's backlog")
	}
	if !queue.push(nil, Record{Type: RecordClose, Connection: 1}) {
		t.Fatal("a connection level record was refused by a session backlog")
	}
	if drained := queue.drain(); len(drained) != maxSessionQueued+2 {
		t.Fatalf("drained %d records, want %d", len(drained), maxSessionQueued+2)
	}
	if !queue.push(slow, Record{Type: RecordText, Connection: 1}) {
		t.Fatal("draining did not release the session budget")
	}
	queue.close()
	if queue.push(slow, Record{Type: RecordText, Connection: 1}) {
		t.Fatal("a closed queue accepted a record")
	}
}
