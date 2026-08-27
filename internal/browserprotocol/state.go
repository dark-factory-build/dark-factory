package browserprotocol

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"unicode/utf8"
)

const (
	MaxStatePageItems            = 8
	MaxFactoryPageItems          = 1
	MaxCursorBytes               = 256
	MaxProjectNameBytes          = 128
	MaxAgentNameBytes            = 128
	MaxTaskTitleBytes            = 1024
	MaxHumanQuestionBytes        = 8192
	MaxHumanReplyBytes           = 8192
	MaxFactoryCapacity           = 1024
	MaxTaskPriority       int64  = 1_000_000
	MaxSQLiteInteger      uint64 = math.MaxInt64
)

// Decimal is one non-negative SQLite chronology value. JSON represents it as
// a canonical decimal string so JavaScript never truncates it.
type Decimal uint64

func (value Decimal) MarshalJSON() ([]byte, error) {
	if uint64(value) > MaxSQLiteInteger {
		return nil, fmt.Errorf("%w: decimal overflow", ErrMalformed)
	}
	return strconv.AppendQuote(nil, strconv.FormatUint(uint64(value), 10)), nil
}

func (value *Decimal) UnmarshalJSON(data []byte) error {
	var encoded string
	if err := json.Unmarshal(data, &encoded); err != nil {
		return fmt.Errorf("%w: decimal string required", ErrMalformed)
	}
	parsed, err := parseDecimal(encoded)
	if err != nil {
		return err
	}
	*value = parsed
	return nil
}

func parseDecimal(value string) (Decimal, error) {
	if value == "" || len(value) > 19 || len(value) > 1 && value[0] == '0' {
		return 0, fmt.Errorf("%w: non-canonical decimal", ErrMalformed)
	}
	for _, character := range []byte(value) {
		if character < '0' || character > '9' {
			return 0, fmt.Errorf("%w: non-canonical decimal", ErrMalformed)
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed > MaxSQLiteInteger {
		return 0, fmt.Errorf("%w: decimal overflow", ErrMalformed)
	}
	return Decimal(parsed), nil
}

// Bool is an exact JSON boolean. encoding/json otherwise accepts null for a
// bool field and silently turns it into false, which would make Go looser than
// the browser parser at the authority boundary.
type Bool bool

func (value Bool) MarshalJSON() ([]byte, error) {
	if value {
		return []byte("true"), nil
	}
	return []byte("false"), nil
}

func (value *Bool) UnmarshalJSON(data []byte) error {
	switch string(bytes.TrimSpace(data)) {
	case "true":
		*value = true
	case "false":
		*value = false
	default:
		return fmt.Errorf("%w: boolean required", ErrMalformed)
	}
	return nil
}

type StateKind string

const (
	StateFactory      StateKind = "factory"
	StateProject      StateKind = "project"
	StateAgent        StateKind = "agent"
	StateTask         StateKind = "task"
	StateHumanRequest StateKind = "human_request"
)

type StateGet struct {
	Cursor *string `json:"cursor"`
}

type FactoryItem struct {
	DispatchEnabled Bool    `json:"dispatch_enabled"`
	Capacity        uint16  `json:"capacity"`
	ActiveRuns      uint16  `json:"active_runs"`
	Revision        Decimal `json:"revision"`
}

type ProjectItem struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Revision Decimal `json:"revision"`
}

type AgentItem struct {
	ID        string  `json:"id"`
	ProjectID string  `json:"project_id"`
	Name      string  `json:"name"`
	Role      string  `json:"role"`
	Paused    Bool    `json:"paused"`
	Revision  Decimal `json:"revision"`
}

type TaskItem struct {
	ID              string  `json:"id"`
	ProjectID       string  `json:"project_id"`
	AssignedAgentID string  `json:"assigned_agent_id"`
	Title           string  `json:"title"`
	Status          string  `json:"status"`
	Priority        int64   `json:"priority"`
	Revision        Decimal `json:"revision"`
}

// HumanRequestItem is deliberately only the public card projection. Private
// question/reply text and copied display prose cannot be represented here.
type HumanRequestItem struct {
	ID            string  `json:"id"`
	ProjectID     string  `json:"project_id"`
	AgentID       string  `json:"agent_id"`
	TaskID        string  `json:"task_id"`
	CreatedAt     Decimal `json:"created_at"`
	UpdatedAt     Decimal `json:"updated_at"`
	Revision      Decimal `json:"revision"`
	Kind          string  `json:"kind"`
	Status        string  `json:"status"`
	ReplyMaxBytes uint16  `json:"reply_max_bytes"`
	CanReply      Bool    `json:"can_reply"`
}

// StateItems is a closed kind-selected page union.
type StateItems struct {
	kind          StateKind
	factory       []FactoryItem
	projects      []ProjectItem
	agents        []AgentItem
	tasks         []TaskItem
	humanRequests []HumanRequestItem
}

func FactoryItems(items []FactoryItem) StateItems {
	return StateItems{kind: StateFactory, factory: cloneSlice(items)}
}
func ProjectItems(items []ProjectItem) StateItems {
	return StateItems{kind: StateProject, projects: cloneSlice(items)}
}
func AgentItems(items []AgentItem) StateItems {
	return StateItems{kind: StateAgent, agents: cloneSlice(items)}
}
func TaskItems(items []TaskItem) StateItems {
	return StateItems{kind: StateTask, tasks: cloneSlice(items)}
}
func HumanRequestItems(items []HumanRequestItem) StateItems {
	return StateItems{kind: StateHumanRequest, humanRequests: cloneSlice(items)}
}

func cloneSlice[T any](items []T) []T {
	result := make([]T, len(items))
	copy(result, items)
	return result
}

func (items StateItems) Kind() StateKind { return items.kind }
func (items StateItems) Factory() ([]FactoryItem, bool) {
	return append([]FactoryItem(nil), items.factory...), items.kind == StateFactory
}
func (items StateItems) Projects() ([]ProjectItem, bool) {
	return append([]ProjectItem(nil), items.projects...), items.kind == StateProject
}
func (items StateItems) Agents() ([]AgentItem, bool) {
	return append([]AgentItem(nil), items.agents...), items.kind == StateAgent
}
func (items StateItems) Tasks() ([]TaskItem, bool) {
	return append([]TaskItem(nil), items.tasks...), items.kind == StateTask
}
func (items StateItems) HumanRequests() ([]HumanRequestItem, bool) {
	return append([]HumanRequestItem(nil), items.humanRequests...), items.kind == StateHumanRequest
}

func (items StateItems) MarshalJSON() ([]byte, error) {
	switch items.kind {
	case StateFactory:
		return json.Marshal(items.factory)
	case StateProject:
		return json.Marshal(items.projects)
	case StateAgent:
		return json.Marshal(items.agents)
	case StateTask:
		return json.Marshal(items.tasks)
	case StateHumanRequest:
		return json.Marshal(items.humanRequests)
	default:
		return nil, fmt.Errorf("%w: invalid state item union", ErrMalformed)
	}
}

type StateSnapshot struct {
	Head       Decimal
	Kind       StateKind
	Items      StateItems
	NextCursor *string
}

func (value StateSnapshot) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Head       Decimal    `json:"head"`
		Kind       StateKind  `json:"kind"`
		Items      StateItems `json:"items"`
		NextCursor *string    `json:"next_cursor"`
	}{value.Head, value.Kind, value.Items, value.NextCursor})
}

type RestartReason string

const (
	RestartHeadChanged      RestartReason = "head_changed"
	RestartGap              RestartReason = "gap"
	RestartPruned           RestartReason = "pruned"
	RestartHiddenDependency RestartReason = "hidden_dependency"
)

type StateRestart struct {
	Head   Decimal       `json:"head"`
	Floor  Decimal       `json:"floor"`
	Reason RestartReason `json:"reason"`
}

type StateSubscribe struct {
	After Decimal `json:"after"`
}

type StateEventKind string

const (
	EventEntityChanged StateEventKind = "entity_changed"
	EventHiddenAdvance StateEventKind = "hidden_advance"
)

type EntityChanged struct {
	Sequence   Decimal
	Head       Decimal
	EntityKind StateKind
	EntityID   string
	Revision   Decimal
	Deleted    Bool
}

type HiddenAdvance struct {
	Sequence Decimal
	Head     Decimal
}

// StateEvent is a closed event union; cross-variant fields cannot be encoded.
type StateEvent struct {
	kind          StateEventKind
	entityChanged *EntityChanged
	hiddenAdvance *HiddenAdvance
}

func EntityChangedEvent(value EntityChanged) StateEvent {
	return StateEvent{kind: EventEntityChanged, entityChanged: &value}
}
func HiddenAdvanceEvent(value HiddenAdvance) StateEvent {
	return StateEvent{kind: EventHiddenAdvance, hiddenAdvance: &value}
}
func (event StateEvent) Kind() StateEventKind { return event.kind }
func (event StateEvent) EntityChanged() (EntityChanged, bool) {
	if event.entityChanged == nil {
		return EntityChanged{}, false
	}
	return *event.entityChanged, event.kind == EventEntityChanged
}
func (event StateEvent) HiddenAdvance() (HiddenAdvance, bool) {
	if event.hiddenAdvance == nil {
		return HiddenAdvance{}, false
	}
	return *event.hiddenAdvance, event.kind == EventHiddenAdvance
}

func (event StateEvent) MarshalJSON() ([]byte, error) {
	switch event.kind {
	case EventEntityChanged:
		if event.entityChanged == nil {
			break
		}
		value := event.entityChanged
		return json.Marshal(struct {
			Event      StateEventKind `json:"event"`
			Sequence   Decimal        `json:"sequence"`
			Head       Decimal        `json:"head"`
			EntityKind StateKind      `json:"entity_kind"`
			EntityID   string         `json:"entity_id"`
			Revision   Decimal        `json:"revision"`
			Deleted    Bool           `json:"deleted"`
		}{event.kind, value.Sequence, value.Head, value.EntityKind, value.EntityID, value.Revision, value.Deleted})
	case EventHiddenAdvance:
		if event.hiddenAdvance == nil {
			break
		}
		value := event.hiddenAdvance
		return json.Marshal(struct {
			Event    StateEventKind `json:"event"`
			Sequence Decimal        `json:"sequence"`
			Head     Decimal        `json:"head"`
		}{event.kind, value.Sequence, value.Head})
	}
	return nil, fmt.Errorf("%w: invalid state event union", ErrMalformed)
}

type StateEntityGet struct {
	Kind StateKind `json:"kind"`
	ID   string    `json:"id"`
}

// StateItem is the closed union used by one entity refresh. A zero value is
// JSON null and is valid only for a deleted response.
type StateItem struct {
	kind         StateKind
	factory      *FactoryItem
	project      *ProjectItem
	agent        *AgentItem
	task         *TaskItem
	humanRequest *HumanRequestItem
}

func FactoryStateItem(value FactoryItem) StateItem {
	return StateItem{kind: StateFactory, factory: &value}
}
func ProjectStateItem(value ProjectItem) StateItem {
	return StateItem{kind: StateProject, project: &value}
}
func AgentStateItem(value AgentItem) StateItem { return StateItem{kind: StateAgent, agent: &value} }
func TaskStateItem(value TaskItem) StateItem   { return StateItem{kind: StateTask, task: &value} }
func HumanRequestStateItem(value HumanRequestItem) StateItem {
	return StateItem{kind: StateHumanRequest, humanRequest: &value}
}
func DeletedStateItem() StateItem                    { return StateItem{} }
func (item StateItem) Kind() StateKind               { return item.kind }
func (item StateItem) IsDeleted() bool               { return item.kind == "" }
func (item StateItem) Factory() (*FactoryItem, bool) { return item.factory, item.kind == StateFactory }
func (item StateItem) Project() (*ProjectItem, bool) { return item.project, item.kind == StateProject }
func (item StateItem) Agent() (*AgentItem, bool)     { return item.agent, item.kind == StateAgent }
func (item StateItem) Task() (*TaskItem, bool)       { return item.task, item.kind == StateTask }
func (item StateItem) HumanRequest() (*HumanRequestItem, bool) {
	return item.humanRequest, item.kind == StateHumanRequest
}

func (item StateItem) MarshalJSON() ([]byte, error) {
	switch item.kind {
	case "":
		return []byte("null"), nil
	case StateFactory:
		return json.Marshal(item.factory)
	case StateProject:
		return json.Marshal(item.project)
	case StateAgent:
		return json.Marshal(item.agent)
	case StateTask:
		return json.Marshal(item.task)
	case StateHumanRequest:
		return json.Marshal(item.humanRequest)
	default:
		return nil, fmt.Errorf("%w: invalid state item", ErrMalformed)
	}
}

type StateEntity struct {
	Head     Decimal
	Kind     StateKind
	ID       string
	Revision Decimal
	Deleted  Bool
	Item     StateItem
}

func (value StateEntity) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Head     Decimal   `json:"head"`
		Kind     StateKind `json:"kind"`
		ID       string    `json:"id"`
		Revision Decimal   `json:"revision"`
		Deleted  Bool      `json:"deleted"`
		Item     StateItem `json:"item"`
	}{value.Head, value.Kind, value.ID, value.Revision, value.Deleted, value.Item})
}

type HumanRequestDetailGet struct {
	RequestID        string  `json:"request_id"`
	ExpectedRevision Decimal `json:"expected_revision"`
}

type HumanRequestDetail struct {
	RequestID      string                           `json:"request_id"`
	Revision       Decimal                          `json:"revision"`
	Question       string                           `json:"question"`
	CanReply       Bool                             `json:"can_reply"`
	ReplyMaxBytes  uint16                           `json:"reply_max_bytes"`
	TerminalTarget *TerminalTargetDescriptor        `json:"terminal_target"`
	CancelRun      *HumanRequestCancelRunDescriptor `json:"cancel_run"`
}

type HumanRequestCancelRunDescriptor struct {
	ExpectedRequestRevision Decimal `json:"expected_request_revision"`
	ExpectedRunRevision     Decimal `json:"expected_run_revision"`
}

func decodeStateSnapshot(data []byte) (StateSnapshot, error) {
	var wire struct {
		Head       Decimal         `json:"head"`
		Kind       StateKind       `json:"kind"`
		Items      json.RawMessage `json:"items"`
		NextCursor *string         `json:"next_cursor"`
	}
	if err := unmarshalObject(data, &wire); err != nil {
		return StateSnapshot{}, err
	}
	items, err := decodeStateItems(wire.Kind, wire.Items)
	if err != nil {
		return StateSnapshot{}, err
	}
	return StateSnapshot{Head: wire.Head, Kind: wire.Kind, Items: items, NextCursor: wire.NextCursor}, nil
}

func decodeStateItems(kind StateKind, data []byte) (StateItems, error) {
	switch kind {
	case StateFactory:
		var value []FactoryItem
		if err := unmarshalArray(data, &value); err != nil {
			return StateItems{}, err
		}
		return FactoryItems(value), nil
	case StateProject:
		var value []ProjectItem
		if err := unmarshalArray(data, &value); err != nil {
			return StateItems{}, err
		}
		return ProjectItems(value), nil
	case StateAgent:
		var value []AgentItem
		if err := unmarshalArray(data, &value); err != nil {
			return StateItems{}, err
		}
		return AgentItems(value), nil
	case StateTask:
		var value []TaskItem
		if err := unmarshalArray(data, &value); err != nil {
			return StateItems{}, err
		}
		return TaskItems(value), nil
	case StateHumanRequest:
		var value []HumanRequestItem
		if err := unmarshalArray(data, &value); err != nil {
			return StateItems{}, err
		}
		return HumanRequestItems(value), nil
	default:
		return StateItems{}, fmt.Errorf("%w: state kind", ErrMalformed)
	}
}

func decodeStateEvent(data []byte) (StateEvent, error) {
	var discriminator struct {
		Event StateEventKind `json:"event"`
	}
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return StateEvent{}, err
	}
	switch discriminator.Event {
	case EventEntityChanged:
		var value struct {
			Event      StateEventKind `json:"event"`
			Sequence   Decimal        `json:"sequence"`
			Head       Decimal        `json:"head"`
			EntityKind StateKind      `json:"entity_kind"`
			EntityID   string         `json:"entity_id"`
			Revision   Decimal        `json:"revision"`
			Deleted    Bool           `json:"deleted"`
		}
		if err := unmarshalObject(data, &value); err != nil {
			return StateEvent{}, err
		}
		return EntityChangedEvent(EntityChanged{value.Sequence, value.Head, value.EntityKind, value.EntityID, value.Revision, value.Deleted}), nil
	case EventHiddenAdvance:
		var value struct {
			Event    StateEventKind `json:"event"`
			Sequence Decimal        `json:"sequence"`
			Head     Decimal        `json:"head"`
		}
		if err := unmarshalObject(data, &value); err != nil {
			return StateEvent{}, err
		}
		return HiddenAdvanceEvent(HiddenAdvance{value.Sequence, value.Head}), nil
	default:
		return StateEvent{}, fmt.Errorf("%w: state event", ErrMalformed)
	}
}

func decodeStateEntity(data []byte) (StateEntity, error) {
	var wire struct {
		Head     Decimal         `json:"head"`
		Kind     StateKind       `json:"kind"`
		ID       string          `json:"id"`
		Revision Decimal         `json:"revision"`
		Deleted  Bool            `json:"deleted"`
		Item     json.RawMessage `json:"item"`
	}
	if err := unmarshalObject(data, &wire); err != nil {
		return StateEntity{}, err
	}
	item, err := decodeStateItem(wire.Kind, wire.Item)
	if err != nil {
		return StateEntity{}, err
	}
	return StateEntity{wire.Head, wire.Kind, wire.ID, wire.Revision, wire.Deleted, item}, nil
}

func decodeStateItem(kind StateKind, data []byte) (StateItem, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return DeletedStateItem(), nil
	}
	switch kind {
	case StateFactory:
		var value FactoryItem
		if err := unmarshalObject(data, &value); err != nil {
			return StateItem{}, err
		}
		return FactoryStateItem(value), nil
	case StateProject:
		var value ProjectItem
		if err := unmarshalObject(data, &value); err != nil {
			return StateItem{}, err
		}
		return ProjectStateItem(value), nil
	case StateAgent:
		var value AgentItem
		if err := unmarshalObject(data, &value); err != nil {
			return StateItem{}, err
		}
		return AgentStateItem(value), nil
	case StateTask:
		var value TaskItem
		if err := unmarshalObject(data, &value); err != nil {
			return StateItem{}, err
		}
		return TaskStateItem(value), nil
	case StateHumanRequest:
		var value HumanRequestItem
		if err := unmarshalObject(data, &value); err != nil {
			return StateItem{}, err
		}
		return HumanRequestStateItem(value), nil
	default:
		return StateItem{}, fmt.Errorf("%w: state kind", ErrMalformed)
	}
}

func validateStateKind(kind StateKind) error {
	switch kind {
	case StateFactory, StateProject, StateAgent, StateTask, StateHumanRequest:
		return nil
	default:
		return fmt.Errorf("%w: state kind", ErrMalformed)
	}
}

func validateCursor(cursor *string) error {
	if cursor == nil {
		return nil
	}
	if len(*cursor) == 0 || len(*cursor) > MaxCursorBytes {
		return fmt.Errorf("%w: cursor length", ErrMalformed)
	}
	for _, character := range []byte(*cursor) {
		if character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return fmt.Errorf("%w: cursor encoding", ErrMalformed)
	}
	return nil
}

func validateEntityID(kind StateKind, id string) error {
	if err := validateStateKind(kind); err != nil {
		return err
	}
	if kind == StateFactory {
		if id == "factory" {
			return nil
		}
		return fmt.Errorf("%w: factory id", ErrMalformed)
	}
	if _, err := fixedHex("entity id", id, 16); err != nil || id == "00000000000000000000000000000000" {
		return fmt.Errorf("%w: entity id", ErrMalformed)
	}
	return nil
}

func validateBoundedText(value string, minimum, maximum int) error {
	if !utf8.ValidString(value) || len([]byte(value)) < minimum || len([]byte(value)) > maximum {
		return fmt.Errorf("%w: text bound", ErrMalformed)
	}
	return nil
}

func validateFactoryItem(value FactoryItem) error {
	if value.Capacity < 1 || value.Capacity > MaxFactoryCapacity || value.ActiveRuns > MaxFactoryCapacity || value.ActiveRuns > value.Capacity || value.Revision == 0 {
		return fmt.Errorf("%w: factory item", ErrMalformed)
	}
	return nil
}

func validateProjectItem(value ProjectItem) error {
	if validateEntityID(StateProject, value.ID) != nil || validateBoundedText(value.Name, 1, MaxProjectNameBytes) != nil || value.Revision == 0 {
		return fmt.Errorf("%w: project item", ErrMalformed)
	}
	return nil
}

func validateAgentItem(value AgentItem) error {
	if validateEntityID(StateAgent, value.ID) != nil || validateEntityID(StateProject, value.ProjectID) != nil || validateBoundedText(value.Name, 1, MaxAgentNameBytes) != nil || value.Revision == 0 || value.Role != "orchestrator" && value.Role != "worker" {
		return fmt.Errorf("%w: agent item", ErrMalformed)
	}
	return nil
}

func validateTaskItem(value TaskItem) error {
	if validateEntityID(StateTask, value.ID) != nil || validateEntityID(StateProject, value.ProjectID) != nil || validateEntityID(StateAgent, value.AssignedAgentID) != nil || validateBoundedText(value.Title, 1, MaxTaskTitleBytes) != nil || value.Priority < -MaxTaskPriority || value.Priority > MaxTaskPriority || value.Revision == 0 {
		return fmt.Errorf("%w: task item", ErrMalformed)
	}
	switch value.Status {
	case "queued", "running", "blocked", "succeeded", "failed", "cancelled":
		return nil
	default:
		return fmt.Errorf("%w: task status", ErrMalformed)
	}
}

func validateHumanRequestItem(value HumanRequestItem) error {
	if validateEntityID(StateHumanRequest, value.ID) != nil || validateEntityID(StateProject, value.ProjectID) != nil || validateEntityID(StateAgent, value.AgentID) != nil || validateEntityID(StateTask, value.TaskID) != nil || value.UpdatedAt < value.CreatedAt || value.Revision == 0 || value.Kind != "question" || value.ReplyMaxBytes < 1 || value.ReplyMaxBytes > MaxHumanReplyBytes {
		return fmt.Errorf("%w: human request item", ErrMalformed)
	}
	switch value.Status {
	case "open", "delivering", "delivery_unknown":
		return nil
	default:
		return fmt.Errorf("%w: human request status", ErrMalformed)
	}
}

func validateStateItems(items StateItems) error {
	var length int
	switch items.kind {
	case StateFactory:
		length = len(items.factory)
		if length != MaxFactoryPageItems {
			return fmt.Errorf("%w: factory page item count", ErrMalformed)
		}
		for _, item := range items.factory {
			if err := validateFactoryItem(item); err != nil {
				return err
			}
		}
	case StateProject:
		length = len(items.projects)
		for _, item := range items.projects {
			if err := validateProjectItem(item); err != nil {
				return err
			}
		}
	case StateAgent:
		length = len(items.agents)
		for _, item := range items.agents {
			if err := validateAgentItem(item); err != nil {
				return err
			}
		}
	case StateTask:
		length = len(items.tasks)
		for _, item := range items.tasks {
			if err := validateTaskItem(item); err != nil {
				return err
			}
		}
	case StateHumanRequest:
		length = len(items.humanRequests)
		for _, item := range items.humanRequests {
			if err := validateHumanRequestItem(item); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: state item union", ErrMalformed)
	}
	if length > MaxStatePageItems {
		return fmt.Errorf("%w: state page item count", ErrMalformed)
	}
	return nil
}

func stateItemCount(items StateItems) int {
	switch items.kind {
	case StateFactory:
		return len(items.factory)
	case StateProject:
		return len(items.projects)
	case StateAgent:
		return len(items.agents)
	case StateTask:
		return len(items.tasks)
	case StateHumanRequest:
		return len(items.humanRequests)
	default:
		return 0
	}
}

func validateStateItem(item StateItem) error {
	switch item.kind {
	case "":
		return nil
	case StateFactory:
		if item.factory == nil {
			return fmt.Errorf("%w: factory item", ErrMalformed)
		}
		return validateFactoryItem(*item.factory)
	case StateProject:
		if item.project == nil {
			return fmt.Errorf("%w: project item", ErrMalformed)
		}
		return validateProjectItem(*item.project)
	case StateAgent:
		if item.agent == nil {
			return fmt.Errorf("%w: agent item", ErrMalformed)
		}
		return validateAgentItem(*item.agent)
	case StateTask:
		if item.task == nil {
			return fmt.Errorf("%w: task item", ErrMalformed)
		}
		return validateTaskItem(*item.task)
	case StateHumanRequest:
		if item.humanRequest == nil {
			return fmt.Errorf("%w: human request item", ErrMalformed)
		}
		return validateHumanRequestItem(*item.humanRequest)
	default:
		return fmt.Errorf("%w: state item", ErrMalformed)
	}
}

func stateItemID(item StateItem) string {
	switch item.kind {
	case StateFactory:
		return "factory"
	case StateProject:
		return item.project.ID
	case StateAgent:
		return item.agent.ID
	case StateTask:
		return item.task.ID
	case StateHumanRequest:
		return item.humanRequest.ID
	default:
		return ""
	}
}

func stateItemRevision(item StateItem) Decimal {
	switch item.kind {
	case StateFactory:
		return item.factory.Revision
	case StateProject:
		return item.project.Revision
	case StateAgent:
		return item.agent.Revision
	case StateTask:
		return item.task.Revision
	case StateHumanRequest:
		return item.humanRequest.Revision
	default:
		return 0
	}
}

func validateStateGet(value StateGet) error { return validateCursor(value.Cursor) }

func validateStateSnapshot(value StateSnapshot) error {
	if validateStateKind(value.Kind) != nil || value.Items.Kind() != value.Kind || validateCursor(value.NextCursor) != nil {
		return fmt.Errorf("%w: state snapshot", ErrMalformed)
	}
	if err := validateStateItems(value.Items); err != nil {
		return err
	}
	count := stateItemCount(value.Items)
	if value.Kind == StateHumanRequest {
		if count < MaxStatePageItems && value.NextCursor != nil {
			return fmt.Errorf("%w: human request page continuation", ErrMalformed)
		}
		return nil
	}
	if value.NextCursor == nil {
		return fmt.Errorf("%w: state page continuation", ErrMalformed)
	}
	return nil
}

func validateStateRestart(value StateRestart) error {
	// A fresh Store has no events and therefore uses the canonical empty
	// chronology head=0,floor=1. Every other restart is nonempty and cannot
	// place the retention floor beyond its durable head.
	if value.Head == 0 && value.Floor != 1 || value.Head != 0 && value.Floor > value.Head {
		return fmt.Errorf("%w: restart chronology", ErrMalformed)
	}
	switch value.Reason {
	case RestartHeadChanged, RestartGap, RestartPruned, RestartHiddenDependency:
		return nil
	default:
		return fmt.Errorf("%w: restart reason", ErrMalformed)
	}
}

func validateStateSubscribe(StateSubscribe) error { return nil }

func validateStateEvent(value StateEvent) error {
	switch value.kind {
	case EventEntityChanged:
		if value.entityChanged == nil || value.hiddenAdvance != nil {
			return fmt.Errorf("%w: entity event union", ErrMalformed)
		}
		event := value.entityChanged
		if event.Sequence == 0 || event.Head < event.Sequence || event.Revision == 0 || validateEntityID(event.EntityKind, event.EntityID) != nil {
			return fmt.Errorf("%w: entity event", ErrMalformed)
		}
		return nil
	case EventHiddenAdvance:
		if value.hiddenAdvance == nil || value.entityChanged != nil || value.hiddenAdvance.Sequence == 0 || value.hiddenAdvance.Head < value.hiddenAdvance.Sequence {
			return fmt.Errorf("%w: hidden event", ErrMalformed)
		}
		return nil
	default:
		return fmt.Errorf("%w: state event", ErrMalformed)
	}
}

func validateStateEntityGet(value StateEntityGet) error {
	return validateEntityID(value.Kind, value.ID)
}

func validateStateEntity(value StateEntity) error {
	if validateEntityID(value.Kind, value.ID) != nil || validateStateItem(value.Item) != nil || value.Revision == 0 {
		return fmt.Errorf("%w: state entity", ErrMalformed)
	}
	if bool(value.Deleted) != value.Item.IsDeleted() {
		return fmt.Errorf("%w: entity tombstone", ErrMalformed)
	}
	if !value.Deleted && (value.Item.Kind() != value.Kind || stateItemID(value.Item) != value.ID || stateItemRevision(value.Item) != value.Revision) {
		return fmt.Errorf("%w: entity item mismatch", ErrMalformed)
	}
	return nil
}

func validateHumanRequestDetailGet(value HumanRequestDetailGet) error {
	if validateEntityID(StateHumanRequest, value.RequestID) != nil || value.ExpectedRevision == 0 {
		return fmt.Errorf("%w: human request detail request", ErrMalformed)
	}
	return nil
}

func validateHumanRequestDetail(value HumanRequestDetail) error {
	if validateEntityID(StateHumanRequest, value.RequestID) != nil || value.Revision == 0 || validateBoundedText(value.Question, 1, MaxHumanQuestionBytes) != nil || value.ReplyMaxBytes != MaxHumanReplyBytes {
		return fmt.Errorf("%w: human request detail", ErrMalformed)
	}
	if value.TerminalTarget != nil {
		if err := validTerminalTargetDescriptor(*value.TerminalTarget); err != nil {
			return fmt.Errorf("%w: human request terminal target", ErrMalformed)
		}
	}
	if value.CancelRun != nil {
		if value.TerminalTarget == nil || !bool(value.CanReply) || value.CancelRun.ExpectedRequestRevision != value.Revision || value.CancelRun.ExpectedRunRevision != value.TerminalTarget.RunRevision {
			return fmt.Errorf("%w: human request cancellation", ErrMalformed)
		}
	}
	if bool(value.CanReply) && (value.TerminalTarget == nil || value.CancelRun == nil) {
		return fmt.Errorf("%w: human request reply availability", ErrMalformed)
	}
	return nil
}
