package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Schema history. A home is recognised by comparing its schema text literally
// against the set for its user_version, so every replaced definition is frozen
// here at the text it had, and each set derives from the next: v1 from v2, v2
// from the current statements.
//
// v1 is every home created before the accounts slice. That slice added the
// accounts table, agents.account_id, and the wider invalidations entity_kind
// check as schemaStatements edits with no migration, so every existing home
// became foreign to the new build. legacyAgents and legacyInvalidations are
// frozen at their pre-accounts text.
//
// v2 bounded the browser capability mask at four bits. v3 adds administration
// (bit 4) and widens the two CHECKs; nothing else about those tables moved.
//
// v3 had no idle rule on agents. v4 adds the five idle_* columns, every
// existing agent keeping the rule that was always true: wait.
const (
	legacyUserVersion   = 1
	previousUserVersion = 2
	priorUserVersion    = 3
	v4UserVersion       = 4
	v5UserVersion       = 5
	v6UserVersion       = 6
	v7UserVersion       = 7
	v8UserVersion       = 8
	v9UserVersion       = 9
	v10UserVersion      = 10
	v11UserVersion      = 11
	v12UserVersion      = 12
	v13UserVersion      = 13
	v14UserVersion      = 14
	v15UserVersion      = 15
	v16UserVersion      = 16
	v17UserVersion      = 17
	v18UserVersion      = 18
	v19UserVersion      = 19
	v20UserVersion      = 20
	v21UserVersion      = 21
	v22UserVersion      = 22
	v23UserVersion      = 23
	v24UserVersion      = 24
	v25UserVersion      = 25
	v26UserVersion      = 26
	v27UserVersion      = 27
	v28UserVersion      = 28
	v29UserVersion      = 29
	v30UserVersion      = 30
	v31UserVersion      = 31
	// v13Changes is the changes table before managed Git worktrees. It bound a
	// Git-free published tree by a manifest digest, its entry and byte counts
	// and its root inode. v14 names the tree's own branch head instead and
	// drops those columns; a v13 row keeps its base, repository and settled
	// run and gets no head until its tree is adopted into a worktree.
	v13Changes = `CREATE TABLE changes (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    project_id BLOB NOT NULL CHECK (length(project_id) = 16),
    task_id BLOB NOT NULL CHECK (length(task_id) = 16),
    task_incarnation_id BLOB NOT NULL CHECK (length(task_incarnation_id) = 16),
	phase TEXT NOT NULL CHECK (phase IN ('reserved', 'prepared', 'available', 'retained', 'abandoned')),
    object_format TEXT CHECK (object_format IS NULL OR object_format IN ('sha1', 'sha256')),
	base_commit BLOB,
	repository_dev INTEGER CHECK (repository_dev IS NULL OR repository_dev >= 0),
	repository_inode INTEGER CHECK (repository_inode IS NULL OR repository_inode > 0),
    prepared_at_ms INTEGER CHECK (prepared_at_ms IS NULL OR prepared_at_ms >= 0),
    tree_digest BLOB CHECK (tree_digest IS NULL OR length(tree_digest) = 32),
    entry_count INTEGER CHECK (entry_count IS NULL OR entry_count BETWEEN 0 AND 10000),
    total_bytes INTEGER CHECK (total_bytes IS NULL OR total_bytes BETWEEN 0 AND 1073741824),
	tree_dev INTEGER CHECK (tree_dev IS NULL OR tree_dev >= 0),
	tree_inode INTEGER CHECK (tree_inode IS NULL OR tree_inode > 0),
    available_at_ms INTEGER CHECK (available_at_ms IS NULL OR available_at_ms >= 0),
	settled_run_id BLOB CHECK (settled_run_id IS NULL OR length(settled_run_id) = 16),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    FOREIGN KEY (task_id, project_id, task_incarnation_id) REFERENCES tasks(id, project_id, incarnation_id),
	FOREIGN KEY (settled_run_id, id, project_id, task_id, task_incarnation_id) REFERENCES runs(id, change_id, project_id, task_id, task_incarnation_id),
	CHECK ((object_format IS NULL AND base_commit IS NULL AND repository_dev IS NULL AND repository_inode IS NULL) OR (object_format = 'sha1' AND length(base_commit) = 20 AND repository_dev IS NOT NULL AND repository_inode IS NOT NULL) OR (object_format = 'sha256' AND length(base_commit) = 32 AND repository_dev IS NOT NULL AND repository_inode IS NOT NULL)),
	CHECK (
		(phase = 'reserved' AND object_format IS NULL AND base_commit IS NULL AND repository_dev IS NULL AND repository_inode IS NULL AND prepared_at_ms IS NULL AND tree_digest IS NULL AND entry_count IS NULL AND total_bytes IS NULL AND tree_dev IS NULL AND tree_inode IS NULL AND available_at_ms IS NULL AND settled_run_id IS NULL) OR
		(phase = 'prepared' AND object_format IS NOT NULL AND base_commit IS NOT NULL AND repository_dev IS NOT NULL AND repository_inode IS NOT NULL AND prepared_at_ms IS NOT NULL AND tree_digest IS NOT NULL AND entry_count IS NOT NULL AND total_bytes IS NOT NULL AND tree_dev IS NOT NULL AND tree_inode IS NOT NULL AND available_at_ms IS NULL AND settled_run_id IS NULL) OR
		(phase = 'available' AND object_format IS NOT NULL AND base_commit IS NOT NULL AND repository_dev IS NOT NULL AND repository_inode IS NOT NULL AND prepared_at_ms IS NOT NULL AND tree_digest IS NOT NULL AND entry_count IS NOT NULL AND total_bytes IS NOT NULL AND tree_dev IS NOT NULL AND tree_inode IS NOT NULL AND available_at_ms IS NOT NULL AND settled_run_id IS NULL) OR
		(phase = 'retained' AND object_format IS NOT NULL AND base_commit IS NOT NULL AND repository_dev IS NOT NULL AND repository_inode IS NOT NULL AND prepared_at_ms IS NOT NULL AND tree_digest IS NOT NULL AND entry_count IS NOT NULL AND total_bytes IS NOT NULL AND tree_dev IS NOT NULL AND tree_inode IS NOT NULL AND available_at_ms IS NOT NULL AND settled_run_id IS NOT NULL) OR
		(phase = 'abandoned' AND object_format IS NULL AND base_commit IS NULL AND repository_dev IS NULL AND repository_inode IS NULL AND prepared_at_ms IS NULL AND tree_digest IS NULL AND entry_count IS NULL AND total_bytes IS NULL AND tree_dev IS NULL AND tree_inode IS NULL AND available_at_ms IS NULL AND settled_run_id IS NOT NULL)
	)
) STRICT, WITHOUT ROWID`
	v8HumanRequests = `CREATE TABLE human_requests (
    id BLOB PRIMARY KEY CHECK (length(id) = 16 AND id <> zeroblob(16)),
    run_id BLOB NOT NULL CHECK (length(run_id) = 16) REFERENCES runs(id),
    idempotency_key BLOB NOT NULL CHECK (length(idempotency_key) = 16 AND idempotency_key <> zeroblob(16)),
    kind TEXT NOT NULL CHECK (kind = 'question'),
    reason_code TEXT NOT NULL CHECK (reason_code = 'provider_question'),
    question_text TEXT NOT NULL CHECK (length(CAST(question_text AS BLOB)) BETWEEN 1 AND 8192),
    status TEXT NOT NULL CHECK (status IN ('open', 'delivering', 'delivery_unknown', 'resolved', 'stale')),
    delivery_id BLOB CHECK (delivery_id IS NULL OR (length(delivery_id) = 16 AND delivery_id <> zeroblob(16))),
    delivery_started_at_ms INTEGER CHECK (delivery_started_at_ms IS NULL OR delivery_started_at_ms >= 0),
    resolution_kind TEXT CHECK (resolution_kind IS NULL OR resolution_kind IN ('reply', 'stale', 'cancel_run')),
    closed_at_ms INTEGER CHECK (closed_at_ms IS NULL OR closed_at_ms >= 0),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    UNIQUE(run_id, idempotency_key),
    UNIQUE(delivery_id),
    CHECK ((delivery_id IS NULL) = (delivery_started_at_ms IS NULL)),
    CHECK (delivery_started_at_ms IS NULL OR (delivery_started_at_ms >= created_at_ms AND delivery_started_at_ms <= updated_at_ms)),
    CHECK (closed_at_ms IS NULL OR (closed_at_ms >= created_at_ms AND closed_at_ms <= updated_at_ms)),
    CHECK (status = 'open' AND delivery_id IS NULL OR status IN ('delivering', 'delivery_unknown') AND delivery_id IS NOT NULL OR status = 'resolved' AND (resolution_kind = 'reply' AND delivery_id IS NOT NULL OR resolution_kind = 'cancel_run' AND delivery_id IS NULL) OR status = 'stale'),
    CHECK ((status IN ('resolved', 'stale')) = (closed_at_ms IS NOT NULL)),
    CHECK (status = 'resolved' AND resolution_kind IN ('reply', 'cancel_run') OR status = 'stale' AND resolution_kind = 'stale' OR status IN ('open', 'delivering', 'delivery_unknown') AND resolution_kind IS NULL),
    CHECK (status IN ('open', 'delivering', 'delivery_unknown') AND closed_at_ms IS NULL OR status IN ('resolved', 'stale')),
    CHECK (status NOT IN ('resolved', 'stale') OR closed_at_ms = updated_at_ms)
) STRICT, WITHOUT ROWID`

	v7Agents = `CREATE TABLE agents (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    project_id BLOB NOT NULL CHECK (length(project_id) = 16) REFERENCES projects(id),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 128),
    role TEXT NOT NULL CHECK (role IN ('orchestrator', 'worker')),
    provider TEXT NOT NULL CHECK (provider IN ('claude_code', 'codex', 'shell')),
    model TEXT CHECK (model IS NULL OR length(CAST(model AS BLOB)) BETWEEN 1 AND 128),
    reasoning_effort TEXT CHECK (reasoning_effort IS NULL OR reasoning_effort IN ('low', 'medium', 'high', 'xhigh', 'max', 'ultra')),
    account_id BLOB CHECK (account_id IS NULL OR length(account_id) = 16) REFERENCES accounts(id),
    paused INTEGER NOT NULL CHECK (paused IN (0, 1)),
    idle_policy TEXT NOT NULL CHECK (idle_policy IN ('wait', 'standing_instruction')),
    idle_after_seconds INTEGER NOT NULL CHECK (idle_after_seconds BETWEEN 0 AND 604800),
    idle_instruction TEXT NOT NULL CHECK (length(CAST(idle_instruction AS BLOB)) <= 32768),
    idle_run_budget INTEGER NOT NULL CHECK (idle_run_budget BETWEEN 0 AND 1000000),
    idle_runs_used INTEGER NOT NULL CHECK (idle_runs_used >= 0 AND idle_runs_used <= idle_run_budget),
    tool_budget_limit INTEGER NOT NULL CHECK (tool_budget_limit BETWEEN 1 AND 1000000000),
    tool_calls_used INTEGER NOT NULL CHECK (tool_calls_used >= 0 AND tool_calls_used <= tool_budget_limit),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    CHECK (provider <> 'shell' OR (model IS NULL AND reasoning_effort IS NULL AND account_id IS NULL)),
    CHECK (idle_policy <> 'standing_instruction' OR (idle_after_seconds >= 1 AND idle_instruction <> '' AND idle_run_budget >= 1))
) STRICT, WITHOUT ROWID`

	v7Projects = `CREATE TABLE projects (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
	    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 128),
	    root TEXT NOT NULL CHECK (length(CAST(root AS BLOB)) BETWEEN 1 AND 4096 AND substr(root, 1, 1) = '/'),
	    verification_policy TEXT NOT NULL CHECK (verification_policy IN ('none', 'rust_workspace_test', 'go_workspace_test')),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms)
) STRICT, WITHOUT ROWID`

	legacyAgents = `CREATE TABLE agents (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    project_id BLOB NOT NULL CHECK (length(project_id) = 16) REFERENCES projects(id),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 128),
    role TEXT NOT NULL CHECK (role IN ('orchestrator', 'worker')),
    provider TEXT NOT NULL CHECK (provider IN ('claude_code', 'codex', 'shell')),
    model TEXT CHECK (model IS NULL OR length(CAST(model AS BLOB)) BETWEEN 1 AND 128),
    reasoning_effort TEXT CHECK (reasoning_effort IS NULL OR reasoning_effort IN ('low', 'medium', 'high', 'xhigh', 'max', 'ultra')),
    paused INTEGER NOT NULL CHECK (paused IN (0, 1)),
    tool_budget_limit INTEGER NOT NULL CHECK (tool_budget_limit BETWEEN 1 AND 1000000000),
    tool_calls_used INTEGER NOT NULL CHECK (tool_calls_used >= 0 AND tool_calls_used <= tool_budget_limit),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    CHECK (provider <> 'shell' OR (model IS NULL AND reasoning_effort IS NULL))
) STRICT, WITHOUT ROWID`

	legacyInvalidations = `CREATE TABLE invalidations (
    sequence INTEGER PRIMARY KEY CHECK (sequence >= 1),
    occurred_at_ms INTEGER NOT NULL CHECK (occurred_at_ms >= 0),
    entity_kind TEXT NOT NULL CHECK (entity_kind IN ('factory', 'project', 'agent', 'task', 'change', 'run', 'human_request')),
    entity_id BLOB NOT NULL CHECK (length(entity_id) = 16),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    deleted INTEGER NOT NULL CHECK (deleted IN (0, 1))
) STRICT`

	v6Invalidations = `CREATE TABLE invalidations (
    sequence INTEGER PRIMARY KEY CHECK (sequence >= 1),
    occurred_at_ms INTEGER NOT NULL CHECK (occurred_at_ms >= 0),
    entity_kind TEXT NOT NULL CHECK (entity_kind IN ('factory', 'project', 'agent', 'task', 'change', 'run', 'human_request', 'account')),
    entity_id BLOB NOT NULL CHECK (length(entity_id) = 16),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    deleted INTEGER NOT NULL CHECK (deleted IN (0, 1))
) STRICT`

	v7AgentColumns = `id, project_id, name, role, provider, model, reasoning_effort, account_id, paused, idle_policy, idle_after_seconds, idle_instruction, idle_run_budget, idle_runs_used, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms`

	legacyAgentColumns        = `id, project_id, name, role, provider, model, reasoning_effort, paused, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms`
	legacyInvalidationColumns = `sequence, occurred_at_ms, entity_kind, entity_id, revision, deleted`

	previousPairingChallenges = `CREATE TABLE browser_pairing_challenges (
    secret_digest BLOB PRIMARY KEY CHECK (length(secret_digest) = 32),
    boot_id BLOB NOT NULL CHECK (length(boot_id) = 16 AND boot_id <> zeroblob(16)),
    intended_origin TEXT NOT NULL CHECK (length(CAST(intended_origin AS BLOB)) BETWEEN 1 AND 4096),
    capability_mask INTEGER NOT NULL CHECK (capability_mask BETWEEN 1 AND 15 AND (capability_mask & 1) = 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    expires_at_ms INTEGER NOT NULL CHECK (expires_at_ms > created_at_ms AND expires_at_ms <= created_at_ms + 300000),
    redeemed_at_ms INTEGER CHECK (redeemed_at_ms IS NULL OR (redeemed_at_ms >= created_at_ms AND redeemed_at_ms < expires_at_ms AND redeemed_at_ms >= 0))
) STRICT, WITHOUT ROWID`

	previousBrowserClients = `CREATE TABLE browser_clients (
    id BLOB PRIMARY KEY CHECK (length(id) = 16 AND id <> zeroblob(16)),
    public_key BLOB NOT NULL CHECK (length(public_key) = 65 AND substr(public_key, 1, 1) = X'04'),
    fingerprint BLOB NOT NULL UNIQUE CHECK (length(fingerprint) = 32),
    capability_mask INTEGER NOT NULL CHECK (capability_mask BETWEEN 1 AND 15 AND (capability_mask & 1) = 1),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    revoked_at_ms INTEGER CHECK (revoked_at_ms IS NULL OR (revoked_at_ms >= created_at_ms AND revoked_at_ms <= updated_at_ms AND revoked_at_ms >= 0))
) STRICT, WITHOUT ROWID`

	previousPairingChallengeColumns = `secret_digest, boot_id, intended_origin, capability_mask, created_at_ms, expires_at_ms, redeemed_at_ms`
	previousBrowserClientColumns    = `id, public_key, fingerprint, capability_mask, revision, created_at_ms, updated_at_ms, revoked_at_ms`

	priorAgents = `CREATE TABLE agents (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    project_id BLOB NOT NULL CHECK (length(project_id) = 16) REFERENCES projects(id),
    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 128),
    role TEXT NOT NULL CHECK (role IN ('orchestrator', 'worker')),
    provider TEXT NOT NULL CHECK (provider IN ('claude_code', 'codex', 'shell')),
    model TEXT CHECK (model IS NULL OR length(CAST(model AS BLOB)) BETWEEN 1 AND 128),
    reasoning_effort TEXT CHECK (reasoning_effort IS NULL OR reasoning_effort IN ('low', 'medium', 'high', 'xhigh', 'max', 'ultra')),
    account_id BLOB CHECK (account_id IS NULL OR length(account_id) = 16) REFERENCES accounts(id),
    paused INTEGER NOT NULL CHECK (paused IN (0, 1)),
    tool_budget_limit INTEGER NOT NULL CHECK (tool_budget_limit BETWEEN 1 AND 1000000000),
    tool_calls_used INTEGER NOT NULL CHECK (tool_calls_used >= 0 AND tool_calls_used <= tool_budget_limit),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    CHECK (provider <> 'shell' OR (model IS NULL AND reasoning_effort IS NULL AND account_id IS NULL))
) STRICT, WITHOUT ROWID`

	v12Tasks = `CREATE TABLE tasks (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    project_id BLOB NOT NULL CHECK (length(project_id) = 16),
    assigned_agent_id BLOB NOT NULL CHECK (length(assigned_agent_id) = 16),
    incarnation_id BLOB NOT NULL CHECK (length(incarnation_id) = 16),
    work_revision INTEGER NOT NULL CHECK (work_revision >= 1),
    title TEXT NOT NULL CHECK (length(CAST(title AS BLOB)) BETWEEN 1 AND 1024),
    body TEXT NOT NULL CHECK (length(CAST(body AS BLOB)) <= 131072),
    sent_back_instruction_bytes INTEGER CHECK (sent_back_instruction_bytes IS NULL OR sent_back_instruction_bytes BETWEEN 0 AND length(CAST(body AS BLOB))),
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'blocked', 'succeeded', 'failed', 'cancelled')),
    priority INTEGER NOT NULL CHECK (priority BETWEEN -1000000 AND 1000000),
    blocked_reason TEXT CHECK (blocked_reason IS NULL OR length(CAST(blocked_reason AS BLOB)) BETWEEN 1 AND 4096),
    result TEXT CHECK (result IS NULL OR length(CAST(result AS BLOB)) <= 131072),
    completed_at_ms INTEGER CHECK (completed_at_ms IS NULL OR completed_at_ms >= 0),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    FOREIGN KEY (assigned_agent_id, project_id) REFERENCES agents(id, project_id),
    CHECK (
        (status IN ('queued', 'running') AND blocked_reason IS NULL AND result IS NULL AND completed_at_ms IS NULL) OR
        (status = 'blocked' AND blocked_reason IS NOT NULL AND result IS NULL AND completed_at_ms IS NULL) OR
        (status = 'succeeded' AND blocked_reason IS NULL AND completed_at_ms IS NOT NULL) OR
        (status IN ('failed', 'cancelled') AND blocked_reason IS NULL AND result IS NULL AND completed_at_ms IS NOT NULL)
    ),
    CHECK (completed_at_ms IS NULL OR completed_at_ms = updated_at_ms)
) STRICT, WITHOUT ROWID`

	v4Tasks = `CREATE TABLE tasks (
    id BLOB PRIMARY KEY CHECK (length(id) = 16),
    project_id BLOB NOT NULL CHECK (length(project_id) = 16),
    assigned_agent_id BLOB NOT NULL CHECK (length(assigned_agent_id) = 16),
    incarnation_id BLOB NOT NULL CHECK (length(incarnation_id) = 16),
    work_revision INTEGER NOT NULL CHECK (work_revision >= 1),
    title TEXT NOT NULL CHECK (length(CAST(title AS BLOB)) BETWEEN 1 AND 1024),
    body TEXT NOT NULL CHECK (length(CAST(body AS BLOB)) <= 131072),
    status TEXT NOT NULL CHECK (status IN ('queued', 'running', 'blocked', 'succeeded', 'failed', 'cancelled')),
    priority INTEGER NOT NULL CHECK (priority BETWEEN -1000000 AND 1000000),
    blocked_reason TEXT CHECK (blocked_reason IS NULL OR length(CAST(blocked_reason AS BLOB)) BETWEEN 1 AND 4096),
    result TEXT CHECK (result IS NULL OR length(CAST(result AS BLOB)) <= 131072),
    completed_at_ms INTEGER CHECK (completed_at_ms IS NULL OR completed_at_ms >= 0),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms),
    FOREIGN KEY (assigned_agent_id, project_id) REFERENCES agents(id, project_id),
    CHECK (
        (status IN ('queued', 'running') AND blocked_reason IS NULL AND result IS NULL AND completed_at_ms IS NULL) OR
        (status = 'blocked' AND blocked_reason IS NOT NULL AND result IS NULL AND completed_at_ms IS NULL) OR
        (status = 'succeeded' AND blocked_reason IS NULL AND completed_at_ms IS NOT NULL) OR
        (status IN ('failed', 'cancelled') AND blocked_reason IS NULL AND result IS NULL AND completed_at_ms IS NOT NULL)
    ),
    CHECK (completed_at_ms IS NULL OR completed_at_ms = updated_at_ms)
) STRICT, WITHOUT ROWID`

	priorAgentColumns = `id, project_id, name, role, provider, model, reasoning_effort, account_id, paused, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms`
	v4TaskColumns     = `id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body, status, priority, blocked_reason, result, completed_at_ms, revision, created_at_ms, updated_at_ms`
	idleColumns       = `idle_policy, idle_after_seconds, idle_instruction, idle_run_budget, idle_runs_used`
	idleDefaults      = `'wait', 0, '', 0, 0`
)

// v4SchemaStatements is the exact v4 schema: the current one with the frozen
// tasks definition substituted.
func v4SchemaStatements() []string {
	v5 := v5SchemaStatements()
	statements := make([]string, 0, len(v5))
	for _, statement := range v5 {
		_, name := schemaObjectIdentity(statement)
		if name == "tasks" {
			statement = v4Tasks
		}
		statements = append(statements, statement)
	}
	return statements
}

// v5SchemaStatements is the exact schema before durable operator
// interventions and overseer wake cursors were added.
func v5SchemaStatements() []string {
	v6 := v6SchemaStatements()
	statements := make([]string, 0, len(v6))
	for _, statement := range v6 {
		_, name := schemaObjectIdentity(statement)
		if name == "task_interventions" || name == "task_interventions_project_task" || name == "overseer_wake_cursors" {
			continue
		}
		statements = append(statements, statement)
	}
	return statements
}

// v6SchemaStatements is the exact schema before task-linked peer questions.
func v6SchemaStatements() []string {
	statements := make([]string, 0, len(schemaStatements))
	for _, statement := range v7SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		switch name {
		case "peer_questions", "peer_questions_source_key_unique", "peer_questions_recipient_delivery_unique", "peer_questions_answer_delivery_unique", "peer_questions_task_history":
			continue
		case "invalidations":
			statement = v6Invalidations
		}
		statements = append(statements, statement)
	}
	return statements
}

// v7SchemaStatements is the exact schema before project run limits.
func v7SchemaStatements() []string {
	statements := make([]string, 0, len(schemaStatements))
	for _, statement := range v8SchemaStatements() {
		if _, name := schemaObjectIdentity(statement); name == "projects" {
			statement = v7Projects
		}
		statements = append(statements, statement)
	}
	return statements
}

// v8SchemaStatements predates private suggested answers.
func v8SchemaStatements() []string {
	statements := v9SchemaStatements()
	for i, statement := range statements {
		if _, name := schemaObjectIdentity(statement); name == "human_requests" {
			statements[i] = v8HumanRequests
		}
	}
	return statements
}

// v9SchemaStatements predates durable sprite appearance.
func v9SchemaStatements() []string {
	statements := v12SchemaStatements()
	for i, statement := range statements {
		if _, name := schemaObjectIdentity(statement); name == "agents" {
			statements[i] = v7Agents
		}
	}
	return statements
}

// v10 predates worker archiving.
func v10SchemaStatements() []string {
	statements := v12SchemaStatements()
	for i, statement := range statements {
		if _, name := schemaObjectIdentity(statement); name == "agents" {
			statement = strings.Replace(statement, "\tarchived INTEGER NOT NULL CHECK (archived IN (0, 1)),\n", "", 1)
			statement = strings.Replace(statement, "idle_runs_used INTEGER NOT NULL CHECK (idle_runs_used >= 0),", "idle_runs_used INTEGER NOT NULL CHECK (idle_runs_used >= 0 AND idle_runs_used <= idle_run_budget),", 1)
			statement = strings.Replace(statement, "CHECK (idle_policy <> 'standing_instruction' OR (idle_after_seconds >= 1 AND idle_instruction <> ''))", "CHECK (idle_policy <> 'standing_instruction' OR (idle_after_seconds >= 1 AND idle_instruction <> '' AND idle_run_budget >= 1))", 1)
			statements[i] = statement
		}
	}
	return statements
}

// v11 predates unlimited standing instructions. Keep its exact schema here so
// an existing home is rebuilt rather than served with weaker constraints than
// its recorded user_version promises.
func v11SchemaStatements() []string {
	statements := v12SchemaStatements()
	for i, statement := range statements {
		if _, name := schemaObjectIdentity(statement); name == "agents" {
			statement = strings.Replace(statement, "idle_runs_used INTEGER NOT NULL CHECK (idle_runs_used >= 0),", "idle_runs_used INTEGER NOT NULL CHECK (idle_runs_used >= 0 AND idle_runs_used <= idle_run_budget),", 1)
			statement = strings.Replace(statement, "CHECK (idle_policy <> 'standing_instruction' OR (idle_after_seconds >= 1 AND idle_instruction <> ''))", "CHECK (idle_policy <> 'standing_instruction' OR (idle_after_seconds >= 1 AND idle_instruction <> '' AND idle_run_budget >= 1))", 1)
			statements[i] = statement
		}
	}
	return statements
}

// v12 predates the shared queue: every task named one agent. v13 lets a
// queued task carry no assigned agent (any eligible worker claims it at
// admission) and requires an agent on every other status.
func v12SchemaStatements() []string {
	statements := v13SchemaStatements()
	for i, statement := range statements {
		if _, name := schemaObjectIdentity(statement); name == "tasks" {
			statements[i] = v12Tasks
		}
	}
	return statements
}

// v13SchemaStatements is the exact schema before managed Git worktrees.
func v13SchemaStatements() []string {
	statements := v14SchemaStatements()
	for i, statement := range statements {
		if _, name := schemaObjectIdentity(statement); name == "changes" {
			statements[i] = v13Changes
		}
	}
	return statements
}

func v14SchemaStatements() []string {
	statements := make([]string, 0, len(schemaStatements)-1)
	for _, statement := range v15SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		if !strings.Contains(statement, "task_prerequisites") && name != "task_conflict_paths" {
			statements = append(statements, statement)
		}
	}
	return statements
}

// v15 predates the optional project library. No existing table is changed.
func v15SchemaStatements() []string {
	statements := make([]string, 0, len(schemaStatements)-5)
	for _, statement := range v16SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		if !strings.HasPrefix(name, "project_content_") && name != "task_content_references" {
			statements = append(statements, statement)
		}
	}
	return statements
}

// v16 predates optional outcome and comparison decisions.
func v16SchemaStatements() []string {
	statements := make([]string, 0, len(schemaStatements)-1)
	for _, statement := range v17SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		if !strings.HasPrefix(name, "project_outcome_") {
			statements = append(statements, statement)
		}
	}
	return statements
}

const v17ProjectContentRevisions = `CREATE TABLE project_content_revisions (
    id BLOB NOT NULL CHECK (length(id) = 16),
    project_id BLOB NOT NULL CHECK (length(project_id) = 16) REFERENCES projects(id),
    kind TEXT NOT NULL CHECK (length(CAST(kind AS BLOB)) BETWEEN 1 AND 64),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    title TEXT NOT NULL CHECK (length(CAST(title AS BLOB)) BETWEEN 1 AND 1024),
    description TEXT NOT NULL CHECK (length(CAST(description AS BLOB)) <= 4096),
    body TEXT NOT NULL CHECK (length(CAST(body AS BLOB)) <= 1048576),
    author TEXT NOT NULL CHECK (length(CAST(author AS BLOB)) BETWEEN 1 AND 256),
    source_references TEXT NOT NULL CHECK (length(CAST(source_references AS BLOB)) <= 32768),
    deprecated INTEGER NOT NULL CHECK (deprecated IN (0, 1)),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    PRIMARY KEY (id, revision)
) STRICT, WITHOUT ROWID`

func v17SchemaStatements() []string {
	statements := v18SchemaStatements()
	for i, statement := range statements {
		if _, name := schemaObjectIdentity(statement); name == "project_content_revisions" {
			statements[i] = v17ProjectContentRevisions
		}
	}
	return statements
}

// v18 predates retained terminal diagnostics and yielded continuations.
func v18SchemaStatements() []string {
	base := v20SchemaStatements()
	statements := make([]string, 0, len(base)-4)
	for _, statement := range base {
		_, name := schemaObjectIdentity(statement)
		if name == "terminal_diagnostics" || name == "continuations" || name == "continuations_one_waiting_per_condition" || name == "continuations_admission_queue" {
			continue
		}
		if name == "invalidations" {
			statement = strings.Replace(statement, ", 'continuation'", "", 1)
		}
		statements = append(statements, statement)
	}
	return statements
}

// v19 has retained terminal diagnostics but predates yielded continuations.
func v19SchemaStatements() []string {
	base := v20SchemaStatements()
	statements := make([]string, 0, len(base)-3)
	for _, statement := range base {
		_, name := schemaObjectIdentity(statement)
		if name == "continuations" || name == "continuations_one_waiting_per_condition" || name == "continuations_admission_queue" {
			continue
		}
		if name == "invalidations" {
			statement = strings.Replace(statement, ", 'continuation'", "", 1)
		}
		statements = append(statements, statement)
	}
	return statements
}

// v20 is the final single-root schema. Keep it frozen: v21 adds repository
// bindings without changing any existing project, task, Change, or history ID.
func v20SchemaStatements() []string {
	statements := make([]string, 0, len(schemaStatements)-5)
	for _, statement := range v25SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		switch name {
		case "repository_source_identities", "project_repositories", "project_repositories_root_unique", "project_repositories_one_default", "task_repository_bindings", "content_repository_bindings", "intake_sources", "intake_sources_repository_destination", "intake_source_trusted_logins", "intake_acceptances", "intake_task_bindings", "intake_task_bindings_acceptance":
			continue
		}
		statements = append(statements, statement)
	}
	return statements
}

func v21SchemaStatements() []string {
	statements := v22SchemaStatements()
	for i, statement := range statements {
		if _, name := schemaObjectIdentity(statement); name == "project_repositories" {
			statements[i] = strings.Replace(statement, "    name TEXT NOT NULL CHECK (length(CAST(name AS BLOB)) BETWEEN 1 AND 128),\n", "", 1)
		}
	}
	return statements
}

func v22SchemaStatements() []string {
	statements := make([]string, 0, len(schemaStatements)-1)
	for _, statement := range v25SchemaStatements() {
		if _, name := schemaObjectIdentity(statement); name != "repository_source_identities" && name != "intake_sources" && name != "intake_sources_repository_destination" && name != "intake_source_trusted_logins" && name != "intake_acceptances" && name != "intake_task_bindings" && name != "intake_task_bindings_acceptance" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func v23SchemaStatements() []string {
	statements := make([]string, 0, len(schemaStatements)-4)
	for _, statement := range v24SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		switch name {
		case "intake_sources", "intake_sources_repository_destination", "intake_source_trusted_logins", "intake_acceptances", "intake_task_bindings", "intake_task_bindings_acceptance":
			continue
		}
		statements = append(statements, statement)
	}
	return statements
}

func v24SchemaStatements() []string {
	statements := v25SchemaStatements()
	for i, statement := range statements {
		if _, name := schemaObjectIdentity(statement); name == "repository_source_identities" {
			statements[i] = strings.Replace(statement, "    github_repository_id INTEGER CHECK (github_repository_id IS NULL OR (github_repository_id > 0 AND root_dev IS NOT NULL AND publication_repository <> '')),\n", "", 1)
		}
	}
	return statements
}

func v25SchemaStatements() []string {
	statements := make([]string, 0, len(schemaStatements)-1)
	for _, statement := range v26SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		if name != "intake_acceptance_reviews" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func v30SchemaStatements() []string {
	statements := make([]string, 0, len(schemaStatements))
	for _, statement := range v31SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		switch name {
		case "intake_sources":
			statement = `CREATE TABLE intake_sources (
    id BLOB PRIMARY KEY CHECK (length(id) = 16 AND id <> zeroblob(16)),
    github_repository_id INTEGER NOT NULL CHECK (github_repository_id > 0),
    github_repository_name TEXT NOT NULL CHECK (length(CAST(github_repository_name AS BLOB)) BETWEEN 3 AND 140),
    project_id BLOB NOT NULL CHECK (length(project_id) = 16) REFERENCES projects(id),
    target_repository_id BLOB NOT NULL CHECK (length(target_repository_id) = 16) REFERENCES project_repositories(id),
    overseer_agent_id BLOB CHECK (overseer_agent_id IS NULL OR length(overseer_agent_id) = 16) REFERENCES agents(id),
    label_filter TEXT NOT NULL CHECK (length(CAST(label_filter AS BLOB)) <= 100),
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    policy TEXT NOT NULL CHECK (policy IN ('manual', 'trusted_authors')),
    poll_seconds INTEGER NOT NULL CHECK (poll_seconds BETWEEN 5 AND 86400),
    admission_limit INTEGER NOT NULL CHECK (admission_limit BETWEEN 1 AND 200),
    revision INTEGER NOT NULL CHECK (revision >= 1),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    updated_at_ms INTEGER NOT NULL CHECK (updated_at_ms >= created_at_ms)
) STRICT, WITHOUT ROWID`
		case "intake_acceptances":
			statement = `CREATE TABLE intake_acceptances (
    source_repository TEXT NOT NULL CHECK (length(source_repository) BETWEEN 3 AND 140),
    id BLOB PRIMARY KEY CHECK (length(id) = 16 AND id <> zeroblob(16)),
    github_repository_id INTEGER NOT NULL CHECK (github_repository_id > 0),
    issue_number INTEGER NOT NULL CHECK (issue_number > 0),
    issue_node_id TEXT NOT NULL CHECK (length(CAST(issue_node_id AS BLOB)) BETWEEN 1 AND 256),
    title TEXT NOT NULL CHECK (length(CAST(title AS BLOB)) BETWEEN 1 AND 900),
    body TEXT NOT NULL CHECK (length(CAST(body AS BLOB)) <= 5000),
    body_hash BLOB NOT NULL CHECK (length(body_hash) = 32),
    project_id BLOB NOT NULL CHECK (length(project_id) = 16) REFERENCES projects(id),
    repository_id BLOB NOT NULL CHECK (length(repository_id) = 16) REFERENCES project_repositories(id),
    overseer_agent_id BLOB CHECK (overseer_agent_id IS NULL OR length(overseer_agent_id) = 16) REFERENCES agents(id),
    task_id BLOB NOT NULL UNIQUE CHECK (length(task_id) = 16 AND task_id <> zeroblob(16)),
    incarnation_id BLOB NOT NULL UNIQUE CHECK (length(incarnation_id) = 16 AND incarnation_id <> zeroblob(16)),
    withdrawn_at_ms INTEGER CHECK (withdrawn_at_ms IS NULL OR withdrawn_at_ms >= 0),
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms >= 0),
    UNIQUE(github_repository_id, issue_number, issue_node_id, title, body_hash, project_id, repository_id),
    CHECK (withdrawn_at_ms IS NULL OR withdrawn_at_ms >= created_at_ms)
) STRICT, WITHOUT ROWID`
		case "intake_sources_repository_destination":
			statement = `CREATE UNIQUE INDEX intake_sources_repository_destination ON intake_sources(github_repository_id, project_id, target_repository_id, label_filter)`
		}
		statements = append(statements, statement)
	}
	return statements
}

func v31SchemaStatements() []string {
	var statements []string
	for _, statement := range schemaStatements {
		_, name := schemaObjectIdentity(statement)
		if name != "mission_task_bindings" && name != "mission_task_bindings_mission" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func v29SchemaStatements() []string {
	var statements []string
	for _, statement := range v30SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		if name != "project_tokens" && name != "run_tokens" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func v28SchemaStatements() []string {
	var statements []string
	for _, statement := range v29SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		if name != "attachment_retention" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func v27SchemaStatements() []string {
	var statements []string
	for _, statement := range v28SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		if name != "task_attachments" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func v26SchemaStatements() []string {
	statements := make([]string, 0, len(schemaStatements)-3)
	for _, statement := range v27SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		if name != "intake_legacy_migrations" && name != "intake_legacy_suppressions" && name != "intake_source_priorities" {
			statements = append(statements, statement)
		}
	}
	return statements
}

// priorSchemaStatements is the exact v3 schema: v4 with the frozen agents
// definition substituted. Every other statement is read live from
// schemaStatements, so editing any of them silently changes what this claims
// an earlier home was and stops recognising it. The next schema change has to
// freeze the text it replaces here and extend the migration, in the same
// change; TestSchemaDigestsArePinned pins every set and fails until it does.
func priorSchemaStatements() []string {
	v4 := v4SchemaStatements()
	statements := make([]string, 0, len(v4))
	for _, statement := range v4 {
		if _, name := schemaObjectIdentity(statement); name == "agents" {
			statement = priorAgents
		}
		statements = append(statements, statement)
	}
	return statements
}

// previousSchemaStatements is the exact v2 schema: v3 with the two frozen
// browser definitions substituted.
func previousSchemaStatements() []string {
	prior := priorSchemaStatements()
	statements := make([]string, 0, len(prior))
	for _, statement := range prior {
		switch _, name := schemaObjectIdentity(statement); name {
		case "browser_pairing_challenges":
			statement = previousPairingChallenges
		case "browser_clients":
			statement = previousBrowserClients
		}
		statements = append(statements, statement)
	}
	return statements
}

// legacySchemaStatements is the exact v1 schema: v2 without the accounts
// objects and with the two frozen definitions substituted.
func legacySchemaStatements() []string {
	previous := previousSchemaStatements()
	statements := make([]string, 0, len(previous))
	for _, statement := range previous {
		switch _, name := schemaObjectIdentity(statement); name {
		case "accounts", "accounts_provider_home_unique":
			continue
		case "agents":
			statement = legacyAgents
		case "invalidations":
			statement = legacyInvalidations
		}
		statements = append(statements, statement)
	}
	return statements
}

// validateOpenableSnapshot accepts either a current database or an exact
// earlier shape that Open migrates. The durable-control pass reads columns only
// v2 has, so a v1 snapshot is checked here for its exact schema and integrity
// alone and validated in full once the migration has committed.
func validateOpenableSnapshot(ctx context.Context, connection *sql.Conn) error {
	if _, version, err := inspectIdentity(ctx, connection); err != nil {
		return err
	} else if statements, migrates := migratableSchema(version); migrates {
		if err := validateSchemaVersion(ctx, connection, version, statements); err != nil {
			return err
		}
		return validateIntegrity(ctx, connection)
	}
	return validateDatabaseSnapshot(ctx, connection)
}

// migratableSchema is the exact schema text of each user_version Open still
// migrates from.
func migratableSchema(version int) ([]string, bool) {
	switch version {
	case legacyUserVersion:
		return legacySchemaStatements(), true
	case previousUserVersion:
		return previousSchemaStatements(), true
	case priorUserVersion:
		return priorSchemaStatements(), true
	case v4UserVersion:
		return v4SchemaStatements(), true
	case v5UserVersion:
		return v5SchemaStatements(), true
	case v6UserVersion:
		return v6SchemaStatements(), true
	case v7UserVersion:
		return v7SchemaStatements(), true
	case v8UserVersion:
		return v8SchemaStatements(), true
	case v9UserVersion:
		return v9SchemaStatements(), true
	case v10UserVersion:
		return v10SchemaStatements(), true
	case v11UserVersion:
		return v11SchemaStatements(), true
	case v12UserVersion:
		return v12SchemaStatements(), true
	case v13UserVersion:
		return v13SchemaStatements(), true
	case v14UserVersion:
		return v14SchemaStatements(), true
	case v15UserVersion:
		return v15SchemaStatements(), true
	case v16UserVersion:
		return v16SchemaStatements(), true
	case v17UserVersion:
		return v17SchemaStatements(), true
	case v18UserVersion:
		return v18SchemaStatements(), true
	case v19UserVersion:
		return v19SchemaStatements(), true
	case v20UserVersion:
		return v20SchemaStatements(), true
	case v21UserVersion:
		return v21SchemaStatements(), true
	case v22UserVersion:
		return v22SchemaStatements(), true
	case v23UserVersion:
		return v23SchemaStatements(), true
	case v24UserVersion:
		return v24SchemaStatements(), true
	case v25UserVersion:
		return v25SchemaStatements(), true
	case v26UserVersion:
		return v26SchemaStatements(), true
	case v27UserVersion:
		return v27SchemaStatements(), true
	case v28UserVersion:
		return v28SchemaStatements(), true
	case v30UserVersion:
		return v30SchemaStatements(), true
	case v31UserVersion:
		return v31SchemaStatements(), true
	case v29UserVersion:
		return v29SchemaStatements(), true
	}
	return nil, false
}

// migrateLegacy upgrades an exact earlier home to the current schema, every
// version step it needs in one transaction, or leaves the database
// byte-untouched and refuses. Open calls it before the store is published, so
// it is the only writer. One case is neither: if restoring foreign key
// enforcement fails after the commit, the home is migrated and this open is
// still refused, because a connection that cannot enforce foreign keys must
// not serve the daemon. The next open then finds a current home and nothing
// to migrate. The migration is one way -- a build from before it refuses the
// newer user_version -- so the rollback plan for an operator home is the
// operator's pre-upgrade copy of factory.sqlite3, which docs/install.md tells
// them to take; nothing here makes one.
func (store *Store) migrateLegacy(ctx context.Context) error {
	connection, err := store.writerConnection(ctx)
	if err != nil {
		return err
	}
	_, version, err := inspectIdentity(ctx, connection)
	if err != nil {
		releaseUncertainConnection(connection)
		return err
	}
	all := []func(context.Context, *sql.Conn) error{migrateLegacyTransaction, migratePreviousTransaction, migratePriorTransaction, migrateV4Transaction, migrateV5Transaction, migrateV6Transaction, migrateV7Transaction, migrateV8Transaction, migrateV9Transaction, migrateV10Transaction, migrateV11Transaction, migrateV12Transaction, migrateV13Transaction, migrateV14Transaction, migrateV15Transaction, migrateV16Transaction, migrateV17Transaction, migrateV18Transaction, migrateV19Transaction, migrateV20Transaction, migrateV21Transaction, migrateV22Transaction, migrateV23Transaction, migrateV24Transaction, migrateV25Transaction, migrateV26Transaction, migrateV27Transaction, migrateV28Transaction, migrateV29Transaction, migrateV30Transaction, migrateV31Transaction}
	var steps []func(context.Context, *sql.Conn) error
	switch version {
	case legacyUserVersion:
		steps = all
	case previousUserVersion:
		steps = all[1:]
	case priorUserVersion:
		steps = all[2:]
	case v4UserVersion:
		steps = all[3:]
	case v5UserVersion:
		steps = all[4:]
	case v6UserVersion:
		steps = all[5:]
	case v7UserVersion:
		steps = all[6:]
	case v8UserVersion:
		steps = all[7:]
	case v9UserVersion:
		steps = all[8:]
	case v10UserVersion:
		steps = all[9:]
	case v11UserVersion:
		steps = all[10:]
	case v12UserVersion:
		steps = all[11:]
	case v13UserVersion:
		steps = all[12:]
	case v14UserVersion:
		steps = all[13:]
	case v15UserVersion:
		steps = all[14:]
	case v16UserVersion:
		steps = all[15:]
	case v17UserVersion:
		steps = all[16:]
	case v18UserVersion:
		steps = all[17:]
	case v19UserVersion:
		steps = all[18:]
	case v20UserVersion:
		steps = all[19:]
	case v21UserVersion:
		steps = all[20:]
	case v22UserVersion:
		steps = all[21:]
	case v23UserVersion:
		steps = all[22:]
	case v24UserVersion:
		steps = all[23:]
	case v25UserVersion:
		steps = all[24:]
	case v26UserVersion:
		steps = all[25:]
	case v27UserVersion:
		steps = all[26:]
	case v28UserVersion:
		steps = all[27:]
	case v30UserVersion:
		steps = all[29:]
	case v31UserVersion:
		steps = all[30:]
	case v29UserVersion:
		steps = all[28:]
	default:
		return connection.Close()
	}
	err = migrateWithoutForeignKeys(ctx, connection, func(ctx context.Context, connection *sql.Conn) error {
		for _, step := range steps {
			if err := step(ctx, connection); err != nil {
				return err
			}
		}
		// The durable-control pass belongs at the end: it reads columns only
		// the current schema has, and a home it rejects keeps every original
		// byte because nothing has been committed yet.
		return validateDurableControls(ctx, connection)
	})
	if err != nil {
		releaseUncertainConnection(connection)
		return err
	}
	return connection.Close()
}

func migrateWithoutForeignKeys(ctx context.Context, connection *sql.Conn, step func(context.Context, *sql.Conn) error) error {
	// Rebuilding a table other tables reference (agents; browser_clients) is
	// a DROP SQLite refuses with foreign keys enforced. PRAGMA foreign_keys is
	// a no-op inside a transaction, so enforcement is toggled around the step
	// and restored before the connection can be reused.
	if err := setForeignKeys(ctx, connection, false); err != nil {
		return err
	}
	return errors.Join(migrateTransaction(ctx, connection, step), setForeignKeys(ctx, connection, true))
}

func migrateTransaction(ctx context.Context, connection *sql.Conn, step func(context.Context, *sql.Conn) error) (resultErr error) {
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer func() {
		if resultErr == nil {
			return
		}
		if _, err := connection.ExecContext(context.Background(), "ROLLBACK"); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("roll back schema migration: %w", err))
		}
	}()
	if err := step(ctx, connection); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	return nil
}

// migrateLegacyTransaction takes an exact v1 home to v2: the accounts objects,
// agents.account_id and the wider invalidations check.
func migrateLegacyTransaction(ctx context.Context, connection *sql.Conn) error {
	// Refuse anything that is not exactly the schema this migration was written
	// against, foreign key violations included.
	if err := validateSchemaVersion(ctx, connection, legacyUserVersion, legacySchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(previousSchemaStatements())
	for _, name := range []string{"accounts", "accounts_provider_home_unique"} {
		if _, err := connection.ExecContext(ctx, target[name].sql); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
	}
	if err := rebuildTable(ctx, connection, target, "agents", legacyAgentColumns, "agents_id_project_unique", "", ""); err != nil {
		return err
	}
	if err := rebuildTable(ctx, connection, target, "invalidations", legacyInvalidationColumns, "invalidations_entity_revision_unique", "", ""); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", previousUserVersion)); err != nil {
		return fmt.Errorf("set sqlite user version: %w", err)
	}
	// The rewritten text must be byte-identical to the v2 statements, with
	// foreign_key_check, before the next step or the commit.
	return validateSchemaVersion(ctx, connection, previousUserVersion, previousSchemaStatements())
}

// migratePreviousTransaction takes an exact v2 home to v3: the browser
// capability mask gains the administration bit, and every pairing minted on
// loopback (the ones that carry terminal_input, which a relay pairing never
// does) is granted it, pending challenges included.
func migratePreviousTransaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, previousUserVersion, previousSchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(priorSchemaStatements())
	if err := rebuildTable(ctx, connection, target, "browser_pairing_challenges", previousPairingChallengeColumns, "", "", ""); err != nil {
		return err
	}
	if err := rebuildTable(ctx, connection, target, "browser_clients", previousBrowserClientColumns, "", "", ""); err != nil {
		return err
	}
	for _, table := range []string{"browser_pairing_challenges", "browser_clients"} {
		if _, err := connection.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET capability_mask = capability_mask | %d WHERE capability_mask & %d <> 0",
			table, BrowserCapabilityAdministration, BrowserCapabilityTerminalInput)); err != nil {
			return fmt.Errorf("grant administration in %s: %w", table, err)
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", priorUserVersion)); err != nil {
		return fmt.Errorf("set sqlite user version: %w", err)
	}
	return validateSchemaVersion(ctx, connection, priorUserVersion, priorSchemaStatements())
}

// migratePriorTransaction takes an exact v3 home to v4: every agent gains the
// idle rule columns, holding the rule that was always in force, wait.
func migratePriorTransaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, priorUserVersion, priorSchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(v4SchemaStatements())
	if err := rebuildTable(ctx, connection, target, "agents", priorAgentColumns, "agents_id_project_unique", idleColumns, idleDefaults); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v4UserVersion)); err != nil {
		return fmt.Errorf("set sqlite user version: %w", err)
	}
	return validateSchemaVersion(ctx, connection, v4UserVersion, v4SchemaStatements())
}

// migrateV4Transaction takes an exact v4 home to v5. A NULL boundary keeps
// every legacy task body opaque; the first new send-back records its own.
func migrateV4Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v4UserVersion, v4SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(v5SchemaStatements())
	if err := rebuildTable(ctx, connection, target, "tasks", v4TaskColumns, "tasks_id_project_incarnation_unique", "tasks_incarnation_unique", "tasks_canonical_queue", "sent_back_instruction_bytes", "NULL"); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v5UserVersion)); err != nil {
		return fmt.Errorf("set sqlite user version: %w", err)
	}
	return validateSchemaVersion(ctx, connection, v5UserVersion, v5SchemaStatements())
}

// migrateV5Transaction adds independent receipt and cursor tables. Existing
// task, run, and invalidation history stays byte-for-byte intact.
func migrateV5Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v5UserVersion, v5SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(v6SchemaStatements())
	for _, name := range []string{"task_interventions", "task_interventions_project_task", "overseer_wake_cursors"} {
		if _, err := connection.ExecContext(ctx, target[name].sql); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v6UserVersion)); err != nil {
		return fmt.Errorf("set sqlite user version: %w", err)
	}
	return validateSchemaVersion(ctx, connection, v6UserVersion, v6SchemaStatements())
}

// migrateV6Transaction adds peer history and widens the invalidation journal
// atomically so an existing wake cursor never sees a foreign entity kind.
func migrateV6Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v6UserVersion, v6SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(v7SchemaStatements())
	if err := rebuildTable(ctx, connection, target, "invalidations", legacyInvalidationColumns, "invalidations_entity_revision_unique", "", ""); err != nil {
		return err
	}
	for _, name := range []string{"peer_questions", "peer_questions_source_key_unique", "peer_questions_recipient_delivery_unique", "peer_questions_answer_delivery_unique", "peer_questions_task_history"} {
		if _, err := connection.ExecContext(ctx, target[name].sql); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v7UserVersion)); err != nil {
		return fmt.Errorf("set sqlite user version: %w", err)
	}
	return validateSchemaVersion(ctx, connection, v7UserVersion, v7SchemaStatements())
}

func migrateV7Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v7UserVersion, v7SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(v8SchemaStatements())
	if err := rebuildTable(ctx, connection, target, "projects", "id, name, root, verification_policy, revision, created_at_ms, updated_at_ms", "projects_root_unique", "", ""); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `UPDATE projects SET runs_used = (SELECT COUNT(*) FROM runs WHERE runs.project_id = projects.id)`); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v8UserVersion)); err != nil {
		return fmt.Errorf("set sqlite user version: %w", err)
	}
	return validateSchemaVersion(ctx, connection, v8UserVersion, v8SchemaStatements())
}

func migrateV8Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v8UserVersion, v8SchemaStatements()); err != nil {
		return err
	}
	columns := "id, run_id, idempotency_key, kind, reason_code, question_text, status, delivery_id, delivery_started_at_ms, resolution_kind, closed_at_ms, revision, created_at_ms, updated_at_ms"
	if err := rebuildTable(ctx, connection, expectedSchemaOf(v9SchemaStatements()), "human_requests", columns, "human_requests_one_unresolved_per_run", "", ""); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v9UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v9UserVersion, v9SchemaStatements())
}

// migrateV9Transaction adds optional durable sprite appearance; existing
// agents keep their deterministic id-derived look.
func migrateV9Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v9UserVersion, v9SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(v10SchemaStatements())
	if err := rebuildTable(ctx, connection, target, "agents", v7AgentColumns, "agents_id_project_unique", "appearance", "''"); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v10UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v10UserVersion, v10SchemaStatements())
}

func migrateV10Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v10UserVersion, v10SchemaStatements()); err != nil {
		return err
	}
	columns := `id, project_id, name, role, provider, model, reasoning_effort, account_id, paused, appearance, idle_policy, idle_after_seconds, idle_instruction, idle_run_budget, idle_runs_used, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms`
	if err := rebuildTable(ctx, connection, expectedSchemaOf(v11SchemaStatements()), "agents", columns, "agents_id_project_unique", "archived", "0"); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v11UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v11UserVersion, v11SchemaStatements())
}

func migrateV11Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v11UserVersion, v11SchemaStatements()); err != nil {
		return err
	}
	columns := `id, project_id, name, role, provider, model, reasoning_effort, account_id, paused, archived, appearance, idle_policy, idle_after_seconds, idle_instruction, idle_run_budget, idle_runs_used, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms`
	if err := rebuildTable(ctx, connection, expectedSchemaOf(v12SchemaStatements()), "agents", columns, "agents_id_project_unique", "", ""); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v12UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v12UserVersion, v12SchemaStatements())
}

// migrateV12Transaction relaxes tasks.assigned_agent_id to nullable. Every
// existing task keeps the agent it always had; only new shared work is NULL.
func migrateV12Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v12UserVersion, v12SchemaStatements()); err != nil {
		return err
	}
	columns := `id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body, sent_back_instruction_bytes, status, priority, blocked_reason, result, completed_at_ms, revision, created_at_ms, updated_at_ms`
	if err := rebuildTable(ctx, connection, expectedSchemaOf(v13SchemaStatements()), "tasks", columns, "tasks_id_project_incarnation_unique", "tasks_incarnation_unique", "tasks_canonical_queue", "", ""); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v13UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v13UserVersion, v13SchemaStatements())
}

// migrateV13Transaction replaces the Git-free tree facts of every Change with
// the head column. Every row keeps its identity, phase, base, repository,
// chronology, revision and settled run; no row gets a head, since none of
// those trees is a worktree yet. Each is adopted, at its recorded base and
// with its contents untouched, the next time it is reopened or read.
func migrateV13Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v13UserVersion, v13SchemaStatements()); err != nil {
		return err
	}
	columns := `id, project_id, task_id, task_incarnation_id, phase, object_format, base_commit, repository_dev, repository_inode, prepared_at_ms, available_at_ms, settled_run_id, revision, created_at_ms, updated_at_ms`
	if err := rebuildTable(ctx, connection, expectedSchemaOf(schemaStatements), "changes", columns, "changes_id_project_task_incarnation_unique", "changes_task_incarnation_unique", "head_commit", "NULL"); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v14UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v14UserVersion, v14SchemaStatements())
}

func migrateV14Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v14UserVersion, v14SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(schemaStatements)
	for _, name := range []string{"task_prerequisites", "task_prerequisites_upstream", "task_conflict_paths"} {
		if _, err := connection.ExecContext(ctx, target[name].sql); err != nil {
			return err
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v15UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v15UserVersion, v15SchemaStatements())
}

func migrateV15Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v15UserVersion, v15SchemaStatements()); err != nil {
		return err
	}
	for _, statement := range v16SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		if strings.HasPrefix(name, "project_content_") || name == "task_content_references" {
			if _, err := connection.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v16UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v16UserVersion, v16SchemaStatements())
}
func migrateV16Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v16UserVersion, v16SchemaStatements()); err != nil {
		return err
	}
	for _, statement := range v17SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		if strings.HasPrefix(name, "project_outcome_") {
			if _, err := connection.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v17UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v17UserVersion, v17SchemaStatements())
}

func migrateV17Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v17UserVersion, v17SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(schemaStatements)
	columns := "id, project_id, kind, revision, title, description, body, author, source_references, deprecated, created_at_ms"
	if err := rebuildTable(ctx, connection, target, "project_content_revisions", columns, "project_content_revisions_project_kind", "object_format, commit_oid, path, repository_dev, repository_inode", "NULL, NULL, NULL, NULL, NULL"); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v18UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v18UserVersion, v18SchemaStatements())
}

func migrateV18Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v18UserVersion, v18SchemaStatements()); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, expectedSchemaOf(schemaStatements)["terminal_diagnostics"].sql); err != nil {
		return fmt.Errorf("create terminal diagnostics: %w", err)
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v19UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v19UserVersion, v19SchemaStatements())
}

// rebuildTable replaces one table with its target definition, preserving every
// row. SQLite cannot alter the constraints of a STRICT table, and a renamed
// table keeps its old text, while the exact-schema check compares that text
// byte for byte. So the target statement is executed verbatim under the real
// name and the rows wait in a scratch table for the moment the real one is
// absent. indexes names the table's separate index statements, if it has any;
// added and values name columns the target has and the rows do not, with the
// constant each row gets.
func rebuildTable(ctx context.Context, connection *sql.Conn, target map[string]schemaObject, table, columns string, indexes ...string) error {
	if len(indexes) < 2 {
		return fmt.Errorf("rebuild %s: missing added columns and values", table)
	}
	added, values := indexes[len(indexes)-2], indexes[len(indexes)-1]
	scratch := table + "_pre_migration"
	into, from := columns, columns
	if added != "" {
		into, from = columns+", "+added, columns+", "+values
	}
	statements := []string{
		"CREATE TABLE " + scratch + " AS SELECT " + columns + " FROM " + table,
		"DROP TABLE " + table,
		target[table].sql,
		"INSERT INTO " + table + "(" + into + ") SELECT " + from + " FROM " + scratch,
		"DROP TABLE " + scratch,
	}
	for _, index := range indexes[:len(indexes)-2] {
		if index != "" {
			statements = append(statements, target[index].sql)
		}
	}
	for _, statement := range statements {
		if _, err := connection.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("rebuild %s: %w", table, err)
		}
	}
	return nil
}

func setForeignKeys(ctx context.Context, connection *sql.Conn, enforced bool) error {
	statement, want := "PRAGMA foreign_keys = OFF", 0
	if enforced {
		statement, want = "PRAGMA foreign_keys = ON", 1
	}
	if _, err := connection.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("set sqlite foreign key enforcement: %w", err)
	}
	var got int
	if err := connection.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&got); err != nil {
		return fmt.Errorf("read sqlite foreign key enforcement: %w", err)
	}
	if got != want {
		return fmt.Errorf("%w: sqlite foreign key enforcement is %d, want %d", ErrCorruptState, got, want)
	}
	return nil
}

func migrateV19Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v19UserVersion, v19SchemaStatements()); err != nil {
		return err
	}
	if err := rebuildTable(ctx, connection, expectedSchemaOf(schemaStatements), "invalidations", "sequence, occurred_at_ms, entity_kind, entity_id, revision, deleted", "invalidations_entity_revision_unique", "", ""); err != nil {
		return err
	}
	for _, statement := range v25SchemaStatements() {
		_, name := schemaObjectIdentity(statement)
		if name == "continuations" || name == "continuations_one_waiting_per_condition" || name == "continuations_admission_queue" {
			if _, err := connection.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v20UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v20UserVersion, v20SchemaStatements())
}

func migrateV20Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v20UserVersion, v20SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(v21SchemaStatements())
	for _, name := range []string{"project_repositories", "project_repositories_root_unique", "project_repositories_one_default", "task_repository_bindings", "content_repository_bindings"} {
		if _, err := connection.ExecContext(ctx, target[name].sql); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO project_repositories(id, project_id, root, base_ref, enabled, is_default, revision, created_at_ms, updated_at_ms) SELECT id, id, root, ?, 1, 1, 1, created_at_ms, updated_at_ms FROM projects`, inheritedRepositoryBase); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO task_repository_bindings(task_id, repository_id, base_ref) SELECT id, project_id, ? FROM tasks`, inheritedRepositoryBase); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO content_repository_bindings(content_id, content_revision, repository_id) SELECT id, revision, project_id FROM project_content_revisions`); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v21UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v21UserVersion, v21SchemaStatements())
}

func migrateV21Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v21UserVersion, v21SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(v22SchemaStatements())
	if _, err := connection.ExecContext(ctx, `CREATE TABLE project_repositories_pre_migration AS SELECT id, project_id, root, base_ref, enabled, is_default, revision, created_at_ms, updated_at_ms FROM project_repositories`); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `DROP TABLE project_repositories`); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, target["project_repositories"].sql); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO project_repositories(id, project_id, name, root, base_ref, enabled, is_default, revision, created_at_ms, updated_at_ms) SELECT r.id, r.project_id, p.name, r.root, r.base_ref, r.enabled, r.is_default, r.revision, r.created_at_ms, r.updated_at_ms FROM project_repositories_pre_migration r JOIN projects p ON p.id = r.project_id`); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `DROP TABLE project_repositories_pre_migration`); err != nil {
		return err
	}
	for _, name := range []string{"project_repositories_root_unique", "project_repositories_one_default"} {
		if _, err := connection.ExecContext(ctx, target[name].sql); err != nil {
			return err
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v22UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v22UserVersion, v22SchemaStatements())
}

func migrateV22Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v22UserVersion, v22SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(v23SchemaStatements())
	if _, err := connection.ExecContext(ctx, target["repository_source_identities"].sql); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO repository_source_identities(repository_id) SELECT id FROM project_repositories`); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v23UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v23UserVersion, v23SchemaStatements())
}

func migrateV23Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v23UserVersion, v23SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(v24SchemaStatements())
	for _, name := range []string{"intake_sources", "intake_sources_repository_destination", "intake_source_trusted_logins", "intake_acceptances", "intake_task_bindings", "intake_task_bindings_acceptance"} {
		if _, err := connection.ExecContext(ctx, target[name].sql); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v24UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v24UserVersion, v24SchemaStatements())
}

func migrateV24Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v24UserVersion, v24SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(schemaStatements)
	if _, err := connection.ExecContext(ctx, `ALTER TABLE repository_source_identities RENAME TO repository_source_identities_pre_migration`); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, target["repository_source_identities"].sql); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `INSERT INTO repository_source_identities(repository_id, root_dev, root_inode, git_dev, git_inode, origin_digest, publication_repository) SELECT repository_id, root_dev, root_inode, git_dev, git_inode, origin_digest, publication_repository FROM repository_source_identities_pre_migration`); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, `DROP TABLE repository_source_identities_pre_migration`); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v25UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v25UserVersion, v25SchemaStatements())
}

func migrateV25Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v25UserVersion, v25SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(schemaStatements)
	for _, name := range []string{"intake_acceptance_reviews"} {
		if _, err := connection.ExecContext(ctx, target[name].sql); err != nil {
			return err
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v26UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v26UserVersion, v26SchemaStatements())
}

func migrateV26Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v26UserVersion, v26SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(schemaStatements)
	for _, name := range []string{"intake_legacy_migrations", "intake_legacy_suppressions", "intake_source_priorities"} {
		if _, err := connection.ExecContext(ctx, target[name].sql); err != nil {
			return err
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v27UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v27UserVersion, v27SchemaStatements())
}

func migrateV27Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v27UserVersion, v27SchemaStatements()); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, expectedSchemaOf(schemaStatements)["task_attachments"].sql); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v28UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v28UserVersion, v28SchemaStatements())
}

func migrateV28Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v28UserVersion, v28SchemaStatements()); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, expectedSchemaOf(schemaStatements)["attachment_retention"].sql); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v29UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v29UserVersion, v29SchemaStatements())
}

func migrateV29Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v29UserVersion, v29SchemaStatements()); err != nil {
		return err
	}
	for _, table := range []string{"project_tokens", "run_tokens"} {
		if _, err := connection.ExecContext(ctx, expectedSchemaOf(schemaStatements)[table].sql); err != nil {
			return err
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v30UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v30UserVersion, v30SchemaStatements())
}

func migrateV30Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v30UserVersion, v30SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(v31SchemaStatements())
	if err := rebuildTable(ctx, connection, target, "intake_sources", strings.TrimSuffix(intakeSourceColumns, ", linear_team_id"), "intake_sources_repository_destination", "linear_team_id", "''"); err != nil {
		return err
	}
	if err := rebuildTable(ctx, connection, target, "intake_acceptances", strings.TrimSuffix(intakeAcceptanceColumns, ", linear_team_id, source_url"), "linear_team_id, source_url", "'', ''"); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v31UserVersion)); err != nil {
		return err
	}
	return validateSchemaVersion(ctx, connection, v31UserVersion, v31SchemaStatements())
}

func migrateV31Transaction(ctx context.Context, connection *sql.Conn) error {
	if err := validateSchemaVersion(ctx, connection, v31UserVersion, v31SchemaStatements()); err != nil {
		return err
	}
	target := expectedSchemaOf(schemaStatements)
	for _, name := range []string{"mission_task_bindings", "mission_task_bindings_mission"} {
		if _, err := connection.ExecContext(ctx, target[name].sql); err != nil {
			return err
		}
	}
	if _, err := connection.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", userVersion)); err != nil {
		return err
	}
	return validateExactSchema(ctx, connection)
}
