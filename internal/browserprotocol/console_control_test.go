package browserprotocol

import (
	"fmt"
	"strings"
	"testing"
)

// The manifest fixture loop proves the positive path and the closed direction
// for every console message. This proves the bounds beside it: without them a
// widened body would ship green.
func TestConsoleControlBounds(t *testing.T) {
	agent := "02020202020202020202020202020202"
	node := strings.Repeat("a1", 16)
	for _, frame := range []string{
		// Every mutable member is optional; only the identity and the observed
		// revision are required.
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"7"}}`,
	} {
		if _, err := DecodeClientControl([]byte(frame)); err != nil {
			t.Fatalf("optional members refused: %v", err)
		}
	}
	for _, frame := range []string{
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"0"}}`,
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"7","model":"` + strings.Repeat("m", MaxAgentModelBytes+1) + `"}}`,
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"7","reasoning_effort":"` + strings.Repeat("e", MaxAgentModelBytes+1) + `"}}`,
		`{"type":"OPERATIONAL_GRAPH_GET","id":"x","body":{"project_id":"0000000000000000000000000000000"}}`,
		// An explicit null is not "absent". encoding/json would leave the
		// pointer nil and report success with an advanced revision, while the
		// browser's exact decoder refuses the same frame.
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"7","model":null}}`,
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"7","reasoning_effort":null}}`,
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"7","paused":null}}`,
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"7","appearance":{"automatic":false,"skin":null,"hair":0,"hair_colour":0,"face":0,"outfit":0,"clothes_colour":0,"shoes":0,"tool":0,"headwear":0}}}`,
		`{"type":"AGENT_UPDATE","id":"x","body":{"agent_id":"` + agent + `","expected_revision":"7","appearance":{"automatic":true,"skin":1,"hair":0,"hair_colour":0,"face":0,"outfit":0,"clothes_colour":0,"shoes":0,"tool":0,"headwear":0}}}`,
	} {
		if _, err := DecodeClientControl([]byte(frame)); err != ErrMalformed {
			t.Fatalf("%s accepted: %v", frame, err)
		}
	}
	graph := func(nodes string) string {
		return `{"type":"OPERATIONAL_GRAPH","id":"x","body":{"project_id":"01010101010101010101010101010101","digest":"` +
			strings.Repeat("ab", 32) + `","observed_at":1,"sources":[],"nodes":[` + nodes + `],"edges":[],"summary":{"components":1,"inferred":1,"observed":0,"quiet":1,"partial":0,"stale":0,"unobserved":0,"opaque":0,"runtime_only":0,"contradicted":0},"omitted":0}}`
	}
	good := `{"id":"` + node + `","kind":"ingress","label":"GET /x","paths":["a.go"],"evidence":"static","observation":"quiet","state":"idle"}`
	if _, err := DecodeServerControl([]byte(graph(good))); err != nil {
		t.Fatalf("graph refused: %v", err)
	}
	for _, bad := range []string{
		strings.Replace(good, `"id":"`+node+`"`, `"id":"a1"`, 1),
		strings.Replace(good, `"label":"GET /x"`, `"label":""`, 1),
		strings.Replace(good, `"paths":["a.go"],`, ``, 1),
		strings.Replace(good, `"label"`, `"unit":"`+strings.Repeat("cd", 16)+`","label"`, 1),
	} {
		if _, err := DecodeServerControl([]byte(graph(bad))); err != ErrMalformed {
			t.Fatalf("%s accepted: %v", bad, err)
		}
	}
}

func TestPageAccountsUsesCumulativeByteBoundedCursor(t *testing.T) {
	items := make([]DiscoveredAccount, 8193)
	for i := range items {
		items[i] = DiscoveredAccount{Provider: "codex", Home: "/Users/operator/.codex-" + fmt.Sprint(i), Label: "codex", Email: strings.Repeat("e", 128), Organization: strings.Repeat("o", 128), DefaultModel: strings.Repeat("m", 128), DefaultReasoningEffort: strings.Repeat("r", 128)}
	}
	seen := 0
	for offset := uint32(0); ; {
		page, err := PageAccounts(items, offset)
		if err != nil || len(page.Accounts) == 0 {
			t.Fatalf("page at %d = %d accounts, %v", offset, len(page.Accounts), err)
		}
		wire, err := EncodeAccounts("accounts", page)
		if err != nil || len(wire) > MaxSnapshotBytes {
			t.Fatalf("page wire at %d = %d bytes, %v", offset, len(wire), err)
		}
		seen += len(page.Accounts)
		if page.NextOffset == nil {
			break
		}
		if *page.NextOffset != offset+uint32(len(page.Accounts)) {
			t.Fatalf("cursor at %d = %d after %d accounts", offset, *page.NextOffset, len(page.Accounts))
		}
		offset = *page.NextOffset
	}
	if seen != len(items) {
		t.Fatalf("paged %d accounts, want %d", seen, len(items))
	}
}

// The manifest fixture loop proves RUN_PATHS decodes and round-trips. This
// proves the bounds beside it: an agent with no live run is in no room.
func TestRunPathsBounds(t *testing.T) {
	agent := "02020202020202020202020202020202"
	run := "04040404040404040404040404040404"
	runPaths := func(runID, paths string) string {
		return `{"type":"RUN_PATHS","id":"x","body":{"agent_id":"` + agent + `","run_id":"` + runID + `","paths":[` + paths + `]}}`
	}
	if _, err := DecodeServerControl([]byte(runPaths("", ""))); err != nil {
		t.Fatalf("idle agent refused: %v", err)
	}
	rooms := make([]string, MaxJSONArray+1)
	for index := range rooms {
		rooms[index] = `"internal/kernel"`
	}
	for _, frame := range []string{
		runPaths("", `"internal/kernel"`),
		runPaths(run, `""`),
		runPaths(run, strings.Join(rooms, ",")),
		`{"type":"RUN_PATHS","id":"x","body":{"agent_id":"` + agent + `","run_id":"` + run + `","paths":null}}`,
		// Telemetry belongs to a run and stays within JSON's exact integers.
		`{"type":"RUN_PATHS","id":"x","body":{"agent_id":"` + agent + `","run_id":"","paths":[],"telemetry":{"tokens_in":1,"tokens_out":0,"cost_micro_usd":0,"tool_calls":0,"api_requests":0}}}`,
		`{"type":"RUN_PATHS","id":"x","body":{"agent_id":"` + agent + `","run_id":"` + run + `","paths":[],"telemetry":{"tokens_in":9007199254740992,"tokens_out":0,"cost_micro_usd":0,"tool_calls":0,"api_requests":0}}}`,
		`{"type":"RUN_PATHS","id":"x","body":{"agent_id":"` + agent + `","run_id":"` + run + `","paths":[],"telemetry":{"tokens_in":-1,"tokens_out":0,"cost_micro_usd":0,"tool_calls":0,"api_requests":0}}}`,
	} {
		if _, err := DecodeServerControl([]byte(frame)); err != ErrMalformed {
			t.Fatalf("%s accepted: %v", frame, err)
		}
	}
}

// OPERATIONAL_GRAPH may pass the 64 KiB control bound, never the snapshot's.
func TestOperationalGraphIsBoundedBySnapshotBytes(t *testing.T) {
	body := func(count int) OperationalGraph {
		value := OperationalGraph{ProjectID: "01010101010101010101010101010101", Digest: strings.Repeat("ab", 32)}
		for index := range count {
			value.Nodes = append(value.Nodes, GraphNode{ID: fmt.Sprintf("%032x", index+1), Kind: "ingress", Label: strings.Repeat("l", 200),
				Paths: []string{strings.Repeat("p", 200)}, Evidence: "static", Observation: "unobserved", State: "unknown"})
		}
		return value
	}
	wide, err := EncodeOperationalGraph("x", body(512))
	if err != nil || len(wide) <= MaxControlBytes {
		t.Fatalf("graph frame: %d bytes, %v", len(wide), err)
	}
	if frame, err := DecodeServerControl(wide); err != nil || len(frame.Body.(OperationalGraph).Nodes) != 512 {
		t.Fatalf("wide graph decode: %v", err)
	}
	if _, err := DecodeClientControl(wide); err == nil {
		t.Fatal("graph crossed into the client decoder")
	}
	if _, err := EncodeOperationalGraph("x", body(MaxSnapshotEntities)); err != ErrOversized {
		t.Fatalf("oversized graph encoded: %v", err)
	}
}
