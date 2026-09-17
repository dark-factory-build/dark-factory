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
var terminalPrivateText = regexp.MustCompile(`(?i)(?:authorization[[:blank:]]*[:=][[:blank:]]*(?:bearer[[:blank:]]+)?|bearer[[:blank:]]+|(?:api[_-]?key|password|token|secret)["']?[[:blank:]]*[:=][[:blank:]]*["']?)[^\r\n]+|(?:/Users/|/home/|/private/|~/)[^[:space:]]+`)
var terminalJSONSecret = regexp.MustCompile(`(?is)"(?:api[_-]?key|password|token|secret)"[[:space:]]*:[[:space:]]*"[^"]{0,512}"`)
var terminalJSONPrivatePath = regexp.MustCompile(`(?is)"[^"]{0,128}"[[:space:]]*:[[:space:]]*"(?:/Users/|/home/|/private/|~/)[^"]{0,512}"`)
var terminalJSONSecretKey = regexp.MustCompile(`(?i)^[[:space:]\{"']*(?:api[_-]?key|password|assword|ssword|sword|secret|ecret|cret|token|oken|ken|en|n)["']?[[:space:]]*:`)
var terminalJSONAmbiguousSecretKey = regexp.MustCompile(`(?i)^[[:space:]\{"']*n["']?[[:space:]]*:`)
var terminalJSONPathStart = regexp.MustCompile(`(?i)"[^"]{0,128}"[[:space:]]*:[[:space:]]*"(?:/Users/|/home/|/private/|~/)`)
var terminalJSONOrphanValue = regexp.MustCompile(`(?m)^[^:\r\n]{1,512}"[[:space:]]*[},]`)
var terminalJSONOrphanSecret = regexp.MustCompile(`(?im)^[^:\r\n]{0,512}(?:secret|token|password|bearer|api[_-]?key)[^:\r\n]{0,512}"[[:space:]]*[},]`)
var terminalJSONOrphanPath = regexp.MustCompile(`(?im)^[[:space:]]*(?:\.|/Users/|/home/|/private/|~/)[^:\r\n]{0,512}"[[:space:]]*[},]`)

type terminalLookbehind struct {
	start uint64
	bytes []byte
}

func redactTerminalWindow(payload []byte, start uint64, lookbehind ...terminalLookbehind) ([]byte, uint64) {
	original := len(payload)
	var droppedKeyLine []byte
	if start != 0 {
		end := bytes.IndexByte(payload, '\n')
		prefixLen := 0
		if len(lookbehind) != 0 && lookbehind[0].start+uint64(len(lookbehind[0].bytes)) == start {
			combined := append(append([]byte(nil), lookbehind[0].bytes...), payload...)
			lineStart := bytes.LastIndexByte(combined[:len(lookbehind[0].bytes)], '\n') + 1
			if combinedEnd := bytes.IndexByte(combined[len(lookbehind[0].bytes):], '\n'); combinedEnd >= 0 {
				combinedEnd += len(lookbehind[0].bytes)
				prefixLen = len(lookbehind[0].bytes)
				end = combinedEnd - prefixLen
				droppedKeyLine = combined[lineStart:combinedEnd]
			}
		}
		if end < 0 {
			return nil, uint64(original)
		}
		if len(droppedKeyLine) == 0 {
			droppedKeyLine = payload[:end]
		}
		payload = payload[end+1:]
	}
	end := bytes.LastIndexByte(payload, '\n')
	if end < 0 {
		return nil, uint64(original)
	}
	payload = payload[:end+1]
	redact := func(match []byte) []byte { return bytes.Repeat([]byte("*"), len(match)) }
	result := terminalJSONSecret.ReplaceAllFunc(payload, redact)
	result = terminalJSONPrivatePath.ReplaceAllFunc(result, redact)
	if start != 0 {
		// A cursor can begin on the value line after a JSON key was omitted.
		// Hide that orphaned quoted fragment so split credentials and paths
		// cannot be recovered by choosing a later cursor.
		result = terminalJSONOrphanSecret.ReplaceAllFunc(result, redact)
		result = terminalJSONOrphanPath.ReplaceAllFunc(result, redact)
		fullLookbehind := len(lookbehind) != 0 && lookbehind[0].start+uint64(len(lookbehind[0].bytes)) == start
		if terminalJSONSecretKey.Match(droppedKeyLine) || terminalJSONPathStart.Match(droppedKeyLine) || (!fullLookbehind && terminalJSONAmbiguousSecretKey.Match(droppedKeyLine)) {
			result = terminalJSONOrphanValue.ReplaceAllFunc(result, redact)
		}
	}
	result = terminalPrivateText.ReplaceAllFunc(result, redact)
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
	if !found || run.Phase != kernel.RunRunning {
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
	var attachedHead uint64
	var lookbehind terminalLookbehind
	finish := func() api.Reply {
		redactionStart := input.Cursor
		if result.Source == "live" && input.Cursor == attachedHead {
			redactionStart = 0
		}
		if len(lookbehind.bytes) != 0 {
			result.Payload, result.Omitted = redactTerminalWindow(raw, redactionStart, lookbehind)
		} else {
			result.Payload, result.Omitted = redactTerminalWindow(raw, redactionStart)
		}
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
				result.Floor, result.Head, attachedHead = event.Floor, event.Head, event.Head
				lookbehind = terminalLookbehind{start: event.ContextStart, bytes: append([]byte(nil), event.Context...)}
				if input.Cursor > event.Head {
					// A rejected attach reports the current head before the
					// attachment closes. Rebase a future cursor to that head so
					// the caller receives a bounded, explicit reset observation.
					result.Cursor, result.NextCursor, result.Gap = event.Head, event.Head, true
					return finish()
				}
			case TerminalEventOutput:
				if event.Start != result.NextCursor || event.End < event.Start {
					return newErrorReply(api.RemoteUnavailable)
				}
				live := event.Start >= attachedHead
				if live {
					result.Source = "live"
					if event.End > result.Head {
						result.Head = event.End
					}
				} else {
					result.Source = "stored"
				}
				count := min(len(event.Payload), int(input.MaxBytes)-len(raw), int(event.End-result.NextCursor))
				raw = append(raw, event.Payload[:count]...)
				result.NextCursor += uint64(count)
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
