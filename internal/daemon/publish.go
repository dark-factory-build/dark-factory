package daemon

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
	"github.com/dark-factory-build/dark-factory/internal/maintainer"
	"github.com/dark-factory-build/dark-factory/internal/review"
)

// publishCall sends one Maintainer tool call and returns its structured result.
type publishCall func(ctx context.Context, name string, arguments map[string]any) (json.RawMessage, error)

// publishCheckout returns the Git directory of a disposable clone holding the
// Change's base, main and the given head (main, or the head of ref, the
// Change's branch), and its cleanup.
type publishCheckout func(ctx context.Context, ref, head string) (string, func(), error)

// errPublishLater is a failure of the connection or the checkout, not of the
// Change: the next pass retries it.
var errPublishLater, errRepositoryDisabled = errors.New("publication waits for the next pass"), errors.New("repository disabled for new work")

type publishIndeterminate struct {
	step        string
	observation string
}

func (e publishIndeterminate) Error() string {
	if e.observation == "" {
		return "publication operation " + e.step + " is indeterminate"
	}
	return "publication operation " + e.step + " is indeterminate: " + e.observation
}

type observedPull struct {
	Number     uint64 `json:"number"`
	URL        string `json:"url"`
	Head       string `json:"head_sha"`
	Base       string `json:"base_sha"`
	State      string `json:"state"`
	HeadRef    string `json:"head_ref"`
	Body       string `json:"body"`
	Mergeable  *bool  `json:"mergeable"`
	MergeState string `json:"merge_state_status"`
	MergeQueue string
}

type observedPublication struct {
	ref  string
	pull *observedPull
}

// publishSettledChanges publishes each succeeded intake worker's settled
// Change as one pull request through the project's Maintainer connection,
// records it against the worker task and launches factoryd's review; a
// corrected Change goes onto that pull request, whose reconciler reviews it,
// or, changing nothing, is an answer reviewed once more at the same head. A
// Change it cannot publish is escalated to the overseer once and retried at
// that revision only after kernel.PublishRetryAfter; an unreachable
// connection waits for the next pass. Given a task, it publishes only that
// task's Change, at once even inside its retry window: the task's pull
// request waits on it (#1800). It reports whether it tried one.
func (daemon *Daemon) publishSettledChanges(ctx context.Context, task kernel.TaskID) bool {
	at, err := daemon.timestamp()
	if err == nil && task != (kernel.TaskID{}) {
		at, err = kernel.NewUnixMillis(at.Int64() + kernel.PublishRetryAfter.Milliseconds())
	}
	var candidates []kernel.PublishableChange
	if err == nil {
		candidates, err = daemon.store.PublishableChanges(ctx, at)
	}
	if err != nil {
		LogFactoryd(daemon.log, "factoryd: publishable changes: %v\n", err)
	}
	tried := false
	for _, candidate := range candidates {
		if task != (kernel.TaskID{}) && candidate.Task.ID != task {
			continue
		}
		tried = true
		if err := daemon.publishSettledChange(ctx, candidate); err != nil {
			LogFactoryd(daemon.log, "factoryd: publish change %s at %s for task %s: %v\n", candidate.Change, candidate.Head, candidate.Task.ID, err)
		}
	}
	return tried
}

func (daemon *Daemon) publishSettledChange(ctx context.Context, c kernel.PublishableChange) error {
	repositoryID := c.Accepted.RepositoryID
	if repositoryID == (kernel.RepositoryID{}) {
		repositoryID = c.Repository
	}
	target, verified, err := daemon.store.RepositorySourceIdentity(ctx, repositoryID)
	if err != nil {
		return err
	}
	id, pinned, err := daemon.store.RepositoryGitHubID(ctx, repositoryID)
	if err != nil {
		return err
	}
	if !verified || !pinned || target.PublicationRepository == "" {
		return errors.New("its repository has no bound publication target")
	}
	repo := strings.ToLower(target.PublicationRepository)
	// Accepted work keeps its destination, but a disabled repository is
	// neither cloned nor reviewed: the overseer or operator decides.
	if repository, found, err := daemon.store.ProjectRepository(ctx, repositoryID); err != nil || !found || !repository.Enabled {
		return daemon.publishFailed(ctx, c, repo, errors.Join(err, errRepositoryDisabled))
	}
	backend := &daemonReviewBackend{daemon: daemon, project: c.Task.ProjectID, repository: repo, repositoryID: id}
	checkout := func(ctx context.Context, ref, head string) (string, func(), error) {
		path, cleanup, err := backend.clone(ctx, repo, ref, head, c.Base, "main")
		return filepath.Join(path, ".git"), cleanup, err
	}
	source, err := daemon.settledChangeGitDirectory(ctx, c)
	if err != nil {
		return daemon.publishFailed(ctx, c, repo, err)
	}
	call := backend.callResponse
	if daemon.reviewBackend != nil { // a test's Maintainer, when it publishes
		if fake, ok := daemon.reviewBackend(repo, id).(interface {
			call(context.Context, string, map[string]any) (json.RawMessage, error)
		}); ok {
			call = fake.call
		}
	}
	return daemon.publishChange(ctx, c, repo, source, call, checkout)
}

// settledChangeGitDirectory is the verified private Git directory holding the
// Change's commits, its worktree still at the settled head.
func (daemon *Daemon) settledChangeGitDirectory(ctx context.Context, c kernel.PublishableChange) (string, error) {
	parent, git := daemon.changeParent.Load(), daemon.gitExecutable.Load()
	if parent == nil || *parent == "" || git == nil || *git == "" {
		return "", errors.Join(errPublishLater, errors.New("no Change parent or Git executable"))
	}
	changeState, found, err := daemon.store.Change(ctx, c.Change)
	if err != nil || !found || changeState.Revision != c.Revision || changeState.Selection == nil {
		return "", errors.Join(errPublishLater, err, errors.New("the Change moved since it was selected"))
	}
	route, err := daemon.repositoryForChange(ctx, changeState)
	if err != nil {
		return "", errors.Join(errPublishLater, err)
	}
	repository, err := changeRepositoryIdentity(changeState.Selection.RepositoryIdentity())
	if err != nil {
		return "", err
	}
	facts, err := change.InspectWorktree(ctx, *git, route.Root, repository, filepath.Join(*parent, c.Change.String()))
	if err != nil {
		return "", fmt.Errorf("its worktree did not verify: %w", err)
	}
	if facts.Head().Hex() != c.Head || facts.Branch() != change.BranchName(c.Change.String()) {
		return "", errors.New("its worktree is not at its settled head " + c.Head)
	}
	return facts.GitDirectory(), nil
}

// publishChange publishes c from the commits in its Git directory source.
func (daemon *Daemon) publishChange(ctx context.Context, c kernel.PublishableChange, repo, source string, call publishCall, checkout publishCheckout) error {
	return daemon.publishFailed(ctx, c, repo, daemon.publishPull(ctx, c, repo, source, call, checkout))
}

// publishFailed records why c cannot be published: the reviewer record
// kernel.PublishFailureID, handled, whose escalation is due to the overseer
// the first time; a repeat refreshes it without one. A failure the next pass
// may not repeat is only returned. Both outcomes it settles are logged.
func (daemon *Daemon) publishFailed(ctx context.Context, c kernel.PublishableChange, repo string, err error) error {
	if err == nil {
		LogFactoryd(daemon.log, "factoryd: published change %s at %s for task %s\n", c.Change, c.Head, c.Task.ID)
		return nil
	}
	if ctx.Err() != nil || errors.Is(err, errPublishLater) || errors.Is(err, maintainer.ErrUnavailable) || errors.Is(err, maintainer.ErrDenied) {
		return err
	}
	why := err.Error()
	why = strings.ToValidUTF8(why[:min(len(why), 1000)], "")
	now, id := daemon.now(), kernel.PublishFailureID(c.Change, c.Revision)
	document, repeat, readErr := daemon.store.ReviewOperation(ctx, c.Task.ProjectID, id)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	previous := review.Operation{}
	if repeat {
		_ = json.Unmarshal(document, &previous)
	}
	failed := review.Operation{ID: id, State: "publish_failed", Retryable: publicationFailureRetryable(err), Handled: true, Detail: why, CreatedAt: now, UpdatedAt: now}
	var indeterminate publishIndeterminate
	if errors.As(err, &indeterminate) {
		failed.Retryable = false
		failed.Escalation = fmt.Sprintf("factoryd cannot publish change %s for task %s: %s", c.Change, c.Task.ID, why)
		if strings.Contains(previous.Escalation, why) {
			failed.Escalation = previous.Escalation
		}
	} else if !repeat {
		failed.Escalation = fmt.Sprintf("factoryd cannot publish change %s for task %s: %s", c.Change, c.Task.ID, why)
	}
	LogFactoryd(daemon.log, "factoryd: publish change %s at %s for task %s: %s\n", c.Change, c.Head, c.Task.ID, why)
	return durableReviewStore{store: daemon.store, project: c.Task.ProjectID, repository: repo, now: daemon.now}.Create(ctx, failed)
}

// publicationFailureRetryable: a Maintainer rejection that can pass outside
// factoryd, unlike a permanent one or a change-caused local failure.
func publicationFailureRetryable(err error) bool {
	var rejection maintainerRejection
	return errors.As(err, &rejection) && !errors.Is(err, review.ErrPermanent) || errors.Is(err, errRepositoryDisabled)
}

// publishPull is the overseer runbook's publication, made deterministic:
// every App write has the operation id uuid5(URL, "dark-factory:<change>:<step>")
// and is observed first, so a replay after a lost response reuses the
// completed write. Commits go under publish-<HEAD8 of the worker's head>-N
// and the pull request under pr-<HEAD8>, so a changed head never reuses a
// stale write. They go on the Change's branch head when the branch exists (on
// replay the first one's parent), carrying only what differs from it, or
// start a new branch; a correction (c.Pull set) then replaces the body under
// body-<HEAD8 of the new head>-<Change revision>.
func (daemon *Daemon) publishPull(ctx context.Context, c kernel.PublishableChange, repo, source string, call publishCall, checkout publishCheckout) error {
	operation := func(step string) string { return uuid5("dark-factory:" + c.Change.String() + ":" + step) }
	state := "" // completed's last observation
	completed := func(step string, result any) (bool, error) {
		response, err := call(ctx, "observe_operation", map[string]any{"repository": repo, "operation_id": operation(step)})
		var observed struct {
			State  string          `json:"state"`
			Result json.RawMessage `json:"result"`
		}
		if err == nil {
			err = json.Unmarshal(response, &observed)
		}
		state = observed.State
		if err != nil || observed.State != "completed" {
			if err == nil && observed.State == "indeterminate" {
				return false, publishIndeterminate{step: step}
			}
			return false, err
		}
		return true, json.Unmarshal(observed.Result, result)
	}
	branch := "factory/" + c.Change.String()[:12]
	if c.Repair && c.Branch != "" {
		branch = c.Branch
	}
	refHead := func(branch string) (string, error) {
		response, err := call(ctx, "observe_ref", map[string]any{"repository": repo, "branch": branch})
		var ref struct {
			Head string `json:"head_sha"`
		}
		if err == nil {
			err = json.Unmarshal(response, &ref)
		}
		return ref.Head, err // "" when the branch does not exist
	}
	observe := func(branch string, pull uint64) (observedPublication, error) {
		ref, err := refHead(branch)
		if err != nil {
			return observedPublication{}, err
		}
		observed := observedPublication{ref: ref}
		for pageNumber := 1; ; {
			args := map[string]any{"repository": repo, "page": pageNumber, "per_page": 100}
			if pull != 0 {
				args["pull_number"] = pull
			}
			response, err := call(ctx, "list_pull_requests", args)
			if err != nil {
				return observed, err
			}
			var page struct {
				PullRequests []observedPull `json:"pull_requests"`
				NextPage     *int           `json:"next_page"`
			}
			if err := json.Unmarshal(response, &page); err != nil {
				return observed, err
			}
			for i := range page.PullRequests {
				if pull != 0 && page.PullRequests[i].Number == pull || pull == 0 && page.PullRequests[i].HeadRef == branch {
					observed.pull = &page.PullRequests[i]
					break
				}
			}
			if observed.pull != nil || page.NextPage == nil {
				break
			}
			if *page.NextPage <= pageNumber || *page.NextPage > 1000 {
				return observed, errors.New("publication: invalid pull request page")
			}
			pageNumber = *page.NextPage
		}
		if observed.pull != nil && observed.pull.State == "open" {
			response, err := call(ctx, "observe_pull_request_merge", map[string]any{"repository": repo, "pull_number": observed.pull.Number, "head_sha": observed.pull.Head, "base": "main"})
			if err != nil {
				return observed, err
			}
			var merge struct {
				Head  string `json:"head_sha"`
				State string `json:"state"`
			}
			if err := json.Unmarshal(response, &merge); err != nil || merge.Head != observed.pull.Head {
				return observed, errors.New("publication: invalid pull merge observation")
			}
			if merge.State == "ACTIVE_QUEUE" {
				observed.pull.MergeQueue = "active"
			}
		}
		return observed, nil
	}
	observationDetail := func(observed observedPublication) string {
		if observed.pull == nil {
			return fmt.Sprintf("branch=%s, pull=absent", observed.ref)
		}
		mergeable := "unknown"
		if observed.pull.Mergeable != nil {
			mergeable = strconv.FormatBool(*observed.pull.Mergeable)
		}
		return fmt.Sprintf("branch=%s, pull=%d head=%s state=%s mergeable=%s merge_state=%s queue=%s", observed.ref, observed.pull.Number, observed.pull.Head, observed.pull.State, mergeable, observed.pull.MergeState, observed.pull.MergeQueue)
	}
	retryCompleted := func(step string, result any, observed observedPublication) (bool, error) {
		done, err := completed(step, result)
		var indeterminate publishIndeterminate
		if errors.As(err, &indeterminate) {
			indeterminate.observation = observationDetail(observed)
			err = indeterminate
		}
		return done, err
	}
	routeObserved := func(observed observedPublication) error {
		if observed.pull == nil {
			return nil
		}
		dirty := strings.EqualFold(observed.pull.MergeState, "DIRTY") || observed.pull.Mergeable != nil && !*observed.pull.Mergeable
		if !dirty && observed.pull.MergeQueue == "" && observed.pull.State == "open" {
			return nil
		}
		// A correction is already represented by the existing pull operation.
		// Route only that operation here: entering advanceReviewOperations would
		// reacquire reconcileMu when publication was reached from reconciliation.
		if c.Pull != 0 {
			pulls, err := daemon.store.OpenPulls(ctx)
			if err != nil {
				return errors.Join(err, errPublishLater)
			}
			found := false
			for _, open := range pulls {
				if open.Repository != repo || open.Pull.Number != c.Pull {
					continue
				}
				found = true
				var op review.Operation
				if len(open.Operations) != 0 {
					if json.Unmarshal(open.Operations[len(open.Operations)-1], &op) != nil {
						continue
					}
				} else {
					op = review.Operation{ID: uuid5(fmt.Sprintf("dark-factory:publish:%s#%d", c.Change, c.Pull)), Request: review.Request{Repository: repo, PullNumber: c.Pull, Head: observed.pull.Head, BaseRef: "main"}}
				}
				if op.ID != "" {
					task := c.Task
					if open.Task != (kernel.TaskID{}) {
						if stored, found, taskErr := daemon.store.Task(ctx, open.Task); taskErr == nil && found {
							task = stored
						}
					}
					return errors.Join(daemon.sendBackPull(ctx, open, task, op, observationDetail(observed)), errPublishLater)
				}
			}
			if found {
				return errors.New("publication observed a correction pull without a routable review operation: " + observationDetail(observed))
			}
			return errors.New("publication observed an unroutable correction pull: " + observationDetail(observed))
		}
		return errPublishLater
	}
	recover := func(step, expected string, pull uint64, expectPull bool) (observedPublication, string, bool, error) {
		observed, err := observe(branch, pull)
		if err != nil {
			return observed, "", false, err
		}
		// A changed branch ref is the durable receipt for a commit write. The
		// pull observation is the receipt for a create-pull write.
		if observed.ref != "" && observed.ref != expected {
			return observed, "", true, nil
		}
		if expectPull && observed.pull != nil && (pull != 0 || observed.pull.Head == observed.ref) {
			return observed, "", true, nil
		}
		if err := routeObserved(observed); err != nil {
			return observed, "", false, err
		}
		token := observed.ref
		if observed.pull != nil {
			token = observed.pull.Head
		}
		if token == "" {
			token = "missing"
		}
		retry := step + "-observed-" + token[:min(len(token), 12)]
		return observed, retry, false, nil
	}
	prefix := "publish-" + c.Head[:8] + "-"
	var first struct {
		Parent string `json:"parent_sha"`
	}
	main, err := refHead("main")
	if err == nil && main == "" {
		err = errors.New("main has no head")
	}
	done, ref := false, ""
	checked, tip := main, "" // the branch head before these commits, if any
	if err == nil {
		tip, err = refHead(branch)
	}
	if err != nil {
		return err
	}
	if tip != "" {
		checked, ref = tip, "refs/heads/"+branch
	}
	if err == nil {
		done, err = completed(prefix+"1", &first)
		if indeterminate, ok := err.(publishIndeterminate); ok {
			observed, retry, landed, observeErr := recover(indeterminate.step, main, c.Pull, false)
			if observeErr != nil {
				return observeErr
			}
			if landed {
				first.Parent, done, err = checked, true, nil
			} else {
				done, err = retryCompleted(retry, &first, observed)
			}
		}
	}
	if err != nil {
		return err
	}
	if done {
		tip = first.Parent
	}
	gitDir, cleanup, err := checkout(ctx, ref, checked)
	if err != nil {
		return errors.Join(errPublishLater, err)
	}
	defer cleanup()
	// The clone borrows only the registered repository's objects; the
	// worker's commits are in the Change's own Git directory.
	if _, err := gitOutput(ctx, gitDir, "-c", "protocol.file.allow=always", "fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--no-auto-maintenance", source, c.Head); err != nil {
		return fmt.Errorf("fetch its head from the Change: %w", err)
	}
	from, err := publicationFrom(ctx, gitDir, c.Base, c.Head, main)
	if err != nil {
		return err
	}
	head, diffFrom, mergeParent := from, from, ""
	if tip != "" {
		if c.Pull != 0 {
			diffFrom = tip // corrections preserve the current PR tree
		} else if diffFrom, err = publicationFrom(ctx, gitDir, tip, c.Head, main); err != nil {
			return err
		}
		if head = tip; diffFrom != tip && c.Pull == 0 {
			mergeParent = diffFrom
		}
	}
	changes, err := publicationEntries(ctx, gitDir, diffFrom, c.Head)
	if err != nil {
		return err
	}
	// A branch an earlier attempt already brought to this tree only lacks
	// its pull request; a correction changing nothing is an answer in its body.
	if len(changes) == 0 && c.Pull == 0 && diffFrom == from {
		return errors.New("nothing to publish: its head " + c.Head + " changes no file from " + diffFrom)
	}
	message := publicationMessage(c)
	for i := 0; i*50 < len(changes); i++ {
		step := prefix + strconv.Itoa(i+1)
		var commit struct {
			Parent string `json:"parent_sha"`
			SHA    string `json:"commit_sha"`
		}
		done, err := completed(step, &commit)
		if indeterminate, ok := err.(publishIndeterminate); ok {
			observed, retry, landed, observeErr := recover(indeterminate.step, head, c.Pull, false)
			if observeErr != nil {
				return observeErr
			}
			if landed {
				commit.SHA, commit.Parent, done, err = observed.ref, head, true, nil
			} else {
				step, done, err = retry, false, nil
				done, err = retryCompleted(step, &commit, observed)
			}
		}
		if err == nil && !done {
			var response json.RawMessage
			arguments := map[string]any{"repository": repo, "operation_id": operation(step), "branch": branch, "expected_head_sha": head, "message": message, "changes": changes[i*50 : min(len(changes), i*50+50)]}
			if i == 0 && mergeParent != "" {
				arguments["merge_parent_sha"] = mergeParent
			}
			response, err = call(ctx, "publish_commit", arguments)
			if err == nil {
				err = json.Unmarshal(response, &commit)
			}
		}
		if err == nil && commit.SHA == "" {
			err = errors.New(step + " returned no commit")
		}
		if err != nil {
			return err
		}
		head = commit.SHA
	}
	numstat, err := gitOutput(ctx, gitDir, "diff", "--no-renames", "--no-ext-diff", "--no-textconv", "--numstat", from, c.Head)
	if err != nil {
		return err
	}
	added, deleted, files := 0, 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(numstat)), "\n") {
		if fields := strings.Fields(line); len(fields) > 2 {
			plus, _ := strconv.Atoi(fields[0])
			minus, _ := strconv.Atoi(fields[1])
			added, deleted, files = added+plus, deleted+minus, files+1
		}
	}
	result := string(redactTerminalText([]byte(c.Task.Result)))
	result = strings.ToValidUTF8(result[:min(len(result), 24000)], "")
	body := fmt.Sprintf("%s\n\nPublished by factoryd from Change %s at %s: +%d -%d across %d files from %s.\n\nThe merge queue runs the gate.", result, c.Change, head, added, deleted, files, from)
	issue := c.Accepted.Snapshot.IssueNumber
	if c.Pull != 0 {
		// The corrected body keeps its close-or-reference line, and a pull
		// request that only references its issue keeps doing so; factoryd's
		// refresh reviews a new head. One settlement's body is written once.
		if !c.Repair {
			step := fmt.Sprintf("body-%s-%d", head[:8], c.Revision.Int64())
			done, err := completed(step, new(json.RawMessage))
			retryStep, varObserved := step, (*observedPull)(nil)
			var observedErr error
			if indeterminate, ok := err.(publishIndeterminate); ok {
				observed, retry, _, observeErr := recover(indeterminate.step, head, c.Pull, false)
				if observeErr != nil {
					return observeErr
				}
				retryStep, varObserved, done, err = retry, observed.pull, false, nil
				done, err = retryCompleted(retryStep, new(json.RawMessage), observed)
				if err != nil {
					var unresolved publishIndeterminate
					if errors.As(err, &unresolved) && varObserved != nil {
						observedErr, err = err, nil
					}
				}
			}
			var current struct {
				PullRequests []struct {
					Body string `json:"body"`
				} `json:"pull_requests"`
			}
			if err == nil && !done {
				var response json.RawMessage
				if response, err = call(ctx, "list_pull_requests", map[string]any{"repository": repo, "page": 1, "per_page": 1, "pull_number": c.Pull}); err == nil {
					err = json.Unmarshal(response, &current)
				}
			}
			if err == nil && !done {
				footer, text := "Closes", c.Task.Result
				for _, pull := range current.PullRequests {
					text += "\n" + pull.Body
				}
				if !closesIssue(text, issue, files) {
					footer, body = "Refs", referencing(body, issue)
				}
				wanted := fmt.Sprintf("%s\n\n%s #%d", body, footer, issue)
				if varObserved != nil && varObserved.Body == wanted {
					done = true
				} else if observedErr != nil {
					err = observedErr
				} else {
					_, err = call(ctx, "update_pull_request_body", map[string]any{"repository": repo, "operation_id": operation(retryStep), "pull_number": c.Pull, "body": wanted})
				}
			}
			if err != nil {
				return err
			}
		}
		// A correction changing nothing is the worker's answer: the
		// reconciler reviews the same head once more (review.Decide).
		at, err := daemon.timestamp()
		if err != nil {
			return err
		}
		return daemon.store.RecordCorrectionPublished(ctx, c, at)
	}
	var pull struct {
		Number uint64 `json:"number"`
		URL    string `json:"url"`
		Head   string `json:"head_sha"`
		Base   string `json:"base_sha"`
	}
	var observedPullState *observedPull
	step, base := "pr-"+c.Head[:8], ""
	done, err = completed(step, &pull)
	if indeterminate, ok := err.(publishIndeterminate); ok {
		observed, retry, landed, observeErr := recover(indeterminate.step, head, 0, true)
		if observeErr != nil {
			return observeErr
		}
		if landed && observed.pull != nil {
			observedPullState = observed.pull
			pull.Number, pull.URL, pull.Head, pull.Base = observed.pull.Number, observed.pull.URL, observed.pull.Head, observed.pull.Base
			done, err = true, nil
		} else {
			step, done, err = retry, false, nil
			done, err = retryCompleted(step, &pull, observed)
		}
	}
	if err == nil && !done {
		base, err = refHead("main")
	}
	// A create stopped before GitHub stays planned, bound to the main it
	// named, and conflicts once main moves: retry under this main's own id.
	if err == nil && !done && state == "planned" {
		step += "-" + base
		done, err = completed(step, &pull)
		if indeterminate, ok := err.(publishIndeterminate); ok {
			observed, retry, landed, observeErr := recover(indeterminate.step, head, 0, true)
			if observeErr != nil {
				return observeErr
			}
			if landed && observed.pull != nil {
				pull.Number, pull.URL, pull.Head, pull.Base = observed.pull.Number, observed.pull.URL, observed.pull.Head, observed.pull.Base
				done, err = true, nil
			} else {
				step, done, err = retry, false, nil
				done, err = retryCompleted(step, &pull, observed)
			}
		}
	}
	closes := closesIssue(c.Task.Result, issue, files)
	if !closes {
		body = referencing(body, issue)
	}
	if err == nil && !done {
		arguments := map[string]any{"repository": repo, "operation_id": operation(step), "head": branch, "head_sha": head, "base": "main", "base_sha": base, "title": message, "body": body, "draft": false,
			"issue_number": issue, "close_on_merge": closes} // the source issue is in repo itself
		var response json.RawMessage
		if response, err = call(ctx, "create_pull_request", arguments); err == nil {
			err = json.Unmarshal(response, &pull)
		}
	}
	if err == nil && pull.Number == 0 {
		err = errors.New("create_pull_request returned no pull request")
	}
	if err != nil {
		return err
	}
	state, mergeable, mergeState, mergeQueue := "open", (*bool)(nil), "", ""
	if observedPullState != nil {
		state, mergeable, mergeState, mergeQueue = observedPullState.State, observedPullState.Mergeable, observedPullState.MergeState, observedPullState.MergeQueue
	}
	return daemon.recordPublishedPull(ctx, c.Task.ProjectID, c.Task.ID, repo, kernel.ProductionPullRequest{Number: pull.Number, Title: message, URL: pull.URL, Head: pull.Head, HeadRepository: repo, Branch: branch, Base: "main", State: state, MergeState: mergeState, MergeQueue: mergeQueue, Mergeable: mergeable, Review: kernel.ProductionReview{Head: pull.Head, State: "unknown"}}, body, pull.Base)
}

// closesIssue is whether a publication completes its intake issue. One that
// changes no file, or whose text references the issue (Refs #N, the partial
// form) instead, leaves it open: merging never closes an unfinished issue.
func closesIssue(text string, issue uint64, files int) bool {
	return files > 0 && !regexp.MustCompile(fmt.Sprintf(`(?i)\brefs #%d\b`, issue)).MatchString(text)
}

// referencing turns each GitHub closing keyword aimed at the issue (#N,
// owner/repo#N or its URL) into Refs, so a reference-only body cannot close
// it on merge through the worker's own words.
func referencing(body string, issue uint64) string {
	return regexp.MustCompile(fmt.Sprintf(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?(\s+\S*(?:#|/issues/)%d)\b`, issue)).ReplaceAllString(body, "Refs${1}")
}

// publicationTitle is the accepted title on one line, within the App's
// 256-byte bound and cut on a UTF-8 boundary.
func publicationTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	return strings.ToValidUTF8(title[:min(len(title), 256)], "")
}

func publicationMessage(c kernel.PublishableChange) string {
	title := c.Accepted.Snapshot.Title
	if title == "" {
		title = c.Task.Title
	}
	return publicationTitle(title)
}

// publicationFrom is where a first publication's commits go: the Change's
// base, or the main commit the worker integrated when that is newer, so the
// published diff never replays main's own hunks against main.
func publicationFrom(ctx context.Context, gitDir, base, head, main string) (string, error) {
	out, err := gitOutput(ctx, gitDir, "merge-base", head, main)
	if err != nil {
		return "", err
	}
	integrated := strings.TrimSpace(string(out))
	_, err = gitOutput(ctx, gitDir, "merge-base", "--is-ancestor", integrated, base)
	if exit := new(exec.ExitError); errors.As(err, &exit) && exit.ExitCode() == 1 {
		return integrated, nil
	}
	return base, err
}

// publicationEntries is publish_commit's changes list from one commit to
// another: a deleted path carries only its path, any other its blob and mode.
// A symlink, a submodule or a file over the App's bound cannot be published.
func publicationEntries(ctx context.Context, gitDir, from, head string) ([]map[string]any, error) {
	listing, err := gitOutput(ctx, gitDir, "diff", "--no-renames", "--name-status", "-z", from, head)
	if err != nil {
		return nil, err
	}
	tree, err := gitOutput(ctx, gitDir, "ls-tree", "-r", "-z", head)
	if err != nil {
		return nil, err
	}
	modes := map[string]string{}
	for _, line := range strings.Split(string(tree), "\x00") {
		if meta, path, ok := strings.Cut(line, "\t"); ok {
			modes[path] = strings.Fields(meta)[0]
		}
	}
	fields := strings.Split(strings.TrimSuffix(string(listing), "\x00"), "\x00")
	var entries []map[string]any
	for i := 0; i+1 < len(fields); i += 2 {
		status, path := fields[i], fields[i+1]
		if status == "D" {
			entries = append(entries, map[string]any{"path": path})
			continue
		}
		mode := modes[path]
		if mode != "100644" && mode != "100755" {
			return nil, fmt.Errorf("%s has mode %s (a symlink or submodule), which the App cannot publish", path, mode)
		}
		content, err := gitOutput(ctx, gitDir, "cat-file", "blob", head+":"+path)
		if err != nil {
			return nil, err
		}
		encoded := base64.StdEncoding.EncodeToString(content)
		if len(encoded) > 1_000_000 {
			return nil, fmt.Errorf("%s is over the App's bound of 1,000,000 base64 characters", path)
		}
		entries = append(entries, map[string]any{"path": path, "mode": mode, "content_base64": encoded})
	}
	return entries, nil
}

func gitOutput(ctx context.Context, gitDir string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, change.TrustedGitExecutable, append([]string{"--git-dir", gitDir}, arguments...)...)
	command.Env = append(filteredReviewEnvironment(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	return command.Output()
}

// uuid5 is RFC 9562 version 5 in the URL namespace, the runbook's opid.
func uuid5(name string) string {
	hash := sha1.New()
	hash.Write([]byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8})
	hash.Write([]byte(name))
	b := hash.Sum(nil)[:16]
	b[6], b[8] = b[6]&0x0f|0x50, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
