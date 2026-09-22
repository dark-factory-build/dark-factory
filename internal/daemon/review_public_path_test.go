//go:build darwin || linux

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/review"
)

type publicReviewBackend struct {
	killed, submitAmbiguous    bool
	reviews, submits, enqueues int
}

func (b *publicReviewBackend) CloneReadOnly(context.Context, review.Request) (string, func(), error) {
	return "/fixture-review", func() {}, nil
}
func (b *publicReviewBackend) Review(context.Context, string, review.Request) (review.Verdict, error) {
	b.reviews++
	if b.killed {
		return review.Verdict{}, errors.New("provider killed at launch")
	}
	return review.Verdict{Event: "ALLOW", Body: "fixture allow"}, nil
}
func (b *publicReviewBackend) Submit(context.Context, review.Operation, review.Verdict) error {
	b.submits++
	if b.submitAmbiguous {
		return errors.New("submit timeout after Maintainer write")
	}
	return nil
}
func (b *publicReviewBackend) Enqueue(context.Context, review.Operation) error {
	b.enqueues++
	return nil
}

func TestPublicReviewPathPersistsKilledProviderFailureAndRetries(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	backend := &publicReviewBackend{killed: true}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	input := api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{Repository: "team/repo", PullNumber: 7, Head: strings.Repeat("a", 40), Base: strings.Repeat("b", 40), Body: "fixture", Provider: "codex"}}
	if result := fixture.daemon.Intake(context.Background(), input); result.State == "ok" {
		t.Fatal("killed provider unexpectedly succeeded")
	}
	op := lastDurableReview(t, fixture.store, project)
	if op.State != "failed" || !op.Retryable || backend.reviews != 1 {
		t.Fatalf("failed operation=%+v backend=%+v", op, backend)
	}
	backend.killed = false
	retry := api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{RetryOperation: op.ID}}
	result := fixture.daemon.Intake(context.Background(), retry)
	if result.State != "ok" || backend.reviews != 2 || backend.submits != 1 || backend.enqueues != 1 {
		t.Fatalf("retry result=%+v operation=%+v backend=%+v", result, lastDurableReview(t, fixture.store, project), backend)
	}
}

func TestPublicReviewPathRefusesRetryAfterAmbiguousSubmit(t *testing.T) {
	fixture, project := reviewPublicFixture(t)
	backend := &publicReviewBackend{submitAmbiguous: true}
	fixture.daemon.reviewBackend = func(string, uint64) review.Backend { return backend }
	input := api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{Repository: "team/repo", PullNumber: 8, Head: strings.Repeat("c", 40), Base: strings.Repeat("d", 40), Body: "fixture", Provider: "claude"}}
	if result := fixture.daemon.Intake(context.Background(), input); result.State == "ok" {
		t.Fatal("ambiguous submit unexpectedly succeeded")
	}
	op := lastDurableReview(t, fixture.store, project)
	if op.State != "failed" || op.Retryable {
		t.Fatalf("ambiguous operation=%+v", op)
	}
	before := backend.reviews
	result := fixture.daemon.Intake(context.Background(), api.IntakeInput{Action: "review_pr", ProjectID: project.String(), ReviewRequest: &api.ReviewRequest{RetryOperation: op.ID}})
	if result.State == "ok" || backend.reviews != before || backend.enqueues != 0 {
		t.Fatalf("ambiguous retry was reissued: result=%+v backend=%+v", result, backend)
	}
}

func reviewPublicFixture(t *testing.T) (*dispatchFixture, kernel.ProjectID) {
	t.Helper()
	fixture := newDispatchFixture(t)
	project := mustProjectID(t, testID(250))
	ctx := context.Background()
	if _, err := fixture.store.CreateProject(ctx, kernel.NewProject{ID: project, Name: "review-public", Root: "/review-public"}, mustKernelTime(t, 2)); err != nil {
		t.Fatal(err)
	}
	repository, err := kernel.RepositoryIDFromBytes(mustIDBytes(t, testID(251)))
	if err != nil {
		t.Fatal(err)
	}
	identity := kernel.RepositorySourceIdentity{RootDevice: 1, RootInode: 2, GitDevice: 1, GitInode: 3, OriginDigest: [32]byte{1}, PublicationRepository: "team/repo"}
	if _, err := fixture.store.AddProjectRepository(ctx, kernel.NewProjectRepository{ID: repository, ProjectID: project, Name: "publication", Root: "/review-public-repository", BaseRef: "main", SourceIdentity: &identity}, mustKernelTime(t, 3)); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.BindRepositoryGitHubID(ctx, repository, 42); err != nil {
		t.Fatal(err)
	}
	return fixture, project
}

func lastDurableReview(t *testing.T, store *kernel.Store, project kernel.ProjectID) review.Operation {
	t.Helper()
	page, err := store.Production(context.Background(), project, 0, 8)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range page.Records {
		if record.Kind == "reviewer" {
			var op review.Operation
			if err := json.Unmarshal(record.Document, &op); err != nil {
				t.Fatal(err)
			}
			return op
		}
	}
	t.Fatal("no durable review operation")
	return review.Operation{}
}
