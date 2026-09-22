package daemon

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// ChangePublicationEvent is the durable fact set read after a Change
// invalidation. It deliberately contains no provider or overseer decision.
// A new event is cheap to replay: the planner is idempotent at every edge.
type ChangePublicationEvent struct {
	ProjectID              string
	ChangeID               string
	TaskID                 string
	Repository             string
	PullNumber             uint64
	Branch                 string
	Title                  string
	SettledHead            string
	PublishedHead          string
	PublishedSourceHead    string
	PublishOperation       string
	BodyOperation          string
	ReviewHead             string
	ReviewState            string
	Clean                  bool
	Body                   string
	Base                   string
	BaseCommit             string
	Delta                  string
	ReviewOperation        string
	ReviewRequestOperation string
}

type ChangePublicationAction uint8

const (
	ChangePublicationNone ChangePublicationAction = iota
	ChangePublicationPublishAndRefresh
	ChangePublicationRequestReview
)

// PlanChangePublication advances one Change through the judgement-free
// portion of publication. Facts come from the retained Change and the last
// host observation; the caller persists the resulting operation before doing
// the remote write.
func PlanChangePublication(event ChangePublicationEvent) ChangePublicationAction {
	if event.ChangeID == "" || !event.Clean || event.SettledHead == "" {
		return ChangePublicationNone
	}
	// A pull request recorded before the daemon owned publication has a head
	// but no source head. Its branch may be merged, closed or moved, so it is
	// never republished; host review keeps covering it.
	if event.PublishedSourceHead == "" && event.PublishOperation == "" && event.PublishedHead != "" {
		return ChangePublicationNone
	}
	if event.SettledHead != event.PublishedSourceHead {
		return ChangePublicationPublishAndRefresh
	}
	if event.PublishOperation != "" && event.BodyOperation == "" {
		return ChangePublicationPublishAndRefresh
	}
	if event.ReviewOperation == "" && (event.ReviewHead != event.PublishedHead || !reviewStateRecorded(event.ReviewState)) {
		return ChangePublicationRequestReview
	}
	return ChangePublicationNone
}

func reviewStateRecorded(state string) bool {
	return state == "allow" || state == "block" || state == "note"
}

const (
	changePublicationHead          = "<!-- dark-factory:head="
	changePublicationBase          = "<!-- dark-factory:base="
	changePublicationDelta         = "<!-- dark-factory:delta="
	changePublicationReviewRequest = "<!-- dark-factory:review-request="
)

// RefreshChangeBody replaces only the daemon-owned fact lines and preserves
// the worker's prose. Missing lines are appended so old publications converge
// without a second judgement pass.
func RefreshChangeBody(body, head, base, delta string) string {
	values := []string{changePublicationHead + head + " -->", changePublicationBase + base + " -->", changePublicationDelta + delta + " -->"}
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, changePublicationHead) || strings.HasPrefix(line, changePublicationBase) || strings.HasPrefix(line, changePublicationDelta) {
			continue
		}
		kept = append(kept, line)
	}
	lines = kept
	insertAt := len(lines)
	for i, line := range lines {
		if terminalIssueFooter(line) {
			insertAt = i
			break
		}
	}
	lines = append(lines, "", "", "")
	copy(lines[insertAt+len(values):], lines[insertAt:len(lines)-len(values)])
	copy(lines[insertAt:], values)
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// RefreshChangeReviewRequest adds the daemon's explicit host handoff while
// keeping the standalone issue footer terminal (the App trailer may follow it).
func RefreshChangeReviewRequest(body, operation, head string) string {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(body, "\r\n", "\n"), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	kept := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, changePublicationReviewRequest) {
			continue
		}
		kept = append(kept, line)
	}
	lines = kept
	marker := changePublicationReviewRequest + operation + ":" + head + " -->"
	insertAt := len(lines)
	for i, line := range lines {
		if terminalIssueFooter(line) {
			insertAt = i
			break
		}
	}
	lines = append(lines, "")
	copy(lines[insertAt+1:], lines[insertAt:len(lines)-1])
	lines[insertAt] = marker
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

func terminalIssueFooter(line string) bool {
	fields := strings.Fields(line)
	if len(fields) != 2 || (fields[0] != "Refs" && fields[0] != "Closes") {
		return false
	}
	value := fields[1]
	if slash := strings.LastIndexByte(value, '#'); slash < 0 || slash == len(value)-1 {
		return false
	} else {
		for _, r := range value[slash+1:] {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// ChangePublicationActions is the narrow remote seam. Implementations are
// host-owned Maintainer calls; the daemon only supplies recorded facts.
type ChangePublicationActions struct {
	PublishAndRefresh func(context.Context, ChangePublicationEvent, string) error
	RequestReview     func(context.Context, ChangePublicationEvent) error
	// RecordFailure persists one Change's failed transition so the loop
	// returns only store errors; a nil RecordFailure surfaces the cause.
	RecordFailure func(context.Context, ChangePublicationEvent, error) error
}

func (daemon *Daemon) processChangePublications(ctx context.Context) error {
	if daemon == nil || daemon.github == nil || daemon.changePublicationEvents == nil {
		return nil
	}
	events, err := daemon.changePublicationEvents(ctx)
	if err != nil {
		return err
	}
	return ProcessChangePublicationEvents(ctx, events, daemon.changePublicationActions)
}

func (daemon *Daemon) durableChangePublicationEvents(ctx context.Context) ([]ChangePublicationEvent, error) {
	facts, err := daemon.store.ChangePublicationFacts(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]ChangePublicationEvent, 0, len(facts))
	for _, fact := range facts {
		result = append(result, ChangePublicationEvent{ProjectID: fact.ProjectID, ChangeID: fact.ChangeID, TaskID: fact.TaskID, Repository: fact.Repository, PullNumber: fact.PullNumber, Branch: fact.Branch, Title: fact.Title, SettledHead: fact.SettledHead, PublishedHead: fact.PublishedHead, PublishedSourceHead: fact.PublishedSourceHead, PublishOperation: fact.PublishOperation, BodyOperation: fact.BodyOperation, ReviewRequestOperation: fact.ReviewRequestOperation, ReviewOperation: fact.ReviewOperation, ReviewHead: fact.ReviewHead, ReviewState: fact.ReviewState, Clean: true, Body: fact.Body, Base: fact.Base, BaseCommit: fact.BaseCommit, Delta: fmt.Sprint(fact.Delta)})
	}
	return result, nil
}

func (daemon *Daemon) configureChangePublication() {
	daemon.changePublicationEvents = daemon.durableChangePublicationEvents
	daemon.changePublicationActions = ChangePublicationActions{PublishAndRefresh: daemon.publishAndRefreshChange, RequestReview: daemon.requestChangeReview, RecordFailure: daemon.recordChangePublicationFailure}
}

// recordChangePublicationFailure keeps the cause on the pull request's
// publication receipt; the next pass retries the idempotent operation.
func (daemon *Daemon) recordChangePublicationFailure(ctx context.Context, event ChangePublicationEvent, cause error) error {
	at, err := daemon.timestamp()
	if err != nil {
		return err
	}
	return daemon.store.RecordChangePublication(ctx, event.ProjectID, event.Repository, event.PullNumber, kernel.PublicationReceipt{Failure: boundedDetail(cause)}, nil, "", at)
}

func operationID(kind, repository, changeID, head string) string {
	digest := sha256.Sum256([]byte("dark-factory:" + kind + ":" + repository + ":" + changeID + ":" + head))
	hexValue := hex.EncodeToString(digest[:16])
	return hexValue[:8] + "-" + hexValue[8:12] + "-4" + hexValue[13:16] + "-8" + hexValue[17:20] + "-" + hexValue[20:32]
}

func hostReviewOperationID(repository string, pull uint64, head string) string {
	name := "dark-factory:host-review:" + repository + ":" + strconv.FormatUint(pull, 10) + ":" + head
	namespace := []byte{0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1, 0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8}
	hash := sha1.Sum(append(namespace, []byte(name)...))
	hash[6] = (hash[6] & 0x0f) | 0x50
	hash[8] = (hash[8] & 0x3f) | 0x80
	hexValue := hex.EncodeToString(hash[:])
	return hexValue[:8] + "-" + hexValue[8:12] + "-" + hexValue[12:16] + "-" + hexValue[16:20] + "-" + hexValue[20:32]
}

func (daemon *Daemon) maintainerCall(ctx context.Context, repository string, operation, name string, arguments map[string]any) (json.RawMessage, error) {
	arguments["repository"] = repository
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}})
	if err != nil {
		return nil, err
	}
	repositoryID, err := daemon.store.RepositoryGitHubIDByName(ctx, repository)
	if err != nil {
		return nil, err
	}
	response, err := daemon.github.MCP(ctx, request, map[string]uint64{repository: repositoryID})
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &envelope); err != nil || envelope.Result.IsError {
		return nil, fmt.Errorf("maintainer %s refused", name)
	}
	return response, nil
}

func (daemon *Daemon) requestChangeReview(ctx context.Context, event ChangePublicationEvent) error {
	requestOperation := operationID("review-request", event.Repository, event.ChangeID, event.SettledHead)
	body := RefreshChangeReviewRequest(event.Body, requestOperation, event.PublishedHead)
	if _, err := daemon.maintainerCall(ctx, event.Repository, requestOperation, "update_pull_request_body", map[string]any{"operation_id": requestOperation, "pull_number": event.PullNumber, "body": body}); err != nil {
		return err
	}
	at, err := daemon.timestamp()
	if err != nil {
		return err
	}
	return daemon.store.RecordChangePublication(ctx, event.ProjectID, event.Repository, event.PullNumber, kernel.PublicationReceipt{PublishedHead: event.PublishedHead, BodyOperation: requestOperation, ReviewRequestOperation: requestOperation}, &body, hostReviewOperationID(event.Repository, event.PullNumber, event.PublishedHead), at)
}

func (daemon *Daemon) publishAndRefreshChange(ctx context.Context, event ChangePublicationEvent, body string) error {
	parent := daemon.changeParent.Load()
	if parent == nil || *parent == "" || event.Branch == "" {
		return fmt.Errorf("Change %s has no publication checkout", event.ChangeID)
	}
	path := filepath.Join(*parent, event.ChangeID)
	git := daemon.gitExecutable.Load()
	if git == nil || *git == "" {
		return fmt.Errorf("Change %s has no trusted Git executable", event.ChangeID)
	}
	if event.SettledHead == event.PublishedSourceHead && event.PublishOperation != "" {
		return daemon.refreshPublishedBody(ctx, event, event.PublishedHead, event.Delta)
	}
	sourceBase, err := publicationDiffBase(event)
	if err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, *git, "-C", path, "diff", "--binary", "--no-renames", "--name-status", sourceBase, event.SettledHead).Output()
	if err != nil {
		return err
	}
	// The Maintainer boundary accepts file contents, not a patch. A retained
	// Change has already been checked out at SettledHead; publish the changed
	// paths with their exact bytes and leave removals content-less.
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	changes := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 || fields[1] == "" {
			continue
		}
		item := map[string]any{"path": fields[1]}
		if fields[0] != "D" {
			content, readErr := exec.CommandContext(ctx, *git, "-C", path, "show", event.SettledHead+":"+fields[1]).Output()
			if readErr != nil {
				return readErr
			}
			item["content_base64"] = base64.StdEncoding.EncodeToString(content)
		}
		changes = append(changes, item)
	}
	if len(changes) == 0 {
		return fmt.Errorf("Change %s has no publishable file delta", event.ChangeID)
	}
	numstat, err := exec.CommandContext(ctx, *git, "-C", path, "diff", "--no-renames", "--numstat", sourceBase, event.SettledHead).Output()
	if err != nil {
		return err
	}
	delta, err := changedProductionLineDelta(string(numstat))
	if err != nil {
		return err
	}
	publishOperation := operationID("publish", event.Repository, event.ChangeID, event.SettledHead)
	response, err := daemon.maintainerCall(ctx, event.Repository, publishOperation, "publish_commit", map[string]any{"operation_id": publishOperation, "branch": event.Branch, "expected_head_sha": event.PublishedHead, "message": "Update Change " + event.ChangeID, "changes": changes})
	if err != nil {
		return err
	}
	var published struct {
		Result struct {
			StructuredContent struct {
				CommitSHA string `json:"commit_sha"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response, &published); err != nil || published.Result.StructuredContent.CommitSHA == "" {
		return fmt.Errorf("publish_commit returned no commit")
	}
	at, err := daemon.timestamp()
	if err != nil {
		return err
	}
	publishedHead := published.Result.StructuredContent.CommitSHA
	if err := daemon.store.RecordChangePublication(ctx, event.ProjectID, event.Repository, event.PullNumber, kernel.PublicationReceipt{SourceHead: event.SettledHead, PublishedHead: publishedHead, Delta: delta, DeltaSet: true, PublishOperation: publishOperation}, nil, "", at); err != nil {
		return err
	}
	body = RefreshChangeBody(event.Body, publishedHead, event.Base, strconv.FormatInt(delta, 10))
	bodyOperation := operationID("body", event.Repository, event.ChangeID, event.SettledHead)
	if _, err := daemon.maintainerCall(ctx, event.Repository, bodyOperation, "update_pull_request_body", map[string]any{"operation_id": bodyOperation, "pull_number": event.PullNumber, "body": body}); err != nil {
		return err
	}
	at, err = daemon.timestamp()
	if err != nil {
		return err
	}
	return daemon.store.RecordChangePublication(ctx, event.ProjectID, event.Repository, event.PullNumber, kernel.PublicationReceipt{PublishedHead: publishedHead, BodyOperation: bodyOperation}, &body, "", at)
}

func publicationDiffBase(event ChangePublicationEvent) (string, error) {
	if event.PublishedSourceHead != "" {
		return event.PublishedSourceHead, nil
	}
	if event.BaseCommit != "" {
		return event.BaseCommit, nil
	}
	return "", fmt.Errorf("Change %s has no locally verified publication base", event.ChangeID)
}

func (daemon *Daemon) refreshPublishedBody(ctx context.Context, event ChangePublicationEvent, head, delta string) error {
	body := RefreshChangeBody(event.Body, head, event.Base, delta)
	operation := operationID("body", event.Repository, event.ChangeID, event.SettledHead)
	if _, err := daemon.maintainerCall(ctx, event.Repository, operation, "update_pull_request_body", map[string]any{"operation_id": operation, "pull_number": event.PullNumber, "body": body}); err != nil {
		return err
	}
	at, err := daemon.timestamp()
	if err != nil {
		return err
	}
	return daemon.store.RecordChangePublication(ctx, event.ProjectID, event.Repository, event.PullNumber, kernel.PublicationReceipt{PublishedHead: head, BodyOperation: operation}, &body, "", at)
}

func changedProductionLineDelta(numstat string) (int64, error) {
	var total int64
	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 {
			return 0, fmt.Errorf("invalid git numstat")
		}
		if !productionLinePath(fields[2]) || fields[0] == "-" || fields[1] == "-" {
			continue
		}
		added, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, err
		}
		removed, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, err
		}
		if (added > 0 && total > int64(^uint64(0)>>1)-added) || (removed > 0 && total < -int64(^uint64(0)>>1)-1+removed) {
			return 0, fmt.Errorf("git numstat overflow")
		}
		total += added - removed
	}
	return total, nil
}

func productionLinePath(path string) bool {
	clean := strings.TrimPrefix(path, "./")
	for _, part := range strings.Split(clean, "/") {
		if part == "docs" || part == "doc" || part == "fixtures" || part == "test" || part == "tests" {
			return false
		}
	}
	return !strings.HasSuffix(clean, "_test.go") && !strings.HasSuffix(clean, "_test.mjs") && !strings.HasSuffix(clean, "_test.ts")
}

// ProcessChangePublicationEvents runs independent Changes concurrently. A
// durable event may be replayed after a crash, so completion is reported only
// after the selected idempotent remote operation returns.
func ProcessChangePublicationEvents(ctx context.Context, events []ChangePublicationEvent, actions ChangePublicationActions) error {
	if ctx == nil {
		return fmt.Errorf("nil publication context")
	}
	var wait sync.WaitGroup
	errors := make(chan error, len(events))
	for _, event := range events {
		event := event
		action := PlanChangePublication(event)
		if action == ChangePublicationNone {
			continue
		}
		wait.Add(1)
		go func() {
			defer wait.Done()
			var err error
			switch action {
			case ChangePublicationPublishAndRefresh:
				if actions.PublishAndRefresh == nil {
					err = fmt.Errorf("Change %s has no publish action", event.ChangeID)
				} else {
					err = actions.PublishAndRefresh(ctx, event, RefreshChangeBody(event.Body, event.SettledHead, event.Base, event.Delta))
				}
			case ChangePublicationRequestReview:
				if actions.RequestReview == nil {
					err = fmt.Errorf("Change %s has no review action", event.ChangeID)
				} else {
					err = actions.RequestReview(ctx, event)
				}
			}
			if err != nil && actions.RecordFailure != nil {
				err = actions.RecordFailure(ctx, event, err)
			}
			if err != nil {
				errors <- err
			}
		}()
	}
	wait.Wait()
	close(errors)
	var result error
	for err := range errors {
		result = errorsJoin(result, err)
	}
	return result
}

// errorsJoin retains every Change failure while other Changes finish their
// independent transition.
func errorsJoin(first, second error) error {
	if first == nil {
		return second
	}
	return fmt.Errorf("%v; %w", first, second)
}
