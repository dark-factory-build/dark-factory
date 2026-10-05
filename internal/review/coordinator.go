// Package review contains the daemon-owned exact-head review state machine.
package review

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var shaRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ErrRejected is the Maintainer answering conflict or refused: resending the
// same operation cannot succeed.
var ErrRejected = errors.New("review: Maintainer rejected operation")

// enqueueRefusedFor bounds resending an enqueue GitHub refused before
// executing; it matches the daemon's reviewEscalateAfter.
const enqueueRefusedFor = 30 * time.Minute

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
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
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

type Store interface {
	Create(context.Context, Operation) error
	Update(context.Context, Operation) error
	// CreateRetry reserves the one allowed retry and creates its operation in
	// the same durable transaction, so two callers cannot replay one failure.
	CreateRetry(context.Context, Operation, Operation) error
}

type Backend interface {
	// StoredPull reads the pull request as GitHub stores it, for the exact head.
	StoredPull(context.Context, uint64, string) (Request, error)
	CloneReadOnly(context.Context, Request) (string, func(), error)
	Review(context.Context, string, Request) (Verdict, error)
	Submit(context.Context, Operation, Verdict) error
	Enqueue(context.Context, Operation) error
	// Observe reads the Maintainer's receipt for one operation id.
	Observe(context.Context, string) (Receipt, error)
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
	return Operation{ID: id, Request: request, State: "running", CreatedAt: stamp, UpdatedAt: stamp}, nil
}

// Resume continues an operation already durably claimed by the caller.
func (c Coordinator) Resume(ctx context.Context, op Operation) (Operation, error) {
	if err := validate(op.Request); err != nil || c.Store == nil || c.Backend == nil || c.Now == nil || op.ID == "" || (op.State != "running" && op.State != "submitting" && op.State != "enqueuing") {
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
		return c.reconcileSubmitting(ctx, op, err)
	}
	return c.finishSubmitted(ctx, op)
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
			return c.reconcileEnqueuing(ctx, op, err)
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
	receipt, err := c.Backend.Observe(ctx, op.ID)
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
	receipt, err := c.Backend.Observe(ctx, op.EnqueueID)
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
		err := c.Backend.Enqueue(ctx, op)
		if errors.Is(err, ErrRejected) && strings.Contains(err.Error(), "rejected before execution") && c.Now().Sub(op.UpdatedAt) < enqueueRefusedFor {
			// GitHub refused before executing and the broker released the
			// claim: the pull request is not enqueueable yet (checks still
			// running, #1236). It stays enqueuing for a later tick to resend.
			return op, err
		} else if errors.Is(err, ErrRejected) {
			// The pull request moved on (merged, closed, queued or a new
			// head), or stayed unenqueueable past the bound: end it and
			// route the last refusal to the overseer once.
			return c.fail(ctx, op, err, false)
		} else if err != nil {
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
	op, err := Prepare(failed.Request, c.Now)
	if err != nil {
		return Operation{}, err
	}
	op.RetryOf = failed.ID
	return op, c.Store.CreateRetry(ctx, failed, op)
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
