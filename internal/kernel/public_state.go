package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

const (
	PublicStatePageSize    = 8
	PublicStateEntityLimit = 4096
)

// PublicStateKind is the complete browser-visible entity set. It is separate
// from EntityKind so hidden run and Change invalidations cannot be paged by
// accidentally accepting an otherwise valid durable kind.
type PublicStateKind uint8

const (
	PublicStateFactory PublicStateKind = iota + 1
	PublicStateProject
	PublicStateAgent
	PublicStateTask
	PublicStateHumanRequest
)

func (kind PublicStateKind) String() string {
	switch kind {
	case PublicStateFactory:
		return "factory"
	case PublicStateProject:
		return "project"
	case PublicStateAgent:
		return "agent"
	case PublicStateTask:
		return "task"
	case PublicStateHumanRequest:
		return "human_request"
	default:
		return ""
	}
}

func (kind PublicStateKind) next() PublicStateKind {
	if kind >= PublicStateFactory && kind < PublicStateHumanRequest {
		return kind + 1
	}
	return 0
}

// PublicStateID represents either the literal public factory identity or one
// nonzero durable 16-byte dynamic identity. Its fields stay private so the
// all-zero SQLite factory sentinel cannot escape through this API.
type PublicStateID struct {
	factory bool
	dynamic identifier
}

func FactoryPublicStateID() PublicStateID { return PublicStateID{factory: true} }

func PublicStateIDFromBytes(value []byte) (PublicStateID, error) {
	id, err := identifierFromBytes(value)
	if err != nil {
		return PublicStateID{}, err
	}
	return PublicStateID{dynamic: id}, nil
}

func (id PublicStateID) String() string {
	if id.factory && id.dynamic.zero() {
		return "factory"
	}
	if !id.factory && !id.dynamic.zero() {
		return id.dynamic.String()
	}
	return ""
}

func (id PublicStateID) Bytes() []byte {
	if id.factory || id.dynamic.zero() {
		return nil
	}
	return id.dynamic.bytes()
}

func (id PublicStateID) validFactory() bool { return id.factory && id.dynamic.zero() }
func (id PublicStateID) validDynamic() bool { return !id.factory && !id.dynamic.zero() }

// PublicStateCursor is the decoded neutral cursor. AfterID is absent at the
// start of a kind; cursor string encoding belongs to the protocol adapter.
type PublicStateCursor struct {
	Head    EventSequence
	Kind    PublicStateKind
	AfterID *PublicStateID
}

// PublicStateItem is a closed union. Store results populate exactly one
// variant; private pointers prevent callers from constructing a multi-variant
// item or mutating a projection through it.
type PublicStateItem struct {
	factory      *FactorySummary
	project      *ProjectSummary
	agent        *AgentSummary
	task         *TaskSummary
	humanRequest *HumanRequestProjection
}

func (item PublicStateItem) Factory() (FactorySummary, bool) {
	if item.factory == nil {
		return FactorySummary{}, false
	}
	return *item.factory, true
}

func (item PublicStateItem) Project() (ProjectSummary, bool) {
	if item.project == nil {
		return ProjectSummary{}, false
	}
	return *item.project, true
}

func (item PublicStateItem) Agent() (AgentSummary, bool) {
	if item.agent == nil {
		return AgentSummary{}, false
	}
	return *item.agent, true
}

func (item PublicStateItem) Task() (TaskSummary, bool) {
	if item.task == nil {
		return TaskSummary{}, false
	}
	return *item.task, true
}

func (item PublicStateItem) HumanRequest() (HumanRequestProjection, bool) {
	if item.humanRequest == nil {
		return HumanRequestProjection{}, false
	}
	return *item.humanRequest, true
}

func (item PublicStateItem) id() PublicStateID {
	switch {
	case item.factory != nil:
		return FactoryPublicStateID()
	case item.project != nil:
		return PublicStateID{dynamic: item.project.ID.identifier}
	case item.agent != nil:
		return PublicStateID{dynamic: item.agent.ID.identifier}
	case item.task != nil:
		return PublicStateID{dynamic: item.task.ID.identifier}
	case item.humanRequest != nil:
		return PublicStateID{dynamic: item.humanRequest.ID.identifier}
	default:
		return PublicStateID{}
	}
}

func (item PublicStateItem) revision() (Revision, bool) {
	switch {
	case item.factory != nil:
		return item.factory.Revision, true
	case item.project != nil:
		return item.project.Revision, true
	case item.agent != nil:
		return item.agent.Revision, true
	case item.task != nil:
		return item.task.Revision, true
	case item.humanRequest != nil:
		return item.humanRequest.Revision, true
	default:
		return Revision{}, false
	}
}

type PublicStatePageResult struct {
	Head       EventSequence
	Kind       PublicStateKind
	Items      []PublicStateItem
	NextCursor *PublicStateCursor
}

type PublicStateEntityResult struct {
	Head     EventSequence
	Kind     PublicStateKind
	EntityID PublicStateID
	Revision Revision
	Deleted  bool
	Item     *PublicStateItem
}

// PublicStateRestartError is the bounded continuation result for a cursor
// whose pinned head is no longer current. The adapter maps it to head_changed;
// no page items are returned and callers restart from a nil cursor.
type PublicStateRestartError struct {
	Head  EventSequence
	Floor EventSequence
}

func (err *PublicStateRestartError) Error() string {
	return fmt.Sprintf("public state restart required: head=%d floor=%d", err.Head.Int64(), err.Floor.Int64())
}

func (store *Store) ReadPublicStatePage(ctx context.Context, cursor *PublicStateCursor) (PublicStatePageResult, error) {
	if err := validatePublicStateCursor(cursor); err != nil {
		return PublicStatePageResult{}, err
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return PublicStatePageResult{}, err
	}
	defer tx.Close()
	if err := validateDurableControls(ctx, tx.connection); err != nil {
		return PublicStatePageResult{}, err
	}
	state, err := factoryState(ctx, tx.connection)
	if err != nil {
		return PublicStatePageResult{}, err
	}
	if cursor != nil && cursor.Head != state.Head {
		return PublicStatePageResult{}, &PublicStateRestartError{Head: state.Head, Floor: state.Floor}
	}
	if err := enforcePublicStateCount(ctx, tx.connection); err != nil {
		return PublicStatePageResult{}, err
	}
	kind := PublicStateFactory
	var after *PublicStateID
	if cursor != nil {
		kind = cursor.Kind
		after = cursor.AfterID
		if after != nil {
			found, err := publicStateAfterExists(ctx, tx.connection, kind, *after)
			if err != nil {
				return PublicStatePageResult{}, err
			}
			if !found {
				return PublicStatePageResult{}, fmt.Errorf("%w: public state cursor identity is not current", ErrInvalidValue)
			}
		}
	}
	items, err := readPublicStateItems(ctx, tx.connection, kind, after)
	if err != nil {
		return PublicStatePageResult{}, err
	}
	result := PublicStatePageResult{Head: state.Head, Kind: kind, Items: items}
	if len(result.Items) > PublicStatePageSize {
		result.Items = result.Items[:PublicStatePageSize]
		last := result.Items[PublicStatePageSize-1].id()
		result.NextCursor = &PublicStateCursor{Head: state.Head, Kind: kind, AfterID: &last}
	} else if next := kind.next(); next != 0 {
		result.NextCursor = &PublicStateCursor{Head: state.Head, Kind: next}
	}
	return result, nil
}

func validatePublicStateCursor(cursor *PublicStateCursor) error {
	if cursor == nil {
		return nil
	}
	if cursor.Kind.String() == "" {
		return fmt.Errorf("%w: unknown public state kind", ErrInvalidValue)
	}
	if cursor.Kind == PublicStateFactory {
		if cursor.AfterID != nil {
			return fmt.Errorf("%w: factory cursor has an after identity", ErrInvalidValue)
		}
		return nil
	}
	if cursor.AfterID != nil && !cursor.AfterID.validDynamic() {
		return fmt.Errorf("%w: invalid public state cursor identity", ErrInvalidValue)
	}
	return nil
}

func enforcePublicStateCount(ctx context.Context, connection *sql.Conn) error {
	var projects, agents, tasks, requests int64
	err := connection.QueryRowContext(ctx, `SELECT
        (SELECT COUNT(*) FROM projects),
        (SELECT COUNT(*) FROM agents),
        (SELECT COUNT(*) FROM tasks),
        (SELECT COUNT(*) FROM human_requests WHERE status IN ('open', 'delivering', 'delivery_unknown'))`).Scan(&projects, &agents, &tasks, &requests)
	if err != nil {
		return fmt.Errorf("count public state: %w", err)
	}
	if projects < 0 || agents < 0 || tasks < 0 || requests < 0 {
		return fmt.Errorf("%w: invalid public state count", ErrCorruptState)
	}
	if projects >= PublicStateEntityLimit || agents >= PublicStateEntityLimit || tasks >= PublicStateEntityLimit || requests >= PublicStateEntityLimit {
		return ErrSnapshotTooLarge
	}
	if 1+projects+agents+tasks+requests > PublicStateEntityLimit {
		return ErrSnapshotTooLarge
	}
	return nil
}

func publicStateAfterExists(ctx context.Context, connection *sql.Conn, kind PublicStateKind, id PublicStateID) (bool, error) {
	var query string
	switch kind {
	case PublicStateProject:
		query = `SELECT 1 FROM projects WHERE id = ?`
	case PublicStateAgent:
		query = `SELECT 1 FROM agents WHERE id = ?`
	case PublicStateTask:
		query = `SELECT 1 FROM tasks WHERE id = ?`
	case PublicStateHumanRequest:
		query = `SELECT 1 FROM human_requests WHERE id = ? AND status IN ('open', 'delivering', 'delivery_unknown')`
	default:
		return false, fmt.Errorf("%w: kind cannot have an after identity", ErrInvalidValue)
	}
	var one int
	err := connection.QueryRowContext(ctx, query, id.Bytes()).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return one == 1, nil
}

func readPublicStateItems(ctx context.Context, connection *sql.Conn, kind PublicStateKind, after *PublicStateID) ([]PublicStateItem, error) {
	if kind == PublicStateFactory {
		summary, err := publicFactorySummary(ctx, connection)
		if err != nil {
			return nil, err
		}
		return []PublicStateItem{{factory: &summary}}, nil
	}
	if kind == PublicStateHumanRequest {
		return readPublicHumanRequestItems(ctx, connection, after)
	}
	rows, err := queryPublicStateRows(ctx, connection, kind, after)
	if err != nil {
		return nil, fmt.Errorf("read %s public state page: %w", kind.String(), err)
	}
	defer rows.Close()
	items := make([]PublicStateItem, 0, PublicStatePageSize+1)
	for rows.Next() {
		item, err := scanPublicStateItem(rows, kind)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func readPublicHumanRequestItems(ctx context.Context, connection *sql.Conn, after *PublicStateID) ([]PublicStateItem, error) {
	rows, err := queryPublicStateRows(ctx, connection, PublicStateHumanRequest, after)
	if err != nil {
		return nil, fmt.Errorf("read human_request public state page: %w", err)
	}
	var ids []HumanRequestID
	for rows.Next() {
		var rawID []byte
		if err := rows.Scan(&rawID); err != nil {
			rows.Close()
			return nil, err
		}
		id, err := HumanRequestIDFromBytes(rawID)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("%w: invalid human request page identity", ErrCorruptState)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	items := make([]PublicStateItem, 0, len(ids))
	for _, id := range ids {
		projection, found, err := humanRequestProjectionByID(ctx, connection, id)
		if err != nil || !found {
			return nil, publicScanError(err)
		}
		items = append(items, PublicStateItem{humanRequest: &projection})
	}
	return items, nil
}

func queryPublicStateRows(ctx context.Context, connection *sql.Conn, kind PublicStateKind, after *PublicStateID) (*sql.Rows, error) {
	switch kind {
	case PublicStateProject:
		if after == nil {
			return connection.QueryContext(ctx, `SELECT id, name, root, verification_policy, revision, created_at_ms, updated_at_ms FROM projects ORDER BY id LIMIT ?`, PublicStatePageSize+1)
		}
		return connection.QueryContext(ctx, `SELECT id, name, root, verification_policy, revision, created_at_ms, updated_at_ms FROM projects WHERE id > ? ORDER BY id LIMIT ?`, after.Bytes(), PublicStatePageSize+1)
	case PublicStateAgent:
		if after == nil {
			return connection.QueryContext(ctx, agentSummarySelect+` ORDER BY a.id LIMIT ?`, PublicStatePageSize+1)
		}
		return connection.QueryContext(ctx, agentSummarySelect+` WHERE a.id > ? ORDER BY a.id LIMIT ?`, after.Bytes(), PublicStatePageSize+1)
	case PublicStateTask:
		if after == nil {
			return connection.QueryContext(ctx, `SELECT id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body, status, priority, blocked_reason, result, completed_at_ms, revision, created_at_ms, updated_at_ms FROM tasks ORDER BY id LIMIT ?`, PublicStatePageSize+1)
		}
		return connection.QueryContext(ctx, `SELECT id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body, status, priority, blocked_reason, result, completed_at_ms, revision, created_at_ms, updated_at_ms FROM tasks WHERE id > ? ORDER BY id LIMIT ?`, after.Bytes(), PublicStatePageSize+1)
	case PublicStateHumanRequest:
		if after == nil {
			return connection.QueryContext(ctx, `SELECT id FROM human_requests WHERE status IN ('open', 'delivering', 'delivery_unknown') ORDER BY id LIMIT ?`, PublicStatePageSize+1)
		}
		return connection.QueryContext(ctx, `SELECT id FROM human_requests WHERE status IN ('open', 'delivering', 'delivery_unknown') AND id > ? ORDER BY id LIMIT ?`, after.Bytes(), PublicStatePageSize+1)
	default:
		return nil, fmt.Errorf("%w: unknown public state kind", ErrInvalidValue)
	}
}

func scanPublicStateItem(scanner rowScanner, kind PublicStateKind) (PublicStateItem, error) {
	switch kind {
	case PublicStateProject:
		project, found, err := scanProject(scanner)
		if err != nil || !found {
			return PublicStateItem{}, publicScanError(err)
		}
		summary := ProjectSummary{ID: project.ID, Name: project.Name, Revision: project.Revision}
		return PublicStateItem{project: &summary}, nil
	case PublicStateAgent:
		summary, err := scanAgentSummary(scanner)
		if err != nil {
			return PublicStateItem{}, err
		}
		return PublicStateItem{agent: &summary}, nil
	case PublicStateTask:
		task, found, err := scanTask(scanner)
		if err != nil || !found {
			return PublicStateItem{}, publicScanError(err)
		}
		summary := TaskSummary{ID: task.ID, ProjectID: task.ProjectID, AssignedAgentID: task.AssignedAgentID, Title: task.Title, Status: task.Status.String(), Priority: task.Priority, Revision: task.Revision}
		return PublicStateItem{task: &summary}, nil
	default:
		return PublicStateItem{}, fmt.Errorf("%w: unknown public state kind", ErrInvalidValue)
	}
}

func publicScanError(err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: public state row disappeared", ErrCorruptState)
}

// agentSummarySelect is the one derivation of the served agent summary.
// Provider is included as a public fact; live activity is deliberately
// absent (see AgentSummary).
const agentSummarySelect = `SELECT a.id, a.project_id, a.name, a.role, a.provider, a.paused, a.revision FROM agents a`

func scanAgentSummary(scanner rowScanner) (AgentSummary, error) {
	var rawID, rawProjectID []byte
	var name, rawRole, rawProvider string
	var paused, rawRevision int64
	if err := scanner.Scan(&rawID, &rawProjectID, &name, &rawRole, &rawProvider, &paused, &rawRevision); err != nil {
		return AgentSummary{}, err
	}
	id, idErr := AgentIDFromBytes(rawID)
	projectID, projectErr := ProjectIDFromBytes(rawProjectID)
	role, roleErr := parseAgentRole(rawRole)
	provider, providerErr := parseProvider(rawProvider)
	revision, revisionErr := NewRevision(rawRevision)
	if idErr != nil || projectErr != nil || roleErr != nil || providerErr != nil || revisionErr != nil ||
		byteLen(name) < 1 || byteLen(name) > 128 || paused != 0 && paused != 1 {
		return AgentSummary{}, fmt.Errorf("%w: invalid agent summary", ErrCorruptState)
	}
	return AgentSummary{ID: id, ProjectID: projectID, Name: name, Role: role.String(), Provider: provider.String(), Paused: paused == 1, Revision: revision}, nil
}

func publicFactorySummary(ctx context.Context, connection *sql.Conn) (FactorySummary, error) {
	state, err := factoryState(ctx, connection)
	if err != nil {
		return FactorySummary{}, err
	}
	var active int64
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE phase <> 'terminal'`).Scan(&active); err != nil {
		return FactorySummary{}, err
	}
	if active < 0 || active > math.MaxUint16 {
		return FactorySummary{}, fmt.Errorf("%w: invalid active run count", ErrCorruptState)
	}
	return FactorySummary{DispatchEnabled: state.DispatchEnabled, Capacity: state.Capacity, ActiveRuns: uint16(active), Revision: state.Revision}, nil
}

func (store *Store) ReadPublicStateEntity(ctx context.Context, kind PublicStateKind, id PublicStateID) (PublicStateEntityResult, error) {
	if kind.String() == "" || kind == PublicStateFactory && !id.validFactory() || kind != PublicStateFactory && !id.validDynamic() {
		return PublicStateEntityResult{}, fmt.Errorf("%w: invalid public state entity locator", ErrInvalidValue)
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return PublicStateEntityResult{}, err
	}
	defer tx.Close()
	if err := validateDurableControls(ctx, tx.connection); err != nil {
		return PublicStateEntityResult{}, err
	}
	state, err := factoryState(ctx, tx.connection)
	if err != nil {
		return PublicStateEntityResult{}, err
	}
	result := PublicStateEntityResult{Head: state.Head, Kind: kind, EntityID: id}
	item, found, err := readPublicStateEntityItem(ctx, tx.connection, kind, id)
	if err != nil {
		return PublicStateEntityResult{}, err
	}
	if !found {
		revision, deleted, exists, err := latestPublicStateInvalidation(ctx, tx.connection, kind, id)
		if err != nil {
			return PublicStateEntityResult{}, err
		}
		if !exists {
			return PublicStateEntityResult{}, ErrNotFound
		}
		if kind != PublicStateHumanRequest || !deleted {
			return PublicStateEntityResult{}, fmt.Errorf("%w: public state row disagrees with its latest invalidation", ErrCorruptState)
		}
		request, requestFound, err := humanRequestByID(ctx, tx.connection, HumanRequestID{id.dynamic})
		if err != nil {
			return PublicStateEntityResult{}, err
		}
		if !requestFound || request.Revision != revision || request.Status != HumanRequestResolved && request.Status != HumanRequestStale {
			return PublicStateEntityResult{}, fmt.Errorf("%w: public state tombstone disagrees with durable request", ErrCorruptState)
		}
		result.Revision = revision
		result.Deleted = true
		return result, nil
	}
	revision, ok := item.revision()
	if !ok || revision.Int64() < 1 {
		return PublicStateEntityResult{}, fmt.Errorf("%w: public state item has no revision", ErrCorruptState)
	}
	result.Revision = revision
	result.Item = &item
	return result, nil
}

func latestPublicStateInvalidation(ctx context.Context, connection *sql.Conn, kind PublicStateKind, id PublicStateID) (Revision, bool, bool, error) {
	var revision, deleted int64
	err := connection.QueryRowContext(ctx, `SELECT revision, deleted FROM invalidations WHERE entity_kind = ? AND entity_id = ? ORDER BY sequence DESC LIMIT 1`, kind.String(), id.Bytes()).Scan(&revision, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, false, false, nil
	}
	if err != nil {
		return Revision{}, false, false, err
	}
	parsed, err := NewRevision(revision)
	if err != nil || deleted != 0 && deleted != 1 {
		return Revision{}, false, false, fmt.Errorf("%w: invalid public state invalidation", ErrCorruptState)
	}
	return parsed, deleted == 1, true, nil
}

func readPublicStateEntityItem(ctx context.Context, connection *sql.Conn, kind PublicStateKind, id PublicStateID) (PublicStateItem, bool, error) {
	switch kind {
	case PublicStateFactory:
		summary, err := publicFactorySummary(ctx, connection)
		return PublicStateItem{factory: &summary}, err == nil, err
	case PublicStateProject:
		project, found, err := projectByID(ctx, connection, ProjectID{id.dynamic})
		if err != nil || !found {
			return PublicStateItem{}, found, err
		}
		summary := ProjectSummary{ID: project.ID, Name: project.Name, Revision: project.Revision}
		return PublicStateItem{project: &summary}, true, nil
	case PublicStateAgent:
		row := connection.QueryRowContext(ctx, agentSummarySelect+` WHERE a.id = ?`, AgentID{id.dynamic}.bytes())
		summary, err := scanAgentSummary(row)
		if errors.Is(err, sql.ErrNoRows) {
			return PublicStateItem{}, false, nil
		}
		if err != nil {
			return PublicStateItem{}, false, err
		}
		return PublicStateItem{agent: &summary}, true, nil
	case PublicStateTask:
		task, found, err := taskByID(ctx, connection, TaskID{id.dynamic})
		if err != nil || !found {
			return PublicStateItem{}, found, err
		}
		summary := TaskSummary{ID: task.ID, ProjectID: task.ProjectID, AssignedAgentID: task.AssignedAgentID, Title: task.Title, Status: task.Status.String(), Priority: task.Priority, Revision: task.Revision}
		return PublicStateItem{task: &summary}, true, nil
	case PublicStateHumanRequest:
		projection, found, err := humanRequestProjectionByID(ctx, connection, HumanRequestID{id.dynamic})
		if err != nil || !found {
			return PublicStateItem{}, found, err
		}
		if projection.Status == HumanRequestResolved || projection.Status == HumanRequestStale {
			return PublicStateItem{}, false, nil
		}
		return PublicStateItem{humanRequest: &projection}, true, nil
	default:
		return PublicStateItem{}, false, fmt.Errorf("%w: unknown public state kind", ErrInvalidValue)
	}
}
