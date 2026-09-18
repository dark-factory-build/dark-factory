package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/dark-factory-build/dark-factory/internal/api"
)

const githubUsage = "factoryctl github connect [--open] | confirm CODE | status | refresh | disconnect | installations [--page N] | repositories --installation ID [--page N] | manage --installation ID [--open] | delegate --repositories FILE\n"

func runGitHub(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, opener browserOpener) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, githubUsage)
		return exitUsage
	}
	input := api.GitHubConnectionInput{Action: args[0]}
	flags := flag.NewFlagSet("github", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var open bool
	var path string
	var manageInstallation int64
	switch input.Action {
	case "connect":
		flags.BoolVar(&open, "open", false, "open GitHub authorization")
	case "confirm":
		if len(args) != 2 {
			_, _ = io.WriteString(stderr, githubUsage)
			return exitUsage
		}
		input.Code = args[1]
		args = args[:1]
	case "installations":
		flags.IntVar(&input.Page, "page", 1, "GitHub result page")
	case "repositories":
		flags.IntVar(&input.Page, "page", 1, "GitHub result page")
		flags.Int64Var(&input.InstallationID, "installation", 0, "GitHub installation ID")
	case "manage":
		flags.Int64Var(&manageInstallation, "installation", 0, "GitHub installation ID")
		flags.BoolVar(&open, "open", false, "open native GitHub installation settings")
	case "delegate":
		flags.StringVar(&path, "repositories", "", "JSON array of selected installation_id, repository_id and repository")
	case "status", "refresh", "disconnect":
	default:
		_, _ = io.WriteString(stderr, githubUsage)
		return exitUsage
	}
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return exitUsage
	}
	if input.Action == "manage" {
		if manageInstallation <= 0 {
			return exitUsage
		}
		input.Action = "installations"
		input.Page = 1
	}
	if input.Action == "delegate" {
		if path == "" {
			_, _ = io.WriteString(stderr, githubUsage)
			return exitUsage
		}
		file, err := os.Open(path)
		if err != nil {
			_, _ = io.WriteString(stderr, "Cannot read repository selection file\n")
			return exitFailure
		}
		data, err := io.ReadAll(io.LimitReader(file, 32769))
		_ = file.Close()
		if err != nil || len(data) > 32768 || json.Unmarshal(data, &input.Repositories) != nil || input.Repositories == nil {
			_, _ = io.WriteString(stderr, "Repository selection must be a bounded JSON array\n")
			return exitUsage
		}
	}
	if !api.ValidGitHubConnectionInput(input) {
		_, _ = io.WriteString(stderr, githubUsage)
		return exitUsage
	}
	if getenv("DARK_FACTORY_ATTEMPT_TOKEN_FILE") != "" {
		_, _ = io.WriteString(stderr, "GitHub connection settings require an operator session, not a worker attempt\n")
		return exitFailure
	}
	client, err := api.NewOperatorClient(getenv("DARK_FACTORY_SOCKET"), getenv("DARK_FACTORY_OPERATOR_TOKEN_FILE"))
	if err != nil {
		_, _ = io.WriteString(stderr, "Set DARK_FACTORY_SOCKET and DARK_FACTORY_OPERATOR_TOKEN_FILE for your factory home\n")
		return exitFailure
	}
	requestContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if manageInstallation > 0 {
		for {
			result, err := client.GitHubConnection(requestContext, input)
			if err != nil {
				return writeWebFailure(stderr, "GitHub installation settings", err)
			}
			if result.State != "ok" || result.Installations == nil {
				_, _ = io.WriteString(stderr, "GitHub installation access unavailable; refresh your connection\n")
				return exitFailure
			}
			for _, installation := range result.Installations.Items {
				if installation.ID != manageInstallation {
					continue
				}
				link, err := url.Parse(installation.URL)
				if err != nil || link.Scheme != "https" || link.Host != "github.com" || link.User != nil || !strings.Contains(link.Path, "/settings/installations/") {
					_, _ = io.WriteString(stderr, "Native GitHub installation settings are unavailable\n")
					return exitFailure
				}
				_, _ = fmt.Fprintln(stdout, installation.URL)
				if open {
					if err := opener(ctx, installation.URL); err != nil {
						return exitFailure
					}
				}
				return 0
			}
			if result.Installations.NextPage == nil {
				_, _ = io.WriteString(stderr, "Installation not visible to your GitHub connection; check pending organization approval on GitHub\n")
				return exitFailure
			}
			next := *result.Installations.NextPage
			if next <= input.Page || next > 1000 {
				return exitFailure
			}
			input.Page = next
		}
	}
	result, err := client.GitHubConnection(requestContext, input)
	if err != nil {
		return writeWebFailure(stderr, "GitHub connection", err)
	}
	if result.State == "legacy_overseers_running" {
		_, _ = io.WriteString(stderr, "Finish or stop the existing legacy overseer, controller, or review pass before connecting GitHub, then retry.\n")
		return exitFailure
	}
	if result.State != "ok" {
		_, _ = fmt.Fprintf(stderr, "GitHub connection: %s. Check connection status; refresh access or retry when GitHub is available.\n", result.State)
		return exitFailure
	}
	if result.Authorization != nil {
		_, _ = io.WriteString(stderr, "Authorize GitHub, then enter the callback confirmation code on this factory with factoryctl github confirm CODE.\n")
		if open {
			if err := opener(ctx, result.Authorization.URL); err != nil {
				_ = writeJSON(stdout, result)
				_, _ = io.WriteString(stderr, "Could not open the browser; use the authorization URL above.\n")
				return exitFailure
			}
		}
	}
	return writeJSON(stdout, result)
}
