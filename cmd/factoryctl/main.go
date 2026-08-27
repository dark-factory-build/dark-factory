package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

const (
	attemptRequestTimeout = 5 * time.Second
	exitUsage             = 64
	exitFailure           = 1

	usage = `usage:
  factoryctl attempt succeed [--result TEXT]
  factoryctl attempt block --detail TEXT
  factoryctl attempt fail [--detail TEXT]
  factoryctl attempt request-human --idempotency-key HEX32 --question TEXT
`
)

type commandKind uint8

const (
	commandSucceed commandKind = iota + 1
	commandBlock
	commandFail
	commandRequestHuman
)

type attemptCommand struct {
	kind           commandKind
	idempotencyKey string
	text           string
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	command, help, ok := parse(args)
	if help {
		_, _ = io.WriteString(stdout, usage)
		return 0
	}
	if !ok {
		_, _ = io.WriteString(stderr, usage)
		return exitUsage
	}

	socket := getenv("DARK_FACTORY_SOCKET")
	if socket == "" {
		_, _ = io.WriteString(stderr, "factoryctl: attempt client configuration is invalid\n")
		return exitFailure
	}
	client, err := api.NewAttemptClientFromEnvironment(socket)
	if err != nil {
		writeFailure(stderr, command.kind, err)
		return exitFailure
	}

	callContext, cancel := context.WithTimeout(ctx, attemptRequestTimeout)
	defer cancel()
	var result api.MutationResult
	switch command.kind {
	case commandSucceed:
		result, err = client.Succeed(callContext, command.text)
	case commandBlock:
		result, err = client.Block(callContext, command.text)
	case commandFail:
		result, err = client.Fail(callContext, command.text)
	case commandRequestHuman:
		result, err = client.RequestHuman(callContext, api.HumanQuestionInput{IdempotencyKey: command.idempotencyKey, Question: command.text})
	default:
		err = api.ErrInvalidInput
	}
	if err != nil {
		writeFailure(stderr, command.kind, err)
		return exitFailure
	}
	if command.kind == commandRequestHuman {
		_, _ = fmt.Fprintf(stdout, "human request accepted: head=%d revision=%d\n", result.Head, result.Revision)
	} else {
		_, _ = fmt.Fprintf(stdout, "attempt outcome request accepted: head=%d revision=%d\n", result.Head, result.Revision)
	}
	return 0
}

func parse(args []string) (attemptCommand, bool, bool) {
	if len(args) == 1 && helpFlag(args[0]) {
		return attemptCommand{}, true, true
	}
	if len(args) < 2 || args[0] != "attempt" {
		return attemptCommand{}, false, false
	}
	if len(args) == 2 && helpFlag(args[1]) {
		return attemptCommand{}, true, true
	}
	if len(args) == 3 && helpFlag(args[2]) {
		switch args[1] {
		case "succeed", "block", "fail", "request-human":
			return attemptCommand{}, true, true
		default:
			return attemptCommand{}, false, false
		}
	}

	switch args[1] {
	case "succeed":
		if len(args) == 2 {
			return attemptCommand{kind: commandSucceed}, false, true
		}
		if len(args) == 4 && args[2] == "--result" {
			return attemptCommand{kind: commandSucceed, text: args[3]}, false, true
		}
	case "block":
		if len(args) == 4 && args[2] == "--detail" && args[3] != "" {
			return attemptCommand{kind: commandBlock, text: args[3]}, false, true
		}
	case "fail":
		if len(args) == 2 {
			return attemptCommand{kind: commandFail}, false, true
		}
		if len(args) == 4 && args[2] == "--detail" {
			return attemptCommand{kind: commandFail, text: args[3]}, false, true
		}
	case "request-human":
		if len(args) == 6 && args[2] == "--idempotency-key" && validHumanRequestKey(args[3]) && args[4] == "--question" && validQuestion(args[5]) {
			return attemptCommand{kind: commandRequestHuman, idempotencyKey: args[3], text: args[5]}, false, true
		}
	}
	return attemptCommand{}, false, false
}

func helpFlag(value string) bool { return value == "-h" || value == "--help" }

func validHumanRequestKey(value string) bool {
	if len(value) != 32 {
		return false
	}
	nonzero := false
	for _, value := range []byte(value) {
		if value < '0' || value > '9' {
			if value < 'a' || value > 'f' {
				return false
			}
		}
		nonzero = nonzero || value != '0'
	}
	return nonzero
}

func validQuestion(value string) bool {
	return len(value) >= 1 && len(value) <= 8192 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func writeFailure(stderr io.Writer, kind commandKind, err error) {
	subject, input := "outcome request", "attempt input"
	if kind == commandRequestHuman {
		subject, input = "human request", "human request input"
	}
	message := "factoryctl: " + subject + " failed\n"
	var remote *api.RemoteError
	switch {
	case errors.Is(err, api.ErrInvalidClient):
		message = "factoryctl: attempt client configuration is invalid\n"
	case errors.Is(err, api.ErrInvalidInput):
		message = "factoryctl: " + input + " is invalid\n"
	case errors.Is(err, context.Canceled):
		message = "factoryctl: " + subject + " canceled\n"
	case errors.Is(err, context.DeadlineExceeded):
		message = "factoryctl: " + subject + " timed out\n"
	case errors.As(err, &remote):
		message = "factoryctl: " + subject + " was not accepted\n"
	}
	_, _ = io.WriteString(stderr, message)
}
