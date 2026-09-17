package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/dark-factory-build/dark-factory/internal/api"
	"math"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/browser"
	"github.com/dark-factory-build/dark-factory/internal/browserprotocol"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// The browser uses the operator DTO vocabulary, with an explicit project on
// every request. Paired private-detail authority is factory-wide; resource
// membership is still checked so a selected project never returns another's data.
type browserContentInput struct {
	api.ContentInput
	ContentID        string `json:"content_id"`
	TaskID           string `json:"task_id"`
	Revision         uint64 `json:"revision"`
	ContentRevision  uint64 `json:"content_revision"`
	TaskWorkRevision uint64 `json:"task_work_revision"`
	Offset           uint64 `json:"offset"`
	Limit            uint64 `json:"limit"`
	TestedSource     string `json:"tested_source"`
	Environment      string `json:"environment"`
	Result           string `json:"result"`
	Location         string `json:"location"`
	Judgment         string `json:"judgment"`
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
	read := request.Operation == "list" || request.Operation == "read" || request.Operation == "body" || request.Operation == "evidence_list" || request.Operation == "attachments"
	_, release, client, err := backend.authorize(ctx, raw, kernel.BrowserCapabilityPrivateHumanRequestDetail)
	if err != nil {
		return browserprotocol.ProjectContentResult{}, err
	}
	defer release()
	if !read && !client.CapabilityMask.Has(kernel.BrowserCapabilityHumanActions) {
		return browserprotocol.ProjectContentResult{}, browser.ErrUnauthorized
	}
	result := browserprotocol.ProjectContentResult{Operation: request.Operation}
	var output any
	if input.Revision > math.MaxInt64 || input.ExpectedRevision > math.MaxInt64 || input.ContentRevision > math.MaxInt64 || input.TaskWorkRevision > math.MaxInt64 || input.Offset > math.MaxInt64 || input.Limit > math.MaxInt64 {
		return result, browser.ErrInvalidRequest
	}
	contentRevision := input.ContentRevision
	at, err := backend.timestamp()
	if err != nil {
		return result, err
	}
	if request.Operation == "list" || request.Operation == "evidence_list" {
		if input.Limit == 0 {
			input.Limit = 1
		}
		if input.Limit > 1 {
			return result, browser.ErrInvalidRequest
		}
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
		page, e := backend.store.ReadContentBody(ctx, id, rev.Int64(), int(input.Offset), int(input.Limit))
		if e != nil {
			return result, mapBrowserError(e)
		}
		output = api.ContentBody{ID: page.ID.String(), Revision: uint64(page.Revision.Int64()), Offset: uint64(page.Offset), Body: page.Body, NextOffset: uint64(page.NextOffset), Complete: page.Complete}
	case "create", "revise":
		id, e := browserContentIDValue(input.ID)
		if e != nil {
			return result, browser.ErrStale
		}
		spec := kernel.NewContent{ID: id, ProjectID: project, Kind: kernel.ContentKind(input.Kind), Title: input.Title, Description: input.Description, Body: input.Body, Author: fmt.Sprintf("browser:%s", client.ID.String()), SourceReferences: input.SourceReferences}
		var item kernel.ContentRevision
		if request.Operation == "create" {
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

	default:
		return result, browser.ErrInvalidRequest
	}
	body, err := json.Marshal(output)
	if err != nil {
		return result, err
	}
	result.Output = body
	if _, err = browserprotocol.EncodeProjectContentResult(strings.Repeat("x", 128), result); err == browserprotocol.ErrOversized {
		return browserprotocol.ProjectContentResult{}, browser.ErrTooLarge
	}
	return result, err
}

var _ browser.ContentBackend = (*browserBackend)(nil)
