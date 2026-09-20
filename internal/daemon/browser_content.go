package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// The browser uses the operator DTO vocabulary, with an explicit project on
// every request. Paired private-detail authority is factory-wide; resource
// membership is still checked so a selected project never returns another's data.
type browserContentInput struct {
	api.ContentInput
	Document              kernel.OutcomeDocument `json:"document"`
	Objective             string                 `json:"objective"`
	Criteria              string                 `json:"criteria"`
	ContentID             string                 `json:"content_id"`
	TaskID                string                 `json:"task_id"`
	Revision              uint64                 `json:"revision"`
	ContentRevision       uint64                 `json:"content_revision"`
	TaskWorkRevision      uint64                 `json:"task_work_revision"`
	Offset                uint64                 `json:"offset"`
	Limit                 uint64                 `json:"limit"`
	TestedSource          string                 `json:"tested_source"`
	Environment           string                 `json:"environment"`
	Result                string                 `json:"result"`
	Location              string                 `json:"location"`
	Judgment              string                 `json:"judgment"`
	OwnerAgentID          string                 `json:"owner_agent_id"`
	ExpectedAgentRevision uint64                 `json:"expected_agent_revision"`
}

func browserContentProject(s string) (kernel.ProjectID, error) {
	return browserID(s, kernel.ProjectIDFromBytes)
}
func browserContentIDValue(s string) (kernel.ContentID, error) {
	return browserID(s, kernel.ContentIDFromBytes)
}
func browserContentRevision(n uint64) (kernel.Revision, error) { return kernel.NewRevision(int64(n)) }

func (backend *browserBackend) ProjectContent(ctx context.Context, raw [browserprotocol.ClientIDSize]byte, request browserprotocol.ProjectContent) (browserprotocol.ProjectContentResult, error) {
	var input browserContentInput
	if err := json.Unmarshal(request.Input, &input); err != nil {
		return browserprotocol.ProjectContentResult{}, browser.ErrInvalidRequest
	}
	project, err := browserContentProject(input.ProjectID)
	if err != nil {
		return browserprotocol.ProjectContentResult{}, browser.ErrInvalidRequest
	}
	read := request.Operation == "list" || request.Operation == "read" || request.Operation == "body" || request.Operation == "evidence_list" || request.Operation == "attachments" || request.Operation == "outcome_list" || request.Operation == "outcome_read" || request.Operation == "mission_tasks"
	_, release, client, err := backend.authorize(ctx, raw, kernel.BrowserCapabilityPrivateHumanRequestDetail)
	if err != nil {
		return browserprotocol.ProjectContentResult{}, err
	}
	defer release()
	if !read && !client.CapabilityMask.Has(kernel.BrowserCapabilityHumanActions) {
		return browserprotocol.ProjectContentResult{}, browser.ErrUnauthorized
	}
	if request.Operation == "create" || request.Operation == "revise" || request.Operation == "deprecate" || request.Operation == "body" || request.Operation == "mission_create" {
		if backend.owner == nil {
			return browserprotocol.ProjectContentResult{}, browser.ErrUnauthorized
		}
		backend.owner.operationMu.Lock()
		defer backend.owner.operationMu.Unlock()
	}
	result := browserprotocol.ProjectContentResult{Operation: request.Operation}
	var output any
	if input.ExpectedAgentRevision > (1<<53)-1 || input.Revision > (1<<53)-1 || input.ExpectedRevision > (1<<53)-1 || input.ContentRevision > (1<<53)-1 || input.TaskWorkRevision > (1<<53)-1 || input.Offset > (1<<53)-1 || input.Limit > (1<<53)-1 {
		return result, browser.ErrInvalidRequest
	}
	contentRevision := input.ContentRevision
	at, err := backend.timestamp()
	if err != nil {
		return result, err
	}
	if request.Operation == "list" || request.Operation == "evidence_list" || request.Operation == "outcome_list" {
		maximum := uint64(1)
		if request.Operation == "outcome_list" && input.Kind == "mission" {
			maximum = 8 // Mission lists return summaries; full documents are read on selection.
		}
		if input.Limit == 0 {
			input.Limit = 1
		}
		if input.Limit > maximum {
			return result, browser.ErrInvalidRequest
		}
	}

	if request.Operation == "mission_tasks" && (input.Limit == 0 || input.Limit > 8) {
		return result, browser.ErrInvalidRequest
	}
	switch request.Operation {
	case "list":
		page, e := backend.store.ListContent(ctx, project, kernel.ContentKind(input.Kind), int(input.Offset), int(input.Limit))
		if e != nil {
			return result, mapBrowserError(e)
		}
		out := api.ContentList{Items: []api.Content{}, NextOffset: uint64(page.NextOffset)}
		for _, item := range page.Items {
			out.Items = append(out.Items, contentDTO(item))
		}
		output = out
	case "read":
		id, e := browserContentIDValue(input.ID)
		if e != nil {
			return result, browser.ErrInvalidRequest
		}
		item, e := backend.store.Content(ctx, id, int64(input.Revision))
		if e != nil {
			return result, mapBrowserError(e)
		}
		if item.ProjectID != project {
			return result, browser.ErrUnauthorized
		}
		output = contentDTO(item)
	case "body":
		id, e := browserContentIDValue(input.ID)
		rev, re := browserContentRevision(input.Revision)
		if e != nil || re != nil || input.Limit == 0 || input.Limit > 8192 {
			return result, browser.ErrInvalidRequest
		}
		item, e := backend.store.Content(ctx, id, rev.Int64())
		if e != nil {
			return result, mapBrowserError(e)
		}
		if item.ProjectID != project {
			return result, browser.ErrUnauthorized
		}
		item, sourceBody, e := backend.owner.contentBodySource(ctx, item)
		if e != nil {
			return result, mapBrowserError(e)
		}
		page, e := pageContentBody(item, sourceBody, int(input.Offset), int(input.Limit))
		if e != nil {
			return result, mapBrowserError(e)
		}
		output = api.ContentBody{ID: page.ID.String(), Revision: uint64(page.Revision.Int64()), Offset: uint64(page.Offset), Body: page.Body, NextOffset: uint64(page.NextOffset), Complete: page.Complete}
	case "create", "revise":
		id, e := browserContentIDValue(input.ID)
		if e != nil || !validBrowserContentWrite(input) {
			return result, browser.ErrStale
		}
		spec := kernel.NewContent{ID: id, ProjectID: project, Kind: kernel.ContentKind(input.Kind), Title: input.Title, Description: input.Description, Body: input.Body, Author: fmt.Sprintf("browser:%s", client.ID.String()), SourceReferences: input.SourceReferences, Commit: input.Commit, Path: input.Path}
		var item kernel.ContentRevision
		if request.Operation == "create" {
			if existing, existingErr := backend.store.Content(ctx, id, 1); existingErr == nil {
				if existing.ProjectID != project || existing.LatestRevision.Int64() != 1 {
					return result, browser.ErrStale
				}
				spec.RepositoryDevice, spec.RepositoryInode = existing.RepositoryDevice, existing.RepositoryInode
			} else if existingErr != kernel.ErrNotFound {
				return result, mapBrowserError(existingErr)
			}
			spec, e = backend.owner.writeContentSource(ctx, spec, 1)
			if e != nil {
				return result, mapBrowserError(e)
			}
			item, e = backend.store.CreateContent(ctx, spec, at)
		} else {
			rev, re := browserContentRevision(input.ExpectedRevision)
			if re != nil {
				return result, browser.ErrStale
			}
			current, ce := backend.store.Content(ctx, id, rev.Int64())
			if ce != nil {
				return result, mapBrowserError(ce)
			}
			if current.ProjectID != project {
				return result, browser.ErrUnauthorized
			}
			if latest := current.LatestRevision.Int64(); latest != int64(input.ExpectedRevision) && latest != int64(input.ExpectedRevision)+1 {
				return result, browser.ErrStale
			}
			spec, e = backend.owner.writeContentSource(ctx, spec, input.ExpectedRevision+1)
			if e != nil {
				return result, mapBrowserError(e)
			}
			item, e = backend.store.ReviseContent(ctx, rev, spec, at)
		}
		if e != nil {
			return result, mapBrowserError(e)
		}
		output = contentDTO(item)
	case "deprecate":
		id, e := browserContentIDValue(input.ID)
		rev, re := browserContentRevision(input.ExpectedRevision)
		if e != nil || re != nil {
			return result, browser.ErrStale
		}
		current, ce := backend.store.Content(ctx, id, rev.Int64())
		if ce != nil {
			return result, mapBrowserError(ce)
		}
		if current.ProjectID != project {
			return result, browser.ErrUnauthorized
		}
		if latest := current.LatestRevision.Int64(); latest != int64(input.ExpectedRevision) && latest != int64(input.ExpectedRevision)+1 {
			return result, browser.ErrStale
		}
		if current.Commit == "" {
			_, _ = backend.owner.exportLegacyContent(ctx, id, int64(input.ExpectedRevision))
		}
		item, e := backend.store.DeprecateContent(ctx, id, project, rev, fmt.Sprintf("browser:%s", client.ID.String()), at)
		if e != nil {
			return result, mapBrowserError(e)
		}
		output = contentDTO(item)
	case "evidence":
		eid, e := browserID(input.ID, kernel.ContentEvidenceIDFromBytes)
		cid, ce := browserContentIDValue(input.ContentID)
		rev, re := browserContentRevision(contentRevision)
		if e != nil || ce != nil || re != nil {
			return result, browser.ErrStale
		}
		content, e := backend.store.Content(ctx, cid, rev.Int64())
		if e != nil {
			return result, mapBrowserError(e)
		}
		if content.ProjectID != project {
			return result, browser.ErrUnauthorized
		}
		item, e := backend.store.CreateContentEvidence(ctx, kernel.NewContentEvidence{ID: eid, ProjectID: project, ContentID: cid, ContentRevision: rev, TestedSource: input.TestedSource, Environment: input.Environment, Result: input.Result, Location: input.Location, Evaluator: fmt.Sprintf("browser:%s", client.ID.String()), Judgment: input.Judgment}, at)
		if e != nil {
			return result, mapBrowserError(e)
		}
		output = evidenceDTO(item)
	case "evidence_list":
		cid, e := browserContentIDValue(input.ContentID)
		rev, re := browserContentRevision(contentRevision)
		if e != nil || re != nil {
			return result, browser.ErrStale
		}
		content, e := backend.store.Content(ctx, cid, rev.Int64())
		if e != nil {
			return result, mapBrowserError(e)
		}
		if content.ProjectID != project {
			return result, browser.ErrUnauthorized
		}
		page, e := backend.store.ListContentEvidence(ctx, project, cid, rev, int(input.Offset), int(input.Limit))
		if e != nil {
			return result, mapBrowserError(e)
		}
		out := api.ContentEvidenceList{Items: []api.ContentEvidence{}, NextOffset: uint64(page.NextOffset)}
		for _, item := range page.Items {
			out.Items = append(out.Items, evidenceDTO(item))
		}
		output = out

	case "attach":
		task, e := browserID(input.TaskID, kernel.TaskIDFromBytes)
		cid, ce := browserContentIDValue(input.ContentID)
		rev, re := browserContentRevision(contentRevision)
		if e != nil || ce != nil || re != nil {
			return result, browser.ErrStale
		}
		current, found, e := backend.store.Task(ctx, task)
		if e != nil {
			return result, mapBrowserError(e)
		}
		if !found {
			return result, browser.ErrNotFound
		}
		if current.ProjectID != project {
			return result, browser.ErrUnauthorized
		}
		content, e := backend.store.Content(ctx, cid, rev.Int64())
		if e != nil {
			return result, mapBrowserError(e)
		}
		if content.ProjectID != project {
			return result, browser.ErrUnauthorized
		}
		if e = backend.store.AttachContentToTask(ctx, task, project, cid, rev, at); e != nil {
			return result, mapBrowserError(e)
		}
		output = map[string]any{"task_id": task.String(), "project_id": project.String(), "content_id": cid.String(), "content_revision": rev.Int64()}
	case "attachments":
		task, e := browserID(input.TaskID, kernel.TaskIDFromBytes)
		work, re := browserContentRevision(input.TaskWorkRevision)
		if e != nil || re != nil {
			return result, browser.ErrStale
		}
		current, found, e := backend.store.Task(ctx, task)
		if e != nil {
			return result, mapBrowserError(e)
		}
		if !found {
			return result, browser.ErrNotFound
		}
		if current.ProjectID != project {
			return result, browser.ErrUnauthorized
		}
		refs, e := backend.store.TaskContentReferences(ctx, project, task, work)
		if e != nil {
			return result, mapBrowserError(e)
		}
		out := api.ContentAttachments{Items: []api.ContentAttachment{}}
		for _, item := range refs {
			out.Items = append(out.Items, attachmentDTO(item))
		}
		output = out

	case "outcome_list":
		list := backend.store.ListOutcomes
		if input.Kind == "mission" {
			list = backend.store.ListMissionOutcomes
		}
		page, e := list(ctx, project, int(input.Offset), int(input.Limit))
		if e != nil {
			return result, mapBrowserError(e)
		}
		if input.Kind == "mission" {
			items := make([]map[string]any, 0, len(page.Items))
			for _, item := range page.Items {
				objective := []rune(item.Objective)
				if len(objective) > 240 {
					objective = append(objective[:240], '…')
				}
				items = append(items, map[string]any{"id": item.ID.String(), "objective": string(objective), "state": item.State, "stale": item.Stale})
			}
			output = map[string]any{"items": items, "next_offset": page.NextOffset}
			break
		}
		out := api.OutcomeList{Items: []api.Outcome{}, NextOffset: uint64(page.NextOffset)}
		for _, item := range page.Items {
			out.Items = append(out.Items, outcomeDTO(item))
		}
		output = out
	case "mission_create":
		owner, e := browserID(input.OwnerAgentID, kernel.AgentIDFromBytes)
		revision, re := kernel.NewRevision(int64(input.ExpectedAgentRevision))
		id, ie := outcomeID(input.ID)
		if e != nil || re != nil || ie != nil || input.ExpectedAgentRevision == 0 || input.Objective == "" || input.Criteria == "" {
			return result, browser.ErrInvalidRequest
		}
		created, ce := backend.store.CreateMissionForBrowser(ctx, client.ID, kernel.MissionCreate{ID: id, ProjectID: project, OwnerAgentID: owner, ExpectedAgentRevision: revision, Objective: input.Objective, Criteria: input.Criteria}, at)
		if ce != nil {
			return result, mapBrowserError(ce)
		}
		out := outcomeDTO(created.Mission)
		out.Objective, out.Criteria = "", ""
		output = out
		backend.owner.notifyScheduler()
	case "mission_tasks":
		id, e := outcomeID(input.ID)
		if e != nil {
			return result, browser.ErrInvalidRequest
		}
		items, next, e := backend.store.ListMissionTasks(ctx, project, id, int(input.Offset), int(input.Limit))
		if e != nil {
			return result, mapBrowserError(e)
		}
		tasks := make([]map[string]any, 0, len(items))
		for _, item := range items {
			tasks = append(tasks, map[string]any{"task_id": item.ID.String(), "project_id": item.ProjectID.String(), "title": item.Title, "status": item.Status.String(), "assigned_agent_id": item.AssignedAgentID.String(), "revision": fmt.Sprintf("%d", item.Revision.Int64()), "work_revision": fmt.Sprintf("%d", item.WorkRevision.Int64()), "priority": fmt.Sprint(item.Priority), "blocked_reason": item.BlockedReason})
		}
		output = map[string]any{"mission_id": id.String(), "tasks": tasks, "next_offset": next}
	case "outcome_read", "outcome_write":
		id, e := outcomeID(input.ID)
		if e != nil {
			return result, browser.ErrInvalidRequest
		}
		var item kernel.OutcomeRevision
		if request.Operation == "outcome_read" {
			item, e = backend.store.Outcome(ctx, project, id, int64(input.Revision))
		} else {
			item, e = backend.store.WriteOutcomeForBrowser(ctx, client.ID, kernel.NewOutcome{ID: id, ProjectID: project, Document: input.Document}, int64(input.ExpectedRevision), at)
		}
		if e != nil {
			return result, mapBrowserError(e)
		}
		out := outcomeDTO(item)
		// The full document already includes these potentially large strings.
		out.Objective, out.Criteria = "", ""
		output = out
	default:
		return result, browser.ErrInvalidRequest
	}
	body, err := json.Marshal(output)
	if err != nil {
		return result, err
	}
	result.Output = body
	if _, err = browserprotocol.EncodeProjectContentResult(strings.Repeat("x", 64), result); err == browserprotocol.ErrOversized {
		return browserprotocol.ProjectContentResult{}, browser.ErrTooLarge
	}
	return result, err
}

func validBrowserContentWrite(input browserContentInput) bool {
	return len(input.Kind) >= 1 && len(input.Kind) <= 64 && len(input.Title) >= 1 && len(input.Title) <= 1024 && len(input.Description) <= 4096 && len(input.Body) <= 1<<20 && len(input.SourceReferences) <= 32768 && utf8.ValidString(input.Kind) && utf8.ValidString(input.Title) && utf8.ValidString(input.Description) && utf8.ValidString(input.Body) && utf8.ValidString(input.SourceReferences) && (input.Commit == "") == (input.Path == "") && (input.Commit == "" || input.Body == "" && len(input.Commit) >= 40 && len(input.Commit) <= 64 && len(input.Path) >= 1 && len(input.Path) <= 4096 && utf8.ValidString(input.Path))
}

var _ browser.ContentBackend = (*browserBackend)(nil)
