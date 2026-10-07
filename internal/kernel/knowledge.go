package kernel

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	ContentProjectBrief    ContentKind = "project_brief"
	ContentDecision        ContentKind = "decision"
	ContentLesson          ContentKind = "lesson"
	ContentObservation     ContentKind = "observation"
	ContentDiscussion      ContentKind = "discussion"
	ContentDiscussionReply ContentKind = "discussion_reply"
)

type KnowledgeMetadata struct {
	Scope          string   `json:"scope,omitempty"`
	Status         string   `json:"status"`
	Evidence       []string `json:"evidence,omitempty"`
	Entities       []string `json:"entities,omitempty"`
	SourceRevision string   `json:"source_revision,omitempty"`
	Branch         string   `json:"branch,omitempty"`
	Environment    string   `json:"environment,omitempty"`
	ThreadID       string   `json:"thread_id,omitempty"`
	TaskID         string   `json:"task_id,omitempty"`
	ChangeID       string   `json:"change_id,omitempty"`
	RecordType     string   `json:"record_type,omitempty"`
	RecordID       string   `json:"record_id,omitempty"`
	Mentions       []string `json:"mentions,omitempty"`
	Resolved       bool     `json:"resolved,omitempty"`
	Pinned         bool     `json:"pinned,omitempty"`
	Supersedes     string   `json:"supersedes,omitempty"`
}

type KnowledgeQuery struct {
	OpenOnly                                   bool
	Kind                                       ContentKind
	Query, Branch, Environment, Entity, Thread string
	Offset, Limit                              int
}

func IsKnowledgeKind(kind ContentKind) bool {
	switch kind {
	case ContentProjectBrief, ContentDecision, ContentLesson, ContentObservation, ContentDiscussion, ContentDiscussionReply:
		return true
	}
	return false
}

// ParseKnowledgeMetadata deliberately accepts no authority or instruction fields.
func ParseKnowledgeMetadata(raw string) (KnowledgeMetadata, error) {
	var m KnowledgeMetadata
	if len(raw) > 32768 || !utf8.ValidString(raw) || !strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return m, ErrInvalidValue
	}
	keys := json.NewDecoder(strings.NewReader(raw))
	if _, err := keys.Token(); err != nil {
		return m, ErrInvalidValue
	}
	seen := map[string]bool{}
	for keys.More() {
		key, err := keys.Token()
		if err != nil {
			return m, ErrInvalidValue
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return m, ErrInvalidValue
		}
		// SQL applicability filters and browser metadata use exact JSON keys.
		// encoding/json otherwise accepts case-insensitive field aliases.
		switch name {
		case "scope", "status", "evidence", "entities", "source_revision", "branch", "environment", "thread_id", "task_id", "change_id", "record_type", "record_id", "mentions", "resolved", "pinned", "supersedes":
		default:
			return m, ErrInvalidValue
		}
		seen[name] = true
		var value json.RawMessage
		if err := keys.Decode(&value); err != nil {
			return m, ErrInvalidValue
		}
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, fmt.Errorf("%w: knowledge metadata: %v", ErrInvalidValue, err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return m, ErrInvalidValue
	}
	if m.Scope != "" && m.Scope != "repository" && m.Scope != "project" {
		return m, ErrInvalidValue
	}
	switch m.Status {
	case "tentative", "current", "needs_revalidation", "superseded":
	default:
		return m, ErrInvalidValue
	}
	for _, s := range []string{m.SourceRevision, m.Branch, m.Environment, m.ThreadID, m.TaskID, m.ChangeID, m.RecordID, m.Supersedes} {
		if len(s) > 1024 {
			return m, ErrInvalidValue
		}
	}
	for _, list := range [][]string{m.Evidence, m.Entities, m.Mentions} {
		if len(list) > 32 {
			return m, ErrInvalidValue
		}
		for _, s := range list {
			if len(s) == 0 || len(s) > 1024 {
				return m, ErrInvalidValue
			}
		}
	}
	return m, nil
}

func knowledgeRepository(ctx context.Context, c *sql.Conn, a AttemptAuthority) (RepositoryID, error) {
	r, found, err := taskRepository(ctx, c, a.TaskID)
	if err != nil {
		return RepositoryID{}, err
	}
	if !found {
		return RepositoryID{}, ErrUnauthorized
	}
	return r.ID, nil
}

// Only strictly governed kinds authored through operator authority can broaden scope.
func projectKnowledgeScopeSQL(alias string) string {
	return `(` + alias + `.kind IN ('project_brief','decision','lesson','observation','discussion','discussion_reply','procedure') AND (` + alias + `.author GLOB 'operator:*' OR ` + alias + `.author GLOB 'browser:*') AND json_extract(CASE WHEN json_valid(` + alias + `.source_references) THEN ` + alias + `.source_references ELSE '{}' END,'$.scope')='project')`
}

func contentScope(ctx context.Context, c *sql.Conn, a AttemptAuthority, id ContentID, revision int64) error {
	repo, err := knowledgeRepository(ctx, c, a)
	if err != nil {
		return err
	}
	var count int
	err = c.QueryRowContext(ctx, `SELECT count(*) FROM project_content_revisions c JOIN content_repository_bindings b ON b.content_id=c.id AND b.content_revision=c.revision WHERE c.id=? AND c.revision=? AND c.project_id=? AND (b.repository_id=? OR `+projectKnowledgeScopeSQL("c")+`)`, id.Bytes(), revision, a.ProjectID.Bytes(), repo.Bytes()).Scan(&count)
	if err == nil && count != 1 {
		return ErrUnauthorized
	}
	return err
}

func knowledgeID(raw string) ([]byte, error) {
	b, err := hex.DecodeString(raw)
	if err != nil || len(b) != IDBytes {
		return nil, ErrInvalidValue
	}
	return b, nil
}

func knowledgeReference(ctx context.Context, c *sql.Conn, project ProjectID, table, raw string) error {
	b, err := knowledgeID(raw)
	if err != nil {
		return err
	}
	var n int
	if err = c.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE id=? AND project_id=?`, b, project.Bytes()).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: unknown knowledge reference", ErrInvalidValue)
	}
	return nil
}

func validateKnowledge(ctx context.Context, c *sql.Conn, spec NewContent, expected Revision, a *AttemptAuthority) error {
	if a != nil {
		if spec.ProjectID != a.ProjectID {
			return ErrUnauthorized
		}
		repo, err := knowledgeRepository(ctx, c, *a)
		if err != nil {
			return err
		}
		if !spec.RepositoryID.zero() && spec.RepositoryID != repo {
			return ErrUnauthorized
		}
		if expected.Int64() > 0 {
			if err := contentScope(ctx, c, *a, spec.ID, expected.Int64()); err != nil {
				return err
			}
		}
	}
	var old ContentRevision
	if expected.Int64() > 0 {
		var err error
		old, err = contentByRevision(ctx, c, spec.ID, expected.Int64())
		if err != nil {
			return err
		}
		if old.ProjectID != spec.ProjectID || old.Kind != spec.Kind {
			return ErrConflict
		}
		if (IsKnowledgeKind(old.Kind) || old.Kind == ContentProcedure && strings.HasPrefix(strings.TrimSpace(old.SourceReferences), "{")) && old.LatestRevision != expected {
			return ErrConflict
		}
		if old.Kind == ContentDiscussionReply {
			return ErrUnauthorized
		}
		if a != nil && (IsKnowledgeKind(old.Kind) || old.Kind == ContentProcedure && strings.HasPrefix(strings.TrimSpace(old.SourceReferences), "{")) {
			meta, err := ParseKnowledgeMetadata(old.SourceReferences)
			if err != nil {
				return err
			}
			if !strings.Contains(old.Author, " agent:"+a.AgentID.String()+" role:") || meta.Status == "current" || old.Kind == ContentProjectBrief {
				return ErrUnauthorized
			}
		}
	}
	if !IsKnowledgeKind(spec.Kind) && !(spec.Kind == ContentProcedure && strings.HasPrefix(strings.TrimSpace(spec.SourceReferences), "{")) {
		return nil
	}
	m, err := ParseKnowledgeMetadata(spec.SourceReferences)
	if err != nil {
		return err
	}
	if a == nil && m.Scope == "project" && !strings.HasPrefix(spec.Author, "operator:") && !strings.HasPrefix(spec.Author, "browser:") {
		return ErrInvalidValue
	}
	if a != nil && (spec.Kind == ContentProjectBrief || m.Status == "current" || m.Pinned || m.Scope == "project") {
		return ErrUnauthorized
	}
	if (spec.Kind == ContentDecision || spec.Kind == ContentLesson || (spec.Kind == ContentProjectBrief && m.Status == "current")) && len(m.Evidence) == 0 {
		return ErrInvalidValue
	}
	if spec.Kind != ContentDiscussion && (m.Resolved || m.Pinned) {
		return ErrInvalidValue
	}
	if spec.Kind == ContentDiscussionReply && m.ThreadID == "" {
		return ErrInvalidValue
	}
	repo := spec.RepositoryID
	if a != nil {
		repo, err = knowledgeRepository(ctx, c, *a)
		if err != nil {
			return err
		}
	}
	if expected.Int64() > 0 {
		var b []byte
		if err := c.QueryRowContext(ctx, `SELECT repository_id FROM content_repository_bindings WHERE content_id=? AND content_revision=?`, spec.ID.Bytes(), expected.Int64()).Scan(&b); err != nil {
			return err
		}
		bound, err := RepositoryIDFromBytes(b)
		if err != nil {
			return err
		}
		if !repo.zero() && repo != bound {
			return ErrInvalidValue
		}
		repo = bound
	}

	if repo.zero() {
		r, err := resolveTaskRepository(ctx, c, spec.ProjectID, repo)
		if err != nil {
			return err
		}
		repo = r.ID
	}
	for _, ref := range []struct{ raw, table, taskColumn string }{{m.TaskID, "tasks", "id"}, {m.ChangeID, "changes", "task_id"}} {
		if ref.raw == "" {
			continue
		}
		id, err := knowledgeID(ref.raw)
		if err != nil {
			return err
		}
		var count int
		if err := c.QueryRowContext(ctx, `SELECT count(*) FROM `+ref.table+` x JOIN task_repository_bindings b ON b.task_id=x.`+ref.taskColumn+` WHERE x.id=? AND x.project_id=? AND (b.repository_id=? OR ?='project')`, id, spec.ProjectID.Bytes(), repo.Bytes(), m.Scope).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("%w: knowledge reference outside repository", ErrInvalidValue)
		}
	}

	for _, ref := range []string{m.ThreadID, m.Supersedes} {
		if ref == "" {
			continue
		}
		b, err := knowledgeID(ref)
		if err != nil {
			return err
		}
		if string(b) == string(spec.ID.Bytes()) {
			return ErrInvalidValue
		}
		var kind string
		err = c.QueryRowContext(ctx, `SELECT c.kind FROM project_content_revisions c JOIN content_repository_bindings b ON b.content_id=c.id AND b.content_revision=c.revision WHERE c.id=? AND c.project_id=? AND (b.repository_id=? OR `+projectKnowledgeScopeSQL("c")+`) ORDER BY c.revision DESC LIMIT 1`, b, spec.ProjectID.Bytes(), repo.Bytes()).Scan(&kind)
		if err != nil {
			return ErrInvalidValue
		}
		if ref == m.ThreadID && kind != string(ContentDiscussion) {
			return ErrInvalidValue
		}
	}
	if (m.RecordType == "") != (m.RecordID == "") {
		return ErrInvalidValue
	}
	switch m.RecordType {
	case "":
	case "human_request", "peer_question":
		if err := knowledgeReference(ctx, c, spec.ProjectID, m.RecordType+"s", m.RecordID); err != nil {
			return err
		}
	case "review":
		var n int
		if err := c.QueryRowContext(ctx, `SELECT count(*) FROM production_records WHERE project_id=? AND kind='reviewer' AND identity=?`, spec.ProjectID.Bytes(), m.RecordID).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return ErrInvalidValue
		}
	default:
		return ErrInvalidValue
	}
	for _, entity := range m.Entities {
		if err := validateKnowledgeEntity(ctx, c, spec.ProjectID, entity); err != nil {
			return err
		}
	}
	for _, mention := range m.Mentions {
		if err := knowledgeReference(ctx, c, spec.ProjectID, "agents", mention); err != nil {
			return err
		}
	}
	return nil
}

func validateKnowledgeEntity(ctx context.Context, c *sql.Conn, project ProjectID, entity string) error {
	raw, ok := strings.CutPrefix(entity, project.String()+":")
	if !ok || len(raw) != 32 {
		return ErrInvalidValue
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return ErrInvalidValue
	}
	return nil
}

func (store *Store) ValidateKnowledgeWrite(ctx context.Context, spec NewContent, expected Revision) error {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	return validateKnowledge(ctx, tx.connection, spec, expected, nil)
}
func (store *Store) ValidateKnowledgeWriteForAttempt(ctx context.Context, digest AttemptDigest, spec NewContent, expected Revision) error {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return err
	}
	defer tx.Close()
	a, err := authenticateAttempt(ctx, tx.connection, digest)
	if err != nil {
		return err
	}
	if a.Role != RoleWorker && a.Role != RoleOrchestrator {
		return ErrUnauthorized
	}
	return validateKnowledge(ctx, tx.connection, spec, expected, &a)
}

func (store *Store) SearchKnowledge(ctx context.Context, project ProjectID, repository RepositoryID, q KnowledgeQuery) (ContentPage, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ContentPage{}, err
	}
	defer tx.Close()
	return searchKnowledge(ctx, tx.connection, project, repository, q)
}
func (store *Store) SearchKnowledgeForAttempt(ctx context.Context, digest AttemptDigest, q KnowledgeQuery) (ContentPage, error) {
	tx, err := store.beginRead(ctx)
	if err != nil {
		return ContentPage{}, err
	}
	defer tx.Close()
	a, err := authenticateAttempt(ctx, tx.connection, digest)
	if err != nil {
		return ContentPage{}, err
	}
	repo, err := knowledgeRepository(ctx, tx.connection, a)
	if err != nil {
		return ContentPage{}, err
	}
	return searchKnowledge(ctx, tx.connection, a.ProjectID, repo, q)
}
func searchKnowledge(ctx context.Context, c *sql.Conn, project ProjectID, repository RepositoryID, q KnowledgeQuery) (ContentPage, error) {
	if project.zero() || repository.zero() || q.Offset < 0 || q.Limit < 0 || q.Limit > contentPageSize || len(q.Query) > 1024 {
		return ContentPage{}, ErrInvalidValue
	}
	if q.Limit == 0 {
		q.Limit = contentPageSize
	}
	query := `SELECT c.id,c.project_id,c.kind,c.revision,c.title,c.description,'',c.author,c.source_references,c.object_format,c.commit_oid,c.path,c.repository_dev,c.repository_inode,c.deprecated,c.created_at_ms,c.revision FROM project_content_revisions c JOIN content_repository_bindings b ON b.content_id=c.id AND b.content_revision=c.revision WHERE c.project_id=? AND (b.repository_id=? OR ` + projectKnowledgeScopeSQL("c") + `) AND c.revision=(SELECT max(revision) FROM project_content_revisions WHERE id=c.id) AND c.deprecated=0`
	args := []any{project.Bytes(), repository.Bytes()}
	if q.Kind != "" {
		query += ` AND c.kind=?`
		args = append(args, string(q.Kind))
	}
	if q.Query != "" {
		query += ` AND instr(lower(c.title||' '||c.description||' '||c.source_references),lower(?))>0`
		args = append(args, q.Query)
	}
	for _, filter := range []struct{ key, value string }{{"branch", q.Branch}, {"environment", q.Environment}} {
		query += ` AND COALESCE(json_extract(CASE WHEN json_valid(c.source_references) THEN c.source_references ELSE '{}' END, '$.` + filter.key + `'),'') IN ('',?)`
		args = append(args, filter.value)
	}
	if q.OpenOnly {
		query += ` AND COALESCE(json_extract(CASE WHEN json_valid(c.source_references) THEN c.source_references ELSE '{}' END,'$.resolved'),0)=0`
	}
	if q.Thread != "" {
		query += ` AND json_extract(CASE WHEN json_valid(c.source_references) THEN c.source_references ELSE '{}' END,'$.thread_id')=?`
		args = append(args, q.Thread)
	}

	if q.Entity != "" {
		query += ` AND EXISTS(SELECT 1 FROM json_each(CASE WHEN json_valid(c.source_references) THEN c.source_references ELSE '{}' END,'$.entities') WHERE value=?)`
		args = append(args, q.Entity)
	}
	query += ` ORDER BY c.created_at_ms DESC,c.id LIMIT ? OFFSET ?`
	args = append(args, q.Limit+1, q.Offset)
	rows, err := c.QueryContext(ctx, query, args...)
	if err != nil {
		return ContentPage{}, err
	}
	defer rows.Close()
	result := ContentPage{}
	for rows.Next() {
		item, err := scanContent(rows)
		if err != nil {
			return ContentPage{}, err
		}
		if len(result.Items) == q.Limit {
			result.NextOffset = q.Offset + q.Limit
		} else {
			result.Items = append(result.Items, item)
		}
	}
	return result, rows.Err()
}
