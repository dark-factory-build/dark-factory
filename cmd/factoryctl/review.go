package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

func runReview(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("review", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var request api.ReviewRequest
	var project string
	flags.StringVar(&project, "project", "", "project id")
	flags.StringVar(&request.Repository, "repository", "", "published repository")
	flags.Uint64Var(&request.PullNumber, "pull", 0, "pull request number")
	flags.StringVar(&request.Head, "head", "", "exact head SHA")
	flags.StringVar(&request.Base, "base", "", "exact base SHA")
	flags.StringVar(&request.BaseRef, "base-ref", "", "base branch ref")
	flags.StringVar(&request.Body, "body", "", "pull request body")
	flags.StringVar(&request.Provider, "provider", "codex", "codex or claude")
	flags.StringVar(&request.RetryOperation, "retry-operation", "", "retry a failed daemon review operation")
	if flags.Parse(args) != nil || flags.NArg() != 0 || project == "" {
		return exitUsage
	}
	if getenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE") != "" {
		return exitFailure
	}
	client, err := api.NewOperatorClient(getenv("DARK_FACTORY_SOCKET"), getenv("DARK_FACTORY_OPERATOR_TOKEN_FILE"))
	if err != nil {
		return exitFailure
	}
	callContext, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	result, err := client.Intake(callContext, api.IntakeInput{Action: "review_pr", ProjectID: project, ReviewRequest: &request})
	if err != nil {
		return writeWebFailure(stderr, "review", err)
	}
	if result.State != "ok" {
		_, _ = fmt.Fprintf(stderr, "factoryctl: review %s\n", result.State)
		return exitFailure
	}
	_, err = fmt.Fprintln(stdout, result.ReviewOperation)
	if err != nil {
		return exitFailure
	}
	return 0
}
