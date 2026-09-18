package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dark-factory-build/dark-factory/internal/buildinfo"
)

const publicBacklogURL = "https://darkfactory.build/api/backlog"

func publicBacklogClient() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func runBacklog(ctx context.Context, args []string, stdout, stderr io.Writer, opener browserOpener) int {
	if len(args) == 1 && args[0] == "--open" {
		link := "https://darkfactory.build/backlog"
		_, _ = fmt.Fprintln(stdout, link)
		if err := opener(ctx, link); err != nil {
			_, _ = fmt.Fprintln(stderr, "Could not open the browser. Open the printed backlog link.")
			return exitFailure
		}
		return 0
	}
	if len(args) != 0 {
		_, _ = fmt.Fprintln(stderr, "usage: factoryctl backlog [--open]")
		return exitUsage
	}
	if err := readPublicBacklog(ctx, publicBacklogClient(), stdout); err != nil {
		_, _ = fmt.Fprintln(stderr, "Public backlog unavailable. Retry or visit https://github.com/dark-factory-build/dark-factory/issues")
		return exitFailure
	}
	return 0
}

func readPublicBacklog(ctx context.Context, client *http.Client, stdout io.Writer) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, publicBacklogURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	const maxBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return err
	}
	var result struct {
		Status     string            `json:"status"`
		Repository string            `json:"repository"`
		AsOf       time.Time         `json:"as_of"`
		Truncated  *bool             `json:"truncated"`
		Issues     []json.RawMessage `json:"issues"`
	}
	if response.StatusCode != http.StatusOK || len(body) > maxBytes || json.Unmarshal(body, &result) != nil || result.Status != "ok" || result.Repository != "dark-factory-build/dark-factory" || result.AsOf.IsZero() || result.Truncated == nil || result.Issues == nil {
		return fmt.Errorf("invalid public backlog response")
	}
	// Re-encode to keep issue-controlled terminal escape sequences inert.
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(value)
}

// Community links use only explicit report fields and the linked build receipt.
// They work without a daemon and never read the factory's private settings.
func runFeedback(ctx context.Context, args []string, stdout, stderr io.Writer, opener browserOpener) int {
	if len(args) == 0 || args[0] != "bug" && args[0] != "feature" {
		_, _ = fmt.Fprintln(stderr, "usage: factoryctl feedback bug|feature [--open] [--agent-assisted] [--factory-name TEXT]")
		return exitUsage
	}
	flags := flag.NewFlagSet("feedback", flag.ContinueOnError)
	flags.SetOutput(stderr)
	open := flags.Bool("open", false, "open the report preparation page")
	assisted := flags.Bool("agent-assisted", false, "include self-reported agent assistance")
	name := flags.String("factory-name", "", "optional public, self-reported factory name")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return exitUsage
	}
	if !utf8.ValidString(*name) || len(*name) > 128 || strings.IndexFunc(*name, unicode.IsControl) >= 0 {
		_, _ = fmt.Fprintln(stderr, "factory name must be at most 128 UTF-8 bytes without control characters")
		return exitUsage
	}
	build := buildinfo.Current()
	query := url.Values{"kind": {args[0]}, "version": {build.Version()}, "source": {build.Source()}, "target": {build.Target()}}
	if *assisted {
		query.Set("agent_assisted", "true")
	}
	if *name != "" {
		query.Set("factory_name", *name)
	}
	link := "https://darkfactory.build/feedback?" + query.Encode()
	if _, err := fmt.Fprintln(stdout, link); err != nil {
		return exitFailure
	}
	if *open {
		if err := opener(ctx, link); err != nil {
			_, _ = fmt.Fprintln(stderr, "Could not open the browser. Open the printed link to prepare your report.")
			return exitFailure
		}
	}
	_, _ = fmt.Fprintln(stderr, "Review your report and submit it on GitHub. No issue has been submitted.")
	return 0
}
