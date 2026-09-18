package daemon

import (
	"context"
	"encoding/hex"
	"fmt"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/change"
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
func contentDTO(v kernel.ContentRevision) api.Content {
	return api.Content{ID: v.ID.String(), ProjectID: v.ProjectID.String(), Kind: string(v.Kind), Title: v.Title, Description: v.Description, Author: v.Author, SourceReferences: v.SourceReferences, ObjectFormat: v.ObjectFormat, Commit: v.Commit, Path: v.Path, Revision: uint64(v.Revision.Int64()), LatestRevision: uint64(v.LatestRevision.Int64()), Deprecated: v.Deprecated}
}
func evidenceDTO(v kernel.ContentEvidence) api.ContentEvidence {
	return api.ContentEvidence{ID: v.ID.String(), ProjectID: v.ProjectID.String(), ContentID: v.ContentID.String(), ContentRevision: uint64(v.ContentRevision.Int64()), TestedSource: v.TestedSource, Environment: v.Environment, Result: v.Result, Location: v.Location, Evaluator: v.Evaluator, Judgment: v.Judgment}
}
func attachmentDTO(v kernel.TaskContentReference) api.ContentAttachment {
	return api.ContentAttachment{TaskID: v.TaskID.String(), ProjectID: v.ProjectID.String(), TaskWorkRevision: uint64(v.TaskWorkRevision.Int64()), ContentID: v.ContentID.String(), ContentRevision: uint64(v.ContentRevision.Int64()), AttachedAtMs: uint64(v.AttachedAt.Int64())}
}

func (daemon *Daemon) writeContentSource(ctx context.Context, spec kernel.NewContent, revision uint64) (kernel.NewContent, error) {
	var repository kernel.ProjectRepository
	var found bool
	var err error
	switch {
	case spec.RepositoryID != (kernel.RepositoryID{}):
		repository, found, err = daemon.store.ProjectRepository(ctx, spec.RepositoryID)
	case revision > 1:
		previousRevision, revisionErr := kernel.NewRevision(int64(revision - 1))
		if revisionErr != nil {
			return kernel.NewContent{}, revisionErr
		}
		repository, found, err = daemon.store.ContentRepository(ctx, spec.ID, previousRevision)
	default:
		repository, found, err = daemon.store.DefaultProjectRepository(ctx, spec.ProjectID)
	}
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrNotFound
		}
		return kernel.NewContent{}, err
	}
	git := change.TrustedGitExecutable
	if configured := daemon.gitExecutable.Load(); configured != nil && *configured != "" {
		git = *configured
	}
	identity, err := inspectRepositoryIdentity(repository.Root)
	if err != nil {
		return kernel.NewContent{}, err
	}
	if spec.RepositoryInode > 0 {
		identity, err = change.NewRepositoryIdentity(uint64(spec.RepositoryDevice), uint64(spec.RepositoryInode))
		if err != nil {
			return kernel.NewContent{}, err
		}
	}
	ref := fmt.Sprintf("refs/dark-factory/content/%s/%d", spec.ID, revision)
	path := fmt.Sprintf(".dark-factory/content/%s.md", spec.ID)
	var parent *change.ContentSource
	if revision > 1 {
		previous, readErr := daemon.store.Content(ctx, spec.ID, int64(revision-1))
		if readErr != nil {
			return kernel.NewContent{}, readErr
		}
		if previous.Commit == "" {
			previous, readErr = daemon.exportLegacyContent(ctx, previous.ID, previous.Revision.Int64())
			if readErr != nil {
				return kernel.NewContent{}, readErr
			}
		}
		if previous.Commit != "" {
			repository, found, readErr = daemon.store.ContentRepository(ctx, previous.ID, previous.Revision)
			if readErr != nil || !found {
				if readErr == nil {
					readErr = kernel.ErrCorruptState
				}
				return kernel.NewContent{}, readErr
			}
			identity, readErr = change.NewRepositoryIdentity(uint64(previous.RepositoryDevice), uint64(previous.RepositoryInode))
			if readErr != nil {
				return kernel.NewContent{}, readErr
			}
			id, idErr := change.NewObjectIDFromHex(previous.ObjectFormat, previous.Commit)
			if idErr != nil {
				return kernel.NewContent{}, idErr
			}
			parent = &change.ContentSource{Commit: id, Path: previous.Path}
		}
	}
	var source change.ContentSource
	if spec.Commit != "" {
		source, err = change.PinContentSource(ctx, git, repository.Root, identity, spec.Commit, spec.Path, ref)
	} else {
		source, err = change.WriteContentSource(ctx, git, repository.Root, identity, parent, path, spec.Body, ref)
	}
	if err != nil {
		return kernel.NewContent{}, err
	}
	spec.RepositoryID = repository.ID
	spec.Body, spec.ObjectFormat, spec.Commit, spec.Path = "", source.Commit.Format().Name(), source.Commit.Hex(), source.Path
	spec.RepositoryDevice, spec.RepositoryInode = int64(identity.Device()), int64(identity.Inode())
	return spec, nil
}

func (daemon *Daemon) exportLegacyContent(ctx context.Context, id kernel.ContentID, revision int64) (kernel.ContentRevision, error) {
	legacy, body, err := daemon.store.LegacyContent(ctx, id, revision)
	if err != nil {
		if err == kernel.ErrConflict {
			return daemon.store.Content(ctx, id, revision)
		}
		return kernel.ContentRevision{}, err
	}
	repository, found, err := daemon.store.ContentRepository(ctx, legacy.ID, legacy.Revision)
	if err != nil {
		return kernel.ContentRevision{}, err
	}
	if !found {
		return kernel.ContentRevision{}, kernel.ErrCorruptState
	}
	spec := kernel.NewContent{ID: legacy.ID, ProjectID: legacy.ProjectID, RepositoryID: repository.ID, Body: body}
	pinned, err := daemon.writeContentSource(ctx, spec, uint64(revision))
	if err != nil {
		return kernel.ContentRevision{}, err
	}
	if err := daemon.store.CompleteContentExport(ctx, id, revision, body, pinned.ObjectFormat, pinned.Commit, pinned.Path, pinned.RepositoryID, pinned.RepositoryDevice, pinned.RepositoryInode); err != nil {
		return kernel.ContentRevision{}, err
	}
	return daemon.store.Content(ctx, id, revision)
}

func (daemon *Daemon) readContentSource(ctx context.Context, content kernel.ContentRevision) (string, error) {
	if content.Commit == "" {
		return "", kernel.ErrConflict
	}
	repository, found, err := daemon.store.ContentRepository(ctx, content.ID, content.Revision)
	if err != nil || !found {
		if err == nil {
			err = kernel.ErrNotFound
		}
		return "", err
	}
	git := change.TrustedGitExecutable
	if configured := daemon.gitExecutable.Load(); configured != nil && *configured != "" {
		git = *configured
	}
	identity, err := change.NewRepositoryIdentity(uint64(content.RepositoryDevice), uint64(content.RepositoryInode))
	if err != nil {
		return "", err
	}
	id, err := change.NewObjectIDFromHex(content.ObjectFormat, content.Commit)
	if err != nil {
		return "", err
	}
	return change.ReadContentSource(ctx, git, repository.Root, identity, change.ContentSource{Commit: id, Path: content.Path})
}

func (daemon *Daemon) contentBodySource(ctx context.Context, content kernel.ContentRevision) (kernel.ContentRevision, string, error) {
	if content.Commit != "" {
		body, err := daemon.readContentSource(ctx, content)
		return content, body, err
	}
	_, body, err := daemon.store.LegacyContent(ctx, content.ID, content.Revision.Int64())
	if err != nil {
		return content, "", err
	}
	if migrated, exportErr := daemon.exportLegacyContent(ctx, content.ID, content.Revision.Int64()); exportErr == nil {
		content = migrated
		body, err = daemon.readContentSource(ctx, content)
	}
	return content, body, err
}

func (daemon *Daemon) content(ctx context.Context, call api.Call) api.Reply {
	if call.Kind() == api.CallContentCreate || call.Kind() == api.CallContentRevise || call.Kind() == api.CallContentDeprecate || call.Kind() == api.CallContentBody {
		daemon.operationMu.Lock()
		defer daemon.operationMu.Unlock()
	}
	at, _ := kernel.NewUnixMillis(daemon.now().UnixMilli())
	input, _ := call.ContentInput()
	list, _ := call.ContentListInput()
	read, _ := call.ContentReadInput()
	body, _ := call.ContentBodyInput()
	ev, _ := call.ContentEvidenceInput()
	attach, _ := call.ContentAttachInput()
	evList, _ := call.ContentEvidenceListInput()
	attachments, _ := call.ContentAttachmentsInput()
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
	// Provenance is authority-derived, never a caller-supplied identity.
	const operatorProvenance = "operator:local"
	pid, err := projectID(firstNonEmpty(input.ProjectID, list.ProjectID, ev.ProjectID, attach.ProjectID, evList.ProjectID, attachments.ProjectID))
	projectOptional := call.Kind() == api.CallContentRead || call.Kind() == api.CallContentBody || !operator && (call.Kind() == api.CallContentList || call.Kind() == api.CallContentEvidenceList || call.Kind() == api.CallContentAttachments)
	if err != nil && !projectOptional {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	if attempt && err == nil {
		authority, authErr := daemon.store.AuthenticateAttempt(ctx, kd)
		if authErr != nil {
			return newErrorReply(remoteErrorCode(authErr))
		}
		if pid != authority.ProjectID {
			return newErrorReply(api.RemoteUnauthorized)
		}
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
		author := operatorProvenance
		return kernel.NewContent{ID: id, ProjectID: p, Kind: kernel.ContentKind(input.Kind), Title: input.Title, Description: input.Description, Body: input.Body, Author: author, SourceReferences: input.SourceReferences, Commit: input.Commit, Path: input.Path}, nil
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
			if existing, existingErr := daemon.contentForCaller(ctx, operator, kd, spec.ID, 1); existingErr == nil {
				if existing.ProjectID != spec.ProjectID || existing.LatestRevision.Int64() != 1 {
					return newErrorReply(api.RemoteRevisionConflict)
				}
				spec.RepositoryDevice, spec.RepositoryInode = existing.RepositoryDevice, existing.RepositoryInode
			} else if existingErr != kernel.ErrNotFound {
				return newErrorReply(remoteErrorCode(existingErr))
			}
			spec, specErr = daemon.writeContentSource(ctx, spec, 1)
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
			current, currentErr := daemon.contentForCaller(ctx, operator, kd, spec.ID, int64(input.ExpectedRevision))
			if currentErr != nil {
				return newErrorReply(remoteErrorCode(currentErr))
			}
			if current.ProjectID != spec.ProjectID {
				return newErrorReply(api.RemoteRevisionConflict)
			}
			if latest := current.LatestRevision.Int64(); latest != int64(input.ExpectedRevision) && latest != int64(input.ExpectedRevision)+1 {
				return newErrorReply(api.RemoteRevisionConflict)
			}
			spec, specErr = daemon.writeContentSource(ctx, spec, input.ExpectedRevision+1)
			if specErr != nil {
				return newErrorReply(remoteErrorCode(specErr))
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
			current, currentErr := daemon.contentForCaller(ctx, operator, kd, id, int64(input.ExpectedRevision))
			if currentErr != nil {
				return newErrorReply(remoteErrorCode(currentErr))
			}
			if current.ProjectID != pid || current.LatestRevision.Int64() != int64(input.ExpectedRevision) {
				return newErrorReply(api.RemoteRevisionConflict)
			}
			if current.Commit == "" {
				if _, exportErr := daemon.exportLegacyContent(ctx, id, int64(input.ExpectedRevision)); exportErr != nil {
					// The Store's deprecation path preserves a legacy body revision;
					// a later authenticated read retries its Git export.
				}
			}
			if operator {
				v, e = daemon.store.DeprecateContent(ctx, id, pid, r, operatorProvenance, at)
			} else {
				v, e = daemon.store.DeprecateContentForAttempt(ctx, kd, id, r, at)
			}
		}
		if e != nil {
			return newErrorReply(remoteErrorCode(e))
		}
		return api.NewContentReply(contentDTO(v))
	case api.CallContentList:
		kind := kernel.ContentKind(list.Kind)
		limit := int(list.Limit)
		if limit == 0 {
			limit = api.MaxContentPageItems
		}
		var v kernel.ContentPage
		if operator {
			v, err = daemon.store.ListContent(ctx, pid, kind, int(list.Offset), limit)
		} else {
			v, err = daemon.store.ListContentForAttempt(ctx, kd, kind, int(list.Offset), limit)
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		out := api.ContentList{NextOffset: uint64(v.NextOffset)}
		for _, x := range v.Items {
			out.Items = append(out.Items, contentDTO(x))
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
		return api.NewContentReply(contentDTO(v))
	case api.CallContentBody:
		id, e := contentID(body.ID)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		var content kernel.ContentRevision
		if operator {
			content, err = daemon.store.Content(ctx, id, int64(body.Revision))
		} else {
			content, err = daemon.store.ContentForAttempt(ctx, kd, id, int64(body.Revision))
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		content, sourceBody, err := daemon.contentBodySource(ctx, content)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		v, err := pageContentBody(content, sourceBody, int(body.Offset), int(body.Limit))
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
		evaluator := operatorProvenance
		spec := kernel.NewContentEvidence{ID: i, ProjectID: pid, ContentID: c, ContentRevision: r, TestedSource: ev.TestedSource, Environment: ev.Environment, Result: ev.Result, Location: ev.Location, Evaluator: evaluator, Judgment: ev.Judgment}
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
	case api.CallContentEvidenceList:
		c, e := contentID(evList.ContentID)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		r, e := revision(evList.ContentRevision)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		limit := int(evList.Limit)
		if limit == 0 {
			limit = api.MaxContentPageItems
		}
		var page kernel.ContentEvidencePage
		if operator {
			page, err = daemon.store.ListContentEvidence(ctx, pid, c, r, int(evList.Offset), limit)
		} else {
			page, err = daemon.store.ListContentEvidenceForAttempt(ctx, kd, c, r, int(evList.Offset), limit)
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		out := api.ContentEvidenceList{NextOffset: uint64(page.NextOffset)}
		for _, item := range page.Items {
			out.Items = append(out.Items, evidenceDTO(item))
		}
		return api.NewContentReply(out)
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
			err = daemon.store.AttachContentForOrchestrator(ctx, kd, t, c, r, at)
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return api.NewContentReply(struct {
			OK bool `json:"ok"`
		}{true})
	case api.CallContentAttachments:
		var refs []kernel.TaskContentReference
		if operator {
			t, e := taskID(attachments.TaskID)
			if e != nil {
				return newErrorReply(api.RemoteInvalidRequest)
			}
			r, e := revision(attachments.TaskWorkRevision)
			if e != nil {
				return newErrorReply(api.RemoteInvalidRequest)
			}
			refs, err = daemon.store.TaskContentReferences(ctx, pid, t, r)
		} else {
			r, e := revision(attachments.TaskWorkRevision)
			if e != nil {
				return newErrorReply(api.RemoteInvalidRequest)
			}
			var target kernel.TaskID
			if attachments.TaskID != "" {
				var e error
				target, e = taskID(attachments.TaskID)
				if e != nil {
					return newErrorReply(api.RemoteInvalidRequest)
				}
			}
			refs, err = daemon.store.TaskContentReferencesForAttempt(ctx, kd, target, r)
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		out := api.ContentAttachments{}
		for _, ref := range refs {
			out.Items = append(out.Items, attachmentDTO(ref))
		}
		return api.NewContentReply(out)
	}
	return newErrorReply(api.RemoteInvalidRequest)
}

func (daemon *Daemon) contentForCaller(ctx context.Context, operator bool, digest kernel.AttemptDigest, id kernel.ContentID, revision int64) (kernel.ContentRevision, error) {
	if operator {
		return daemon.store.Content(ctx, id, revision)
	}
	return daemon.store.ContentForAttempt(ctx, digest, id, revision)
}

func pageContentBody(content kernel.ContentRevision, sourceBody string, offset, limit int) (kernel.ContentBodyPage, error) {
	if offset < 0 || limit <= 0 || limit > 64*1024 || offset > len(sourceBody) || (offset < len(sourceBody) && !utf8.RuneStart(sourceBody[offset])) {
		return kernel.ContentBodyPage{}, kernel.ErrInvalidValue
	}
	end := offset + limit
	if end > len(sourceBody) {
		end = len(sourceBody)
	}
	for end > offset && end < len(sourceBody) && !utf8.RuneStart(sourceBody[end]) {
		end--
	}
	if end == offset && end < len(sourceBody) {
		return kernel.ContentBodyPage{}, kernel.ErrInvalidValue
	}
	next := 0
	if end < len(sourceBody) {
		next = end
	}
	return kernel.ContentBodyPage{ID: content.ID, Revision: content.Revision, Offset: offset, Body: sourceBody[offset:end], NextOffset: next, Complete: next == 0}, nil
}
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
