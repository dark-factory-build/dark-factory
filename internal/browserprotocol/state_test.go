package browserprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

const (
	projectID = "01010101010101010101010101010101"
	agentID   = "02020202020202020202020202020202"
	taskID    = "03030303030303030303030303030303"
	requestID = "04040404040404040404040404040404"
	runID     = "05050505050505050505050505050505"
)

func factoryItem() FactoryItem {
	return FactoryItem{DispatchEnabled: true, Capacity: 8, ActiveRuns: 2, Revision: 1}
}
func projectItem() ProjectItem { return ProjectItem{ID: projectID, Name: "Factory", Revision: 1} }
func agentItem() AgentItem {
	return AgentItem{ID: agentID, ProjectID: projectID, Name: "Worker", Role: "worker", Revision: 1}
}
func taskItem() TaskItem {
	return TaskItem{ID: taskID, ProjectID: projectID, AssignedAgentID: agentID, Title: "Ship", Status: "queued", Priority: 1, Revision: 1}
}
func humanRequestItem() HumanRequestItem {
	return HumanRequestItem{ID: requestID, ProjectID: projectID, AgentID: agentID, TaskID: taskID, CreatedAt: 10, UpdatedAt: 11, Revision: 1, Kind: "question", Status: "open", ReplyMaxBytes: MaxHumanReplyBytes, CanReply: true}
}

func repeatedStateItems(kind StateKind, count int) StateItems {
	switch kind {
	case StateFactory:
		return FactoryItems(repeat(count, factoryItem))
	case StateProject:
		return ProjectItems(repeat(count, projectItem))
	case StateAgent:
		return AgentItems(repeat(count, agentItem))
	case StateTask:
		return TaskItems(repeat(count, taskItem))
	case StateHumanRequest:
		return HumanRequestItems(repeat(count, humanRequestItem))
	default:
		panic("unknown state kind")
	}
}

func repeat[T any](count int, item func() T) []T {
	result := make([]T, count)
	for index := range result {
		result[index] = item()
	}
	return result
}

func rawControl(t *testing.T, kind MessageType, id string, body any) []byte {
	t.Helper()
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	idJSON, err := json.Marshal(id)
	if err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(controlEnvelope{Version: 1, Type: kind, ID: idJSON, Body: bodyJSON})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestStateFixturesRoundTrip(t *testing.T) {
	for _, name := range []string{"state_get", "state_snapshot", "state_restart", "state_subscribe", "state_event", "state_entity_get", "state_entity", "human_request_detail_get", "human_request_detail"} {
		t.Run(name, func(t *testing.T) {
			wire := fixtureBytes(t, name+".json")
			frame, err := decodeFixtureControl(name, wire)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := encodeDecoded(frame)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded, wire) {
				t.Fatalf("fixture drift:\n got %s\nwant %s", encoded, wire)
			}
		})
	}
	frame, err := DecodeServerControl(fixtureBytes(t, "state_snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	snapshot := frame.Body.(StateSnapshot)
	if snapshot.Head != 9_007_199_254_740_993 || snapshot.Items.Kind() != StateTask {
		t.Fatalf("unsafe chronology truncated: %+v", snapshot)
	}
}

func TestStatePageKindsAndItemCap(t *testing.T) {
	cursor := "YWZ0ZXI"
	for _, kind := range []StateKind{StateProject, StateAgent, StateTask} {
		for _, count := range []int{0, MaxStatePageItems} {
			t.Run(fmt.Sprintf("%s/%d", kind, count), func(t *testing.T) {
				encoded, err := EncodeStateSnapshot("page", StateSnapshot{Head: 1, Kind: kind, Items: repeatedStateItems(kind, count), NextCursor: &cursor})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := DecodeServerControl(encoded); err != nil {
					t.Fatal(err)
				}
			})
		}
		if _, err := EncodeStateSnapshot("page", StateSnapshot{Head: 1, Kind: kind, Items: repeatedStateItems(kind, 1)}); err == nil {
			t.Fatalf("%s accepted a terminal page", kind)
		}
		body := StateSnapshot{Head: 1, Kind: kind, Items: repeatedStateItems(kind, MaxStatePageItems+1)}
		if _, err := EncodeStateSnapshot("page", body); err == nil {
			t.Fatalf("%s encoded nine items", kind)
		}
		if _, err := DecodeServerControl(rawControl(t, TypeStateSnapshot, "page", body)); err != ErrMalformed {
			t.Fatalf("%s decoded nine items: %v", kind, err)
		}
	}
	for _, count := range []int{0, MaxStatePageItems - 1} {
		if _, err := EncodeStateSnapshot("page", StateSnapshot{Head: 1, Kind: StateHumanRequest, Items: repeatedStateItems(StateHumanRequest, count)}); err != nil {
			t.Fatalf("human request terminal count %d: %v", count, err)
		}
		if _, err := EncodeStateSnapshot("page", StateSnapshot{Head: 1, Kind: StateHumanRequest, Items: repeatedStateItems(StateHumanRequest, count), NextCursor: &cursor}); err == nil {
			t.Fatalf("human request short count %d continued", count)
		}
	}
	if _, err := EncodeStateSnapshot("page", StateSnapshot{Head: 1, Kind: StateHumanRequest, Items: repeatedStateItems(StateHumanRequest, MaxStatePageItems), NextCursor: &cursor}); err != nil {
		t.Fatalf("full human request page did not continue: %v", err)
	}
	if _, err := EncodeStateSnapshot("page", StateSnapshot{Head: 1, Kind: StateHumanRequest, Items: repeatedStateItems(StateHumanRequest, MaxStatePageItems)}); err != nil {
		t.Fatalf("full human request final page rejected: %v", err)
	}
	encoded, err := EncodeStateSnapshot("page", StateSnapshot{Head: 1, Kind: StateFactory, Items: FactoryItems([]FactoryItem{factoryItem()}), NextCursor: &cursor})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeServerControl(encoded); err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeStateSnapshot("page", StateSnapshot{Head: 1, Kind: StateFactory, Items: FactoryItems([]FactoryItem{factoryItem()})}); err == nil {
		t.Fatal("factory page terminated")
	}
	for _, count := range []int{0, 2, MaxStatePageItems, MaxStatePageItems + 1} {
		body := StateSnapshot{Head: 1, Kind: StateFactory, Items: repeatedStateItems(StateFactory, count), NextCursor: &cursor}
		if _, err := EncodeStateSnapshot("page", body); err == nil {
			t.Fatalf("factory encoded %d items", count)
		}
		if _, err := DecodeServerControl(rawControl(t, TypeStateSnapshot, "page", body)); err != ErrMalformed {
			t.Fatalf("factory decoded %d items: %v", count, err)
		}
	}
}

func TestStateCursorNullableOpaqueAndBounded(t *testing.T) {
	for _, cursor := range []*string{nil, pointer("A"), pointer(strings.Repeat("_", MaxCursorBytes))} {
		encoded, err := EncodeStateGet("page", StateGet{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeClientControl(encoded); err != nil {
			t.Fatal(err)
		}
	}
	for _, cursor := range []string{"", "a=", "a/b", "a+b", "é", strings.Repeat("a", MaxCursorBytes+1)} {
		if _, err := EncodeStateGet("page", StateGet{Cursor: &cursor}); err == nil {
			t.Fatalf("invalid cursor %q accepted", cursor)
		}
	}
	missing := `{"v":1,"type":"STATE_GET","id":"page","body":{}}`
	if _, err := DecodeClientControl([]byte(missing)); err != ErrMalformed {
		t.Fatalf("missing cursor accepted: %v", err)
	}
}

func pointer(value string) *string { return &value }

func TestDecimalCanonicalBoundaries(t *testing.T) {
	valid := []uint64{0, 1, (1 << 53) - 1, 1 << 53, (1 << 53) + 1, math.MaxInt64}
	for _, value := range valid {
		encoded := strconvUint(value)
		parsed, err := parseDecimal(encoded)
		if err != nil || uint64(parsed) != value {
			t.Fatalf("decimal %s: parsed=%d err=%v", encoded, parsed, err)
		}
		wire, err := EncodeStateSubscribe("watch", StateSubscribe{After: Decimal(value)})
		if err != nil {
			t.Fatal(err)
		}
		frame, err := DecodeClientControl(wire)
		if err != nil || uint64(frame.Body.(StateSubscribe).After) != value {
			t.Fatalf("wire decimal %s: %+v %v", encoded, frame, err)
		}
	}
	for _, value := range []string{"", "+1", "-1", "-0", "00", "01", " 1", "1 ", "9223372036854775808", "18446744073709551615", "1.0", "1e0"} {
		if _, err := parseDecimal(value); err == nil {
			t.Fatalf("invalid decimal %q accepted", value)
		}
		wire := fmt.Sprintf(`{"v":1,"type":"STATE_SUBSCRIBE","id":"watch","body":{"after":%q}}`, value)
		if _, err := DecodeClientControl([]byte(wire)); err != ErrMalformed {
			t.Fatalf("wire decimal %q accepted: %v", value, err)
		}
	}
	for _, number := range []string{"0", "1", "9007199254740991", "9007199254740992", "9223372036854775807", "-1"} {
		wire := fmt.Sprintf(`{"v":1,"type":"STATE_SUBSCRIBE","id":"watch","body":{"after":%s}}`, number)
		if _, err := DecodeClientControl([]byte(wire)); err != ErrMalformed {
			t.Fatalf("JSON number chronology %s accepted: %v", number, err)
		}
	}
	if _, err := EncodeStateSubscribe("watch", StateSubscribe{After: Decimal(MaxSQLiteInteger + 1)}); !errors.Is(err, ErrMalformed) {
		t.Fatalf("overflow encode error = %v", err)
	}
}

func strconvUint(value uint64) string { return fmt.Sprintf("%d", value) }

func TestStateExactFieldAndEnumBounds(t *testing.T) {
	badFactories := []FactoryItem{
		{Capacity: 0, Revision: 1},
		{Capacity: MaxFactoryCapacity + 1, Revision: 1},
		{Capacity: 1, ActiveRuns: 2, Revision: 1},
		{Capacity: 1, Revision: 0},
	}
	for _, item := range badFactories {
		if _, err := EncodeStateSnapshot("x", StateSnapshot{Head: 1, Kind: StateFactory, Items: FactoryItems([]FactoryItem{item})}); err == nil {
			t.Fatalf("invalid factory accepted: %+v", item)
		}
	}
	badProjects := []ProjectItem{{ID: projectID, Name: "", Revision: 1}, {ID: projectID, Name: strings.Repeat("x", MaxProjectNameBytes+1), Revision: 1}, {ID: strings.Repeat("AB", 16), Name: "x", Revision: 1}, {ID: projectID, Name: "x", Revision: 0}}
	for _, item := range badProjects {
		if _, err := EncodeStateSnapshot("x", StateSnapshot{Head: 1, Kind: StateProject, Items: ProjectItems([]ProjectItem{item})}); err == nil {
			t.Fatalf("invalid project accepted: %+v", item)
		}
	}
	badAgent := agentItem()
	badAgent.Role = "manager"
	if _, err := EncodeStateSnapshot("x", StateSnapshot{Head: 1, Kind: StateAgent, Items: AgentItems([]AgentItem{badAgent})}); err == nil {
		t.Fatal("unknown agent role accepted")
	}
	for _, status := range []string{"", "ready", "OPEN"} {
		item := taskItem()
		item.Status = status
		if _, err := EncodeStateSnapshot("x", StateSnapshot{Head: 1, Kind: StateTask, Items: TaskItems([]TaskItem{item})}); err == nil {
			t.Fatalf("task status %q accepted", status)
		}
	}
	for _, priority := range []int64{-MaxTaskPriority - 1, MaxTaskPriority + 1} {
		item := taskItem()
		item.Priority = priority
		if _, err := EncodeStateSnapshot("x", StateSnapshot{Head: 1, Kind: StateTask, Items: TaskItems([]TaskItem{item})}); err == nil {
			t.Fatalf("priority %d accepted", priority)
		}
	}
	item := taskItem()
	item.Title = strings.Repeat("x", MaxTaskTitleBytes+1)
	if _, err := EncodeStateSnapshot("x", StateSnapshot{Head: 1, Kind: StateTask, Items: TaskItems([]TaskItem{item})}); err == nil {
		t.Fatal("oversized task title accepted")
	}
	for _, edit := range []func(*HumanRequestItem){
		func(v *HumanRequestItem) { v.Kind = "reply" },
		func(v *HumanRequestItem) { v.Status = "resolved" },
		func(v *HumanRequestItem) { v.UpdatedAt = v.CreatedAt - 1 },
		func(v *HumanRequestItem) { v.ReplyMaxBytes = 0 },
		func(v *HumanRequestItem) { v.ReplyMaxBytes = MaxHumanReplyBytes + 1 },
	} {
		item := humanRequestItem()
		edit(&item)
		if _, err := EncodeStateSnapshot("x", StateSnapshot{Head: 1, Kind: StateHumanRequest, Items: HumanRequestItems([]HumanRequestItem{item})}); err == nil {
			t.Fatalf("invalid human request accepted: %+v", item)
		}
	}
}

func TestFactoryLiteralAndDynamicEntityIDs(t *testing.T) {
	if _, err := EncodeStateEntityGet("x", StateEntityGet{Kind: StateFactory, ID: "factory"}); err != nil {
		t.Fatal(err)
	}
	for _, test := range []StateEntityGet{
		{Kind: StateFactory, ID: projectID},
		{Kind: StateProject, ID: "factory"},
		{Kind: StateProject, ID: strings.Repeat("0", 32)},
		{Kind: StateProject, ID: strings.Repeat("AB", 16)},
		{Kind: "run", ID: projectID},
	} {
		if _, err := EncodeStateEntityGet("x", test); err == nil {
			t.Fatalf("invalid entity identity accepted: %+v", test)
		}
	}
}

func TestStateEventVariantsAndChronology(t *testing.T) {
	entity := EntityChangedEvent(EntityChanged{Sequence: 8, Head: 8, EntityKind: StateTask, EntityID: taskID, Revision: 2})
	hidden := HiddenAdvanceEvent(HiddenAdvance{Sequence: 9, Head: 10})
	for _, value := range []StateEvent{entity, hidden} {
		wire, err := EncodeStateEvent("watch", value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeServerControl(wire); err != nil {
			t.Fatal(err)
		}
	}
	malformed := []string{
		`{"v":1,"type":"STATE_EVENT","id":"watch","body":{"event":"hidden_advance","sequence":"9","head":"9","entity_kind":"task"}}`,
		`{"v":1,"type":"STATE_EVENT","id":"watch","body":{"event":"entity_changed","sequence":"9","head":"9"}}`,
		`{"v":1,"type":"STATE_EVENT","id":"watch","body":{"event":"unknown","sequence":"9","head":"9"}}`,
	}
	for _, wire := range malformed {
		if _, err := DecodeServerControl([]byte(wire)); err != ErrMalformed {
			t.Fatalf("cross-variant event accepted: %s: %v", wire, err)
		}
	}
	for _, value := range []StateEvent{
		HiddenAdvanceEvent(HiddenAdvance{Sequence: 0, Head: 0}),
		HiddenAdvanceEvent(HiddenAdvance{Sequence: 2, Head: 1}),
		EntityChangedEvent(EntityChanged{Sequence: 2, Head: 2, EntityKind: StateTask, EntityID: taskID, Revision: 0}),
	} {
		if _, err := EncodeStateEvent("watch", value); err == nil {
			t.Fatalf("invalid event accepted: %+v", value)
		}
	}
}

func TestStateEntityTombstoneAndKindConsistency(t *testing.T) {
	valid := []StateEntity{
		{Head: 1, Kind: StateFactory, ID: "factory", Revision: 1, Item: FactoryStateItem(factoryItem())},
		{Head: 1, Kind: StateProject, ID: projectID, Revision: 1, Item: ProjectStateItem(projectItem())},
		{Head: 1, Kind: StateProject, ID: projectID, Revision: 2, Deleted: true, Item: DeletedStateItem()},
	}
	for _, value := range valid {
		wire, err := EncodeStateEntity("entity", value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeServerControl(wire); err != nil {
			t.Fatal(err)
		}
	}
	invalid := []StateEntity{
		{Head: 1, Kind: StateProject, ID: projectID, Revision: 1, Deleted: true, Item: ProjectStateItem(projectItem())},
		{Head: 1, Kind: StateProject, ID: projectID, Revision: 1, Item: DeletedStateItem()},
		{Head: 1, Kind: StateProject, ID: projectID, Revision: 1, Item: AgentStateItem(agentItem())},
		{Head: 1, Kind: StateProject, ID: "06060606060606060606060606060606", Revision: 1, Item: ProjectStateItem(projectItem())},
		{Head: 1, Kind: StateProject, ID: projectID, Revision: 2, Item: ProjectStateItem(projectItem())},
		{Head: 1, Kind: StateProject, ID: projectID, Item: ProjectStateItem(projectItem())},
	}
	for _, value := range invalid {
		if _, err := EncodeStateEntity("entity", value); err == nil {
			t.Fatalf("inconsistent entity accepted: %+v", value)
		}
	}
	wrongKind := strings.Replace(string(fixtureBytes(t, "state_entity.json")), `"kind":"human_request"`, `"kind":"task"`, 1)
	if _, err := DecodeServerControl([]byte(wrongKind)); err != ErrMalformed {
		t.Fatalf("wrong-kind item accepted: %v", err)
	}
	missingRevision := strings.Replace(string(fixtureBytes(t, "state_entity.json")), `"revision":"2","deleted"`, `"deleted"`, 1)
	if _, err := DecodeServerControl([]byte(missingRevision)); err != ErrMalformed {
		t.Fatalf("missing entity revision accepted: %v", err)
	}
	mismatchedRevision := strings.Replace(string(fixtureBytes(t, "state_entity.json")), `"revision":"2","deleted"`, `"revision":"3","deleted"`, 1)
	if _, err := DecodeServerControl([]byte(mismatchedRevision)); err != ErrMalformed {
		t.Fatalf("mismatched entity revision accepted: %v", err)
	}
}

func TestStateBooleanNullIsAlwaysRejected(t *testing.T) {
	serverFrames := []struct {
		name  string
		frame []byte
		field string
		value string
	}{
		{"factory dispatch", mustEncodeStateSnapshot(t, StateSnapshot{Head: 1, Kind: StateFactory, Items: FactoryItems([]FactoryItem{factoryItem()}), NextCursor: pointer("next")}), "dispatch_enabled", "true"},
		{"agent paused", mustEncodeStateSnapshot(t, StateSnapshot{Head: 1, Kind: StateAgent, Items: AgentItems([]AgentItem{agentItem()}), NextCursor: pointer("next")}), "paused", "false"},
		{"human can reply", mustEncodeStateSnapshot(t, StateSnapshot{Head: 1, Kind: StateHumanRequest, Items: HumanRequestItems([]HumanRequestItem{humanRequestItem()})}), "can_reply", "true"},
		{"event deleted", mustEncodeStateEvent(t, EntityChangedEvent(EntityChanged{Sequence: 1, Head: 1, EntityKind: StateTask, EntityID: taskID, Revision: 1})), "deleted", "false"},
		{"entity deleted", mustEncodeStateEntity(t, StateEntity{Head: 1, Kind: StateTask, ID: taskID, Revision: 1, Item: TaskStateItem(taskItem())}), "deleted", "false"},
		{"error retryable", mustEncodeError(t, Error{Code: ErrorInvalidRequest}), "retryable", "false"},
	}
	for _, test := range serverFrames {
		t.Run(test.name, func(t *testing.T) {
			wire := strings.Replace(string(test.frame), fmt.Sprintf(`%q:%s`, test.field, test.value), fmt.Sprintf(`%q:null`, test.field), 1)
			if wire == string(test.frame) {
				t.Fatalf("test field %s not found in %s", test.field, test.frame)
			}
			if _, err := DecodeServerControl([]byte(wire)); err != ErrMalformed {
				t.Fatalf("null boolean accepted: %v", err)
			}
		})
	}
}

func mustEncodeStateSnapshot(t *testing.T, value StateSnapshot) []byte {
	t.Helper()
	wire, err := EncodeStateSnapshot("boolean", value)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func mustEncodeStateEvent(t *testing.T, value StateEvent) []byte {
	t.Helper()
	wire, err := EncodeStateEvent("boolean", value)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func mustEncodeStateEntity(t *testing.T, value StateEntity) []byte {
	t.Helper()
	wire, err := EncodeStateEntity("boolean", value)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func mustEncodeError(t *testing.T, value Error) []byte {
	t.Helper()
	wire, err := EncodeError("boolean", value)
	if err != nil {
		t.Fatal(err)
	}
	return wire
}

func TestHumanRequestPublicPrivacyAndDetailBounds(t *testing.T) {
	fields := reflect.VisibleFields(reflect.TypeOf(HumanRequestItem{}))
	actual := make([]string, 0, len(fields))
	for _, field := range fields {
		actual = append(actual, field.Tag.Get("json"))
	}
	want := []string{"id", "project_id", "agent_id", "task_id", "created_at", "updated_at", "revision", "kind", "status", "reply_max_bytes", "can_reply"}
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("public HumanRequest fields drifted: got %v want %v", actual, want)
	}
	encoded, err := EncodeStateSnapshot("state", StateSnapshot{Head: 1, Kind: StateHumanRequest, Items: HumanRequestItems([]HumanRequestItem{humanRequestItem()})})
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{`"run_id":`, `"question":`, `"reply":`, `"terminal_target":`, `"cancel_run":`, `"action":`, `"project_name":`, `"agent_name":`, `"task_title":`, `"summary":`, `"why_human_needed":`} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatalf("public item exposed private field %q: %s", private, encoded)
		}
	}
	for _, question := range []string{"x", strings.Repeat("x", MaxHumanQuestionBytes), strings.Repeat("\x00", MaxHumanQuestionBytes)} {
		if !utf8.ValidString(question) {
			t.Fatal("test question invalid")
		}
		wire, err := EncodeHumanRequestDetail("detail", HumanRequestDetail{RequestID: requestID, Revision: 1, Question: question, ReplyMaxBytes: MaxHumanReplyBytes})
		if err != nil {
			t.Fatal(err)
		}
		if len(wire) > MaxControlBytes {
			t.Fatalf("detail frame %d exceeds max", len(wire))
		}
	}
	for _, question := range []string{"", strings.Repeat("x", MaxHumanQuestionBytes+1), string([]byte{0xff})} {
		if _, err := EncodeHumanRequestDetail("detail", HumanRequestDetail{RequestID: requestID, Revision: 1, Question: question, ReplyMaxBytes: MaxHumanReplyBytes}); err == nil {
			t.Fatalf("invalid question accepted: %d bytes", len(question))
		}
	}
	if _, err := EncodeHumanRequestDetailGet("detail", HumanRequestDetailGet{RequestID: requestID}); err == nil {
		t.Fatal("zero expected revision accepted")
	}
	target := TerminalTargetDescriptor{RunID: "11111111111111111111111111111111", SessionID: "22222222222222222222222222222222", RunRevision: 8, SessionRevision: 9}
	cancelRun := HumanRequestCancelRunDescriptor{ExpectedRequestRevision: 2, ExpectedRunRevision: 8}
	authorized := HumanRequestDetail{RequestID: requestID, Revision: 2, Question: "choose", CanReply: true, ReplyMaxBytes: MaxHumanReplyBytes, TerminalTarget: &target, CancelRun: &cancelRun}
	if _, err := EncodeHumanRequestDetail("detail", authorized); err != nil {
		t.Fatalf("authorized detail = %v", err)
	}
	for name, mutate := range map[string]func(*HumanRequestDetail){
		"reply without target": func(value *HumanRequestDetail) { value.TerminalTarget = nil },
		"reply without cancel": func(value *HumanRequestDetail) { value.CancelRun = nil },
		"cancel without reply": func(value *HumanRequestDetail) { value.CanReply = false },
		"request revision":     func(value *HumanRequestDetail) { value.CancelRun.ExpectedRequestRevision++ },
		"run revision":         func(value *HumanRequestDetail) { value.CancelRun.ExpectedRunRevision++ },
	} {
		t.Run(name, func(t *testing.T) {
			value := authorized
			copiedTarget, copiedCancelRun := *authorized.TerminalTarget, *authorized.CancelRun
			value.TerminalTarget, value.CancelRun = &copiedTarget, &copiedCancelRun
			mutate(&value)
			if _, err := EncodeHumanRequestDetail("detail", value); err == nil {
				t.Fatalf("inconsistent detail accepted: %+v", value)
			}
		})
	}
}

func TestMaximallyEscapedFramesStayBounded(t *testing.T) {
	tasks := make([]TaskItem, MaxStatePageItems)
	for index := range tasks {
		tasks[index] = taskItem()
		tasks[index].ID = fmt.Sprintf("%032x", index+1)
		tasks[index].Title = strings.Repeat("\x00", MaxTaskTitleBytes)
	}
	page, err := EncodeStateSnapshot("max-page", StateSnapshot{Head: Decimal(MaxSQLiteInteger), Kind: StateTask, Items: TaskItems(tasks), NextCursor: pointer(strings.Repeat("_", MaxCursorBytes))})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := EncodeHumanRequestDetail("max-detail", HumanRequestDetail{RequestID: requestID, Revision: Decimal(MaxSQLiteInteger), Question: strings.Repeat("\x00", MaxHumanQuestionBytes), ReplyMaxBytes: MaxHumanReplyBytes})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) < 49_000 || len(page) > MaxControlBytes || len(detail) < 49_000 || len(detail) > MaxControlBytes {
		t.Fatalf("unexpected maximal frame sizes: page=%d detail=%d max=%d", len(page), len(detail), MaxControlBytes)
	}
	t.Logf("maximally escaped task page=%d bytes, question detail=%d bytes, limit=%d", len(page), len(detail), MaxControlBytes)
}

func TestStateMalformedEnvelopeBodyAndGlobalBounds(t *testing.T) {
	valid := string(fixtureBytes(t, "state_snapshot.json"))
	cases := []string{
		strings.Replace(valid, `"head"`, `"Head"`, 1),
		strings.Replace(valid, `"kind"`, `"Kind"`, 1),
		strings.Replace(valid, `"title"`, `"TITLE"`, 1),
		strings.Replace(valid, `"title"`, `"extra":false,"title"`, 1),
		strings.Replace(valid, `"head":"9007199254740993"`, `"head":"9007199254740993","head":"2"`, 1),
		valid + `{}`,
	}
	for _, wire := range cases {
		if _, err := DecodeServerControl([]byte(wire)); err != ErrMalformed {
			t.Fatalf("malformed state accepted: %v\n%s", err, wire)
		}
	}
	objectMembers := strings.Repeat(`"x":0,`, MaxJSONObject) + `"tail":0`
	tooManyMembers := `{"v":1,"type":"STATE_SUBSCRIBE","id":"watch","body":{` + objectMembers + `}}`
	if _, err := DecodeClientControl([]byte(tooManyMembers)); err != ErrMalformed {
		t.Fatalf("object member bound error = %v", err)
	}
	tooManyArray := `{"v":1,"type":"STATE_SNAPSHOT","id":"state","body":{"head":"1","kind":"task","items":[` + strings.TrimSuffix(strings.Repeat(`{},`, MaxJSONArray+1), ",") + `],"next_cursor":null}}`
	if _, err := DecodeServerControl([]byte(tooManyArray)); err != ErrMalformed {
		t.Fatalf("array bound error = %v", err)
	}
	if _, err := DecodeServerControl(append(fixtureBytes(t, "state_snapshot.json"), 0xff)); err != ErrMalformed {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	if _, err := DecodeServerControl(bytes.Repeat([]byte{' '}, MaxControlBytes+1)); !errors.Is(err, ErrOversized) {
		t.Fatalf("oversize error = %v", err)
	}
}

func TestErrorTooLargeIsFinite(t *testing.T) {
	wire, err := EncodeError("state", Error{Code: ErrorTooLarge})
	if err != nil {
		t.Fatal(err)
	}
	frame, err := DecodeServerControl(wire)
	if err != nil || frame.Body.(Error).Code != ErrorTooLarge {
		t.Fatalf("too_large round trip: %+v %v", frame, err)
	}
}
