package daemon

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/change"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func revision(n uint64) (kernel.Revision, error) { return kernel.NewRevision(int64(n)) }
func contentDTO(v kernel.ContentRevision) api.Content {
	return api.Content{ID: v.ID.String(), ProjectID: v.ProjectID.String(), Kind: string(v.Kind), Title: v.Title, Description: v.Description, Author: v.Author, SourceReferences: v.SourceReferences, ObjectFormat: v.ObjectFormat, Commit: v.Commit, Path: v.Path, Revision: uint64(v.Revision.Int64()), LatestRevision: uint64(v.LatestRevision.Int64()), Deprecated: v.Deprecated}
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
	if repository.ProjectID != spec.ProjectID {
		return kernel.NewContent{}, kernel.ErrUnauthorized
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
	var source change.ContentSource
	if spec.Commit != "" {
		source, err = change.PinContentSource(ctx, git, repository.Root, identity, spec.Commit, spec.Path, ref)
	} else {
		source, err = change.WriteContentSource(ctx, git, repository.Root, identity, parent, path, spec.Body, ref, daemon.gitAuthor(ctx))
	}
	if err != nil {
		return kernel.NewContent{}, err
	}
	spec.RepositoryID = repository.ID
	spec.Body, spec.ObjectFormat, spec.Commit, spec.Path = "", source.Commit.Format().Name(), source.Commit.Hex(), source.Path
	spec.RepositoryDevice, spec.RepositoryInode = int64(identity.Device()), int64(identity.Inode())
	return spec, nil
}

func (daemon *Daemon) readContentSource(ctx context.Context, content kernel.ContentRevision) (string, error) {
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
	body, err := daemon.readContentSource(ctx, content)
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
	attach, _ := call.ContentAttachInput()
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
	pid, err := decodeID(firstNonEmpty(input.ProjectID, list.ProjectID, attach.ProjectID, attachments.ProjectID), kernel.ProjectIDFromBytes)
	projectOptional := call.Kind() == api.CallContentRead || call.Kind() == api.CallContentBody || !operator && (call.Kind() == api.CallContentList || call.Kind() == api.CallContentAttachments)
	if err != nil && !projectOptional {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	var authorTask kernel.TaskID
	if attempt && err == nil {
		authority, authErr := daemon.store.AuthenticateAttempt(ctx, kd)
		if authErr != nil {
			return newErrorReply(remoteErrorCode(authErr))
		}
		if pid != authority.ProjectID {
			return newErrorReply(api.RemoteUnauthorized)
		}
		authorTask = authority.TaskID
	}
	makeSpec := func() (kernel.NewContent, error) {
		id, e := decodeID(input.ID, kernel.ContentIDFromBytes)
		if e != nil {
			return kernel.NewContent{}, e
		}
		p, e := decodeID(input.ProjectID, kernel.ProjectIDFromBytes)
		if e != nil {
			return kernel.NewContent{}, e
		}
		var repository kernel.RepositoryID
		if input.RepositoryID != "" {
			repository, e = decodeID(input.RepositoryID, kernel.RepositoryIDFromBytes)
			if e != nil {
				return kernel.NewContent{}, e
			}
		}
		author := operatorProvenance
		return kernel.NewContent{ID: id, ProjectID: p, RepositoryID: repository, Kind: kernel.ContentKind(input.Kind), Title: input.Title, Description: input.Description, Body: input.Body, Author: author, SourceReferences: input.SourceReferences, Commit: input.Commit, Path: input.Path}, nil
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
				repository, found, readErr := daemon.store.ContentRepository(ctx, existing.ID, existing.Revision)
				if readErr != nil || !found {
					return newErrorReply(api.RemoteInternal)
				}
				spec.RepositoryID = repository.ID
				spec.RepositoryDevice, spec.RepositoryInode = existing.RepositoryDevice, existing.RepositoryInode
			} else if existingErr != kernel.ErrNotFound {
				return newErrorReply(remoteErrorCode(existingErr))
			} else if attempt {
				repository, found, readErr := daemon.store.TaskRepository(ctx, authorTask)
				if readErr != nil {
					return newErrorReply(remoteErrorCode(readErr))
				}
				if found {
					if spec.RepositoryID != (kernel.RepositoryID{}) && spec.RepositoryID != repository.ID {
						return newErrorReply(api.RemoteUnauthorized)
					}
					spec.RepositoryID = repository.ID
				}
			}
			if specErr = daemon.validateKnowledgeWrite(ctx, operator, kd, spec, kernel.Revision{}); specErr != nil {
				return newErrorReply(remoteErrorCode(specErr))
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
			if specErr = daemon.validateKnowledgeWrite(ctx, operator, kd, spec, r); specErr != nil {
				return newErrorReply(remoteErrorCode(specErr))
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
			id, e2 := decodeID(input.ID, kernel.ContentIDFromBytes)
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
			if operator {
				v, e = daemon.store.DeprecateContent(ctx, id, pid, r, operatorProvenance, at)
			} else {
				v, e = daemon.store.DeprecateContentForAttempt(ctx, kd, id, r, at)
			}
		}
		if e != nil {
			return newErrorReply(remoteErrorCode(e))
		}
		return api.NewContentReply(daemon.knowledgeDTO(ctx, v))
	case api.CallContentList:
		kind := kernel.ContentKind(list.Kind)
		limit := int(list.Limit)
		if limit == 0 {
			limit = api.MaxContentPageItems
		}
		var v kernel.ContentPage
		if list.Knowledge {
			query := kernel.KnowledgeQuery{OpenOnly: list.OpenOnly, Kind: kind, Query: list.Query, Branch: list.Branch, Environment: list.Environment, Entity: list.Entity, Thread: list.Thread, Offset: int(list.Offset), Limit: limit}
			if operator {
				repository, e := daemon.knowledgeRepository(ctx, pid, list.RepositoryID)
				if e != nil {
					return newErrorReply(remoteErrorCode(e))
				}
				v, err = daemon.store.SearchKnowledge(ctx, pid, repository, query)
			} else {
				v, err = daemon.store.SearchKnowledgeForAttempt(ctx, kd, query)
			}
		} else if operator {
			v, err = daemon.store.ListContent(ctx, pid, kind, int(list.Offset), limit)
		} else {
			v, err = daemon.store.ListContentForAttempt(ctx, kd, kind, int(list.Offset), limit)
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		out := api.ContentList{NextOffset: uint64(v.NextOffset)}
		for _, x := range v.Items {
			out.Items = append(out.Items, daemon.knowledgeDTO(ctx, x))
		}
		return api.NewContentReply(out)
	case api.CallContentRead:
		id, e := decodeID(read.ID, kernel.ContentIDFromBytes)
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
		return api.NewContentReply(daemon.knowledgeDTO(ctx, v))
	case api.CallContentBody:
		id, e := decodeID(body.ID, kernel.ContentIDFromBytes)
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
		if attempt {
			if err := daemon.store.RecordContentAccessForAttempt(ctx, kd, kernel.ContentAccess{ContentID: v.ID, ContentRevision: v.Revision, Kind: "read", Offset: v.Offset, ByteLength: len(v.Body), CreatedAt: at}); err != nil {
				return newErrorReply(remoteErrorCode(err))
			}
		}
		return api.NewContentReply(api.ContentBody{ID: v.ID.String(), Revision: uint64(v.Revision.Int64()), Offset: uint64(v.Offset), Body: v.Body, NextOffset: uint64(v.NextOffset), Complete: v.Complete})
	case api.CallContentAttach:
		t, e := decodeID(attach.TaskID, kernel.TaskIDFromBytes)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		c, e := decodeID(attach.ContentID, kernel.ContentIDFromBytes)
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
			t, e := decodeID(attachments.TaskID, kernel.TaskIDFromBytes)
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
				target, e = decodeID(attachments.TaskID, kernel.TaskIDFromBytes)
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
