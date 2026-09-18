package main

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/dark-factory-build/dark-factory/internal/api"
	"github.com/dark-factory-build/dark-factory/internal/kernel"
)

type outcomeClient interface {
	OutcomeWrite(context.Context, api.OutcomeWriteInput) (api.Outcome, error)
	OutcomeRead(context.Context, api.OutcomeReadInput) (api.Outcome, error)
	OutcomeList(context.Context, api.OutcomeListInput) (api.OutcomeList, error)
}

func parseOutcome(args []string) (attemptCommand, bool, bool) {
	start := 0
	if args[0] == "attempt" {
		start = 1
	}
	if len(args) < start+2 || args[start] != "outcome" {
		return attemptCommand{}, false, false
	}
	c := attemptCommand{}
	switch args[start+1] {
	case "write":
		c.kind = commandOutcomeWrite
	case "read":
		c.kind = commandOutcomeRead
	case "list":
		c.kind = commandOutcomeList
	default:
		return attemptCommand{}, false, false
	}
	seen := map[string]bool{}
	for i := start + 2; i < len(args); i += 2 {
		if i+1 >= len(args) || seen[args[i]] {
			return attemptCommand{}, false, false
		}
		seen[args[i]] = true
		n, v := args[i], args[i+1]
		switch n {
		case "--id":
			if !validHumanRequestKey(v) {
				return attemptCommand{}, false, false
			}
			c.contentID = v
		case "--project":
			if !validHumanRequestKey(v) {
				return attemptCommand{}, false, false
			}
			c.project = v
		case "--revision":
			x, ok := parseRevision(v)
			if !ok {
				return attemptCommand{}, false, false
			}
			c.contentRevision = x
		case "--offset":
			x, ok := parseOffset(v)
			if !ok {
				return attemptCommand{}, false, false
			}
			c.offset = x
		case "--limit":
			x, ok := parseRevision(v)
			if !ok || x > api.MaxContentPageItems {
				return attemptCommand{}, false, false
			}
			c.head = x
		case "--document":
			if len(v) == 0 || len(v) > kernel.OutcomeDocumentLimit {
				return attemptCommand{}, false, false
			}
			c.document, c.bodySet = v, true
		case "--document-file":
			if len(v) == 0 || len(v) > 4096 {
				return attemptCommand{}, false, false
			}
			c.documentFile = v
		default:
			return attemptCommand{}, false, false
		}
	}
	if c.bodySet && c.documentFile != "" {
		return attemptCommand{}, false, false
	}
	if c.kind == commandOutcomeWrite {
		if c.project == "" || c.contentID == "" || (!c.bodySet && c.documentFile == "") {
			return attemptCommand{}, false, false
		}
	} else if c.project == "" || c.contentID == "" && c.kind == commandOutcomeRead {
		return attemptCommand{}, false, false
	}
	return c, false, true
}

func outcomeDocument(command attemptCommand) (kernel.OutcomeDocument, error) {
	var r io.Reader
	var file *os.File
	if command.documentFile == "-" {
		r = os.Stdin
	} else if command.documentFile != "" {
		var err error
		file, err = os.Open(command.documentFile)
		if err != nil {
			return kernel.OutcomeDocument{}, err
		}
		defer file.Close()
		r = file
	} else {
		r = strings.NewReader(command.document)
	}
	b, err := io.ReadAll(io.LimitReader(r, kernel.OutcomeDocumentLimit+1))
	if err != nil || len(b) > kernel.OutcomeDocumentLimit {
		return kernel.OutcomeDocument{}, api.ErrInvalidInput
	}
	return kernel.DecodeOutcomeDocument(string(b))
}

func runOutcome(ctx context.Context, client outcomeClient, command attemptCommand, stdout, stderr io.Writer) int {
	var value any
	var err error
	switch command.kind {
	case commandOutcomeWrite:
		var doc kernel.OutcomeDocument
		doc, err = outcomeDocument(command)
		if err == nil {
			value, err = client.OutcomeWrite(ctx, api.OutcomeWriteInput{ID: command.contentID, ProjectID: command.project, Document: doc, ExpectedRevision: command.contentRevision})
		}
	case commandOutcomeRead:
		value, err = client.OutcomeRead(ctx, api.OutcomeReadInput{ID: command.contentID, ProjectID: command.project, Revision: command.contentRevision})
	case commandOutcomeList:
		value, err = client.OutcomeList(ctx, api.OutcomeListInput{ProjectID: command.project, Offset: command.offset, Limit: command.head})
	}
	if err != nil {
		return writeWebFailure(stderr, "outcome", err)
	}
	return writeJSON(stdout, value)
}
