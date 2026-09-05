package browserprotocol

import (
	"strings"
	"testing"
)

// The manifest fixture loop proves the positive path and the closed direction
// for every console message. This proves the bounds beside it: without them a
// widened body would ship green.
func TestConsoleControlBounds(t *testing.T) {
	agent := "02020202020202020202020202020202"
	task := "03030303030303030303030303030303"
	node := strings.Repeat("a1", 32)
	for _, frame := range []string{
		// Every mutable member is optional; only the identity and the observed
		// revision are required.
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"7"}}`,
		`{"type":"TASK_UPDATE","id":"x","body":{"task_id":"` + task + `","expected_revision":"7"}}`,
	} {
		if _, err := DecodeClientControl([]byte(frame)); err != nil {
			t.Fatalf("optional members refused: %v", err)
		}
	}
	for _, frame := range []string{
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"0"}}`,
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"7","model":"` + strings.Repeat("m", MaxAgentModelBytes+1) + `"}}`,
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"7","reasoning_effort":"` + strings.Repeat("e", MaxAgentModelBytes+1) + `"}}`,
		// Cancellation is the only status transition the console may ask for.
		`{"type":"TASK_UPDATE","id":"x","body":{"task_id":"` + task + `","expected_revision":"7","status":"succeeded"}}`,
		`{"type":"TASK_UPDATE","id":"x","body":{"task_id":"` + task + `","expected_revision":"7","priority":1000001}}`,
		`{"type":"TASK_UPDATE","id":"x","body":{"task_id":"` + task + `","expected_revision":"7","title":""}}`,
		`{"type":"TOPOLOGY_GET","id":"x","body":{"project_id":"0000000000000000000000000000000"}}`,
	} {
		if _, err := DecodeClientControl([]byte(frame)); err != ErrMalformed {
			t.Fatalf("%s accepted: %v", frame, err)
		}
	}
	topology := func(nodes string) string {
		return `{"type":"TOPOLOGY","id":"x","body":{"project_id":"01010101010101010101010101010101","digest":"` +
			strings.Repeat("ab", 32) + `","source_revision":"","nodes":[` + nodes + `]}}`
	}
	good := `{"id":"` + node + `","parent_id":"","kind":"package","path":"internal/kernel","label":"kernel","language":"go","size_bucket":"large"}`
	if _, err := DecodeServerControl([]byte(topology(good))); err != nil {
		t.Fatalf("topology refused: %v", err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"kind":"package"`, `"kind":"symbol"`, 1),
		strings.Replace(good, `"size_bucket":"large"`, `"size_bucket":"huge"`, 1),
		strings.Replace(good, `"id":"`+node+`"`, `"id":"a1"`, 1),
		strings.Replace(good, `"label":"kernel"`, `"label":""`, 1),
	} {
		if _, err := DecodeServerControl([]byte(topology(bad))); err != ErrMalformed {
			t.Fatalf("%s accepted: %v", bad, err)
		}
	}
	// TOPOLOGY is the second frame allowed past the control bound, and it is
	// bounded by the snapshot entity count rather than the array limit.
	wide := make([]string, MaxJSONArray+1)
	for index := range wide {
		wide[index] = strings.Replace(good, `"id":"`+node+`"`, `"id":"`+strings.Repeat("0", 31)+strings.Repeat("1", 33)+`"`, 1)
	}
	if _, err := DecodeServerControl([]byte(topology(strings.Join(wide, ",")))); err != nil {
		t.Fatalf("topology array bound is the control bound: %v", err)
	}
	if controlLimit(TypeTopology) != MaxSnapshotBytes {
		t.Fatal("topology does not share the snapshot byte bound")
	}
}
