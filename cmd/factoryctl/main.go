package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

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
`
)

type outcome uint8

const (
	outcomeSucceed outcome = iota + 1
	outcomeBlock
	outcomeFail
)

type attemptCommand struct {
	outcome outcome
	text    string
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
		writeFailure(stderr, err)
		return exitFailure
	}

	callContext, cancel := context.WithTimeout(ctx, attemptRequestTimeout)
	defer cancel()
	var result api.MutationResult
	switch command.outcome {
	case outcomeSucceed:
		result, err = client.Succeed(callContext, command.text)
	case outcomeBlock:
		result, err = client.Block(callContext, command.text)
	case outcomeFail:
		result, err = client.Fail(callContext, command.text)
	default:
		err = api.ErrInvalidInput
	}
	if err != nil {
		writeFailure(stderr, err)
		return exitFailure
	}
	_, _ = fmt.Fprintf(stdout, "attempt outcome request accepted: head=%d revision=%d\n", result.Head, result.Revision)
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
		case "succeed", "block", "fail":
			return attemptCommand{}, true, true
		default:
			return attemptCommand{}, false, false
		}
	}

	switch args[1] {
	case "succeed":
		if len(args) == 2 {
			return attemptCommand{outcome: outcomeSucceed}, false, true
		}
		if len(args) == 4 && args[2] == "--result" {
			return attemptCommand{outcome: outcomeSucceed, text: args[3]}, false, true
		}
	case "block":
		if len(args) == 4 && args[2] == "--detail" && args[3] != "" {
			return attemptCommand{outcome: outcomeBlock, text: args[3]}, false, true
		}
	case "fail":
		if len(args) == 2 {
			return attemptCommand{outcome: outcomeFail}, false, true
		}
		if len(args) == 4 && args[2] == "--detail" {
			return attemptCommand{outcome: outcomeFail, text: args[3]}, false, true
		}
	}
	return attemptCommand{}, false, false
}

func helpFlag(value string) bool { return value == "-h" || value == "--help" }

func writeFailure(stderr io.Writer, err error) {
	message := "factoryctl: outcome request failed\n"
	var remote *api.RemoteError
	switch {
	case errors.Is(err, api.ErrInvalidClient):
		message = "factoryctl: attempt client configuration is invalid\n"
	case errors.Is(err, api.ErrInvalidInput):
		message = "factoryctl: attempt input is invalid\n"
	case errors.Is(err, context.Canceled):
		message = "factoryctl: outcome request canceled\n"
	case errors.Is(err, context.DeadlineExceeded):
		message = "factoryctl: outcome request timed out\n"
	case errors.As(err, &remote):
		message = "factoryctl: outcome request was not accepted\n"
	}
	_, _ = io.WriteString(stderr, message)
}
