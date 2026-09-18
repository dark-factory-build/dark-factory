package daemon

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

// Only complete lines are exposed. Starting inside a retained page discards
// its first line, and a partial final line is omitted, so a caller cannot
// bypass credential labels by requesting a cursor inside their value.
var terminalPrivateText = regexp.MustCompile(`(?i)(?:authorization[[:blank:]]*[:=][[:blank:]]*(?:bearer[[:blank:]]+)?|bearer[[:blank:]]+|(?:api[_-]?key|password|token|secret)["']?[[:blank:]]*[:=][[:blank:]]*["']?)[^\r\n]+|(?:/Users/|/home/|/private/|/var/|/tmp/|/Volumes/|/opt/|/usr/local/|~/)[^[:space:]]+`)

const terminalJSONStringAtom = `(?:\\.|[^"\\])`

var terminalJSONSecret = regexp.MustCompile(`(?is)"(?:api[_-]?key|password|token|secret)"[[:space:]]*:[[:space:]]*"` + terminalJSONStringAtom + `{0,512}"`)
var terminalJSONPrivatePath = regexp.MustCompile(`(?is)"` + terminalJSONStringAtom + `{0,128}"[[:space:]]*:[[:space:]]*"(?:/Users/|/home/|/private/|/var/|/tmp/|/Volumes/|/opt/|/usr/local/|~/)` + terminalJSONStringAtom + `{0,512}"`)
var terminalJSONSecretKey = regexp.MustCompile(`(?i)^[[:space:]\{"']*(?:api[_-]?key|password|assword|ssword|sword|secret|ecret|cret|token|oken|ken|en|n)["']?[[:space:]]*:`)
var terminalJSONAmbiguousSecretKey = regexp.MustCompile(`(?i)^[[:space:]\{"']*n["']?[[:space:]]*:`)
var terminalJSONPathStart = regexp.MustCompile(`(?i)"[^"]{0,128}"[[:space:]]*:[[:space:]]*"(?:/Users/|/home/|/private/|/var/|/tmp/|/Volumes/|/opt/|/usr/local/|~/)`)
var terminalJSONOrphanValue = regexp.MustCompile(`(?m)^[^:\r\n]{1,512}"[[:space:]]*[},]`)
var terminalJSONOrphanSecret = regexp.MustCompile(`(?im)^[^:\r\n]{0,512}(?:secret|token|password|bearer|api[_-]?key)[^:\r\n]{0,512}"[[:space:]]*[},]`)
var terminalJSONOrphanPath = regexp.MustCompile(`(?im)^[[:space:]]*(?:\.|/Users/|/home/|/private/|/var/|/tmp/|/Volumes/|/opt/|/usr/local/|~/)[^:\r\n]{0,512}"[[:space:]]*[},]`)

type terminalLookbehind struct {
	start uint64
	bytes []byte
}

func redactTerminalText(payload []byte) []byte {
	redact := func(match []byte) []byte { return bytes.Repeat([]byte("*"), len(match)) }
	result := terminalJSONSecret.ReplaceAllFunc(payload, redact)
	result = terminalJSONPrivatePath.ReplaceAllFunc(result, redact)
	return terminalPrivateText.ReplaceAllFunc(result, redact)
}

// terminalTextProjection turns one complete retained diagnostic snapshot into
// inert text. Escape controls are removed before redaction, never interpreted
// as record boundaries: a credential split by a cursor movement remains one
// credential to the redactor.
func terminalTextProjection(payload []byte, droppedPrefix bool, limit int) string {
	if droppedPrefix {
		// The retained floor may be inside a multi-line credential or private
		// path. No later terminal control or line end proves that record ended.
		return ""
	}
	text := make([]byte, 0, len(payload))
	space := func() {
		if len(text) != 0 && text[len(text)-1] != ' ' {
			text = append(text, ' ')
		}
	}
	for index := 0; index < len(payload); {
		if payload[index] == 0x1b {
			index = terminalEscapeEnd(payload, index)
			continue
		}
		if payload[index] == 0x9b {
			index = terminalCSIEnd(payload, index+1)
			continue
		}
		if payload[index] == 0x90 || payload[index] == 0x98 || payload[index] == 0x9d || payload[index] == 0x9e || payload[index] == 0x9f {
			index = terminalStringEnd(payload, index+1, payload[index] == 0x9d)
			continue
		}
		if payload[index] < 0x20 || payload[index] == 0x7f || payload[index] >= 0x80 && payload[index] <= 0x9f {
			index++
			space()
			continue
		}
		value, width := utf8.DecodeRune(payload[index:])
		if value == utf8.RuneError && width == 1 {
			index++
			space()
			continue
		}
		if value < 0x20 || value >= 0x7f && value <= 0x9f {
			index += width
			space()
			continue
		}
		text = append(text, payload[index:index+width]...)
		index += width
	}
	text = bytes.TrimSpace(redactTerminalText(text))
	if len(text) > limit {
		text = text[len(text)-limit:]
		for len(text) != 0 && !utf8.Valid(text) {
			text = text[1:]
		}
	}
	return string(text)
}

func terminalEscapeEnd(payload []byte, start int) int {
	if start+1 >= len(payload) {
		return len(payload)
	}
	next := payload[start+1]
	if next == '[' { // CSI: parameters/intermediates followed by one final byte.
		return terminalCSIEnd(payload, start+2)
	}
	if strings.ContainsRune("]PX^_", rune(next)) { // OSC or a string terminated by ST.
		return terminalStringEnd(payload, start+2, next == ']')
	}
	index := start + 1
	for index < len(payload) && payload[index] >= 0x20 && payload[index] <= 0x2f {
		index++
	}
	if index < len(payload) {
		index++
	}
	return index
}

func terminalCSIEnd(payload []byte, start int) int {
	for index := start; index < len(payload); index++ {
		if payload[index] >= 0x40 && payload[index] <= 0x7e {
			return index + 1
		}
	}
	return len(payload)
}

func terminalStringEnd(payload []byte, start int, bellTerminated bool) int {
	for index := start; index < len(payload); index++ {
		if bellTerminated && payload[index] == 0x07 || payload[index] == 0x9c {
			return index + 1
		}
		if payload[index] == 0x1b && index+1 < len(payload) && payload[index+1] == '\\' {
			return index + 2
		}
	}
	return len(payload)
}

func redactTerminalWindow(payload []byte, start uint64, lookbehind ...terminalLookbehind) ([]byte, uint64) {
	original := len(payload)
	if start == 0 && len(lookbehind) != 0 && len(lookbehind[0].bytes) != 0 {
		prefixLen := len(lookbehind[0].bytes)
		end := bytes.LastIndexByte(payload, '\n')
		if end < 0 {
			return nil, uint64(original)
		}
		combined := append(append([]byte(nil), lookbehind[0].bytes...), payload[:end+1]...)
		redact := func(match []byte) []byte { return bytes.Repeat([]byte("*"), len(match)) }
		result := terminalJSONSecret.ReplaceAllFunc(combined, redact)
		result = terminalJSONPrivatePath.ReplaceAllFunc(result, redact)
		result = terminalPrivateText.ReplaceAllFunc(result, redact)
		return result[prefixLen:], uint64(original - (end + 1))
	}
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
	if !found {
		return newErrorReply(api.RemoteConflict)
	}
	if run.Phase == kernel.RunTerminal {
		if authority.Role != kernel.RoleOrchestrator || run.Role != kernel.RoleWorker {
			return newErrorReply(api.RemoteForbidden)
		}
		return daemon.readStoredTerminalObservation(ctx, input, run.ID)
	}
	if run.Phase != kernel.RunRunning {
		return newErrorReply(api.RemoteConflict)
	}
	return daemon.readTerminalObservation(ctx, input, run)
}

func (daemon *Daemon) operatorTerminalObserve(ctx context.Context, call api.Call) api.Reply {
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
	run, found, err := daemon.store.Run(ctx, runID)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if !found || run.ProjectID != project || run.TaskID != task {
		return newErrorReply(api.RemoteForbidden)
	}
	if input.Text {
		return daemon.readTerminalTextObservation(ctx, input, run)
	}
	if run.Phase == kernel.RunTerminal {
		return daemon.readStoredTerminalObservation(ctx, input, run.ID)
	}
	if run.Phase != kernel.RunRunning {
		return newErrorReply(api.RemoteConflict)
	}
	return daemon.readTerminalObservation(ctx, input, run)
}

func (daemon *Daemon) readTerminalTextObservation(ctx context.Context, input api.TerminalObserveInput, run kernel.Run) api.Reply {
	var floor, head uint64
	var payload []byte
	source := "live"
	if run.Phase == kernel.RunTerminal {
		diagnostics, present, err := daemon.store.TerminalDiagnostics(ctx, run.ID)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		if !present {
			return newErrorReply(api.RemoteNotFound)
		}
		floor, head, payload, source = diagnostics.Floor, diagnostics.Head, diagnostics.Payload, "stored"
	} else if run.Phase == kernel.RunRunning {
		session, found, err := daemon.store.TerminalSessionForRun(ctx, run.ID)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		if !found {
			return newErrorReply(api.RemoteNotFound)
		}
		attempt, err := daemon.liveTerminalAttempt(run.ID, session.ID)
		if err != nil {
			return newErrorReply(remoteErrorCode(err))
		}
		floor, head, payload = attempt.diagnosticSnapshot()
	} else {
		return newErrorReply(api.RemoteConflict)
	}
	result := api.TerminalObservation{ProjectID: input.ProjectID, TaskID: input.TaskID, RunID: input.RunID, Floor: floor, Head: head, Source: source, TextMode: true, Text: terminalTextProjection(payload, floor != 0, int(input.MaxBytes))}
	reply, err := api.NewTerminalObservationReply(result)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) readStoredTerminalObservation(ctx context.Context, input api.TerminalObserveInput, runID kernel.RunID) api.Reply {
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
	remaining := min(uint64(input.MaxBytes), diagnostics.Head-input.Cursor)
	offset := input.Cursor - diagnostics.Floor
	if offset > uint64(len(diagnostics.Payload)) || remaining > uint64(len(diagnostics.Payload))-offset {
		return newErrorReply(api.RemoteInternal)
	}
	raw := diagnostics.Payload[offset : offset+remaining]
	var payload []byte
	var omitted uint64
	if offset == 0 {
		payload, omitted = redactTerminalWindow(raw, input.Cursor)
	} else {
		contextOffset := offset - min(offset, uint64(512))
		payload, omitted = redactTerminalWindow(raw, input.Cursor, terminalLookbehind{start: diagnostics.Floor + contextOffset, bytes: diagnostics.Payload[contextOffset:offset]})
	}
	result := api.TerminalObservation{ProjectID: input.ProjectID, TaskID: input.TaskID, RunID: input.RunID, Cursor: input.Cursor, NextCursor: input.Cursor + remaining, Floor: diagnostics.Floor, Head: diagnostics.Head, Source: "stored", Omitted: omitted, Payload: payload}
	reply, err := api.NewTerminalObservationReply(result)
	if err != nil {
		return newErrorReply(api.RemoteInternal)
	}
	return reply
}

func (daemon *Daemon) readTerminalObservation(ctx context.Context, input api.TerminalObserveInput, run kernel.Run) api.Reply {
	session, found, err := daemon.store.TerminalSessionForRun(ctx, run.ID)
	if err != nil {
		return newErrorReply(remoteErrorCode(err))
	}
	if !found {
		return newErrorReply(api.RemoteNotFound)
	}
	attachment, err := daemon.AttachTerminal(ctx, run.ID, session.ID, run.Revision, session.Revision, input.Cursor)
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
