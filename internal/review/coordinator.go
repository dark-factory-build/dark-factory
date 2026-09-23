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
	ID        string    `json:"id"`
	EnqueueID string    `json:"enqueue_id,omitempty"`
	Request   Request   `json:"request"`
	State     string    `json:"state"`
	Retryable bool      `json:"retryable,omitempty"`
	Verdict   string    `json:"verdict,omitempty"`
	Detail    string    `json:"detail,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Verdict struct {
	Event string
	Body  string
}

type Store interface {
	Create(context.Context, Operation) error
	Update(context.Context, Operation) error
}

type Backend interface {
	CloneReadOnly(context.Context, Request) (string, func(), error)
	Review(context.Context, string, Request) (Verdict, error)
	Submit(context.Context, Operation, Verdict) error
	Enqueue(context.Context, Operation) error
}

type Coordinator struct {
	Store   Store
	Backend Backend
	Now     func() time.Time
}

func (c Coordinator) Start(ctx context.Context, request Request) (Operation, error) {
	if c.Store == nil || c.Backend == nil {
		return Operation{}, errors.New("review: incomplete coordinator")
	}
	op, err := Prepare(request, c.Now)
	if err != nil {
		return Operation{}, err
	}
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
	if err := validate(op.Request); err != nil || c.Store == nil || c.Backend == nil || c.Now == nil || op.ID == "" || op.State != "running" {
		if err == nil {
			err = errors.New("review: incomplete coordinator")
		}
		return Operation{}, err
	}
	checkout, cleanup, err := c.Backend.CloneReadOnly(ctx, op.Request)
	if err != nil {
		return c.fail(ctx, op, err, true)
	}
	defer cleanup()
	verdict, err := c.Backend.Review(ctx, checkout, op.Request)
	if err != nil {
		return c.fail(ctx, op, err, true)
	}
	if verdict.Event != "ALLOW" && verdict.Event != "REQUEST_CHANGES" {
		return c.fail(ctx, op, errors.New("review: provider returned no valid verdict"), true)
	}
	op.Verdict, op.Detail, op.UpdatedAt = strings.ToLower(verdict.Event), verdict.Body, c.Now()
	if err := c.Store.Update(ctx, op); err != nil {
		return Operation{}, err
	}
	if err := c.Backend.Submit(ctx, op, verdict); err != nil {
		return c.fail(ctx, op, err, false)
	}
	if verdict.Event == "ALLOW" {
		op.EnqueueID, err = operationID()
		if err != nil {
			return c.fail(ctx, op, err, false)
		}
		op.UpdatedAt = c.Now()
		if err := c.Store.Update(ctx, op); err != nil {
			return Operation{}, err
		}
		if err := c.Backend.Enqueue(ctx, op); err != nil {
			return c.fail(ctx, op, err, false)
		}
		op.State = "enqueued"
	} else {
		op.State = "completed"
	}
	op.UpdatedAt = c.Now()
	if err := c.Store.Update(ctx, op); err != nil {
		return Operation{}, err
	}
	return op, nil
}

// Retry starts a new durable attempt for a failed operation. The original
// failure remains immutable history; the request is reused so the retry cannot
// silently move to a different pull-request head.
func (c Coordinator) Retry(ctx context.Context, failed Operation) (Operation, error) {
	if failed.State != "failed" || !failed.Retryable {
		return Operation{}, errors.New("review: only pre-submit launch failures are retryable")
	}
	return c.Start(ctx, failed.Request)
}

func (c Coordinator) fail(ctx context.Context, op Operation, cause error, retryable bool) (Operation, error) {
	op.State, op.Detail, op.Retryable, op.UpdatedAt = "failed", cause.Error(), retryable, c.Now()
	if err := c.Store.Update(ctx, op); err != nil {
		return Operation{}, errors.Join(cause, err)
	}
	return op, cause
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
