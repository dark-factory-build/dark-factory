package daemon

import (
	"context"
	"encoding/hex"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func contentID(s string) (kernel.ContentID, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return kernel.ContentID{}, kernel.ErrInvalidValue
	}
	return kernel.ContentIDFromBytes(b)
}
func projectID(s string) (kernel.ProjectID, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return kernel.ProjectID{}, kernel.ErrInvalidValue
	}
	return kernel.ProjectIDFromBytes(b)
}
func taskID(s string) (kernel.TaskID, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 16 {
		return kernel.TaskID{}, kernel.ErrInvalidValue
	}
	return kernel.TaskIDFromBytes(b)
}
func revision(n uint64) (kernel.Revision, error) { return kernel.NewRevision(int64(n)) }
func contentDTO(v kernel.ContentRevision, body bool) api.Content {
	return api.Content{ID: v.ID.String(), ProjectID: v.ProjectID.String(), Kind: string(v.Kind), Title: v.Title, Description: v.Description, Body: func() string {
		if body {
			return v.Body
		}
		return ""
	}(), Author: v.Author, SourceReferences: v.SourceReferences, Revision: uint64(v.Revision.Int64()), Deprecated: v.Deprecated}
}
func evidenceDTO(v kernel.ContentEvidence) api.ContentEvidence {
	return api.ContentEvidence{ID: v.ID.String(), ProjectID: v.ProjectID.String(), ContentID: v.ContentID.String(), ContentRevision: uint64(v.ContentRevision.Int64()), TestedSource: v.TestedSource, Environment: v.Environment, Result: v.Result, Location: v.Location, Evaluator: v.Evaluator, Judgment: v.Judgment}
}

func (daemon *Daemon) content(ctx context.Context, call api.Call) api.Reply {
	at, _ := kernel.NewUnixMillis(daemon.now().UnixMilli())
	input, _ := call.ContentInput()
	list, _ := call.ContentListInput()
	read, _ := call.ContentReadInput()
	body, _ := call.ContentBodyInput()
	ev, _ := call.ContentEvidenceInput()
	attach, _ := call.ContentAttachInput()
	digest, attempt := call.AttemptDigest()
	var kd kernel.AttemptDigest
	if attempt {
		var err error
		kd, err = attemptDigest(digest)
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
	}
	operator := !attempt
	pid, err := projectID(firstNonEmpty(input.ProjectID, list.ProjectID, ev.ProjectID, attach.ProjectID))
	if err != nil && (operator || call.Kind() != api.CallContentRead && call.Kind() != api.CallContentBody) {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	makeSpec := func() (kernel.NewContent, error) {
		id, e := contentID(input.ID)
		if e != nil {
			return kernel.NewContent{}, e
		}
		p, e := projectID(input.ProjectID)
		if e != nil {
			return kernel.NewContent{}, e
		}
		return kernel.NewContent{ID: id, ProjectID: p, Kind: kernel.ContentKind(input.Kind), Title: input.Title, Description: input.Description, Body: input.Body, Author: input.Author, SourceReferences: input.SourceReferences}, nil
	}
	switch call.Kind() {
	case api.CallContentCreate, api.CallContentRevise, api.CallContentDeprecate:
		var v kernel.ContentRevision
		var e error
		if call.Kind() == api.CallContentCreate {
			spec, specErr := makeSpec()
			if specErr != nil {
				return newErrorReply(remoteErrorCode(specErr))
			}
			if operator {
				v, e = daemon.store.CreateContent(ctx, spec, at)
			} else {
				v, e = daemon.store.CreateContentForAttempt(ctx, kd, spec, at)
			}
		} else if call.Kind() == api.CallContentRevise {
			spec, specErr := makeSpec()
			if specErr != nil {
				return newErrorReply(remoteErrorCode(specErr))
			}
			r, e2 := revision(input.ExpectedRevision)
			if e2 != nil {
				return newErrorReply(api.RemoteInvalidRequest)
			}
			if operator {
				v, e = daemon.store.ReviseContent(ctx, r, spec, at)
			} else {
				v, e = daemon.store.ReviseContentForAttempt(ctx, kd, r, spec, at)
			}
		} else {
			id, e2 := contentID(input.ID)
			if e2 != nil {
				return newErrorReply(api.RemoteInvalidRequest)
			}
			r, e2 := revision(input.ExpectedRevision)
			if e2 != nil {
				return newErrorReply(api.RemoteInvalidRequest)
			}
			if operator {
				v, e = daemon.store.DeprecateContent(ctx, id, pid, r, input.Author, at)
			} else {
				v, e = daemon.store.DeprecateContentForAttempt(ctx, kd, id, r, input.Author, at)
			}
		}
		if e != nil {
			return newErrorReply(remoteErrorCode(e))
		}
		return api.NewContentReply(contentDTO(v, false))
	case api.CallContentList:
		kind := kernel.ContentKind(list.Kind)
		var v kernel.ContentPage
		if operator {
			v, err = daemon.store.ListContent(ctx, pid, kind, int(list.Offset), int(list.Limit))
		} else {
			v, err = daemon.store.ListContentForAttempt(ctx, kd, kind, int(list.Offset), int(list.Limit))
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		out := api.ContentList{NextOffset: uint64(v.NextOffset)}
		for _, x := range v.Items {
			out.Items = append(out.Items, contentDTO(x, false))
		}
		return api.NewContentReply(out)
	case api.CallContentRead:
		id, e := contentID(read.ID)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		var v kernel.ContentRevision
		if operator {
			v, err = daemon.store.Content(ctx, id, int64(read.Revision))
		} else {
			v, err = daemon.store.ContentForAttempt(ctx, kd, id, int64(read.Revision))
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return api.NewContentReply(contentDTO(v, false))
	case api.CallContentBody:
		id, e := contentID(body.ID)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		var v kernel.ContentBodyPage
		if operator {
			v, err = daemon.store.ReadContentBody(ctx, id, int(body.Revision), int(body.Offset), int(body.Limit))
		} else {
			v, err = daemon.store.ReadContentBodyForAttempt(ctx, kd, id, int(body.Revision), int(body.Offset), int(body.Limit))
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return api.NewContentReply(api.ContentBody{ID: v.ID.String(), Revision: uint64(v.Revision.Int64()), Offset: uint64(v.Offset), Body: v.Body, NextOffset: uint64(v.NextOffset), Complete: v.Complete})
	case api.CallContentEvidence:
		ib, e := hex.DecodeString(ev.ID)
		if e != nil || len(ib) != 16 {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		i, e := kernel.ContentEvidenceIDFromBytes(ib)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		c, e := contentID(ev.ContentID)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		r, e := revision(ev.ContentRevision)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		spec := kernel.NewContentEvidence{ID: i, ProjectID: pid, ContentID: c, ContentRevision: r, TestedSource: ev.TestedSource, Environment: ev.Environment, Result: ev.Result, Location: ev.Location, Evaluator: ev.Evaluator, Judgment: ev.Judgment}
		var v kernel.ContentEvidence
		if operator {
			v, err = daemon.store.CreateContentEvidence(ctx, spec, at)
		} else {
			v, err = daemon.store.CreateContentEvidenceForAttempt(ctx, kd, spec, at)
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return api.NewContentReply(evidenceDTO(v))
	case api.CallContentAttach:
		t, e := taskID(attach.TaskID)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		c, e := contentID(attach.ContentID)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		r, e := revision(attach.ContentRevision)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		if operator {
			err = daemon.store.AttachContentToTask(ctx, t, pid, c, r, at)
		} else {
			err = daemon.store.AttachContentForAttempt(ctx, kd, c, r, at)
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return api.NewContentReply(struct {
			OK bool `json:"ok"`
		}{true})
	}
	return newErrorReply(api.RemoteInvalidRequest)
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
