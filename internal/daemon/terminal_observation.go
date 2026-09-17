package daemon

import (
	"bytes"
	"context"
	"regexp"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// Only complete lines are exposed. Starting inside a retained page discards
// its first line, and a partial final line is omitted, so a caller cannot
// bypass credential labels by requesting a cursor inside their value.
var terminalPrivateText = regexp.MustCompile(`(?i)(?:authorization[[:blank:]]*[:=][[:blank:]]*(?:bearer[[:blank:]]+)?|bearer[[:blank:]]+|(?:api[_-]?key|password|token|secret)[[:blank:]]*[:=][[:blank:]]*)[^\r\n]+|(?:/Users/|/home/|/private/|/var/|/tmp/|/Volumes/|/opt/|/usr/local/|~/)[^[:space:]]+`)

func redactTerminalWindow(payload []byte, start uint64) ([]byte, uint64) {
	original := len(payload)
	if start != 0 {
		end := bytes.IndexByte(payload, '\n')
		if end < 0 {
			return nil, uint64(original)
		}
		payload = payload[end+1:]
	}
	end := bytes.LastIndexByte(payload, '\n')
	if end < 0 {
		return nil, uint64(original)
	}
	payload = payload[:end+1]
	result := terminalPrivateText.ReplaceAllFunc(payload, func(match []byte) []byte { return bytes.Repeat([]byte("*"), len(match)) })
	return result, uint64(original - len(payload))
}

func terminalObservationTargetAllowed(authority kernel.AttemptAuthority, projectID kernel.ProjectID, taskID kernel.TaskID, run kernel.Run) bool {
	if projectID != authority.ProjectID || run.ProjectID != projectID || run.TaskID != taskID {
		return false
	}
	switch authority.Role {
	case kernel.RoleWorker:
		return taskID == authority.TaskID && run.ID == authority.RunID
	case kernel.RoleOrchestrator:
		return run.Role == kernel.RoleWorker
	default:
		return false
	}
}

func (daemon *Daemon) terminalObserve(ctx context.Context, call api.Call) api.Reply {
	digest, ok := call.AttemptDigest()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	bearer, err := attemptDigest(digest)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	authority, err := daemon.store.AuthenticateAttempt(ctx, bearer)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	input, ok := call.TerminalObserveInput()
	if !ok {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	project, err := parseProjectID(input.ProjectID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	task, err := parseTaskID(input.TaskID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	runID, err := parseRunID(input.RunID)
	if err != nil {
		return newErrorReply(api.RemoteInvalidRequest)
	}
	if project != authority.ProjectID || (authority.Role != kernel.RoleOrchestrator && (task != authority.TaskID || runID != authority.RunID)) {
		return newErrorReply(api.RemoteForbidden)
	}
	run, found, err := daemon.store.Run(ctx, runID)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if found && !terminalObservationTargetAllowed(authority, project, task, run) {
		return newErrorReply(api.RemoteForbidden)
	}
	if !found {
		return newErrorReply(api.RemoteConflict)
	}
	if run.Phase == kernel.RunTerminal {
		if authority.Role != kernel.RoleOrchestrator || run.Role != kernel.RoleWorker {
			return newErrorReply(api.RemoteForbidden)
		}
		diagnostics, present, err := daemon.store.TerminalDiagnostics(ctx, runID)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		if !present {
			return newErrorReply(api.RemoteNotFound)
		}
		if input.Cursor < diagnostics.Floor {
			result := api.TerminalObservation{ProjectID: input.ProjectID, TaskID: input.TaskID, RunID: input.RunID, Cursor: input.Cursor, NextCursor: diagnostics.Floor, Floor: diagnostics.Floor, Head: diagnostics.Head, Source: "stored", Gap: true, Omitted: diagnostics.Floor - input.Cursor}
			reply, err := api.NewTerminalObservationReply(result)
			if err != nil {
				return newErrorReply(api.RemoteInternal)
			}
			return reply
		}
		if input.Cursor > diagnostics.Head {
			return newErrorReply(api.RemoteConflict)
		}
		remaining := diagnostics.Head - input.Cursor
		if remaining > uint64(input.MaxBytes) {
			remaining = uint64(input.MaxBytes)
		}
		offset := input.Cursor - diagnostics.Floor
		raw := diagnostics.Payload[offset : offset+remaining]
		payload, omitted := redactTerminalWindow(raw, input.Cursor)
		result := api.TerminalObservation{ProjectID: input.ProjectID, TaskID: input.TaskID, RunID: input.RunID, Cursor: input.Cursor, NextCursor: input.Cursor + remaining, Floor: diagnostics.Floor, Head: diagnostics.Head, Source: "stored", Omitted: omitted, Payload: payload}
		reply, err := api.NewTerminalObservationReply(result)
		if err != nil {
			return newErrorReply(api.RemoteInternal)
		}
		return reply
	}
	if run.Phase != kernel.RunRunning {
		return newErrorReply(api.RemoteConflict)
	}
	session, found, err := daemon.store.TerminalSessionForRun(ctx, runID)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if !found {
		return newErrorReply(api.RemoteNotFound)
	}
	attachment, err := daemon.AttachTerminal(ctx, runID, session.ID, run.Revision, session.Revision, input.Cursor)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	defer attachment.Close()
	result := api.TerminalObservation{ProjectID: input.ProjectID, TaskID: input.TaskID, RunID: input.RunID, Cursor: input.Cursor, NextCursor: input.Cursor, Source: "none"}
	raw := make([]byte, 0, input.MaxBytes)
	finish := func() api.Reply {
		result.Payload, result.Omitted = redactTerminalWindow(raw, input.Cursor)
		reply, err := api.NewTerminalObservationReply(result)
		if err != nil {
			return newErrorReply(api.RemoteInternal)
		}
		return reply
	}
	for {
		select {
		case <-ctx.Done():
			return newErrorReply(api.RemoteUnavailable)
		case event, open := <-attachment.Events():
			if !open {
				return newErrorReply(api.RemoteUnavailable)
			}
			switch event.Kind {
			case TerminalEventAttached:
				result.Floor, result.Head = event.Floor, event.Head
				if input.Cursor > event.Head {
					return newErrorReply(api.RemoteConflict)
				}
				if input.Cursor == event.Head {
					return finish()
				}
			case TerminalEventOutput:
				if event.Start != result.NextCursor || event.End < event.Start {
					return newErrorReply(api.RemoteUnavailable)
				}
				count := min(len(event.Payload), int(input.MaxBytes)-len(raw), int(result.Head-result.NextCursor))
				raw = append(raw, event.Payload[:count]...)
				result.NextCursor += uint64(count)
				result.Source = "stored"
				if len(raw) == int(input.MaxBytes) || result.NextCursor == result.Head {
					return finish()
				}
			case TerminalEventReset:
				if input.Cursor > event.Head {
					return newErrorReply(api.RemoteConflict)
				}
				result.Gap, result.Floor, result.Head = true, event.Floor, event.Head
				result.NextCursor = max(input.Cursor, event.Floor)
				result.Omitted = result.NextCursor - input.Cursor
				reply, err := api.NewTerminalObservationReply(result)
				if err != nil {
					return newErrorReply(api.RemoteInternal)
				}
				return reply
			case TerminalEventPTYEOF, TerminalEventExit:
				return finish()
			}
		}
	}
}
