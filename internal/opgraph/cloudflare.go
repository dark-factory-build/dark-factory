package opgraph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CloudflareEndpoint is the Cloudflare GraphQL Analytics API.
var CloudflareEndpoint = "https://api.cloudflare.com/client/v4/graphql"

// PullCloudflare reads Workers invocation analytics for one account over a
// window. The platform counts every invocation, so each named script is
// covered whether or not it saw traffic; its granularity is the script, so
// nothing inside a Worker is ever claimed quiet from this source.
func PullCloudflare(ctx context.Context, client *http.Client, account, token string, scripts []string, environment string, now time.Time, window time.Duration) ([]Observation, []Coverage, error) {
	query := `query($account: string!, $from: Time!, $to: Time!) { viewer { accounts(filter: {accountTag: $account}) {
  workersInvocationsAdaptive(limit: 1000, filter: {datetime_geq: $from, datetime_leq: $to}) {
    sum { requests errors } quantiles { wallTimeP99 } dimensions { scriptName }
  } } } }`
	body, _ := json.Marshal(map[string]any{"query": query, "variables": map[string]string{
		"account": account, "from": now.Add(-window).UTC().Format(time.RFC3339), "to": now.UTC().Format(time.RFC3339)}})
	raw, status, err := cloudflare(ctx, client, http.MethodPost, CloudflareEndpoint, token, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	if status != http.StatusOK {
		return nil, nil, fmt.Errorf("cloudflare analytics: HTTP %d", status)
	}
	var result struct {
		Data struct {
			Viewer struct {
				Accounts []struct {
					Invocations []struct {
						Sum struct {
							Requests uint64 `json:"requests"`
							Errors   uint64 `json:"errors"`
						} `json:"sum"`
						Quantiles struct {
							WallTimeP99 float64 `json:"wallTimeP99"`
						} `json:"quantiles"`
						Dimensions struct {
							ScriptName string `json:"scriptName"`
						} `json:"dimensions"`
					} `json:"workersInvocationsAdaptive"`
				} `json:"accounts"`
			} `json:"viewer"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, nil, err
	}
	if len(result.Errors) > 0 {
		return nil, nil, errors.New("cloudflare analytics: " + result.Errors[0].Message)
	}
	end := now.UnixMilli()
	var observations []Observation
	for _, account := range result.Data.Viewer.Accounts {
		for _, row := range account.Invocations {
			if !contains(scripts, row.Dimensions.ScriptName) {
				continue
			}
			observations = append(observations, Observation{Source: "cloudflare", Environment: environment, Kind: "server",
				Start: end - window.Milliseconds(), End: end, Attributes: map[string]string{"service.name": row.Dimensions.ScriptName},
				Count: row.Sum.Requests, Errors: row.Sum.Errors, LatencyP95: row.Quantiles.WallTimeP99 / 1000})
		}
	}
	var coverage []Coverage
	for _, script := range scripts {
		coverage = append(coverage, Coverage{Source: "cloudflare", Environment: environment, Unit: script, Keys: []string{"service.name"}, AsOf: end, TTL: 2 * window.Milliseconds()})
	}
	return observations, coverage, nil
}

// CloudflareDomains reads the custom domains each mapped Worker serves, by
// the unit's service.name. A token without Workers Scripts Read is refused
// with 403: the domains are not visible, which is not an error.
func CloudflareDomains(ctx context.Context, client *http.Client, account, token string, services map[string]string) (map[string][]string, error) {
	raw, status, err := cloudflare(ctx, client, http.MethodGet, strings.TrimSuffix(CloudflareEndpoint, "/graphql")+"/accounts/"+url.PathEscape(account)+"/workers/domains", token, nil)
	if err != nil || status == http.StatusForbidden {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("cloudflare domains: HTTP %d", status)
	}
	var result struct {
		Result []struct {
			Hostname string `json:"hostname"`
			Service  string `json:"service"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	hosts := map[string][]string{}
	for _, domain := range result.Result {
		if unit := services[domain.Service]; unit != "" && domain.Hostname != "" {
			hosts[unit] = append(hosts[unit], strings.ToLower(domain.Hostname))
		}
	}
	return hosts, nil
}

func cloudflare(ctx context.Context, client *http.Client, method, address, token string, body io.Reader) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	return raw, response.StatusCode, err
}
