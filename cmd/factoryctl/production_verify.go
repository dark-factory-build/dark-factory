package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var verificationSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
var metaAttribute = regexp.MustCompile(`(?i)([a-z][a-z0-9:-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
var metaElement = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)
var rawScript = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)

type verificationResult struct {
	SHA     string            `json:"sha"`
	Healthy bool              `json:"healthy"`
	URLs    map[string]string `json:"urls,omitempty"`
	Error   string            `json:"error,omitempty"`
}

func validSHA(value string) bool { return verificationSHA.MatchString(value) }

func validVerificationURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func runProductionVerify(ctx context.Context, command attemptCommand, stdout, stderr io.Writer) int {
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	result := verificationResult{URLs: make(map[string]string, len(command.verificationURLs))}
	for _, target := range command.verificationURLs {
		observed, status, err := verifySourceURL(ctx, client, target, command.metaTag)
		if observed != "" && result.SHA == "" {
			result.SHA = observed
		}
		result.URLs[target] = status
		if err != nil {
			result.Error = err.Error()
			break
		}
		if observed != command.expectedSHA {
			result.Error = fmt.Sprintf("%s reported source commit %q", target, observed)
			break
		}
	}
	result.Healthy = result.Error == "" && result.SHA == command.expectedSHA
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return exitFailure
	}
	if !result.Healthy {
		return exitFailure
	}
	return 0
}

func verifySourceURL(ctx context.Context, client *http.Client, target, metaTag string) (string, string, error) {
	body, status, err := fetchURL(ctx, client, target)
	if err != nil {
		return "", status, err
	}
	observed := sourceCommitMeta(string(body), metaTag)
	if !validSHA(observed) {
		return observed, status, fmt.Errorf("%s has no valid %s meta tag", target, metaTag)
	}
	return observed, status, nil
}

func fetchURL(ctx context.Context, client *http.Client, target string) ([]byte, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, "request_error", err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "request_error", err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, response.Status, fmt.Errorf("%s returned %s", target, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, response.Status, err
	}
	return body, response.Status, nil
}

func sourceCommitMeta(body, name string) string {
	clean := rawScript.ReplaceAllString(htmlComment.ReplaceAllString(body, ""), "")
	for _, tag := range metaElement.FindAllString(clean, -1) {
		attributes := map[string]string{}
		for _, match := range metaAttribute.FindAllStringSubmatch(tag, -1) {
			value := match[2]
			if value == "" {
				value = match[3]
			}
			if value == "" {
				value = match[4]
			}
			attributes[strings.ToLower(match[1])] = value
		}
		if attributes["name"] == name {
			return attributes["content"]
		}
	}
	return ""
}
