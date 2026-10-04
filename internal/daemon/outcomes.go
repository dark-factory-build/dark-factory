package daemon

import (
	"context"
	"math"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

func outcomeDTO(v kernel.OutcomeRevision) api.Outcome {
	return api.Outcome{ID: v.ID.String(), ProjectID: v.ProjectID.String(), Revision: uint64(v.Revision.Int64()), Document: v.Document, Kind: v.Kind, Objective: v.Objective, Criteria: v.Criteria, State: v.State, Author: v.Author, Authority: v.Authority, ObjectiveWorkRevision: uint64(v.ObjectiveWorkRevision.Int64()), Stale: v.Stale, MissingReferences: v.MissingReferences}
}
func uintInt(n uint64) (int, error) {
	if n > uint64(math.MaxInt) {
		return 0, kernel.ErrInvalidValue
	}
	return int(n), nil
}

func (daemon *Daemon) outcomes(ctx context.Context, call api.Call) api.Reply {
	at, _ := kernel.NewUnixMillis(daemon.now().UnixMilli())
	digest, attempt := call.AttemptDigest()
	var kd kernel.AttemptDigest
	if attempt {
		var err error
		kd, err = attemptDigest(digest)
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
	}
	if call.Kind() == api.CallOutcomeWrite {
		in, _ := call.OutcomeWriteInput()
		id, err := decodeID(in.ID, kernel.OutcomeIDFromBytes)
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		project, err := decodeID(in.ProjectID, kernel.ProjectIDFromBytes)
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		if in.ExpectedRevision > uint64(math.MaxInt64) {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		spec := kernel.NewOutcome{ID: id, ProjectID: project, Document: in.Document}
		var v kernel.OutcomeRevision
		if attempt {
			v, err = daemon.store.WriteOutcomeForAttempt(ctx, kd, spec, int64(in.ExpectedRevision), at)
		} else {
			v, err = daemon.store.WriteOutcome(ctx, spec, int64(in.ExpectedRevision), at)
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return api.NewContentReply(outcomeDTO(v))
	}
	var project kernel.ProjectID
	var err error
	if attempt {
		in, _ := call.OutcomeReadInput()
		if call.Kind() == api.CallOutcomeList {
			l, _ := call.OutcomeListInput()
			if l.ProjectID != "" {
				project, err = decodeID(l.ProjectID, kernel.ProjectIDFromBytes)
			}
		} else if in.ProjectID != "" {
			project, err = decodeID(in.ProjectID, kernel.ProjectIDFromBytes)
		}
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
	} else {
		if call.Kind() == api.CallOutcomeList {
			l, _ := call.OutcomeListInput()
			project, err = decodeID(l.ProjectID, kernel.ProjectIDFromBytes)
		} else {
			in, _ := call.OutcomeReadInput()
			project, err = decodeID(in.ProjectID, kernel.ProjectIDFromBytes)
		}
		if err != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
	}
	if attempt {
		authority, authErr := daemon.store.AuthenticateAttempt(ctx, kd)
		if authErr != nil {
			return newErrorReply(remoteErrorCode(authErr))
		}
		in, _ := call.OutcomeReadInput()
		l, _ := call.OutcomeListInput()
		if (in.ProjectID != "" || l.ProjectID != "") && project != authority.ProjectID {
			return newErrorReply(api.RemoteUnauthorized)
		}
	}
	if call.Kind() == api.CallOutcomeRead {
		in, _ := call.OutcomeReadInput()
		if in.Revision > uint64(math.MaxInt64) {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		id, e := decodeID(in.ID, kernel.OutcomeIDFromBytes)
		if e != nil {
			return newErrorReply(api.RemoteInvalidRequest)
		}
		var v kernel.OutcomeRevision
		if attempt {
			v, err = daemon.store.OutcomeForAttempt(ctx, kd, id, int64(in.Revision))
		} else {
			v, err = daemon.store.Outcome(ctx, project, id, int64(in.Revision))
		}
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		return api.NewContentReply(outcomeDTO(v))
	}
	l, _ := call.OutcomeListInput()
	offset, e := uintInt(l.Offset)
	if e != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	limit, e := uintInt(l.Limit)
	if e != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	if limit == 0 {
		limit = api.MaxContentPageItems
	}
	var page kernel.OutcomePage
	if attempt {
		page, err = daemon.store.ListOutcomesForAttempt(ctx, kd, offset, limit)
	} else {
		page, err = daemon.store.ListOutcomes(ctx, project, offset, limit)
	}
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	out := api.OutcomeList{NextOffset: uint64(page.NextOffset)}
	for _, item := range page.Items {
		out.Items = append(out.Items, outcomeDTO(item))
	}
	return api.NewContentReply(out)
}
