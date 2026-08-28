package kernel

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
)

func validateDurableControls(ctx context.Context, connection *sql.Conn) error {
	state, err := validateDurableEntityControls(ctx, connection)
	if err != nil {
		return err
	}
	return validateInvalidations(ctx, connection, state)
}

// validateDurableEntityControls validates every durable authority row except
// invalidations. WatchAfter validates the invalidation log itself so it can
// distinguish a finite browser restart from malformed durable values.
func validateDurableEntityControls(ctx context.Context, connection *sql.Conn) (FactoryState, error) {
	state, err := factoryState(ctx, connection)
	if err != nil {
		return FactoryState{}, err
	}
	if err := validateProjects(ctx, connection); err != nil {
		return FactoryState{}, err
	}
	if err := validateAgents(ctx, connection); err != nil {
		return FactoryState{}, err
	}
	if err := validateTasks(ctx, connection); err != nil {
		return FactoryState{}, err
	}
	if err := validateChanges(ctx, connection); err != nil {
		return FactoryState{}, err
	}
	if err := validateRuns(ctx, connection); err != nil {
		return FactoryState{}, err
	}
	if err := validateResources(ctx, connection); err != nil {
		return FactoryState{}, err
	}
	if err := validateOwnershipLocators(ctx, connection); err != nil {
		return FactoryState{}, err
	}
	if err := validateRunRelationships(ctx, connection); err != nil {
		return FactoryState{}, err
	}
	if err := validateBrowserAuthority(ctx, connection); err != nil {
		return FactoryState{}, err
	}
	if err := validateHumanRequests(ctx, connection); err != nil {
		return FactoryState{}, err
	}
	if err := validateResourceIdentityCollisions(ctx, connection); err != nil {
		return FactoryState{}, err
	}
	return state, nil
}

func validateHumanRequests(ctx context.Context, connection *sql.Conn) error {
	var unresolved int64
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM human_requests WHERE status IN ('open', 'delivering', 'delivery_unknown')`).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved < 0 || unresolved > MaxOpenHumanRequests {
		return fmt.Errorf("%w: human request bound exceeded", ErrCorruptState)
	}
	rows, err := connection.QueryContext(ctx, `SELECT `+humanRequestColumns+` FROM human_requests ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		request, found, scanErr := scanHumanRequest(rows)
		if scanErr != nil || !found {
			if scanErr == nil {
				scanErr = ErrCorruptState
			}
			return scanErr
		}
		var phase string
		if err := connection.QueryRowContext(ctx, `SELECT phase FROM runs WHERE id = ?`, request.RunID.Bytes()).Scan(&phase); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: human request run is missing", ErrCorruptState)
			}
			return err
		}
		validPhase := false
		switch request.Status {
		case HumanRequestOpen, HumanRequestDelivering:
			validPhase = phase == RunRunning.String()
		case HumanRequestDeliveryUnknown:
			// Recovery can discover an uncertain delivery while the run is
			// still running; finalization preserves that uncertainty until
			// terminalization makes it stale.
			validPhase = phase == RunRunning.String() || phase == RunFinalizing.String()
		case HumanRequestResolved:
			// A reply may resolve before the run later enters finalizing or
			// terminal. The request remains historical after that point.
			validPhase = phase == RunRunning.String() || phase == RunFinalizing.String() || phase == RunTerminal.String()
		case HumanRequestStale:
			validPhase = phase == RunFinalizing.String() || phase == RunTerminal.String()
		}
		if !validPhase {
			return fmt.Errorf("%w: human request status has impossible run phase", ErrCorruptState)
		}
	}
	return rows.Err()
}

// validateBrowserAuthority is intentionally a concrete integrity pass. Browser
// rows are credentials and leases, not a general permission/state framework.
func validateBrowserAuthority(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT id FROM browser_clients ORDER BY id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		id, err := BrowserClientIDFromBytes(raw)
		if err != nil {
			rows.Close()
			return fmt.Errorf("%w: invalid browser client identity", ErrCorruptState)
		}
		if _, found, err := browserClientByID(ctx, connection, id); err != nil || !found {
			rows.Close()
			if err == nil {
				err = ErrCorruptState
			}
			return err
		}
	}
	if err := closeValidatedBrowserRows(rows); err != nil {
		return err
	}
	rows, err = connection.QueryContext(ctx, `SELECT secret_digest FROM browser_pairing_challenges ORDER BY secret_digest`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		digest, err := BrowserChallengeDigestFromBytes(raw)
		if err != nil {
			rows.Close()
			return fmt.Errorf("%w: invalid browser challenge identity", ErrCorruptState)
		}
		if _, found, err := browserChallengeByDigest(ctx, connection, digest); err != nil || !found {
			rows.Close()
			if err == nil {
				err = ErrCorruptState
			}
			return err
		}
	}
	if err := closeValidatedBrowserRows(rows); err != nil {
		return err
	}
	var challengeCount int64
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_pairing_challenges`).Scan(&challengeCount); err != nil {
		return err
	}
	if challengeCount < 0 || challengeCount > 32 {
		return fmt.Errorf("%w: browser pairing challenge retention exceeded", ErrCorruptState)
	}
	var count int64
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*) FROM browser_security_events`).Scan(&count); err != nil {
		return err
	}
	if count > EventRetentionLimit {
		return fmt.Errorf("%w: browser security event retention exceeded", ErrCorruptState)
	}
	rows, err = connection.QueryContext(ctx, `SELECT sequence, kind, client_id, occurred_at_ms FROM browser_security_events ORDER BY sequence`)
	if err != nil {
		return err
	}
	var previous int64
	for rows.Next() {
		var sequence, occurred int64
		var kind string
		var client nullableBlob
		if err := rows.Scan(&sequence, &kind, &client, &occurred); err != nil {
			rows.Close()
			return err
		}
		parsedKind := BrowserSecurityEventKind(kind)
		if sequence < 1 || sequence <= previous || !validBrowserSecurityKind(parsedKind) || isBrowserChallengeEvent(parsedKind) != !client.valid || occurred < 0 {
			rows.Close()
			return fmt.Errorf("%w: invalid browser security event", ErrCorruptState)
		}
		if client.valid {
			id, err := BrowserClientIDFromBytes(client.bytes)
			if err != nil {
				rows.Close()
				return fmt.Errorf("%w: invalid browser event client", ErrCorruptState)
			}
			if _, found, err := browserClientByID(ctx, connection, id); err != nil || !found {
				rows.Close()
				if err == nil {
					err = ErrCorruptState
				}
				return err
			}
		}
		previous = sequence
	}
	if err := closeValidatedBrowserRows(rows); err != nil {
		return err
	}
	rows, err = connection.QueryContext(ctx, `SELECT id, run_id, state, lease_client_id, lease_generation, lease_expires_at_ms, last_input_sequence FROM terminal_sessions`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var rawID, rawRun []byte
		var state string
		var client nullableBlob
		var generation, expiry, sequence sql.NullInt64
		if err := rows.Scan(&rawID, &rawRun, &state, &client, &generation, &expiry, &sequence); err != nil {
			rows.Close()
			return err
		}
		if !generation.Valid || !sequence.Valid || generation.Int64 < 0 || sequence.Int64 < 0 || expiry.Valid && expiry.Int64 < 0 {
			rows.Close()
			return fmt.Errorf("%w: invalid terminal lease controls", ErrCorruptState)
		}
		if client.valid {
			if state != "active" || !expiry.Valid || !generation.Valid || generation.Int64 < 1 {
				rows.Close()
				return fmt.Errorf("%w: lease on inactive terminal session", ErrCorruptState)
			}
			id, err := BrowserClientIDFromBytes(client.bytes)
			if err != nil {
				rows.Close()
				return fmt.Errorf("%w: invalid terminal lease client", ErrCorruptState)
			}
			browser, found, err := browserClientByID(ctx, connection, id)
			if err != nil || !found || browser.RevokedAt != nil || !browser.CapabilityMask.Has(BrowserCapabilityTerminalInput) {
				rows.Close()
				return fmt.Errorf("%w: terminal lease client is not active", ErrCorruptState)
			}
			var phase string
			if err := connection.QueryRowContext(ctx, `SELECT phase FROM runs WHERE id = ?`, rawRun).Scan(&phase); err != nil || phase != "running" {
				rows.Close()
				return fmt.Errorf("%w: terminal lease run is not running", ErrCorruptState)
			}
		} else if expiry.Valid || sequence.Int64 != 0 {
			rows.Close()
			return fmt.Errorf("%w: terminal lease nullability mismatch", ErrCorruptState)
		}
		if _, err := TerminalSessionIDFromBytes(rawID); err != nil {
			rows.Close()
			return fmt.Errorf("%w: invalid terminal session identity", ErrCorruptState)
		}
	}
	return closeValidatedBrowserRows(rows)
}

func closeValidatedBrowserRows(rows *sql.Rows) error {
	return errors.Join(rows.Err(), rows.Close())
}

func validateResourceIdentityCollisions(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT left_resource.run_id, left_resource.kind, right_resource.run_id, right_resource.kind
		FROM resources AS left_resource
		JOIN resources AS right_resource ON left_resource.id < right_resource.id AND (
			(left_resource.path_dev IS NOT NULL AND left_resource.path_dev = right_resource.path_dev AND left_resource.path_inode = right_resource.path_inode) OR
			(left_resource.pid IS NOT NULL AND left_resource.pid = right_resource.pid AND left_resource.pgid = right_resource.pgid AND left_resource.birth_digest = right_resource.birth_digest)
		)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var leftRunRaw, rightRunRaw []byte
		var leftKindRaw, rightKindRaw string
		if err := rows.Scan(&leftRunRaw, &leftKindRaw, &rightRunRaw, &rightKindRaw); err != nil {
			return err
		}
		leftRun, leftRunErr := RunIDFromBytes(leftRunRaw)
		rightRun, rightRunErr := RunIDFromBytes(rightRunRaw)
		leftKind, leftKindErr := parseResourceKind(leftKindRaw)
		rightKind, rightKindErr := parseResourceKind(rightKindRaw)
		providerPair := leftRun == rightRun && (leftKind == ResourceProviderProcess && rightKind == ResourceProviderGroup || leftKind == ResourceProviderGroup && rightKind == ResourceProviderProcess)
		if leftRunErr != nil || rightRunErr != nil || leftKindErr != nil || rightKindErr != nil || !providerPair {
			return fmt.Errorf("%w: resource identities collide", ErrCorruptState)
		}
	}
	return rows.Err()
}

func validateRunRelationships(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT `+runColumns+` FROM runs`)
	if err != nil {
		return err
	}
	var runs []Run
	for rows.Next() {
		run, found, err := scanRun(rows)
		if err != nil || !found {
			rows.Close()
			if err == nil {
				err = ErrCorruptState
			}
			return err
		}
		runs = append(runs, run)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, run := range runs {
		if _, err := loadRunRelationships(ctx, connection, run); err != nil {
			return err
		}
	}
	return nil
}

type runRelationships struct {
	task      Task
	change    *Change
	resources []Resource
	session   TerminalSession
}

func loadRunRelationships(ctx context.Context, connection *sql.Conn, run Run) (runRelationships, error) {
	task, found, err := taskByID(ctx, connection, run.TaskID)
	if err != nil || !found {
		if err == nil {
			err = ErrCorruptState
		}
		return runRelationships{}, err
	}
	if task.ProjectID != run.ProjectID || task.IncarnationID != run.TaskIncarnationID {
		return runRelationships{}, fmt.Errorf("%w: task does not match run", ErrCorruptState)
	}
	if err := validateTaskRunTopology(ctx, connection, task); err != nil {
		return runRelationships{}, err
	}
	if run.Phase != RunTerminal {
		if !taskMatchesRun(task, run) {
			return runRelationships{}, fmt.Errorf("%w: task does not match run", ErrCorruptState)
		}
	} else if task.WorkRevision.Int64() < run.AdmittedTaskWorkRevision.Int64() || task.WorkRevision == run.AdmittedTaskWorkRevision && !taskMatchesRun(task, run) {
		return runRelationships{}, fmt.Errorf("%w: task does not match run", ErrCorruptState)
	}

	var change *Change
	if run.ChangeID != nil {
		value, found, err := changeByID(ctx, connection, *run.ChangeID)
		if err != nil || !found {
			if err == nil {
				err = ErrCorruptState
			}
			return runRelationships{}, err
		}
		if run.Role != RoleWorker || value.ProjectID != run.ProjectID || value.TaskID != run.TaskID || value.TaskIncarnationID != run.TaskIncarnationID || run.RunningAt != nil && (value.Phase != ChangeAvailable || value.AvailableAt == nil || value.AvailableAt.Int64() > run.RunningAt.Int64()) {
			return runRelationships{}, fmt.Errorf("%w: Change does not match run", ErrCorruptState)
		}
		if run.Phase == RunTerminal && task.WorkRevision == run.AdmittedTaskWorkRevision && run.TerminalAt != nil && value.UpdatedAt.Int64() > run.TerminalAt.Int64() {
			return runRelationships{}, fmt.Errorf("%w: terminal run predates Change checkpoint", ErrCorruptState)
		}
		change = &value
	} else if run.Role != RoleOrchestrator {
		return runRelationships{}, fmt.Errorf("%w: missing worker Change", ErrCorruptState)
	}

	resources, err := resourcesForRun(ctx, connection, run.ID)
	if err != nil {
		return runRelationships{}, err
	}
	if !resourcesMatchRunPhase(run.Phase, resources) {
		return runRelationships{}, fmt.Errorf("%w: resources do not match run phase", ErrCorruptState)
	}
	var providerProcessIdentity, providerGroupIdentity ResourceIdentity
	for _, resource := range resources {
		switch resource.Kind {
		case ResourceProviderProcess:
			providerProcessIdentity = resource.Identity
		case ResourceProviderGroup:
			providerGroupIdentity = resource.Identity
		}
	}
	if !resourceIdentityEqual(providerProcessIdentity, providerGroupIdentity) {
		return runRelationships{}, fmt.Errorf("%w: provider process/group identity is not atomic", ErrCorruptState)
	}
	var providerProcessActivation, providerGroupActivation *UnixMillis
	for _, resource := range resources {
		switch resource.Kind {
		case ResourceProviderProcess:
			providerProcessActivation = resource.ActivatedAt
		case ResourceProviderGroup:
			providerGroupActivation = resource.ActivatedAt
		}
	}
	if (providerProcessActivation == nil) != (providerGroupActivation == nil) || providerProcessActivation != nil && *providerProcessActivation != *providerGroupActivation {
		return runRelationships{}, fmt.Errorf("%w: provider process/group activation is not atomic", ErrCorruptState)
	}
	for _, resource := range resources {
		if resource.State != ResourceReleased || resource.Identity.Empty() {
			continue
		}
		switch resource.Kind {
		case ResourceProviderProcess, ResourceProviderGroup:
			if run.ProviderExit == nil {
				return runRelationships{}, fmt.Errorf("%w: released provider resource lacks exit evidence", ErrCorruptState)
			}
		case ResourceRunnerProcess:
			if run.RunnerExit == nil {
				return runRelationships{}, fmt.Errorf("%w: released runner resource lacks exit evidence", ErrCorruptState)
			}
		}
	}
	if err := validatePersistedProcessExits(run, resources); err != nil {
		return runRelationships{}, err
	}
	session, err := validateRunTerminalSession(ctx, connection, run)
	if err != nil {
		return runRelationships{}, err
	}
	if err := validateRunResourceChronology(run, change, resources); err != nil {
		return runRelationships{}, err
	}
	return runRelationships{task: task, change: change, resources: resources, session: session}, nil
}

func validateRunTerminalSession(ctx context.Context, connection *sql.Conn, run Run) (TerminalSession, error) {
	session, found, err := terminalSessionByRunID(ctx, connection, run.ID)
	if err != nil {
		return TerminalSession{}, err
	}
	if !found {
		return TerminalSession{}, fmt.Errorf("%w: run has no terminal session", ErrCorruptState)
	}
	if session.DeclaredAt != run.AdmittedAt || session.UpdatedAt.Int64() > run.UpdatedAt.Int64() {
		return TerminalSession{}, fmt.Errorf("%w: terminal session chronology does not match run", ErrCorruptState)
	}
	if session.ActivatedAt == nil {
		if run.RunningAt != nil {
			return TerminalSession{}, fmt.Errorf("%w: running run lacks terminal activation", ErrCorruptState)
		}
	} else if run.RunningAt == nil || *session.ActivatedAt != *run.RunningAt {
		return TerminalSession{}, fmt.Errorf("%w: terminal activation does not match run", ErrCorruptState)
	}
	if session.ClosedAt != nil && run.TerminalAt != nil && session.ClosedAt.Int64() > run.TerminalAt.Int64() {
		return TerminalSession{}, fmt.Errorf("%w: terminal session closes after run", ErrCorruptState)
	}
	switch run.Phase {
	case RunAdmitted:
		if session.State != TerminalSessionDeclared {
			return TerminalSession{}, fmt.Errorf("%w: admitted run terminal session is not declared", ErrCorruptState)
		}
	case RunRunning:
		if session.State != TerminalSessionActive {
			return TerminalSession{}, fmt.Errorf("%w: running run terminal session is not active", ErrCorruptState)
		}
	case RunTerminal:
		if session.State != TerminalSessionClosed {
			return TerminalSession{}, fmt.Errorf("%w: terminal run terminal session is not closed", ErrCorruptState)
		}
	}
	return session, nil
}

func validateRunResourceChronology(run Run, change *Change, resources []Resource) error {
	if run.Phase == RunRunning {
		if run.RunningAt == nil {
			return fmt.Errorf("%w: running run lacks running time", ErrCorruptState)
		}
		runningAt := run.RunningAt.Int64()
		if change != nil && (change.Phase != ChangeAvailable || change.AvailableAt == nil || change.AvailableAt.Int64() > runningAt || change.UpdatedAt.Int64() > runningAt) {
			return fmt.Errorf("%w: Change became available after run activation", ErrCorruptState)
		}
		for _, resource := range resources {
			if resource.State == ResourceActive && (resource.ActivatedAt == nil || resource.ActivatedAt.Int64() > runningAt || resource.UpdatedAt.Int64() > runningAt) {
				return fmt.Errorf("%w: active resource became current after run activation", ErrCorruptState)
			}
		}
	}
	if run.RunningAt != nil {
		runningAt := run.RunningAt.Int64()
		for _, resource := range resources {
			if resource.ActivatedAt != nil && resource.ActivatedAt.Int64() > runningAt {
				return fmt.Errorf("%w: resource activation follows run activation", ErrCorruptState)
			}
		}
	}
	if run.Phase == RunFinalizing || run.Phase == RunTerminal {
		if run.FinalizingAt == nil {
			return fmt.Errorf("%w: cleanup phase lacks finalizing time", ErrCorruptState)
		}
		finalizingAt := run.FinalizingAt.Int64()
		for _, resource := range resources {
			if resource.ActivatedAt != nil && resource.ActivatedAt.Int64() > finalizingAt {
				return fmt.Errorf("%w: resource activation follows finalizing", ErrCorruptState)
			}
			if resource.UpdatedAt.Int64() < finalizingAt {
				return fmt.Errorf("%w: resource update predates finalizing", ErrCorruptState)
			}
			if run.Phase == RunTerminal && resource.State == ResourceReleased && (resource.ReleasedAt == nil || resource.ReleasedAt.Int64() > run.TerminalAt.Int64()) {
				return fmt.Errorf("%w: resource release follows terminal run", ErrCorruptState)
			}
		}
	}
	return nil
}

func validatePersistedProcessExits(run Run, resources []Resource) error {
	// Exit rows carry no identity of their own. Their authority is the exact
	// activated resource (or provider process/group pair) still recorded here.
	var providerProcess, providerGroup, runnerProcess *Resource
	for index := range resources {
		resource := &resources[index]
		switch resource.Kind {
		case ResourceProviderProcess:
			providerProcess = resource
		case ResourceProviderGroup:
			providerGroup = resource
		case ResourceRunnerProcess:
			runnerProcess = resource
		}
	}
	if run.ProviderExit != nil {
		if providerProcess == nil || providerGroup == nil || providerProcess.ActivatedAt == nil || providerGroup.ActivatedAt == nil || providerProcess.Identity.Empty() || !resourceIdentityEqual(providerProcess.Identity, providerGroup.Identity) || *providerProcess.ActivatedAt != *providerGroup.ActivatedAt || run.ProviderExit.At().Int64() < providerProcess.ActivatedAt.Int64() {
			return fmt.Errorf("%w: provider exit does not match activated provider resources", ErrCorruptState)
		}
		for _, resource := range []*Resource{providerProcess, providerGroup} {
			if resource.ReleasedAt != nil && resource.ReleasedAt.Int64() < run.ProviderExit.At().Int64() {
				return fmt.Errorf("%w: provider resource released before provider exit", ErrCorruptState)
			}
		}
	}
	if run.RunnerExit != nil {
		if runnerProcess == nil || runnerProcess.ActivatedAt == nil || runnerProcess.Identity.Empty() || run.RunnerExit.At().Int64() < runnerProcess.ActivatedAt.Int64() {
			return fmt.Errorf("%w: runner exit does not match activated runner resource", ErrCorruptState)
		}
		if runnerProcess.ReleasedAt != nil && runnerProcess.ReleasedAt.Int64() < run.RunnerExit.At().Int64() {
			return fmt.Errorf("%w: runner resource released before runner exit", ErrCorruptState)
		}
	}
	return nil
}

func taskMatchesRun(task Task, run Run) bool {
	if run.Phase != RunTerminal {
		return task.AssignedAgentID == run.AgentID && task.WorkRevision == run.AdmittedTaskWorkRevision && task.Status == TaskRunning
	}
	if task.AssignedAgentID != run.AgentID || task.WorkRevision != run.AdmittedTaskWorkRevision || run.Terminal == nil || run.TerminalAt == nil || task.UpdatedAt != *run.TerminalAt {
		return false
	}
	switch run.Terminal.kind {
	case OutcomeSucceeded:
		return task.Status == TaskSucceeded && task.Result == run.Terminal.result && task.CompletedAt != nil && *task.CompletedAt == *run.TerminalAt
	case OutcomeBlocked:
		return task.Status == TaskBlocked && task.BlockedReason == run.Terminal.detail && task.CompletedAt == nil
	case OutcomeFailed:
		return task.Status == TaskFailed && task.CompletedAt != nil && *task.CompletedAt == *run.TerminalAt
	case OutcomeCancelled:
		return task.Status == TaskCancelled && task.CompletedAt != nil && *task.CompletedAt == *run.TerminalAt
	default:
		return false
	}
}

func validateTaskRunTopology(ctx context.Context, connection *sql.Conn, task Task) error {
	rows, err := connection.QueryContext(ctx, `SELECT `+runColumns+` FROM runs WHERE task_id = ? AND task_incarnation_id = ? ORDER BY admitted_task_work_revision`, task.ID.Bytes(), task.IncarnationID.Bytes())
	if err != nil {
		return err
	}
	expectedRevision := int64(1)
	var latest Run
	found := false
	var invalid error
	for rows.Next() {
		run, present, err := scanRun(rows)
		if err != nil || !present || run.ProjectID != task.ProjectID || run.TaskID != task.ID || run.TaskIncarnationID != task.IncarnationID || run.AdmittedTaskWorkRevision.Int64() != expectedRevision {
			if err != nil {
				invalid = err
			} else {
				invalid = fmt.Errorf("%w: invalid task/run revision topology", ErrCorruptState)
			}
			break
		}
		if _, err := validateRunTerminalSession(ctx, connection, run); err != nil {
			invalid = err
			break
		}
		if found {
			if !retryableTerminal(latest) {
				invalid = fmt.Errorf("%w: run history predecessor is not retryable", ErrCorruptState)
				break
			}
			if run.AdmittedAt.Int64() < latest.UpdatedAt.Int64() {
				invalid = fmt.Errorf("%w: run history admission predates predecessor terminal run", ErrCorruptState)
				break
			}
		}
		latest = run
		found = true
		expectedRevision++
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return rowsErr
	}
	if closeErr != nil {
		return closeErr
	}
	if invalid != nil {
		return invalid
	}
	if !found {
		if task.Status == TaskQueued && task.WorkRevision.Int64() == 1 {
			return nil
		}
		return fmt.Errorf("%w: task has no run history", ErrCorruptState)
	}
	delta := task.WorkRevision.Int64() - latest.AdmittedTaskWorkRevision.Int64()
	switch delta {
	case 0:
		if task.Status == TaskQueued || !taskMatchesRun(task, latest) {
			return fmt.Errorf("%w: current task does not match latest run", ErrCorruptState)
		}
	case 1:
		if latest.Phase != RunTerminal || task.Status != TaskQueued {
			return fmt.Errorf("%w: queued task is not the next run revision", ErrCorruptState)
		}
		if !retryableTerminal(latest) {
			return fmt.Errorf("%w: successful task cannot be retried", ErrCorruptState)
		}
		if task.UpdatedAt.Int64() < latest.UpdatedAt.Int64() {
			return fmt.Errorf("%w: queued retry predates predecessor terminal run", ErrCorruptState)
		}
	default:
		return fmt.Errorf("%w: task/run revisions are not contiguous", ErrCorruptState)
	}
	return nil
}

func retryableTerminal(run Run) bool {
	if run.Phase != RunTerminal || run.Terminal == nil {
		return false
	}
	switch run.Terminal.kind {
	case OutcomeBlocked, OutcomeFailed, OutcomeCancelled:
		return true
	default:
		return false
	}
}

type ownershipLocator struct {
	path     string
	changeID *ChangeID
	runID    *RunID
}

func ownershipLocators(ctx context.Context, connection *sql.Conn) ([]ownershipLocator, error) {
	changeRows, err := connection.QueryContext(ctx, `SELECT id, source_root, staging_root FROM changes`)
	if err != nil {
		return nil, err
	}
	var locators []ownershipLocator
	for changeRows.Next() {
		var rawID []byte
		var source, staging string
		if err := changeRows.Scan(&rawID, &source, &staging); err != nil {
			changeRows.Close()
			return nil, err
		}
		id, err := ChangeIDFromBytes(rawID)
		if err != nil || !validOwnedLocator(source) || !validOwnedLocator(staging) {
			changeRows.Close()
			return nil, fmt.Errorf("%w: invalid Change ownership locator", ErrCorruptState)
		}
		changeID := id
		locators = append(locators, ownershipLocator{path: source, changeID: &changeID}, ownershipLocator{path: staging, changeID: &changeID})
	}
	if err := changeRows.Close(); err != nil {
		return nil, err
	}

	resourceRows, err := connection.QueryContext(ctx, `SELECT run_id, path FROM resources WHERE kind = 'runtime_root'`)
	if err != nil {
		return nil, err
	}
	defer resourceRows.Close()
	for resourceRows.Next() {
		var rawRunID []byte
		var path string
		if err := resourceRows.Scan(&rawRunID, &path); err != nil {
			return nil, err
		}
		runID, err := RunIDFromBytes(rawRunID)
		if err != nil || !validOwnedLocator(path) {
			return nil, fmt.Errorf("%w: invalid runtime ownership locator", ErrCorruptState)
		}
		id := runID
		locators = append(locators, ownershipLocator{path: path, runID: &id})
	}
	if err := resourceRows.Err(); err != nil {
		return nil, err
	}
	return locators, nil
}

func validateOwnershipLocators(ctx context.Context, connection *sql.Conn) error {
	locators, err := ownershipLocators(ctx, connection)
	if err != nil {
		return err
	}
	return validateDistinctOwnershipLocators(locators)
}

func validateAdmissionLocatorOwnership(ctx context.Context, connection *sql.Conn, keys AdmissionKeys) error {
	locators, err := ownershipLocators(ctx, connection)
	if err != nil {
		return err
	}
	if err := validateDistinctOwnershipLocators(locators); err != nil {
		return err
	}
	candidates := []string{keys.RuntimeRoot}
	if keys.Change != nil {
		candidates = append(candidates, keys.Change.SourceRoot, keys.Change.StagingRoot)
	}
	for _, locator := range locators {
		if locator.runID != nil && *locator.runID == keys.RunID || locator.changeID != nil && keys.Change != nil && *locator.changeID == keys.Change.ID {
			continue
		}
		for _, candidate := range candidates {
			if pathsOverlap(locator.path, candidate) {
				return ErrConflict
			}
		}
	}
	return nil
}

func validateDistinctOwnershipLocators(locators []ownershipLocator) error {
	paths := make(map[string]struct{}, len(locators))
	for _, locator := range locators {
		if _, duplicate := paths[locator.path]; duplicate {
			return fmt.Errorf("%w: durable ownership locators overlap", ErrCorruptState)
		}
		paths[locator.path] = struct{}{}
	}
	for path := range paths {
		for parent := filepath.Dir(path); parent != string(filepath.Separator); parent = filepath.Dir(parent) {
			if _, overlap := paths[parent]; overlap {
				return fmt.Errorf("%w: durable ownership locators overlap", ErrCorruptState)
			}
		}
	}
	return nil
}

func resourcesMatchRunPhase(phase RunPhase, resources []Resource) bool {
	if !exactResourceSet(resources, false) {
		return false
	}
	for _, resource := range resources {
		switch phase {
		case RunAdmitted:
			if resource.State != ResourceDeclared && resource.State != ResourceActive {
				return false
			}
		case RunRunning:
			if resource.State != ResourceActive {
				return false
			}
		case RunFinalizing:
			if resource.State != ResourceReleasing && resource.State != ResourceUnresolved && resource.State != ResourceReleased {
				return false
			}
		case RunTerminal:
			if resource.State != ResourceReleased {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validateProjects(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT id, name, root, verification_policy, revision, created_at_ms, updated_at_ms FROM projects`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if _, _, err := scanProject(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func validateAgents(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT id, project_id, name, role, provider, model, reasoning_effort, paused, tool_budget_limit, tool_calls_used, revision, created_at_ms, updated_at_ms FROM agents`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if _, _, err := scanAgent(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func validateTasks(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT id, project_id, assigned_agent_id, incarnation_id, work_revision, title, body, status, priority, blocked_reason, result, completed_at_ms, revision, created_at_ms, updated_at_ms FROM tasks`)
	if err != nil {
		return err
	}
	var tasks []Task
	var invalid error
	for rows.Next() {
		task, found, err := scanTask(rows)
		if err != nil || !found {
			if err == nil {
				err = ErrCorruptState
			}
			invalid = err
			break
		}
		tasks = append(tasks, task)
	}
	rowsErr := rows.Err()
	closeErr := rows.Close()
	if rowsErr != nil {
		return rowsErr
	}
	if closeErr != nil {
		return closeErr
	}
	if invalid != nil {
		return invalid
	}
	for _, task := range tasks {
		if err := validateTaskRunTopology(ctx, connection, task); err != nil {
			return err
		}
	}
	return nil
}

func validateChanges(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT `+changeColumns+` FROM changes`)
	if err != nil {
		return err
	}
	for rows.Next() {
		if _, _, err := scanChange(rows); err != nil {
			rows.Close()
			return err
		}
	}
	return rows.Close()
}

func validateRuns(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT `+runColumns+` FROM runs`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if _, _, err := scanRun(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func validateResources(ctx context.Context, connection *sql.Conn) error {
	rows, err := connection.QueryContext(ctx, `SELECT `+resourceColumns+` FROM resources`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if _, _, err := scanResource(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func validateInvalidations(ctx context.Context, connection *sql.Conn, factory FactoryState) error {
	if err := validateInvalidationBounds(ctx, connection, factory); err != nil {
		return err
	}
	if factory.Head.Int64() == 0 {
		return nil
	}
	rows, err := connection.QueryContext(ctx, `SELECT sequence, occurred_at_ms, entity_kind, entity_id, revision, deleted FROM invalidations ORDER BY sequence`)
	if err != nil {
		return err
	}
	defer rows.Close()
	expected := factory.Floor.Int64()
	for rows.Next() {
		var sequence, at, revision, deleted int64
		var kind string
		var id []byte
		if err := rows.Scan(&sequence, &at, &kind, &id, &revision, &deleted); err != nil {
			return err
		}
		entityKind, kindErr := parseEntityKind(kind)
		validID := len(id) == IDBytes
		if entityKind == EntityFactory {
			validID = validID && string(id) == string(factoryEntityID[:])
		} else {
			validID = validID && validNonzeroID(id)
		}
		if sequence != expected || at < 0 || revision < 1 || deleted != 0 && deleted != 1 || kindErr != nil || !validID {
			return fmt.Errorf("%w: invalid invalidation row", ErrCorruptState)
		}
		expected++
	}
	return rows.Err()
}

func validateInvalidationBounds(ctx context.Context, connection *sql.Conn, factory FactoryState) error {
	var count, minimum, maximum int64
	if err := connection.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MIN(sequence), 0), COALESCE(MAX(sequence), 0) FROM invalidations`).Scan(&count, &minimum, &maximum); err != nil {
		return err
	}
	if count < 0 || count > EventRetentionLimit {
		return fmt.Errorf("%w: invalid invalidation count", ErrCorruptState)
	}
	if count == 0 {
		if factory.Head.Int64() != 0 || factory.Floor.Int64() != 1 {
			return fmt.Errorf("%w: empty invalidation log has advanced metadata", ErrCorruptState)
		}
		return nil
	}
	if minimum != factory.Floor.Int64() || maximum != factory.Head.Int64() || maximum-minimum+1 != count {
		return fmt.Errorf("%w: invalidation metadata or gap", ErrCorruptState)
	}
	return nil
}

func validNonzeroID(value []byte) bool {
	_, err := identifierFromBytes(value)
	return err == nil
}

func anyNullIntValid(values ...sql.NullInt64) bool {
	for _, value := range values {
		if value.Valid {
			return true
		}
	}
	return false
}
