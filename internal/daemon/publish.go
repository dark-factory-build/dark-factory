package daemon

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
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
// Change's base and main at the given head, and its cleanup.
type publishCheckout func(ctx context.Context, main string) (string, func(), error)

// errPublishLater is a failure of the connection or the checkout, not of the
// Change: the next pass retries it.
var errPublishLater, errRepositoryDisabled = errors.New("publication waits for the next pass"), errors.New("repository disabled for new work")

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
	checkout := func(ctx context.Context, main string) (string, func(), error) {
		path, cleanup, err := backend.clone(ctx, repo, "", main, c.Base, "main")
		return filepath.Join(path, ".git"), cleanup, err
	}
	if daemon.settledPublishCheckout != nil {
		checkout = func(ctx context.Context, main string) (string, func(), error) {
			return daemon.settledPublishCheckout(ctx, repo, main, c.Base, "main")
		}
	}
	source, err := daemon.settledChangeGitDirectory(ctx, c)
	if daemon.settledPublishSource != nil {
		source, err = daemon.settledPublishSource(ctx, c)
	}
	if err != nil {
		return daemon.publishFailed(ctx, c, repo, err)
	}
	call := backend.callResponse
	if c.Accepted.Snapshot.LinearTeamID != "" {
		call = backend.callResponsePrivate
	}
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
	_, repeat, readErr := daemon.store.ReviewOperation(ctx, c.Task.ProjectID, id)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	failed := review.Operation{ID: id, State: "publish_failed", Retryable: publicationFailureRetryable(err), Handled: true, Detail: why, CreatedAt: now, UpdatedAt: now}
	if !repeat {
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
// completed write. The worker's exact head goes to the Change's branch under
// pack-<HEAD8> (publishPack), replacing whatever the branch holds only if it
// is still the head observed here, and the pull request under pr-<HEAD8>; a
// correction (c.Pull set) then replaces the body under body-<HEAD8>-<Change
// revision>. A branch already at the head is not pushed again.
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
			return false, err
		}
		return true, json.Unmarshal(observed.Result, result)
	}
	branch := "factory/" + c.Change.String()[:12]
	if c.Branch != "" {
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
	main, err := refHead("main")
	if err == nil && main == "" {
		err = errors.New("main has no head")
	}
	tip := "" // the branch head the push replaces, if any
	if err == nil {
		tip, err = refHead(branch)
	}
	if err != nil {
		return err
	}
	gitDir, cleanup, err := checkout(ctx, main)
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
	if files == 0 && c.Pull == 0 {
		return errors.New("nothing to publish: its head " + c.Head + " changes no file from " + from)
	}
	if tip != c.Head {
		if err := publishPack(ctx, call, gitDir, repo, operation("pack-"+c.Head[:8]), branch, tip, from, c.Head); err != nil {
			return err
		}
	}
	issue := c.Accepted.Snapshot.IssueNumber
	linearSource := c.Accepted.Snapshot.LinearTeamID != ""
	if linearSource {
		issue = 0
	}
	sourceRepository := c.Accepted.SourceRepository
	crossSource := issue != 0 && sourceRepository != "" && !strings.EqualFold(sourceRepository, repo)
	issueTitle := c.Accepted.Snapshot.Title
	issueBody := c.Accepted.Snapshot.Body
	if sourceRepository != "" && !linearSource {
		response, observeErr := call(ctx, "observe_issue", map[string]any{"repository": sourceRepository, "issue_number": issue})
		if observeErr != nil {
			return fmt.Errorf("observe accepted issue #%d: %w", issue, observeErr)
		}
		var observed struct {
			Number uint64 `json:"number"`
			Title  string `json:"title"`
			Body   string `json:"body"`
		}
		if err := json.Unmarshal(response, &observed); err != nil {
			return fmt.Errorf("decode accepted issue #%d: %w", issue, err)
		}
		if observed.Number != issue {
			return fmt.Errorf("observe accepted issue #%d: response was issue #%d", issue, observed.Number)
		}
		issueTitle, issueBody = observed.Title, observed.Body
	}
	if strings.TrimSpace(issueBody) == "" {
		issueBody = issueTitle
		if strings.TrimSpace(issueBody) == "" {
			issueBody = c.Task.Result
		}
	}
	if c.Pull != 0 && strings.TrimSpace(c.Task.Result) != "" && issueBody != c.Task.Result {
		issueBody += "\n\nCorrection result:\n" + c.Task.Result
	}
	issueBody = string(redactTerminalText([]byte(issueBody)))
	issueBody = strings.ToValidUTF8(issueBody[:min(len(issueBody), 24000)], "")
	body := fmt.Sprintf("%s\n\nPublished by factoryd from Change %s at %s: +%d -%d across %d files from %s.\n\nThe merge queue runs the gate.", issueBody, c.Change, c.Head, added, deleted, files, from)
	if crossSource {
		body = qualifySourceReferences(body, sourceRepository)
	} else if linearSource {
		body = qualifyExternalReferences(body, c.Accepted.Snapshot.URL)
	}
	if c.Pull != 0 {
		// The corrected body keeps the issue referenced while the steward waits
		// One settlement's body is written once.
		step := fmt.Sprintf("body-%s-%d", c.Head[:8], c.Revision.Int64())
		done, err := completed(step, new(json.RawMessage))
		if err == nil && !done {
			updatedBody := body
			switch {
			case crossSource:
				updatedBody = appendPublicationFooter(updatedBody, fmt.Sprintf("Refs %s#%d", sourceRepository, issue))
			case linearSource:
				updatedBody = appendPublicationFooter(updatedBody, "Refs "+c.Accepted.Snapshot.URL)
			case issue != 0:
				updatedBody = fmt.Sprintf("%s\n\nRefs #%d", referenceIssue(updatedBody, issue), issue)
			}
			_, err = call(ctx, "update_pull_request_body", map[string]any{"repository": repo, "operation_id": operation(step), "pull_number": c.Pull, "body": updatedBody})
		}
		if err != nil {
			return err
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
	step, base := "pr-"+c.Head[:8], ""
	done, err := completed(step, &pull)
	if err == nil && !done {
		base, err = refHead("main")
	}
	// A create stopped before GitHub stays planned, bound to the main it
	// named, and conflicts once main moves: retry under this main's own id.
	if err == nil && !done && state == "planned" {
		step += "-" + base
		done, err = completed(step, &pull)
	}
	if issue != 0 && !crossSource && !linearSource {
		body = referenceIssue(body, issue)
	}
	if err == nil && !done {
		arguments := map[string]any{"repository": repo, "operation_id": operation(step), "head": branch, "head_sha": c.Head, "base": "main", "base_sha": base, "title": publicationMessage(c), "body": body, "draft": false,
			"issue_number": issue, "close_on_merge": false} // live acceptance closes the source issue
		if crossSource {
			arguments["source_repository"] = sourceRepository
		}
		if linearSource {
			arguments["external_source_url"] = c.Accepted.Snapshot.URL
		}
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
	return daemon.recordPublishedPull(ctx, c.Task.ProjectID, c.Task.ID, repo, kernel.ProductionPullRequest{Number: pull.Number, Title: publicationMessage(c), URL: pull.URL, Head: pull.Head, HeadRepository: repo, Branch: branch, Base: "main", State: "open", Review: kernel.ProductionReview{Head: pull.Head, State: "unknown"}}, body, pull.Base)
}

// referencing turns each GitHub closing keyword aimed at the issue (#N,
// owner/repo#N or its URL) into Refs, so a reference-only body cannot close
// it on merge through the worker's own words.
func referencing(body string, issue uint64) string {
	return regexp.MustCompile(fmt.Sprintf(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?(\s+\S*(?:#|/issues/)%d)\b`, issue)).ReplaceAllString(body, "Refs${1}")
}

func referenceIssue(body string, issue uint64) string {
	body = referencing(body, issue)
	if !regexp.MustCompile(fmt.Sprintf(`(?i)\brefs #%d\b`, issue)).MatchString(body) {
		body += fmt.Sprintf("\n\nRefs #%d", issue)
	}
	return body
}

var sourceClosingReference = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?):?(\s+\S*(?:#|/issues/)\d+)\b`)
var bareIssueReference = regexp.MustCompile(`(^|[^\w/])#(\d+)\b`)

func qualifySourceReferences(body, source string) string {
	body = sourceClosingReference.ReplaceAllString(body, "Refs${1}")
	return bareIssueReference.ReplaceAllString(body, "${1}"+source+"#$2")
}

func qualifyExternalReferences(body, sourceURL string) string {
	body = sourceClosingReference.ReplaceAllString(body, "Refs${1}")
	return bareIssueReference.ReplaceAllString(body, "${1}"+sourceURL)
}

func appendPublicationFooter(body, footer string) string {
	body = strings.TrimSpace(body)
	if last := body[strings.LastIndex(body, "\n")+1:]; strings.EqualFold(strings.TrimSpace(last), footer) {
		return body
	}
	return body + "\n\n" + footer
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

// packChunkBytes is publish_pack's chunk, whose base64 and JSON-RPC envelope
// fit the 512 KiB attempt envelope (control-plane PACK_CHUNK_BYTES).
const packChunkBytes = 256 << 10

// publishPack moves branch from expected ("" creates it) to head through the
// Maintainer's publish_pack, which pushes the objects head has beyond known, a
// commit GitHub holds, byte for byte. Every chunk replays under operation, so
// after an ambiguous response the whole call is repeated and the Maintainer
// reconciles the push. A pack is opaque to the Maintainer, so the paths
// publish_commit refuses (valid_repository_path) are refused here.
func publishPack(ctx context.Context, call publishCall, gitDir, repo, operation, branch, expected, known, head string) error {
	changed, err := gitOutput(ctx, gitDir, "diff", "--no-renames", "--name-only", "-z", known, head, "--", ":(icase).github", "CODEOWNERS", "docs/CODEOWNERS")
	if err != nil {
		return err
	}
	for _, name := range strings.Split(string(changed), "\x00") {
		authority := strings.EqualFold(name, ".github") // replacing the tree itself
		for _, path := range []string{"CODEOWNERS", ".github/CODEOWNERS", "docs/CODEOWNERS", ".github/dependabot.yml", ".github/dependabot.yaml"} {
			authority = authority || name == path || strings.HasPrefix(name, path+"/")
		}
		if authority {
			return fmt.Errorf("%s changes review authority: %s", head, name)
		}
	}
	// The pack's digest is the operation's identity, so a retry must rebuild
	// the same bytes however Git has since packed these objects: nothing is
	// reused, and one thread at a fixed compression makes the build
	// deterministic for one Git version.
	command := exec.CommandContext(ctx, change.TrustedGitExecutable, "--git-dir", gitDir, "-c", "pack.compression=9", "pack-objects", "--stdout", "--revs", "--quiet", "--no-reuse-delta", "--no-reuse-object", "--threads=1")
	command.Env = append(filteredReviewEnvironment(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	command.Stdin = strings.NewReader(head + "\n^" + known + "\n")
	pack, err := command.Output()
	if err != nil {
		return fmt.Errorf("pack %s from %s: %w", head, known, err)
	}
	sum := sha256.Sum256(pack)
	var published struct {
		Commit *string `json:"commit_sha"`
	}
	for index := 0; index*packChunkBytes < len(pack); index++ {
		arguments := map[string]any{"repository": repo, "operation_id": operation, "branch": branch, "head_sha": head, "pack_sha256": hex.EncodeToString(sum[:]), "pack_bytes": len(pack),
			"chunk_index": index, "chunk_base64": base64.StdEncoding.EncodeToString(pack[index*packChunkBytes : min(len(pack), (index+1)*packChunkBytes)])}
		if expected != "" {
			arguments["expected_head_sha"] = expected
		}
		response, err := call(ctx, "publish_pack", arguments)
		if err == nil {
			err = json.Unmarshal(response, &published)
		}
		if err != nil {
			return err
		}
	}
	if published.Commit == nil || *published.Commit != head {
		return fmt.Errorf("publish_pack did not move %s to %s", branch, head)
	}
	return nil
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
