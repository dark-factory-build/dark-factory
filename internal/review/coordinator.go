// Package review contains the daemon-owned exact-head review state machine.
package review

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// HeadRE matches one exact commit sha; the merge-queue gate reuses it.
var HeadRE = regexp.MustCompile(`^[0-9a-f]{40}$`)

type Request struct {
	Repository    string
	PullNumber    uint64
	Head          string
	Base          string
	BaseRef       string
	Body          string
	Provider      string
	Answer        string // its task's answer to the block it disputes, for a review once more
	PriorFindings string // blocking findings from an earlier head, for an incremental re-review
}

type Operation struct {
	ID            string    `json:"id"`
	Request       Request   `json:"request"`
	State         string    `json:"state"`
	Retryable     bool      `json:"retryable"`
	RetryOf       string    `json:"retry_of,omitempty"`
	Verdict       string    `json:"verdict,omitempty"`
	DiffID        string    `json:"diff_id,omitempty"`
	Detail        string    `json:"detail,omitempty"`
	Submitted     bool      `json:"submitted,omitempty"`
	Escalation    string    `json:"escalation,omitempty"`  // why factoryd cannot advance it: due to the overseer
	Handled       bool      `json:"handled,omitempty"`     // a failure already retried or escalated
	Enqueues      int       `json:"enqueues,omitempty"`    // times factoryd put this head in the merge queue
	Failures      int       `json:"failures,omitempty"`    // consecutive passes whose action failed
	Observation   string    `json:"observation,omitempty"` // the pull request the last pass observed
	Rebased       bool      `json:"rebased,omitempty"`     // its Change was brought up to main once at this head
	OwnerApproval bool      `json:"owner_approval,omitempty"`
	Conversation  string    `json:"conversation,omitempty"` // GitHub refused to queue it over these unresolved review threads
	LastAction    string    `json:"last_action,omitempty"`  // the reconciler's last action at this head and why, for factoryctl why
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Verdict struct {
	Event  string
	Body   string
	DiffID string
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
	Head             string
	State            string // open, closed or merged
	Review           string // current review state at this head
	MergeStateStatus string // GitHub's mergeability state: DIRTY, BLOCKED, CLEAN or UNKNOWN
	Mergeable        *bool  // nil while GitHub computes it
	Queued           bool
	Failing          []string  // required checks at the head that finished unsuccessfully
	Pending          bool      // a required check at the head has not finished
	Checks           []string  // stable name=conclusion values for every observed required check
	Tests            []string  // failure lines of the head's failed checks: the failing steps and tests CI names
	Group            *GroupRun // the newest completed merge-group run that built the head
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

// ErrOwnerApproval and ErrPermanent mark an enqueue refusal no retry passes:
// it waits, escalated at once, for the pull request to change.
var ErrOwnerApproval = errors.New("the merge queue requires CODEOWNERS owner approval")

// ErrConversation marks an enqueue refused while a review thread is
// unresolved: no retry passes it, so the head goes back to its task once.
var ErrConversation = errors.New("a review conversation must be resolved")

// ErrPermanent marks a failure the same request meets again; others retry.
var ErrPermanent = errors.New("the same request fails the same way, so factoryd will not repeat it; a new head is reviewed afresh")

// FailuresBeforeEscalation is how many consecutive passes an action may fail
// before the overseer is told, and the pull request waits for a change: 30
// minutes at the 5-minute merge tick.
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
	if op, err := c.awaitChecks(ctx, op); err != nil || op.State != "running" {
		return op, err
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
	op.DiffID = verdict.DiffID
	if err := c.Store.Update(ctx, op); err != nil {
		return Operation{}, err
	}
	if err := c.Backend.Submit(ctx, op, verdict); err != nil {
		return c.reconcileSubmitting(ctx, op, err)
	}
	return c.finishSubmitted(ctx, op)
}

// CarryForward records a prior ALLOW for a new commit whose exact diff is
// unchanged. The merge queue still receives a fresh exact-head review record;
// the provider is not needlessly asked to rereview identical code.
func (c Coordinator) CarryForward(ctx context.Context, request Request, prior Operation) (Operation, error) {
	if prior.Verdict != "allow" || prior.DiffID == "" {
		return Operation{}, errors.New("review: prior operation is not carryable")
	}
	op, err := Prepare(request, c.Now)
	if err != nil {
		return Operation{}, err
	}
	op.DiffID, op.Verdict, op.Detail, op.State = prior.DiffID, "allow", prior.Detail, "submitting"
	if err := c.Store.Create(ctx, op); err != nil {
		return Operation{}, err
	}
	verdict := Verdict{Event: "ALLOW", Body: prior.Detail, DiffID: prior.DiffID}
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
	// The pull request's reconciler takes it from here (Decide).
	op.State, op.UpdatedAt = "enqueued", c.Now()
	if op.Verdict != "allow" {
		op.State = "completed"
	}
	return op, c.Store.Update(ctx, op)
}

// Action is the one step an open pull request's reconciler takes in a pass.
type Action int

const (
	Wait     Action = iota
	Rebase          // bring its task's Change up to current main
	SendBack        // the note goes to its task, bound to the head
	Review          // review the head: first, after a review that ended without a verdict, or once more for a disputed block
	Retry           // the failed review's one retry
	Enqueue
	Escalate // the overseer is told why, once
)

// Decide is a reconciler pass's single decision for an open pull request at
// the exact head pull shows. ops are the operations at that head, oldest
// first; answered says its task settled after the newest one sent it back,
// with nothing new to publish. A head goes back once per operation: a
// disputed block is reviewed once more, any other second send-back is
// escalated. An action that keeps failing waits, escalated, for the pull
// request to change.
func Decide(pull Pull, ops []Operation, answered bool) (Action, string) {
	op := ops[len(ops)-1]
	conflict := strings.EqualFold(pull.MergeStateStatus, "DIRTY") || pull.Mergeable != nil && !*pull.Mergeable
	action, note := Enqueue, ""
	switch {
	case op.Failures >= FailuresBeforeEscalation && !conflict && len(pull.Failing) == 0:
		return Wait, ""
	case !op.Rebased && (conflict || len(pull.Failing) > 0):
		action = Rebase
		if conflict {
			note = op.conflict()
		} else {
			note = pull.failing()
		}
	case conflict:
		action, note = SendBack, op.conflict()
	case len(pull.Failing) > 0:
		action, note = SendBack, pull.failing()
	case pull.Pending:
		action = Wait
	case op.State == "failed" && op.Retryable && op.RetryOf == "":
		action = Retry
	case op.State == "failed":
		action, note = Escalate, "its review failed: "+op.Detail
	case op.Verdict == "":
		action = Review
	case op.Verdict == "request_changes":
		action, note = SendBack, "This is the review of record for exact head "+op.Request.Head+". No other verdict on this head supersedes its findings: correct each one.\n\n"+op.Detail
		if op.Request.Answer != "" {
			note += "\n\nIt read your answer to the block before it and upheld that block:\n\n" + op.Request.Answer
		}
	case op.Conversation != "":
		action, note = SendBack, op.Conversation
	case pull.Queued:
		action = Wait
	case op.Enqueues >= 2:
		// Queued, removed, re-queued once and removed again.
		action, note = SendBack, pull.Group.note(op.Request.Head)
	}
	if action == SendBack && answered {
		blocks := 0
		for _, op := range ops {
			if op.Verdict == "request_changes" && op.Submitted {
				blocks++
			}
		}
		if op.Verdict == "request_changes" && blocks < 2 {
			return Review, ""
		}
		action, note = Escalate, "its task answered the send-back of operation "+op.ID+" without a new head:\n\n"+note
	}
	if action == Escalate && op.Handled {
		return Wait, ""
	}
	return action, note
}

// checksPoll paces the read of a published head's checks before review;
// checksGrace bounds the wait, so checks that never decide (none required, a
// stuck runner, an unreadable pull) cannot hold the review forever.
var checksPoll, checksGrace = time.Minute, 30 * time.Minute

// awaitChecks holds review until the head's required checks finish (#1582).
// A red or conflicting head goes back to its author unreviewed; a failed read
// decides nothing.
func (c Coordinator) awaitChecks(ctx context.Context, op Operation) (Operation, error) {
	for {
		pull, err := c.Backend.ObservePull(ctx, op)
		switch {
		case err != nil:
		case !strings.EqualFold(pull.Head, op.Request.Head):
			op.State = "superseded"
		case pull.State != "open":
			op.State = pull.State
		case pull.Mergeable != nil && !*pull.Mergeable:
			op.State, op.Detail = "ejected", op.conflict()
		case len(pull.Failing) > 0:
			op.State, op.Detail = "ejected", "Not reviewed: its checks failed. "+pull.failing()
		case !pull.Pending && len(pull.Checks) > 0:
			return op, nil
		}
		if op.State != "running" {
			op.UpdatedAt = c.Now()
			return op, c.Store.Update(ctx, op)
		}
		if c.Now().Sub(op.CreatedAt) >= checksGrace {
			return op, nil
		}
		select {
		case <-ctx.Done():
			return op, ctx.Err()
		case <-time.After(checksPoll):
		}
	}
}

func (op Operation) conflict() string {
	return fmt.Sprintf("Exact head %s conflicts with %s. Rebase this Change onto origin/%s and resolve the conflict.", op.Request.Head, op.Request.BaseRef, op.Request.BaseRef)
}

// failing names the failed required checks at the head and the first 40
// failure lines CI wrote (failing steps and tests with their file:line), as
// the premise a sandbox rerun cannot overturn.
func (p Pull) failing() string {
	note := fmt.Sprintf("Exact head %s cannot merge. Failing checks: %s. CI reproduced this failure on the exact head, so it is the premise: if your rerun passes, report what differs (environment, which tests you ran) and work from these lines; \"premise not reproduced\" is not a valid ending.", p.Head, strings.Join(p.Failing, ", "))
	for _, line := range p.Tests[:min(len(p.Tests), 40)] {
		note += "\n- " + line
	}
	return note
}

// Observation identifies what the pull request shows: a change resets the
// failures counted against its head.
func (p Pull) Observation() string {
	value, _ := json.Marshal(p)
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
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
