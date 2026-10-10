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

// HeadRE matches one exact commit sha; the merge-queue gate reuses it.
var HeadRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

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
	Request      Request   `json:"request"`
	State        string    `json:"state"`
	Retryable    bool      `json:"retryable"`
	RetryOf      string    `json:"retry_of,omitempty"`
	Verdict      string    `json:"verdict,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	Submitted    bool      `json:"submitted,omitempty"`
	RoutePending bool      `json:"route_pending,omitempty"`
	Escalation   string    `json:"escalation,omitempty"` // why factoryd cannot advance it: due to the overseer
	Handled      bool      `json:"handled,omitempty"`    // a failure already retried or escalated
	Enqueues     int       `json:"enqueues,omitempty"`   // times factoryd put this head in the merge queue
	Failures     int       `json:"failures,omitempty"`   // consecutive merge-stage passes that failed
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
	// Blocked reports a block of record at this exact head. The merge queue's
	// review gate refuses such a head whatever later ALLOW it carries.
	Blocked(context.Context, uint64, string) (bool, error)
}

type Backend interface {
	// StoredPull reads the pull request as GitHub stores it, for the exact head.
	StoredPull(context.Context, uint64, string) (Request, error)
	CloneReadOnly(context.Context, Request) (string, func(), error)
	Review(context.Context, string, Request) (Verdict, error)
	Submit(context.Context, Operation, Verdict) error
	// Enqueue ensures the exact head is in its base's merge queue; a head
	// already queued is success.
	Enqueue(context.Context, Operation) error
	// Observe reads the Maintainer's receipt for one operation id.
	Observe(context.Context, string) (Receipt, error)
	// ObservePull reads the pull request once for the operation's exact head.
	ObservePull(context.Context, Operation) (Pull, error)
}

// Pull is one observation of a published pull request. Queue, checks and
// merge group are read only while it is open at the operation's head.
type Pull struct {
	Head           string
	State          string // open, closed or merged
	Mergeable      *bool  // nil while GitHub computes it
	ReviewDecision string // APPROVED, REVIEW_REQUIRED or CHANGES_REQUESTED
	Queued         bool
	Failing        []string  // required checks at the head that finished unsuccessfully
	Pending        bool      // a required check at the head has not finished
	Group          *GroupRun // the newest completed merge-group run that built the head
}

type GroupRun struct {
	ID         uint64     `json:"run_id"`
	URL        string     `json:"url"`
	Conclusion string     `json:"conclusion"`
	Jobs       []GroupJob `json:"failed_jobs"`
}

type GroupJob struct {
	Name        string   `json:"name"`
	Conclusion  string   `json:"conclusion"`
	Annotations []string `json:"annotations"`
}

func (g *GroupRun) note(head string) string {
	if g == nil {
		return fmt.Sprintf("The merge queue removed exact head %s twice without merging it, with every check on that head passing, and no merge-group run that built it was readable. Rebase onto current origin/main and check the change against it.", head)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The merge queue removed exact head %s twice without merging it: merge-group run %d failed (%s). Fix the failures below against current origin/main.", head, g.ID, g.URL)
	for _, job := range g.Jobs {
		fmt.Fprintf(&b, "\n\nJob %s (%s):", job.Name, job.Conclusion)
		for _, line := range job.Annotations {
			b.WriteString("\n- " + line)
		}
	}
	note := b.String()
	if len(note) > 4000 {
		note = strings.ToValidUTF8(note[:4000], "") + "\n…"
	}
	return note
}

// ErrRefused marks an enqueue GitHub refused as UNPROCESSABLE. The refusal may
// be terminal, or may wait for a review/check state that can change later.
var ErrRefused = errors.New("the merge queue refuses this exact head, so factoryd will not enqueue it again; a new head is reviewed afresh")

// FailuresBeforeEscalation is how many consecutive merge-stage passes may
// fail before the overseer is told: 30 minutes at the 5-minute merge tick.
const FailuresBeforeEscalation = 6

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
	if err := validate(op.Request); err != nil || c.Store == nil || c.Backend == nil || c.Now == nil || op.ID == "" || (op.State != "running" && op.State != "submitting") {
		if err == nil {
			err = errors.New("review: incomplete coordinator")
		}
		return Operation{}, err
	}
	if op.State == "submitting" {
		return c.reconcileSubmitting(ctx, op, nil)
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
	// A second opinion does not clear a same-head block (#1300): the queue's
	// review job would fail and eject every entry behind it.
	if op.Verdict == "allow" {
		if blocked, err := c.Store.Blocked(ctx, op.Request.PullNumber, op.Request.Head); err != nil {
			return op, err
		} else if blocked {
			return c.fail(ctx, op, fmt.Errorf("a blocking verdict of record stands at exact head %s, so the merge queue's review gate refuses it: push a fix or record an operation-bound correction", op.Request.Head), false)
		}
	}
	op.State, op.UpdatedAt = "enqueued", c.Now()
	if op.Verdict != "allow" {
		// Routing task feedback is a separate durable step. Keep the
		// completed operation recoverable until that step has committed.
		op.State, op.RoutePending = "completed", true
	}
	if err := c.Store.Update(ctx, op); err != nil || op.State != "enqueued" {
		return op, err
	}
	return c.Advance(ctx, op)
}

// Advance moves an allowed exact head one step toward merge, deciding from
// one observation of its pull request. It sends the head back to its author
// when it conflicts, a check on it fails, or the queue removes it again after
// the one re-queue, and fails it when the queue refuses it (ErrRefused); it
// never decides from a write's journal.
// A wrong enqueue cannot merge unreviewed code: the merge group's gate still
// requires an ALLOW at the exact head.
func (c Coordinator) Advance(ctx context.Context, op Operation) (Operation, error) {
	if op.State != "enqueued" {
		return op, errors.New("review: operation is not enqueued")
	}
	pull, err := c.Backend.ObservePull(ctx, op)
	if err != nil {
		return c.failedPass(ctx, op, err)
	}
	// A pass that observes resets the failure count and any escalation.
	failures, escalation, enqueues := op.Failures, op.Escalation, op.Enqueues
	op.Failures, op.Escalation = 0, ""
	head := op.Request.Head
	switch {
	case !strings.EqualFold(pull.Head, head):
		op.State = "superseded"
	case pull.State == "merged" || pull.State == "closed":
		op.State = pull.State
	case pull.Mergeable != nil && !*pull.Mergeable:
		op.State, op.RoutePending = "ejected", true
		op.Detail = fmt.Sprintf("Exact head %s conflicts with %s. Rebase this Change onto origin/%s and resolve the conflict.", head, op.Request.BaseRef, op.Request.BaseRef)
	case pull.Queued:
	case len(pull.Failing) > 0:
		op.State, op.RoutePending = "ejected", true
		op.Detail = fmt.Sprintf("Exact head %s cannot merge. Failing checks: %s.", head, strings.Join(pull.Failing, ", "))
	case pull.Pending:
	case op.Enqueues >= 2:
		// Queued, removed, re-queued once and removed again.
		op.State, op.RoutePending, op.Detail = "ejected", true, pull.Group.note(head)
	default:
		if err := c.Backend.Enqueue(ctx, op); errors.Is(err, ErrRefused) {
			// Missing approval or a required check can clear without a new head.
			// Leave the operation waiting so the next refresh gets one attempt.
			if pull.ReviewDecision == "REVIEW_REQUIRED" || pull.Pending {
				return op, nil
			}
			// Terminal for this head: fail it, escalate once, and never re-enqueue.
			return c.fail(ctx, op, err, false)
		} else if err != nil {
			op.Failures, op.Escalation = failures, escalation
			return c.failedPass(ctx, op, err)
		}
		op.Enqueues++
	}
	if op.State == "enqueued" && failures == 0 && escalation == "" && op.Enqueues == enqueues {
		return op, nil // waiting, unchanged
	}
	op.UpdatedAt = c.Now()
	return op, c.Store.Update(ctx, op)
}

// failedPass records a pass that could not observe the pull request or
// enqueue its head. A transient failure, or a merge landing between the read
// and the write, settles on a later pass; one that persists for
// FailuresBeforeEscalation consecutive passes is escalated once.
func (c Coordinator) failedPass(ctx context.Context, op Operation, cause error) (Operation, error) {
	if op.Failures++; op.Failures == FailuresBeforeEscalation {
		op.Escalate(fmt.Sprintf("its merge stage failed %d passes in a row: %v", op.Failures, cause))
	}
	op.UpdatedAt = c.Now()
	return op, errors.Join(cause, c.Store.Update(ctx, op))
}

// Escalate records why factoryd cannot advance the pull request, which makes
// it an item due to the project's overseer.
func (op *Operation) Escalate(why string) {
	op.Escalation = fmt.Sprintf("factoryd cannot advance %s#%d at exact head %s: %s", op.Request.Repository, op.Request.PullNumber, op.Request.Head, why)
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
	if !strings.Contains(r.Repository, "/") || r.PullNumber == 0 || !HeadRE.MatchString(r.Head) || !HeadRE.MatchString(r.Base) || r.BaseRef == "" || len(r.BaseRef) > 240 || strings.ContainsAny(r.BaseRef, "\x00\r\n") || r.Body == "" || (r.Provider != "codex" && r.Provider != "claude") {
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
