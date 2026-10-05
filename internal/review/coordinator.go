// Package review contains the daemon-owned exact-head review state machine.
package review

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Request struct {
	Repository string
	PullNumber uint64
	Head       string
	Base       string
	BaseRef    string
	Body       string
	Provider   string
}

type Operation struct {
	ID           string    `json:"id"`
	EnqueueID    string    `json:"enqueue_id,omitempty"`
	Request      Request   `json:"request"`
	State        string    `json:"state"`
	Retryable    bool      `json:"retryable,omitempty"`
	RetryOf      string    `json:"retry_of,omitempty"`
	Verdict      string    `json:"verdict,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	Submitted    bool      `json:"submitted,omitempty"`
	RoutePending bool      `json:"route_pending,omitempty"`
	Escalation   string    `json:"escalation,omitempty"` // why factoryd cannot advance it: due to the overseer
	Handled      bool      `json:"handled,omitempty"`    // a failure already retried or escalated
	Gates        []GateRun `json:"gates,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// GateRun is one completed run of the operator's full gate at one commit.
// Runs are recorded only once finished, so a restarted operation replays the
// re-gate rule from what it already knows.
type GateRun struct {
	Commit   string   `json:"commit"`
	ExitCode int      `json:"exit_code"`
	Failed   []string `json:"failed,omitempty"`
	Log      string   `json:"log"`
}

type Verdict struct {
	Event string
	Body  string
}

// Receipt is the durable broker observation for a write operation. A receipt
// is the only authority that turns a response-loss phase into a completed
// external effect.
type Receipt struct {
	State string
	Kind  string
	Head  string
	Event string
}

type Observer interface {
	Observe(context.Context, string) (Receipt, error)
}

type Store interface {
	Create(context.Context, Operation) error
	Update(context.Context, Operation) error
}

// RetryStore reserves the one allowed retry and creates its operation in the
// same durable transaction. Keeping the reservation beside the original
// failure prevents two callers from replaying the same failed operation.
type RetryStore interface {
	Store
	CreateRetry(context.Context, Operation, Operation) error
}

type Backend interface {
	CloneReadOnly(context.Context, Request) (string, func(), error)
	// Gate runs the full gate once at commit. An error means nothing ran (a
	// host blocker), never that the commit failed.
	Gate(ctx context.Context, checkout string, op Operation, commit string) (GateRun, error)
	Review(context.Context, string, Request) (Verdict, error)
	Submit(context.Context, Operation, Verdict) error
	Enqueue(context.Context, Operation) error
	// ObserveMerge reads the merge queue's view of an enqueued exact head.
	ObserveMerge(context.Context, Operation) (Merge, error)
}

// Merge is one merge-queue observation: State is the Maintainer's
// ACTIVE_QUEUE, MERGED_AFTER_ENQUEUE_ATTEMPT or NOT_QUEUED, and Failing names
// the head's failing checks when an open pull request is no longer queued.
type Merge struct {
	State   string
	Open    bool
	Failing []string
}

type Coordinator struct {
	Store   Store
	Backend Backend
	Now     func() time.Time
}

func (c Coordinator) Start(ctx context.Context, request Request) (Operation, error) {
	return c.start(ctx, request, "")
}

func (c Coordinator) start(ctx context.Context, request Request, retryOf string) (Operation, error) {
	if c.Store == nil || c.Backend == nil {
		return Operation{}, errors.New("review: incomplete coordinator")
	}
	op, err := Prepare(request, c.Now)
	if err != nil {
		return Operation{}, err
	}
	op.RetryOf = retryOf
	// The record is durable before any provider or clone is started. A crash
	// after this point is therefore observable and retryable, never invisible.
	if err := c.Store.Create(ctx, op); err != nil {
		return Operation{}, err
	}
	return c.Resume(ctx, op)
}

// Prepare mints the durable operation identity without performing external
// work. Publication uses it to claim the review in the same transaction as
// the pull request record.
func Prepare(request Request, now func() time.Time) (Operation, error) {
	if err := validate(request); err != nil || now == nil {
		if err == nil {
			err = errors.New("review: incomplete operation")
		}
		return Operation{}, err
	}
	id, err := operationID()
	if err != nil {
		return Operation{}, err
	}
	stamp := now()
	return Operation{ID: id, Request: request, State: "gating", CreatedAt: stamp, UpdatedAt: stamp}, nil
}

// Resume continues an operation already durably claimed by the caller.
func (c Coordinator) Resume(ctx context.Context, op Operation) (Operation, error) {
	if err := validate(op.Request); err != nil || c.Store == nil || c.Backend == nil || c.Now == nil || op.ID == "" || (op.State != "gating" && op.State != "running" && op.State != "submitting" && op.State != "enqueuing") {
		if err == nil {
			err = errors.New("review: incomplete coordinator")
		}
		return Operation{}, err
	}
	if op.State == "submitting" {
		return c.reconcileSubmitting(ctx, op, nil)
	}
	if op.State == "enqueuing" {
		return c.reconcileEnqueuing(ctx, op, nil)
	}
	checkout, cleanup, err := c.Backend.CloneReadOnly(ctx, op.Request)
	if err != nil {
		return c.failPreSubmit(ctx, op, err)
	}
	defer cleanup()
	if op.State == "gating" {
		if op, err = c.gate(ctx, checkout, op); err != nil || op.State != "running" {
			return op, err
		}
	}
	verdict, err := c.Backend.Review(ctx, checkout, op.Request)
	if err != nil {
		return c.failPreSubmit(ctx, op, err)
	}
	if verdict.Event != "ALLOW" && verdict.Event != "REQUEST_CHANGES" {
		return c.failPreSubmit(ctx, op, errors.New("review: provider returned no valid verdict"))
	}
	op.Verdict, op.Detail, op.State, op.UpdatedAt = strings.ToLower(verdict.Event), verdict.Body, "submitting", c.Now()
	if err := c.Store.Update(ctx, op); err != nil {
		return Operation{}, err
	}
	if err := c.Backend.Submit(ctx, op, verdict); err != nil {
		if _, ok := c.Backend.(Observer); ok {
			return c.reconcileSubmitting(ctx, op, err)
		}
		return c.fail(ctx, op, err, false)
	}
	return c.finishSubmitted(ctx, op)
}

// gate runs the operator's full gate before any reviewer reads the head. A
// head failure is rerun once, and a pass on either run passes. The tests that
// failed in both head runs reproduce; when the runs share none, the failure is
// a flake and review proceeds. Otherwise the gate runs once at the base:
// review proceeds when every reproducing test also fails there, and the head
// goes back to its author with the reproducing tests the base does not share.
// A head run with no parsed test names cannot be compared, so the head goes
// back without a base run.
func (c Coordinator) gate(ctx context.Context, checkout string, op Operation) (Operation, error) {
	for {
		commit, note := nextGate(op)
		if commit == "" && note == "" {
			op.State, op.UpdatedAt = "running", c.Now()
			return op, c.Store.Update(ctx, op)
		}
		if note != "" {
			// Routed like a REQUEST_CHANGES verdict, by the same marker, but
			// nothing is submitted: the gate is not a review.
			op.State, op.Verdict, op.Detail, op.RoutePending, op.UpdatedAt = "completed", "request_changes", note, true, c.Now()
			return op, c.Store.Update(ctx, op)
		}
		run, err := c.Backend.Gate(ctx, checkout, op, commit)
		if err != nil {
			return c.failPreSubmit(ctx, op, fmt.Errorf("review: gate could not run: %w", err))
		}
		op.Gates, op.UpdatedAt = append(op.Gates, run), c.Now()
		if err := c.Store.Update(ctx, op); err != nil {
			return Operation{}, err
		}
	}
}

// nextGate returns the commit to gate next, a send-back note, or neither when
// review may proceed.
func nextGate(op Operation) (string, string) {
	runs := op.Gates
	for _, run := range runs {
		if run.Commit == op.Request.Head && run.ExitCode == 0 {
			return "", ""
		}
	}
	if len(runs) < 2 {
		return op.Request.Head, ""
	}
	var reproduced []string
	for _, name := range runs[0].Failed {
		if slices.Contains(runs[1].Failed, name) && !slices.Contains(reproduced, name) {
			reproduced = append(reproduced, name)
		}
	}
	unnamed := len(runs[0].Failed) == 0 || len(runs[1].Failed) == 0
	if !unnamed && len(reproduced) == 0 {
		return "", ""
	}
	base := "was not run: the head failures name no tests"
	var uncovered []string
	if !unnamed {
		if len(runs) == 2 {
			return op.Request.Base, ""
		}
		for _, name := range reproduced {
			if runs[2].ExitCode == 0 || !slices.Contains(runs[2].Failed, name) {
				uncovered = append(uncovered, name)
			}
		}
		if len(uncovered) == 0 {
			return "", ""
		}
		base = "passed"
		if runs[2].ExitCode != 0 {
			base = "failed, but not on these"
		}
		base = op.Request.Base + " " + base
	}
	tests := "unavailable"
	if len(uncovered) > 0 {
		tests = strings.Join(uncovered, ", ")
	}
	return "", fmt.Sprintf("pre-review full gate failed twice at the exact head (exit %d): tests=%s. Base gate %s. Gate logs: runtimes/gates/%s/1.log and 2.log in the factory home. Exact head %s.", runs[1].ExitCode, tests, base, op.ID, op.Request.Head)
}

func (c Coordinator) finishSubmitted(ctx context.Context, op Operation) (Operation, error) {
	op.Submitted = true
	if op.Verdict == "allow" {
		var err error
		op.EnqueueID, err = operationID()
		if err != nil {
			return c.fail(ctx, op, err, false)
		}
		op.State, op.UpdatedAt = "enqueuing", c.Now()
		if err := c.Store.Update(ctx, op); err != nil {
			return Operation{}, err
		}
		if err := c.Backend.Enqueue(ctx, op); err != nil {
			if _, ok := c.Backend.(Observer); ok {
				return c.reconcileEnqueuing(ctx, op, err)
			}
			return c.fail(ctx, op, err, false)
		}
		op.State = "enqueued"
	} else {
		op.State = "completed"
		// Routing task feedback is a separate durable step. Keep the
		// completed operation recoverable until that step has committed.
		op.RoutePending = true
	}
	op.UpdatedAt = c.Now()
	if err := c.Store.Update(ctx, op); err != nil {
		return Operation{}, err
	}
	return op, nil
}

// ObserveMerge advances an enqueued operation from the merge queue: merged,
// closed, or ejected while the pull request is still open, which goes back to
// the author with the head's failing checks, routed like a REQUEST_CHANGES.
func (c Coordinator) ObserveMerge(ctx context.Context, op Operation) (Operation, error) {
	if op.State != "enqueued" {
		return op, errors.New("review: operation is not enqueued")
	}
	merge, err := c.Backend.ObserveMerge(ctx, op)
	switch {
	case err != nil:
		return op, err
	case merge.State == "MERGED_AFTER_ENQUEUE_ATTEMPT":
		op.State = "merged"
	case merge.State != "NOT_QUEUED":
		return op, nil
	case !merge.Open:
		op.State = "closed"
	default:
		failing := "none reported"
		if len(merge.Failing) > 0 {
			failing = strings.Join(merge.Failing, ", ")
		}
		op.State, op.RoutePending = "ejected", true
		op.Detail = fmt.Sprintf("The merge queue removed exact head %s without merging it. Failing checks: %s.", op.Request.Head, failing)
	}
	op.UpdatedAt = c.Now()
	return op, c.Store.Update(ctx, op)
}

func (c Coordinator) reconcileSubmitting(ctx context.Context, op Operation, cause error) (Operation, error) {
	observer, ok := c.Backend.(Observer)
	if !ok {
		if cause == nil {
			cause = errors.New("review: submit receipt observer unavailable")
		}
		return c.fail(ctx, op, cause, false)
	}
	receipt, err := observer.Observe(ctx, op.ID)
	if err != nil {
		if cause != nil {
			return op, errors.Join(cause, err)
		}
		return op, err
	}
	switch receipt.State {
	case "completed":
		if receipt.Kind != "submit_pull_request_review" || receipt.Head != op.Request.Head || receipt.Event != strings.ToUpper(op.Verdict) {
			return c.fail(ctx, op, errors.New("review: submit receipt does not match operation"), false)
		}
		return c.finishSubmitted(ctx, op)
	case "missing":
		if cause == nil {
			cause = errors.New("review: submit operation is missing")
		}
		return c.fail(ctx, op, cause, false)
	case "planned", "executing", "indeterminate":
		if cause == nil {
			cause = fmt.Errorf("review: submit operation is %s", receipt.State)
		}
		return op, cause
	default:
		return op, errors.New("review: invalid submit operation state")
	}
}

func (c Coordinator) reconcileEnqueuing(ctx context.Context, op Operation, cause error) (Operation, error) {
	observer, ok := c.Backend.(Observer)
	if !ok {
		if cause == nil {
			cause = errors.New("review: enqueue receipt observer unavailable")
		}
		return c.fail(ctx, op, cause, false)
	}
	receipt, err := observer.Observe(ctx, op.EnqueueID)
	if err != nil {
		if cause != nil {
			return op, errors.Join(cause, err)
		}
		return op, err
	}
	switch receipt.State {
	case "completed":
		if receipt.Kind != "enqueue_pull_request" || receipt.Head != op.Request.Head {
			return c.fail(ctx, op, errors.New("review: enqueue receipt does not match operation"), false)
		}
		op.State, op.UpdatedAt = "enqueued", c.Now()
		if err := c.Store.Update(ctx, op); err != nil {
			return Operation{}, err
		}
		return op, nil
	case "missing":
		if cause == nil {
			cause = errors.New("review: enqueue operation is missing")
		}
		return c.fail(ctx, op, cause, false)
	case "planned":
		// The broker claimed the enqueue but never ran it; resending the same
		// operation id resumes that claim (canary 6, #1167).
		if err := c.Backend.Enqueue(ctx, op); err != nil {
			return op, err
		}
		op.State, op.UpdatedAt = "enqueued", c.Now()
		return op, c.Store.Update(ctx, op)
	case "executing", "indeterminate":
		if cause == nil {
			cause = fmt.Errorf("review: enqueue operation is %s", receipt.State)
		}
		return op, cause
	default:
		return op, errors.New("review: invalid enqueue operation state")
	}
}

// Retry starts a new durable attempt for a failed operation. The original
// failure remains immutable history; the request is reused so the retry cannot
// silently move to a different pull-request head.
func (c Coordinator) Retry(ctx context.Context, failed Operation) (Operation, error) {
	op, err := c.ReserveRetry(ctx, failed)
	if err != nil {
		return Operation{}, err
	}
	return c.Resume(ctx, op)
}

// ReserveRetry durably claims a failed operation's one retry without running it.
func (c Coordinator) ReserveRetry(ctx context.Context, failed Operation) (Operation, error) {
	if failed.State != "failed" || !failed.Retryable || failed.ID == "" || failed.RetryOf != "" {
		return Operation{}, errors.New("review: only pre-submit launch failures are retryable")
	}
	store, ok := c.Store.(RetryStore)
	if !ok {
		return Operation{}, errors.New("review: retry reservation unavailable")
	}
	op, err := Prepare(failed.Request, c.Now)
	if err != nil {
		return Operation{}, err
	}
	op.RetryOf = failed.ID
	return op, store.CreateRetry(ctx, failed, op)
}

func (c Coordinator) fail(ctx context.Context, op Operation, cause error, retryable bool) (Operation, error) {
	op.State, op.Detail, op.Retryable, op.UpdatedAt = "failed", cause.Error(), retryable, c.Now()
	// An ALLOW whose enqueue did not happen stays pending, in the same write,
	// until the overseer has been told.
	op.RoutePending = op.EnqueueID != ""
	if err := c.Store.Update(ctx, op); err != nil {
		return Operation{}, errors.Join(cause, err)
	}
	return op, cause
}

func (c Coordinator) failPreSubmit(ctx context.Context, op Operation, cause error) (Operation, error) {
	if op.RetryOf != "" {
		cause = fmt.Errorf("review retries exhausted: %w", cause)
		return c.fail(ctx, op, cause, false)
	}
	return c.fail(ctx, op, cause, true)
}

func validate(r Request) error {
	if !strings.Contains(r.Repository, "/") || r.PullNumber == 0 || !shaRE.MatchString(r.Head) || !shaRE.MatchString(r.Base) || r.BaseRef == "" || len(r.BaseRef) > 240 || strings.ContainsAny(r.BaseRef, "\x00\r\n") || r.Body == "" || (r.Provider != "codex" && r.Provider != "claude") {
		return errors.New("review: invalid exact-head request")
	}
	return nil
}

func operationID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:])), nil
}
