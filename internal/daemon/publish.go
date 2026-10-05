package daemon

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// Change's base and the given main head, and its cleanup.
type publishCheckout func(ctx context.Context, main string) (string, func(), error)

// errPublishLater is a failure of the connection or the checkout, not of the
// Change: the next pass retries it.
var errPublishLater = errors.New("publication waits for the next pass")

// publishSettledChanges publishes each succeeded intake worker's settled
// Change as one pull request through the project's Maintainer connection,
// records it against the worker task and launches factoryd's review. A
// Change it cannot publish is escalated to the overseer once and not retried
// at that revision; only an unreachable connection waits for the next pass.
func (daemon *Daemon) publishSettledChanges(ctx context.Context) {
	candidates, err := daemon.store.PublishableChanges(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "factoryd: publishable changes: %v\n", err)
	}
	for _, candidate := range candidates {
		if err := daemon.publishSettledChange(ctx, candidate); err != nil {
			fmt.Fprintf(os.Stderr, "factoryd: publish change %s: %v\n", candidate.Change, err)
		}
	}
}

func (daemon *Daemon) publishSettledChange(ctx context.Context, c kernel.PublishableChange) error {
	target, verified, err := daemon.store.RepositorySourceIdentity(ctx, c.Accepted.RepositoryID)
	if err != nil {
		return err
	}
	id, pinned, err := daemon.store.RepositoryGitHubID(ctx, c.Accepted.RepositoryID)
	if err != nil {
		return err
	}
	if !verified || !pinned || target.PublicationRepository == "" {
		return errors.New("its repository has no bound publication target")
	}
	repo := strings.ToLower(target.PublicationRepository)
	backend := &daemonReviewBackend{daemon: daemon, project: c.Task.ProjectID, repository: repo, repositoryID: id}
	method := "tools/call"
	if c.Accepted.Snapshot.LinearTeamID != "" {
		method = "factory/tools/call_private" // as attemptMaintainer sends a Linear source's calls
	}
	call := func(ctx context.Context, name string, arguments map[string]any) (json.RawMessage, error) {
		repositories := map[string]uint64{repo: id}
		if source, ok := arguments["source_repository"].(string); ok {
			repositories[strings.ToLower(source)] = c.Accepted.Snapshot.GitHubRepositoryID
		}
		return backend.callAs(ctx, method, repositories, name, arguments)
	}
	checkout := daemon.publicationCheckout(c.Accepted.RepositoryID, c.Base)
	source, err := daemon.settledChangeGitDirectory(ctx, c)
	if err != nil {
		return daemon.publishFailed(ctx, c, repo, err)
	}
	return daemon.publishChange(ctx, c, repo, source, call, checkout)
}

// publicationCheckout clones the acceptance's repository even after it is
// disabled for new work: accepted work keeps its destination.
func (daemon *Daemon) publicationCheckout(id kernel.RepositoryID, base string) publishCheckout {
	return func(ctx context.Context, main string) (string, func(), error) {
		repository, found, err := daemon.store.ProjectRepository(ctx, id)
		source, verified, sourceErr := daemon.store.RepositorySourceIdentity(ctx, id)
		if err = errors.Join(err, sourceErr); err == nil && (!found || !verified) {
			err = errors.New("its repository has no verified checkout")
		}
		if err != nil {
			return "", nil, err
		}
		path, cleanup, err := cloneRepository(ctx, repository, source, review.Request{Head: main, Base: base, BaseRef: "main"})
		return filepath.Join(path, ".git"), cleanup, err
	}
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
// kernel.PublishFailureID, handled, whose escalation is due to the overseer.
// A failure the next pass may not repeat is only returned.
func (daemon *Daemon) publishFailed(ctx context.Context, c kernel.PublishableChange, repo string, err error) error {
	if err == nil || ctx.Err() != nil || errors.Is(err, errPublishLater) || errors.Is(err, maintainer.ErrUnavailable) || errors.Is(err, maintainer.ErrDenied) {
		return err
	}
	why := err.Error()
	why = strings.ToValidUTF8(why[:min(len(why), 1000)], "")
	now := daemon.now()
	failed := review.Operation{ID: kernel.PublishFailureID(c.Change, c.Revision), State: "publish_failed", Handled: true, Detail: why,
		Escalation: fmt.Sprintf("factoryd cannot publish change %s for task %s: %s", c.Change, c.Task.ID, why), CreatedAt: now, UpdatedAt: now}
	return durableReviewStore{store: daemon.store, project: c.Task.ProjectID, repository: repo, now: daemon.now}.Create(ctx, failed)
}

// publishPull is the overseer runbook's first publication, made
// deterministic: every App write has the operation id
// uuid5(URL, "dark-factory:<change>:<step>") and is observed first, so a
// replay after a lost response reuses the completed write.
func (daemon *Daemon) publishPull(ctx context.Context, c kernel.PublishableChange, repo, source string, call publishCall, checkout publishCheckout) error {
	operation := func(step string) string { return uuid5("dark-factory:" + c.Change.String() + ":" + step) }
	completed := func(step string, result any) (bool, error) {
		response, err := call(ctx, "observe_operation", map[string]any{"repository": repo, "operation_id": operation(step)})
		var observed struct {
			State  string          `json:"state"`
			Result json.RawMessage `json:"result"`
		}
		if err == nil {
			err = json.Unmarshal(response, &observed)
		}
		if err != nil || observed.State != "completed" {
			return false, err
		}
		return true, json.Unmarshal(observed.Result, result)
	}
	mainHead := func() (string, error) {
		response, err := call(ctx, "observe_ref", map[string]any{"repository": repo, "branch": "main"})
		var ref struct {
			Head string `json:"head_sha"`
		}
		if err == nil {
			err = json.Unmarshal(response, &ref)
		}
		if err == nil && ref.Head == "" {
			err = errors.New("main has no head")
		}
		return ref.Head, err
	}
	main, err := mainHead()
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
	changes, err := publicationEntries(ctx, gitDir, from, c.Head)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return errors.New("nothing to publish: its head " + c.Head + " changes no file from " + from)
	}
	message := publicationTitle(c.Accepted.Snapshot.Title)
	branch := "factory/" + c.Change.String()[:12]
	head := from
	for i := 0; i*50 < len(changes); i++ {
		step := "publish-" + strconv.Itoa(i+1)
		var commit struct {
			SHA string `json:"commit_sha"`
		}
		done, err := completed(step, &commit)
		if err == nil && !done {
			var response json.RawMessage
			response, err = call(ctx, "publish_commit", map[string]any{"repository": repo, "operation_id": operation(step), "branch": branch, "expected_head_sha": head, "message": message, "changes": changes[i*50 : min(len(changes), i*50+50)]})
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
	added, deleted := 0, 0
	for _, line := range strings.Split(strings.TrimSpace(string(numstat)), "\n") {
		if fields := strings.Fields(line); len(fields) > 2 {
			plus, _ := strconv.Atoi(fields[0])
			minus, _ := strconv.Atoi(fields[1])
			added, deleted = added+plus, deleted+minus
		}
	}
	result := string(redactTerminalText([]byte(c.Task.Result)))
	result = strings.ToValidUTF8(result[:min(len(result), 24000)], "")
	body := fmt.Sprintf("%s\n\nPublished by factoryd from Change %s at %s: +%d -%d across %d files from %s.\n\nThe merge queue runs the gate.", result, c.Change, head, added, deleted, len(changes), from)
	var pull struct {
		Number uint64 `json:"number"`
		URL    string `json:"url"`
		Head   string `json:"head_sha"`
		Base   string `json:"base_sha"`
	}
	done, err := completed("pr", &pull)
	if err == nil && !done {
		var base string
		if base, err = mainHead(); err == nil {
			arguments := map[string]any{"repository": repo, "operation_id": operation("pr"), "head": branch, "head_sha": head, "base": "main", "base_sha": base, "title": message, "body": body, "draft": false,
				"issue_number": c.Accepted.Snapshot.IssueNumber, "source_repository": c.Accepted.SourceRepository, "close_on_merge": true}
			if c.Accepted.Snapshot.LinearTeamID != "" {
				// Source provenance comes from the accepted receipt.
				delete(arguments, "source_repository")
				arguments["external_source_url"], arguments["issue_number"], arguments["close_on_merge"] = c.Accepted.Snapshot.URL, 0, false
			}
			var response json.RawMessage
			if response, err = call(ctx, "create_pull_request", arguments); err == nil {
				err = json.Unmarshal(response, &pull)
			}
		}
	}
	if err == nil && pull.Number == 0 {
		err = errors.New("create_pull_request returned no pull request")
	}
	if err != nil {
		return err
	}
	return daemon.recordPublishedPull(ctx, c.Task.ProjectID, c.Task.ID, repo, kernel.ProductionPullRequest{Number: pull.Number, Title: message, URL: pull.URL, Head: pull.Head, HeadRepository: repo, Branch: branch, Base: "main", State: "open", Review: kernel.ProductionReview{Head: pull.Head, State: "unknown"}}, body, pull.Base)
}

// publicationTitle is the accepted title on one line, within the App's
// 256-byte bound and cut on a UTF-8 boundary.
func publicationTitle(title string) string {
	title = strings.Join(strings.Fields(title), " ")
	return strings.ToValidUTF8(title[:min(len(title), 256)], "")
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
