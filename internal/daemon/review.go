package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/review"
)

func (daemon *Daemon) reviewPR(ctx context.Context, project kernel.ProjectID, request api.ReviewRequest) (string, error) {
	if daemon.reviewOperation != nil {
		return daemon.reviewOperation(ctx, project, request)
	}
	var failed review.Operation
	if request.RetryOperation != "" {
		document, found, err := daemon.store.ReviewOperation(ctx, project, request.RetryOperation)
		if err != nil || !found {
			return "", errors.New("review: retry operation not found")
		}
		if err := json.Unmarshal(document, &failed); err != nil || failed.State != "failed" || !failed.Retryable {
			return "", errors.New("review: operation is not failed")
		}
		request = api.ReviewRequest{Repository: failed.Request.Repository, PullNumber: failed.Request.PullNumber, Head: failed.Request.Head, Base: failed.Request.Base, Body: failed.Request.Body, Provider: failed.Request.Provider}
	}
	return daemon.startReview(ctx, project, request, failed)
}

func (daemon *Daemon) startReview(ctx context.Context, project kernel.ProjectID, request api.ReviewRequest, failed review.Operation) (string, error) {
	targets, _, unbound, err := daemon.projectMaintainerRepositories(ctx, project)
	if err != nil {
		return "", err
	}
	repository := strings.ToLower(request.Repository)
	repositoryID := targets[repository]
	if repositoryID == 0 {
		if unbound[repository] {
			return "", kernel.ErrConflict
		}
		return "", kernel.ErrUnauthorized
	}
	if daemon.github == nil && daemon.reviewBackend == nil {
		return "", errors.New("review: Maintainer unavailable")
	}
	var backend review.Backend = &daemonReviewBackend{daemon: daemon, repository: repository, repositoryID: repositoryID}
	if daemon.reviewBackend != nil {
		backend = daemon.reviewBackend(repository, repositoryID)
	}
	coordinator := review.Coordinator{Store: durableReviewStore{store: daemon.store, project: project, repository: repository, now: daemon.now}, Backend: backend, Now: daemon.now}
	var op review.Operation
	if failed.ID != "" {
		op, err = coordinator.Retry(ctx, failed)
	} else {
		op, err = coordinator.Start(ctx, review.Request{Repository: repository, PullNumber: request.PullNumber, Head: request.Head, Base: request.Base, Body: request.Body, Provider: request.Provider})
	}
	if err != nil {
		return op.ID, err
	}
	return op.ID, nil
}

// reviewPublishedPR observes the PR after publication, rather than trusting
// the branch/base names supplied to create_pull_request. This makes corrected
// publications naturally create a new exact-head operation as well.
func (daemon *Daemon) reviewPublishedPR(ctx context.Context, project kernel.ProjectID, repository string, pull uint64, publishedHead string) (string, error) {
	targets, _, _, err := daemon.projectMaintainerRepositories(ctx, project)
	if err != nil {
		return "", err
	}
	repository = strings.ToLower(repository)
	repositoryID := targets[repository]
	if repositoryID == 0 || daemon.github == nil {
		return "", errors.New("review: published repository unavailable")
	}
	response, err := (&daemonReviewBackend{daemon: daemon, repository: repository, repositoryID: repositoryID}).callResponse(ctx, "list_pull_requests", map[string]any{"repository": repository, "page": 1, "pull_number": pull})
	if err != nil {
		return "", err
	}
	var value struct {
		PullRequests []struct {
			Number  uint64 `json:"number"`
			HeadSHA string `json:"head_sha"`
			BaseSHA string `json:"base_sha"`
			Body    string `json:"body"`
		} `json:"pull_requests"`
	}
	if err := json.Unmarshal(response, &value); err != nil || len(value.PullRequests) != 1 {
		return "", errors.New("review: published pull not found")
	}
	pullValue := value.PullRequests[0]
	if pullValue.Number != pull || !strings.EqualFold(pullValue.HeadSHA, publishedHead) {
		return "", errors.New("review: publication head changed before review")
	}
	reviewRequest := api.ReviewRequest{Repository: repository, PullNumber: pull, Head: strings.ToLower(pullValue.HeadSHA), Base: strings.ToLower(pullValue.BaseSHA), Body: pullValue.Body, Provider: "codex"}
	if daemon.reviewPublished != nil {
		return daemon.reviewPublished(ctx, project, reviewRequest)
	}
	return daemon.reviewPR(ctx, project, reviewRequest)
}

type durableReviewStore struct {
	store      *kernel.Store
	project    kernel.ProjectID
	repository string
	now        func() time.Time
}

func (s durableReviewStore) write(ctx context.Context, op review.Operation) error {
	at, err := kernel.NewUnixMillis(s.now().UnixMilli())
	if err != nil {
		return err
	}
	return s.store.RecordReviewOperation(ctx, s.project, s.repository, op.ID, op, at)
}
func (s durableReviewStore) Create(ctx context.Context, op review.Operation) error {
	return s.write(ctx, op)
}
func (s durableReviewStore) Update(ctx context.Context, op review.Operation) error {
	return s.write(ctx, op)
}

type daemonReviewBackend struct {
	daemon       *Daemon
	repository   string
	repositoryID uint64
}

func (b *daemonReviewBackend) CloneReadOnly(ctx context.Context, request review.Request) (string, func(), error) {
	root, err := os.MkdirTemp("", "dark-factory-review-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	repo := filepath.Join(root, "repo")
	if output, err := exec.CommandContext(ctx, "/usr/bin/git", "clone", "--filter=blob:none", "--no-checkout", "https://github.com/"+request.Repository, repo).CombinedOutput(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("review clone: %s", strings.TrimSpace(string(output)))
	}
	for _, args := range [][]string{{"-C", repo, "fetch", "origin", "refs/pull/" + fmt.Sprint(request.PullNumber) + "/head"}, {"-C", repo, "checkout", "--detach", request.Head}} {
		if output, err := exec.CommandContext(ctx, "/usr/bin/git", args...).CombinedOutput(); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("review checkout: %s", strings.TrimSpace(string(output)))
		}
	}
	return repo, cleanup, nil
}

func (b *daemonReviewBackend) Review(ctx context.Context, checkout string, request review.Request) (review.Verdict, error) {
	prompt := "You are an independent adversarial reviewer. Read the exact-head checkout at " + checkout + ", pull request body: " + request.Body + ". Review only this change and finish with exactly VERDICT: ALLOW or VERDICT: REQUEST_CHANGES."
	var command *exec.Cmd
	if request.Provider == "claude" {
		command = exec.CommandContext(ctx, "claude", "-p", prompt, "--permission-mode", "plan")
	} else {
		command = exec.CommandContext(ctx, "codex", "exec", "--sandbox", "read-only", "--skip-git-repo-check", prompt)
	}
	command.Dir = checkout
	command.Env = filteredReviewEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		return review.Verdict{}, err
	}
	text := string(output)
	if strings.Contains(text, "VERDICT: ALLOW") {
		return review.Verdict{Event: "ALLOW", Body: text}, nil
	}
	if strings.Contains(text, "VERDICT: REQUEST_CHANGES") {
		return review.Verdict{Event: "REQUEST_CHANGES", Body: text}, nil
	}
	return review.Verdict{}, errors.New("review: provider returned no verdict")
}

func filteredReviewEnvironment() []string {
	result := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		name := strings.SplitN(value, "=", 2)[0]
		if strings.Contains(strings.ToUpper(name), "TOKEN") || strings.Contains(strings.ToUpper(name), "CREDENTIAL") || name == "GH_TOKEN" || name == "GITHUB_TOKEN" {
			continue
		}
		result = append(result, value)
	}
	return result
}

func (b *daemonReviewBackend) Submit(ctx context.Context, operation review.Operation, verdict review.Verdict) error {
	return b.call(ctx, "submit_pull_request_review", map[string]any{"repository": b.repository, "pull_number": operation.Request.PullNumber, "head_sha": operation.Request.Head, "operation_id": operation.ID, "event": verdict.Event, "body": verdict.Body})
}
func (b *daemonReviewBackend) Enqueue(ctx context.Context, operation review.Operation) error {
	digest := sha256.Sum256([]byte(operation.Request.Body))
	return b.call(ctx, "enqueue_pull_request", map[string]any{"repository": b.repository, "pull_number": operation.Request.PullNumber, "head_sha": operation.Request.Head, "base": operation.Request.Base, "operation_id": operation.EnqueueID, "reviewed_body_digest": "sha256:" + hex.EncodeToString(digest[:])})
}
func (b *daemonReviewBackend) call(ctx context.Context, name string, arguments map[string]any) error {
	_, err := b.callResponse(ctx, name, arguments)
	return err
}

func (b *daemonReviewBackend) callResponse(ctx context.Context, name string, arguments map[string]any) (json.RawMessage, error) {
	params := map[string]any{"name": name, "arguments": arguments}
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": params}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	response, err := b.daemon.github.MCP(ctx, encoded, map[string]uint64{b.repository: b.repositoryID})
	if err != nil {
		return nil, err
	}
	var value struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &value); err != nil || value.Result.IsError {
		return nil, errors.New("review: Maintainer rejected operation")
	}
	var envelope struct {
		Result struct {
			StructuredContent json.RawMessage `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil {
		return nil, err
	}
	return envelope.Result.StructuredContent, nil
}
