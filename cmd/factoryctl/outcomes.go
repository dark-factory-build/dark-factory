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
		count, isCount := parseCount(v, true)
		switch {
		case n == "--id" && validHumanRequestKey(v):
			c.contentID = v
		case n == "--project" && validHumanRequestKey(v):
			c.project = v
		case n == "--revision" && isCount && count > 0:
			c.contentRevision = count
		case n == "--offset" && isCount:
			c.offset = count
		case n == "--limit" && isCount && count > 0 && count <= api.MaxContentPageItems:
			c.head = count
		case n == "--document" && len(v) > 0 && len(v) <= kernel.OutcomeDocumentLimit:
			c.document, c.bodySet = v, true
		case n == "--document-file" && len(v) > 0 && len(v) <= 4096:
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
