package kernel

import (
	"context"
	"fmt"
)

// TerminalTarget is the exact durable identity needed to attach to an agent's
// live terminal. Its fields are private so callers cannot substitute an ID or
// revision after Store has checked the relationship.
type TerminalTarget struct {
	projectID       ProjectID
	agentID         AgentID
	runID           RunID
	sessionID       TerminalSessionID
	runRevision     Revision
	sessionRevision Revision
}

func (target TerminalTarget) ProjectID() ProjectID         { return target.projectID }
func (target TerminalTarget) AgentID() AgentID             { return target.agentID }
func (target TerminalTarget) RunID() RunID                 { return target.runID }
func (target TerminalTarget) SessionID() TerminalSessionID { return target.sessionID }
func (target TerminalTarget) RunRevision() Revision        { return target.runRevision }
func (target TerminalTarget) SessionRevision() Revision    { return target.sessionRevision }

func newTerminalTarget(projectID ProjectID, agentID AgentID, run Run, session TerminalSession) (TerminalTarget, error) {
	if projectID.zero() || agentID.zero() || run.ID.zero() || session.ID.zero() || run.AgentID != agentID || run.ProjectID != projectID || session.RunID != run.ID || run.Phase != RunRunning || session.State != TerminalSessionActive {
		return TerminalTarget{}, fmt.Errorf("%w: invalid terminal target relationship", ErrCorruptState)
	}
	if run.Revision.Int64() < 1 || session.Revision.Int64() < 1 {
		return TerminalTarget{}, fmt.Errorf("%w: invalid terminal target revision", ErrCorruptState)
	}
	return TerminalTarget{
		projectID: projectID, agentID: agentID, runID: run.ID, sessionID: session.ID,
		runRevision: run.Revision, sessionRevision: session.Revision,
	}, nil
}

// ResolveAgentTerminalTarget resolves an exact public-state observation to
// the currently attachable run and terminal session. All reads happen on one
// pinned SQLite snapshot; daemon live-owner state is deliberately not part of
// this authority decision.
func (store *Store) ResolveAgentTerminalTarget(ctx context.Context, clientID BrowserClientID, agentID AgentID, expectedAgent Revision, expectedHead EventSequence) (TerminalTarget, bool, error) {
	if err := validateBrowserID(clientID); err != nil || agentID.zero() || expectedAgent.Int64() < 1 || expectedHead.Int64() < 0 {
		if err != nil {
			return TerminalTarget{}, false, err
		}
		return TerminalTarget{}, false, fmt.Errorf("%w: invalid terminal target request", ErrInvalidValue)
	}
	tx, err := store.beginRead(ctx)
	if err != nil {
		return TerminalTarget{}, false, err
	}
	defer tx.Close()

	client, found, err := browserClientByID(ctx, tx.connection, clientID)
	if err != nil {
		return TerminalTarget{}, false, err
	}
	if !found || client.RevokedAt != nil || !client.CapabilityMask.Has(BrowserCapabilityObserve) {
		return TerminalTarget{}, false, ErrUnauthorized
	}

	state, err := factoryState(ctx, tx.connection)
	if err != nil {
		return TerminalTarget{}, false, err
	}
	if state.Head != expectedHead {
		return TerminalTarget{}, false, ErrRevisionConflict
	}

	agent, found, err := agentByID(ctx, tx.connection, agentID)
	if err != nil {
		return TerminalTarget{}, false, err
	}
	if !found {
		return TerminalTarget{}, false, ErrNotFound
	}
	if agent.Revision != expectedAgent {
		return TerminalTarget{}, false, ErrRevisionConflict
	}

	rows, err := tx.connection.QueryContext(ctx, `SELECT `+runColumns+` FROM runs WHERE agent_id = ? AND phase <> 'terminal' LIMIT 2`, agentID.Bytes())
	if err != nil {
		return TerminalTarget{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return TerminalTarget{}, false, err
		}
		return TerminalTarget{}, false, nil
	}
	run, found, err := scanRun(rows)
	if err != nil {
		return TerminalTarget{}, false, err
	}
	if !found {
		return TerminalTarget{}, false, fmt.Errorf("%w: missing selected run", ErrCorruptState)
	}
	if rows.Next() {
		_ = rows.Close()
		return TerminalTarget{}, false, fmt.Errorf("%w: agent has multiple nonterminal runs", ErrCorruptState)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return TerminalTarget{}, false, err
	}
	if err := rows.Close(); err != nil {
		return TerminalTarget{}, false, err
	}

	if run.AgentID != agent.ID || run.ProjectID != agent.ProjectID {
		return TerminalTarget{}, false, fmt.Errorf("%w: run does not match agent relationship", ErrCorruptState)
	}
	relationships, err := loadRunRelationships(ctx, tx.connection, run)
	if err != nil {
		return TerminalTarget{}, false, err
	}
	if run.Phase != RunRunning {
		return TerminalTarget{}, false, nil
	}
	returnTarget, err := newTerminalTarget(agent.ProjectID, agent.ID, run, relationships.session)
	if err != nil {
		return TerminalTarget{}, false, err
	}
	return returnTarget, true, nil
}
